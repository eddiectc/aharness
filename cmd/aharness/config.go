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

	acceptedModels = "crouton-alpha-free, deepseek-v4.1-flash, muse-spark-1.3-contributor"

	defaultMaxTurns         = 30
	defaultMaxTotalTokens   = 2_000_000
	defaultMaxContextTokens = 200_000
	defaultMaxOutputTokens  = 8_192
	defaultSessionTimeout   = 60 * time.Minute
	defaultModelTimeout     = 300 * time.Second
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
	Limits           agent.Limits
	SessionTimeout   time.Duration
	ModelTimeout     time.Duration
	LogRequests      bool

	ResumeID string
	ForkID   string

	Interactive bool
	Stream      bool

	ShowVersion        bool
	ProvisionalContext bool
	HarnessVersion     string
	PromptVersion      string
	ToolsetVersion     string

	modelSet          bool
	workspaceSet      bool
	baseURLSet        bool
	maxTurnsSet       bool
	maxTotalTokensSet bool
	maxContextSet     bool
	maxOutputSet      bool
	sessionTimeoutSet bool
}

// envSettings is the environment layer of the flag defaults. It is parsed
// before flags so defaults are env-aware and env errors remain config errors.
type envSettings struct {
	model         string
	workspace     string
	stateDir      string
	token         string
	baseURL       string
	maxContext    int
	maxContextSet bool
	logRequests   bool
}

func loadEnv(getenv func(string) string) (envSettings, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	env := envSettings{
		model:      getenv("AHARNESS_MODEL"),
		workspace:  getenv("AHARNESS_WORKSPACE"),
		stateDir:   getenv("AHARNESS_STATE_DIR"),
		token:      getenv("AHARNESS_PROVIDER_TOKEN"),
		baseURL:    getenv("AHARNESS_PROVIDER_BASE_URL"),
		maxContext: defaultMaxContextTokens,
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
		resumeID         string
		forkID           string
		interactive      bool
		stream           bool
		maxTurns         int
		maxTotalTokens   int
		maxContextTokens int
		maxOutputTokens  int
		sessionTimeout   time.Duration
		providerBaseURL  string
		showVersion      bool
	)
	fs.StringVar(&task, "task", "", "task text; exactly one of -task or -task-file is required for a new session")
	fs.StringVar(&taskFile, "task-file", "", "file containing the task; exactly one of -task or -task-file is required for a new session")
	fs.StringVar(&workspace, "workspace", env.workspace, "workspace directory (default: AHARNESS_WORKSPACE, then the current directory; inherited on resume/fork)")
	fs.StringVar(&stateDir, "state-dir", env.stateDir, "state directory (default: AHARNESS_STATE_DIR, then XDG_STATE_HOME/aharness, then ~/.local/state/aharness)")
	fs.StringVar(&modelName, "model", env.model, "model ID (required for a new session; "+acceptedModels+")")
	fs.StringVar(&resumeID, "resume", "", "continue an existing v2 session under the state directory")
	fs.StringVar(&forkID, "fork", "", "create a new v2 session from an existing v2 session; requires a task")
	fs.BoolVar(&interactive, "interactive", false, "read steering from stdin and enable first-SIGINT interruption")
	fs.BoolVar(&stream, "stream", false, "stream assistant text to stdout (implied by -interactive)")
	fs.IntVar(&maxTurns, "max-turns", defaultMaxTurns, "maximum cumulative model turns")
	fs.IntVar(&maxTotalTokens, "max-total-tokens", defaultMaxTotalTokens, "cumulative token budget")
	fs.IntVar(&maxContextTokens, "max-context-tokens", env.maxContext, "per-call context window")
	fs.DurationVar(&sessionTimeout, "session-timeout", defaultSessionTimeout, "per-run wall-clock timeout")
	fs.IntVar(&maxOutputTokens, "max-output-tokens", defaultMaxOutputTokens, "maximum output tokens per model call")
	baseDefault := env.baseURL
	if baseDefault == "" {
		baseDefault = model.DefaultBaseURL
	}
	fs.StringVar(&providerBaseURL, "provider-base-url", baseDefault, "provider OpenAI-compatible base URL")
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
		if !taskSet {
			return config{}, errors.New("-fork requires a task")
		}
	}
	if resumeID == "" && forkID == "" && !taskSet {
		return config{}, errors.New("one of -task or -task-file is required")
	}
	if stream && !taskSet && resumeID == "" {
		return config{}, errors.New("-stream without a task is allowed only with -resume")
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
		Provider:          providerName,
		Token:             env.token,
		MaxTurns:          maxTurns,
		MaxTotalTokens:    maxTotalTokens,
		MaxContextTokens:  maxContextTokens,
		MaxOutputTokens:   maxOutputTokens,
		SessionTimeout:    sessionTimeout,
		ModelTimeout:      defaultModelTimeout,
		LogRequests:       env.logRequests,
		ResumeID:          resumeID,
		ForkID:            forkID,
		Interactive:       interactive,
		Stream:            stream,
		PromptVersion:     agent.PromptVersion,
		ToolsetVersion:    tools.Version,
		modelSet:          env.model != "" || visited["model"],
		workspaceSet:      env.workspace != "" || visited["workspace"],
		baseURLSet:        env.baseURL != "" || visited["provider-base-url"],
		maxTurnsSet:       visited["max-turns"],
		maxTotalTokensSet: visited["max-total-tokens"],
		maxContextSet:     env.maxContextSet || visited["max-context-tokens"],
		maxOutputSet:      visited["max-output-tokens"],
		sessionTimeoutSet: visited["session-timeout"],
	}
	// -interactive implies streaming unless -stream=false was explicit.
	if interactive && !visited["stream"] {
		cfg.Stream = true
	}
	if providerBaseURL == "" {
		providerBaseURL = model.DefaultBaseURL
	}
	cfg.BaseURL = providerBaseURL
	if cfg.baseURLSet {
		if err := validateBaseURL(cfg.BaseURL); err != nil {
			return config{}, err
		}
	}
	if cfg.modelSet {
		dialect, err := dialectForModel(cfg.Model)
		if err != nil {
			return config{}, err
		}
		cfg.Dialect = dialect
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
		cfg.ProvisionalContext = !cfg.maxContextSet
	}
	if err := validateExplicitValues(&cfg); err != nil {
		return config{}, err
	}
	if cfg.ResumeID == "" && cfg.ForkID == "" {
		if err := validateEffective(&cfg); err != nil {
			return config{}, err
		}
	}
	return cfg, nil
}

