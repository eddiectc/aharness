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
	// SearXNGURL is the validated web_search base URL. Empty disables search.
	SearXNGURL string

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
	// HTTPTimeout bounds one provider request, including body reads. It is
	// resolved to a positive duration before the adapter is built.
	HTTPTimeout time.Duration
	// CatalogTimeout bounds one catalog request, including body reads.
	CatalogTimeout time.Duration
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
	// ConfigPath is the resolved config file path, empty when no file is
	// consulted. It is reported at startup for operator visibility.
	ConfigPath string

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
	httpTimeoutSet    bool
	catalogTimeoutSet bool
	thinkingEffortSet bool
}

// envSettings is the environment layer. It is parsed before flags so defaults
// are environment-aware and environment errors remain config errors. Each
// value carries a "set" bit because an empty variable means unset, which lets
// the config-file layer below it apply.
type envSettings struct {
	model             string
	provider          string
	providerSet       bool
	workspace         string
	workspaceSet      bool
	stateDir          string
	token             string
	baseURL           string
	baseURLSet        bool
	searxngURL        string
	searxngURLSet     bool
	modelsDevURL      string
	thinkingEffort    string
	thinkingEffortSet bool
	maxTurns          int
	maxTurnsSet       bool
	maxTotalTokens    int
	maxTotalTokensSet bool
	maxContext        int
	maxContextSet     bool
	maxOutput         int
	maxOutputSet      bool
	sessionTimeout    time.Duration
	sessionTimeoutSet bool
	modelTimeout      time.Duration
	modelTimeoutSet   bool
	httpTimeout       time.Duration
	httpTimeoutSet    bool
	catalogTimeout    time.Duration
	catalogTimeoutSet bool
	requireUsage      bool
	requireUsageSet   bool
	logRequests       bool
	logRequestsSet    bool
	config            string
}

// envInt reads an optional integer environment variable.
func envInt(getenv func(string) string, name string) (int, bool, error) {
	raw := getenv(name)
	if raw == "" {
		return 0, false, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false, fmt.Errorf("parse %s %q: %w", name, raw, err)
	}
	return n, true, nil
}

// envDuration reads an optional duration environment variable. Durations use
// the same "90s"/"2h" spelling as the flags.
func envDuration(getenv func(string) string, name string) (time.Duration, bool, error) {
	raw := getenv(name)
	if raw == "" {
		return 0, false, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, false, fmt.Errorf("parse %s %q: %w", name, raw, err)
	}
	return d, true, nil
}

// envBool reads an optional boolean environment variable.
func envBool(getenv func(string) string, name string) (bool, bool, error) {
	raw := getenv(name)
	if raw == "" {
		return false, false, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false, fmt.Errorf("parse %s %q: %w", name, raw, err)
	}
	return b, true, nil
}

// durationOr returns value when it was set, otherwise fallback. It keeps the
// help text honest for a timeout whose flag default is the built-in value
// rather than the zero sentinel: an unset variable must not print 0s when the
// effective default is positive.
func durationOr(value time.Duration, set bool, fallback time.Duration) time.Duration {
	if !set {
		return fallback
	}
	return value
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
		searxngURL:     getenv("AHARNESS_SEARXNG_URL"),
		modelsDevURL:   getenv("AHARNESS_MODELS_DEV_URL"),
		thinkingEffort: getenv("AHARNESS_THINKING_EFFORT"),
		config:         getenv("AHARNESS_CONFIG"),
	}
	env.providerSet = env.provider != ""
	env.workspaceSet = env.workspace != ""
	env.baseURLSet = env.baseURL != ""
	env.searxngURLSet = env.searxngURL != ""
	env.thinkingEffortSet = env.thinkingEffort != ""

	var err error
	if env.searxngURLSet {
		env.searxngURL, err = tools.ParseSearXNGURL(env.searxngURL)
		if err != nil {
			return envSettings{}, fmt.Errorf("AHARNESS_SEARXNG_URL: %w", err)
		}
	}
	if env.maxTurns, env.maxTurnsSet, err = envInt(getenv, "AHARNESS_MAX_TURNS"); err != nil {
		return envSettings{}, err
	}
	if env.maxTotalTokens, env.maxTotalTokensSet, err = envInt(getenv, "AHARNESS_MAX_TOTAL_TOKENS"); err != nil {
		return envSettings{}, err
	}
	if env.maxContext, env.maxContextSet, err = envInt(getenv, "AHARNESS_MAX_CONTEXT_TOKENS"); err != nil {
		return envSettings{}, err
	}
	if env.maxOutput, env.maxOutputSet, err = envInt(getenv, "AHARNESS_MAX_OUTPUT_TOKENS"); err != nil {
		return envSettings{}, err
	}
	if env.sessionTimeout, env.sessionTimeoutSet, err = envDuration(getenv, "AHARNESS_SESSION_TIMEOUT"); err != nil {
		return envSettings{}, err
	}
	if env.modelTimeout, env.modelTimeoutSet, err = envDuration(getenv, "AHARNESS_MODEL_TIMEOUT"); err != nil {
		return envSettings{}, err
	}
	if env.httpTimeout, env.httpTimeoutSet, err = envDuration(getenv, "AHARNESS_HTTP_TIMEOUT"); err != nil {
		return envSettings{}, err
	}
	if env.catalogTimeout, env.catalogTimeoutSet, err = envDuration(getenv, "AHARNESS_CATALOG_TIMEOUT"); err != nil {
		return envSettings{}, err
	}
	if env.requireUsage, env.requireUsageSet, err = envBool(getenv, "AHARNESS_REQUIRE_USAGE"); err != nil {
		return envSettings{}, err
	}
	if env.logRequests, env.logRequestsSet, err = envBool(getenv, "AHARNESS_LOG_REQUESTS"); err != nil {
		return envSettings{}, err
	}
	return env, nil
}

