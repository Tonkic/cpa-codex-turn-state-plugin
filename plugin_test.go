package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func makeFernetToken(t *testing.T, issuedAt time.Time, blocks int) string {
	t.Helper()
	raw := make([]byte, fernetFixedBytes+blocks*16)
	raw[0] = fernetVersion
	binary.BigEndian.PutUint64(raw[1:9], uint64(issuedAt.Unix()))
	return base64.URLEncoding.EncodeToString(raw)
}

func boolPointer(value bool) *bool { return &value }

func configureRuntime(t *testing.T, state *runtimeState, yamlConfig string) {
	t.Helper()
	// Existing unit cases exercise the legacy eager-probe/inject behavior;
	// production configs without these fields use the safer error-only default.
	if !strings.Contains(yamlConfig, "probe_on_errors_only:") {
		if strings.Contains(yamlConfig, "\nprobe:\n") {
			yamlConfig = strings.Replace(yamlConfig, "\nprobe:\n", "\nprobe:\n  probe_on_errors_only: false\n", 1)
		} else if strings.HasPrefix(yamlConfig, "probe:\n") {
			yamlConfig = strings.Replace(yamlConfig, "probe:\n", "probe:\n  probe_on_errors_only: false\n", 1)
		} else {
			yamlConfig += "\nprobe:\n  probe_on_errors_only: false\n"
		}
	}
	if !strings.Contains(yamlConfig, "inject_on_errors_only:") {
		yamlConfig += "\ninject_on_errors_only: false\n"
	}
	t.Cleanup(state.shutdown)
	raw, errMarshal := json.Marshal(lifecycleRequest{ConfigYAML: []byte(yamlConfig), SchemaVersion: pluginSchema})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errConfigure := state.configure(raw); errConfigure != nil {
		t.Fatal(errConfigure)
	}
}

func TestFernetLengthsAndBlocks(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		blocks int
		length int
	}{
		{blocks: 10, length: 292},
		{blocks: 11, length: 312},
		{blocks: 12, length: 332},
		{blocks: 13, length: 356},
	}
	for _, test := range tests {
		token := makeFernetToken(t, now, test.blocks)
		if len(token) != test.length {
			t.Fatalf("blocks %d: length = %d, want %d", test.blocks, len(token), test.length)
		}
		parsed, errParse := parseTurnState(token, defaultMaxBytes)
		if errParse != nil {
			t.Fatal(errParse)
		}
		if parsed.Blocks != test.blocks || !parsed.IssuedAt.Equal(now) {
			t.Fatalf("parsed = %#v", parsed)
		}
	}
}

func TestPlanBaselines(t *testing.T) {
	if !normalBlockCount(credentialConfig{Plan: "plus"}, 10) || normalBlockCount(credentialConfig{Plan: "plus"}, 11) {
		t.Fatal("Plus baseline mismatch")
	}
	if !normalBlockCount(credentialConfig{Plan: "team"}, 12) || normalBlockCount(credentialConfig{Plan: "team"}, 13) {
		t.Fatal("Team baseline mismatch")
	}
	if normalBlockCount(credentialConfig{Plan: "unknown"}, 10) {
		t.Fatal("unknown plan must not auto-promote")
	}
	policy := credentialConfig{AcceptedBlocks: []int{10, 12}}
	if !normalBlockCount(policy, 10) || !normalBlockCount(policy, 12) || normalBlockCount(policy, 11) || normalBlockCount(policy, 13) {
		t.Fatal("accepted block set mismatch")
	}
}

func TestDefaultsBootstrapUnknownCredential(t *testing.T) {
	state := newRuntimeState()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return now }
	configureRuntime(t, state, "enabled: true\nauto_update: true\ndefaults:\n  accepted_blocks: [10, 12]\n")

	request := requestInterceptRequest{RequestID: "request-1", ToFormat: "codex", Model: "gpt-5.6-sol", Metadata: map[string]any{"selected_auth_id": "private-auth-id"}}
	rawRequest, _ := json.Marshal(request)
	rawResponse, errIntercept := state.interceptAfter(rawRequest)
	if errIntercept != nil {
		t.Fatal(errIntercept)
	}
	if strings.Contains(string(rawResponse), `"Headers"`) {
		t.Fatal("first request must not inject before bootstrap")
	}

	candidate := makeFernetToken(t, now.Add(-time.Minute), 12)
	rawObserved, _ := json.Marshal(streamChunkInterceptRequest{RequestID: "request-1", ChunkIndex: -1, ResponseHeaders: http.Header{turnStateHeader: {candidate}}})
	if _, errObserve := state.interceptStreamChunk(rawObserved); errObserve != nil {
		t.Fatal(errObserve)
	}
	rawSucceeded, _ := json.Marshal(requestCompletion{RequestID: "request-1", Outcome: "succeeded"})
	if _, errComplete := state.complete(rawSucceeded); errComplete != nil {
		t.Fatal(errComplete)
	}
	if got := state.current[stateKey("private-auth-id", "gpt-5.6-sol")].Value; got != candidate {
		t.Fatal("default policy did not bootstrap unknown credential")
	}

	request.RequestID = "request-2"
	rawRequest, _ = json.Marshal(request)
	rawResponse, errIntercept = state.interceptAfter(rawRequest)
	if errIntercept != nil {
		t.Fatal(errIntercept)
	}
	if !strings.Contains(string(rawResponse), candidate) {
		t.Fatal("bootstrapped state was not injected on the next request")
	}
}

