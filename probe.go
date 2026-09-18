package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type probeConfig struct {
	Enabled              bool            `yaml:"enabled"`
	FirstProxy           *proxyEndpoint  `yaml:"first_proxy"`
	ProxyPool            []proxyEndpoint `yaml:"proxy_pool"`
	TimeoutSeconds       int             `yaml:"timeout_seconds"`
	RetrySeconds         int             `yaml:"retry_seconds"`
	RefreshBeforeSeconds int             `yaml:"refresh_before_seconds"`
	MaxAttempts          int             `yaml:"max_attempts"`
	BackgroundRefresh    *bool           `yaml:"background_refresh"`
	RefreshOnErrors      *bool           `yaml:"refresh_on_errors"`
	ProbeOnErrorsOnly    *bool           `yaml:"probe_on_errors_only"`
	QuotaBackoffSeconds  int             `yaml:"quota_backoff_seconds"`
}

func normalizeProbe(cfg *probeConfig) error {
	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = 15
	}
	if cfg.RetrySeconds == 0 {
		cfg.RetrySeconds = 60
	}
	if cfg.RefreshBeforeSeconds == 0 {
		cfg.RefreshBeforeSeconds = 300
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 1
	}
	if cfg.QuotaBackoffSeconds == 0 {
		cfg.QuotaBackoffSeconds = 900
	}
	if cfg.QuotaBackoffSeconds < 60 || cfg.QuotaBackoffSeconds > 86400 {
		return errors.New("quota_backoff_seconds must be between 60 and 86400")
	}
	if cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 180 || cfg.RetrySeconds < 1 || cfg.RefreshBeforeSeconds < 0 || cfg.RefreshBeforeSeconds >= 3600 || cfg.MaxAttempts < 1 || cfg.MaxAttempts > 20 {
		return errors.New("invalid probe limits")
	}
	if !cfg.Enabled {
		return nil
	}
	if len(cfg.ProxyPool) == 0 || len(cfg.ProxyPool) > 100 {
		return errors.New("enabled probe requires 1 to 100 proxy_pool entries")
	}
	entries := append([]proxyEndpoint(nil), cfg.ProxyPool...)
	if cfg.FirstProxy != nil {
		entries = append(entries, *cfg.FirstProxy)
	}
	for _, entry := range entries {
		sources := 0
		for _, s := range []string{entry.URL, entry.URLFile, entry.URLEnv} {
			if s != "" {
				sources++
			}
		}
		if sources != 1 {
			return errors.New("each proxy requires exactly one url, url_file or url_env")
		}
		if strings.ContainsAny(entry.ConnectHost, "\r\n") {
			return errors.New("invalid proxy connect_host")
		}
		if _, err := entry.resolve(); err != nil {
			return err
		}
	}
	return nil
}

type probeAuth struct {
	AccessToken string `json:"access_token"`
	AccountID   string `json:"account_id"`
	Type        string `json:"type"`
	BaseURL     string `json:"base_url"`
	Disabled    bool   `json:"disabled"`
}

func (state *runtimeState) selectedProbeAuth(authID string) (probeAuth, error) {
	if state.hostCall == nil {
		return probeAuth{}, errors.New("host auth callbacks unavailable")
	}
	var listing struct {
		Files []struct {
			ID       string `json:"id"`
			Index    string `json:"auth_index"`
			Provider string `json:"provider"`
			Disabled bool   `json:"disabled"`
			BaseURL  string `json:"base_url"`
		} `json:"files"`
	}
	if err := state.hostCall("host.auth.list", struct{}{}, &listing); err != nil {
		return probeAuth{}, err
	}
	for _, entry := range listing.Files {
		if entry.ID != authID {
			continue
		}
		if entry.Disabled || entry.Provider != "codex" || entry.BaseURL != "" {
			return probeAuth{}, errors.New("auth not eligible for Codex probe")
		}
		var result struct {
			JSON probeAuth `json:"json"`
		}
		if err := state.hostCall("host.auth.get", map[string]string{"auth_index": entry.Index}, &result); err != nil {
			return probeAuth{}, err
		}
		auth := result.JSON
		// Never forward arbitrary client Authorization or third-party API keys.
		// Tokens are obtained only from the host-selected Codex OAuth credential.
		if auth.Type != "codex" || auth.Disabled || auth.BaseURL != "" || auth.AccessToken == "" {
			return probeAuth{}, errors.New("Codex OAuth credential unavailable")
		}
		return auth, nil
	}
	return probeAuth{}, errors.New("selected auth not found")
}