// parseConfig resolves flags, environment, the optional config file, and
// defaults in that order (first match wins) and validates what can be
// validated before a session log is opened. Resume and fork defer
// model/workspace/limit inheritance to applySessionDefaults.
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
		configFile       string
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
		httpTimeout      time.Duration
		catalogTimeout   time.Duration
		providerBaseURL  string
		searxngURL       string
		requireUsage     bool
		logRequests      bool
		showVersion      bool
	)
	fs.StringVar(&configFile, "config", env.config, "JSON config file (default: AHARNESS_CONFIG, then "+configFileHint+"; \"\" disables it)")
	fs.StringVar(&task, "task", "", "task text; seeds the first message in TUI modes")
	fs.StringVar(&taskFile, "task-file", "", "file containing the task; seeds the first message in TUI modes")
	fs.StringVar(&workspace, "workspace", env.workspace, "workspace directory (default: AHARNESS_WORKSPACE, then the current directory; inherited on resume/fork)")
	fs.StringVar(&stateDir, "state-dir", env.stateDir, "state directory (default: AHARNESS_STATE_DIR, then ~/.aharness)")
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
	fs.IntVar(&maxTurns, "max-turns", env.maxTurns, "maximum cumulative model turns (0: unlimited; negative is invalid)")
	fs.IntVar(&maxTotalTokens, "max-total-tokens", env.maxTotalTokens, "cumulative token budget (0: unlimited; negative is invalid)")
	fs.IntVar(&maxContextTokens, "max-context-tokens", env.maxContext, "per-call context window (0: use the model catalog)")
	fs.DurationVar(&sessionTimeout, "session-timeout", env.sessionTimeout, "per-run wall-clock timeout (0: no deadline; negative is invalid)")
	fs.DurationVar(&modelTimeout, "model-timeout", env.modelTimeout, "per-model-call harness timeout (0: no deadline; negative is invalid)")
	fs.DurationVar(&httpTimeout, "http-timeout", durationOr(env.httpTimeout, env.httpTimeoutSet, model.DefaultHTTPTimeout), "provider HTTP client timeout, including body reads (default: AHARNESS_HTTP_TIMEOUT, then "+model.DefaultHTTPTimeout.String()+"; must be positive)")
	fs.DurationVar(&catalogTimeout, "catalog-timeout", durationOr(env.catalogTimeout, env.catalogTimeoutSet, model.CatalogRequestTimeout), "catalog HTTP request timeout, including body reads (default: AHARNESS_CATALOG_TIMEOUT, then "+model.CatalogRequestTimeout.String()+"; must be positive)")
	fs.IntVar(&maxOutputTokens, "max-output-tokens", env.maxOutput, "maximum output tokens per model call (0: no harness cap; negative is invalid)")
	fs.StringVar(&thinkingEffort, "thinking-effort", env.thinkingEffort, "model-advertised reasoning effort for model calls, or \"default\" to clear (default: AHARNESS_THINKING_EFFORT)")
	fs.StringVar(&providerBaseURL, "provider-base-url", env.baseURL, "provider OpenAI-compatible base URL (default: catalog)")
	fs.StringVar(&searxngURL, "searxng-url", env.searxngURL, "SearXNG base URL for web_search (default: AHARNESS_SEARXNG_URL, then the config file; empty disables search)")
	fs.BoolVar(&requireUsage, "require-usage", false, "fail with protocol_error when a completed model response omits provider usage")
	fs.BoolVar(&logRequests, "log-requests", false, "record each projected model request as a debug event (default: AHARNESS_LOG_REQUESTS)")
	fs.BoolVar(&showVersion, "version", false, "print build information and exit")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			writeUsage(out, fs)
		}
		return config{}, err
	}
	visited := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })

	if visited["searxng-url"] {
		normalized, err := tools.ParseSearXNGURL(searxngURL)
		if err != nil {
			return config{}, fmt.Errorf("-searxng-url: %w", err)
		}
		searxngURL = normalized
	}

	if showVersion {
		return config{ShowVersion: true}, nil
	}

	// Resolve every layered setting. The config file sits below flags and
	// environment; the state directory is resolved first because the implicit
	// config path depends on it, and the file's own keys never move it.
	state, err := resolveStateDir(stateDir, getenv)
	if err != nil {
		return config{}, err
	}

	// Load the config file below flags and environment. An explicit -config or
	// AHARNESS_CONFIG must exist; the implicit default may be absent. An
	// explicit empty -config disables the file entirely.
	var configPath string
	var configExplicit bool
	switch {
	case visited["config"]:
		configPath, configExplicit = configFile, configFile != ""
	case env.config != "":
		configPath, configExplicit = env.config, true
	default:
		configPath = filepath.Join(state, "config.json")
	}
	file, err := loadFileConfig(configPath, configExplicit)
	if err != nil {
		return config{}, err
	}

	modelName = layered(modelName, visited["model"], env.model, env.model != "", file.Model, "").value()
	modelSet := visited["model"] || env.model != "" || file.Model != nil
	provider := layered(providerNameFlag, visited["provider"], env.provider, env.providerSet,
		file.Provider, providerName).value()
	providerSet := visited["provider"] || env.providerSet || file.Provider != nil
	if err := validateProvider(provider); err != nil {
		return config{}, err
	}
	workspace = layered(workspace, visited["workspace"], env.workspace, env.workspaceSet, file.Workspace, "").value()
	workspaceSet := visited["workspace"] || env.workspaceSet || file.Workspace != nil
	providerBaseURL = layered(providerBaseURL, visited["provider-base-url"], env.baseURL, env.baseURLSet,
		file.ProviderBaseURL, "").value()
	baseURLSet := visited["provider-base-url"] || env.baseURLSet || file.ProviderBaseURL != nil
	modelsDevURL := layered("", false, env.modelsDevURL, env.modelsDevURL != "", file.ModelsDevURL, "").value()
	catalogTimeoutLayer := durationLayer(catalogTimeout, visited["catalog-timeout"], env.catalogTimeout, env.catalogTimeoutSet,
		file.CatalogTimeout, model.CatalogRequestTimeout)

	// Terminal catalog actions ignore task, session, and mode flags. They
	// still honor the provider, state directory, catalog URL, and timeouts.
	if listModels || refreshModels {
		catalogTimeout := catalogTimeoutLayer.value()
		if catalogTimeout <= 0 {
			return config{}, fmt.Errorf("catalog timeout must be positive, got %s", catalogTimeout)
		}
		return config{
			Provider:       provider,
			providerSet:    providerSet,
			StateDir:       state,
			ModelsDevURL:   modelsDevURL,
			CatalogTimeout: catalogTimeout,
			ListModels:     listModels,
			RefreshModels:  refreshModels,
			ConfigPath:     configPath,
		}, nil
	}

	thinkingEffort = layered(thinkingEffort, visited["thinking-effort"], env.thinkingEffort, env.thinkingEffortSet,
		file.ThinkingEffort, "").value()
	thinkingEffortSet := visited["thinking-effort"] || env.thinkingEffortSet || file.ThinkingEffort != nil
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

	maxTurnsLayer := layered(maxTurns, visited["max-turns"], env.maxTurns, env.maxTurnsSet,
		file.MaxTurns, defaultMaxTurns)
	maxTotalTokensLayer := layered(maxTotalTokens, visited["max-total-tokens"], env.maxTotalTokens, env.maxTotalTokensSet,
		file.MaxTotalTokens, defaultMaxTotalTokens)
	maxContextLayer := layered(maxContextTokens, visited["max-context-tokens"], env.maxContext, env.maxContextSet,
		file.MaxContextTokens, 0)
	maxOutputLayer := layered(maxOutputTokens, visited["max-output-tokens"], env.maxOutput, env.maxOutputSet,
		file.MaxOutputTokens, 0)
	sessionTimeoutLayer := durationLayer(sessionTimeout, visited["session-timeout"], env.sessionTimeout, env.sessionTimeoutSet,
		file.SessionTimeout, defaultSessionTimeout)
	modelTimeoutLayer := durationLayer(modelTimeout, visited["model-timeout"], env.modelTimeout, env.modelTimeoutSet,
		file.ModelTimeout, defaultModelTimeout)
	httpTimeoutLayer := durationLayer(httpTimeout, visited["http-timeout"], env.httpTimeout, env.httpTimeoutSet,
		file.HTTPTimeout, model.DefaultHTTPTimeout)
	requireUsageLayer := layered(requireUsage, visited["require-usage"], env.requireUsage, env.requireUsageSet,
		file.RequireUsage, false)
	logRequestsLayer := layered(logRequests, visited["log-requests"], env.logRequests, env.logRequestsSet,
		file.LogRequests, false)

	searxngURL = layered(searxngURL, visited["searxng-url"], env.searxngURL, env.searxngURLSet,
		file.SearXNGURL, "").value()
	if searxngURL != "" {
		normalized, parseErr := tools.ParseSearXNGURL(searxngURL)
		if parseErr != nil {
			return config{}, fmt.Errorf("searxng_url: %w", parseErr)
		}
		searxngURL = normalized
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

	cfg := config{
		Task:              taskText,
		TaskSource:        taskSource,
		TaskSet:           taskSet,
		StateDir:          state,
		Model:             modelName,
		Provider:          provider,
		Token:             env.token,
		SearXNGURL:        searxngURL,
		MaxTurns:          maxTurnsLayer.value(),
		MaxTotalTokens:    maxTotalTokensLayer.value(),
		MaxContextTokens:  maxContextLayer.value(),
		MaxOutputTokens:   maxOutputLayer.value(),
		ThinkingEffort:    thinkingEffort,
		ModelsDevURL:      modelsDevURL,
		HTTPTimeout:       httpTimeoutLayer.value(),
		CatalogTimeout:    catalogTimeoutLayer.value(),
		SessionTimeout:    sessionTimeoutLayer.value(),
		ModelTimeout:      modelTimeoutLayer.value(),
		LogRequests:       logRequestsLayer.value(),
		RequireUsage:      requireUsageLayer.value(),
		ResumeID:          resumeID,
		ForkID:            forkID,
		Mode:              mode,
		Batch:             batch,
		UIJSON:            uiJSON,
		UISizeWidth:       sizeWidth,
		UISizeHeight:      sizeHeight,
		Stream:            stream,
		StreamSet:         visited["stream"],
		ConfigPath:        configPath,
		PromptVersion:     agent.PromptVersion,
		ToolsetVersion:    tools.Version,
		modelSet:          modelSet,
		providerSet:       providerSet,
		workspaceSet:      workspaceSet,
		baseURLSet:        baseURLSet,
		maxTurnsSet:       maxTurnsLayer.set(),
		maxTotalTokensSet: maxTotalTokensLayer.set(),
		maxContextSet:     maxContextLayer.set(),
		maxOutputSet:      maxOutputLayer.set(),
		sessionTimeoutSet: sessionTimeoutLayer.set(),
		modelTimeoutSet:   modelTimeoutLayer.set(),
		httpTimeoutSet:    httpTimeoutLayer.set(),
		catalogTimeoutSet: catalogTimeoutLayer.set(),
		thinkingEffortSet: thinkingEffortSet,
	}
	if cfg.maxContextSet {
		cfg.maxContextValue = cfg.MaxContextTokens
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
	_, _ = fmt.Fprintln(w, "AHARNESS_STATE_DIR, AHARNESS_CONFIG, AHARNESS_PROVIDER_TOKEN,")
	_, _ = fmt.Fprintln(w, "AHARNESS_MAX_CONTEXT_TOKENS, AHARNESS_MAX_TURNS, AHARNESS_MAX_TOTAL_TOKENS,")
	_, _ = fmt.Fprintln(w, "AHARNESS_MAX_OUTPUT_TOKENS, AHARNESS_SESSION_TIMEOUT, AHARNESS_MODEL_TIMEOUT,")
	_, _ = fmt.Fprintln(w, "AHARNESS_HTTP_TIMEOUT, AHARNESS_CATALOG_TIMEOUT, AHARNESS_REQUIRE_USAGE,")
	_, _ = fmt.Fprintln(w, "AHARNESS_LOG_REQUESTS, AHARNESS_SEARXNG_URL, AHARNESS_PROVIDER_BASE_URL,")
	_, _ = fmt.Fprintln(w, "AHARNESS_MODELS_DEV_URL, AHARNESS_THINKING_EFFORT")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "precedence: flag > environment > config file > default;")
	_, _ = fmt.Fprintf(w, "the config file is %s (JSON; see docs/configuration.md)\n", configFileHint)
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
	return filepath.Join(home, ".aharness"), nil
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
	// The HTTP client timeout is always in effect, so it must be positive
	// whichever layer supplied it.
	if cfg.HTTPTimeout <= 0 {
		return fmt.Errorf("HTTP timeout must be positive, got %s", cfg.HTTPTimeout)
	}
	if cfg.CatalogTimeout <= 0 {
		return fmt.Errorf("catalog timeout must be positive, got %s", cfg.CatalogTimeout)
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
