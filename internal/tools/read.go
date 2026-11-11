package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultReadLimit = 2000
	maxReadLimit     = 4000
	maxReadFileBytes = 2 << 20
)

// readFileArgs is the typed argument shape for read_file. It must agree with
// the read_file schema in schemas.go.
type readFileArgs struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
}

func (r *Registry) executeRead(ctx context.Context, args json.RawMessage) Result {
	if err := ctx.Err(); err != nil {
		return errorResult(err)
	}
	var a readFileArgs
	if err := decodeArgs("read_file", args, &a); err != nil {
		return Failure(CodeInvalidArgs, err.Error())
	}
	if a.Path == "" {
		return invalidArgs("read_file", "path is required")
	}
	offset := 1
	if a.Offset != nil {
		offset = *a.Offset
		if offset < 1 {
			return invalidArgs("read_file", "offset must be at least 1")
		}
	}
	limit := defaultReadLimit
	if a.Limit != nil {
		limit = *a.Limit
		if limit < 1 {
			return invalidArgs("read_file", "limit must be at least 1")
		}
		if limit > maxReadLimit {
			limit = maxReadLimit
		}
	}
	return r.readFile(a.Path, offset, limit)
}

func (r *Registry) readFile(path string, offset, limit int) Result {
	rel, err := r.workspaceRel(path)
	if err != nil {
		return errorResult(err)
	}
	if err := r.ws.checkResolved(rel); err != nil {
		return errorResult(err)
	}
	info, err := r.ws.root.Stat(rel)
	if err != nil {
		return errorResult(fmt.Errorf("stat %s: %w", path, err))
	}
	if !info.Mode().IsRegular() {
		return errorResult(fmt.Errorf("%w: %s", errNotRegular, path))
	}
	if info.Size() > maxReadFileBytes {
		return errorResult(fmt.Errorf("%w: %s is %d bytes (limit %d)", ErrTooLarge, path, info.Size(), maxReadFileBytes))
	}

	f, err := r.ws.root.Open(rel)
	if err != nil {
		return errorResult(fmt.Errorf("open %s: %w", path, err))
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxReadFileBytes+1))
	closeErr := f.Close()
	if readErr != nil {
		return errorResult(fmt.Errorf("read %s: %w", path, readErr))
	}
	if closeErr != nil {
		return errorResult(fmt.Errorf("close %s: %w", path, closeErr))
	}
	if len(data) > maxReadFileBytes {
		return errorResult(fmt.Errorf("%w: %s exceeds %d bytes", ErrTooLarge, path, maxReadFileBytes))
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return errorResult(fmt.Errorf("%w: %s", ErrBinaryFile, path))
	}

	total := countLines(data)
	if total == 0 || offset > total {
		return Success(readHeader(0, 0, total))
	}
	if limit < 1 {
		limit = 1
	}
	if limit > maxReadLimit {
		limit = maxReadLimit
	}
	end := offset + limit - 1
	if end > total {
		end = total
	}
	spans := lineSpans(data, offset, end)
	if len(spans) == 0 {
		return Success(readHeader(0, 0, total))
	}

	var body strings.Builder
	renderLines(&body, data, spans, offset)
	header := readHeader(offset, end, total)
	footer := readFooter(end, total)
	if len(header)+body.Len()+len(footer) <= fileOutputCap {
		return Result{OK: true, Code: CodeOK, Output: header + body.String() + footer}
	}

	// The byte cap stopped the window before the requested limit. Deliver the
	// largest run of whole lines that fits with the actual header and the
	// continuation footer; Truncated reports the byte loss.
	body.Reset()
	bodyLen := 0
	fit := -1
	for i, span := range spans {
		lineNo := offset + i
		bodyLen += len(strconv.Itoa(lineNo)) + 1 + (span.end - span.start) + 1
		candidateFooter := readFooter(lineNo, total)
		if len(readHeader(offset, lineNo, total))+bodyLen+len(candidateFooter) <= fileOutputCap {
			fit = i
		}
	}
	if fit < 0 {
		return partialReadResult(data, spans[0], offset, total)
	}
	renderLines(&body, data, spans[:fit+1], offset)
	last := offset + fit
	out := readHeader(offset, last, total) + body.String() + readFooter(last, total)
	return Result{OK: true, Code: CodeOK, Output: out, Truncated: true}
}

// partialReadResult delivers a rune-safe prefix of one line that cannot fit
// whole inside the result cap. The marker replaces the continuation footer:
// the line itself is incomplete, so an offset would be misleading.
func partialReadResult(data []byte, span lineSpan, lineNo, total int) Result {
	header := readHeader(lineNo, lineNo, total)
	prefix := strconv.Itoa(lineNo) + "\t"
	content := string(data[span.start:span.end])
	delivered := 0
	for range 8 {
		marker := partialLineMarker(lineNo, delivered)
		budget := fileOutputCap - len(header) - len(prefix) - len(marker)
		if budget < 0 {
			budget = 0
		}
		next := safeHeadLen(content, budget)
		if next == delivered {
			break
		}
		delivered = next
	}
	// Marker digit counts can move the budget across a boundary; shrink until
	// the rendered result actually fits the cap.
	for delivered > 0 {
		marker := partialLineMarker(lineNo, delivered)
		if len(header)+len(prefix)+delivered+len(marker) <= fileOutputCap {
			break
		}
		delivered = safeHeadLen(content, delivered-1)
	}
	marker := partialLineMarker(lineNo, delivered)
	out := header + prefix + content[:delivered] + marker
	return Result{OK: true, Code: CodeOK, Output: out, Truncated: true}
}

func partialLineMarker(line, delivered int) string {
	return fmt.Sprintf("\n[line %d truncated at %d bytes]\nuse search or run_command to inspect the rest of line %d\n",
		line, delivered, line)
}

// readHeader describes the delivered inclusive line window. A zero window is
// used only for an empty file or an offset past the end.
func readHeader(start, end, total int) string {
	return fmt.Sprintf("lines %d-%d of %d\n", start, end, total)
}

// readFooter names the next offset when the window ends before the file does.
func readFooter(end, total int) string {
	if end < total {
		return fmt.Sprintf("continue with offset=%d\n", end+1)
	}
	return ""
}

func renderLines(b *strings.Builder, data []byte, spans []lineSpan, offset int) {
	for i, span := range spans {
		b.WriteString(strconv.Itoa(offset + i))
		b.WriteByte('\t')
		b.Write(data[span.start:span.end])
		b.WriteByte('\n')
	}
}

type lineSpan struct {
	start int
	end   int
}

// lineSpans returns the inclusive line window [offset, end], where line
// numbers are 1-based and a trailing newline does not add a line.
func lineSpans(data []byte, offset, end int) []lineSpan {
	spans := make([]lineSpan, 0, end-offset+1)
	line := 0
	start := 0
	for i := 0; i <= len(data); i++ {
		if i < len(data) && data[i] != '\n' {
			continue
		}
		if i == len(data) && start == i {
			break
		}
		line++
		if line >= offset {
			spans = append(spans, lineSpan{start: start, end: i})
			if line >= end {
				break
			}
		}
		start = i + 1
	}
	return spans
}

// countLines counts lines split on '\n'; a trailing newline adds no line and
// an empty file has zero lines.
func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}
