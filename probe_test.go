package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLiveCodexProbe(t *testing.T) {
	path := os.Getenv("CPA_LIVE_AUTH_FILE")
	if path == "" || os.Getenv("CPA_LIVE_PROXY_URL") == "" {
		t.Skip("explicit live credential and proxy required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("live auth unavailable")
	}
	var auth probeAuth
	if json.Unmarshal(raw, &auth) != nil || auth.AccessToken == "" {
		t.Fatal("live auth invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	value, status := fetchProbe(ctx, auth, "gpt-5.6-sol", liveFirstProxy(), proxyEndpoint{URLEnv: "CPA_LIVE_PROXY_URL", ConnectHost: os.Getenv("CPA_LIVE_CONNECT_HOST")})
	if status != "ok" {
		t.Fatalf("Codex probe result=%s", status)
	}
	parsed, err := parseTurnState(value, defaultMaxBytes)
	if err != nil {
		t.Fatal("returned state format invalid")
	}
	t.Logf("Codex probe completed; state_length=%d blocks=%d (state suppressed)", len(value), parsed.Blocks)
}

func mockAuthHost(method string, request any, result any) error {
	raw := `{"files":[{"id":"auth-a","auth_index":"index-a","provider":"codex"},{"id":"auth-b","auth_index":"index-b","provider":"codex"}]}`
	if method == "host.auth.get" {
		raw = `{"json":{"type":"codex","access_token":"test-token","account_id":"test-account"}}`
	}
	return json.Unmarshal([]byte(raw), result)
}

const probeTestConfig = `enabled: true
defaults:
  accepted_blocks: [10, 12]
probe:
  enabled: true
  background_refresh: false
  first_proxy:
    url: socks5h://127.0.0.1:1080
  proxy_pool:
    - url: http://proxy.invalid:5000
      connect_host: proxy.invalid:5000
    - url: socks5h://proxy2.invalid:5001
  max_attempts: 2
`

func TestProbePoolIsolationExpiryAndCooldown(t *testing.T) {
	state := newRuntimeState()
	now := time.Now().UTC().Truncate(time.Second)
	state.now = func() time.Time { return now }
	state.hostCall = mockAuthHost
	configureRuntime(t, state, probeTestConfig)
	token := makeFernetToken(t, now, 10)
	var calls int
	state.fetch = func(ctx context.Context, a probeAuth, model string, first *proxyEndpoint, second proxyEndpoint) (string, string) {
		calls++
		if a.AccessToken != "test-token" || first.URL != "socks5h://127.0.0.1:1080" {
			t.Fatal("probe auth/chain missing")
		}
		if calls == 1 {
			return "", "network_error"
		}
		return token, "ok"
	}
	state.ensureProbe("auth-a", "model-a")
	if calls != 2 || state.current[stateKey("auth-a", "model-a")].Value != token {
		t.Fatal("pool failover did not promote")
	}
	state.ensureProbe("auth-a", "model-a")
	if calls != 2 {
		t.Fatal("fresh state was unnecessarily probed")
	}
	state.ensureProbe("auth-a", "model-b")
	state.ensureProbe("auth-b", "model-a")
	if calls != 4 || len(state.current) != 3 {
		t.Fatal("account/model isolation broken")
	}
	now = now.Add(56 * time.Minute)
	state.fetch = func(context.Context, probeAuth, string, *proxyEndpoint, proxyEndpoint) (string, string) {
		calls++
		return "", "network_error"
	}
	state.ensureProbe("auth-a", "model-a")
	state.ensureProbe("auth-a", "model-a")
	if calls != 6 {
		t.Fatal("failed refresh was not throttled")
	}
	raw, _ := json.Marshal(requestInterceptRequest{RequestID: "r", ToFormat: "codex", Model: "model-a", Metadata: map[string]any{"selected_auth_id": "auth-a"}})
	response, _ := state.interceptAfter(raw)
	if !strings.Contains(string(response), token) {
		t.Fatal("failed probe must preserve valid state")
	}
	now = now.Add(5 * time.Minute)
	response, _ = state.interceptAfter(raw)
	if strings.Contains(string(response), token) {
		t.Fatal("expired state reused")
	}
}

func TestProbeConcurrentSingleflightAndReconfigure(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = mockAuthHost
	configureRuntime(t, state, probeTestConfig)
	started := make(chan struct{})
	done := make(chan struct{})
	var calls atomic.Int32
	state.fetch = func(ctx context.Context, _ probeAuth, _ string, _ *proxyEndpoint, _ proxyEndpoint) (string, string) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		return "", "canceled"
	}
	go func() { defer close(done); state.ensureProbe("auth-a", "model-a") }()
	<-started
	state.ensureProbe("auth-a", "model-a")
	configureRuntime(t, state, probeTestConfig)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reconfigure did not cancel probe")
	}
	if calls.Load() != 1 || len(state.probeResults) != 0 {
		t.Fatal("old probe contaminated new generation")
	}
}

func TestProbeCompletedRejectsFailedOrTruncatedStreams(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", true},
		{"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"failed\"}}\n\n", false},
		{"data: {\"type\":\"response.failed\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", false},
		{"data: {\"type\":\"response.created\"}\n\n", false},
	}
	for _, c := range cases {
		if probeCompleted(strings.NewReader(c.body)) != c.want {
			t.Fatal("SSE terminal classification wrong")
		}
	}
}

