package model

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testSessionID = "01932f4a-7c00-7000-8000-0123456789ab"
	testVersion   = "9.9.9-test"
	testToken     = "tok-secret-value"
)

// staticResponse is a canned provider response served for every request.
type staticResponse struct {
	status  int
	body    []byte
	headers http.Header
}

// newStaticServer returns an httptest server that always answers with the
// provided status, headers, and body.
func newStaticServer(t *testing.T, response staticResponse) *httptest.Server {
	t.Helper()
	if response.status == 0 {
		response.status = http.StatusOK
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, values := range response.headers {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(response.status)
		if _, err := w.Write(response.body); err != nil {
			t.Errorf("write response body: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// serveJSON writes a JSON response body.
func serveJSON(t *testing.T, w http.ResponseWriter, status int, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write response body: %v", err)
	}
}

// readRequestBody reads the request body within a bounded size.
func readRequestBody(t *testing.T, r *http.Request) ([]byte, bool) {
	t.Helper()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		t.Errorf("read request body: %v", err)
		return nil, false
	}
	return body, true
}

// checkRequiredHeaders asserts the headers every adapter must send.
func checkRequiredHeaders(t *testing.T, header http.Header, wantToken bool) {
	t.Helper()
	if got, want := header.Get("Content-Type"), "application/json"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got, want := header.Get("x-opencode-session"), testSessionID; got != want {
		t.Errorf("x-opencode-session = %q, want %q", got, want)
	}
	if got, want := header.Get("User-Agent"), testVersion; got != want {
		t.Errorf("User-Agent = %q, want %q", got, want)
	}
	if wantToken {
		if got, want := header.Get("Authorization"), "Bearer "+testToken; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		return
	}
	if values := header.Values("Authorization"); len(values) != 0 {
		t.Errorf("Authorization = %v, want absent", values)
	}
}

func requireProtocolError(t *testing.T, err error) {
	t.Helper()
	if _, ok := errors.AsType[*ProtocolError](err); !ok {
		t.Fatalf("error = %T %v, want *ProtocolError", err, err)
	}
}

func requireHTTPError(t *testing.T, err error, status int) *HTTPError {
	t.Helper()
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %T %v, want *HTTPError", err, err)
	}
	if httpErr.StatusCode != status {
		t.Fatalf("HTTPError.StatusCode = %d, want %d", httpErr.StatusCode, status)
	}
	return httpErr
}

func requireContextOverflow(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("errors.Is(err, ErrContextOverflow) = false for %T %v", err, err)
	}
	var overflow *ContextOverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("error = %T %v, want *ContextOverflowError", err, err)
	}
	if _, ok := errors.AsType[*HTTPError](err); !ok {
		t.Fatalf("overflow cause = %T %v, want wrapped *HTTPError", overflow.Err, overflow.Err)
	}
}

