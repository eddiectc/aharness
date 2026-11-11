package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"aharness/internal/agent"
	"aharness/internal/model"
	"aharness/internal/session"
	"aharness/internal/tools"
)

const (
	providerName = "opencode-go"

	// Run budgets default to unlimited. A zero value disables the
	// corresponding check; each flag accepts an explicit positive budget.
	defaultMaxTurns       = 0
	defaultMaxTotalTokens = 0
	defaultSessionTimeout = time.Duration(0)
	defaultModelTimeout   = time.Duration(0)

	defaultUISizeWidth  = 100
	defaultUISizeHeight = 30
	minUISizeWidth      = 20
	minUISizeHeight     = 5
	maxUISizeWidth      = 500
	maxUISizeHeight     = 200
)

// Mode selects the front end for one process invocation.
type Mode int

const (
	// ModeTUI is the default interactive terminal UI.
	ModeTUI Mode = iota
	// ModeBatch is the one-shot CLI.
	ModeBatch
	// ModeUIJSON is the headless JSONL TUI driver.
	ModeUIJSON
)

// config is the effective command-line and environment configuration. Session
// mode fields are resolved before session-dependent values are inherited.
type config struct {
	Task       string
	TaskSource string
	TaskSet    bool

	Workspace string
	StateDir  string
	Model     string
	Provider  string
	Dialect   model.Dialect
	BaseURL   string
	Token     string

	MaxTurns         int
	MaxTotalTokens   int
	MaxContextTokens int
	MaxOutputTokens  int
	// ThinkingEffort is the effective effort after clearing "default". Empty
	// means the provider/model default.
	ThinkingEffort string
	// InputLimit is the catalog's optional input ceiling for the selected
	// model. Zero means the context window minus the safety margin applies.
	InputLimit     int
	Limits         agent.Limits
	SessionTimeout time.Duration
	ModelTimeout   time.Duration
	LogRequests    bool
	RequireUsage   bool
	// Pricing is the effective model's catalog price snapshot. It is
	// observational and may be unavailable when no catalog was loaded.
	Pricing model.Pricing

	ResumeID string
	ForkID   string

	Mode         Mode
	Batch        bool
	UIJSON       bool
	UISizeWidth  int
	UISizeHeight int

	Stream    bool
	StreamSet bool

	ShowVersion    bool
	ListModels     bool
	RefreshModels  bool
	ModelsDevURL   string
	HarnessVersion string
	PromptVersion  string
	ToolsetVersion string

	modelSet          bool
	providerSet       bool
	workspaceSet      bool
	baseURLSet        bool
	maxTurnsSet       bool
	maxTotalTokensSet bool
	maxContextSet     bool
	maxContextValue   int
	maxOutputSet      bool
	sessionTimeoutSet bool
	modelTimeoutSet   bool
	thinkingEffortSet bool
}

// envSettings is the environment layer of the flag defaults. It is parsed
// before flags so defaults are env-aware and env errors remain config errors.
type envSettings struct {
	model          string
	provider       string
	workspace      string
	stateDir       string
	token          string
	baseURL        string
	modelsDevURL   string
	thinkingEffort string
	maxContext     int
	maxContextSet  bool
	logRequests    bool
}

func loadEnv(getenv func(string) string) (envSettings, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	env := envSettings{
		model:          getenv("AHARNESS_MODEL"),
		provider:       getenv("AHARNESS_PROVIDER"),
		workspace:      getenv("AHARNESS_WORKSPACE"),
		stateDir:       getenv("AHARNESS_STATE_DIR"),
		token:          getenv("AHARNESS_PROVIDER_TOKEN"),
		baseURL:        getenv("AHARNESS_PROVIDER_BASE_URL"),
		modelsDevURL:   getenv("AHARNESS_MODELS_DEV_URL"),
		thinkingEffort: getenv("AHARNESS_THINKING_EFFORT"),
	}
	if raw := getenv("AHARNESS_MAX_CONTEXT_TOKENS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return envSettings{}, fmt.Errorf("parse AHARNESS_MAX_CONTEXT_TOKENS %q: %w", raw, err)
		}
		env.maxContext = n
		env.maxContextSet = true
	}
	if raw := getenv("AHARNESS_LOG_REQUESTS"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return envSettings{}, fmt.Errorf("parse AHARNESS_LOG_REQUESTS %q: %w", raw, err)
		}
		env.logRequests = b
	}
	return env, nil
}

