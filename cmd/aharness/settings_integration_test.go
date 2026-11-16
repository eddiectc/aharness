//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBinaryUIJSONSettingsWritesConfig proves the shipped /settings overlay
// lists the config-file keys, persists display preferences, and edits a numeric
// run setting through the real config writer.
func TestBinaryUIJSONSettingsWritesConfig(t *testing.T) {
	t.Parallel()
	bin := buildHarness(t)
	catalog := startFakeModelsDev(t)
	extra := []string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}
	workspace := t.TempDir()
	stateDir := t.TempDir()
	configPath := filepath.Join(stateDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"ui_rich": true}`), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	h := startInteractiveHarness(t, bin, workspace, stateDir, "", extra,
		"-ui-json", "-ui-size", "100x30", "-model", "deepseek-v4.1-flash", "-config", configPath)
	h.waitFor(`"type":"frame"`)
	h.send(`{"v":1,"type":"text","text":"/settings"}`)
	h.send(`{"v":1,"type":"key","key":"enter"}`)
	h.waitFor("SETTING")
	h.waitFor(`"overlay":"settings"`)
	if text := lastFrameText(t, h.output()); !strings.Contains(text, "model") || !strings.Contains(text, "max turns") {
		t.Fatalf("settings frame lacks the config rows:\n%s", text)
	}

	// The display rows are last (before agent overrides): nav from the bottom.
	h.send(`{"v":1,"type":"key","key":"end"}`)
	h.send(`{"v":1,"type":"key","key":"up"}`) // ui_expand_all
	h.send(`{"v":1,"type":"key","key":"up"}`) // ui_rich
	h.send(`{"v":1,"type":"key","key":"enter"}`)
	h.waitFor("saved assistant rendering")
	h.send(`{"v":1,"type":"key","key":"down"}`)
	h.send(`{"v":1,"type":"key","key":"enter"}`)
	h.waitFor("saved block folding")

	// max_turns is the seventh row: home, then six downs.
	h.send(`{"v":1,"type":"key","key":"home"}`)
	for range 6 {
		h.send(`{"v":1,"type":"key","key":"down"}`)
	}
	h.send(`{"v":1,"type":"key","key":"enter"}`)
	h.waitFor("max turns: ")
	h.send(`{"v":1,"type":"key","key":"ctrl+u"}`)
	h.send(`{"v":1,"type":"text","text":"5"}`)
	h.send(`{"v":1,"type":"key","key":"enter"}`)
	h.waitFor("saved max turns")
	h.quit()

	loaded, err := loadFileConfig(configPath, true)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.UIRich == nil || *loaded.UIRich {
		t.Fatalf("ui_rich = %+v, want false", loaded.UIRich)
	}
	if loaded.UIExpandAll == nil || !*loaded.UIExpandAll {
		t.Fatalf("ui_expand_all = %+v, want true", loaded.UIExpandAll)
	}
	if loaded.MaxTurns == nil || *loaded.MaxTurns != 5 {
		t.Fatalf("max_turns = %+v, want 5", loaded.MaxTurns)
	}
}
