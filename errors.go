package aharness

import (
	"errors"
	"fmt"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/engine"
	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
)

// SchemaVersion is the session log schema version this package reads and
// writes. It is independent of the module version.
const SchemaVersion = session.Version

// Public sentinel errors. Wrap errors with errors.Is when matching.
var (
	ErrClosed           = errors.New("aharness: agent is closed")
	ErrBusy             = errors.New("aharness: a run is already active")
	ErrEmptyTask        = errors.New("aharness: task is empty")
	ErrIteratorConsumed = errors.New("aharness: events already consumed")
	ErrSessionNotFound  = errors.New("aharness: session not found")
	ErrSessionLocked    = errors.New("aharness: session is locked")
	ErrInvalidSessionID = errors.New("aharness: invalid session id")
	ErrModelRequired    = errors.New("aharness: model is required")
	ErrToolConflict     = errors.New("aharness: tool name conflict")
	ErrToolMismatch     = errors.New("aharness: tool set does not match the session")
	ErrSteeringFull     = errors.New("aharness: steering queue is full")
	ErrNoSuchChild      = errors.New("aharness: no such live child session")
)

// ConfigError describes an invalid Config or construction input.
type ConfigError struct {
	Field  string
	Value  string
	Detail string
}

func (e *ConfigError) Error() string {
	if e.Value != "" {
		return fmt.Sprintf("aharness: %s=%q: %s", e.Field, e.Value, e.Detail)
	}
	if e.Field != "" {
		return fmt.Sprintf("aharness: %s: %s", e.Field, e.Detail)
	}
	return "aharness: " + e.Detail
}

// ProviderError classifies a provider HTTP or transport failure when the
// underlying error exposes a status code.
type ProviderError struct {
	StatusCode int
	Retryable  bool
	Err        error
}

func (e *ProviderError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("provider error (status %d)", e.StatusCode)
	}
	return e.Err.Error()
}

func (e *ProviderError) Unwrap() error { return e.Err }

// Deny is the recommended error for Authorize. Its Reason is shown to the
// model as a permission_denied tool result.
type Deny struct{ Reason string }

func (d *Deny) Error() string {
	if d.Reason == "" {
		return "permission denied"
	}
	return d.Reason
}

// publicError converts internal engine/agent errors into the public error
// vocabulary while preserving the original cause.
func publicError(err error) error {
	if err == nil {
		return nil
	}
	if config, ok := errors.AsType[*engine.ConfigError](err); ok {
		return &ConfigError{Field: config.Field, Detail: config.Detail}
	}
	switch {
	case errors.Is(err, agent.ErrToolMismatch):
		return fmt.Errorf("%w: %w", ErrToolMismatch, err)
	case errors.Is(err, engine.ErrSessionNotFound):
		return fmt.Errorf("%w: %w", ErrSessionNotFound, err)
	case errors.Is(err, engine.ErrSessionLocked):
		return fmt.Errorf("%w: %w", ErrSessionLocked, err)
	case errors.Is(err, engine.ErrInvalidSessionID):
		return fmt.Errorf("%w: %w", ErrInvalidSessionID, err)
	case errors.Is(err, engine.ErrClosed):
		return ErrClosed
	case errors.Is(err, model.ErrContextOverflow):
		return err
	}
	if httpErr, ok := errors.AsType[*model.HTTPError](err); ok {
		return &ProviderError{StatusCode: httpErr.StatusCode, Retryable: httpErr.Retryable(), Err: err}
	}
	return err
}
