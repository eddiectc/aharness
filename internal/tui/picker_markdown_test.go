package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aharness/internal/agent"
	"aharness/internal/tools"
)

func TestFilePickerWorkspaceContainmentAndInsertion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("contents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "inner.txt"), []byte("inner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
		t.Logf("symlink not available: %v", err)
	}
	ws, err := tools.OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	host := newManualHost()
	host.ws = ws
	host.info.Workspace = dir
	app := newTestApp(t, host)

	press(t, app, "ctrl+o")
	if app.overlay.kind != OverlayFile {
		t.Fatalf("overlay = %q, want file", app.overlay.kind)
	}
	var names []string
	for _, item := range app.overlay.file.list.Items() {
		names = append(names, item.(fileItem).name)
	}
	joined := strings.Join(names, ",")
	if strings.Contains(joined, ".git") || strings.Contains(joined, "link.txt") {
		t.Fatalf("unsafe entries listed: %v", names)
	}
	// Filter to the workspace file and select it.
	app.insertInput("hello")
	press(t, app, "enter")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("picker did not close: %q", app.overlay.kind)
	}
	if got := app.composer.Value(); got != "hello.txt" {
		t.Fatalf("composer = %q, want workspace-relative path", got)
	}
	// Esc leaves the composer unchanged when reopened.
	app.clearComposer()
	press(t, app, "ctrl+o")
	press(t, app, "esc")
	if app.overlay.kind != OverlayNone || app.composer.Value() != "" {
		t.Fatalf("esc changed state: overlay=%q composer=%q", app.overlay.kind, app.composer.Value())
	}
}

func TestWorkspaceListSkipsGitAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := tools.OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	entries, err := ws.ListDisplayDir(".", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "a.txt" {
		t.Fatalf("entries = %+v", entries)
	}
	if _, err := ws.ListDisplayDir("..", 100); err == nil {
		t.Fatal("parent directory listed")
	}
	if _, err := ws.ListDisplayDir(".git", 100); err == nil {
		t.Fatal(".git listed")
	}
	if _, err := ws.ReadDisplayFile("../x", 10); err == nil {
		t.Fatal("outside file read")
	}
}

func TestMarkdownRenderingAndFallbacks(t *testing.T) {
	source := "# Heading\n\n**bold** and *em* and `code`\n\n- one\n- two\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n[link](https://example.com)\n"
	rendered, table, err := renderMarkdown(source, 50, true, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Heading", "bold", "em", "code", "one", "two", "link"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered markdown missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "\x1b]8;") {
		t.Fatal("rendered markdown contains OSC 8 hyperlink")
	}
	if table == nil || len(table.headers) != 2 || len(table.rows) != 1 {
		t.Fatalf("table = %+v", table)
	}
	raw, _, err := renderMarkdown(source, 50, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "# Heading") {
		t.Fatalf("raw mode did not preserve markdown source: %q", raw)
	}

	deep := strings.Repeat("> ", 20) + "deep"
	plain, _, err := renderMarkdown(deep, 40, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "nesting exceeds") {
		t.Fatalf("over-deep markdown did not fall back: %q", plain)
	}

	html := "<script>alert(1)</script>\n\nhello"
	out, _, err := renderMarkdown(html, 40, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(out, 0x1b) && strings.Contains(out, "]8;") {
		t.Fatalf("raw HTML produced a terminal hyperlink: %q", out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("raw HTML broke following text: %q", out)
	}
}

func TestMarkdownImageSyntaxIsLeftToImageBlocks(t *testing.T) {
	rendered, _, err := renderMarkdown("before ![chart](chart.png) after", 60, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, "chart.png") {
		t.Fatalf("glamour rendered the image path as text: %q", rendered)
	}
	if !strings.Contains(rendered, "chart") || !strings.Contains(rendered, "before") {
		t.Fatalf("alt text was lost: %q", rendered)
	}
}

func TestTableParsingCaps(t *testing.T) {
	var b strings.Builder
	b.WriteString("| h1 | h2 | h3 |\n|---|---|---|\n")
	for range 600 {
		b.WriteString("| a | b | c |\n")
	}
	table := parseLastTable(b.String())
	if table == nil || len(table.rows) != maxTableRows {
		t.Fatalf("rows = %v", len(table.rows))
	}
	wide := "| " + strings.Repeat("| x ", 20) + "|\n|---" + strings.Repeat("|---", 20) + "|\n"
	table = parseLastTable(wide)
	if table == nil || len(table.headers) > maxTableColumns {
		t.Fatalf("headers = %d, want <= %d", len(table.headers), maxTableColumns)
	}
}

func TestOverlayStackRestoresPreviousAndCaps(t *testing.T) {
	host := newManualHost()
	host.history = []agent.HistoryItem{{Kind: agent.HistoryAssistant, Text: "| a |\n|---|\n| 1 |\n"}}
	app := newTestApp(t, host)
	press(t, app, "f1")
	press(t, app, "ctrl+t")
	if app.overlay.kind != OverlayTable {
		t.Fatalf("overlay = %q", app.overlay.kind)
	}
	press(t, app, "esc")
	if app.overlay.kind != OverlayHelp {
		t.Fatalf("overlay after one esc = %q, want help", app.overlay.kind)
	}
	press(t, app, "esc")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("overlay after second esc = %q", app.overlay.kind)
	}
}

func FuzzRenderMarkdown(f *testing.F) {
	f.Add("# heading\n\n**bold**")
	f.Add("| a | b |\n|---|---|\n| 1 | 2 |")
	f.Add("<script>alert(1)</script>")
	f.Add(strings.Repeat("> ", 30) + "deep")
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 128<<10 {
			return
		}
		out, table, err := renderMarkdown(source, 40, true, false)
		if err != nil {
			return
		}
		if strings.Contains(out, "\x1b]8;") {
			t.Fatalf("OSC 8 in output: %q", out)
		}
		if table != nil && len(table.headers) > maxTableColumns {
			t.Fatalf("table headers = %d", len(table.headers))
		}
	})
}

func TestRichRawToggle(t *testing.T) {
	host := newManualHost()
	host.history = []agent.HistoryItem{{Kind: agent.HistoryAssistant, Text: "## Heading\n\n**bold**"}}
	app := newTestApp(t, host)
	app.ensureContent()
	rich := app.items[0].rendered
	if !app.rich || !strings.Contains(rich, "Heading") {
		t.Fatalf("rich render = %q", rich)
	}
	press(t, app, "ctrl+r")
	app.ensureContent()
	raw := app.items[0].rendered
	if app.rich {
		t.Fatal("rich flag still set")
	}
	if !strings.Contains(raw, "## Heading") || !strings.Contains(raw, "**bold**") {
		t.Fatalf("raw render = %q", raw)
	}
	press(t, app, "ctrl+r")
	app.ensureContent()
	if !app.rich {
		t.Fatal("rich flag not restored")
	}
}
