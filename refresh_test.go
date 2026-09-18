package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshAt55MinutesWithoutBusinessRequest(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = mockAuthHost
	now := time.Now().UTC().Truncate(time.Second)
	state.now = func() time.Time { return now }
	configureRuntime(t, state, probeTestConfig)
	key := stateKey("auth-a", "model-a")
	issued := now.Add(-54 * time.Minute)
	old := makeFernetToken(t, issued, 10)
	state.current[key] = storedState{Value: old, IssuedAt: issued, Blocks: 10}
	calls := 0
	state.fetch = func(context.Context, probeAuth, string, *proxyEndpoint, proxyEndpoint) (string, string) {
		calls++
		return makeFernetToken(t, now, 10), "ok"
	}
	state.refreshDue(context.Background())
	if calls != 0 {
		t.Fatal("refreshed before 5-minute window")
	}
	now = now.Add(time.Minute)
	state.refreshDue(context.Background())
	if calls != 1 || !state.current[key].IssuedAt.Equal(now) {
		t.Fatal("did not proactively refresh at minute 55")
	}
	if !state.nextRefreshLocked(key).Equal(now.Add(55 * time.Minute)) {
		t.Fatal("new refresh deadline not rescheduled")
	}
}

func TestBackgroundWorkerRefreshesPersistedSeedAndStops(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = mockAuthHost
	now := time.Now().UTC().Truncate(time.Second)
	old := makeFernetToken(t, now.Add(-56*time.Minute), 10)
	newValue := makeFernetToken(t, now, 10)
	var calls atomic.Int32
	state.fetch = func(context.Context, probeAuth, string, *proxyEndpoint, proxyEndpoint) (string, string) {
		calls.Add(1)
		return newValue, "ok"
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := persistStates(path, map[string]storedState{stateKey("auth-a", "model-a"): {Value: old, IssuedAt: now.Add(-56 * time.Minute), Blocks: 10}}); err != nil {
		t.Fatal(err)
	}
	cfg := strings.Replace(probeTestConfig, "background_refresh: false", "background_refresh: true", 1) + "\nstate_file: " + filepath.ToSlash(path) + "\n"
	configureRuntime(t, state, cfg)
	deadline := time.Now().Add(2 * time.Second)
	for {
		state.mu.Lock()
		promoted := state.current[stateKey("auth-a", "model-a")].Value == newValue
		state.mu.Unlock()
		if promoted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background worker never refreshed without a request")
		}
		time.Sleep(time.Millisecond)
	}
	state.setAccepting(false)
	state.mu.Lock()
	running := state.workerDone != nil
	state.mu.Unlock()
	if running || calls.Load() != 1 {
		t.Fatal("worker did not drain or over-probed")
	}
}

func TestQuiesceCancelsActiveBackgroundProbe(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = mockAuthHost
	started := make(chan struct{})
	state.fetch = func(ctx context.Context, _ probeAuth, _ string, _ *proxyEndpoint, _ proxyEndpoint) (string, string) {
		close(started)
		<-ctx.Done()
		return "", "network_error"
	}
	old := makeFernetToken(t, time.Now().Add(-56*time.Minute), 10)
	cfg := strings.Replace(probeTestConfig, "background_refresh: false", "background_refresh: true", 1) + "\ncredentials:\n  auth-a:\n    plan: plus\n    state_model: model-a\n    state: " + old + "\n"
	configureRuntime(t, state, cfg)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	done := make(chan struct{})
	go func() { state.setAccepting(false); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("quiesce did not cancel and drain background probe")
	}
}