func (state *runtimeState) ensureProbe(authID, model string) {
	key := stateKey(authID, model)
	state.mu.Lock()
	cfg := state.config
	policy, ok := credentialFor(cfg, authID)
	now := state.now()
	if !state.accepting || !cfg.Probe.Enabled || state.probeCtx == nil || state.probeCtx.Err() != nil || !ok || !autoUpdateEnabled(cfg, policy) || !matchesModels(policy.Models, model) || state.probing[key] || len(state.probing) >= 4 {
		state.mu.Unlock()
		return
	}
	current := state.current[key]
	reason := state.refreshRequests[key]
	if enabledByDefault(cfg.Probe.ProbeOnErrorsOnly) && reason == "" {
		state.mu.Unlock()
		return
	}
	if reason == "" && current.Value != "" && !current.IssuedAt.After(now.Add(5*time.Minute)) && now.Before(current.IssuedAt.Add(turnStateTTL-time.Duration(cfg.Probe.RefreshBeforeSeconds)*time.Second)) {
		state.mu.Unlock()
		return
	}
	if now.Before(state.blockedUntil[key]) {
		state.mu.Unlock()
		return
	}
	if now.Before(state.accountBlockedUntil[authID]) {
		state.mu.Unlock()
		return
	}
	if last, ok := state.lastProbe[key]; ok && now.Sub(last) < time.Duration(cfg.Probe.RetrySeconds)*time.Second {
		state.mu.Unlock()
		return
	}
	state.lastProbe[key] = now
	if reason == "" {
		if current.Value != "" {
			reason = "before_expiry"
		} else {
			reason = "missing_state"
		}
	}
	state.probeReasons[key] = reason
	state.probing[key] = true
	generation := state.generation
	ctx, cancel := context.WithTimeout(state.probeCtx, time.Duration(cfg.Probe.TimeoutSeconds)*time.Second)
	start := state.poolCursor
	state.poolCursor++
	state.mu.Unlock()
	defer cancel()
	auth, err := state.selectedProbeAuth(authID)
	var candidate storedState
	outcome := "auth_unavailable"
	if err == nil {
		outcome = "probe_failed"
		attempts := 0
		poolSize := len(cfg.Probe.ProxyPool)
		for offset := 0; offset < poolSize && attempts < cfg.Probe.MaxAttempts && ctx.Err() == nil; offset++ {
			endpointIndex := (int(start) + offset) % poolSize
			state.mu.Lock()
			cooling := state.now().Before(state.proxyBlockedUntil[endpointIndex])
			state.mu.Unlock()
			if cooling {
				continue
			}
			attempts++
			// All attempts share one total deadline; none can escape the chain.
			endpoint := cfg.Probe.ProxyPool[endpointIndex]
			attemptCtx, attemptCancel := context.WithTimeout(ctx, time.Duration(cfg.Probe.TimeoutSeconds)*time.Second/time.Duration(cfg.Probe.MaxAttempts))
			value, status := state.fetch(attemptCtx, auth, model, cfg.Probe.FirstProxy, endpoint)
			attemptCancel()
			outcome = status
			if status != "ok" {
				if accountLevelProbeFailure(status) {
					state.mu.Lock()
					state.accountBlockedUntil[authID] = state.now().Add(probeAccountBackoff(status, cfg.Probe.QuotaBackoffSeconds))
					state.mu.Unlock()
					break
				}
				if proxyLevelProbeFailure(status) {
					state.mu.Lock()
					state.proxyBlockedUntil[endpointIndex] = state.now().Add(time.Duration(cfg.Probe.RetrySeconds) * time.Second)
					state.mu.Unlock()
				}
				continue
			}
			state.mu.Lock()
			delete(state.proxyBlockedUntil, endpointIndex)
			state.mu.Unlock()
			parsed, parseErr := parseTurnState(value, cfg.MaxStateBytes)
			if parseErr != nil || !normalBlockCount(policy, parsed.Blocks) || parsed.IssuedAt.After(now.Add(5*time.Minute)) || !now.Before(parsed.IssuedAt.Add(turnStateTTL)) {
				outcome = "state_rejected"
				continue
			}
			candidate = storedState{Value: value, IssuedAt: parsed.IssuedAt, Blocks: parsed.Blocks}
			break
		}
	}
	state.mu.Lock()
	if generation != state.generation {
		state.mu.Unlock()
		return
	}
	delete(state.probing, key)
	state.probeResults[key] = outcome
	if strings.Contains(outcome, "usage_limit_reached") || strings.Contains(outcome, "insufficient_quota") {
		state.blockedUntil[key] = state.now().Add(time.Duration(cfg.Probe.QuotaBackoffSeconds) * time.Second)
	}
	promoted := false
	if state.accepting && candidate.Value != "" && candidate.IssuedAt.After(state.current[key].IssuedAt) && state.now().Before(candidate.IssuedAt.Add(turnStateTTL)) {
		state.current[key] = candidate
		delete(state.refreshRequests, key)
		delete(state.blockedUntil, key)
		promoted = true
	}
	state.appendHistoryLocked(historyEntry{
		At:      state.now(),
		Account: accountDigest(authID),
		Model:   model,
		Event:   "probe",
		Outcome: outcome,
		Reason:  reason,
	})
	if promoted {
		delete(state.accountBlockedUntil, authID)
		state.forceInject[key] = true
		state.appendHistoryLocked(historyEntry{
			At:      state.now(),
			Account: accountDigest(authID),
			Model:   model,
			Event:   "promote",
			Outcome: "ok",
			Reason:  reason,
			Detail:  fmt.Sprintf("blocks=%d length=%d", candidate.Blocks, len(candidate.Value)),
		})
	}
	path := state.config.StateFile
	state.mu.Unlock()
	if promoted && path != "" {
		if state.persistCurrent(path) != nil {
			state.mu.Lock()
			state.probeResults[key] = "persist_failed"
			state.mu.Unlock()
		}
	}
}

