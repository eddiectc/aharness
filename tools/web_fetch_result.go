package tools

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// redirectResult reports a stopped cross-host or downgrade redirect as
// successful data; the model decides whether to fetch the target explicitly.
func (t *webFetchTool) redirectResult(target *url.URL, res fetchResult, start time.Time) Result {
	finalURL := res.FinalURL
	if finalURL == "" {
		finalURL = target.String()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "URL: %s\n", finalURL)
	fmt.Fprintf(&b, "Provider: %s\n", t.provider)
	if res.Status != 0 {
		fmt.Fprintf(&b, "Status: %d\n", res.Status)
	}
	if ct := sanitizeWebText(res.ContentType); ct != "" {
		fmt.Fprintf(&b, "Content-Type: %s\n", ct)
	}
	fmt.Fprintf(&b, "Redirect: %s\n", sanitizeWebText(res.RedirectURL))
	b.WriteString("---\n")
	fmt.Fprintf(&b, "Redirect not followed (%s). Call web_fetch with url=%s to follow it explicitly.\n",
		redirectReason(target, res.RedirectURL), res.RedirectURL)

	details, err := marshalWebFetchDetails(webFetchDetails{
		Provider:    t.provider,
		FinalURL:    finalURL,
		Status:      res.Status,
		ContentType: res.ContentType,
		Redirect:    true,
		RedirectURL: res.RedirectURL,
	})
	if err != nil {
		return webFetchErrorResult(err, start)
	}
	return Result{
		OK:         true,
		Code:       CodeOK,
		Output:     b.String(),
		Details:    details,
		DurationMS: time.Since(start).Milliseconds(),
	}
}

// redirectReason names why the redirect was not followed.
func redirectReason(from *url.URL, to string) string {
	next, err := url.Parse(to)
	if err != nil {
		return "redirect target"
	}
	if from.Scheme == "https" && next.Scheme == "http" {
		return "HTTPS-to-HTTP downgrade"
	}
	return "cross-host redirect"
}

// textResult renders the header, the requested text page, and its details.
func (t *webFetchTool) textResult(res fetchResult, finalURL, text, title, extraction, warning string, offset int, start time.Time) Result {
	var b strings.Builder
	fmt.Fprintf(&b, "URL: %s\n", sanitizeWebText(finalURL))
	fmt.Fprintf(&b, "Provider: %s\n", t.provider)
	if res.Status != 0 {
		fmt.Fprintf(&b, "Status: %d\n", res.Status)
	}
	if ct := sanitizeWebText(res.ContentType); ct != "" {
		fmt.Fprintf(&b, "Content-Type: %s\n", ct)
	}
	if title != "" {
		fmt.Fprintf(&b, "Title: %s\n", title)
	}
	if extraction != "" {
		fmt.Fprintf(&b, "Extraction: %s\n", extraction)
	}
	fmt.Fprintf(&b, "Text-Bytes: %d\n", len(text))
	if warning != "" {
		fmt.Fprintf(&b, "Warning: %s\n", warning)
	}
	b.WriteString("---\n")

	details := webFetchDetails{
		Provider:    t.provider,
		FinalURL:    finalURL,
		Status:      res.Status,
		ContentType: res.ContentType,
		Title:       title,
		Extraction:  extraction,
		TextBytes:   len(text),
		Offset:      offset,
		Warning:     warning,
	}

	if offset >= len(text) {
		fmt.Fprintf(&b, "offset %d is at or beyond the end of the text (%d bytes); no text returned.\n", offset, len(text))
		encoded, err := marshalWebFetchDetails(details)
		if err != nil {
			return webFetchErrorResult(err, start)
		}
		return Result{OK: true, Code: CodeOK, Output: b.String(), Details: encoded, DurationMS: time.Since(start).Milliseconds()}
	}
	if offset > 0 && !utf8.RuneStart(text[offset]) {
		for offset < len(text) && !utf8.RuneStart(text[offset]) {
			offset++
		}
		details.Offset = offset
		if offset == len(text) {
			fmt.Fprintf(&b, "offset %d is at or beyond the end of the text (%d bytes); no text returned.\n", offset, len(text))
			encoded, err := marshalWebFetchDetails(details)
			if err != nil {
				return webFetchErrorResult(err, start)
			}
			return Result{OK: true, Code: CodeOK, Output: b.String(), Details: encoded, DurationMS: time.Since(start).Milliseconds()}
		}
	}

	page := text[offset:]
	truncated := false
	if len(page) > maxWebOutputBytes {
		page = page[:safeHeadLen(page, maxWebOutputBytes)]
		truncated = true
	}
	b.WriteString(page)
	if truncated {
		if !strings.HasSuffix(page, "\n") {
			b.WriteByte('\n')
		}
		next := offset + len(page)
		fmt.Fprintf(&b, "[truncated at %d bytes; continue with offset=%d]\n", maxWebOutputBytes, next)
		details.NextOffset = next
	}
	details.Truncated = truncated

	encoded, err := marshalWebFetchDetails(details)
	if err != nil {
		return webFetchErrorResult(err, start)
	}
	return Result{
		OK:         true,
		Code:       CodeOK,
		Output:     b.String(),
		Truncated:  truncated,
		Details:    encoded,
		DurationMS: time.Since(start).Milliseconds(),
	}
}

func marshalWebFetchDetails(details webFetchDetails) (json.RawMessage, error) {
	encoded, err := jsonv2.Marshal(details)
	if err != nil {
		return nil, webErrorf(CodeInternal, "web_fetch: encode details: %v", err)
	}
	return encoded, nil
}

// webFetchErrorResult maps a coded fetch failure or a context error to one
// result and stamps the elapsed time.
func webFetchErrorResult(err error, start time.Time) Result {
	elapsed := time.Since(start).Milliseconds()
	var we *webError
	if errors.As(err, &we) {
		res := Failure(we.code, sanitizeUTF8(we.msg))
		res.DurationMS = elapsed
		return res
	}
	res := errorResult(err)
	res.DurationMS = elapsed
	return res
}

// extractedTextRunes counts non-space runes in extracted text.
func extractedTextRunes(text string) int {
	count := 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		count++
	}
	return count
}

// textExtractionLabel names the pass-through extraction kind from the media
// type. The tool never synthesizes Markdown.
func textExtractionLabel(contentType string) string {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return extractionText
	}
	switch strings.ToLower(mediaType) {
	case "text/markdown":
		return extractionMarkdown
	case "application/json":
		return extractionJSON
	case "application/xml", "text/xml":
		return extractionXML
	default:
		return extractionText
	}
}
