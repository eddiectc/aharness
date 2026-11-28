package agent

import (
	"fmt"
	"time"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/tools"
)

// finishedWithoutStart emits the outcome event for a task that failed before
// its child run started. It carries the resolved agent, an empty session
// unless the report names one, zero counters, the failed status, and the
// reason. It never emits a SubagentStarted event.
func (s *Spawner) finishedWithoutStart(call tools.Call, index int, spec tools.SubagentSpec, report tools.SubagentReport) {
	agentName := report.Agent
	if agentName == "" || agentName == "?" {
		agentName = spec.Agent
	}
	d := s.newChildDisplay(call, index, agentName, report.Session, report.Model, report.Effort, nil, model.Pricing{})
	d.intent = report.Intent
	if d.intent == "" {
		d.intent = subagentIntent(spec.Task)
	}
	d.finished(report)
}

// subagentReportError returns the failure text for a report that did not
// complete or stop at its own limit. A partial report from a limit or a
// mid-run interrupt is output, not an error.
func subagentReportError(report tools.SubagentReport) string {
	switch report.Status {
	case tools.SubagentCompleted, tools.SubagentMaxTurns, tools.SubagentTokenLimit, tools.SubagentTimeLimit:
		return ""
	case tools.SubagentInterrupted:
		if report.Turns == 0 {
			return report.Text
		}
		return ""
	default:
		return report.Text
	}
}

// childDisplay is the display adapter for one child run. It forwards only
// counters and names to the parent display: child text, arguments, and tool
// output stay in the child's own transcript unless the host registered an
// observation sink for this child, which receives the child's own events.
type childDisplay struct {
	spawner       *Spawner
	call          tools.Call
	index         int
	agent         string
	intent        string
	session       string
	model         string
	effort        string
	tools         []string
	usage         model.Usage
	pricing       model.Pricing
	cost          int64
	lastEmit      time.Time
	announced     bool
	startedAt     time.Time
	childTurns    int
	contextTokens int
	lastTool      string
	finishedSent  bool
	// sink is this child's observation sink, nil when unobserved. Only the
	// child's runner goroutine calls it.
	sink ChildSink
	// sinkErr is the first failure observed from this child's sink, if any.
	sinkErr error
}

func (s *Spawner) newChildDisplay(call tools.Call, index int, agent, session, modelID, effort string, grant []string, pricing model.Pricing) *childDisplay {
	return &childDisplay{
		spawner: s,
		call:    call,
		index:   index,
		agent:   agent,
		session: session,
		model:   modelID,
		effort:  effort,
		tools:   append([]string(nil), grant...),
		pricing: pricing,
	}
}

// started emits SubagentStarted with the resolved parent-side fields.
func (d *childDisplay) started() error {
	d.announced = true
	d.startedAt = d.spawner.opts.Now()
	return d.emit(SubagentStarted{
		Turn:    d.call.Turn,
		CallID:  d.call.ID,
		Index:   d.index,
		Agent:   d.agent,
		Intent:  d.intent,
		Session: d.session,
		Model:   d.model,
		Effort:  d.effort,
		Tools:   d.tools,
	})
}

// withIntent sets the derived roster intent before the start event.
func (d *childDisplay) withIntent(task string) *childDisplay {
	d.intent = subagentIntent(task)
	return d
}

// observe registers this child's observation sink. It is called once per child
// that actually starts a run, after the intent and before the start event, so a
// host never receives a registration with no session behind it. A returned
// error, or a panic in the host hook, terminates that child as internal_error.
// The error is not marked errDisplay: it never passes through the runner, which
// would otherwise double the marker on the report text.
func (d *childDisplay) observe() (err error) {
	observe := d.spawner.opts.Observe
	if observe == nil {
		return nil
	}
	defer func() {
		if v := recover(); v != nil {
			d.sink = nil
			err = fmt.Errorf("child observation hook panicked: %v", v)
		}
	}()
	d.sink = observe(ChildObservation{
		CallID:  d.call.ID,
		Index:   d.index,
		Agent:   d.agent,
		Session: d.session,
		Intent:  d.intent,
		Model:   d.model,
		Effort:  d.effort,
		Tools:   append([]string(nil), d.tools...),
	})
	return nil
}

