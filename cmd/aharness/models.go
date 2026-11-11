package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"aharness/internal/agent"
	"aharness/internal/model"
	"aharness/internal/tui"
)

const (
	// exitProvider is the provider/transport failure exit code.
	exitProvider = 3
	// exitProtocol is the malformed provider/catalog response exit code.
	exitProtocol = 4
)

// newCatalogSource wires the models.dev source for this invocation. The
// source is unauthenticated and never receives the provider token.
func newCatalogSource(cfg config) (model.CatalogSource, error) {
	return model.NewModelsDevSource(model.CatalogOptions{
		URL:       cfg.ModelsDevURL,
		StateDir:  cfg.StateDir,
		UserAgent: "aharness/" + cfg.HarnessVersion,
		Now:       time.Now,
		TTL:       model.CatalogTTL,
	})
}

// catalogExitCode maps catalog failures to the documented exit codes.
func catalogExitCode(err error) int {
	switch {
	case errors.Is(err, model.ErrCatalogUnavailable):
		return exitProvider
	case errors.Is(err, model.ErrCatalogInvalid):
		return exitProtocol
	case errors.Is(err, model.ErrCatalogCache):
		return exitConfig
	default:
		return exitConfig
	}
}

// warnf writes one operator warning line. All discovered strings are
// sanitized before reaching a terminal.
func warnf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "aharness: warning: %s\n", catalogField(fmt.Sprintf(format, args...), 2048))
}

func warnCatalog(w io.Writer, cat model.Catalog) {
	for _, warning := range cat.Warnings {
		warnf(w, "%s", warning)
	}
}

// warnCatalogTo routes catalog warnings through a caller-supplied sink, such
// as the TUI notice stream.
func warnCatalogTo(warn func(string), cat model.Catalog) {
	if warn == nil {
		return
	}
	for _, warning := range cat.Warnings {
		warn(warning)
	}
}

// sessionCatalogSource constructs a source only when the caller explicitly
// changed the model; resume/fork without an explicit model stays offline.
func sessionCatalogSource(cfg config) (model.CatalogSource, error) {
	// An explicit non-default effort must be validated against the effective
	// model, even when the recorded model is unchanged. Explicit "default"
	// and plain resume/fork stay offline.
	explicitEffort := cfg.thinkingEffortSet && cfg.ThinkingEffort != ""
	if !cfg.modelSet && !explicitEffort {
		return nil, nil
	}
	return newCatalogSource(cfg)
}

// runCatalogAction performs the -refresh-models and -list-models terminal
// actions. It creates no session and reads no task or provider token.
func runCatalogAction(cfg config, stdout, stderr io.Writer) int {
	source, err := newCatalogSource(cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
		return exitConfig
	}
	ctx := context.Background()
	var cat model.Catalog
	if cfg.RefreshModels {
		cat, err = source.Refresh(ctx, cfg.Provider)
	} else {
		cat, err = source.Revalidate(ctx, cfg.Provider)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", sanitizeText(err.Error()))
		return catalogExitCode(err)
	}
	warnCatalog(stderr, cat)
	usable := cat.Usable()
	if cfg.RefreshModels && !cfg.ListModels {
		writeRefreshSummary(stdout, cat, len(usable))
	}
	if cfg.ListModels {
		for _, m := range cat.SortedModels() {
			if m.Unavailable != "" {
				warnf(stderr, "model %s is not usable: %s", m.ID, m.Unavailable)
			}
		}
		writeCatalogList(stdout, usable)
		if cfg.RefreshModels {
			writeRefreshSummary(stderr, cat, len(usable))
		}
	}
	return 0
}

func writeRefreshSummary(w io.Writer, cat model.Catalog, usable int) {
	_, _ = fmt.Fprintf(w, "refreshed %d usable models for %s from %s at %s\n",
		usable, catalogField(cat.Provider, 64), catalogField(cat.Source, 512),
		cat.FetchedAt.UTC().Format(time.RFC3339))
}

func writeCatalogList(w io.Writer, models []model.CatalogModel) {
	_, _ = fmt.Fprintln(w, "ID\tNAME\tDIALECT\tCONTEXT\tOUTPUT\tSTATUS\tEFFORT")
	for _, m := range models {
		output := "-"
		if m.OutputLimit > 0 {
			output = strconv.Itoa(m.OutputLimit)
		}
		status := m.Status
		if status == "" {
			status = "-"
		}
		effort := "-"
		if len(m.ReasoningEfforts) > 0 {
			effort = strings.Join(m.ReasoningEfforts, ",")
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			catalogField(m.ID, 256), catalogField(m.Name, 200), m.Dialect,
			m.ContextWindow, output, catalogField(status, 32), catalogField(effort, 256))
	}
}

// catalogField makes an untrusted catalog string safe for one tab-separated
// column: invalid UTF-8 and terminal controls are sanitized, and the value is
// capped without splitting a rune.
func catalogField(s string, limit int) string {
	s = tui.SanitizeLine(s)
	var b strings.Builder
	for _, r := range s {
		if b.Len()+utf8.RuneLen(r) > limit {
			break
		}
		_, _ = b.WriteRune(r)
	}
	return b.String()
}

