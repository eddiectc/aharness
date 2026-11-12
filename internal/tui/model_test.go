package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"aharness/internal/agent"
	"aharness/internal/model"
)

type selectingHost struct {
	*manualHost
	selected []string
	result   ModelChoice
	err      error
}

func (h *selectingHost) SelectModel(id string) (ModelChoice, error) {
	h.selected = append(h.selected, id)
	if h.err != nil {
		return ModelChoice{}, h.err
	}
	if h.result.ID != "" {
		return h.result, nil
	}
	for _, choice := range h.models {
		if choice.ID == id {
			return choice, nil
		}
	}
	return ModelChoice{}, errors.New("model is not in the catalog")
}

type warningHost struct {
	*selectingHost
	warning string
}

func (h *warningHost) TakeModelWarning() string {
	warning := h.warning
	h.warning = ""
	return warning
}

type refreshingHost struct {
	*manualHost

	mu       sync.Mutex
	calls    int
	choices  []ModelChoice
	err      error
	block    bool
	started  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func (h *refreshingHost) RefreshModels(ctx context.Context) ([]ModelChoice, error) {
	h.mu.Lock()
	h.calls++
	block, started, canceled := h.block, h.started, h.canceled
	choices, err := h.choices, h.err
	h.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block {
		<-ctx.Done()
		if canceled != nil {
			h.once.Do(func() { close(canceled) })
		}
		return nil, ctx.Err()
	}
	return choices, err
}

func (h *refreshingHost) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

func catalogChoice(id, name, status string, switchable bool) ModelChoice {
	return ModelChoice{
		ID: id, Name: name, Dialect: model.DialectChat,
		ContextWindow: 200_000, InputLimit: 160_000, OutputLimit: 8_192,
		Status: status, Switchable: switchable,
	}
}

func selectedModelID(t *testing.T, app *App) string {
	t.Helper()
	item, ok := app.overlay.model.list.SelectedItem().(modelItem)
	if !ok {
		return ""
	}
	return item.choice.ID
}

func TestModelPickerRowsShowCatalogMetadata(t *testing.T) {
	item := modelItem{
		current: true,
		choice: ModelChoice{
			ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash",
			Dialect: model.DialectResponses, ContextWindow: 200_000, InputLimit: 160_000,
			OutputLimit: 8_192, Status: "deprecated", Translated: true, Switchable: true,
			ReasoningEfforts: []string{"low", "high", "max"},
		},
	}
	if got, want := item.Title(), "DeepSeek V4.1 Flash (current)"; got != want {
		t.Fatalf("Title() = %q, want %q", got, want)
	}
	description := item.Description()
	for _, want := range []string{
		"deepseek-v4.1-flash", "openai-responses", "ctx 200000", "in 160000",
		"out 8192", "effort low/high/max", "deprecated", "translated",
	} {
		if !strings.Contains(description, want) {
			t.Errorf("Description() = %q, missing %q", description, want)
		}
	}
	filter := item.FilterValue()
	if !strings.Contains(filter, "deepseek-v4.1-flash") || !strings.Contains(filter, "DeepSeek V4.1 Flash") {
		t.Fatalf("FilterValue() = %q, want ID and name", filter)
	}

	fallback := modelItem{choice: ModelChoice{
		ID: "unnamed-model", Dialect: model.DialectChat, ContextWindow: 1, InputLimit: 1,
	}}
	if got, want := fallback.Title(), "unnamed-model"; got != want {
		t.Fatalf("fallback Title() = %q, want %q", got, want)
	}
	if !strings.Contains(fallback.Description(), "out -") {
		t.Fatalf("missing output ceiling placeholder: %q", fallback.Description())
	}
	if !strings.Contains(fallback.Description(), "effort -") {
		t.Fatalf("missing effort placeholder: %q", fallback.Description())
	}

	untrusted := modelItem{choice: ModelChoice{
		ID: "evil\x1b[31m", Name: "name\x1b]0;pwn\x07", Status: "beta",
	}}
	for _, got := range []string{untrusted.Title(), untrusted.Description(), untrusted.FilterValue()} {
		if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\x07') {
			t.Fatalf("catalog string was not sanitized: %q", got)
		}
	}
	if !strings.Contains(untrusted.Description(), "beta") {
		t.Fatalf("marker missing after sanitizing: %q", untrusted.Description())
	}
}