// forward delivers one event to this child's observation sink. It runs on the
// child's own goroutine before the parent display, so per-child order is the
// runner's order and no lock is needed; sinks for different children may run
// concurrently. A returned error or panic is a display failure for this child
// only. The returned error is not marked errDisplay: the runner's emit adds that
// marker exactly once.
func (d *childDisplay) forward(ev DisplayEvent) (err error) {
	if d.sink == nil {
		return nil
	}
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("child observation sink panicked: %v", v)
			d.recordSinkError(err)
		}
	}()
	if ferr := d.sink(ev); ferr != nil {
		d.recordSinkError(ferr)
		return ferr
	}
	return nil
}

// recordSinkError keeps the first sink failure for the child's report. The
// terminating run repeats the same cause on its later display attempts, so only
// the first one names the original failure.
func (d *childDisplay) recordSinkError(err error) {
	if d.sinkErr == nil {
		d.sinkErr = err
	}
}

// display is the child runner's display callback. It forwards the event to the
// child's own sink, then accumulates counters and emits at most one progress
// event per second per child to the parent display.
func (d *childDisplay) display(ev DisplayEvent) error {
	if err := d.forward(ev); err != nil {
		return err
	}
	switch ev := ev.(type) {
	case ResponseCompleted:
		d.childTurns++
		if total := ev.Usage.Total(); total > 0 {
			d.contextTokens = total
		}
		d.usage = d.usage.Add(ev.Usage)
		if cost, priced := d.pricing.Cost(ev.Usage); priced {
			d.cost += cost
		}
		now := d.spawner.opts.Now()
		if now.Sub(d.lastEmit) < time.Second {
			return nil
		}
		d.lastEmit = now
		return d.emitProgress()
	case ToolStarted:
		d.lastTool = ev.Call.Name
		return nil
	case MessageRecorded:
		return d.emit(SubagentMessageRecorded{
			Turn:    ev.Turn,
			CallID:  d.call.ID,
			Index:   d.index,
			Agent:   d.agent,
			Session: d.session,
			ID:      ev.ID,
			Source:  ev.Source,
			Text:    ev.Text,
		})
	}
	return nil
}

func (d *childDisplay) emitProgress() error {
	return d.emit(SubagentProgress{
		Turn:          d.call.Turn,
		CallID:        d.call.ID,
		Index:         d.index,
		Agent:         d.agent,
		Session:       d.session,
		ChildTurns:    d.childTurns,
		ContextTokens: d.contextTokens,
		Usage:         d.usage,
		CostMicroUSD:  d.cost,
		CostComplete:  d.priced(),
		LastTool:      d.lastTool,
	})
}

// priced reports whether every observed child response had a price. The
// pricing snapshot itself is unavailable when the model has no catalog prices.
func (d *childDisplay) priced() bool {
	return d.pricing.Available
}

// finished emits SubagentFinished and folds the child's spend into the run's
// child total used for the token budget. It is idempotent and emits once even
// when the child never started, so a pre-start failure still has an outcome.
func (d *childDisplay) finished(report tools.SubagentReport) {
	d.spawner.addChildUsage(report.Usage)
	if d.finishedSent {
		return
	}
	d.finishedSent = true
	duration := time.Duration(0)
	if d.announced {
		duration = d.spawner.opts.Now().Sub(d.startedAt)
	}
	sessionID := d.session
	if !d.announced {
		sessionID = report.Session
	}
	_ = d.emit(SubagentFinished{
		Turn:          d.call.Turn,
		CallID:        d.call.ID,
		Index:         d.index,
		Agent:         d.agent,
		Session:       sessionID,
		Status:        report.Status,
		ChildTurns:    report.Turns,
		ContextTokens: report.ContextTokens,
		Usage:         report.Usage,
		CostMicroUSD:  report.CostMicroUSD,
		CostComplete:  report.CostComplete,
		Duration:      duration,
		Error:         subagentReportError(report),
	})
}

// emit forwards one event through the parent display, serialized because a
// future milestone runs children on separate goroutines.
func (d *childDisplay) emit(ev DisplayEvent) error {
	d.spawner.displayMu.Lock()
	defer d.spawner.displayMu.Unlock()
	if d.spawner.opts.Display == nil {
		return nil
	}
	return d.spawner.opts.Display(ev)
}