// resolveNewModel loads the catalog and applies the selected model's dialect,
// base URL, context window, and optional input ceiling to a new session.
func resolveNewModel(ctx context.Context, cfg *config, source model.CatalogSource, stderr io.Writer) error {
	cat, err := source.Load(ctx, cfg.Provider)
	if err != nil {
		return err
	}
	warnCatalog(stderr, cat)
	m, err := usableCatalogModel(cat, cfg.Model)
	if err != nil {
		return err
	}
	if err := applyCatalogModel(cfg, m, func(w string) { warnf(stderr, "%s", w) }); err != nil {
		return err
	}
	cfg.Pricing = m.Pricing(cat.Source, cat.FetchedAt)
	if cfg.thinkingEffortSet && cfg.ThinkingEffort != "" {
		if err := validateModelThinkingEffort(cfg.ThinkingEffort, m); err != nil {
			return err
		}
	}
	if m.Deprecated {
		warnf(stderr, "model %s is deprecated", m.ID)
	}
	return validateEffective(cfg)
}

// validateModelThinkingEffort rejects an explicit effort the effective model
// does not advertise, with the model's advertised values in the message.
func validateModelThinkingEffort(value string, m model.CatalogModel) error {
	id := catalogField(m.ID, 256)
	if !m.Reasoning {
		return fmt.Errorf("thinking effort %q is not supported by model %q: the model does not support reasoning", value, id)
	}
	if m.SupportsEffort(value) {
		return nil
	}
	if len(m.ReasoningEfforts) == 0 {
		return fmt.Errorf("thinking effort %q is not supported by model %q: the model advertises no effort levels", value, id)
	}
	return fmt.Errorf("thinking effort %q is not supported by model %q; supported: %s",
		value, id, strings.Join(m.ReasoningEfforts, ", "))
}

// clearInheritedThinkingEffort applies the model-switch rule: an inherited
// effort the new model does not advertise is cleared with a visible warning.
func clearInheritedThinkingEffort(cfg *config, m model.CatalogModel, warn func(string)) {
	if cfg.ThinkingEffort == "" {
		return
	}
	if m.SupportsEffort(cfg.ThinkingEffort) {
		return
	}
	reason := "does not advertise it"
	switch {
	case !m.Reasoning:
		reason = "does not support reasoning"
	case len(m.ReasoningEfforts) == 0:
		reason = "advertises no effort levels"
	}
	value := cfg.ThinkingEffort
	cfg.ThinkingEffort = ""
	if warn != nil {
		warn(fmt.Sprintf("thinking effort %q cleared: model %s %s; using the provider default",
			value, catalogField(m.ID, 256), reason))
	}
}

// usableCatalogModel looks up id and rejects entries the harness cannot use.
func usableCatalogModel(cat model.Catalog, id string) (model.CatalogModel, error) {
	m, err := cat.Lookup(id)
	if err != nil {
		return model.CatalogModel{}, fmt.Errorf("%w: %q for provider %s", model.ErrModelNotFound, id, cat.Provider)
	}
	if m.Unavailable != "" {
		return model.CatalogModel{}, fmt.Errorf("model %q is not usable: %s", id, catalogField(m.Unavailable, 512))
	}
	return m, nil
}

// applyCatalogModel applies one discovered model to the effective
// configuration. An explicit context flag is clamped to the model window and
// remains the value used when the TUI switches models again.
func applyCatalogModel(cfg *config, m model.CatalogModel, warn func(string)) error {
	if m.Dialect != model.DialectChat && m.Dialect != model.DialectResponses {
		return fmt.Errorf("model %q has unsupported dialect %q", m.ID, m.Dialect)
	}
	if m.BaseURL == "" {
		return fmt.Errorf("model %q has no provider base URL", m.ID)
	}
	cfg.Model = m.ID
	cfg.Dialect = m.Dialect
	if !cfg.baseURLSet {
		cfg.BaseURL = m.BaseURL
	}
	contextWindow := m.ContextWindow
	if cfg.maxContextSet && cfg.maxContextValue < contextWindow {
		contextWindow = cfg.maxContextValue
	} else if cfg.maxContextSet && cfg.maxContextValue > m.ContextWindow && warn != nil {
		warn(fmt.Sprintf("max context tokens %d clamped to the %s model context %d",
			cfg.maxContextValue, m.ID, m.ContextWindow))
	}
	cfg.MaxContextTokens = contextWindow
	cfg.InputLimit = m.InputLimit
	return nil
}