// Probe outcomes and promotions are recorded in state.history for the panel.
// Probes use a tiny independent prompt, never the user's business payload.
// Success requires response.completed; an HTTP 200 with a failed SSE is rejected.
func fetchProbe(ctx context.Context, auth probeAuth, model string, first *proxyEndpoint, second proxyEndpoint) (string, string) {
	body, _ := json.Marshal(map[string]any{
		"model": model, "stream": true, "store": false, "instructions": "Reply with OK only.",
		"input": []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": "ping"}}}},
	})
	transport := chainTransport(first, second)
	dial := transport.DialContext
	// net/http can detach its dial context from request cancellation. Bind the
	// full SOCKS/CONNECT handshake to this probe's total budget explicitly.
	transport.DialContext = func(_ context.Context, network, address string) (net.Conn, error) {
		return dial(ctx, network, address)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(body))
	if err != nil {
		return "", "request_invalid"
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	if auth.AccountID != "" {
		req.Header.Set("ChatGPT-Account-ID", auth.AccountID)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("User-Agent", "codex_cli_rs/0.101.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", "network_error"
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		status := "upstream_http_" + httpStatus(resp.StatusCode)
		var failure struct {
			Error struct {
				Code string `json:"code"`
				Type string `json:"type"`
			} `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&failure) == nil {
			for _, code := range []string{failure.Error.Code, failure.Error.Type} {
				switch code {
				case "usage_limit_reached", "rate_limit_exceeded", "insufficient_quota", "model_not_found", "invalid_api_key":
					return "", status + "_" + code
				}
			}
		}
		return "", status
	}
	value := strings.TrimSpace(resp.Header.Get(turnStateHeader))
	if !probeCompleted(io.LimitReader(resp.Body, 1<<20)) {
		return "", "upstream_incomplete"
	}
	if value == "" {
		return "", "state_missing"
	}
	return value, "ok"
}

func httpStatus(status int) string {
	b, _ := json.Marshal(status)
	return string(b)
}

// Account-level failures are deterministic for the selected credential; trying
// another exit proxy cannot repair them and only spends more quota.
func accountLevelProbeFailure(status string) bool {
	return strings.Contains(status, "upstream_http_401") ||
		strings.Contains(status, "upstream_http_403") ||
		strings.Contains(status, "upstream_http_429") ||
		strings.Contains(status, "usage_limit_reached") ||
		strings.Contains(status, "rate_limit_exceeded") ||
		strings.Contains(status, "insufficient_quota") ||
		strings.Contains(status, "invalid_api_key")
}

func probeAccountBackoff(status string, quotaSeconds int) time.Duration {
	if strings.Contains(status, "usage_limit_reached") || strings.Contains(status, "rate_limit_exceeded") || strings.Contains(status, "insufficient_quota") {
		return time.Duration(quotaSeconds) * time.Second
	}
	return time.Minute
}

// Transport/proxy failures are isolated to the selected pool entry. Upstream
// 5xx remains retryable across exits without poisoning a proxy that may be fine.
func proxyLevelProbeFailure(status string) bool {
	return status == "network_error" || strings.Contains(status, "proxy") || strings.Contains(status, "connect") || strings.Contains(status, "tls")
}

func probeCompleted(reader io.Reader) bool {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 256<<10)
	var data []string
	check := func() (bool, bool) {
		var event struct {
			Type     string `json:"type"`
			Response struct {
				Status string `json:"status"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(strings.Join(data, "\n")), &event) != nil {
			return false, false
		}
		switch event.Type {
		case "response.completed":
			return true, event.Response.Status == "completed"
		case "response.failed", "response.incomplete", "error":
			return true, false
		}
		return false, false
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if done, ok := check(); done {
				return ok
			}
			data = nil
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if scanner.Err() != nil {
		return false
	}
	done, ok := check()
	return done && ok
}
