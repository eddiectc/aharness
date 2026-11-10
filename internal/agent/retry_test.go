package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"aharness/internal/model"
	"aharness/internal/session"
)

func TestRunRetry429ThenSuccess(t *testing.T) {
	synctestTest(t, func(t *testing.T) {
		cfg := testConfig()
		cfg.LogRequests = true
		comp := &fakeCompleter{steps: []scriptStep{
			{err: &model.HTTPError{
				StatusCode: 429,
				Status:     "429 Too Many Requests",
				Body:       "slow down",
				RetryAfter: 2 * time.Second,
			}},
			{resp: model.Response{Text: "recovered", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 3, OutputTokens: 4}}},
		}}
		store := &fakeStore{}
		runner := newRunner(cfg, comp, &fakeToolset{}, store)
		startTestSession(t, runner)
		runner.jitter = func(d time.Duration) time.Duration { return d }
		outcome := make(chan runOutcome, 1)
		go func() {
			res, err := runner.Run(t.Context(), defaultRunOptions())
			outcome <- runOutcome{res: res, err: err}
		}()
		got := <-outcome
		if got.err != nil {
			t.Fatalf("Run error = %v, want nil", got.err)
		}
		if got.res.Status != StopCompleted || got.res.CumulativeTurns != 1 {
			t.Fatalf("result = %+v", got.res)
		}
		if comp.calls != 2 {
			t.Fatalf("model calls = %d, want 2", comp.calls)
		}
		retries := eventsByType(store, session.TypeModelRetry)
		if len(retries) != 1 {
			t.Fatalf("model_retry events = %d, want 1", len(retries))
		}
		data := decodePayload[modelRetryData](t, retries[0])
		if data.Attempt != 1 || data.Class != retryClassHTTP || data.Status != 429 || data.DelayMS != 2000 {
			t.Fatalf("model_retry = %+v", data)
		}
		if got := len(eventsByType(store, session.TypeModelRequest)); got != 1 {
			t.Fatalf("model_request events = %d, want 1 (retries reuse the request)", got)
		}
		responses := eventsByType(store, session.TypeModelResponse)
		if len(responses) != 1 {
			t.Fatalf("model_response events = %d, want 1", len(responses))
		}
		response := decodePayload[modelResponseData](t, responses[0])
		if response.Attempt != 2 {
			t.Fatalf("model_response attempt = %d, want 2", response.Attempt)
		}
		if got := len(eventsByType(store, session.TypeError)); got != 0 {
			t.Fatalf("error events = %d, want 0", got)
		}
	})
}

func TestRunRetry401FastFail(t *testing.T) {
	cfg := testConfig()
	comp := &fakeCompleter{steps: []scriptStep{{err: &model.HTTPError{
		StatusCode: 401,
		Status:     "401 Unauthorized",
		Body:       "bad token",
	}}}}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if res.Status != StopProviderError {
		t.Fatalf("status = %q, want %q", res.Status, StopProviderError)
	}
	if err == nil {
		t.Fatal("Run error = nil, want provider error")
	}
	if comp.calls != 1 {
		t.Fatalf("model calls = %d, want 1", comp.calls)
	}
	if got := len(eventsByType(store, session.TypeModelRetry)); got != 0 {
		t.Fatalf("model_retry events = %d, want 0", got)
	}
	errs := eventsByType(store, session.TypeError)
	if len(errs) != 1 {
		t.Fatalf("error events = %d, want 1", len(errs))
	}
	data := decodePayload[errorData](t, errs[0])
	if data.Class != errorClassHTTP || data.Message == "" {
		t.Fatalf("error = %+v", data)
	}
	assertExactlyOne(t, store, session.TypeFinal)
	assertExactlyOne(t, store, session.TypeRunEnd)
}

func TestRunProtocolErrorNoRetry(t *testing.T) {
	cfg := testConfig()
	comp := &fakeCompleter{steps: []scriptStep{{err: &model.ProtocolError{Err: errors.New("duplicate object name")}}}}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if res.Status != StopProtocolError {
		t.Fatalf("status = %q, want %q", res.Status, StopProtocolError)
	}
	if err == nil {
		t.Fatal("Run error = nil, want protocol error")
	}
	if comp.calls != 1 {
		t.Fatalf("model calls = %d, want 1", comp.calls)
	}
	if got := len(eventsByType(store, session.TypeModelRetry)); got != 0 {
		t.Fatalf("model_retry events = %d, want 0", got)
	}
	errs := eventsByType(store, session.TypeError)
	if len(errs) != 1 || decodePayload[errorData](t, errs[0]).Class != errorClassProtocol {
		t.Fatalf("error events = %+v", errs)
	}
}