func TestModelPickerMarksCurrentAndSelectsDeprecated(t *testing.T) {
	host := newManualHost()
	host.info.Model = "alpha"
	host.info.Dialect = string(model.DialectChat)
	host.models = []ModelChoice{
		catalogChoice("alpha", "Alpha", "", true),
		catalogChoice("old-model", "Old Model", "deprecated", true),
	}
	app := newTestApp(t, host)
	press(t, app, "ctrl+p")
	if app.overlay.kind != OverlayModel {
		t.Fatalf("overlay = %q, want model", app.overlay.kind)
	}
	items := app.overlay.model.list.Items()
	if len(items) != 2 {
		t.Fatalf("picker items = %d, want 2", len(items))
	}
	current, ok := items[0].(modelItem)
	if !ok || !current.current || !strings.Contains(current.Title(), "(current)") {
		t.Fatalf("current entry = %+v", items[0])
	}
	if other := items[1].(modelItem); other.current {
		t.Fatalf("wrong current entry: %+v", other)
	}

	press(t, app, "down")
	press(t, app, "enter")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("deprecated selection kept the picker open: %q", app.overlay.kind)
	}
	if app.nextModel != "old-model" {
		t.Fatalf("nextModel = %q, want deprecated model", app.nextModel)
	}
}

func TestModelPickerSwitchShowsSelectorWarning(t *testing.T) {
	host := &warningHost{
		selectingHost: &selectingHost{manualHost: newManualHost()},
	}
	host.result = ModelChoice{
		ID: "kimi-k2.7-code", Dialect: model.DialectChat, ContextWindow: 262_144, Switchable: true,
	}
	app, _ := newTestAppWithEvents(t, host)
	// The app consumes one startup warning in New; a selection warning is
	// produced later by the host.
	host.warning = "max context tokens 500000 clamped to the selected model context 262144"
	press(t, app, "ctrl+p")
	press(t, app, "down")
	press(t, app, "enter")

	if !strings.Contains(app.notice, "clamped") {
		t.Fatalf("notice = %q, want the selector warning", app.notice)
	}
	if got := host.TakeModelWarning(); got != "" {
		t.Fatalf("warning was not consumed: %q", got)
	}
}

func TestModelPickerSwitchUsesSelector(t *testing.T) {
	host := &selectingHost{manualHost: newManualHost()}
	host.result = ModelChoice{
		ID: "muse-spark", Name: "Muse Spark", Dialect: model.DialectResponses,
		ContextWindow: 1_000_000, InputLimit: 900_000, Switchable: true,
	}
	app, events := newTestAppWithEvents(t, host)
	press(t, app, "ctrl+p")
	press(t, app, "down")
	press(t, app, "enter")

	if len(host.selected) != 1 || host.selected[0] != "crouton-alpha-free" {
		t.Fatalf("SelectModel calls = %v", host.selected)
	}
	if app.overlay.kind != OverlayNone {
		t.Fatalf("picker stayed open: %q", app.overlay.kind)
	}
	if app.nextModel != "muse-spark" || app.nextDialect != model.DialectResponses {
		t.Fatalf("next model = %q/%q, want the selector result", app.nextModel, app.nextDialect)
	}
	select {
	case event := <-events:
		changed, ok := event.(EventModelChanged)
		if !ok {
			t.Fatalf("event = %T, want EventModelChanged", event)
		}
		if changed.Model != "muse-spark" || changed.Dialect != string(model.DialectResponses) {
			t.Fatalf("event = %+v", changed)
		}
	default:
		t.Fatal("no model_changed event emitted")
	}
}

func TestModelPickerSwitchKeepsSupportedEffort(t *testing.T) {
	host := &selectingHost{manualHost: newManualHost()}
	host.info.ThinkingEffort = "high"
	host.result = ModelChoice{
		ID: "muse-spark", Name: "Muse Spark", Dialect: model.DialectResponses,
		ReasoningEfforts: []string{"low", "high", "max"}, Switchable: true,
	}
	app, events := newTestAppWithEvents(t, host)
	press(t, app, "ctrl+p")
	press(t, app, "down")
	press(t, app, "enter")
	if app.nextEffort != "high" {
		t.Fatalf("nextEffort = %q, want the supported inherited value", app.nextEffort)
	}
	if strings.Contains(app.notice, "cleared") {
		t.Fatalf("supported effort was cleared: %q", app.notice)
	}
	select {
	case event := <-events:
		changed, ok := event.(EventModelChanged)
		if !ok {
			t.Fatalf("event = %T, want EventModelChanged", event)
		}
		if changed.ThinkingEffort != "high" {
			t.Fatalf("model_changed effort = %q, want high", changed.ThinkingEffort)
		}
	default:
		t.Fatal("no model_changed event emitted")
	}
}