func TestValidateBaseURL(t *testing.T) {
	for _, ok := range []string{"", DefaultBaseURL, "https://example.com/v1", "http://127.0.0.1:8080/v1"} {
		if err := ValidateBaseURL(ok); err != nil {
			t.Fatalf("ValidateBaseURL(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"example.com/v1", "ftp://example.com/v1", "https://", "https://example.com/v1#frag", "https:opaque"} {
		if err := ValidateBaseURL(bad); err == nil {
			t.Fatalf("ValidateBaseURL(%q) = nil, want error", bad)
		}
	}
}

func TestNewAdapterTargets(t *testing.T) {
	chat := NewOpenAIChat("", testToken, testSessionID, testVersion, 0)
	if got, want := chat.transport.url, DefaultBaseURL+"/chat/completions"; got != want {
		t.Errorf("chat URL = %q, want %q", got, want)
	}
	if got, want := chat.transport.client.Timeout, DefaultHTTPTimeout; got != want {
		t.Errorf("chat client timeout = %v, want %v", got, want)
	}

	responses := NewOpenAIResponses("http://provider.test/v1/", testToken, testSessionID, testVersion, 0)
	if got, want := responses.transport.url, "http://provider.test/v1/responses"; got != want {
		t.Errorf("responses URL = %q, want %q", got, want)
	}
	if got, want := responses.transport.client.Timeout, DefaultHTTPTimeout; got != want {
		t.Errorf("responses client timeout = %v, want %v", got, want)
	}
}

func TestNewAdapterTimeoutOverride(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{"explicit positive", 90 * time.Second, 90 * time.Second},
		{"zero selects the default", 0, DefaultHTTPTimeout},
		{"negative selects the default", -time.Second, DefaultHTTPTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chat := NewOpenAIChat("", testToken, testSessionID, testVersion, tc.timeout)
			if got := chat.transport.client.Timeout; got != tc.want {
				t.Errorf("chat client timeout = %v, want %v", got, tc.want)
			}
			responses := NewOpenAIResponses("", testToken, testSessionID, testVersion, tc.timeout)
			if got := responses.transport.client.Timeout; got != tc.want {
				t.Errorf("responses client timeout = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAdapterRequiredHeaders(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		response   string
		wantToken  bool
		newAdapter func(baseURL, token string) Completer
	}{
		{
			name:      "chat completions with token",
			path:      "/chat/completions",
			response:  `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`,
			wantToken: true,
			newAdapter: func(baseURL, token string) Completer {
				return NewOpenAIChat(baseURL, token, testSessionID, testVersion, 0)
			},
		},
		{
			name:      "chat completions without token",
			path:      "/chat/completions",
			response:  `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`,
			wantToken: false,
			newAdapter: func(baseURL, token string) Completer {
				return NewOpenAIChat(baseURL, token, testSessionID, testVersion, 0)
			},
		},
		{
			name:      "responses with token",
			path:      "/responses",
			response:  `{"status":"completed","output":[]}`,
			wantToken: true,
			newAdapter: func(baseURL, token string) Completer {
				return NewOpenAIResponses(baseURL, token, testSessionID, testVersion, 0)
			},
		},
		{
			name:      "responses without token",
			path:      "/responses",
			response:  `{"status":"completed","output":[]}`,
			wantToken: false,
			newAdapter: func(baseURL, token string) Completer {
				return NewOpenAIResponses(baseURL, token, testSessionID, testVersion, 0)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got, want := r.Method, http.MethodPost; got != want {
					t.Errorf("method = %q, want %q", got, want)
				}
				if got, want := r.URL.Path, tc.path; got != want {
					t.Errorf("path = %q, want %q", got, want)
				}
				checkRequiredHeaders(t, r.Header, tc.wantToken)
				serveJSON(t, w, http.StatusOK, tc.response)
			}))
			t.Cleanup(srv.Close)

			token := ""
			if tc.wantToken {
				token = testToken
			}
			resp, err := tc.newAdapter(srv.URL, token).Complete(t.Context(), Request{Model: "demo-model"})
			if err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			if got, want := resp.FinishReason, FinishStop; got != want {
				t.Errorf("FinishReason = %q, want %q", got, want)
			}
		})
	}
}

func TestAdapterResponseErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   []byte
		check  func(*testing.T, error)
	}{
		{
			name:   "malformed json",
			status: http.StatusOK,
			body:   []byte(`{"choices":`),
			check:  requireProtocolError,
		},
		{
			name:   "duplicate object names",
			status: http.StatusOK,
			body:   []byte(`{"choices":[],"choices":[]}`),
			check:  requireProtocolError,
		},
		{
			name:   "invalid utf8",
			status: http.StatusOK,
			body:   []byte("{\"choices\":[{\"message\":{\"content\":\"\xff\"},\"finish_reason\":\"stop\"}]}"),
			check:  requireProtocolError,
		},
		{
			name:   "oversized body",
			status: http.StatusOK,
			body:   bytes.Repeat([]byte("x"), maxResponseBody+1),
			check:  requireProtocolError,
		},
		{
			name:   "unauthorized",
			status: http.StatusUnauthorized,
			body:   []byte(`{"error":{"message":"invalid api key"}}`),
			check: func(t *testing.T, err error) {
				t.Helper()
				httpErr := requireHTTPError(t, err, http.StatusUnauthorized)
				if httpErr.Retryable() {
					t.Errorf("Retryable() = true for 401")
				}
			},
		},
		{
			name:   "context overflow",
			status: http.StatusBadRequest,
			body:   []byte(`{"error":{"message":"context_length_exceeded"}}`),
			check:  requireContextOverflow,
		},
	}
	adapters := []struct {
		name       string
		newAdapter func(string) Completer
	}{
		{
			name: "chat completions",
			newAdapter: func(baseURL string) Completer {
				return NewOpenAIChat(baseURL, testToken, testSessionID, testVersion, 0)
			},
		},
		{
			name: "responses",
			newAdapter: func(baseURL string) Completer {
				return NewOpenAIResponses(baseURL, testToken, testSessionID, testVersion, 0)
			},
		},
	}
	for _, adapter := range adapters {
		t.Run(adapter.name, func(t *testing.T) {
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					srv := newStaticServer(t, staticResponse{status: tc.status, body: tc.body})
					_, err := adapter.newAdapter(srv.URL).Complete(t.Context(), Request{Model: "demo-model"})
					if err == nil {
						t.Fatal("Complete() error = nil, want error")
					}
					tc.check(t, err)
				})
			}
		})
	}
}