// parseConfig resolves defaults, environment, and flags in that order and
// validates what can be validated before a session log is opened. Resume and
// fork defer model/workspace/limit inheritance to applySessionDefaults.
func parseConfig(args []string, getenv func(string) string, out io.Writer) (config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if out == nil {
		out = io.Discard
	}
	if versionRequested(args) {
		return config{ShowVersion: true}, nil
	}

	env, err := loadEnv(getenv)
	if err != nil {
		return config{}, err
	}

	fs := flag.NewFlagSet("aharness", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var (
		task             string
		taskFile         string
		workspace        string
		stateDir         string
		modelName        string
		providerNameFlag string
		listModels       bool
		refreshModels    bool
		resumeID         string
		forkID           string
		batch            bool
		uiJSON           bool
		uiSize           string
		stream           bool
		maxTurns         int
		maxTotalTokens   int
		maxContextTokens int
		maxOutputTokens  int
		thinkingEffort   string
		sessionTimeout   time.Duration
		modelTimeout     time.Duration
		providerBaseURL  string
		requireUsage     bool
		showVersion      bool
	)
	fs.StringVar(&task, "task", "", "task text; seeds the first message in TUI modes")
	fs.StringVar(&taskFile, "task-file", "", "file containing the task; seeds the first message in TUI modes")
	fs.StringVar(&workspace, "workspace", env.workspace, "workspace directory (default: AHARNESS_WORKSPACE, then the current directory; inherited on resume/fork)")
	fs.StringVar(&stateDir, "state-dir", env.stateDir, "state directory (default: AHARNESS_STATE_DIR, then XDG_STATE_HOME/aharness, then ~/.local/state/aharness)")
	fs.StringVar(&modelName, "model", env.model, "model ID from the provider catalog (required for a new session)")
	providerDefault := env.provider
	if providerDefault == "" {
		providerDefault = providerName
	}
	fs.StringVar(&providerNameFlag, "provider", providerDefault, "provider catalog key (supported: "+providerName+")")
	fs.BoolVar(&listModels, "list-models", false, "print the usable provider catalog and exit")
	fs.BoolVar(&refreshModels, "refresh-models", false, "unconditionally refresh the model catalog cache and exit")
	fs.StringVar(&resumeID, "resume", "", "continue an existing v2 session under the state directory")
	fs.StringVar(&forkID, "fork", "", "create a new v2 session from an existing v2 session")
	fs.BoolVar(&batch, "batch", false, "force the one-shot CLI instead of the interactive TUI")
	fs.BoolVar(&uiJSON, "ui-json", false, "run the TUI headlessly with the versioned JSONL protocol")
	fs.StringVar(&uiSize, "ui-size", fmt.Sprintf("%dx%d", defaultUISizeWidth, defaultUISizeHeight), "initial -ui-json frame size (WxH, 20x5-500x200)")
	fs.BoolVar(&stream, "stream", false, "stream assistant text in batch mode (implied in TUI modes)")
	fs.IntVar(&maxTurns, "max-turns", defaultMaxTurns, "maximum cumulative model turns (0: unlimited; negative is invalid)")
	fs.IntVar(&maxTotalTokens, "max-total-tokens", defaultMaxTotalTokens, "cumulative token budget (0: unlimited; negative is invalid)")
	fs.IntVar(&maxContextTokens, "max-context-tokens", env.maxContext, "per-call context window (0: use the model catalog)")
	fs.DurationVar(&sessionTimeout, "session-timeout", defaultSessionTimeout, "per-run wall-clock timeout (0: no deadline; negative is invalid)")
	fs.DurationVar(&modelTimeout, "model-timeout", defaultModelTimeout, "per-model-call harness timeout (0: no deadline; negative is invalid)")
	fs.IntVar(&maxOutputTokens, "max-output-tokens", 0, "maximum output tokens per model call (0: no harness cap; negative is invalid)")
	fs.StringVar(&thinkingEffort, "thinking-effort", env.thinkingEffort, "model-advertised reasoning effort for model calls, or \"default\" to clear (default: AHARNESS_THINKING_EFFORT)")
	fs.StringVar(&providerBaseURL, "provider-base-url", env.baseURL, "provider OpenAI-compatible base URL (default: catalog)")
	fs.BoolVar(&requireUsage, "require-usage", false, "fail with protocol_error when a completed model response omits provider usage")
	fs.BoolVar(&showVersion, "version", false, "print build information and exit")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			writeUsage(out, fs)
		}
		return config{}, err
	}
	visited := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })

	if showVersion {
		return config{ShowVersion: true}, nil
	}

	providerSet := env.provider != "" || visited["provider"]
	provider := providerNameFlag
	if provider == "" && !providerSet {
		provider = providerName
	}
	if err := validateProvider(provider); err != nil {
		return config{}, err
	}

	// Terminal catalog actions ignore task, session, and mode flags. They
	// still honor the provider, state directory, and catalog URL.
	if listModels || refreshModels {
		state, err := resolveStateDir(stateDir, getenv)
		if err != nil {
			return config{}, err
		}
		return config{
			Provider:      provider,
			providerSet:   providerSet,
			StateDir:      state,
			ModelsDevURL:  env.modelsDevURL,
			ListModels:    listModels,
			RefreshModels: refreshModels,
		}, nil
	}

	thinkingEffortSet := visited["thinking-effort"] || env.thinkingEffort != ""
	if visited["thinking-effort"] && thinkingEffort == "" {
		return config{}, errors.New("thinking effort must not be empty; use \"default\" to clear")
	}
	if thinkingEffort != "" && thinkingEffort != "default" && !model.ValidReasoningEffort(thinkingEffort) {
		return config{}, fmt.Errorf("invalid thinking effort %q: must match [a-z][a-z0-9_-]* and be at most %d bytes",
			thinkingEffort, model.MaxThinkingEffortBytes)
	}
	if thinkingEffort == "default" {
		thinkingEffort = ""
	}

	taskText, taskSource, taskSet, err := resolveTask(task, taskFile)
	if err != nil {
		return config{}, err
	}
	if resumeID != "" && forkID != "" {
		return config{}, errors.New("-resume and -fork are mutually exclusive")
	}
	if resumeID != "" {
		resumeID, err = session.CanonicalID(resumeID)
		if err != nil {
			return config{}, err
		}
	}
	if forkID != "" {
		forkID, err = session.CanonicalID(forkID)
		if err != nil {
			return config{}, err
		}
	}
	if batch && uiJSON {
		return config{}, errors.New("-batch and -ui-json are mutually exclusive")
	}
	mode := ModeTUI
	switch {
	case uiJSON:
		mode = ModeUIJSON
	case batch:
		mode = ModeBatch
	}
	if visited["ui-size"] && !uiJSON {
		return config{}, errors.New("-ui-size is only valid with -ui-json")
	}
	sizeWidth, sizeHeight := defaultUISizeWidth, defaultUISizeHeight
	if uiJSON {
		sizeWidth, sizeHeight, err = parseUISize(uiSize)
		if err != nil {
			return config{}, err
		}
	}

	state, err := resolveStateDir(stateDir, getenv)
	if err != nil {
		return config{}, err
	}

	cfg := config{
		Task:              taskText,
		TaskSource:        taskSource,
		TaskSet:           taskSet,
		StateDir:          state,
		Model:             modelName,
		Provider:          provider,
		Token:             env.token,
		MaxTurns:          maxTurns,
		MaxTotalTokens:    maxTotalTokens,
		MaxContextTokens:  maxContextTokens,
		MaxOutputTokens:   maxOutputTokens,
		ThinkingEffort:    thinkingEffort,
		ModelsDevURL:      env.modelsDevURL,
		SessionTimeout:    sessionTimeout,
		ModelTimeout:      modelTimeout,
		LogRequests:       env.logRequests,
		RequireUsage:      requireUsage,
		ResumeID:          resumeID,
		ForkID:            forkID,
		Mode:              mode,
		Batch:             batch,
		UIJSON:            uiJSON,
		UISizeWidth:       sizeWidth,
		UISizeHeight:      sizeHeight,
		Stream:            stream,
		StreamSet:         visited["stream"],
		PromptVersion:     agent.PromptVersion,
		ToolsetVersion:    tools.Version,
		modelSet:          env.model != "" || visited["model"],
		providerSet:       providerSet,
		workspaceSet:      env.workspace != "" || visited["workspace"],
		baseURLSet:        env.baseURL != "" || visited["provider-base-url"],
		maxTurnsSet:       visited["max-turns"],
		maxTotalTokensSet: visited["max-total-tokens"],
		maxContextSet:     env.maxContextSet || visited["max-context-tokens"],
		maxOutputSet:      visited["max-output-tokens"],
		sessionTimeoutSet: visited["session-timeout"],
		modelTimeoutSet:   visited["model-timeout"],
		thinkingEffortSet: thinkingEffortSet,
	}
	if cfg.maxContextSet {
		cfg.maxContextValue = maxContextTokens
	}
	if providerBaseURL != "" {
		cfg.BaseURL = providerBaseURL
	}
	if cfg.baseURLSet {
		if err := validateBaseURL(cfg.BaseURL); err != nil {
			return config{}, err
		}
	}
	if cfg.workspaceSet {
		dir, err := resolveWorkspace(workspace)
		if err != nil {
			return config{}, err
		}
		cfg.Workspace = dir
	}

	if cfg.ResumeID == "" && cfg.ForkID == "" {
		if !cfg.modelSet || cfg.Model == "" {
			return config{}, errors.New("model is required; set -model or AHARNESS_MODEL")
		}
		if cfg.Workspace == "" {
			dir, err := resolveWorkspace(workspace)
			if err != nil {
				return config{}, err
			}
			cfg.Workspace = dir
		}
	}
	if err := validateExplicitValues(&cfg); err != nil {
		return config{}, err
	}
	if cfg.ResumeID == "" && cfg.ForkID == "" {
		if err := validateCumulative(&cfg); err != nil {
			return config{}, err
		}
	}
	if err := validateModeRules(&cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

// parseUISize parses and bounds the -ui-json initial frame size.
func parseUISize(raw string) (int, int, error) {
	w, h, ok := strings.Cut(raw, "x")
	if !ok {
		return 0, 0, fmt.Errorf("invalid -ui-size %q: want WxH", raw)
	}
	width, err := strconv.Atoi(w)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid -ui-size %q: %w", raw, err)
	}
	height, err := strconv.Atoi(h)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid -ui-size %q: %w", raw, err)
	}
	if width < minUISizeWidth || width > maxUISizeWidth || height < minUISizeHeight || height > maxUISizeHeight {
		return 0, 0, fmt.Errorf("invalid -ui-size %dx%d: width must be %d-%d and height %d-%d",
			width, height, minUISizeWidth, maxUISizeWidth, minUISizeHeight, maxUISizeHeight)
	}
	return width, height, nil
}