func writeUsage(w io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintln(w, "usage: aharness (-task text | -task-file path) -model model-id [flags]")
	_, _ = fmt.Fprintln(w, "       aharness -resume session-id [options]")
	_, _ = fmt.Fprintln(w, "       aharness -fork session-id (-task text | -task-file path) [options]")
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
	_, _ = fmt.Fprintln(w, "environment: AHARNESS_MODEL, AHARNESS_WORKSPACE, AHARNESS_STATE_DIR,")
	_, _ = fmt.Fprintln(w, "AHARNESS_PROVIDER_TOKEN, AHARNESS_MAX_CONTEXT_TOKENS, AHARNESS_LOG_REQUESTS,")
	_, _ = fmt.Fprintln(w, "AHARNESS_PROVIDER_BASE_URL")
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

func dialectForModel(name string) (model.Dialect, error) {
	switch name {
	case "":
		return "", errors.New("model is required; set -model or AHARNESS_MODEL")
	case "crouton-alpha-free":
		return model.DialectResponses, nil
	case "deepseek-v4.1-flash":
		return model.DialectChat, nil
	case "muse-spark-1.3-contributor":
		return model.DialectResponses, nil
	default:
		return "", fmt.Errorf("unsupported model %q; accepted models: %s", name, acceptedModels)
	}
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
// before session inheritance.
func validateExplicitValues(cfg *config) error {
	if cfg.maxTurnsSet && cfg.MaxTurns <= 0 {
		return fmt.Errorf("max turns must be positive, got %d", cfg.MaxTurns)
	}
	if cfg.maxTotalTokensSet && cfg.MaxTotalTokens <= 0 {
		return fmt.Errorf("max total tokens must be positive, got %d", cfg.MaxTotalTokens)
	}
	if cfg.maxContextSet && cfg.MaxContextTokens <= 0 {
		return fmt.Errorf("max context tokens must be positive, got %d", cfg.MaxContextTokens)
	}
	if cfg.maxOutputSet && cfg.MaxOutputTokens <= 0 {
		return fmt.Errorf("max output tokens must be positive, got %d", cfg.MaxOutputTokens)
	}
	if cfg.sessionTimeoutSet && cfg.SessionTimeout <= 0 {
		return fmt.Errorf("session timeout must be positive, got %s", cfg.SessionTimeout)
	}
	return nil
}

// validateEffective checks the resolved per-call and cumulative limits and
// recomputes the derived projection limits.
func validateEffective(cfg *config) error {
	if cfg.MaxTurns <= 0 {
		return fmt.Errorf("max turns must be positive, got %d", cfg.MaxTurns)
	}
	if cfg.MaxTotalTokens <= 0 {
		return fmt.Errorf("max total tokens must be positive, got %d", cfg.MaxTotalTokens)
	}
	if cfg.MaxContextTokens <= 0 {
		return fmt.Errorf("max context tokens must be positive, got %d", cfg.MaxContextTokens)
	}
	if cfg.MaxOutputTokens <= 0 {
		return fmt.Errorf("max output tokens must be positive, got %d", cfg.MaxOutputTokens)
	}
	if cfg.SessionTimeout <= 0 {
		return fmt.Errorf("session timeout must be positive, got %s", cfg.SessionTimeout)
	}
	if cfg.ModelTimeout <= 0 {
		return fmt.Errorf("model timeout must be positive, got %s", cfg.ModelTimeout)
	}
	margin := agent.SafetyMargin(cfg.MaxContextTokens)
	if cfg.MaxOutputTokens >= cfg.MaxContextTokens-margin {
		return fmt.Errorf("max output tokens (%d) plus safety margin (%d) must be less than max context tokens (%d)",
			cfg.MaxOutputTokens, margin, cfg.MaxContextTokens)
	}
	input := cfg.MaxContextTokens - cfg.MaxOutputTokens - margin
	cfg.Limits = agent.Limits{
		MaxContextTokens: cfg.MaxContextTokens,
		MaxOutputTokens:  cfg.MaxOutputTokens,
		SafetyMargin:     margin,
		MaxInputTokens:   input,
		CompactionTarget: input,
	}
	return nil
}

// applySessionDefaults inherits model, workspace, base URL, and limits from
// the durable session for resume and fork. Explicit values win.
func applySessionDefaults(cfg *config, state agent.State) error {
	if state.Provider != providerName {
		return fmt.Errorf("session provider %q is not supported; want %q", state.Provider, providerName)
	}
	if !agent.SupportsPrompt(state.PromptVersion) {
		return fmt.Errorf("session prompt version %q is not supported by this binary", state.PromptVersion)
	}
	if !agent.SupportsToolset(state.ToolsetVersion) {
		return fmt.Errorf("session toolset version %q is not supported by this binary", state.ToolsetVersion)
	}
	if !cfg.modelSet {
		cfg.Model = state.Model
		if cfg.Model == "" {
			cfg.Model = state.InitialModel
		}
	}
	if cfg.Model == "" {
		return errors.New("session does not record a model; set -model")
	}
	dialect, err := dialectForModel(cfg.Model)
	if err != nil {
		return err
	}
	cfg.Dialect = dialect
	if !cfg.workspaceSet {
		cfg.Workspace = state.Workspace
	}
	if cfg.Workspace == "" {
		return errors.New("session does not record a workspace; set -workspace")
	}
	if !cfg.baseURLSet {
		cfg.BaseURL = state.BaseURL
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = model.DefaultBaseURL
	}
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return err
	}
	if !cfg.maxTurnsSet && state.MaxTurns > 0 {
		cfg.MaxTurns = state.MaxTurns
	}
	if !cfg.maxTotalTokensSet && state.MaxTotalTokens > 0 {
		cfg.MaxTotalTokens = state.MaxTotalTokens
	}
	if !cfg.maxContextSet && state.Limits.MaxContextTokens > 0 {
		cfg.MaxContextTokens = state.Limits.MaxContextTokens
	}
	if !cfg.maxOutputSet && state.Limits.MaxOutputTokens > 0 {
		cfg.MaxOutputTokens = state.Limits.MaxOutputTokens
	}
	if !cfg.sessionTimeoutSet && state.SessionTimeout > 0 {
		cfg.SessionTimeout = state.SessionTimeout
	}
	if state.ModelTimeout > 0 {
		cfg.ModelTimeout = state.ModelTimeout
	}
	cfg.PromptVersion = state.PromptVersion
	cfg.ToolsetVersion = state.ToolsetVersion
	cfg.ProvisionalContext = false
	return validateEffective(cfg)
}

// validateSessionRules enforces task rules and inherited cumulative limits
// after the session state is known.
func validateSessionRules(cfg *config, state agent.State) error {
	if cfg.ResumeID != "" {
		if state.LastRunEnded && state.LastRunStatus == agent.StopCompleted && !cfg.TaskSet {
			return errors.New("-resume requires a task because the last run completed")
		}
	}
	if cfg.ForkID != "" && !cfg.TaskSet {
		return errors.New("-fork requires a task")
	}
	if cfg.Interactive && cfg.ResumeID == "" && !cfg.TaskSet {
		return errors.New("-interactive without -resume requires a task")
	}
	if state.Turns >= cfg.MaxTurns {
		return fmt.Errorf("session is at its turn limit (%d >= %d); raise -max-turns", state.Turns, cfg.MaxTurns)
	}
	if state.Usage.Total() >= cfg.MaxTotalTokens {
		return fmt.Errorf("session is at its token limit (%d >= %d); raise -max-total-tokens", state.Usage.Total(), cfg.MaxTotalTokens)
	}
	return nil
}

// sanitizeText replaces invalid UTF-8 so task text can be encoded in the
// session log.
func sanitizeText(s string) string {
	return strings.ToValidUTF8(s, "\uFFFD")
}
