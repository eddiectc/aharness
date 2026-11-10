package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	defaultSearchResults = 100
	maxSearchResults     = 200
	maxSearchFileBytes   = 1 << 20
	maxSearchLineBytes   = 512
)

// searchArgs is the typed argument shape for search. It must agree with the
// search schema in schemas.go.
type searchArgs struct {
	Pattern       string  `json:"pattern"`
	Path          *string `json:"path,omitempty"`
	Glob          string  `json:"glob,omitempty"`
	CaseSensitive bool    `json:"case_sensitive,omitempty"`
	MaxResults    *int    `json:"max_results,omitempty"`
}

func (r *Registry) executeSearch(ctx context.Context, args json.RawMessage) Result {
	var a searchArgs
	if err := decodeArgs("search", args, &a); err != nil {
		return Failure(CodeInvalidArgs, err.Error())
	}
	if a.Pattern == "" {
		return invalidArgs("search", "pattern is required")
	}
	pathArg := "."
	if a.Path != nil {
		pathArg = *a.Path
		if pathArg == "" {
			return invalidArgs("search", "path must not be empty")
		}
	}
	maxResults := defaultSearchResults
	if a.MaxResults != nil {
		maxResults = *a.MaxResults
		if maxResults < 1 {
			return invalidArgs("search", "max_results must be at least 1")
		}
		if maxResults > maxSearchResults {
			maxResults = maxSearchResults
		}
	}
	if a.Glob != "" {
		if _, err := path.Match(a.Glob, ""); err != nil {
			return invalidArgs("search", "invalid glob %q: %v", a.Glob, err)
		}
	}
	expr := a.Pattern
	if !a.CaseSensitive {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return invalidArgs("search", "invalid pattern: %v", err)
	}

	rel, err := r.workspaceRel(pathArg)
	if err != nil {
		return errorResult(err)
	}
	if err := r.ws.checkResolved(rel); err != nil {
		return errorResult(err)
	}
	info, err := r.ws.root.Lstat(rel)
	if err != nil {
		return errorResult(fmt.Errorf("stat %s: %w", pathArg, err))
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return invalidArgs("search", "path %s is a symlink; search requires a real directory", pathArg)
	}
	if !info.IsDir() {
		return invalidArgs("search", "path %s is not a directory", pathArg)
	}

	s := &searcher{ws: r.ws, ctx: ctx, re: re, glob: a.Glob, max: maxResults}
	if err := s.walk(rel); err != nil {
		return errorResult(err)
	}
	sort.Strings(s.files)
	for _, file := range s.files {
		if err := s.ctx.Err(); err != nil {
			return errorResult(err)
		}
		if err := s.searchFile(file); err != nil {
			if cerr := s.ctx.Err(); cerr != nil {
				return errorResult(cerr)
			}
			continue
		}
		if s.done {
			break
		}
	}
	if len(s.results) == 0 {
		return Success(fmt.Sprintf("no matches for %q", a.Pattern))
	}
	lines := s.results
	if s.capped {
		lines = append(lines, fmt.Sprintf("[results capped at %d]", maxResults))
	}
	out, truncated := capOutput(strings.Join(lines, "\n")+"\n", fileOutputCap)
	return Result{OK: true, Code: CodeOK, Output: out, Truncated: truncated || s.capped}
}

type searcher struct {
	ws      *Workspace
	ctx     context.Context
	re      *regexp.Regexp
	glob    string
	max     int
	files   []string
	results []string
	capped  bool
	done    bool
}

func (s *searcher) walk(rel string) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	dir, err := s.ws.root.Open(rel)
	if err != nil {
		return fmt.Errorf("open directory %s: %w", rel, err)
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil {
		return fmt.Errorf("read directory %s: %w", rel, readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close directory %s: %w", rel, closeErr)
	}
	for _, entry := range entries {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if name == ".git" {
			continue
		}
		childRel := filepath.Join(rel, name)
		info, err := s.ws.root.Lstat(childRel)
		if err != nil {
			continue
		}
		mode := info.Mode()
		if mode&os.ModeSymlink != 0 {
			continue
		}
		if info.IsDir() {
			if err := s.walk(childRel); err != nil {
				if cerr := s.ctx.Err(); cerr != nil {
					return cerr
				}
				// An unreadable subtree is skipped; the rest of the search
				// remains useful.
				continue
			}
			continue
		}
		if !mode.IsRegular() || info.Size() > maxSearchFileBytes {
			continue
		}
		if s.glob != "" {
			matched, err := path.Match(s.glob, filepath.ToSlash(childRel))
			if err != nil {
				return fmt.Errorf("match glob %q: %w", s.glob, err)
			}
			if !matched {
				continue
			}
		}
		s.files = append(s.files, childRel)
	}
	return nil
}

func (s *searcher) searchFile(rel string) error {
	f, err := s.ws.root.Open(rel)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxSearchFileBytes+1))
	closeErr := f.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(data) > maxSearchFileBytes || bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return nil
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}

	display := strings.ToValidUTF8(filepath.ToSlash(rel), "\uFFFD")
	lineNo := 0
	lineStart := 0
	for i := 0; i <= len(data); i++ {
		if i < len(data) && data[i] != '\n' {
			continue
		}
		if i == len(data) && lineStart == i {
			break
		}
		lineNo++
		line := data[lineStart:i]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if s.re.Match(line) {
			s.add(fmt.Sprintf("%s:%d: %s", display, lineNo, clipLine(string(line))))
			if s.done {
				return nil
			}
		}
		lineStart = i + 1
	}
	return nil
}

func (s *searcher) add(line string) {
	if s.done {
		return
	}
	if len(s.results) >= s.max {
		s.capped = true
		s.done = true
		return
	}
	s.results = append(s.results, line)
}

// clipLine bounds one reported line so a single minified file cannot dominate
// the result.
func clipLine(line string) string {
	if len(line) <= maxSearchLineBytes {
		return line
	}
	cut := maxSearchLineBytes
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut] + "..."
}