func TestModelPickerSwitchClearsUnsupportedEffort(t *testing.T) {
	host := &selectingHost{manualHost: newManualHost()}
	host.info.ThinkingEffort = "high"
	host.result = ModelChoice{
		ID: "muse-spark", Name: "Muse Spark", Dialect: model.DialectResponses,
		ReasoningEfforts: []string{"low"}, Switchable: true,
	}
	app, events := newTestAppWithEvents(t, host)
	press(t, app, "ctrl+p")
	press(t, app, "down")
	press(t, app, "enter")
	if app.nextEffort != "" {
		t.Fatalf("nextEffort = %q, want the provider default", app.nextEffort)
	}
	for _, want := range []string{"cleared", "high", "provider default"} {
		if !strings.Contains(app.notice, want) {
			t.Fatalf("notice = %q, missing %q", app.notice, want)
		}
	}
	var notice, changed *string
	var modelChanged EventModelChanged
	for len(events) > 0 {
		event := <-events
		switch event := event.(type) {
		case EventNotice:
			if event.Kind == "warning" {
				text := event.Text
				notice = &text
			}
		case EventModelChanged:
			modelChanged = event
			text := event.ThinkingEffort
			changed = &text
		}
	}
	if notice == nil || !strings.Contains(*notice, "cleared") {
		t.Fatalf("warning notice = %v, want the clearing notice", notice)
	}
	if changed == nil || modelChanged.ThinkingEffort != "" {
		t.Fatalf("model_changed effort = %q, want the provider default", modelChanged.ThinkingEffort)
	}
}

func TestStartupModelWarningConsumedOnce(t *testing.T) {
	host := &warningHost{
		selectingHost: &selectingHost{manualHost: newManualHost()},
		warning:       `thinking effort "high" cleared: model small does not advertise it; using the provider default`,
	}
	app, _ := newTestAppWithEvents(t, host)
	if got := host.TakeModelWarning(); got != "" {
		t.Fatalf("startup warning was not consumed: %q", got)
	}
	count := func() int {
		n := 0
		for _, item := range app.items {
			if item.kind == itemNotice && strings.Contains(item.text, "cleared") {
				n++
			}
		}
		return n
	}
	if count() != 1 {
		t.Fatalf("startup warning notices = %d, want 1", count())
	}
	host.result = ModelChoice{ID: "small", Dialect: model.DialectChat, Switchable: true}
	press(t, app, "ctrl+p")
	press(t, app, "down")
	press(t, app, "enter")
	if count() != 1 {
		t.Fatalf("startup warning repeated after a model selection: %d notices", count())
	}
}

func TestEffortPickerOpensSelectsAndReachesRunOptions(t *testing.T) {
	host := newManualHost()
	host.models[0].ReasoningEfforts = []string{"low", "high", "max"}
	app, events := newTestAppWithEvents(t, host)
	press(t, app, "f2")
	if app.overlay.kind != OverlayEffort {
		t.Fatalf("overlay = %q, want effort", app.overlay.kind)
	}
	items := app.overlay.effort.list.Items()
	if len(items) != 4 {
		t.Fatalf("effort rows = %d, want 4", len(items))
	}
	wantRows := []string{"provider default", "low", "high", "max"}
	for i, want := range wantRows {
		item, ok := items[i].(effortItem)
		if !ok || item.Title() != want {
			t.Fatalf("row %d = %+v, want %q", i, items[i], want)
		}
	}
	press(t, app, "down")
	press(t, app, "enter")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("selection did not close the picker: %q", app.overlay.kind)
	}
	if app.nextEffort != "low" {
		t.Fatalf("nextEffort = %q, want low", app.nextEffort)
	}
	if !strings.Contains(app.notice, "thinking effort for the next run: low") {
		t.Fatalf("notice = %q", app.notice)
	}
	select {
	case event := <-events:
		notice, ok := event.(EventNotice)
		if !ok || notice.Kind != "thinking_effort" || !strings.Contains(notice.Text, "low") {
			t.Fatalf("event = %+v, want a thinking_effort notice", event)
		}
	default:
		t.Fatal("no thinking_effort notice emitted")
	}
	typeText(t, app, "run with effort")
	press(t, app, "enter")
	_, call, ok := host.last()
	if !ok {
		t.Fatal("run did not start")
	}
	if call.opts.ThinkingEffort != "low" || !call.opts.ThinkingEffortSet {
		t.Fatalf("Start options = %+v, want low with the set flag", call.opts)
	}
}

func TestEffortPickerProviderDefaultSelection(t *testing.T) {
	host := newManualHost()
	host.info.ThinkingEffort = "high"
	host.models[0].ReasoningEfforts = []string{"low", "high", "max"}
	app, events := newTestAppWithEvents(t, host)
	press(t, app, "f2")
	// The current value is highlighted; move back to provider default.
	press(t, app, "up")
	press(t, app, "up")
	press(t, app, "enter")
	if app.nextEffort != "" {
		t.Fatalf("nextEffort = %q, want the provider default", app.nextEffort)
	}
	select {
	case event := <-events:
		notice, ok := event.(EventNotice)
		if !ok || notice.Kind != "thinking_effort" || !strings.Contains(notice.Text, "provider default") {
			t.Fatalf("event = %+v, want a provider-default notice", event)
		}
	default:
		t.Fatal("no thinking_effort notice emitted")
	}
	typeText(t, app, "run with default effort")
	press(t, app, "enter")
	_, call, ok := host.last()
	if !ok {
		t.Fatal("run did not start")
	}
	if call.opts.ThinkingEffort != "" || !call.opts.ThinkingEffortSet {
		t.Fatalf("Start options = %+v, want an explicit empty effort", call.opts)
	}
}