// validateModeRules applies the task, stream, and presentation rules for the
// selected mode. It runs again after a TTY check downgrades the default TUI to
// batch.
func validateModeRules(cfg *config) error {
	switch cfg.Mode {
	case ModeBatch:
		if cfg.ResumeID == "" && cfg.ForkID == "" && !cfg.TaskSet {
			return errors.New("one of -task or -task-file is required")
		}
		if cfg.ForkID != "" && !cfg.TaskSet {
			return errors.New("-fork requires a task")
		}
		if cfg.Stream && !cfg.TaskSet && cfg.ResumeID == "" {
			return errors.New("-stream without a task is allowed only with -resume")
		}
	case ModeTUI, ModeUIJSON:
		if cfg.StreamSet && !cfg.Stream {
			return errors.New("-stream=false is not supported in TUI mode; use -batch for one-shot output")
		}
		cfg.Stream = true
	default:
		return errors.New("invalid runtime mode")
	}
	return nil
}

func writeUsage(w io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintln(w, "usage: aharness -model model-id [flags]              # interactive TUI in a terminal")
	_, _ = fmt.Fprintln(w, "       aharness -batch (-task text | -task-file path) -model model-id [flags]")
	_, _ = fmt.Fprintln(w, "       aharness -batch -resume session-id [options]")
	_, _ = fmt.Fprintln(w, "       aharness -ui-json -model model-id [-ui-size WxH] [options]")
	_, _ = fmt.Fprintln(w, "       aharness -list-models [-provider opencode-go]")
	_, _ = fmt.Fprintln(w, "       aharness -refresh-models [-list-models]")
	_, _ = fmt.Fprintln(w)
	fs.VisitAll(func(f *flag.Flag) {
		name, usage := flag.UnquoteUsage(f)
		if name != "" {
			_, _ = fmt.Fprintf(w, "  -%s %s\n", f.Name, name)
		} else {
			_, _ = fmt.Fprintf(w, "  -%s\n", f.Name)
		}
		_, _ = fmt.Fprintf(w, "        %s (default %s)\n", usage, f.DefValue)
	})
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "environment: AHARNESS_MODEL, AHARNESS_PROVIDER, AHARNESS_WORKSPACE,")
	_, _ = fmt.Fprintln(w, "AHARNESS_STATE_DIR, AHARNESS_PROVIDER_TOKEN, AHARNESS_MAX_CONTEXT_TOKENS,")
	_, _ = fmt.Fprintln(w, "AHARNESS_LOG_REQUESTS, AHARNESS_PROVIDER_BASE_URL, AHARNESS_MODELS_DEV_URL,")
	_, _ = fmt.Fprintln(w, "AHARNESS_THINKING_EFFORT")
}

