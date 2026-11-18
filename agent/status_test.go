package agent

import (
	"errors"
	"testing"
)

func TestStopReasonFailureAndLabel(t *testing.T) {
	cases := []struct {
		status  StopReason
		failure bool
		label   string
	}{
		{StopCompleted, false, "completed"},
		{StopMaxTurns, false, "max turns reached"},
		{StopTokenLimit, false, "token limit reached"},
		{StopTimeLimit, false, "time limit reached"},
		{StopProviderError, true, "provider error"},
		{StopProtocolError, true, "protocol error"},
		{StopStuck, true, "stuck"},
		{StopInterrupted, false, "interrupted"},
		{StopInternalError, true, "internal error"},
		{StopReason("future"), true, `unknown status "future"`},
	}
	for _, tc := range cases {
		if got := tc.status.Failure(); got != tc.failure {
			t.Errorf("%q.Failure() = %v, want %v", tc.status, got, tc.failure)
		}
		if got := tc.status.Label(); got != tc.label {
			t.Errorf("%q.Label() = %q, want %q", tc.status, got, tc.label)
		}
	}
	if NoticeError.Failure() != true || NoticeLimit.Failure() || NoticeWarning.Failure() {
		t.Fatal("notice failure styling is incorrect")
	}
}

func TestStartupFailureReturnsANonEmptyStatus(t *testing.T) {
	store := &fakeStore{failAfter: 1, appendErr: errors.New("cannot append run_start")}
	runner := newRunner(testConfig(), &fakeCompleter{}, &fakeToolset{}, store)
	if err := runner.StartSession(nil); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	res, err := runner.Run(t.Context(), defaultRunOptions())
	if err == nil {
		t.Fatal("Run = nil error, want the run_start failure")
	}
	if res.Status != StopInternalError {
		t.Fatalf("Status = %q, want internal_error", res.Status)
	}
	if res.Status.Label() == "" || !res.Status.Failure() {
		t.Fatalf("startup status classification = %q", res.Status)
	}
}
