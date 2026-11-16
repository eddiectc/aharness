package tui

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eddiectc/aharness/internal/agent"
	"github.com/eddiectc/aharness/internal/model"
)

type blockingHost struct {
	info Info
	mu   sync.Mutex
	run  *blockingRun
}

type blockingRun struct {
	done      chan agent.Result
	terminate chan struct{}
	termOnce  sync.Once
}

func (r *blockingRun) Interrupt() {}
func (r *blockingRun) Terminate() {
	r.termOnce.Do(func() { close(r.terminate) })
}
func (r *blockingRun) Done() <-chan agent.Result { return r.done }

func (h *blockingHost) Info() Info                            { return h.info }
func (h *blockingHost) Models() []ModelChoice                 { return nil }
func (h *blockingHost) History() ([]agent.HistoryItem, error) { return nil, nil }
func (h *blockingHost) Close() error                          { return nil }

func (h *blockingHost) Start(sub Submission, opts RunOptions, steer <-chan string, display func(agent.DisplayEvent) error) (Run, error) {
	run := &blockingRun{done: make(chan agent.Result, 1), terminate: make(chan struct{})}
	h.mu.Lock()
	h.run = run
	h.mu.Unlock()
	go func() {
		defer close(run.done)
		if display != nil {
			_ = display(agent.RunStarted{Model: "m", Dialect: "openai-chat"})
			_ = display(agent.ModelStarted{Turn: 1, Attempt: 1})
		}
		status := agent.StopCompleted
		select {
		case <-run.terminate:
			status = agent.StopInterrupted
		case <-time.After(10 * time.Second):
			status = agent.StopTimeLimit
		}
		result := agent.Result{Status: status, RunTurns: 1, CumulativeTurns: 1}
		if display != nil {
			_ = display(agent.RunFinished{Result: result, Reason: string(status)})
		}
		run.done <- result
	}()
	return run, nil
}

func TestHeadlessWaitTimeoutLeavesRunActive(t *testing.T) {
	host := &blockingHost{info: Info{
		SessionID: "0190a0c4-0000-7000-8000-0000000000aa", TracePath: "/tmp/t",
		Model: "m", Dialect: string(model.DialectChat), Workspace: "/tmp/w",
		MaxTurns: 30, MaxTotalTokens: 2_000_000,
	}}
	script := strings.Join([]string{
		`{"v":1,"type":"text","text":"start"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":50}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	exit, err := RunHeadless(HeadlessOptions{
		Host: host, Model: "m", Dialect: model.DialectChat,
		Width: 60, Height: 16, Stdin: strings.NewReader(script), Stdout: &out,
		ImageProtocol: "glyph",
	})
	if err != nil {
		t.Fatalf("RunHeadless: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
	lines := parseOutput(t, out.String())
	var timeout, runningFrame bool
	for _, line := range lines {
		if line["type"] == "error" && line["code"] == "timeout" {
			timeout = true
		}
		if line["type"] == "frame" && (line["state"] == "running" || line["state"] == "streaming") {
			runningFrame = true
		}
	}
	if !timeout {
		t.Fatalf("missing timeout error:\n%s", out.String())
	}
	if !runningFrame {
		t.Fatalf("no frame showed the run active:\n%s", out.String())
	}
}
