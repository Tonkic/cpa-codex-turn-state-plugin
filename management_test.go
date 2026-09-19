package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

const panelPath = "/v0/resource/plugins/cpa-codex-turn-state/panel"
const statusPath = "/v0/management/codex-turn-state/status"

type managementResult struct {
	StatusCode int                 `json:"StatusCode"`
	Headers    map[string][]string `json:"Headers"`
	Body       []byte              `json:"Body"`
}

// callManagement mirrors how the host dispatches a Management API or resource
// request: the request body travels base64 encoded inside the RPC envelope.
func callManagement(t *testing.T, state *runtimeState, method, path string, body any) managementResult {
	t.Helper()
	request := map[string]any{"Method": method, "Path": path, "Headers": map[string][]string{}}
	if body != nil {
		raw, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		request["Body"] = base64.StdEncoding.EncodeToString(raw)
	}
	rawRequest, errRequest := json.Marshal(request)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	rawResponse, errHandle := state.handleManagement(rawRequest)
	if errHandle != nil {
		t.Fatalf("handleManagement: %v", errHandle)
	}
	var wrapped envelope
	if json.Unmarshal(rawResponse, &wrapped) != nil || !wrapped.OK {
		t.Fatalf("management envelope not ok: %s", string(rawResponse))
	}
	var result managementResult
	if errResult := json.Unmarshal(wrapped.Result, &result); errResult != nil {
		t.Fatal(errResult)
	}
	return result
}

func decodeStatus(t *testing.T, result managementResult) statusView {
	t.Helper()
	var view statusView
	if errDecode := json.Unmarshal(result.Body, &view); errDecode != nil {
		t.Fatalf("decode status: %v (body=%s)", errDecode, string(result.Body))
	}
	return view
}

func TestManagementRegistrationExposesPanelResource(t *testing.T) {
	registration := managementRegistration()
	declared := make(map[string]bool, len(registration.Routes))
	for _, route := range registration.Routes {
		declared[route.Method+" "+route.Path] = true
	}
	// Every route the panel calls must be declared, otherwise the host answers 404.
	for _, want := range []string{
		"GET /codex-turn-state/status",
		"POST /codex-turn-state/refresh",
		"POST /codex-turn-state/clear",
	} {
		if !declared[want] {
			t.Fatalf("management route %q is not registered: %#v", want, registration.Routes)
		}
	}
	if len(declared) != 3 {
		t.Fatalf("unexpected routes: %#v", registration.Routes)
	}
	if len(registration.Resources) != 2 {
		t.Fatalf("resources = %#v", registration.Resources)
	}
	panel := registration.Resources[0]
	if panel.Path != resourcePage || strings.TrimSpace(panel.Menu) == "" || strings.TrimSpace(panel.Description) == "" {
		t.Fatalf("panel resource incomplete: %#v", panel)
	}
	if registration.Resources[1].Path != "/status" {
		t.Fatalf("status resource missing: %#v", registration.Resources)
	}
	raw, errMarshal := json.Marshal(registration)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	// The host decodes Go field names case-insensitively, so the wire keys must
	// stay lowercase for compatibility with both shapes.
	if !strings.Contains(string(raw), `"resources"`) {
		t.Fatalf("registration missing resources key: %s", string(raw))
	}
}