func TestEffortPickerNoValuesAndDisabledDuringRun(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	press(t, app, "f2")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("overlay = %q, want none", app.overlay.kind)
	}
	if !strings.Contains(app.notice, "advertises no effort levels") {
		t.Fatalf("notice = %q, want the no-effort notice", app.notice)
	}

	host.models[0].ReasoningEfforts = []string{"low"}
	run, _ := startTestRun(t, app, "task")
	press(t, app, "f2")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("effort picker opened during a run: %q", app.overlay.kind)
	}
	if !strings.Contains(app.notice, "disabled while a run is active") {
		t.Fatalf("notice = %q, want the disabled notice", app.notice)
	}
	run.done <- agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1}
	close(run.done)
	finishRun(t, app, agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1})
}

func TestEffortPickerRequiresModelMetadata(t *testing.T) {
	host := newManualHost()
	host.info.Model = "unknown-model"
	host.models = []ModelChoice{catalogChoice("other-model", "Other", "", true)}
	app := newTestApp(t, host)
	press(t, app, "f2")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("overlay = %q, want none", app.overlay.kind)
	}
	if !strings.Contains(app.notice, "unknown-model") || !strings.Contains(app.notice, "advertises no effort levels") {
		t.Fatalf("notice = %q", app.notice)
	}
}

func TestRunStartedTracksEffectiveEffort(t *testing.T) {
	host := newManualHost()
	app, events := newTestAppWithEvents(t, host)
	run, _ := startTestRun(t, app, "task")
	deliver(app, agent.RunStarted{
		Model: "muse-spark", Dialect: "openai-responses", ThinkingEffort: "max",
	})
	if app.nextEffort != "max" {
		t.Fatalf("nextEffort = %q, want the recorded run effort", app.nextEffort)
	}
	select {
	case event := <-events:
		started, ok := event.(EventRunStarted)
		if !ok {
			t.Fatalf("event = %T, want EventRunStarted", event)
		}
		if started.ThinkingEffort != "max" || started.Model != "muse-spark" {
			t.Fatalf("event = %+v", started)
		}
	default:
		t.Fatal("no run_started event emitted")
	}
	run.done <- agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1}
	close(run.done)
	finishRun(t, app, agent.Result{Status: agent.StopCompleted, CumulativeTurns: 1})
}

func TestHelpOverlayDocumentsF2(t *testing.T) {
	app := newTestApp(t, newManualHost())
	press(t, app, "f1")
	text := strings.Join(app.overlay.help, "\n")
	if !strings.Contains(text, "F2") || !strings.Contains(text, "thinking effort picker") {
		t.Fatalf("help does not document F2:\n%s", text)
	}
}

func TestModelPickerSwitchErrorKeepsPickerOpen(t *testing.T) {
	host := &selectingHost{manualHost: newManualHost(), err: errors.New("catalog is offline")}
	app, events := newTestAppWithEvents(t, host)
	before := app.nextModel
	press(t, app, "ctrl+p")
	press(t, app, "enter")

	if app.overlay.kind != OverlayModel {
		t.Fatalf("picker closed on selector error: %q", app.overlay.kind)
	}
	if app.nextModel != before {
		t.Fatalf("model changed on selector error: %q", app.nextModel)
	}
	if !strings.Contains(app.notice, "catalog is offline") {
		t.Fatalf("notice = %q", app.notice)
	}
	select {
	case event := <-events:
		t.Fatalf("unexpected event after selector error: %+v", event)
	default:
	}
}

func TestModelPickerRefreshReplacesEntriesAndPreservesFilter(t *testing.T) {
	host := &refreshingHost{manualHost: newManualHost()}
	host.models = []ModelChoice{
		catalogChoice("alpha-1", "Alpha One", "", true),
		catalogChoice("beta-2", "Beta Two", "", true),
		catalogChoice("gamma-3", "Gamma Three", "", true),
	}
	host.choices = append(append([]ModelChoice(nil), host.models...),
		catalogChoice("delta-4", "Delta Four", "", true))
	app, _ := newTestAppWithEvents(t, host)

	press(t, app, "ctrl+p")
	app.insertInput("beta")
	if got := selectedModelID(t, app); got != "beta-2" {
		t.Fatalf("filtered selection = %q, want beta-2", got)
	}

	cmd := press(t, app, "ctrl+r")
	if cmd == nil {
		t.Fatal("Ctrl-R did not start a refresh")
	}
	if !strings.Contains(app.overlay.model.view(), "refreshing models…") {
		t.Fatalf("refreshing footer missing:\n%s", app.overlay.model.view())
	}
	updateApp(t, app, cmd())

	if app.overlay.kind != OverlayModel {
		t.Fatalf("refresh closed the picker: %q", app.overlay.kind)
	}
	if got := app.overlay.model.list.FilterInput.Value(); got != "beta" {
		t.Fatalf("filter = %q, want preserved text", got)
	}
	if got := selectedModelID(t, app); got != "beta-2" {
		t.Fatalf("selection = %q, want preserved beta-2", got)
	}
	if len(app.models) != 4 || len(app.overlay.model.list.Items()) != 4 {
		t.Fatalf("models=%d items=%d, want 4", len(app.models), len(app.overlay.model.list.Items()))
	}
	if !strings.Contains(app.notice, "4") {
		t.Fatalf("notice = %q, want refreshed count", app.notice)
	}
	if strings.Contains(app.overlay.model.view(), "refreshing models…") {
		t.Fatal("refreshing footer survived the result")
	}
	if host.callCount() != 1 {
		t.Fatalf("RefreshModels calls = %d, want 1", host.callCount())
	}
}

