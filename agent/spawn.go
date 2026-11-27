package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

// ModelResolution is the catalog-derived adapter and budget set a child run
// needs for one model.
type ModelResolution struct {
	Dialect string
	BaseURL string
	Limits  Limits
	Pricing model.Pricing
	// ImageInput is the resolved model's image-input capability. Nil means
	// unknown, and the child keeps the capability inherited from the parent.
	ImageInput *bool
}

// ModelResolver resolves one agent model and optional effort through the model
// catalog. It is supplied by cmd, which owns the catalog. An unknown or
// unusable model, or an effort the model does not support, is an error; the
// spawner reports it per task instead of ending the parent run.
type ModelResolver func(modelID, effort string) (ModelResolution, error)

// CompleterFunc builds the provider adapter for a child run. cmd supplies it so
// the spawner never needs the provider token.
type CompleterFunc func(dialect model.Dialect, baseURL, sessionID string) model.Completer

// ChildCompleterFunc builds a child adapter from the resolved definition and
// model resolution, so a public host can construct the right ModelConfig. It
// takes precedence over CompleterFunc.
type ChildCompleterFunc func(def AgentDefinition, resolution ModelResolution, sessionID string) model.Completer

// ToolOptionsFunc builds the child registry options from the parent's
// effective tool configuration. The spawner clears the spawn interface on the
// result, so a child can never delegate.
type ToolOptionsFunc func(sessionID, workDir, tmpDir string) tools.Options

// RegistryFunc builds a fully configured child registry, including any custom
// tools, for the given per-child work directories. The spawner applies the
// child grant after the factory returns. A nil factory uses tools.NewRegistry.
// A factory must not install a spawn interface: delegation depth is one.
type RegistryFunc func(ws *tools.Workspace, sessionID, workDir, tmpDir string) (*tools.Registry, error)

// ChildObservation identifies one delegated child to an observation sink. It
// is resolved parent-side before the child's first event, so the fields match
// the child's own SubagentStarted event and the child's recorded session.
type ChildObservation struct {
	CallID  string
	Index   int
	Agent   string
	Session string
	Intent  string
	Model   string
	Effort  string
	Tools   []string
}

// ChildSink receives one delegated child's display events in that child's
// order, with the same sanitization the parent display receives. It is never
// entered concurrently with itself; sinks for different children, and a child
// sink versus the parent display, may run concurrently. It is called on the
// child's own goroutine, so it must not block, call the runner, or write the
// session log.
type ChildSink func(DisplayEvent) error

// SpawnerOptions configures one per-run spawner. The spawner is created with
// the host's effective configuration and reused for every delegate call in the
// run, so its child counters are per-run. A read-only fan-out calls Resolve,
// NewCompleter, NewToolOptions, ParentSeq, Now, and Observe from several child
// goroutines, so each must be safe for concurrent use.
type SpawnerOptions struct {
	Config            Config
	Workspace         *tools.Workspace
	Definitions       *AgentCatalog
	Resolve           ModelResolver
	NewCompleter      CompleterFunc
	NewChildCompleter ChildCompleterFunc
	NewToolOptions    ToolOptionsFunc
	NewRegistry       RegistryFunc
	Logger            *slog.Logger
	// Display is the parent's display callback. Child progress is forwarded
	// through it, serialized by the spawner.
	Display func(DisplayEvent) error
	// Observe, when non-nil, registers one live display sink per delegated
	// child. It is called once per child, after the definition is resolved and
	// before the child's first event, and never for a task that fails before
	// its run starts. The returned sink may be nil, which observes nothing.
	// Observe runs from child goroutines during read-only fan-out, so it must be
	// safe for concurrent use. Registering an observer makes every delegated
	// child stream, which turns a post-delta transport failure terminal instead
	// of retried. A returned error or recovered panic terminates that child as
	// internal_error; siblings and the parent are unaffected.
	Observe func(ChildObservation) ChildSink
	// ParentSession is the delegating session's ID. A continuation must record
	// it as its parent_session.
	ParentSession string
	// ParentSeq returns the parent's current last sequence number at spawn
	// time; used for the child's parent_seq.
	ParentSeq func() uint64
	// MaxSubagents bounds fresh children per run; 0 means unlimited.
	MaxSubagents int
	// Now is the clock used to throttle progress events; nil uses time.Now.
	Now func() time.Time
}