func TestErrorOnlyModeSkipsNormalProbeAndInjection(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = mockAuthHost
	now := time.Now().UTC().Truncate(time.Second)
	state.now = func() time.Time { return now }
	token := makeFernetToken(t, now, 10)
	t.Cleanup(state.shutdown)
	rawConfig := `enabled: true
defaults:
  accepted_blocks: [10]
probe:
  enabled: true
  background_refresh: false
  proxy_pool:
    - url: http://proxy.invalid:1
credentials:
  auth-a:
    accepted_blocks: [10]
    state_model: model-a
    state: ` + token + "\n"
	rawLifecycle, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte(rawConfig), SchemaVersion: pluginSchema})
	if err := state.configure(rawLifecycle); err != nil {
		t.Fatal(err)
	}
	state.fetch = func(context.Context, probeAuth, string, *proxyEndpoint, proxyEndpoint) (string, string) {
		t.Fatal("normal account must not be probed")
		return "", "network_error"
	}
	raw, _ := json.Marshal(requestInterceptRequest{RequestID: "normal", ToFormat: "codex", Model: "model-a", Metadata: map[string]any{"selected_auth_id": "auth-a"}})
	response, _ := state.interceptAfter(raw)
	if strings.Contains(string(response), token) {
		t.Fatal("normal account unexpectedly received state injection")
	}
	_, _ = state.complete([]byte(`{"RequestID":"normal","Outcome":"failed","StatusCode":429}`))
	if state.forceInject[stateKey("auth-a", "model-a")] {
		t.Fatal("429 must wait for a newly probed state before injection")
	}
}

func TestDefaultsRejectSharedSeed(t *testing.T) {
	state := newRuntimeState()
	seed := makeFernetToken(t, time.Now(), 10)
	raw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("defaults:\n  accepted_blocks: [10, 12]\n  state: " + seed + "\n"), SchemaVersion: pluginSchema})
	if errConfigure := state.configure(raw); errConfigure == nil || !strings.Contains(errConfigure.Error(), "defaults.state") {
		t.Fatalf("expected defaults.state isolation error, got %v", errConfigure)
	}
}

func TestInjectAndPromoteOnlyAfterSuccess(t *testing.T) {
	state := newRuntimeState()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return now }
	seed := makeFernetToken(t, now.Add(-30*time.Minute), 10)
	candidate := makeFernetToken(t, now.Add(-10*time.Minute), 10)
	configureRuntime(t, state, "enabled: true\nauto_update: true\ncredentials:\n  auth-1:\n    plan: plus\n    state_model: gpt-5.6-sol\n    state: "+seed+"\n    models: [gpt-5.6-*]\n")

	request := requestInterceptRequest{
		RequestID:      "request-1",
		ToFormat:       "codex",
		Model:          "gpt-5.6-sol",
		RequestedModel: "gpt-5.6-sol",
		Metadata:       map[string]any{"selected_auth_id": "auth-1"},
	}
	rawRequest, _ := json.Marshal(request)
	rawResponse, errIntercept := state.interceptAfter(rawRequest)
	if errIntercept != nil {
		t.Fatal(errIntercept)
	}
	var wrapped envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &wrapped); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	var response requestInterceptResponse
	if errUnmarshal := json.Unmarshal(wrapped.Result, &response); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if got := response.Headers.Get(turnStateHeader); got != seed {
		t.Fatalf("injected state mismatch: %q", got)
	}

	rawObserved, _ := json.Marshal(responseInterceptRequest{RequestID: "request-1", ResponseHeaders: http.Header{turnStateHeader: {candidate}}})
	if _, errObserve := state.interceptResponse(rawObserved); errObserve != nil {
		t.Fatal(errObserve)
	}
	rawFailed, _ := json.Marshal(requestCompletion{RequestID: "request-1", Outcome: "failed"})
	if _, errComplete := state.complete(rawFailed); errComplete != nil {
		t.Fatal(errComplete)
	}
	if got := state.current[stateKey("auth-1", "gpt-5.6-sol")].Value; got != seed {
		t.Fatal("failed request promoted candidate")
	}

	request.RequestID = "request-2"
	rawRequest, _ = json.Marshal(request)
	if _, errIntercept = state.interceptAfter(rawRequest); errIntercept != nil {
		t.Fatal(errIntercept)
	}
	rawObserved, _ = json.Marshal(responseInterceptRequest{RequestID: "request-2", ResponseHeaders: http.Header{turnStateHeader: {candidate}}})
	if _, errObserve := state.interceptResponse(rawObserved); errObserve != nil {
		t.Fatal(errObserve)
	}
	rawSucceeded, _ := json.Marshal(requestCompletion{RequestID: "request-2", Outcome: "succeeded"})
	if _, errComplete := state.complete(rawSucceeded); errComplete != nil {
		t.Fatal(errComplete)
	}
	if got := state.current[stateKey("auth-1", "gpt-5.6-sol")].Value; got != candidate {
		t.Fatal("successful request did not promote candidate")
	}
}