func TestErrorRefreshIsQueuedDeduplicatedAndCooledDown(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = mockAuthHost
	now := time.Now().UTC().Truncate(time.Second)
	state.now = func() time.Time { return now }
	configureRuntime(t, state, probeTestConfig)
	key := stateKey("auth-a", "model-a")
	old := makeFernetToken(t, now.Add(-time.Minute), 10)
	state.current[key] = storedState{Value: old, IssuedAt: now.Add(-time.Minute), Blocks: 10}
	calls := 0
	state.fetch = func(context.Context, probeAuth, string, *proxyEndpoint, proxyEndpoint) (string, string) {
		calls++
		return "", "network_error"
	}
	raw := []byte(`{"RequestID":"r","ToFormat":"codex","Model":"model-a","Metadata":{"selected_auth_id":"auth-a"}}`)
	_, _ = state.interceptAfter(raw)
	_, _ = state.complete([]byte(`{"RequestID":"r","Outcome":"failed","StatusCode":429}`))
	if calls != 0 || state.refreshRequests[key] != "rate_limit" {
		t.Fatal("failure hook blocked or failed to queue")
	}
	state.refreshDue(context.Background())
	if calls != 2 || state.current[key].Value != old {
		t.Fatal("early probe did not run/preserve good cache")
	}
	state.mu.Lock()
	state.queueRefreshLocked(key, "overload")
	state.mu.Unlock()
	state.refreshDue(context.Background())
	if calls != 2 {
		t.Fatal("error burst bypassed cooldown")
	}
	now = now.Add(time.Minute)
	state.fetch = func(context.Context, probeAuth, string, *proxyEndpoint, proxyEndpoint) (string, string) {
		calls++
		return makeFernetToken(t, now, 10), "ok"
	}
	state.refreshDue(context.Background())
	if calls != 3 || state.refreshRequests[key] != "" || !state.current[key].IssuedAt.Equal(now) {
		t.Fatal("pending refresh not retried/promoted")
	}
}

func TestStreamFailureFragmentsAndContentIsolation(t *testing.T) {
	state := newRuntimeState()
	configureRuntime(t, state, probeTestConfig)
	key := stateKey("auth-a", "model-a")
	state.requests["r"] = requestBinding{AuthID: "auth-a", Key: key}
	state.observeStreamFailure("r", []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"429 overloaded rate_limit\"}\n\n"))
	if len(state.refreshRequests) != 0 {
		t.Fatal("assistant content triggered refresh")
	}
	state.captureCandidate("r", http.Header{turnStateHeader: {makeFernetToken(t, time.Now(), 10)}})
	state.observeStreamFailure("r", []byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_is_"))
	state.observeStreamFailure("r", []byte("overloaded\"}}}\n\n"))
	if state.refreshRequests[key] != "overload" || len(state.candidates) != 0 {
		t.Fatal("split SSE overload not classified")
	}
	_, _ = state.complete([]byte(`{"RequestID":"r","Outcome":"succeeded"}`))
	if len(state.current) != 0 {
		t.Fatal("failed stream candidate promoted after outer success")
	}
}

func TestCanceledAndClientErrorsDoNotRefresh(t *testing.T) {
	for _, c := range []requestCompletion{{Outcome: "canceled", StatusCode: 429}, {Outcome: "failed", StatusCode: 400, Error: "invalid request"}} {
		state := newRuntimeState()
		configureRuntime(t, state, probeTestConfig)
		state.requests["r"] = requestBinding{AuthID: "auth-a", Key: stateKey("auth-a", "model-a")}
		c.RequestID = "r"
		raw, _ := json.Marshal(c)
		_, _ = state.complete(raw)
		if len(state.refreshRequests) != 0 {
			t.Fatal("non-upstream failure triggered refresh")
		}
	}
}

func TestQuotaBackoffAndQuiesceCancel(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = mockAuthHost
	now := time.Now()
	state.now = func() time.Time { return now }
	configureRuntime(t, state, probeTestConfig)
	calls := 0
	state.fetch = func(context.Context, probeAuth, string, *proxyEndpoint, proxyEndpoint) (string, string) {
		calls++
		return "", "upstream_http_429_usage_limit_reached"
	}
	state.ensureProbe("auth-a", "model-a")
	before := calls
	now = now.Add(2 * time.Minute)
	state.ensureProbe("auth-a", "model-a")
	if calls != before {
		t.Fatal("quota backoff ignored")
	}
	state.setAccepting(false)
	now = now.Add(time.Hour)
	state.refreshDue(context.Background())
	if calls != before {
		t.Fatal("probe after quiesce")
	}
}
