package tools

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	trafilatura "github.com/markusmobius/go-trafilatura/v2"
	"golang.org/x/net/html"
)

// Extraction labels reported in the result header and details.
const (
	extractionTrafilatura = "trafilatura"
	extractionFallback    = "fallback"
	extractionText        = "text"
	extractionMarkdown    = "markdown"
	extractionJSON        = "json"
	extractionXML         = "xml"
)

// Challenge and JavaScript-shell reasons for content_unavailable.
const (
	unavailableChallenge       = "challenge"
	unavailableJavaScript      = "javascript_required"
	unavailableEmpty           = "empty_extraction"
	unavailableEmptyResponse   = "empty_response"
	unavailableShortExtraction = "short_extraction"
)

// webExtraction is the plain-text extraction outcome for one HTML document.
type webExtraction struct {
	Text   string
	Title  string
	Method string
}

// challengeMarkers and javascriptMarkers are the documented, deliberately
// small detection set. They are matched against the first 4 KiB of the decoded
// document (which includes the <head> and title) and are not a classifier.
var (
	challengeMarkers = []string{
		"just a moment...",
		"attention required",
		"verify you are human",
		"are you a robot",
		"checking your browser",
		"cf-challenge",
		"g-recaptcha",
		"hcaptcha",
		"cf-turnstile",
		"access denied",
	}
	javascriptMarkers = []string{
		"enable javascript",
		"javascript is required",
		"javascript must be enabled",
		"you need to enable javascript",
		"requires javascript",
	}
	appShellMarkers = []string{
		`id="root"`,
		`id='root'`,
		`id="app"`,
		`id='app'`,
		`id="__next"`,
		`id='__next'`,
		`id="___gatsby"`,
		`id="react-root"`,
	}
)

// extractWebHTML turns a decoded HTML document into sanitized plain text. The
// document is first stripped of page chrome and hidden nodes, so no extractor
// output can contain excluded element text. The primary path is
// go-trafilatura; an empty or failed extraction runs the conservative DOM
// fallback. No model, summarizer, or classifier participates.
func extractWebHTML(document string, base *url.URL) webExtraction {
	doc, err := html.Parse(strings.NewReader(document))
	if err != nil || doc == nil {
		if text, title, ok := trafilaturaText(document, base); ok {
			return webExtraction{Text: text, Title: sanitizeWebTitle(title), Method: extractionTrafilatura}
		}
		return webExtraction{Method: extractionFallback}
	}
	title := domTitleFromNode(doc)
	removeWebChrome(doc)
	if cleaned, err := renderHTMLDocument(doc); err == nil {
		if text, metaTitle, ok := trafilaturaText(cleaned, base); ok {
			if metaTitle != "" {
				title = metaTitle
			}
			// A title-only extraction is a page shell, not article text.
			if !onlyWebTitle(text, title) {
				return webExtraction{Text: text, Title: sanitizeWebTitle(title), Method: extractionTrafilatura}
			}
		}
	}
	var b strings.Builder
	renderWebText(&b, doc, false)
	return webExtraction{Text: sanitizeWebText(b.String()), Title: sanitizeWebTitle(title), Method: extractionFallback}
}

// onlyWebTitle reports whether extracted text is just the document title, the
// usual artifact of an empty page shell.
func onlyWebTitle(text, title string) bool {
	if extractedTextRunes(text) > maxWebShortTextRunes {
		return false
	}
	title = sanitizeWebTitle(title)
	if title == "" {
		return false
	}
	return strings.Join(strings.Fields(sanitizeWebText(text)), " ") == title
}

// renderHTMLDocument serializes a parsed tree back to HTML for the extractor.
func renderHTMLDocument(doc *html.Node) (string, error) {
	var b strings.Builder
	if err := html.Render(&b, doc); err != nil {
		return "", err
	}
	return b.String(), nil
}

// trafilaturaText runs the primary extractor on an already-cleaned document.
// ok is false when it errors or yields no visible text, letting the caller fall
// back. ContentText is used because the extractor flattens ContentNode; the
// caller's pre-cleaning supplies the exclusion guarantee.
func trafilaturaText(document string, base *url.URL) (string, string, bool) {
	result, err := trafilatura.Extract(strings.NewReader(document), trafilatura.Options{
		OriginalURL:     base,
		InputEncoding:   "utf-8",
		EnableFallback:  false,
		ExcludeComments: true,
	})
	if err != nil || result == nil {
		return "", "", false
	}
	text := sanitizeWebText(result.ContentText)
	if text == "" {
		return "", "", false
	}
	return text, sanitizeWebTitle(result.Metadata.Title), true
}

// dropWebTags are removed from the document before extraction. Their content is
// never model-visible: scripts, styles, embedded objects, forms, and page
// chrome.
var dropWebTags = map[string]bool{
	"script":   true,
	"style":    true,
	"noscript": true,
	"template": true,
	"svg":      true,
	"canvas":   true,
	"iframe":   true,
	"object":   true,
	"embed":    true,
	"form":     true,
	"input":    true,
	"select":   true,
	"textarea": true,
	"button":   true,
	"nav":      true,
	"footer":   true,
	"header":   true,
	"aside":    true,
}

