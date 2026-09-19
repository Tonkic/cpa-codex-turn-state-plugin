package main

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed web/index.html web/app.css web/app.js
var webAssets embed.FS

const (
	// historyLimit bounds the probe/action timeline kept for the management panel.
	historyLimit = 50
	// accountCacheTTL keeps host auth lookups off the management hot path.
	accountCacheTTL = 10 * time.Second
	// resourcePage is the single embedded panel document served to browsers.
	resourcePage = "/panel"
)

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes"`
	Resources []managementRoute `json:"resources"`
}

// managementRegistration registers the diagnostic Management API routes and the
// browser-navigable panel resource that CPA Management clients render. Every
// route the panel calls must be declared here or the host returns 404.
func managementRegistration() managementRegistrationResponse {
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: "/codex-turn-state/status"},
			{Method: http.MethodPost, Path: "/codex-turn-state/refresh"},
			{Method: http.MethodPost, Path: "/codex-turn-state/clear"},
		},
		Resources: []managementRoute{
			{
				Path:        resourcePage,
				Menu:        "Codex Turn State",
				Description: "Inspect and maintain every OAuth Codex turn state.",
			},
			{Path: "/status", Description: "Read-only Codex Turn State status."},
		},
	}
}

type managementRequest struct {
	Method      string              `json:"Method"`
	MethodLower string              `json:"method"`
	Path        string              `json:"Path"`
	PathLower   string              `json:"path"`
	Headers     map[string][]string `json:"Headers"`
	Query       map[string][]string `json:"Query"`
	Body        json.RawMessage     `json:"Body"`
	BodyLower   json.RawMessage     `json:"body"`
}

type managementResponse struct {
	StatusCode int                 `json:"StatusCode"`
	Headers    map[string][]string `json:"Headers"`
	Body       []byte              `json:"Body"`
}

// handleManagement dispatches one Management API or browser resource request.
// Host-side HTTP failures are reported through StatusCode so the caller receives a
// usable payload instead of a generic plugin error.
func (state *runtimeState) handleManagement(raw []byte) ([]byte, error) {
	var request managementRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
			return nil, fmt.Errorf("decode management request: %w", errUnmarshal)
		}
	}
	method := strings.ToUpper(strings.TrimSpace(firstText(request.Method, request.MethodLower)))
	if method == "" {
		method = http.MethodGet
	}
	path, resource := managementRoutePath(firstText(request.Path, request.PathLower))
	body, errBody := decodeManagementBody(request.Body, request.BodyLower)
	if errBody != nil {
		return managementEnvelope(jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid_body"})), nil
	}

	if resource {
		if method != http.MethodGet || (path != resourcePage && path != "/" && path != "/status") {
			return managementEnvelope(jsonResponse(http.StatusNotFound, map[string]any{"error": "resource_not_found"})), nil
		}
		if path == "/status" {
			return managementEnvelope(jsonResponse(http.StatusOK, state.statusPayload())), nil
		}
		page, errPage := renderPanel()
		if errPage != nil {
			return managementEnvelope(jsonResponse(http.StatusInternalServerError, map[string]any{"error": "panel_unavailable"})), nil
		}
		return managementEnvelope(managementResponse{
			StatusCode: http.StatusOK,
			Headers: map[string][]string{
				"Content-Type":                 {"text/html; charset=utf-8"},
				"Cache-Control":                {"no-store"},
				"Content-Security-Policy":      {"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src data:; frame-ancestors 'self'; base-uri 'none'; form-action 'none'"},
				"X-Content-Type-Options":       {"nosniff"},
				"Referrer-Policy":              {"no-referrer"},
				"X-Frame-Options":              {"SAMEORIGIN"},
				"Cross-Origin-Resource-Policy": {"same-origin"},
			},
			Body: page,
		}), nil
	}

	switch {
	case method == http.MethodGet && path == "/status":
		return managementEnvelope(jsonResponse(http.StatusOK, state.statusPayload())), nil
	case method == http.MethodPost && path == "/refresh":
		return managementEnvelope(jsonResponse(http.StatusOK, state.refreshAction(body))), nil
	case method == http.MethodPost && path == "/clear":
		result, errClear := state.clearAction(body)
		if errClear != nil {
			return managementEnvelope(jsonResponse(http.StatusBadRequest, map[string]any{"error": safeError(errClear)})), nil
		}
		return managementEnvelope(jsonResponse(http.StatusOK, result)), nil
	case method == http.MethodGet && (path == "/" || path == ""):
		return managementEnvelope(jsonResponse(http.StatusOK, map[string]any{
			"plugin":  pluginName,
			"version": pluginVersion,
			"routes":  []string{"GET /codex-turn-state/status", "POST /codex-turn-state/refresh", "POST /codex-turn-state/clear"},
			"panel":   "/v0/resource/plugins/" + pluginName + resourcePage,
		})), nil
	default:
		return managementEnvelope(jsonResponse(http.StatusNotFound, map[string]any{"error": "route_not_found", "method": method, "path": path})), nil
	}
}