func TestModelPickerRefreshFallsBackToFirstUsable(t *testing.T) {
	host := &refreshingHost{manualHost: newManualHost()}
	host.models = []ModelChoice{
		catalogChoice("alpha-1", "Alpha One", "", true),
		catalogChoice("beta-2", "Beta Two", "", true),
	}
	offline := catalogChoice("recorded", "Recorded", "", false)
	host.choices = []ModelChoice{
		offline,
		catalogChoice("alpha-1", "Alpha One", "", true),
		catalogChoice("gamma-3", "Gamma Three", "", true),
	}
	app, _ := newTestAppWithEvents(t, host)
	press(t, app, "ctrl+p")
	press(t, app, "down")
	if got := selectedModelID(t, app); got != "beta-2" {
		t.Fatalf("initial selection = %q", got)
	}
	cmd := press(t, app, "ctrl+r")
	updateApp(t, app, cmd())
	if got := selectedModelID(t, app); got != "alpha-1" {
		t.Fatalf("fallback selection = %q, want first switchable entry", got)
	}
}

func TestModelPickerRefreshFailureKeepsEntriesAndAllowsRetry(t *testing.T) {
	host := &refreshingHost{manualHost: newManualHost()}
	before := append([]ModelChoice(nil), host.models...)
	host.err = errors.New("catalog unavailable")
	host.choices = []ModelChoice{catalogChoice("new-model", "New Model", "", true)}
	app, _ := newTestAppWithEvents(t, host)
	press(t, app, "ctrl+p")
	selected := selectedModelID(t, app)

	cmd := press(t, app, "ctrl+r")
	updateApp(t, app, cmd())
	if app.overlay.kind != OverlayModel {
		t.Fatalf("failure closed the picker: %q", app.overlay.kind)
	}
	if len(app.models) != len(before) || len(app.overlay.model.list.Items()) != len(before) {
		t.Fatalf("failure replaced entries: models=%d items=%d", len(app.models), len(app.overlay.model.list.Items()))
	}
	if app.overlay.model.refreshing {
		t.Fatal("failure left the refresh flagged in flight")
	}
	if got := selectedModelID(t, app); got != selected {
		t.Fatalf("failure changed selection: %q -> %q", selected, got)
	}
	if !strings.Contains(app.notice, "catalog unavailable") {
		t.Fatalf("notice = %q", app.notice)
	}

	host.err = nil
	cmd = press(t, app, "ctrl+r")
	if cmd == nil {
		t.Fatal("retry after failure was not offered")
	}
	updateApp(t, app, cmd())
	if len(app.models) != 1 || len(app.overlay.model.list.Items()) != 1 {
		t.Fatalf("retry did not replace entries: models=%d", len(app.models))
	}
	if !strings.Contains(app.notice, "1") {
		t.Fatalf("retry notice = %q", app.notice)
	}
}

func TestModelPickerRefreshCacheWriteFailureUsesRefreshedEntries(t *testing.T) {
	host := &refreshingHost{manualHost: newManualHost()}
	host.err = model.ErrCatalogCache
	host.choices = []ModelChoice{catalogChoice("new-model", "New Model", "", true)}
	app, _ := newTestAppWithEvents(t, host)
	press(t, app, "ctrl+p")

	cmd := press(t, app, "ctrl+r")
	updateApp(t, app, cmd())
	if len(app.models) != 1 || app.models[0].ID != "new-model" {
		t.Fatalf("cache failure did not use refreshed entries: %+v", app.models)
	}
	if !strings.Contains(app.notice, "model catalog cache") {
		t.Fatalf("notice = %q, want the cache failure", app.notice)
	}
}