func TestRunTransportRetriesExhausted(t *testing.T) {
	synctestTest(t, func(t *testing.T) {
		cfg := testConfig()
		var steps []scriptStep
		for range maxModelAttempts {
			steps = append(steps, scriptStep{err: &model.TransportError{Err: errors.New("connection reset")}})
		}
		comp := &fakeCompleter{steps: steps}
		store := &fakeStore{}
		runner := newRunner(cfg, comp, &fakeToolset{}, store)
		startTestSession(t, runner)
		runner.jitter = func(d time.Duration) time.Duration { return d }
		outcome := make(chan runOutcome, 1)
		go func() {
			res, err := runner.Run(t.Context(), defaultRunOptions())
			outcome <- runOutcome{res: res, err: err}
		}()
		got := <-outcome
		if got.res.Status != StopProviderError {
			t.Fatalf("status = %q, want %q", got.res.Status, StopProviderError)
		}
		if got.err == nil {
			t.Fatal("Run error = nil, want provider error")
		}
		if comp.calls != maxModelAttempts {
			t.Fatalf("model calls = %d, want %d", comp.calls, maxModelAttempts)
		}
		retries := eventsByType(store, session.TypeModelRetry)
		if len(retries) != maxModelAttempts-1 {
			t.Fatalf("model_retry events = %d, want %d", len(retries), maxModelAttempts-1)
		}
		wantDelays := []int64{1000, 2000, 4000, 8000}
		for i, ev := range retries {
			data := decodePayload[modelRetryData](t, ev)
			if data.Attempt != i+1 || data.Class != retryClassTransport || data.Status != 0 || data.DelayMS != wantDelays[i] {
				t.Fatalf("model_retry[%d] = %+v", i, data)
			}
		}
		errs := eventsByType(store, session.TypeError)
		if len(errs) != 1 || decodePayload[errorData](t, errs[0]).Class != errorClassTransport {
			t.Fatalf("error events = %+v", errs)
		}
		if got := len(eventsByType(store, session.TypeModelResponse)); got != 0 {
			t.Fatalf("model_response events = %d, want 0", got)
		}
		assertExactlyOne(t, store, session.TypeFinal)
		assertExactlyOne(t, store, session.TypeRunEnd)
	})
}

func TestRunModelTimeoutRetried(t *testing.T) {
	synctestTest(t, func(t *testing.T) {
		cfg := testConfig()
		cfg.ModelTimeout = 5 * time.Second
		cfg.LogRequests = true
		comp := &fakeCompleter{steps: []scriptStep{
			{blockOnContext: true},
			{resp: model.Response{Text: "after timeout", FinishReason: model.FinishStop}},
		}}
		store := &fakeStore{}
		runner := newRunner(cfg, comp, &fakeToolset{}, store)
		startTestSession(t, runner)
		runner.jitter = func(d time.Duration) time.Duration { return d }
		outcome := make(chan runOutcome, 1)
		go func() {
			res, err := runner.Run(t.Context(), defaultRunOptions())
			outcome <- runOutcome{res: res, err: err}
		}()
		got := <-outcome
		if got.err != nil {
			t.Fatalf("Run error = %v, want nil", got.err)
		}
		if got.res.Status != StopCompleted || got.res.FinalText != "after timeout" {
			t.Fatalf("result = %+v", got.res)
		}
		if comp.calls != 2 {
			t.Fatalf("model calls = %d, want 2", comp.calls)
		}
		retries := eventsByType(store, session.TypeModelRetry)
		if len(retries) != 1 {
			t.Fatalf("model_retry events = %d, want 1", len(retries))
		}
		data := decodePayload[modelRetryData](t, retries[0])
		if data.Attempt != 1 || data.Class != retryClassTransport || data.Status != 0 || data.DelayMS != 1000 {
			t.Fatalf("model_retry = %+v", data)
		}
		if got := len(eventsByType(store, session.TypeModelRequest)); got != 1 {
			t.Fatalf("model_request events = %d, want 1", got)
		}
	})
}

