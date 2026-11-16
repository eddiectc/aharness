package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/eddiectc/aharness/internal/model"
)

func TestZeroWindowSizeFallsBack(t *testing.T) {
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{},
		tea.WindowSizeMsg{Width: 0, Height: 24},
		tea.WindowSizeMsg{Width: 80, Height: 0},
	} {
		got, ok := filterTerminalMsg(nil, msg).(tea.WindowSizeMsg)
		if !ok || got.Width <= 0 || got.Height <= 0 {
			t.Fatalf("filterTerminalMsg(%+v) = %+v", msg, got)
		}
	}
	host := newManualHost()
	app := New(Options{Host: host, Model: "deepseek-v4.1-flash", Dialect: model.DialectChat, Headless: true})
	app.setSize(0, 0)
	if app.width != 80 || app.height != 24 {
		t.Fatalf("setSize(0,0) = %dx%d", app.width, app.height)
	}
}