func TestModelPickerRefreshSecondCtrlRIgnoredWhileInFlight(t *testing.T) {
	host := &refreshingHost{
		manualHost: newManualHost(),
		block:      true,
		started:    make(chan struct{}, 1),
		canceled:   make(chan struct{}),
	}
	app, _ := newTestAppWithEvents(t, host)
	press(t, app, "ctrl+p")

	cmd := press(t, app, "ctrl+r")
	if cmd == nil {
		t.Fatal("Ctrl-R did not start a refresh")
	}
	results := make(chan tea.Msg, 1)
	go func() { results <- cmd() }()
	select {
	case <-host.started:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}

	if second := press(t, app, "ctrl+r"); second != nil {
		t.Fatal("second Ctrl-R started another refresh")
	}
	if got := host.callCount(); got != 1 {
		t.Fatalf("RefreshModels calls = %d, want 1", got)
	}
	if !strings.Contains(app.overlay.model.view(), "refreshing models…") {
		t.Fatal("refreshing footer missing while in flight")
	}

	press(t, app, "esc")
	select {
	case <-host.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the picker did not cancel the refresh")
	}
	updateApp(t, app, <-results)
}

func TestModelPickerCloseCancelsRefreshAndDiscardsLateResult(t *testing.T) {
	host := &refreshingHost{manualHost: newManualHost()}
	host.choices = []ModelChoice{catalogChoice("new-model", "New Model", "", true)}
	app, _ := newTestAppWithEvents(t, host)
	before := append([]ModelChoice(nil), app.models...)
	press(t, app, "ctrl+p")

	cmd := press(t, app, "ctrl+r")
	press(t, app, "esc")
	updateApp(t, app, cmd())

	if app.overlay.kind != OverlayNone {
		t.Fatalf("overlay = %q, want closed", app.overlay.kind)
	}
	if len(app.models) != len(before) {
		t.Fatalf("late result replaced models: got %d, want %d", len(app.models), len(before))
	}
	for i := range before {
		if app.models[i].ID != before[i].ID {
			t.Fatalf("late result changed model %d: %q", i, app.models[i].ID)
		}
	}
	if app.notice != "" {
		t.Fatalf("late result changed the notice: %q", app.notice)
	}
}

func TestOfflineRecordedModelCannotSwitchUntilRefresh(t *testing.T) {
	host := &refreshingHost{manualHost: newManualHost()}
	recorded := ModelChoice{
		ID: "deepseek-v4.1-flash", Name: "Recorded Model", Dialect: model.DialectChat,
		ContextWindow: 200_000, InputLimit: 160_000,
	}
	host.models = []ModelChoice{recorded}
	host.choices = []ModelChoice{
		catalogChoice("alpha-1", "Alpha One", "", true),
		{
			ID: "deepseek-v4.1-flash", Name: "DeepSeek", Dialect: model.DialectChat,
			ContextWindow: 200_000, InputLimit: 160_000, Switchable: true,
		},
	}
	app, _ := newTestAppWithEvents(t, host)

	press(t, app, "ctrl+p")
	if app.overlay.kind != OverlayModel {
		t.Fatalf("overlay = %q, want model", app.overlay.kind)
	}
	if !strings.Contains(app.notice, "model switching is unavailable until the catalog loads") {
		t.Fatalf("open notice = %q", app.notice)
	}
	press(t, app, "enter")
	if app.overlay.kind != OverlayModel {
		t.Fatalf("offline fallback switched models: overlay=%q", app.overlay.kind)
	}
	if app.nextModel != "deepseek-v4.1-flash" {
		t.Fatalf("offline fallback changed nextModel: %q", app.nextModel)
	}
	if !strings.Contains(app.notice, "catalog loads") || !strings.Contains(app.notice, "Ctrl-R") {
		t.Fatalf("notice = %q", app.notice)
	}

	cmd := press(t, app, "ctrl+r")
	if cmd == nil {
		t.Fatal("Ctrl-R did not retry the catalog")
	}
	updateApp(t, app, cmd())
	if len(app.models) != 2 {
		t.Fatalf("refresh did not expand the picker: %d models", len(app.models))
	}
	if app.overlay.kind != OverlayModel {
		t.Fatalf("refresh closed the picker: %q", app.overlay.kind)
	}
	// The retained recorded model is highlighted; move to the catalog model.
	press(t, app, "up")
	press(t, app, "enter")
	if app.overlay.kind != OverlayNone || app.nextModel != "alpha-1" {
		t.Fatalf("switching after refresh: overlay=%q next=%q", app.overlay.kind, app.nextModel)
	}
}

type detailsHost struct {
	*manualHost
	details Details
}

func (h *detailsHost) Details() Details { return h.details }

