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

// sessionCatalogSource constructs a source only when the caller explicitly
// changed the model; resume/fork without an explicit model stays offline.
func sessionCatalogSource(cfg config) (model.CatalogSource, error) {
	if !cfg.modelSet {
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
	_, _ = fmt.Fprintln(w, "ID\tNAME\tDIALECT\tCONTEXT\tOUTPUT\tSTATUS")
	for _, m := range models {
		output := "-"
		if m.OutputLimit > 0 {
			output = strconv.Itoa(m.OutputLimit)
		}
		status := m.Status
		if status == "" {
			status = "-"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n",
			catalogField(m.ID, 256), catalogField(m.Name, 200), m.Dialect,
			m.ContextWindow, output, catalogField(status, 32))
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
	if m.Deprecated {
		warnf(stderr, "model %s is deprecated", m.ID)
	}
	return validateEffective(cfg)
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

// applySessionDefaults inherits model, workspace, base URL, and limits from
// the durable session for resume and fork. Explicit values win. An explicit
// model change is resolved through the catalog; otherwise the catalog is not
// consulted and the recorded replay values are authoritative.
func applySessionDefaults(ctx context.Context, cfg *config, state agent.State, source model.CatalogSource, stderr io.Writer) error {
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
	if cfg.modelSet {
		if source == nil {
			return errors.New("model catalog is required to change the session model")
		}
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
		if m.Deprecated {
			warnf(stderr, "model %s is deprecated", m.ID)
		}
	} else {
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
				warnf(stderr, "max context tokens %d clamped to the recorded session context %d",
					cfg.maxContextValue, recordedContext)
			}
		}
		cfg.MaxContextTokens = contextWindow
		// The recorded input cap already includes the old safety margin; the
		// effective cap is the smaller of it and the new margin-adjusted
		// context window.
		cfg.InputLimit = state.Limits.MaxInputTokens
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
	if !cfg.maxTurnsSet && state.MaxTurns > 0 {
		cfg.MaxTurns = state.MaxTurns
	}
	if !cfg.maxTotalTokensSet && state.MaxTotalTokens > 0 {
		cfg.MaxTotalTokens = state.MaxTotalTokens
	}
	if !cfg.sessionTimeoutSet && state.SessionTimeout > 0 {
		cfg.SessionTimeout = state.SessionTimeout
	}
	if state.ModelTimeout > 0 {
		cfg.ModelTimeout = state.ModelTimeout
	}
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