func TestContextOverflowMarkers(t *testing.T) {
	messages := []string{
		"context_length_exceeded",
		"This request exceeds the Context Window",
		"MAXIMUM CONTEXT LENGTH is 8192 tokens",
		"Too Many Tokens in request",
		"prompt is too long",
	}
	for _, message := range messages {
		t.Run(message, func(t *testing.T) {
			body := []byte(`{"error":{"message":` + strconvQuote(message) + `}}`)
			srv := newStaticServer(t, staticResponse{status: http.StatusBadRequest, body: body})
			adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
			_, err := adapter.Complete(t.Context(), Request{Model: "demo-model"})
			if err == nil {
				t.Fatal("Complete() error = nil, want error")
			}
			requireContextOverflow(t, err)
		})
	}
}

func TestRetryAfter(t *testing.T) {
	t.Run("seconds are not capped by the adapter", func(t *testing.T) {
		srv := newStaticServer(t, staticResponse{
			status:  http.StatusTooManyRequests,
			body:    []byte(`{"error":"slow down"}`),
			headers: http.Header{"Retry-After": []string{"120"}},
		})
		adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
		_, err := adapter.Complete(t.Context(), Request{Model: "demo-model"})
		if err == nil {
			t.Fatal("Complete() error = nil, want error")
		}
		httpErr := requireHTTPError(t, err, http.StatusTooManyRequests)
		if !httpErr.Retryable() {
			t.Errorf("Retryable() = false for 429")
		}
		if got, want := httpErr.RetryAfter, 120*time.Second; got != want {
			t.Errorf("RetryAfter = %v, want %v", got, want)
		}
	})

	t.Run("http date", func(t *testing.T) {
		when := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
		srv := newStaticServer(t, staticResponse{
			status:  http.StatusTooManyRequests,
			body:    []byte(`{"error":"slow down"}`),
			headers: http.Header{"Retry-After": []string{when}},
		})
		adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
		_, err := adapter.Complete(t.Context(), Request{Model: "demo-model"})
		if err == nil {
			t.Fatal("Complete() error = nil, want error")
		}
		httpErr := requireHTTPError(t, err, http.StatusTooManyRequests)
		if httpErr.RetryAfter <= 0 || httpErr.RetryAfter > 31*time.Second {
			t.Errorf("RetryAfter = %v, want (0, 31s]", httpErr.RetryAfter)
		}
	})

	for _, value := range []string{"", "not-a-duration"} {
		t.Run("unusable value "+value, func(t *testing.T) {
			headers := http.Header{}
			if value != "" {
				headers.Set("Retry-After", value)
			}
			srv := newStaticServer(t, staticResponse{
				status:  http.StatusServiceUnavailable,
				body:    []byte(`{"error":"unavailable"}`),
				headers: headers,
			})
			adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
			_, err := adapter.Complete(t.Context(), Request{Model: "demo-model"})
			if err == nil {
				t.Fatal("Complete() error = nil, want error")
			}
			httpErr := requireHTTPError(t, err, http.StatusServiceUnavailable)
			if httpErr.RetryAfter != 0 {
				t.Errorf("RetryAfter = %v, want 0", httpErr.RetryAfter)
			}
		})
	}
}

