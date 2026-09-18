package main

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

func enabledByDefault(value *bool) bool { return value == nil || *value }

// Caller holds mu. Each configuration generation owns one worker and context.
func (state *runtimeState) startBackgroundLocked() {
	state.workerDone = nil
	state.wake = make(chan struct{}, 1)
	if !state.accepting || !state.config.Probe.Enabled || !enabledByDefault(state.config.Probe.BackgroundRefresh) {
		return
	}
	done := make(chan struct{})
	state.workerDone = done
	go state.refreshLoop(state.probeCtx, state.wake, done)
}

func (state *runtimeState) stopBackground() {
	state.mu.Lock()
	if state.probeCancel != nil {
		state.probeCancel()
	}
	done := state.workerDone
	state.mu.Unlock()
	// Never hold mu while joining: a canceled probe still needs to finish its
	// bookkeeping. Quiesce must drain the worker before host callbacks retire.
	if done != nil {
		<-done
	}
	state.mu.Lock()
	if state.workerDone == done {
		state.workerDone = nil
	}
	state.mu.Unlock()
}

func (state *runtimeState) refreshLoop(ctx context.Context, wake <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		state.refreshDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-wake:
		}
	}
}

// Deterministic due-work selection is separated from real timers for testing.
func (state *runtimeState) refreshDue(ctx context.Context) {
	type job struct {
		key string
		due time.Time
	}
	state.mu.Lock()
	keys := make(map[string]bool)
	for key := range state.current {
		keys[key] = true
	}
	for key := range state.refreshRequests {
		keys[key] = true
	}
	jobs := make([]job, 0, len(keys))
	now := state.now()
	for key := range keys {
		due := state.nextRefreshLocked(key)
		if !now.Before(due) {
			jobs = append(jobs, job{key, due})
		}
	}
	state.mu.Unlock()
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].due.Equal(jobs[j].due) {
			return jobs[i].key < jobs[j].key
		}
		return jobs[i].due.Before(jobs[j].due)
	})
	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		auth, model := splitStateKey(job.key)
		if auth != "" && model != "" {
			state.ensureProbe(auth, model)
		}
	}
}

func (state *runtimeState) nextRefreshLocked(key string) time.Time {
	due := state.now()
	if enabledByDefault(state.config.Probe.ProbeOnErrorsOnly) && state.refreshRequests[key] == "" {
		return due.Add(365 * 24 * time.Hour)
	}
	if current := state.current[key]; current.Value != "" && state.refreshRequests[key] == "" {
		due = current.IssuedAt.Add(turnStateTTL - time.Duration(state.config.Probe.RefreshBeforeSeconds)*time.Second)
	}
	if last, ok := state.lastProbe[key]; ok {
		retry := last.Add(time.Duration(state.config.Probe.RetrySeconds) * time.Second)
		if retry.After(due) {
			due = retry
		}
	}
	if state.blockedUntil[key].After(due) {
		due = state.blockedUntil[key]
	}
	return due
}

func (state *runtimeState) queueRefreshLocked(key, reason string) {
	if !state.accepting || !state.config.Probe.Enabled || !enabledByDefault(state.config.Probe.RefreshOnErrors) || key == "" || reason == "" {
		return
	}
	auth, model := splitStateKey(key)
	policy, ok := credentialFor(state.config, auth)
	if !ok || !autoUpdateEnabled(state.config, policy) || !matchesModels(policy.Models, model) {
		return
	}
	// Coalesce a burst into one pending refresh. Existing good state survives.
	if state.refreshRequests[key] == "" {
		state.refreshRequests[key] = reason
	}
	select {
	case state.wake <- struct{}{}:
	default:
	}
}

func failureRefreshReason(status int, message string) string {
	if status == 429 {
		return "rate_limit"
	}
	if status == 502 || status == 503 || status == 504 {
		return "upstream_unavailable"
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "overload") {
		return "overload"
	}
	for _, code := range []string{"rate_limit", "usage_limit_reached", "insufficient_quota", "too many requests"} {
		if strings.Contains(lower, code) {
			return "rate_limit"
		}
	}
	return ""
}

func (state *runtimeState) observeStreamFailure(requestID string, chunk []byte) {
	state.mu.Lock()
	defer state.mu.Unlock()
	binding, ok := state.requests[requestID]
	if !ok || len(chunk) == 0 {
		return
	}
	// Host normally delivers one SSE event per chunk; retain bounded fragments
	// for split/multiline events. Never classify assistant text as an error.
	if len(binding.StreamBuffer)+len(chunk) > 64<<10 {
		binding.StreamBuffer = nil
		state.requests[requestID] = binding
		return
	}
	buffer := append(binding.StreamBuffer, chunk...)
	buffer = bytes.ReplaceAll(buffer, []byte("\r\n"), []byte("\n"))
	for {
		end := bytes.Index(buffer, []byte("\n\n"))
		if end < 0 {
			break
		}
		frame := buffer[:end]
		buffer = buffer[end+2:]
		var data []string
		for _, line := range strings.Split(string(frame), "\n") {
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if reason := streamFailureReason([]byte(strings.Join(data, "\n"))); reason != "" {
			state.queueRefreshLocked(binding.Key, reason)
			delete(state.candidates, requestID)
		}
	}
	// Some hosts send a raw JSON event rather than SSE framing.
	if json.Valid(buffer) {
		if reason := streamFailureReason(buffer); reason != "" {
			state.queueRefreshLocked(binding.Key, reason)
			delete(state.candidates, requestID)
		}
		buffer = nil
	}
	binding.StreamBuffer = append([]byte(nil), buffer...)
	state.requests[requestID] = binding
}

func streamFailureReason(raw []byte) string {
	type upstreamError struct {
		Code       string `json:"code"`
		Type       string `json:"type"`
		Message    string `json:"message"`
		StatusCode int    `json:"status_code"`
	}
	var event struct {
		Type       string        `json:"type"`
		StatusCode int           `json:"status_code"`
		Code       string        `json:"code"`
		Message    string        `json:"message"`
		Error      upstreamError `json:"error"`
		Response   struct {
			Error upstreamError `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return ""
	}
	if event.Type != "error" && event.Type != "response.failed" && event.Type != "response.incomplete" {
		return ""
	}
	for _, e := range []upstreamError{event.Error, event.Response.Error, {Code: event.Code, Message: event.Message, StatusCode: event.StatusCode}} {
		if reason := failureRefreshReason(e.StatusCode, e.Code+" "+e.Type+" "+e.Message); reason != "" {
			return reason
		}
	}
	return ""
}
