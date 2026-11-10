package tui

import (
	"strings"

	"charm.land/glamour/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	maxMarkdownBytes  = 64 << 10
	maxMarkdownDepth  = 16
	maxMarkdownBlocks = 4_000
	maxTableColumns   = 12
	maxTableRows      = 500
	maxTableColumnLen = 40
)

// tableData is one parsed GFM table for the table overlay.
type tableData struct {
	headers []string
	rows    [][]string
}

// renderMarkdown renders assistant markdown source. Raw mode returns sanitized
// text without parsing. Over-deep or oversized input falls back to plain text
// with a visible reason.
func renderMarkdown(source string, width int, rich, noColor bool) (string, *tableData, error) {
	source = Sanitize(source)
	table := parseLastTable(source)
	if !rich {
		return source, table, nil
	}
	if len(source) > maxMarkdownBytes {
		return source + "\n[markdown not rendered: source exceeds 64 KiB]", table, nil
	}
	if depth := markdownDepth(source); depth > maxMarkdownDepth {
		return source + "\n[markdown not rendered: nesting exceeds 16 levels]", table, nil
	}
	if blocks := markdownBlocks(source); blocks > maxMarkdownBlocks {
		return source + "\n[markdown not rendered: too many blocks]", table, nil
	}
	style := "dark"
	if noColor {
		style = "notty"
	}
	// Image links are rendered by the image block; replace them with alt text
	// so Glamour does not emit an OSC 8 image placeholder.
	source = imageLinkRE.ReplaceAllString(source, "$1")
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(max(10, width)),
		glamour.WithTableWrap(true),
	)
	if err != nil {
		return "", table, err
	}
	rendered, err := r.Render(source)
	if err != nil {
		return "", table, err
	}
	// The TUI deliberately never emits OSC 8 hyperlinks or other OSC
	// sequences from the renderer.
	return strings.TrimLeft(stripOSC(rendered), "\n"), table, nil
}

// stripOSC removes OSC strings (including OSC 8 hyperlinks) while preserving
// SGR styling. The input is trusted library output, not untrusted text.
func stripOSC(s string) string {
	if !strings.Contains(s, "\x1b]") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == ']' {
			j := i + 2
			for j < len(s) {
				if s[j] == 0x07 {
					j++
					break
				}
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// markdownDepth approximates block nesting from quote markers and leading
// indentation so pathological input falls back instead of exploding.
func markdownDepth(source string) int {
	maxDepth := 0
	inFence := false
	for line := range strings.SplitSeq(source, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		depth := indent / 2
		for len(trimmed) > 0 && trimmed[0] == '>' {
			depth++
			trimmed = strings.TrimLeft(trimmed[1:], " ")
		}
		maxDepth = max(maxDepth, depth)
	}
	return maxDepth
}

// markdownBlocks approximates the number of block elements without a second
// markdown parser. It bounds pathological input before Glamour sees it.
func markdownBlocks(source string) int {
	blocks := 0
	inFence := false
	lastBlank := true
	for line := range strings.SplitSeq(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			blocks++
			inFence = !inFence
			lastBlank = false
			continue
		}
		if inFence {
			continue
		}
		if trimmed == "" {
			lastBlank = true
			continue
		}
		if lastBlank || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ">") ||
			strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") ||
			strings.HasPrefix(trimmed, "+ ") || isOrderedList(trimmed) || strings.HasPrefix(trimmed, "|") {
			blocks++
		}
		lastBlank = false
	}
	return blocks
}

func isOrderedList(line string) bool {
	for i := 0; i < len(line); i++ {
		if line[i] < '0' || line[i] > '9' {
			return i > 0 && i+1 < len(line) && line[i] == '.' && line[i+1] == ' '
		}
	}
	return false
}

// parseLastTable returns the last GFM table in source, capped for display.
func parseLastTable(source string) *tableData {
	lines := strings.Split(source, "\n")
	var last *tableData
	for i := 0; i+1 < len(lines); i++ {
		if !strings.Contains(lines[i], "|") || !isTableSeparator(lines[i+1]) {
			continue
		}
		headers := splitTableRow(lines[i])
		if len(headers) == 0 {
			continue
		}
		rows := make([][]string, 0, 8)
		for j := i + 2; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "" || !strings.Contains(lines[j], "|") {
				break
			}
			if isTableSeparator(lines[j]) {
				continue
			}
			rows = append(rows, splitTableRow(lines[j]))
			if len(rows) >= maxTableRows {
				break
			}
		}
		last = &tableData{headers: clampColumns(headers), rows: rows}
		i = i + 1 + len(rows)
	}
	return last
}

func isTableSeparator(line string) bool {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	if line == "" {
		return false
	}
	cells := strings.SplitSeq(line, "|")
	for cell := range cells {
		cell = strings.TrimSpace(cell)
		cell = strings.TrimPrefix(cell, ":")
		cell = strings.TrimSuffix(cell, ":")
		if len(cell) < 3 || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
}

func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cell := SanitizeLine(strings.TrimSpace(part))
		cell = strings.TrimPrefix(cell, "`")
		cell = strings.TrimSuffix(cell, "`")
		cells = append(cells, cell)
	}
	return cells
}

func clampColumns(cells []string) []string {
	if len(cells) > maxTableColumns {
		cells = cells[:maxTableColumns]
	}
	out := make([]string, len(cells))
	for i, cell := range cells {
		out[i] = truncateDisplay(cell, maxTableColumnLen)
	}
	return out
}

func truncateDisplay(s string, width int) string {
	return ansi.Truncate(s, width, "…")
}