func TestManagementDispatchServesPanelAndStatus(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = mockAuthHost
	configureRuntime(t, state, probeTestConfig)
	now := time.Now().UTC().Truncate(time.Second)
	state.now = func() time.Time { return now }
	state.mu.Lock()
	state.current[stateKey("auth-a", "model-a")] = storedState{Value: makeFernetToken(t, now, 10), IssuedAt: now, Blocks: 10}
	state.mu.Unlock()

	panel := callManagement(t, state, http.MethodGet, panelPath, nil)
	if panel.StatusCode != http.StatusOK || !strings.HasPrefix(panel.Headers["Content-Type"][0], "text/html") {
		t.Fatalf("panel status=%d headers=%#v", panel.StatusCode, panel.Headers)
	}
	if !strings.Contains(panel.Headers["Content-Security-Policy"][0], "frame-ancestors 'self'") || panel.Headers["X-Frame-Options"][0] != "SAMEORIGIN" {
		t.Fatalf("panel security headers=%#v", panel.Headers)
	}
	page := string(panel.Body)
	for _, token := range []string{"Codex Turn State", "entryRows", "codex-turn-state"} {
		if !strings.Contains(page, token) {
			t.Fatalf("panel page missing %q", token)
		}
	}
	if strings.Contains(page, "__INLINE_") {
		t.Fatal("panel assets were not inlined")
	}

	// The Management API keeps answering with JSON on the plugin path.
	status := callManagement(t, state, http.MethodGet, statusPath, nil)
	if status.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d", status.StatusCode)
	}
	view := decodeStatus(t, status)
	if view.Version != pluginVersion || view.Plugin != pluginName {
		t.Fatalf("status identity = %#v", view)
	}
	if view.Now.IsZero() || view.Counters.Total != 1 || len(view.Entries) != 1 {
		t.Fatalf("status payload = %#v", view)
	}
	entry := view.Entries[0]
	if entry.Account != accountDigest("auth-a") || entry.Model != "model-a" || entry.Blocks != 10 ||
		!entry.Valid || !entry.Compatible || entry.StateLength != len(makeFernetToken(t, now, 10)) {
		t.Fatalf("entry = %#v", entry)
	}
	if entry.Handle == "" || entry.Handle == entry.Account {
		t.Fatalf("entry handle missing: %#v", entry)
	}
	publicStatus := callManagement(t, state, http.MethodGet, "/v0/resource/plugins/cpa-codex-turn-state/status", nil)
	if publicStatus.StatusCode != http.StatusOK {
		t.Fatalf("public status code = %d", publicStatus.StatusCode)
	}
	if got := decodeStatus(t, publicStatus); got.Plugin != pluginName || got.Version != pluginVersion {
		t.Fatalf("public status identity = %#v", got)
	}
}

func TestManagementStatusReportsBackoffHistoryAndAccounts(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = func(method string, _ any, result any) error {
		if method == "host.auth.list" {
			return json.Unmarshal([]byte(`{"files":[{"id":"auth-a","auth_index":"index-a","provider":"codex","email":"owner@example.com","status":"active","priority":3}]}`), result)
		}
		return json.Unmarshal([]byte(`{}`), result)
	}
	configureRuntime(t, state, probeTestConfig)
	now := time.Now().UTC().Truncate(time.Second)
	state.now = func() time.Time { return now }
	key := stateKey("auth-a", "model-a")
	token := makeFernetToken(t, now, 10)
	state.mu.Lock()
	state.current[key] = storedState{Value: token, IssuedAt: now, Blocks: 10}
	state.blockedUntil[key] = now.Add(15 * time.Minute)
	state.probeResults[key] = "upstream_http_429_usage_limit_reached"
	state.lastProbe[key] = now.Add(-time.Minute)
	state.probeReasons[key] = "manual"
	state.appendHistoryLocked(historyEntry{At: now, Account: accountDigest("auth-a"), Model: "model-a", Event: "probe", Outcome: "ok", Reason: "missing_state"})
	state.mu.Unlock()

	view := decodeStatus(t, callManagement(t, state, http.MethodGet, statusPath, nil))
	if len(view.Entries) != 1 {
		t.Fatalf("entries = %#v", view.Entries)
	}
	entry := view.Entries[0]
	if !entry.QuotaBackoff || entry.BlockedUntil == nil || !entry.BlockedUntil.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("backoff not reported: %#v", entry)
	}
	if entry.Email != "owner@example.com" || entry.AuthIndex != "index-a" {
		t.Fatalf("account enrichment missing: %#v", entry)
	}
	if entry.LastProbe != "upstream_http_429_usage_limit_reached" || entry.LastProbeReason != "manual" {
		t.Fatalf("probe bookkeeping missing: %#v", entry)
	}
	if view.Counters.QuotaBack != 1 || view.Counters.Failed != 1 || view.Counters.Valid != 1 {
		t.Fatalf("counters = %#v", view.Counters)
	}
	if view.Counters.Accounts != 1 || view.Counters.Models != 1 {
		t.Fatalf("account counters = %#v", view.Counters)
	}
	if len(view.History) != 1 || view.History[0].Event != "probe" || view.History[0].Email != "owner@example.com" {
		t.Fatalf("history = %#v", view.History)
	}
	if !view.AccountsResolved {
		t.Fatal("accounts should resolve through the host callback")
	}
	if view.Probe.ProxyPoolSize != len(state.config.Probe.ProxyPool) || view.Probe.TimeoutSeconds != state.config.Probe.TimeoutSeconds {
		t.Fatalf("probe config view = %#v", view.Probe)
	}
}

