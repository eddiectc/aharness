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
		if err := validateGlob(a.Glob); err != nil {
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
		return s.render([]string{fmt.Sprintf("no matches for %q", a.Pattern)})
	}
	return s.render(s.results)
}

type searcher struct {
	ws             *Workspace
	ctx            context.Context
	re             *regexp.Regexp
	glob           string
	max            int
	files          []string
	results        []string
	capped         bool
	done           bool
	skippedTooBig  int
	skippedBinary  int
	skippedSymlink int
}

// render applies the head-only byte cap and appends the count-cap and skip
// trailers. Trailers are reserved before the byte cap so they stay visible.
// Only dropped bytes set Truncated; the result-count cap is its own marker.
func (s *searcher) render(lines []string) Result {
	var trailers []string
	if s.capped {
		trailers = append(trailers, fmt.Sprintf("[results capped at %d]", s.max))
	}
	if summary := s.skipSummary(); summary != "" {
		trailers = append(trailers, summary)
	}
	trailer := ""
	if len(trailers) > 0 {
		trailer = strings.Join(trailers, "\n") + "\n"
	}
	body := strings.Join(lines, "\n") + "\n"
	budget := fileOutputCap - len(trailer)
	if budget < 1 {
		budget = 1
	}
	out, truncated := capHeadOutput(body, budget)
	return Result{OK: true, Code: CodeOK, Output: out + trailer, Truncated: truncated}
}

func (s *searcher) skipSummary() string {
	parts := make([]string, 0, 3)
	if s.skippedTooBig > 0 {
		parts = append(parts, fmt.Sprintf("%d over 1 MiB", s.skippedTooBig))
	}
	if s.skippedBinary > 0 {
		parts = append(parts, fmt.Sprintf("%d binary", s.skippedBinary))
	}
	if s.skippedSymlink > 0 {
		parts = append(parts, fmt.Sprintf("%d symlink", s.skippedSymlink))
	}
	if len(parts) == 0 {
		return ""
	}
	total := s.skippedTooBig + s.skippedBinary + s.skippedSymlink
	return fmt.Sprintf("[skipped %d files: %s]", total, strings.Join(parts, ", "))
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
			s.skippedSymlink++
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
		if !mode.IsRegular() {
			continue
		}
		if s.glob != "" {
			matched, err := matchGlob(s.glob, filepath.ToSlash(childRel))
			if err != nil {
				return fmt.Errorf("match glob %q: %w", s.glob, err)
			}
			if !matched {
				continue
			}
		}
		if info.Size() > maxSearchFileBytes {
			s.skippedTooBig++
			continue
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
	if len(data) > maxSearchFileBytes {
		s.skippedTooBig++
		return nil
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		s.skippedBinary++
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

// validateGlob checks every path segment. A standalone ** segment is the
// recursive wildcard; all other segments use path.Match syntax.
func validateGlob(pattern string) error {
	for _, segment := range strings.Split(pattern, "/") {
		if segment == "**" {
			continue
		}
		if _, err := path.Match(segment, ""); err != nil {
			return err
		}
	}
	return nil
}

// matchGlob matches a workspace-relative slash path. ** matches zero or more
// whole path segments; * and ? do not cross a '/'. Matching is dynamic over
// segments so repeated ** patterns cannot blow up recursively.
func matchGlob(pattern, name string) (bool, error) {
	patternSegments := strings.Split(pattern, "/")
	nameSegments := strings.Split(name, "/")
	ok := make([][]bool, len(patternSegments)+1)
	for i := range ok {
		ok[i] = make([]bool, len(nameSegments)+1)
	}
	ok[len(patternSegments)][len(nameSegments)] = true
	for i := len(patternSegments) - 1; i >= 0; i-- {
		for j := len(nameSegments); j >= 0; j-- {
			if patternSegments[i] == "**" {
				ok[i][j] = ok[i+1][j] || (j < len(nameSegments) && ok[i][j+1])
				continue
			}
			if j == len(nameSegments) {
				continue
			}
			matched, err := path.Match(patternSegments[i], nameSegments[j])
			if err != nil {
				return false, err
			}
			ok[i][j] = matched && ok[i+1][j+1]
		}
	}
	return ok[0][0], nil
}