func TestDetailsOverlayOutputBudget(t *testing.T) {
	tests := []struct {
		name    string
		details Details
		want    string
		absent  string
	}{
		{
			name:    "unset",
			details: Details{Provider: "opencode-go", MaxContextTokens: 200_000, MaxOutputUnset: true},
			want:    "max output: unset",
		},
		{
			name:    "explicit",
			details: Details{Provider: "opencode-go", MaxContextTokens: 200_000, MaxOutputTokens: 8_192},
			want:    "max output: 8192",
			absent:  "max output: unset",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host := &detailsHost{manualHost: newManualHost(), details: test.details}
			app := newTestApp(t, host)
			press(t, app, "ctrl+x")
			if app.overlay.kind != OverlayDetails {
				t.Fatalf("overlay = %q, want details", app.overlay.kind)
			}
			text := strings.Join(app.overlay.help, "\n")
			if !strings.Contains(text, test.want) {
				t.Fatalf("details missing %q:\n%s", test.want, text)
			}
			if test.absent != "" && strings.Contains(text, test.absent) {
				t.Fatalf("details unexpectedly contain %q:\n%s", test.absent, text)
			}
		})
	}
}

func TestDetailsOverlayThinkingEffort(t *testing.T) {
	host := newManualHost()
	host.models[0].ReasoningEfforts = []string{"low", "high", "max"}
	app := newTestApp(t, host)
	press(t, app, "ctrl+x")
	text := strings.Join(app.overlay.help, "\n")
	if !strings.Contains(text, "thinking effort: provider default") {
		t.Fatalf("details missing the provider default effort:\n%s", text)
	}
	press(t, app, "esc")

	press(t, app, "f2")
	press(t, app, "down")
	press(t, app, "down")
	press(t, app, "enter")
	if app.nextEffort != "high" {
		t.Fatalf("nextEffort = %q, want high", app.nextEffort)
	}
	press(t, app, "ctrl+x")
	text = strings.Join(app.overlay.help, "\n")
	if !strings.Contains(text, "thinking effort: high") {
		t.Fatalf("details did not reflect the F2 selection:\n%s", text)
	}
}

func TestStatusLineUnlimitedBudgets(t *testing.T) {
	host := newManualHost()
	host.info.MaxTurns = 0
	host.info.MaxTotalTokens = 0
	host.info.Turns = 31
	host.info.Usage = model.Usage{InputTokens: 1_500_000, OutputTokens: 600_000}
	host.info.ContextTokens = 2_100_000
	app := newTestApp(t, host)
	line := app.statusLine()
	if !strings.Contains(line, "turns 31") || strings.Contains(line, "turns 31/") {
		t.Fatalf("status turns = %q, want an unlimited count", line)
	}
	// Tokens are context occupancy against the context window, never the
	// cumulative sum over every response.
	if !strings.Contains(line, "ctx 2.1M/200.0k") {
		t.Fatalf("status ctx = %q, want occupancy against the context window", line)
	}
}

func TestStatusLineExplicitBudgets(t *testing.T) {
	host := newManualHost()
	host.info.Turns = 31
	host.info.Usage = model.Usage{InputTokens: 1_500_000, OutputTokens: 600_000}
	host.info.ContextTokens = 123_456
	app := newTestApp(t, host)
	line := app.statusLine()
	if !strings.Contains(line, "turns 31/30") {
		t.Fatalf("status = %q, want the turn ratio", line)
	}
	if !strings.Contains(line, "ctx 123.5k/200.0k") {
		t.Fatalf("status = %q, want the context ratio", line)
	}
}

func TestStatusLineTracksALiveRun(t *testing.T) {
	host := newManualHost()
	host.info.Turns = 4
	host.info.ContextTokens = 10_000
	app := newTestApp(t, host)
	if line := app.statusLine(); !strings.Contains(line, "turns 4") || !strings.Contains(line, "ctx 10.0k/200.0k") {
		t.Fatalf("idle status = %q, want the session counters", line)
	}

	startTestRun(t, app, "live counters")
	// Mid-run counters come from the live run, not the values that are only
	// written when a run finishes.
	deliver(app, agent.ResponseCompleted{
		Turn: 5, Text: "working", FinishReason: model.FinishToolCalls,
		Usage: model.Usage{InputTokens: 30_000, OutputTokens: 500, Reported: true},
	})
	line := app.statusLine()
	if !strings.Contains(line, "turns 5") {
		t.Fatalf("live status = %q, want the incremented turn", line)
	}
	if !strings.Contains(line, "ctx 30.5k/200.0k") {
		t.Fatalf("live status = %q, want the live context occupancy", line)
	}

	// A second response moves both counters again, before the run ends.
	deliver(app, agent.ResponseCompleted{
		Turn: 6, Text: "done", FinishReason: model.FinishStop,
		Usage: model.Usage{InputTokens: 42_000, OutputTokens: 700, Reported: true},
	})
	line = app.statusLine()
	if !strings.Contains(line, "turns 6") || !strings.Contains(line, "ctx 42.7k/200.0k") {
		t.Fatalf("live status = %q, want the second response reflected", line)
	}
	if host.info.Turns != 4 {
		t.Fatalf("host info turns = %d, want the finished-run value to stay untouched", host.info.Turns)
	}

	run, _, _ := host.last()
	run.done <- agent.Result{
		Status: agent.StopCompleted, RunTurns: 2,
		CumulativeTurns: 6, ContextTokens: 42_700,
	}
	close(run.done)
	finishRun(t, app, agent.Result{
		Status: agent.StopCompleted, RunTurns: 2,
		CumulativeTurns: 6, ContextTokens: 42_700,
	})
	if line := app.statusLine(); !strings.Contains(line, "turns 6") || !strings.Contains(line, "ctx 42.7k/200.0k") {
		t.Fatalf("settled status = %q, want the run result", line)
	}
}