func TestManagementRefreshEmptyBodyDoesNotSelectAll(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = mockAuthHost
	configureRuntime(t, state, probeTestConfig)
	now := time.Now().UTC().Truncate(time.Second)
	state.now = func() time.Time { return now }
	key := stateKey("auth-a", "model-a")
	state.mu.Lock()
	state.current[key] = storedState{Value: makeFernetToken(t, now, 10), IssuedAt: now, Blocks: 10}
	state.mu.Unlock()

	result := callManagement(t, state, http.MethodPost, "/v0/management/codex-turn-state/refresh", map[string]any{})
	if result.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d body=%s", result.StatusCode, string(result.Body))
	}
	var payload refreshResult
	if json.Unmarshal(result.Body, &payload) != nil {
		t.Fatalf("decode refresh: %s", string(result.Body))
	}
	if len(payload.Queued) != 0 {
		t.Fatalf("empty body must not queue refreshes: %#v", payload)
	}
}

func TestManagementRefreshSkipsAuthUnavailable(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = mockAuthHost
	configureRuntime(t, state, probeTestConfig)
	now := time.Now().UTC().Truncate(time.Second)
	state.now = func() time.Time { return now }
	key := stateKey("auth-a", "grok-4.6")
	state.mu.Lock()
	state.probeResults[key] = "auth_unavailable"
	state.mu.Unlock()

	result := callManagement(t, state, http.MethodPost, "/v0/management/codex-turn-state/refresh", map[string]any{"all": true})
	var payload refreshResult
	if json.Unmarshal(result.Body, &payload) != nil {
		t.Fatalf("decode refresh: %s", string(result.Body))
	}
	if len(payload.Queued) != 0 || len(payload.Skipped) != 1 || payload.Skipped[0].Reason != "unprobeable" {
		t.Fatalf("auth_unavailable must be skipped: %#v", payload)
	}
}

func TestManagementRefreshActionSkipsQuotaBackoff(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = mockAuthHost
	configureRuntime(t, state, probeTestConfig)
	now := time.Now().UTC().Truncate(time.Second)
	state.now = func() time.Time { return now }
	key := stateKey("auth-a", "model-a")
	state.mu.Lock()
	state.current[key] = storedState{Value: makeFernetToken(t, now, 10), IssuedAt: now, Blocks: 10}
	state.blockedUntil[key] = now.Add(10 * time.Minute)
	state.mu.Unlock()

	result := callManagement(t, state, http.MethodPost, "/v0/management/codex-turn-state/refresh", map[string]any{"all": true})
	if result.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d body=%s", result.StatusCode, string(result.Body))
	}
	var payload refreshResult
	if json.Unmarshal(result.Body, &payload) != nil {
		t.Fatalf("decode refresh: %s", string(result.Body))
	}
	if len(payload.Queued) != 0 || len(payload.Skipped) != 1 || payload.Skipped[0].Reason != "quota_backoff" {
		t.Fatalf("refresh result = %#v", payload)
	}
}

