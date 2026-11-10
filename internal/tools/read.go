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
	defaultReadLimit = 100
	maxReadLimit     = 500
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
	start, end := pageBounds(total, offset, limit)
	var b strings.Builder
	fmt.Fprintf(&b, "lines %d-%d of %d\n", start, end, total)

	lineNo := 0
	lineStart := 0
	for i := 0; i <= len(data); i++ {
		if i < len(data) && data[i] != '\n' {
			continue
		}
		if i == len(data) && lineStart == i {
			// A trailing newline does not add a line, and an empty file has
			// zero lines.
			break
		}
		lineNo++
		if lineNo >= offset && lineNo < offset+limit {
			b.WriteString(strconv.Itoa(lineNo))
			b.WriteByte('\t')
			b.Write(data[lineStart:i])
			b.WriteByte('\n')
		}
		lineStart = i + 1
		if lineNo >= offset && lineNo >= offset+limit-1 {
			break
		}
	}

	out, truncated := capOutput(b.String(), fileOutputCap)
	return Result{OK: true, Code: CodeOK, Output: out, Truncated: truncated}
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

// pageBounds computes the inclusive 1-based line window for a page request.
func pageBounds(total, offset, limit int) (int, int) {
	if total == 0 || offset > total || limit < 1 {
		return 0, 0
	}
	end := offset + limit - 1
	if end > total {
		end = total
	}
	return offset, end
}