func TestRunCancelDuringRetryBackoffMapsInterrupted(t *testing.T) {
	synctestTest(t, func(t *testing.T) {
		cfg := testConfig()
		comp := &fakeCompleter{
			steps:  []scriptStep{{err: &model.HTTPError{StatusCode: 500, Status: "500 Internal Server Error"}}},
			called: make(chan int, 1),
		}
		store := &fakeStore{}
		runner := newRunner(cfg, comp, &fakeToolset{}, store)
		startTestSession(t, runner)
		runner.jitter = func(d time.Duration) time.Duration { return d }
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		outcome := make(chan runOutcome, 1)
		go func() {
			res, err := runner.Run(ctx, defaultRunOptions())
			outcome <- runOutcome{res: res, err: err}
		}()
		<-comp.called
		synctest.Wait()
		cancel()
		got := <-outcome
		if got.err != nil {
			t.Fatalf("Run error = %v, want nil", got.err)
		}
		if got.res.Status != StopInterrupted {
			t.Fatalf("status = %q, want %q", got.res.Status, StopInterrupted)
		}
		if comp.calls != 1 {
			t.Fatalf("model calls = %d, want 1", comp.calls)
		}
		retries := eventsByType(store, session.TypeModelRetry)
		if len(retries) != 1 {
			t.Fatalf("model_retry events = %d, want 1", len(retries))
		}
		assertExactlyOne(t, store, session.TypeFinal)
	})
}

func TestRetryDelay(t *testing.T) {
	cfg := testConfig()
	runner := newRunner(cfg, &fakeCompleter{}, &fakeToolset{}, &fakeStore{})
	runner.jitter = func(d time.Duration) time.Duration { return d }

	tests := []struct {
		name    string
		attempt int
		err     error
		want    time.Duration
	}{
		{name: "retry after", attempt: 1, err: &model.HTTPError{RetryAfter: 2 * time.Second}, want: 2 * time.Second},
		{name: "retry after zero", attempt: 1, err: &model.HTTPError{RetryAfterSet: true}, want: 0},
		{name: "retry after capped", attempt: 1, err: &model.HTTPError{RetryAfter: 10 * time.Minute}, want: 60 * time.Second},
		{name: "transport attempt one", attempt: 1, err: &model.TransportError{Err: errors.New("x")}, want: time.Second},
		{name: "transport attempt four", attempt: 4, err: &model.TransportError{Err: errors.New("x")}, want: 8 * time.Second},
		{name: "max delay", attempt: 10, err: &model.TransportError{Err: errors.New("x")}, want: 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runner.retryDelay(tt.attempt, tt.err); got != tt.want {
				t.Fatalf("retryDelay(%d) = %v, want %v", tt.attempt, got, tt.want)
			}
		})
	}
}

func TestRetryErrorClassification(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		retryable  bool
		wantClass  string
		wantStatus int
	}{
		{name: "429", err: &model.HTTPError{StatusCode: 429}, retryable: true, wantClass: retryClassHTTP, wantStatus: 429},
		{name: "500", err: &model.HTTPError{StatusCode: 503}, retryable: true, wantClass: retryClassHTTP, wantStatus: 503},
		{name: "401", err: &model.HTTPError{StatusCode: 401}, retryable: false, wantClass: retryClassHTTP, wantStatus: 401},
		{name: "transport", err: &model.TransportError{Err: errors.New("reset")}, retryable: true, wantClass: retryClassTransport},
		{name: "deadline", err: context.DeadlineExceeded, retryable: true, wantClass: retryClassTransport},
		{name: "protocol", err: &model.ProtocolError{Err: fmt.Errorf("bad json")}, retryable: false, wantClass: errorClassProtocol},
		{name: "canceled", err: context.Canceled, retryable: false, wantClass: retryClassTransport},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retryableError(tt.err); got != tt.retryable {
				t.Fatalf("retryableError(%v) = %v, want %v", tt.err, got, tt.retryable)
			}
			class, status := retryErrorClass(tt.err)
			if class != tt.wantClass || status != tt.wantStatus {
				t.Fatalf("retryErrorClass(%v) = (%q,%d), want (%q,%d)", tt.err, class, status, tt.wantClass, tt.wantStatus)
			}
		})
	}
}

func TestRunToolResultsSyncedBeforeNextCall(t *testing.T) {
	cfg := testConfig()
	comp := &fakeCompleter{steps: []scriptStep{
		{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("a", "read_file", `{}`)},
			FinishReason: model.FinishToolCalls,
		}},
		{resp: model.Response{Text: "done", FinishReason: model.FinishStop}},
	}}
	store := &fakeStore{}
	runner := newRunner(cfg, comp, &fakeToolset{}, store)
	startTestSession(t, runner)
	if _, err := runner.Run(t.Context(), defaultRunOptions()); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if store.syncs < 3 {
		t.Fatalf("syncs = %d, want at least 3 (model, tools, final)", store.syncs)
	}
}