// Spawner runs delegated tasks as child sessions. A call whose every task
// holds a read-only or concurrency-safe grant executes its tasks concurrently
// under maxConcurrentChildren; any other call executes in array order. The
// parent run is suspended for the whole call either way.
type Spawner struct {
	opts SpawnerOptions

	displayMu sync.Mutex
	mu        sync.Mutex
	spawned   int
	childUse  model.Usage
	// steering routes composer lines to a running child by session ID. A child
	// entry exists only while its run is in flight.
	steering map[string]chan SteeringLine
}

// ErrSubagentNotRunning reports steering addressed to a child that is not in
// flight, for example after it finished or before it started.
var ErrSubagentNotRunning = errors.New("the child is not running")

// ErrSubagentSteeringFull reports a child steering channel at capacity.
var ErrSubagentSteeringFull = errors.New("the child steering queue is full")

// NewSpawner builds the per-run spawner. A nil definition set yields a spawner
// that reports every task as failed with the available names.
func NewSpawner(opts SpawnerOptions) *Spawner {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Spawner{opts: opts, steering: make(map[string]chan SteeringLine)}
}

// Agents returns the discovered catalog the delegate tool validates against.
func (s *Spawner) Agents() []tools.AgentInfo {
	return s.opts.Definitions.SpawnInfos()
}

// SteerChild routes one composer line to a running child. It never blocks: a
// full queue is an error, and a missing child means the run already finished.
func (s *Spawner) SteerChild(sessionID string, line SteeringLine) error {
	text := cleanText(line.Text)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	line.Text = text
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.steering[sessionID]
	if ch == nil {
		return ErrSubagentNotRunning
	}
	select {
	case ch <- line:
		return nil
	default:
		return ErrSubagentSteeringFull
	}
}

// ChildHistory returns the display transcript of one delegated child. It reads
// the child log without taking the session lock, so it works while the child
// run still holds it; a torn final line is tolerated by the reader. The session
// must be a delegated child of this spawner's parent session.
func (s *Spawner) ChildHistory(sessionID string) ([]HistoryItem, error) {
	id, err := session.CanonicalID(sessionID)
	if err != nil {
		return nil, fmt.Errorf("child session %q: %w", sessionID, err)
	}
	events, err := session.ReadSnapshot(session.SessionPath(s.opts.Config.StateDir, id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && s.childRegistered(id) {
			// SubagentStarted is emitted before the child store is created, so
			// an immediate focus can beat the first append.
			return nil, nil
		}
		return nil, err
	}
	if len(events) == 0 {
		if s.childRegistered(id) {
			return nil, nil
		}
		return nil, fmt.Errorf("session %s has no session_start", id)
	}
	if events[0].Type != session.TypeSessionStart {
		return nil, fmt.Errorf("session %s has no session_start", id)
	}
	var start sessionStartData
	if err := decodeEventData(events[0], &start); err != nil {
		return nil, fmt.Errorf("session %s has an unreadable session_start: %w", id, err)
	}
	if start.ParentSession != s.opts.ParentSession || start.ParentCallID == "" {
		return nil, fmt.Errorf("session %s is not a child of this session", id)
	}
	return History(events)
}

// registerSteering creates the channel a child consumes at its turn
// boundaries.
func (s *Spawner) registerSteering(sessionID string) chan SteeringLine {
	ch := make(chan SteeringLine, 64)
	s.mu.Lock()
	if s.steering == nil {
		s.steering = make(map[string]chan SteeringLine)
	}
	s.steering[sessionID] = ch
	s.mu.Unlock()
	return ch
}

func (s *Spawner) unregisterSteering(sessionID string) {
	s.mu.Lock()
	delete(s.steering, sessionID)
	s.mu.Unlock()
}

// childRegistered reports whether the child run is in flight.
func (s *Spawner) childRegistered(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.steering[sessionID] != nil
}
