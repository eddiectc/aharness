package tools

import (
	"net/url"
	"strings"
	"testing"
)

const articleFixture = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Deep Work Habits</title>
<style>.body { color: red } /* STYLE_MARKER */</style>
<script>var secret = "SCRIPT_MARKER";</script>
</head>
<body>
<header><a href="/">Site header HEADER_MARKER</a></header>
<nav>Home About NAV_MARKER</nav>
<main>
<article>
<h1>Deep Work Habits</h1>
<p>Focused work beats distracted work.</p>
<p>Short sessions with a clear goal produce more than long distracted ones.</p>
<pre><code>func main() { fmt.Println("CODE_MARKER") }</code></pre>
<table>
<tr><th>Phase</th><th>Hours</th></tr>
<tr><td>Focus</td><td>4</td></tr>
</table>
<form action="/submit"><input name="q" value="FORM_MARKER"><button>Submit FORM_MARKER</button></form>
<div hidden>HIDDEN_MARKER</div>
<div aria-hidden="true">ARIA_MARKER</div>
</article>
</main>
<aside>ASIDE_MARKER</aside>
<footer>FOOTER_MARKER</footer>
<script>document.write("SCRIPT_MARKER")</script>
</body>
</html>`

func fetchURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func TestExtractWebHTMLArticle(t *testing.T) {
	ext := extractWebHTML(articleFixture, fetchURL(t, "https://example.com/deep-work"))
	if ext.Text == "" {
		t.Fatal("extraction is empty")
	}
	if ext.Method != extractionTrafilatura {
		t.Fatalf("method = %q, want trafilatura", ext.Method)
	}
	for _, want := range []string{"Focused work beats distracted work.", "Phase", "Focus"} {
		if !strings.Contains(ext.Text, want) {
			t.Errorf("extracted text missing %q:\n%s", want, ext.Text)
		}
	}
	for _, unwanted := range []string{
		"STYLE_MARKER", "SCRIPT_MARKER", "HEADER_MARKER", "NAV_MARKER", "FORM_MARKER",
		"HIDDEN_MARKER", "ARIA_MARKER", "ASIDE_MARKER", "FOOTER_MARKER",
	} {
		if strings.Contains(ext.Text, unwanted) {
			t.Errorf("extracted text contains excluded %q:\n%s", unwanted, ext.Text)
		}
	}
	if ext.Title != "Deep Work Habits" {
		t.Errorf("title = %q, want Deep Work Habits", ext.Title)
	}
	if strings.Contains(ext.Text, "<") {
		t.Errorf("extracted text contains markup: %q", ext.Text)
	}
}

func TestExtractWebHTMLFallback(t *testing.T) {
	text := domText(articleFixture)
	if text == "" {
		t.Fatal("fallback extraction is empty")
	}
	for _, want := range []string{"Focused work beats distracted work.", "CODE_MARKER", "Phase", "Focus"} {
		if !strings.Contains(text, want) {
			t.Errorf("fallback text missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{
		"STYLE_MARKER", "SCRIPT_MARKER", "HEADER_MARKER", "NAV_MARKER", "FORM_MARKER",
		"HIDDEN_MARKER", "ARIA_MARKER", "ASIDE_MARKER", "FOOTER_MARKER",
	} {
		if strings.Contains(text, unwanted) {
			t.Errorf("fallback text contains excluded %q:\n%s", unwanted, text)
		}
	}
}

func TestExtractWebHTMLTitleFallbacks(t *testing.T) {
	document := `<html><head><meta property="og:title" content="OG Title"></head><body><p>Body text that is long enough to extract.</p></body></html>`
	ext := extractWebHTML(document, fetchURL(t, "https://example.com/"))
	if ext.Title != "OG Title" {
		t.Errorf("title = %q, want the og:title fallback", ext.Title)
	}
	if !strings.Contains(ext.Text, "Body text that is long enough to extract.") {
		t.Errorf("text = %q", ext.Text)
	}
}

func TestExtractWebHTMLAppShellAndChallenge(t *testing.T) {
	shell := `<!DOCTYPE html><html><head><title>Dashboard</title></head><body><div id="root"></div><script src="/a.js"></script><script src="/b.js"></script></body></html>`
	if got := detectWebBlockage(shell, true); got != "" {
		t.Errorf("app shell classified as %q before extraction", got)
	}
	if !looksLikeJSShell(shell) {
		t.Errorf("app shell not detected")
	}
	if text := extractWebHTML(shell, fetchURL(t, "https://example.com/")).Text; text != "" {
		t.Errorf("app shell text = %q, want empty", text)
	}

	challenge := `<html><head><title>Just a moment...</title></head><body><script src="/cf-challenge.js"></script></body></html>`
	if got := detectWebBlockage(challenge, true); got != unavailableChallenge {
		t.Errorf("challenge detection = %q, want %q", got, unavailableChallenge)
	}

	js := `<html><head><title>Loading</title></head><body><noscript>Please enable JavaScript to continue.</noscript></body></html>`
	if got := detectWebBlockage(js, true); got != unavailableJavaScript {
		t.Errorf("JavaScript detection = %q, want %q", got, unavailableJavaScript)
	}
}

func TestWebPageTextRunes(t *testing.T) {
	document := `<html><body><script>hidden()</script><style>.x{}</style><p>visible text</p></body></html>`
	if got := webPageTextRunes(document); got != len("visible text") {
		t.Errorf("webPageTextRunes = %d, want %d", got, len("visible text"))
	}
}

func TestSanitizeWebText(t *testing.T) {
	input := "line one\r\n\r\n\r\nline\ttwo\x00 \x1b[31mred\x1b[0m  \nend\n\n"
	got := sanitizeWebText(input)
	want := "line one\n\nline\ttwo red\nend"
	if got != want {
		t.Errorf("sanitizeWebText = %q, want %q", got, want)
	}
	if !strings.HasPrefix(sanitizeWebText("\xff\xfeok"), "\uFFFD") {
		t.Errorf("invalid UTF-8 was not replaced")
	}
}

func TestStripANSISequences(t *testing.T) {
	tests := map[string]string{
		"plain":                    "plain",
		"\x1b[31mred\x1b[0m":       "red",
		"a\x1b]0;title\x07b":       "ab",
		"a\x1b]8;;http://x\x1b\\b": "ab",
		"tail\x1b":                 "tail",
		"\x1b[1;32mok":             "ok",
	}
	for input, want := range tests {
		if got := stripANSISequences(input); got != want {
			t.Errorf("stripANSISequences(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSanitizeWebTitle(t *testing.T) {
	if got := sanitizeWebTitle("  Example\npage  "); got != "Example" {
		t.Errorf("sanitizeWebTitle = %q, want Example", got)
	}
	long := strings.Repeat("a", maxWebTitleRunes+50)
	if got := sanitizeWebTitle(long); len([]rune(got)) != maxWebTitleRunes {
		t.Errorf("sanitizeWebTitle length = %d, want %d", len([]rune(got)), maxWebTitleRunes)
	}
}