func TestRejectsAbnormalCandidateAndExpiredInjection(t *testing.T) {
	state := newRuntimeState()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return now }
	seed := makeFernetToken(t, now.Add(-2*time.Hour), 10)
	abnormal := makeFernetToken(t, now.Add(-5*time.Minute), 11)
	configureRuntime(t, state, "enabled: true\ncredentials:\n  auth-1:\n    plan: pro\n    state_model: gpt-5.6-sol\n    state: "+seed+"\n")

	request := requestInterceptRequest{RequestID: "request-1", ToFormat: "codex", Model: "gpt-5.6-sol", Metadata: map[string]any{"selected_auth_id": "auth-1"}}
	rawRequest, _ := json.Marshal(request)
	rawResponse, errIntercept := state.interceptAfter(rawRequest)
	if errIntercept != nil {
		t.Fatal(errIntercept)
	}
	if strings.Contains(string(rawResponse), seed) {
		t.Fatal("expired state was injected")
	}
	rawObserved, _ := json.Marshal(responseInterceptRequest{RequestID: "request-1", ResponseHeaders: http.Header{turnStateHeader: {abnormal}}})
	if _, errObserve := state.interceptResponse(rawObserved); errObserve != nil {
		t.Fatal(errObserve)
	}
	rawSucceeded, _ := json.Marshal(requestCompletion{RequestID: "request-1", Outcome: "succeeded"})
	if _, errComplete := state.complete(rawSucceeded); errComplete != nil {
		t.Fatal(errComplete)
	}
	if got := state.current[stateKey("auth-1", "gpt-5.6-sol")].Value; got != seed {
		t.Fatal("abnormal candidate was promoted")
	}
}

func TestPersistedRefreshSurvivesReconfigure(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state", "turn-state.json")
	state := newRuntimeState()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return now }
	seed := makeFernetToken(t, now.Add(-30*time.Minute), 12)
	newer := makeFernetToken(t, now.Add(-5*time.Minute), 12)
	config := "enabled: true\nstate_file: " + strings.ReplaceAll(statePath, "\\", "/") + "\ncredentials:\n  team-auth:\n    plan: team\n    state_model: gpt-6-astra\n    state: " + seed + "\n"
	configureRuntime(t, state, config)

	request := requestInterceptRequest{RequestID: "request-1", ToFormat: "codex", Model: "gpt-6-astra", Metadata: map[string]any{"selected_auth_id": "team-auth"}}
	rawRequest, _ := json.Marshal(request)
	_, _ = state.interceptAfter(rawRequest)
	rawObserved, _ := json.Marshal(streamChunkInterceptRequest{RequestID: "request-1", ChunkIndex: -1, ResponseHeaders: http.Header{turnStateHeader: {newer}}})
	_, _ = state.interceptStreamChunk(rawObserved)
	rawSucceeded, _ := json.Marshal(requestCompletion{RequestID: "request-1", Outcome: "succeeded"})
	if _, errComplete := state.complete(rawSucceeded); errComplete != nil {
		t.Fatal(errComplete)
	}

	reloaded := newRuntimeState()
	reloaded.now = state.now
	configureRuntime(t, reloaded, config)
	if got := reloaded.current[stateKey("team-auth", "gpt-6-astra")].Value; got != newer {
		t.Fatal("persisted refreshed state was not restored")
	}
}