// versionRequested reports whether args select -version, including the
// standard flag forms. It lets -version print build information even when the
// environment or the rest of the configuration is unusable.
func versionRequested(args []string) bool {
	skipValue := false
	for _, arg := range args {
		if skipValue {
			skipValue = false
			continue
		}
		if arg == "--" || arg == "-" || !strings.HasPrefix(arg, "-") {
			return false
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if name == "" {
			continue
		}
		if name == "h" || name == "help" {
			return false
		}
		if name == "version" {
			if !hasValue {
				return true
			}
			if b, err := strconv.ParseBool(value); err == nil && b {
				return true
			}
			continue
		}
		if !hasValue {
			skipValue = true
		}
	}
	return false
}

func resolveTask(task, taskFile string) (string, string, bool, error) {
	switch {
	case task != "" && taskFile != "":
		return "", "", false, errors.New("exactly one of -task and -task-file may be set")
	case task == "" && taskFile == "":
		return "", "", false, nil
	case taskFile != "":
		data, err := os.ReadFile(taskFile)
		if err != nil {
			return "", "", false, fmt.Errorf("read task file %s: %w", taskFile, err)
		}
		return sanitizeText(string(data)), "-task-file", true, nil
	default:
		return sanitizeText(task), "-task", true, nil
	}
}

// validateProvider rejects provider keys outside the supported catalog.
func validateProvider(name string) error {
	if name == providerName {
		return nil
	}
	return fmt.Errorf("unsupported provider %q; supported providers: %s", name, providerName)
}

func resolveWorkspace(raw string) (string, error) {
	if raw == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve workspace: %w", err)
		}
		raw = cwd
	}
	dir, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve workspace %s: %w", raw, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("workspace %s: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace %s is not a directory", dir)
	}
	return filepath.Clean(dir), nil
}

