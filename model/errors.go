package model

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrContextOverflow marks a provider rejection caused by an input that does
// not fit the model context window.
var ErrContextOverflow = errors.New("model context overflow")

// ErrUserAgentRequired marks an outbound request made without a caller-supplied
// User-Agent.
var ErrUserAgentRequired = errors.New("user agent is required")

// HTTPError is a non-success HTTP response from a provider.
type HTTPError struct {
	StatusCode    int
	Status        string
	Body          string
	RetryAfter    time.Duration
	RetryAfterSet bool
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("provider returned %s", e.Status)
	}
	return fmt.Sprintf("provider returned %s: %s", e.Status, e.Body)
}

// Retryable reports whether the response is a transient server or rate-limit
// failure.
func (e *HTTPError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// TransportError wraps a connection-level failure before a response arrives.
type TransportError struct {
	Err error
}

func (e *TransportError) Error() string { return "provider transport: " + e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

// ProtocolError wraps malformed or schema-invalid provider JSON.
type ProtocolError struct {
	Err error
}

func (e *ProtocolError) Error() string { return "provider protocol: " + e.Err.Error() }
func (e *ProtocolError) Unwrap() error { return e.Err }

// PartialError wraps a failure that occurred after at least one assistant-text
// delta was emitted. A streamed retry after output began would duplicate
// stdout, so the agent treats it as terminal. Text and Bytes are diagnostics
// only and are never projected.
type PartialError struct {
	Err   error
	Text  string
	Bytes int
}

func (e *PartialError) Error() string { return e.Err.Error() }
func (e *PartialError) Unwrap() error { return e.Err }

// ContextOverflowError wraps the provider error that indicated context
// overflow. It satisfies errors.Is(err, ErrContextOverflow).
type ContextOverflowError struct {
	Err error
}

func (e *ContextOverflowError) Error() string { return "provider context overflow: " + e.Err.Error() }
func (e *ContextOverflowError) Unwrap() error { return e.Err }
func (e *ContextOverflowError) Is(target error) bool {
	return target == ErrContextOverflow
}