// skipWebTags adds the document head to dropWebTags for plain-text rendering.
// The head stays in the tree so the extractor can read its metadata.
var skipWebTags = func() map[string]bool {
	tags := make(map[string]bool, len(dropWebTags)+5)
	for tag := range dropWebTags {
		tags[tag] = true
	}
	for _, tag := range []string{"head", "title", "meta", "link", "base"} {
		tags[tag] = true
	}
	return tags
}()

// blockWebTags start a new line in the plain-text rendering.
var blockWebTags = map[string]bool{
	"address":    true,
	"article":    true,
	"blockquote": true,
	"body":       true,
	"caption":    true,
	"dd":         true,
	"div":        true,
	"dl":         true,
	"dt":         true,
	"fieldset":   true,
	"figcaption": true,
	"figure":     true,
	"h1":         true,
	"h2":         true,
	"h3":         true,
	"h4":         true,
	"h5":         true,
	"h6":         true,
	"hr":         true,
	"html":       true,
	"li":         true,
	"main":       true,
	"ol":         true,
	"p":          true,
	"pre":        true,
	"section":    true,
	"table":      true,
	"tbody":      true,
	"tfoot":      true,
	"thead":      true,
	"tr":         true,
	"ul":         true,
}

// domText renders the document body as block-aware plain text after removing
// page chrome. It is the conservative fallback when extraction yields nothing.
func domText(document string) string {
	doc, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return ""
	}
	removeWebChrome(doc)
	var b strings.Builder
	b.Grow(min(len(document), maxWebBodyBytes))
	renderWebText(&b, doc, false)
	return sanitizeWebText(b.String())
}

// removeWebChrome drops excluded elements and hidden nodes in one walk.
func removeWebChrome(n *html.Node) {
	for child := n.FirstChild; child != nil; {
		next := child.NextSibling
		if shouldDropWebNode(child) {
			n.RemoveChild(child)
		} else {
			removeWebChrome(child)
		}
		child = next
	}
}

func shouldDropWebNode(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	if dropWebTags[strings.ToLower(n.Data)] {
		return true
	}
	for _, attr := range n.Attr {
		if attr.Namespace != "" {
			continue
		}
		switch strings.ToLower(attr.Key) {
		case "hidden":
			return true
		case "aria-hidden":
			if strings.EqualFold(strings.TrimSpace(attr.Val), "true") {
				return true
			}
		}
	}
	return false
}

// renderWebText writes text nodes with block separators. Markup is never
// emitted. Tabs separate table cells; a <br> starts a new line. Indentation
// from the source document is dropped outside code blocks.
func renderWebText(b *strings.Builder, n *html.Node, pre bool) {
	r := &webTextRenderer{b: b, lineStart: true}
	r.write(n, pre)
}

type webTextRenderer struct {
	b         *strings.Builder
	lineStart bool
}

func (r *webTextRenderer) write(n *html.Node, pre bool) {
	switch n.Type {
	case html.TextNode:
		r.text(n.Data, pre)
	case html.ElementNode:
		tag := strings.ToLower(n.Data)
		if skipWebTags[tag] || shouldDropWebNode(n) {
			return
		}
		if tag == "br" {
			r.newline()
			return
		}
		block := blockWebTags[tag]
		if block && !pre {
			r.newline()
		}
		if tag == "pre" {
			pre = true
		}
		if tag == "td" || tag == "th" {
			r.cell()
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			r.write(child, pre)
		}
		if tag == "td" || tag == "th" {
			r.cell()
		}
		if tag == "pre" {
			pre = false
		}
		if block && !pre {
			r.newline()
		}
	case html.DocumentNode:
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			r.write(child, pre)
		}
	}
}

// text writes one text node, collapsing whitespace and dropping indentation
// outside code blocks.
func (r *webTextRenderer) text(s string, pre bool) {
	if pre {
		r.b.WriteString(s)
		r.lineStart = strings.HasSuffix(s, "\n")
		return
	}
	for _, c := range s {
		if unicode.IsSpace(c) {
			if r.lineStart {
				continue
			}
			r.b.WriteByte(' ')
			continue
		}
		r.b.WriteRune(c)
		r.lineStart = false
	}
}

// newline ends the current line without producing blank runs.
func (r *webTextRenderer) newline() {
	if r.b.Len() == 0 || r.lineStart {
		return
	}
	r.b.WriteByte('\n')
	r.lineStart = true
}

// cell separates table cells with a tab.
func (r *webTextRenderer) cell() {
	if !r.lineStart {
		r.b.WriteByte('\t')
	}
}