func TestCapturedStateNeverCrossesModels(t *testing.T) {
	state := newRuntimeState()
	configureRuntime(t, state, "defaults:\n  accepted_blocks: [10,12]\n")
	token := makeFernetToken(t, time.Now(), 10)
	req := requestInterceptRequest{RequestID: "r", ToFormat: "codex", Model: "model-a", Metadata: map[string]any{"selected_auth_id": "auth-a"}}
	raw, _ := json.Marshal(req)
	_, _ = state.interceptAfter(raw)
	state.captureCandidate("r", http.Header{turnStateHeader: {token}})
	_, _ = state.complete([]byte(`{"RequestID":"r","Outcome":"succeeded"}`))
	req.Model = "model-b"
	raw, _ = json.Marshal(req)
	response, _ := state.interceptAfter(raw)
	if strings.Contains(string(response), token) {
		t.Fatal("state leaked across models")
	}
	req.Model = "model-a"
	req.Metadata["selected_auth_id"] = "auth-b"
	raw, _ = json.Marshal(req)
	response, _ = state.interceptAfter(raw)
	if strings.Contains(string(response), token) {
		t.Fatal("state leaked across accounts")
	}
}

func TestProbeHostFailureDoesNotBreakBusinessRequest(t *testing.T) {
	state := newRuntimeState()
	configureRuntime(t, state, probeTestConfig)
	state.hostCall = func(string, any, any) error { return errors.New("unavailable") }
	raw, _ := json.Marshal(requestInterceptRequest{ToFormat: "codex", Model: "model-a", Metadata: map[string]any{"selected_auth_id": "auth-a"}})
	result, err := state.interceptAfter(raw)
	if err != nil || !strings.Contains(string(result), `"ok":true`) {
		t.Fatal("probe failure blocked business request")
	}
}

func TestAbnormalProbeIsRejected(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = mockAuthHost
	configureRuntime(t, state, probeTestConfig)
	bad := makeFernetToken(t, time.Now(), 13)
	state.fetch = func(context.Context, probeAuth, string, *proxyEndpoint, proxyEndpoint) (string, string) {
		return bad, "ok"
	}
	state.ensureProbe("auth-a", "model-a")
	if len(state.current) != 0 || state.probeResults[stateKey("auth-a", "model-a")] != "state_rejected" {
		t.Fatal("356-character state was promoted")
	}
}

func TestRetryToUnmanagedProviderDiscardsCandidate(t *testing.T) {
	state := newRuntimeState()
	configureRuntime(t, state, "defaults:\n  accepted_blocks: [10,12]\n")
	raw := []byte(`{"RequestID":"r","ToFormat":"codex","Model":"model-a","Metadata":{"selected_auth_id":"auth-a"}}`)
	_, _ = state.interceptAfter(raw)
	state.captureCandidate("r", http.Header{turnStateHeader: {makeFernetToken(t, time.Now(), 10)}})
	_, _ = state.interceptAfter([]byte(`{"RequestID":"r","ToFormat":"openai","Model":"model-b"}`))
	_, _ = state.complete([]byte(`{"RequestID":"r","Outcome":"succeeded"}`))
	if len(state.current) != 0 {
		t.Fatal("candidate from failed attempt promoted after provider switch")
	}
}