func resolveStateDir(raw string, getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if raw != "" {
		dir, err := filepath.Abs(raw)
		if err != nil {
			return "", fmt.Errorf("resolve state directory %s: %w", raw, err)
		}
		return filepath.Clean(dir), nil
	}
	if xdg := getenv("XDG_STATE_HOME"); xdg != "" {
		if !filepath.IsAbs(xdg) {
			return "", fmt.Errorf("environment variable XDG_STATE_HOME must be an absolute path, got %q", xdg)
		}
		return filepath.Join(filepath.Clean(xdg), "aharness"), nil
	}
	home := getenv("HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
	}
	if home == "" {
		return "", errors.New("resolve home directory: empty path")
	}
	return filepath.Join(home, ".local", "state", "aharness"), nil
}

func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid provider base URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid provider base URL %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid provider base URL %q: missing host", raw)
	}
	if u.User != nil {
		return fmt.Errorf("invalid provider base URL %q: userinfo is not allowed", raw)
	}
	if u.RawQuery != "" || u.ForceQuery {
		return fmt.Errorf("invalid provider base URL %q: query is not allowed", raw)
	}
	if u.Fragment != "" {
		return fmt.Errorf("invalid provider base URL %q: fragment is not allowed", raw)
	}
	return nil
}

// validateExplicitValues rejects explicitly set values that are invalid even
// before session inheritance. Run budgets accept zero (unlimited) and reject
// negative values.
func validateExplicitValues(cfg *config) error {
	if cfg.maxTurnsSet && cfg.MaxTurns < 0 {
		return fmt.Errorf("max turns must not be negative, got %d", cfg.MaxTurns)
	}
	if cfg.maxTotalTokensSet && cfg.MaxTotalTokens < 0 {
		return fmt.Errorf("max total tokens must not be negative, got %d", cfg.MaxTotalTokens)
	}
	if cfg.maxContextSet && cfg.MaxContextTokens <= 0 {
		return fmt.Errorf("max context tokens must be positive, got %d", cfg.MaxContextTokens)
	}
	if cfg.maxOutputSet && cfg.MaxOutputTokens < 0 {
		return fmt.Errorf("max output tokens must not be negative, got %d", cfg.MaxOutputTokens)
	}
	if cfg.sessionTimeoutSet && cfg.SessionTimeout < 0 {
		return fmt.Errorf("session timeout must not be negative, got %s", cfg.SessionTimeout)
	}
	if cfg.modelTimeoutSet && cfg.ModelTimeout < 0 {
		return fmt.Errorf("model timeout must not be negative, got %s", cfg.ModelTimeout)
	}
	return nil
}

