package session

import "errors"

// Sentinel errors exposed to the CLI and tests.
var (
	// ErrStoreClosed is returned by operations that require an open store
	// after Close has run.
	ErrStoreClosed = errors.New("session store closed")
	// ErrInvalidEvent reports an event payload that cannot be encoded or
	// decoded under the strict json/v2 rules.
	ErrInvalidEvent = errors.New("invalid session event")
	// ErrInvalidID reports a session identifier that is not a UUID.
	ErrInvalidID = errors.New("invalid session id")
	// ErrLocked reports that another process holds the session lock.
	ErrLocked = errors.New("session locked by another process")
	// ErrUnsupportedVersion reports a v0 or unknown schema version.
	ErrUnsupportedVersion = errors.New("unsupported session schema version")
	// ErrCorrupt reports a decodable line that violates the session log
	// contract. It refuses resume and fork.
	ErrCorrupt = errors.New("corrupt session log")
	// ErrMode reports a session log with unsafe file permissions.
	ErrMode = errors.New("unsafe session log mode")
)