func TestStatusLineKeepsOccupancyWithoutReportedUsage(t *testing.T) {
	host := newManualHost()
	host.info.ContextTokens = 9_000
	app := newTestApp(t, host)
	startTestRun(t, app, "unreported usage")
	deliver(app, agent.ResponseCompleted{
		Turn: 1, Text: "no usage", FinishReason: model.FinishStop,
	})
	if line := app.statusLine(); !strings.Contains(line, "ctx 9.0k/200.0k") {
		t.Fatalf("status = %q, want the last known occupancy kept", line)
	}
}

func TestHeaderLineThinkingEffort(t *testing.T) {
	host := newManualHost()
	host.info.ThinkingEffort = "max"
	host.models[0].ReasoningEfforts = []string{"low", "high", "max"}
	app := newTestApp(t, host)
	if line := app.headerLine(); !strings.Contains(line, "effort max") {
		t.Fatalf("header = %q, want the inherited effort", line)
	}

	press(t, app, "f2")
	press(t, app, "up")
	press(t, app, "up")
	press(t, app, "enter")
	if line := app.headerLine(); !strings.Contains(line, "effort low") {
		t.Fatalf("header = %q, want the selected effort", line)
	}

	press(t, app, "f2")
	press(t, app, "up")
	press(t, app, "enter")
	if line := app.headerLine(); !strings.Contains(line, "effort default") {
		t.Fatalf("header = %q, want the provider default", line)
	}
}

func TestDetailsOverlayRunBudgets(t *testing.T) {
	t.Run("unlimited", func(t *testing.T) {
		host := newManualHost()
		host.info.MaxTurns = 0
		host.info.MaxTotalTokens = 0
		host.info.Turns = 31
		host.info.Usage = model.Usage{InputTokens: 1_500_000, OutputTokens: 600_000}
		details := &detailsHost{manualHost: host, details: Details{Provider: "opencode-go", MaxContextTokens: 200_000}}
		app := newTestApp(t, details)
		press(t, app, "ctrl+x")
		text := strings.Join(app.overlay.help, "\n")
		for _, want := range []string{
			"turns:      31",
			"tokens:     2.1M",
			"ctx:        0/200.0k",
			"max turns:  unlimited",
			"total tokens: unlimited",
			"run timeout:  none",
			"model timeout: none",
			"http timeout: none",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("details missing %q:\n%s", want, text)
			}
		}
		if strings.Contains(text, "31/0") || strings.Contains(text, "2.1M/0") {
			t.Fatalf("details contain a zero ratio:\n%s", text)
		}
	})

	t.Run("explicit", func(t *testing.T) {
		host := newManualHost()
		host.info.Turns = 31
		host.info.Usage = model.Usage{InputTokens: 1_500_000, OutputTokens: 600_000}
		details := &detailsHost{manualHost: host, details: Details{
			Provider:         "opencode-go",
			MaxContextTokens: 200_000,
			SessionTimeout:   13 * time.Minute,
			ModelTimeout:     42 * time.Second,
			HTTPTimeout:      15 * time.Minute,
		}}
		app := newTestApp(t, details)
		press(t, app, "ctrl+x")
		text := strings.Join(app.overlay.help, "\n")
		for _, want := range []string{
			"turns:      31/30",
			"tokens:     2.1M/2.0M",
			"ctx:        0/200.0k",
			"max turns:  30",
			"total tokens: 2.0M",
			"run timeout:  13m0s",
			"model timeout: 42s",
			"http timeout: 15m0s",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("details missing %q:\n%s", want, text)
			}
		}
	})
}

func TestCtrlROutsideModelPickerTogglesRendering(t *testing.T) {
	host := &refreshingHost{manualHost: newManualHost()}
	app, _ := newTestAppWithEvents(t, host)
	if !app.rich {
		t.Fatal("rich rendering is not the default")
	}
	press(t, app, "ctrl+r")
	if app.rich || !strings.Contains(app.notice, "raw") {
		t.Fatalf("rich=%v notice=%q", app.rich, app.notice)
	}
	press(t, app, "ctrl+r")
	if !app.rich || !strings.Contains(app.notice, "rich") {
		t.Fatalf("rich=%v notice=%q", app.rich, app.notice)
	}
	if got := host.callCount(); got != 0 {
		t.Fatalf("Ctrl-R outside the picker called RefreshModels %d times", got)
	}
}