func TestModelScopeMatchesCurrentOrRequestedModel(t *testing.T) {
	patterns := canonicalModels([]string{"gpt-6-*", "gpt-5.6-sol"})
	if !matchesModels(patterns, "upstream-alias", "gpt-6-astra") {
		t.Fatal("requested model wildcard should match")
	}
	if !matchesModels(patterns, "gpt-5.6-sol", "client-alias") {
		t.Fatal("current model exact pattern should match")
	}
	if matchesModels(patterns, "gpt-5.5", "gpt-5.5") {
		t.Fatal("unexpected model match")
	}
}

func TestRegistrationUsesHostCapabilityNames(t *testing.T) {
	raw, errMarshal := json.Marshal(pluginRegistration())
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	text := string(raw)
	for _, key := range []string{
		`"request_interceptor":true`,
		`"request_lifecycle_plugin":true`,
		`"response_interceptor":true`,
		`"response_stream_interceptor":true`,
	} {
		if !strings.Contains(text, key) {
			t.Fatalf("registration does not contain %s: %s", key, text)
		}
	}
	if strings.Contains(text, `"stream_chunk_interceptor"`) {
		t.Fatalf("registration contains a non-host capability name: %s", text)
	}
}

func TestConfiguredSeedMustMatchBaseline(t *testing.T) {
	state := newRuntimeState()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	abnormal := makeFernetToken(t, now, 11)
	raw, errMarshal := json.Marshal(lifecycleRequest{
		ConfigYAML:    []byte("enabled: true\ncredentials:\n  auth-1:\n    plan: plus\n    state_model: gpt-5.6-sol\n    state: " + abnormal + "\n"),
		SchemaVersion: pluginSchema,
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errConfigure := state.configure(raw); errConfigure == nil {
		t.Fatal("abnormal configured seed was accepted")
	}
}

func TestConcurrentPersistenceKeepsLatestSnapshot(t *testing.T) {
	state := newRuntimeState()
	statePath := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	state.mu.Lock()
	state.config.StateFile = statePath
	state.current[stateKey("auth-a", "gpt-5.6-sol")] = storedState{Value: makeFernetToken(t, now, 10), IssuedAt: now, Blocks: 10}
	state.current[stateKey("auth-b", "gpt-5.6-sol")] = storedState{Value: makeFernetToken(t, now.Add(time.Minute), 12), IssuedAt: now.Add(time.Minute), Blocks: 12}
	state.mu.Unlock()

	var wait sync.WaitGroup
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if errPersist := state.persistCurrent(statePath); errPersist != nil {
				t.Errorf("persistCurrent: %v", errPersist)
			}
		}()
	}
	wait.Wait()
	loaded, errLoad := loadPersisted(statePath, defaultMaxBytes)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if len(loaded) != 2 || loaded[stateKey("auth-a", "gpt-5.6-sol")].Blocks != 10 || loaded[stateKey("auth-b", "gpt-5.6-sol")].Blocks != 12 {
		t.Fatalf("persisted snapshot = %#v", loaded)
	}
}

func TestRetryRebindDiscardsPreviousCredentialCandidate(t *testing.T) {
	state := newRuntimeState()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return now }
	seedA := makeFernetToken(t, now.Add(-30*time.Minute), 10)
	seedB := makeFernetToken(t, now.Add(-25*time.Minute), 10)
	candidateA := makeFernetToken(t, now.Add(-5*time.Minute), 10)
	configureRuntime(t, state, "enabled: true\ncredentials:\n  auth-a:\n    plan: plus\n    state_model: gpt-5.6-sol\n    state: "+seedA+"\n  auth-b:\n    plan: plus\n    state_model: gpt-5.6-sol\n    state: "+seedB+"\n")

	request := requestInterceptRequest{RequestID: "shared-request", ToFormat: "codex", Model: "gpt-5.6-sol", Metadata: map[string]any{"selected_auth_id": "auth-a"}}
	rawRequest, _ := json.Marshal(request)
	_, _ = state.interceptAfter(rawRequest)
	rawObserved, _ := json.Marshal(responseInterceptRequest{RequestID: request.RequestID, ResponseHeaders: http.Header{turnStateHeader: {candidateA}}})
	_, _ = state.interceptResponse(rawObserved)

	request.Metadata["selected_auth_id"] = "auth-b"
	rawRequest, _ = json.Marshal(request)
	_, _ = state.interceptAfter(rawRequest)
	rawSucceeded, _ := json.Marshal(requestCompletion{RequestID: request.RequestID, Outcome: "succeeded"})
	_, _ = state.complete(rawSucceeded)

	if got := state.current[stateKey("auth-a", "gpt-5.6-sol")].Value; got != seedA {
		t.Fatal("candidate from failed/retried auth-a attempt was promoted")
	}
	if got := state.current[stateKey("auth-b", "gpt-5.6-sol")].Value; got != seedB {
		t.Fatal("auth-b state changed without an observed candidate")
	}
}