func TestManagementClearProtectsValidState(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = mockAuthHost
	configureRuntime(t, state, probeTestConfig)
	now := time.Now().UTC().Truncate(time.Second)
	state.now = func() time.Time { return now }
	validKey := stateKey("auth-a", "model-a")
	expiredKey := stateKey("auth-b", "model-a")
	futureKey := stateKey("auth-a", "model-future")
	incompatibleKey := stateKey("auth-a", "model-incompatible")
	state.mu.Lock()
	state.current[validKey] = storedState{Value: makeFernetToken(t, now, 10), IssuedAt: now, Blocks: 10}
	state.current[expiredKey] = storedState{Value: makeFernetToken(t, now.Add(-2*time.Hour), 10), IssuedAt: now.Add(-2 * time.Hour), Blocks: 10}
	state.current[futureKey] = storedState{Value: makeFernetToken(t, now.Add(10*time.Minute), 10), IssuedAt: now.Add(10 * time.Minute), Blocks: 10}
	state.current[incompatibleKey] = storedState{Value: makeFernetToken(t, now, 11), IssuedAt: now, Blocks: 11}
	state.probeResults[expiredKey] = "network_error"
	state.mu.Unlock()

	result := callManagement(t, state, http.MethodPost, "/v0/management/codex-turn-state/clear", map[string]any{"scope": "invalid"})
	var cleared clearResult
	if json.Unmarshal(result.Body, &cleared) != nil {
		t.Fatalf("decode clear: %s", string(result.Body))
	}
	if len(cleared.Removed) != 3 || cleared.Remaining != 1 {
		t.Fatalf("clear result = %#v", cleared)
	}
	state.mu.Lock()
	_, stillThere := state.current[validKey]
	state.mu.Unlock()
	if !stillThere || len(state.current) != 1 {
		t.Fatal("valid state must survive an invalid-scope clear")
	}

	// Clearing everything requires an explicit acknowledgement.
	guard := callManagement(t, state, http.MethodPost, "/v0/management/codex-turn-state/clear", map[string]any{"scope": "all"})
	if guard.StatusCode != http.StatusBadRequest {
		t.Fatalf("scope=all without all=true must be rejected: %d", guard.StatusCode)
	}
	confirmed := callManagement(t, state, http.MethodPost, "/v0/management/codex-turn-state/clear", map[string]any{"scope": "all", "all": true})
	var hard clearResult
	if json.Unmarshal(confirmed.Body, &hard) != nil {
		t.Fatalf("decode hard clear: %s", string(confirmed.Body))
	}
	if len(hard.Removed) != 1 || hard.Remaining != 0 {
		t.Fatalf("hard clear result = %#v", hard)
	}
}

func TestManagementClearExpiredProtectsUnexpiredState(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = mockAuthHost
	configureRuntime(t, state, probeTestConfig)
	now := time.Now().UTC().Truncate(time.Second)
	state.now = func() time.Time { return now }
	validKey := stateKey("auth-a", "model-a")
	expiredKey := stateKey("auth-b", "model-a")
	state.mu.Lock()
	state.current[validKey] = storedState{Value: makeFernetToken(t, now, 10), IssuedAt: now, Blocks: 10}
	state.current[expiredKey] = storedState{Value: makeFernetToken(t, now.Add(-2*time.Hour), 10), IssuedAt: now.Add(-2 * time.Hour), Blocks: 10}
	state.mu.Unlock()

	result := callManagement(t, state, http.MethodPost, "/v0/management/codex-turn-state/clear", map[string]any{"scope": "expired"})
	var cleared clearResult
	if json.Unmarshal(result.Body, &cleared) != nil {
		t.Fatalf("decode clear: %s", string(result.Body))
	}
	if len(cleared.Removed) != 1 || cleared.Remaining != 1 {
		t.Fatalf("clear result = %#v", cleared)
	}
	state.mu.Lock()
	_, validStillThere := state.current[validKey]
	_, expiredStillThere := state.current[expiredKey]
	state.mu.Unlock()
	if !validStillThere || expiredStillThere {
		t.Fatal("expired-scope clear must remove only expired state")
	}
}

func TestManagementUnknownRouteAndMethod(t *testing.T) {
	state := newRuntimeState()
	state.hostCall = mockAuthHost
	configureRuntime(t, state, probeTestConfig)

	if got := callManagement(t, state, http.MethodGet, "/v0/management/codex-turn-state/nope", nil); got.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown route status = %d", got.StatusCode)
	}
	if got := callManagement(t, state, http.MethodPost, statusPath, nil); got.StatusCode != http.StatusNotFound {
		t.Fatalf("status must stay read-only: %d", got.StatusCode)
	}
	if got := callManagement(t, state, http.MethodGet, panelPath+"-extra", nil); got.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown resource status = %d", got.StatusCode)
	}
}

func TestManagementRouteNormalization(t *testing.T) {
	cases := []struct {
		path     string
		want     string
		resource bool
	}{
		{"/v0/management/codex-turn-state/status", "/status", false},
		{"/v0/management/plugins/cpa-codex-turn-state/refresh", "/refresh", false},
		{"/v0/resource/plugins/cpa-codex-turn-state/panel", "/panel", true},
		{"/v0/resource/plugins/cpa-codex-turn-state", "/", true},
		{"/codex-turn-state/", "/", false},
	}
	for _, item := range cases {
		path, resource := managementRoutePath(item.path)
		if path != item.want || resource != item.resource {
			t.Fatalf("managementRoutePath(%q) = (%q,%v), want (%q,%v)", item.path, path, resource, item.want, item.resource)
		}
	}
}
