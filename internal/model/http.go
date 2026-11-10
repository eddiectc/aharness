package model

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// maxResponseBody is the largest provider response the adapters decode.
	maxResponseBody = 1 << 20
	// maxErrorBody is the largest provider error body retained for logs.
	maxErrorBody = 4 << 10
	// requestTimeout bounds one provider request, including body reads.
	requestTimeout = 5 * time.Minute
)

// httpTransport is the shared HTTP plumbing for the dialect adapters. Each
// adapter owns one instance and therefore one http.Client.
type httpTransport struct {
	client    *http.Client
	url       string
	token     string
	sessionID string
	userAgent string
}

// newHTTPTransport builds the request target and client for one adapter. An
// empty baseURL selects DefaultBaseURL; a trailing slash on the base URL is
// trimmed so the dialect path is appended exactly once.
func newHTTPTransport(baseURL, path, token, sessionID, version string) *httpTransport {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &httpTransport{
		client:    &http.Client{Timeout: requestTimeout},
		url:       strings.TrimRight(baseURL, "/") + path,
		token:     token,
		sessionID: sessionID,
		userAgent: "aharness/" + version,
	}
}

// post marshals payload, performs one POST, and returns the capped response
// body for dialect-specific decoding. HTTP and transport failures are
// normalized into the typed errors the retry policy consumes.
func (t *httpTransport) post(ctx context.Context, payload any) ([]byte, error) {
	requestBody, err := jsonv2.Marshal(payload)
	if err != nil {
		return nil, &ProtocolError{Err: fmt.Errorf("encode provider request: %w", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(requestBody))
	if err != nil {
		return nil, &TransportError{Err: fmt.Errorf("build provider request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	req.Header.Set("x-opencode-session", t.sessionID)
	req.Header.Set("User-Agent", t.userAgent)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &TransportError{Err: err}
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return nil, &TransportError{Err: fmt.Errorf("read provider response: %w", err)}
	}
	if len(body) > maxResponseBody {
		return nil, &ProtocolError{Err: fmt.Errorf("provider response exceeds %d bytes", maxResponseBody)}
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		httpErr := &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       redactBody(string(body), t.token),
		}
		if retryAfter, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
			httpErr.RetryAfter = retryAfter
			httpErr.RetryAfterSet = true
		}
		if isContextOverflow(string(body)) {
			return nil, &ContextOverflowError{Err: httpErr}
		}
		return nil, httpErr
	}

	return body, nil
}

// parseRetryAfter accepts delta-seconds or an HTTP-date and reports whether
// the header was present in a recognized form. A past date yields zero; the
// retry policy owns any upper bound.
func parseRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if when, err := http.ParseTime(value); err == nil {
		delay := time.Until(when)
		if delay < 0 {
			return 0, true
		}
		return delay, true
	}
	return 0, false
}

// isContextOverflow reports whether a provider error body names a context
// window rejection. Matching is case-insensitive because providers vary.
func isContextOverflow(body string) bool {
	lower := strings.ToLower(body)
	for _, marker := range [...]string{
		"context_length_exceeded",
		"context window",
		"maximum context length",
		"too many tokens",
		"prompt is too long",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// redactBody removes the provider token from an error body, sanitizes it to
// valid UTF-8, and caps it to maxErrorBody bytes. The truncation marker is
// included in the cap.
func redactBody(body, token string) string {
	if token != "" {
		body = strings.ReplaceAll(body, token, "[REDACTED]")
	}
	body = strings.ToValidUTF8(body, "\uFFFD")
	const marker = "…[truncated]"
	if len(body) <= maxErrorBody {
		return body
	}
	keep := maxErrorBody - len(marker)
	for keep > 0 && !utf8.RuneStart(body[keep]) {
		keep--
	}
	return body[:keep] + marker
}