// domTitleFromNode finds the title in an already parsed tree.
func domTitleFromNode(doc *html.Node) string {
	if title := findElementText(doc, "title"); title != "" {
		return title
	}
	for _, meta := range []struct{ key, value string }{
		{"property", "og:title"},
		{"name", "twitter:title"},
		{"name", "og:title"},
	} {
		if content := findMetaContent(doc, meta.key, meta.value); content != "" {
			return content
		}
	}
	return ""
}

func findElementText(n *html.Node, tag string) string {
	if n.Type == html.ElementNode && strings.EqualFold(n.Data, tag) {
		return textContent(n)
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := findElementText(child, tag); found != "" {
			return found
		}
	}
	return ""
}

func findMetaContent(n *html.Node, key, value string) string {
	if n.Type == html.ElementNode && strings.EqualFold(n.Data, "meta") {
		var hasKey bool
		content := ""
		for _, attr := range n.Attr {
			switch strings.ToLower(attr.Key) {
			case strings.ToLower(key):
				hasKey = strings.EqualFold(strings.TrimSpace(attr.Val), value)
			case "content":
				content = attr.Val
			}
		}
		if hasKey {
			return content
		}
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := findMetaContent(child, key, value); found != "" {
			return found
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	var b strings.Builder
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		switch child.Type {
		case html.TextNode:
			b.WriteString(child.Data)
		case html.ElementNode:
			b.WriteString(textContent(child))
		}
	}
	return b.String()
}

// webPageTextRunes counts visible document text outside page chrome, used to
// decide whether a short extraction deserves a warning.
func webPageTextRunes(document string) int {
	doc, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return 0
	}
	count := 0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (skipWebTags[strings.ToLower(n.Data)] || shouldDropWebNode(n)) {
			return
		}
		if n.Type == html.TextNode {
			if text := strings.TrimSpace(n.Data); text != "" {
				count += utf8.RuneCountInString(text)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return count
}

// detectWebBlockage reports challenge or javascript_required when the document
// opens with a known bot challenge or JavaScript shell. A rendered provider
// response passes checkJavaScript=false: the provider already ran the page's
// scripts, so a leftover noscript hint is not a failure. An empty result means
// the page is not a known challenge; extraction decides the outcome.
func detectWebBlockage(document string, checkJavaScript bool) string {
	prefix := document
	if len(prefix) > maxWebChallengeBytes {
		prefix = prefix[:maxWebChallengeBytes]
	}
	prefix = strings.ToLower(prefix)
	for _, marker := range challengeMarkers {
		if strings.Contains(prefix, marker) {
			return unavailableChallenge
		}
	}
	if checkJavaScript {
		for _, marker := range javascriptMarkers {
			if strings.Contains(prefix, marker) {
				return unavailableJavaScript
			}
		}
	}
	return ""
}

// looksLikeJSShell reports an app-shell placeholder or a script-heavy document
// with little visible text. It is consulted only when extraction is empty.
func looksLikeJSShell(document string) bool {
	prefix := document
	if len(prefix) > maxWebChallengeBytes*16 {
		prefix = prefix[:maxWebChallengeBytes*16]
	}
	lower := strings.ToLower(prefix)
	for _, marker := range appShellMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return strings.Count(lower, "<script") >= 2 && webPageTextRunes(document) < maxWebShortTextRunes*2
}

// sanitizeWebTitle makes a title safe for one header line.
func sanitizeWebTitle(raw string) string {
	title := sanitizeWebText(raw)
	if index := strings.IndexByte(title, '\n'); index >= 0 {
		title = title[:index]
	}
	title = strings.Join(strings.Fields(title), " ")
	return truncateRunes(title, maxWebTitleRunes)
}

// sanitizeWebText makes fetched text safe for the transcript and log: valid
// UTF-8, LF line endings, no NUL or other controls except tab/newline, no
// terminal escape sequences, no trailing line whitespace, and no runs of blank
// lines.
func sanitizeWebText(s string) string {
	s = sanitizeUTF8(s)
	s = stripANSISequences(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if strings.IndexFunc(s, func(r rune) bool { return r == 0 || unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0 {
		s = strings.Map(func(r rune) rune {
			switch {
			case r == '\n' || r == '\t':
				return r
			case r == 0 || unicode.IsControl(r):
				return -1
			default:
				return r
			}
		}, s)
	}

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			if len(out) == 0 || blank {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, line)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// stripANSISequences removes ESC-initiated control sequences so a hostile page
// cannot inject terminal commands into the transcript.
func stripANSISequences(s string) string {
	if !strings.ContainsRune(s, '\x1b') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '\x1b' {
			b.WriteByte(s[i])
			i++
			continue
		}
		i++
		if i >= len(s) {
			break
		}
		switch s[i] {
		case '[': // CSI: parameter/intermediate bytes, then a final byte.
			i++
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			if i < len(s) {
				i++
			}
		case ']': // OSC: terminated by BEL or ST.
			i++
			for i < len(s) {
				if s[i] == 0x07 {
					i++
					break
				}
				if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
		default: // Two-byte escape sequence.
			i++
		}
	}
	return b.String()
}
