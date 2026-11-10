package model

import (
	"bufio"
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// maxResponseBody is the largest non-streaming provider response the
	// adapters decode.
	maxResponseBody = 1 << 20
	// maxErrorBody is the largest provider error body retained for logs.
	maxErrorBody = 4 << 10
	// requestTimeout bounds one provider request, including body reads.
	requestTimeout = 5 * time.Minute
	// maxSSEEvent bounds one SSE event's data. A terminal Responses event
	// wraps the full response in JSON.
	maxSSEEvent = 4 << 20
	// maxStreamBytes bounds the total raw bytes read from one SSE stream.
	maxStreamBytes = 8 << 20
	// maxPartialText bounds diagnostics from a failed stream.
	maxPartialText = 64 << 10
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
	resp, err := t.do(ctx, payload)
	if err != nil {
		return nil, err
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
		return nil, t.responseError(resp, body)
	}
	return body, nil
}

// postStream performs one POST and returns the open response for SSE
// consumption. A non-2xx response is read, capped, redacted, and converted to a
// typed error. The caller closes the body.
func (t *httpTransport) postStream(ctx context.Context, payload any) (*http.Response, error) {
	resp, err := t.do(ctx, payload)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody+1))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, &TransportError{Err: fmt.Errorf("read provider error response: %w", readErr)}
		}
		if len(body) > maxErrorBody {
			body = body[:maxErrorBody]
		}
		return nil, t.responseError(resp, body)
	}
	return resp, nil
}

func (t *httpTransport) do(ctx context.Context, payload any) (*http.Response, error) {
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
	return resp, nil
}

// responseError converts a non-2xx response and its capped body into a typed
// error. The body is redacted and capped before it can reach a log.
func (t *httpTransport) responseError(resp *http.Response, body []byte) error {
	text := redactBody(string(body), t.token)
	httpErr := &HTTPError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       text,
	}
	if retryAfter, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
		httpErr.RetryAfter = retryAfter
		httpErr.RetryAfterSet = true
	}
	if isContextOverflow(text) {
		return &ContextOverflowError{Err: httpErr}
	}
	return httpErr
}

// readBodyCapped reads and closes a non-SSE 2xx response body.
func readBodyCapped(resp *http.Response) ([]byte, error) {
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
	return body, nil
}

// isEventStream reports whether a 2xx response carries SSE.
func isEventStream(header string) bool {
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return false
	}
	return strings.EqualFold(mediaType, "text/event-stream")
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

// sseEvent is one dispatched server-sent event.
type sseEvent struct {
	Name string
	Data []byte
}

// sseDecoder reads SSE events with event and total-stream caps. It tolerates
// comments, CRLF, multi-line data, and a final event without a trailing blank
// line.
type sseDecoder struct {
	br    *bufio.Reader
	total int64
	done  bool
}

func newSSEDecoder(r io.Reader) *sseDecoder {
	return &sseDecoder{br: bufio.NewReaderSize(r, 64<<10)}
}

// Next returns the next SSE event or io.EOF.
func (d *sseDecoder) Next() (sseEvent, error) {
	if d.done {
		return sseEvent{}, io.EOF
	}
	var (
		name string
		data []byte
	)
	for {
		line, err := d.readLine()
		if errors.Is(err, io.EOF) {
			d.done = true
			if len(data) > 0 {
				return d.dispatch(name, data)
			}
			return sseEvent{}, io.EOF
		}
		if err != nil {
			return sseEvent{}, err
		}
		if line == "" {
			if len(data) == 0 {
				name = ""
				continue
			}
			return d.dispatch(name, data)
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, hasValue := strings.Cut(line, ":")
		if hasValue && strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		switch field {
		case "event":
			name = value
		case "data":
			if len(data)+len(value)+1 > maxSSEEvent {
				return sseEvent{}, &ProtocolError{Err: fmt.Errorf("SSE event exceeds %d bytes", maxSSEEvent)}
			}
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, value...)
		}
	}
}

func (d *sseDecoder) dispatch(name string, data []byte) (sseEvent, error) {
	if !utf8.Valid(data) {
		return sseEvent{}, &ProtocolError{Err: errors.New("SSE event contains invalid UTF-8")}
	}
	return sseEvent{Name: name, Data: data}, nil
}

// readLine reads one SSE line, strips CR, and enforces the total-stream cap.
func (d *sseDecoder) readLine() (string, error) {
	var line []byte
	for {
		chunk, err := d.br.ReadSlice('\n')
		d.total += int64(len(chunk))
		if d.total > maxStreamBytes {
			return "", &ProtocolError{Err: fmt.Errorf("provider stream exceeds %d bytes", maxStreamBytes)}
		}
		if len(chunk) > 0 {
			if len(line)+len(chunk) > maxSSEEvent+64 {
				return "", &ProtocolError{Err: fmt.Errorf("SSE line exceeds %d bytes", maxSSEEvent)}
			}
			line = append(line, chunk...)
		}
		switch {
		case err == nil:
			line = line[:len(line)-1]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			return string(line), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(line) == 0 {
				return "", io.EOF
			}
			if line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			return string(line), nil
		default:
			return "", &TransportError{Err: fmt.Errorf("read provider stream: %w", err)}
		}
	}
}

// partialStreamText caps the display text attached to a failed stream.
func partialStreamText(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= maxPartialText {
		return s
	}
	cut := maxPartialText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