// managementRoutePath strips the host prefixes so one handler can serve both the
// Management API namespace and the browser resource namespace.
func managementRoutePath(path string) (string, bool) {
	path = strings.TrimSpace(path)
	resourceBase := "/v0/resource/plugins/" + pluginName
	if path == resourceBase || strings.HasPrefix(path, resourceBase+"/") {
		return orRootPath(cleanRoutePath(strings.TrimPrefix(path, resourceBase))), true
	}
	for _, prefix := range []string{
		"/v0/management/plugins/" + pluginName,
		"/v0/management/" + pluginName,
		"/v0/management/codex-turn-state",
		"/v0/management",
		"/plugins/" + pluginName,
		"/codex-turn-state",
	} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return orRootPath(cleanRoutePath(strings.TrimPrefix(path, prefix))), false
		}
	}
	if path == "" {
		return "/", false
	}
	return cleanRoutePath(path), false
}

func orRootPath(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

func cleanRoutePath(path string) string {
	trimmed := strings.TrimRight(path, "/")
	if trimmed == "" {
		return "/"
	}
	return trimmed
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// decodeManagementBody accepts the host's base64 envelope and, defensively, a
// request body that arrived already decoded.
func decodeManagementBody(official json.RawMessage, legacy json.RawMessage) ([]byte, error) {
	raw := official
	if len(raw) == 0 || string(raw) == "null" {
		raw = legacy
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var encoded string
	if errUnmarshal := json.Unmarshal(raw, &encoded); errUnmarshal == nil {
		if encoded == "" {
			return nil, nil
		}
		decoded, errDecode := base64.StdEncoding.DecodeString(encoded)
		if errDecode != nil {
			return nil, errDecode
		}
		return decoded, nil
	}
	return raw, nil
}

func jsonResponse(status int, payload any) managementResponse {
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"error":"encode_response_failed"}`)
	}
	return managementResponse{
		StatusCode: status,
		Headers: map[string][]string{
			"Content-Type":  {"application/json; charset=utf-8"},
			"Cache-Control": {"no-store"},
		},
		Body: body,
	}
}

func managementEnvelope(response managementResponse) []byte {
	raw, errEncode := okEnvelope(response)
	if errEncode != nil {
		return errorEnvelope("encode_response_failed", errEncode.Error())
	}
	return raw
}

func renderPanel() ([]byte, error) {
	html, errHTML := webAssets.ReadFile("web/index.html")
	if errHTML != nil {
		return nil, errHTML
	}
	css, errCSS := webAssets.ReadFile("web/app.css")
	if errCSS != nil {
		return nil, errCSS
	}
	js, errJS := webAssets.ReadFile("web/app.js")
	if errJS != nil {
		return nil, errJS
	}
	page := strings.Replace(string(html), "/*__INLINE_CSS__*/", string(css), 1)
	page = strings.Replace(page, "/*__INLINE_JS__*/", string(js), 1)
	return []byte(page), nil
}

func accountDigest(authID string) string {
	digest := sha256.Sum256([]byte(authID))
	return hex.EncodeToString(digest[:6])
}

func entryHandle(key string) string {
	digest := sha256.Sum256([]byte("handle:" + key))
	return hex.EncodeToString(digest[:4])
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}

// --- account enrichment -----------------------------------------------------

type accountCache struct {
	mu        sync.Mutex
	fetchedAt time.Time
	inflight  bool
	byHash    map[string]accountInfo
}

type accountInfo struct {
	Hash          string `json:"account"`
	Email         string `json:"email,omitempty"`
	Account       string `json:"account_id,omitempty"`
	AuthIndex     string `json:"auth_index,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Label         string `json:"label,omitempty"`
	Plan          string `json:"plan,omitempty"`
	Status        string `json:"status,omitempty"`
	StatusMessage string `json:"status_message,omitempty"`
	Disabled      bool   `json:"auth_disabled"`
	Unavailable   bool   `json:"auth_unavailable"`
	Priority      int    `json:"priority,omitempty"`
	Success       int64  `json:"success,omitempty"`
	Failed        int64  `json:"failed,omitempty"`
}

func inferredAccountPlan(name, accountType string) string {
	value := strings.ToLower(strings.TrimSpace(name + " " + accountType))
	for _, plan := range []string{"free", "plus", "team", "pro"} {
		if strings.Contains(value, "-"+plan) || strings.Contains(value, "_"+plan) || strings.Contains(value, " "+plan) {
			return plan
		}
	}
	return ""
}

// accountsByHash maps the anonymous account digest back to host credential
// metadata. Tokens are never read or returned; the result is cacheable display data.
func (state *runtimeState) accountsByHash() (map[string]accountInfo, bool) {
	cache := &state.accounts
	cache.mu.Lock()
	stale := cache.byHash
	if !cache.fetchedAt.IsZero() && state.now().Sub(cache.fetchedAt) < accountCacheTTL {
		cache.mu.Unlock()
		return stale, true
	}
	if cache.inflight {
		cache.mu.Unlock()
		return stale, stale != nil
	}
	cache.inflight = true
	call := state.hostCall
	cache.mu.Unlock()

	index := map[string]accountInfo{}
	var errList error
	if call == nil {
		errList = fmt.Errorf("host auth callbacks unavailable")
	} else {
		var listing struct {
			Files []struct {
				ID            string `json:"id"`
				AuthIndex     string `json:"auth_index"`
				Name          string `json:"name"`
				Type          string `json:"type"`
				Provider      string `json:"provider"`
				Label         string `json:"label"`
				AccountType   string `json:"account_type"`
				Status        string `json:"status"`
				Email         string `json:"email"`
				Account       string `json:"account"`
				Disabled      bool   `json:"disabled"`
				Unavailable   bool   `json:"unavailable"`
				BaseURL       string `json:"base_url"`
				Priority      int    `json:"priority"`
				Success       int64  `json:"success"`
				Failed        int64  `json:"failed"`
				StatusMessage string `json:"status_message"`
			} `json:"files"`
		}
		if errCall := call("host.auth.list", struct{}{}, &listing); errCall != nil {
			errList = errCall
		}
		for _, entry := range listing.Files {
			if strings.TrimSpace(entry.ID) == "" {
				continue
			}
			info := accountInfo{
				Hash:          accountDigest(entry.ID),
				Email:         strings.TrimSpace(entry.Email),
				Account:       strings.TrimSpace(entry.Account),
				AuthIndex:     strings.TrimSpace(entry.AuthIndex),
				Provider:      strings.TrimSpace(entry.Provider),
				Label:         strings.TrimSpace(entry.Label),
				Plan:          inferredAccountPlan(entry.Name, entry.AccountType),
				Status:        strings.TrimSpace(entry.Status),
				StatusMessage: strings.TrimSpace(entry.StatusMessage),
				Disabled:      entry.Disabled,
				Unavailable:   entry.Unavailable,
				Priority:      entry.Priority,
				Success:       entry.Success,
				Failed:        entry.Failed,
			}
			if info.Email == "" {
				info.Email = info.Account
			}
			if info.Label == "" {
				info.Label = strings.TrimSpace(entry.Name)
			}
			index[info.Hash] = info
		}
	}

	cache.mu.Lock()
	cache.inflight = false
	if errList == nil {
		cache.byHash = index
		cache.fetchedAt = state.now()
	}
	cached := cache.byHash
	cache.mu.Unlock()
	if cached == nil {
		cached = index
	}
	return cached, errList == nil
}

func (state *runtimeState) authIsFree(authID string) bool {
	accounts, resolved := state.accountsByHash()
	if !resolved {
		return false
	}
	info, ok := accounts[accountDigest(authID)]
	return ok && strings.EqualFold(info.Plan, "free")
}

// --- status payload ---------------------------------------------------------

type entryView struct {
	Handle             string     `json:"key"`
	Account            string     `json:"account"`
	Model              string     `json:"model"`
	Email              string     `json:"email,omitempty"`
	AuthIndex          string     `json:"auth_index,omitempty"`
	AuthDisabled       bool       `json:"auth_disabled"`
	AuthUnavailable    bool       `json:"auth_unavailable"`
	Managed            bool       `json:"managed"`
	AutoUpdate         bool       `json:"auto_update"`
	InScopeModel       bool       `json:"model_in_scope"`
	Plan               string     `json:"plan,omitempty"`
	ExpectedBlocks     int        `json:"expected_blocks"`
	AcceptedBlocks     []int      `json:"accepted_blocks,omitempty"`
	StateLength        int        `json:"state_length"`
	Blocks             int        `json:"blocks"`
	Compatible         bool       `json:"compatible"`
	Valid              bool       `json:"valid"`
	IssuedAt           time.Time  `json:"issued_at"`
	ExpiresAt          time.Time  `json:"expires_at"`
	SecondsToExpiry    int64      `json:"seconds_to_expiry"`
	LastProbe          string     `json:"last_probe"`
	LastProbeAt        time.Time  `json:"last_probe_at"`
	LastProbeReason    string     `json:"last_probe_reason"`
	Probing            bool       `json:"probing"`
	RefreshPending     string     `json:"refresh_pending"`
	NextRefreshAt      time.Time  `json:"next_refresh_at"`
	SecondsToRefresh   int64      `json:"seconds_to_refresh"`
	BlockedUntil       *time.Time `json:"blocked_until,omitempty"`
	QuotaBackoff       bool       `json:"quota_backoff"`
	SeededFromConfig   bool       `json:"seeded_from_config"`
	LastProbeSucceeded bool       `json:"last_probe_ok"`
}

type configuredView struct {
	Account        string   `json:"account"`
	Email          string   `json:"email,omitempty"`
	AuthIndex      string   `json:"auth_index,omitempty"`
	Plan           string   `json:"plan,omitempty"`
	NormalBlocks   int      `json:"normal_blocks"`
	AcceptedBlocks []int    `json:"accepted_blocks,omitempty"`
	Models         []string `json:"models,omitempty"`
	StateModel     string   `json:"state_model,omitempty"`
	AutoUpdate     bool     `json:"auto_update"`
	Seeded         bool     `json:"seeded"`
	Disabled       bool     `json:"auth_disabled"`
	HasState       bool     `json:"has_state"`
	EntryCount     int      `json:"entry_count"`
}

type historyView struct {
	At      time.Time `json:"at"`
	Account string    `json:"account"`
	Model   string    `json:"model"`
	Email   string    `json:"email,omitempty"`
	Event   string    `json:"event"`
	Outcome string    `json:"outcome,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

type historyEntry struct {
	At      time.Time
	Account string
	Model   string
	Event   string
	Outcome string
	Reason  string
	Detail  string
}

type statusCounters struct {
	Total        int `json:"total"`
	Valid        int `json:"valid"`
	Probing      int `json:"probing"`
	Pending      int `json:"pending"`
	QuotaBack    int `json:"quota_backoff"`
	Incompatible int `json:"incompatible"`
	Failed       int `json:"failed"`
	Empty        int `json:"empty_state"`
	Accounts     int `json:"accounts"`
	Models       int `json:"models"`
}

type probeConfigView struct {
	Enabled              bool   `json:"enabled"`
	BackgroundRefresh    bool   `json:"background_refresh"`
	RefreshOnErrors      bool   `json:"refresh_on_errors"`
	InjectExpired        bool   `json:"inject_expired"`
	TimeoutSeconds       int    `json:"timeout_seconds"`
	RetrySeconds         int    `json:"retry_seconds"`
	RefreshBeforeSeconds int    `json:"refresh_before_seconds"`
	MaxAttempts          int    `json:"max_attempts"`
	QuotaBackoffSeconds  int    `json:"quota_backoff_seconds"`
	ProxyPoolSize        int    `json:"proxy_pool_size"`
	FirstProxy           bool   `json:"first_proxy"`
	ProxySource          string `json:"proxy_source,omitempty"`
	LiveProbes           int    `json:"live_probes"`
	Accepting            bool   `json:"accepting"`
}

type defaultsView struct {
	Plan           string   `json:"plan,omitempty"`
	NormalBlocks   int      `json:"normal_blocks"`
	AcceptedBlocks []int    `json:"accepted_blocks,omitempty"`
	Models         []string `json:"models,omitempty"`
	AutoUpdate     bool     `json:"auto_update"`
}

type statusView struct {
	Plugin               string           `json:"plugin"`
	Version              string           `json:"version"`
	Schema               int              `json:"schema"`
	Now                  time.Time        `json:"now"`
	StateTTLSeconds      int64            `json:"state_ttl_seconds"`
	ProbeEnabled         bool             `json:"probe_enabled"`
	BackgroundRefresh    bool             `json:"background_refresh"`
	RefreshBeforeSeconds int              `json:"refresh_before_seconds"`
	Probe                probeConfigView  `json:"probe"`
	Defaults             *defaultsView    `json:"defaults,omitempty"`
	Counters             statusCounters   `json:"counters"`
	Entries              []entryView      `json:"entries"`
	Configured           []configuredView `json:"configured"`
	History              []historyView    `json:"history"`
	AccountsResolved     bool             `json:"accounts_resolved"`
}

// statusPayload snapshots runtime state, then resolves host credential metadata
// outside the runtime lock so a slow host callback cannot block request handling.
func (state *runtimeState) statusPayload() statusView {
	type snapshot struct {
		key            string
		current        storedState
		hasCurrent     bool
		probeResult    string
		lastProbeAt    time.Time
		probeReason    string
		refreshPending string
		probing        bool
		nextRefresh    time.Time
		blockedUntil   time.Time
	}

	state.mu.Lock()
	cfg := state.config
	now := state.now()
	keys := make(map[string]bool)
	for key := range state.current {
		keys[key] = true
	}
	for key := range state.probeResults {
		keys[key] = true
	}
	for key := range state.refreshRequests {
		keys[key] = true
	}
	for key := range state.probing {
		keys[key] = true
	}
	for key := range state.blockedUntil {
		if state.blockedUntil[key].After(now) {
			keys[key] = true
		}
	}
	seeded := make(map[string]bool)
	for rawAuthID, credential := range cfg.Credentials {
		if strings.TrimSpace(credential.State) != "" && strings.TrimSpace(credential.StateModel) != "" {
			seeded[stateKey(strings.TrimSpace(rawAuthID), credential.StateModel)] = true
		}
	}
	snapshots := make([]snapshot, 0, len(keys))
	for key := range keys {
		current, hasCurrent := state.current[key]
		snapshots = append(snapshots, snapshot{
			key:            key,
			current:        current,
			hasCurrent:     hasCurrent,
			probeResult:    state.probeResults[key],
			lastProbeAt:    state.lastProbe[key],
			probeReason:    state.probeReasons[key],
			refreshPending: state.refreshRequests[key],
			probing:        state.probing[key],
			nextRefresh:    state.nextRefreshLocked(key),
			blockedUntil:   state.blockedUntil[key],
		})
	}
	history := append([]historyEntry(nil), state.history...)
	liveProbes := len(state.probing)
	state.mu.Unlock()

	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].key == snapshots[j].key {
			return false
		}
		return snapshots[i].key < snapshots[j].key
	})

	accounts, resolved := state.accountsByHash()

	entries := make([]entryView, 0, len(snapshots))
	configuredAuths := make(map[string]int)
	modelSet := make(map[string]bool)
	visibleAccounts := make(map[string]bool)
	counters := statusCounters{}
	for _, item := range snapshots {
		authID, model := splitStateKey(item.key)
		digest := accountDigest(authID)
		if resolved {
			info, known := accounts[digest]
			if !known || strings.EqualFold(info.Plan, "free") {
				continue
			}
		}
		policy, managed := credentialFor(cfg, authID)
		issuedAt := item.current.IssuedAt
		expiresAt := issuedAt.Add(turnStateTTL)
		valid := item.current.Value != "" && !issuedAt.After(now.Add(5*time.Minute)) && now.Before(expiresAt)
		compatible := item.current.Value == "" || normalBlockCount(policy, item.current.Blocks)
		entry := entryView{
			Handle:             entryHandle(item.key),
			Account:            digest,
			Model:              model,
			AuthDisabled:       false,
			Managed:            managed,
			AutoUpdate:         managed && autoUpdateEnabled(cfg, policy),
			InScopeModel:       matchesModels(policy.Models, model),
			Plan:               policy.Plan,
			ExpectedBlocks:     policy.NormalBlocks,
			AcceptedBlocks:     policy.AcceptedBlocks,
			StateLength:        len(item.current.Value),
			Blocks:             item.current.Blocks,
			Compatible:         compatible,
			Valid:              valid,
			IssuedAt:           issuedAt,
			ExpiresAt:          expiresAt,
			SecondsToExpiry:    int64(expiresAt.Sub(now).Seconds()),
			LastProbe:          item.probeResult,
			LastProbeAt:        item.lastProbeAt,
			LastProbeReason:    item.probeReason,
			Probing:            item.probing,
			RefreshPending:     item.refreshPending,
			NextRefreshAt:      item.nextRefresh,
			SecondsToRefresh:   int64(item.nextRefresh.Sub(now).Seconds()),
			QuotaBackoff:       item.blockedUntil.After(now),
			SeededFromConfig:   seeded[item.key],
			LastProbeSucceeded: item.probeResult == "ok",
		}
		if entry.SecondsToExpiry < 0 {
			entry.SecondsToExpiry = 0
		}
		if item.blockedUntil.After(now) {
			blocked := item.blockedUntil
			entry.BlockedUntil = &blocked
		}
		if info, ok := accounts[digest]; ok {
			entry.Email = info.Email
			entry.AuthIndex = info.AuthIndex
			entry.AuthDisabled = info.Disabled
			entry.AuthUnavailable = info.Unavailable
		}
		if model != "" {
			modelSet[model] = true
		}
		if managed {
			configuredAuths[authID]++
		}
		counters.Total++
		if valid {
			counters.Valid++
		}
		if item.probing {
			counters.Probing++
		}
		if item.refreshPending != "" {
			counters.Pending++
		}
		if entry.QuotaBackoff {
			counters.QuotaBack++
		}
		if !compatible {
			counters.Incompatible++
		}
		if item.current.Value == "" {
			counters.Empty++
		}
		if item.probeResult != "" && item.probeResult != "ok" {
			counters.Failed++
		}
		entries = append(entries, entry)
		visibleAccounts[digest] = true
	}
	counters.Models = len(modelSet)
	if resolved {
		counters.Accounts = len(visibleAccounts)
	} else {
		counters.Accounts = len(accounts)
	}

	configured := make([]configuredView, 0, len(cfg.Credentials))
	for rawAuthID, credential := range cfg.Credentials {
		authID := strings.TrimSpace(rawAuthID)
		digest := accountDigest(authID)
		policy, _ := credentialFor(cfg, authID)
		view := configuredView{
			Account:        digest,
			Plan:           policy.Plan,
			NormalBlocks:   policy.NormalBlocks,
			AcceptedBlocks: policy.AcceptedBlocks,
			Models:         policy.Models,
			StateModel:     policy.StateModel,
			AutoUpdate:     autoUpdateEnabled(cfg, policy),
			Seeded:         strings.TrimSpace(credential.State) != "",
			HasState:       configuredAuths[authID] > 0,
			EntryCount:     configuredAuths[authID],
		}
		if info, ok := accounts[digest]; ok {
			view.Email = info.Email
			view.AuthIndex = info.AuthIndex
			view.Disabled = info.Disabled
		}
		configured = append(configured, view)
	}
	sort.Slice(configured, func(i, j int) bool { return configured[i].Account < configured[j].Account })

	historyViews := make([]historyView, 0, len(history))
	for index := len(history) - 1; index >= 0; index-- {
		item := history[index]
		view := historyView{At: item.At, Account: item.Account, Model: item.Model, Event: item.Event, Outcome: item.Outcome, Reason: item.Reason, Detail: item.Detail}
		if info, ok := accounts[item.Account]; ok {
			view.Email = info.Email
		}
		historyViews = append(historyViews, view)
	}

	background := false
	state.mu.Lock()
	background = state.workerDone != nil
	state.mu.Unlock()

	view := statusView{
		Plugin:               pluginName,
		Version:              pluginVersion,
		Schema:               1,
		Now:                  now,
		StateTTLSeconds:      int64(turnStateTTL.Seconds()),
		ProbeEnabled:         cfg.Probe.Enabled,
		BackgroundRefresh:    background,
		RefreshBeforeSeconds: cfg.Probe.RefreshBeforeSeconds,
		Probe: probeConfigView{
			Enabled:              cfg.Probe.Enabled,
			BackgroundRefresh:    enabledByDefault(cfg.Probe.BackgroundRefresh),
			RefreshOnErrors:      enabledByDefault(cfg.Probe.RefreshOnErrors),
			InjectExpired:        cfg.InjectExpired,
			TimeoutSeconds:       cfg.Probe.TimeoutSeconds,
			RetrySeconds:         cfg.Probe.RetrySeconds,
			RefreshBeforeSeconds: cfg.Probe.RefreshBeforeSeconds,
			MaxAttempts:          cfg.Probe.MaxAttempts,
			QuotaBackoffSeconds:  cfg.Probe.QuotaBackoffSeconds,
			ProxyPoolSize:        len(cfg.Probe.ProxyPool),
			FirstProxy:           cfg.Probe.FirstProxy != nil,
			ProxySource:          proxySource(cfg.Probe),
			LiveProbes:           liveProbes,
			Accepting:            state.accepting,
		},
		Counters:         counters,
		Entries:          entries,
		Configured:       configured,
		History:          historyViews,
		AccountsResolved: resolved,
	}
	if cfg.Defaults != nil {
		view.Defaults = &defaultsView{
			Plan:           cfg.Defaults.Plan,
			NormalBlocks:   cfg.Defaults.NormalBlocks,
			AcceptedBlocks: cfg.Defaults.AcceptedBlocks,
			Models:         cfg.Defaults.Models,
			AutoUpdate:     autoUpdateEnabled(cfg, *cfg.Defaults),
		}
	}
	return view
}

// proxySource summarizes where probe proxies come from without exposing secrets.
func proxySource(cfg probeConfig) string {
	sources := make(map[string]bool, 3)
	for _, entry := range cfg.ProxyPool {
		switch {
		case strings.TrimSpace(entry.URLFile) != "":
			sources["file"] = true
		case strings.TrimSpace(entry.URLEnv) != "":
			sources["env"] = true
		case strings.TrimSpace(entry.URL) != "":
			sources["inline"] = true
		}
	}
	if cfg.FirstProxy != nil {
		sources["first_proxy"] = true
	}
	if len(sources) == 0 {
		return ""
	}
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, "+")
}

// --- panel actions ----------------------------------------------------------

type refreshRequest struct {
	All      bool     `json:"all"`
	Keys     []string `json:"keys"`
	Accounts []string `json:"accounts"`
	Models   []string `json:"models"`
}

type actionTarget struct {
	Key     string `json:"key"`
	Account string `json:"account"`
	Model   string `json:"model"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type refreshResult struct {
	Queued  []actionTarget `json:"queued"`
	Skipped []actionTarget `json:"skipped"`
	Reason  string         `json:"reason,omitempty"`
	Message string         `json:"message,omitempty"`
}

// refreshAction queues a manual probe. Quota backoff and the short retry window
// still apply, so an impatient click cannot hammer a limited account.
func (state *runtimeState) refreshAction(body []byte) refreshResult {
	var request refreshRequest
	if len(body) > 0 {
		if errUnmarshal := json.Unmarshal(body, &request); errUnmarshal != nil {
			return refreshResult{Reason: "invalid_body", Message: "请求体不是有效的 JSON"}
		}
	}
	state.mu.Lock()
	cfg := state.config
	now := state.now()
	if !state.accepting {
		state.mu.Unlock()
		return refreshResult{Reason: "quiesced", Message: "插件正在停止或重载，暂不接受手动刷新"}
	}
	if !cfg.Probe.Enabled {
		state.mu.Unlock()
		return refreshResult{Reason: "probe_disabled", Message: "探测已在插件配置中关闭"}
	}
	accounts := make(map[string]bool, len(request.Accounts))
	for _, value := range request.Accounts {
		accounts[strings.TrimSpace(value)] = true
	}
	models := make(map[string]bool, len(request.Models))
	for _, value := range request.Models {
		models[strings.ToLower(strings.TrimSpace(value))] = true
	}
	handles := make(map[string]bool, len(request.Keys))
	for _, value := range request.Keys {
		handles[strings.TrimSpace(value)] = true
	}
	selectAll := request.All

	candidates := make(map[string]bool)
	for key := range state.current {
		candidates[key] = true
	}
	for key := range state.probeResults {
		candidates[key] = true
	}
	for key := range state.probing {
		candidates[key] = true
	}

	result := refreshResult{Queued: []actionTarget{}, Skipped: []actionTarget{}}
	start := make([]string, 0, len(candidates))
	for key := range candidates {
		authID, model := splitStateKey(key)
		if authID == "" || model == "" {
			continue
		}
		policy, managed := credentialFor(cfg, authID)
		if !managed {
			continue
		}
		target := actionTarget{Key: entryHandle(key), Account: accountDigest(authID), Model: model}
		switch {
		case !selectAll && !handles[target.Key] && !accounts[target.Account] && !models[strings.ToLower(model)]:
			continue
		case state.probeResults[key] == "auth_unavailable":
			result.Skipped = append(result.Skipped, skipTarget(target, "unprobeable", "该凭证不能用于 Codex 探测"))
			continue
		case !autoUpdateEnabled(cfg, policy):
			result.Skipped = append(result.Skipped, skipTarget(target, "out_of_scope", "该账号未开启 auto_update"))
			continue
		case !matchesModels(policy.Models, model):
			result.Skipped = append(result.Skipped, skipTarget(target, "out_of_scope", "该模型不在账号的 models 范围内"))
			continue
		case state.probing[key]:
			result.Skipped = append(result.Skipped, skipTarget(target, "in_flight", "已在探测中"))
			continue
		case state.blockedUntil[key].After(now):
			result.Skipped = append(result.Skipped, skipTarget(target, "quota_backoff", "额度退避中，稍后会自动重试"))
			continue
		case state.lastProbe[key].Add(time.Duration(cfg.Probe.RetrySeconds) * time.Second).After(now):
			result.Skipped = append(result.Skipped, skipTarget(target, "cooldown", "处于探测冷却窗口"))
			continue
		}
		state.refreshRequests[key] = "manual"
		state.probeReasons[key] = "manual"
		result.Queued = append(result.Queued, target)
		start = append(start, key)
	}
	if len(start) > 0 {
		state.appendHistoryLocked(historyEntry{At: now, Event: "manual_refresh", Reason: "manual", Detail: fmt.Sprintf("排队 %d 个条目", len(start))})
	}
	hasWorker := state.workerDone != nil
	state.mu.Unlock()

	if hasWorker {
		select {
		case state.wake <- struct{}{}:
		default:
		}
	} else {
		// Without the background worker the queue would never drain, so run one
		// bounded probe round directly. ensureProbe keeps the single-flight and
		// cooldown guarantees.
		go func(keys []string) {
			for _, key := range keys {
				authID, model := splitStateKey(key)
				if authID == "" || model == "" {
					continue
				}
				state.ensureProbe(authID, model)
			}
		}(start)
	}
	if result.Message == "" {
		if len(result.Queued) == 0 {
			result.Message = "没有可刷新的条目"
		} else {
			result.Message = fmt.Sprintf("已排队 %d 个条目", len(result.Queued))
		}
	}
	return result
}

func skipTarget(target actionTarget, reason, message string) actionTarget {
	target.Reason = reason
	target.Message = message
	return target
}

type clearRequest struct {
	Scope    string   `json:"scope"`
	All      bool     `json:"all"`
	Keys     []string `json:"keys"`
	Accounts []string `json:"accounts"`
	Models   []string `json:"models"`
}

type clearResult struct {
	Removed   []actionTarget `json:"removed"`
	Remaining int            `json:"remaining"`
	Persisted bool           `json:"persisted"`
	Reason    string         `json:"reason,omitempty"`
	Message   string         `json:"message,omitempty"`
	Failed    string         `json:"failed,omitempty"`
}

// clearAction drops cached states. The default scope only removes entries that
// can no longer be injected, so a valid state is never lost by accident.
func (state *runtimeState) clearAction(body []byte) (clearResult, error) {
	var request clearRequest
	if len(body) > 0 {
		if errUnmarshal := json.Unmarshal(body, &request); errUnmarshal != nil {
			return clearResult{}, fmt.Errorf("请求体不是有效的 JSON")
		}
	}
	scope := strings.ToLower(strings.TrimSpace(request.Scope))
	if scope == "" {
		scope = "invalid"
	}
	if scope != "invalid" && scope != "expired" && scope != "all" {
		return clearResult{}, fmt.Errorf("scope 必须是 invalid、expired 或 all")
	}
	if scope == "all" && !request.All {
		return clearResult{}, fmt.Errorf("清除全部缓存必须显式传入 all=true")
	}
	accounts := make(map[string]bool, len(request.Accounts))
	for _, value := range request.Accounts {
		accounts[strings.TrimSpace(value)] = true
	}
	models := make(map[string]bool, len(request.Models))
	for _, value := range request.Models {
		models[strings.ToLower(strings.TrimSpace(value))] = true
	}
	handles := make(map[string]bool, len(request.Keys))
	for _, value := range request.Keys {
		handles[strings.TrimSpace(value)] = true
	}
	selective := len(accounts) > 0 || len(models) > 0 || len(handles) > 0

	state.mu.Lock()
	now := state.now()
	result := clearResult{Removed: []actionTarget{}}
	for key, current := range state.current {
		authID, model := splitStateKey(key)
		if authID == "" || model == "" {
			continue
		}
		target := actionTarget{Key: entryHandle(key), Account: accountDigest(authID), Model: model}
		if selective && !handles[target.Key] && !accounts[target.Account] && !models[strings.ToLower(model)] {
			continue
		}
		expiresAt := current.IssuedAt.Add(turnStateTTL)
		expired := current.Value == "" || !now.Before(expiresAt)
		policy, managed := credentialFor(state.config, authID)
		valid := current.Value != "" && !current.IssuedAt.After(now.Add(5*time.Minute)) && now.Before(expiresAt)
		compatible := managed && matchesModels(policy.Models, model) && normalBlockCount(policy, current.Blocks)
		switch scope {
		case "invalid":
			if valid && compatible {
				continue
			}
		case "expired":
			if !expired {
				continue
			}
		}
		delete(state.current, key)
		delete(state.refreshRequests, key)
		result.Removed = append(result.Removed, target)
	}
	// Probe bookkeeping for entries with no state left is dropped as well, so the
	// panel does not resurrect rows that no longer exist.
	for key := range state.probeResults {
		authID, model := splitStateKey(key)
		if _, exists := state.current[key]; exists {
			continue
		}
		target := actionTarget{Key: entryHandle(key), Account: accountDigest(authID), Model: model}
		if selective && !handles[target.Key] && !accounts[target.Account] && !models[strings.ToLower(model)] {
			continue
		}
		delete(state.probeResults, key)
		delete(state.probeReasons, key)
		delete(state.lastProbe, key)
		delete(state.blockedUntil, key)
		delete(state.refreshRequests, key)
	}
	result.Remaining = len(state.current)
	if len(result.Removed) > 0 {
		state.appendHistoryLocked(historyEntry{At: now, Event: "clear", Reason: scope, Detail: fmt.Sprintf("清理 %d 个条目", len(result.Removed))})
	}
	path := state.config.StateFile
	state.mu.Unlock()

	if path != "" {
		if errPersist := state.persistCurrent(path); errPersist != nil {
			result.Failed = safeError(errPersist)
		} else {
			result.Persisted = true
		}
	}
	if result.Message == "" {
		if len(result.Removed) == 0 {
			result.Message = "没有需要清理的条目"
		} else {
			result.Message = fmt.Sprintf("已清理 %d 个条目", len(result.Removed))
		}
	}
	return result, nil
}

func (state *runtimeState) appendHistoryLocked(entry historyEntry) {
	if entry.Reason == "" && entry.Outcome == "" {
		return
	}
	state.history = append(state.history, entry)
	if len(state.history) > historyLimit {
		state.history = append([]historyEntry(nil), state.history[len(state.history)-historyLimit:]...)
	}
}

// recordProbeHistory is unnecessary: probe bookkeeping already holds the runtime
// lock while writing outcomes, so history is appended there.