func TestHTTPErrorBodyRedactionAndCap(t *testing.T) {
	body := []byte(`{"error":"` + strings.Repeat(testToken+" ", 2000) + `"}`)
	srv := newStaticServer(t, staticResponse{status: http.StatusInternalServerError, body: body})
	adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
	_, err := adapter.Complete(t.Context(), Request{Model: "demo-model"})
	if err == nil {
		t.Fatal("Complete() error = nil, want error")
	}
	httpErr := requireHTTPError(t, err, http.StatusInternalServerError)
	if strings.Contains(httpErr.Body, testToken) {
		t.Errorf("error body contains the provider token")
	}
	if !strings.Contains(httpErr.Body, "[REDACTED]") {
		t.Errorf("error body = %q, want redaction marker", httpErr.Body)
	}
	if got := len(httpErr.Body); got > maxErrorBody {
		t.Errorf("error body length = %d, want <= %d", got, maxErrorBody)
	}
	if strings.Contains(httpErr.Error(), testToken) {
		t.Errorf("error string contains the provider token")
	}
}

func TestTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	adapter := NewOpenAIChat(url, testToken, testSessionID, testVersion, 0)
	_, err := adapter.Complete(t.Context(), Request{Model: "demo-model"})
	if err == nil {
		t.Fatal("Complete() error = nil, want error")
	}
	if _, ok := errors.AsType[*TransportError](err); !ok {
		t.Fatalf("error = %T %v, want *TransportError", err, err)
	}
	if _, ok := errors.AsType[*HTTPError](err); ok {
		t.Errorf("error = %T %v, want no *HTTPError", err, err)
	}
}

// strconvQuote returns a JSON string literal for s.
func strconvQuote(s string) string {
	var buf bytes.Buffer
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			buf.WriteByte('\\')
			buf.WriteRune(r)
		default:
			buf.WriteRune(r)
		}
	}
	buf.WriteByte('"')
	return buf.String()
}

// TestWithUserAgent verifies that the public adapter identity override is sent
// verbatim, and that an empty override leaves the constructor identity intact.
func TestWithUserAgent(t *testing.T) {
	type completer interface {
		Complete(context.Context, Request) (Response, error)
	}
	cases := []struct {
		name string
		body string
		new  func(string) completer
	}{
		{
			name: "chat",
			body: `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			new: func(base string) completer {
				return NewOpenAIChat(base, "", testSessionID, testVersion, 0)
			},
		},
		{
			name: "responses",
			body: `{"id":"resp","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`,
			new: func(base string) completer {
				return NewOpenAIResponses(base, "", testSessionID, testVersion, 0)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name+" override", func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("User-Agent")
				serveJSON(t, w, http.StatusOK, tc.body)
			}))
			defer srv.Close()
			adapter := tc.new(srv.URL)
			switch a := adapter.(type) {
			case *OpenAIChat:
				adapter = a.WithUserAgent("acode/1.0.0")
			case *OpenAIResponses:
				adapter = a.WithUserAgent("acode/1.0.0")
			}
			if _, err := adapter.Complete(context.Background(), Request{Model: "model", Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if got != "acode/1.0.0" {
				t.Errorf("User-Agent = %q, want acode/1.0.0", got)
			}
		})
		t.Run(tc.name+" empty", func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("User-Agent")
				serveJSON(t, w, http.StatusOK, tc.body)
			}))
			defer srv.Close()
			adapter := tc.new(srv.URL)
			switch a := adapter.(type) {
			case *OpenAIChat:
				adapter = a.WithUserAgent("")
			case *OpenAIResponses:
				adapter = a.WithUserAgent("")
			}
			if _, err := adapter.Complete(context.Background(), Request{Model: "model", Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if want := testVersion; got != want {
				t.Errorf("User-Agent = %q, want %q", got, want)
			}
		})
	}
}

// TestAdapterRequiresUserAgent verifies that an adapter without a
// caller-supplied identity refuses to build a provider request.
func TestAdapterRequiresUserAgent(t *testing.T) {
	req := Request{Model: "model", Messages: []Message{{Role: RoleUser, Content: "hi"}}}
	cases := []struct {
		name string
		new  func() interface {
			Complete(context.Context, Request) (Response, error)
		}
	}{
		{
			name: "chat",
			new: func() interface {
				Complete(context.Context, Request) (Response, error)
			} {
				return NewOpenAIChat("https://provider.test", "", testSessionID, "", 0)
			},
		},
		{
			name: "responses",
			new: func() interface {
				Complete(context.Context, Request) (Response, error)
			} {
				return NewOpenAIResponses("https://provider.test", "", testSessionID, "", 0)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.new().Complete(t.Context(), req)
			if !errors.Is(err, ErrUserAgentRequired) {
				t.Fatalf("error = %T %v, want ErrUserAgentRequired", err, err)
			}
		})
	}
}