// validateCumulative rejects negative cumulative run budgets. Zero is the
// documented unlimited/sentinel value.
func validateCumulative(cfg *config) error {
	if cfg.MaxTurns < 0 {
		return fmt.Errorf("max turns must not be negative, got %d", cfg.MaxTurns)
	}
	if cfg.MaxTotalTokens < 0 {
		return fmt.Errorf("max total tokens must not be negative, got %d", cfg.MaxTotalTokens)
	}
	if cfg.SessionTimeout < 0 {
		return fmt.Errorf("session timeout must not be negative, got %s", cfg.SessionTimeout)
	}
	if cfg.ModelTimeout < 0 {
		return fmt.Errorf("model timeout must not be negative, got %s", cfg.ModelTimeout)
	}
	return nil
}

// validateEffective checks the resolved per-call and cumulative limits and
// recomputes the derived projection limits. The output budget may be zero,
// meaning the request omits the field and the provider/model ceiling applies.
func validateEffective(cfg *config) error {
	if err := validateCumulative(cfg); err != nil {
		return err
	}
	if cfg.MaxContextTokens <= 0 {
		return fmt.Errorf("max context tokens must be positive, got %d", cfg.MaxContextTokens)
	}
	if cfg.MaxOutputTokens < 0 {
		return fmt.Errorf("max output tokens must not be negative, got %d", cfg.MaxOutputTokens)
	}
	margin := agent.SafetyMargin(cfg.MaxContextTokens)
	input := cfg.MaxContextTokens - margin
	if cfg.InputLimit > 0 && cfg.InputLimit < input {
		input = cfg.InputLimit
	}
	if input <= 0 {
		return fmt.Errorf("max input tokens must be positive, got %d (context %d, safety margin %d)",
			input, cfg.MaxContextTokens, margin)
	}
	cfg.Limits = agent.Limits{
		MaxContextTokens: cfg.MaxContextTokens,
		MaxOutputTokens:  cfg.MaxOutputTokens,
		SafetyMargin:     margin,
		MaxInputTokens:   input,
		CompactionTarget: input,
	}
	return nil
}

// validateSessionRules enforces task rules and any explicit run budget after
// the session state is known. Recorded budgets are historical and do not
// participate; zero means unlimited.
func validateSessionRules(cfg *config, state agent.State) error {
	if cfg.Mode == ModeBatch {
		if cfg.ResumeID != "" {
			if state.LastRunEnded && state.LastRunStatus == agent.StopCompleted && !cfg.TaskSet {
				return errors.New("-resume requires a task because the last run completed")
			}
		}
		if cfg.ForkID != "" && !cfg.TaskSet {
			return errors.New("-fork requires a task")
		}
	}
	if cfg.MaxTurns > 0 && state.Turns >= cfg.MaxTurns {
		return fmt.Errorf("session is at its turn limit (%d >= %d); raise -max-turns", state.Turns, cfg.MaxTurns)
	}
	if cfg.MaxTotalTokens > 0 && state.Usage.Total() >= cfg.MaxTotalTokens {
		return fmt.Errorf("session is at its token limit (%d >= %d); raise -max-total-tokens", state.Usage.Total(), cfg.MaxTotalTokens)
	}
	return nil
}

// sanitizeText replaces invalid UTF-8 so task text can be encoded in the
// session log.
func sanitizeText(s string) string {
	return strings.ToValidUTF8(s, "\uFFFD")
}