// resolveSessionThinkingEffort applies the resume/fork precedence: the
// explicit setting wins, otherwise the latest recorded value is inherited.
// Only an explicit non-default value requires a catalog fetch when the model
// is unchanged; an explicit model change already loaded it.
func resolveSessionThinkingEffort(ctx context.Context, cfg *config, state agent.State, source model.CatalogSource, resolved *model.CatalogModel, warn func(string)) error {
	if cfg.thinkingEffortSet {
		if cfg.ThinkingEffort == "" {
			// Explicit "default": clear any recorded value without a fetch.
			return nil
		}
		if resolved == nil {
			if source == nil {
				return errors.New("model catalog is required to set a thinking effort")
			}
			loaded, err := source.Load(ctx, cfg.Provider)
			if err != nil {
				return err
			}
			warnCatalogTo(warn, loaded)
			m, err := usableCatalogModel(loaded, cfg.Model)
			if err != nil {
				return err
			}
			cfg.Pricing = m.Pricing(loaded.Source, loaded.FetchedAt)
			resolved = &m
		}
		return validateModelThinkingEffort(cfg.ThinkingEffort, *resolved)
	}
	cfg.ThinkingEffort = state.ThinkingEffort
	if resolved != nil {
		clearInheritedThinkingEffort(cfg, *resolved, warn)
	}
	return nil
}

// applySessionDefaults inherits model, workspace, base URL, and limits from
// the durable session for resume and fork. Explicit values win. An explicit
// model change is resolved through the catalog; otherwise the catalog is not
// consulted and the recorded replay values are authoritative.
func applySessionDefaults(ctx context.Context, cfg *config, state agent.State, source model.CatalogSource, warn func(string)) error {
	if warn == nil {
		warn = func(string) {}
	}
	if state.Provider != providerName {
		return fmt.Errorf("session provider %q is not supported; want %q", state.Provider, providerName)
	}
	if cfg.providerSet && cfg.Provider != state.Provider {
		return fmt.Errorf("provider %q does not match the session provider %q", cfg.Provider, state.Provider)
	}
	cfg.Provider = state.Provider
	if !agent.SupportsPrompt(state.PromptVersion) {
		return fmt.Errorf("session prompt version %q is not supported by this binary", state.PromptVersion)
	}
	if !agent.SupportsToolset(state.ToolsetVersion) {
		return fmt.Errorf("session toolset version %q is not supported by this binary", state.ToolsetVersion)
	}
	var (
		cat      model.Catalog
		resolved *model.CatalogModel
	)
	if cfg.modelSet {
		if source == nil {
			return errors.New("model catalog is required to change the session model")
		}
		var err error
		cat, err = source.Load(ctx, cfg.Provider)
		if err != nil {
			return err
		}
		warnCatalogTo(warn, cat)
		m, err := usableCatalogModel(cat, cfg.Model)
		if err != nil {
			return err
		}
		if err := applyCatalogModel(cfg, m, warn); err != nil {
			return err
		}
		cfg.Pricing = m.Pricing(cat.Source, cat.FetchedAt)
		resolved = &m
		if m.Deprecated {
			warn(fmt.Sprintf("model %s is deprecated", m.ID))
		}
	} else {
		cfg.Pricing = state.Pricing
		cfg.Model = state.Model
		if cfg.Model == "" {
			cfg.Model = state.InitialModel
		}
		if cfg.Model == "" {
			return errors.New("session does not record a model; set -model")
		}
		cfg.Dialect = model.Dialect(state.Dialect)
		if cfg.Dialect == "" {
			cfg.Dialect = model.Dialect(state.InitialDialect)
		}
		if cfg.Dialect != model.DialectChat && cfg.Dialect != model.DialectResponses {
			return fmt.Errorf("session records unsupported dialect %q; set -model", cfg.Dialect)
		}
		recordedContext := state.Limits.MaxContextTokens
		if recordedContext <= 0 {
			return errors.New("session does not record a context limit; set -model to resolve one")
		}
		contextWindow := recordedContext
		if cfg.maxContextSet {
			switch {
			case cfg.maxContextValue < contextWindow:
				contextWindow = cfg.maxContextValue
			case cfg.maxContextValue > contextWindow:
				warn(fmt.Sprintf("max context tokens %d clamped to the recorded session context %d",
					cfg.maxContextValue, recordedContext))
			}
		}
		cfg.MaxContextTokens = contextWindow
		// The recorded input cap already includes the old safety margin; the
		// effective cap is the smaller of it and the new margin-adjusted
		// context window.
		cfg.InputLimit = state.Limits.MaxInputTokens
	}
	if err := resolveSessionThinkingEffort(ctx, cfg, state, source, resolved, warn); err != nil {
		return err
	}
	if !cfg.workspaceSet {
		cfg.Workspace = state.Workspace
	}
	if cfg.Workspace == "" {
		return errors.New("session does not record a workspace; set -workspace")
	}
	if !cfg.baseURLSet && cfg.BaseURL == "" {
		cfg.BaseURL = state.BaseURL
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = model.DefaultBaseURL
	}
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return err
	}
	// Recorded run budgets are historical metadata. Only explicit flags
	// configure the new run; an omitted flag means unlimited.
	// An unset output flag inherits the recorded budget, including 0 (no
	// harness cap). An explicit 0 clears an inherited budget.
	if !cfg.maxOutputSet {
		cfg.MaxOutputTokens = state.Limits.MaxOutputTokens
	}
	cfg.PromptVersion = state.PromptVersion
	cfg.ToolsetVersion = state.ToolsetVersion
	return validateEffective(cfg)
}

// baseURLIsLoopback reports whether a resolved base URL points at a local
// test server, where a missing provider token is expected.
func baseURLIsLoopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
