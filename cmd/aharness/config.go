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

// config is the effective, validated command-line and environment
// configuration.
type config struct {
	Task             string
	TaskSource       string
	Workspace        string
	StateDir         string
	Model            string
	Provider         string
	Dialect          model.Dialect
	BaseURL          string
	Token            string
	MaxTurns         int
	MaxTotalTokens   int
	MaxContextTokens int
	MaxOutputTokens  int
	Limits           agent.Limits

	SessionTimeout time.Duration
	ModelTimeout   time.Duration
	LogRequests    bool

	ShowVersion        bool
	ProvisionalContext bool
	HarnessVersion     string
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
// validates the effective configuration once. It does not create session
// state.
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
		maxTurns         int
		maxTotalTokens   int
		maxContextTokens int
		maxOutputTokens  int
		sessionTimeout   time.Duration
		providerBaseURL  string
		showVersion      bool
	)
	fs.StringVar(&task, "task", "", "task text; exactly one of -task or -task-file is required")
	fs.StringVar(&taskFile, "task-file", "", "file containing the task; exactly one of -task or -task-file is required")
	fs.StringVar(&workspace, "workspace", env.workspace, "workspace directory (default: AHARNESS_WORKSPACE, then the current directory)")
	fs.StringVar(&stateDir, "state-dir", env.stateDir, "state directory (default: AHARNESS_STATE_DIR, then XDG_STATE_HOME/aharness, then ~/.local/state/aharness)")
	fs.StringVar(&modelName, "model", env.model, "model ID (required; "+acceptedModels+")")
	fs.IntVar(&maxTurns, "max-turns", defaultMaxTurns, "maximum model turns")
	fs.IntVar(&maxTotalTokens, "max-total-tokens", defaultMaxTotalTokens, "cumulative token budget")
	fs.IntVar(&maxContextTokens, "max-context-tokens", env.maxContext, "per-call context window")
	fs.DurationVar(&sessionTimeout, "session-timeout", defaultSessionTimeout, "session wall-clock timeout")
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

	maxContextSet := env.maxContextSet
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "max-context-tokens" {
			maxContextSet = true
		}
	})

	if showVersion {
		return config{ShowVersion: true, ProvisionalContext: !maxContextSet}, nil
	}

	taskText, taskSource, err := resolveTask(task, taskFile)
	if err != nil {
		return config{}, err
	}
	dialect, err := dialectForModel(modelName)
	if err != nil {
		return config{}, err
	}
	workspaceDir, err := resolveWorkspace(workspace)
	if err != nil {
		return config{}, err
	}
	state, err := resolveStateDir(stateDir, getenv)
	if err != nil {
		return config{}, err
	}
	baseURL := providerBaseURL
	if baseURL == "" {
		baseURL = model.DefaultBaseURL
	}
	if err := validateBaseURL(baseURL); err != nil {
		return config{}, err
	}

	cfg := config{
		Task:               taskText,
		TaskSource:         taskSource,
		Workspace:          workspaceDir,
		StateDir:           state,
		Model:              modelName,
		Provider:           providerName,
		Dialect:            dialect,
		BaseURL:            baseURL,
		Token:              env.token,
		MaxTurns:           maxTurns,
		MaxTotalTokens:     maxTotalTokens,
		MaxContextTokens:   maxContextTokens,
		MaxOutputTokens:    maxOutputTokens,
		SessionTimeout:     sessionTimeout,
		ModelTimeout:       defaultModelTimeout,
		LogRequests:        env.logRequests,
		ProvisionalContext: !maxContextSet,
	}
	if err := validateConfig(&cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func writeUsage(w io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintln(w, "usage: aharness (-task text | -task-file path) -model model-id [flags]")
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

func resolveTask(task, taskFile string) (string, string, error) {
	switch {
	case task != "" && taskFile != "":
		return "", "", errors.New("exactly one of -task and -task-file may be set")
	case task == "" && taskFile == "":
		return "", "", errors.New("one of -task or -task-file is required")
	case taskFile != "":
		data, err := os.ReadFile(taskFile)
		if err != nil {
			return "", "", fmt.Errorf("read task file %s: %w", taskFile, err)
		}
		return sanitizeText(string(data)), "-task-file", nil
	default:
		return sanitizeText(task), "-task", nil
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
	return nil
}

func validateConfig(cfg *config) error {
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

// sanitizeText replaces invalid UTF-8 so task text can be encoded in the
// session log.
func sanitizeText(s string) string {
	return strings.ToValidUTF8(s, "\uFFFD")
}
