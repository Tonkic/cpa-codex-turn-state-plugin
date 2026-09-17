package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	pluginName        = "cpa-codex-turn-state"
	pluginVersion     = "0.1.0"
	pluginSchema      = uint32(4)
	pluginABIVersion  = uint32(1)
	defaultMaxBytes   = 4096
	maxStateFileBytes = 16 << 20
	turnStateHeader   = "X-Codex-Turn-State"
)

const (
	methodPluginRegister               = "plugin.register"
	methodPluginQuiesce                = "plugin.quiesce"
	methodPluginReconfigure            = "plugin.reconfigure"
	methodPluginShutdown               = "plugin.shutdown"
	methodRequestInterceptBefore       = "request.intercept_before"
	methodRequestInterceptAfter        = "request.intercept_after"
	methodRequestComplete              = "request.complete"
	methodResponseInterceptAfter       = "response.intercept_after"
	methodResponseInterceptStreamChunk = "response.intercept_stream_chunk"
)

type pluginConfig struct {
	Enabled       bool                        `yaml:"enabled"`
	Priority      int                         `yaml:"priority"`
	AutoUpdate    *bool                       `yaml:"auto_update"`
	InjectExpired bool                        `yaml:"inject_expired"`
	StateFile     string                      `yaml:"state_file"`
	MaxStateBytes int                         `yaml:"max_state_bytes"`
	Credentials   map[string]credentialConfig `yaml:"credentials"`
}

type credentialConfig struct {
	Plan         string   `yaml:"plan"`
	NormalBlocks int      `yaml:"normal_blocks"`
	State        string   `yaml:"state"`
	Models       []string `yaml:"models"`
	AutoUpdate   *bool    `yaml:"auto_update"`
}

type storedState struct {
	Value    string    `json:"state"`
	IssuedAt time.Time `json:"issued_at"`
	Blocks   int       `json:"blocks"`
}

type persistedFile struct {
	Version     int                    `json:"version"`
	Credentials map[string]storedState `json:"credentials"`
}

type requestBinding struct {
	AuthID string
}

type stateCandidate struct {
	AuthID string
	State  storedState
}

type runtimeState struct {
	mu         sync.Mutex
	persistMu  sync.Mutex
	now        func() time.Time
	accepting  bool
	config     pluginConfig
	current    map[string]storedState
	requests   map[string]requestBinding
	candidates map[string]stateCandidate
}

var runtime = newRuntimeState()

func newRuntimeState() *runtimeState {
	return &runtimeState{
		now:        time.Now,
		current:    make(map[string]storedState),
		requests:   make(map[string]requestBinding),
		candidates: make(map[string]stateCandidate),
	}
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      registrationMetadata   `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationMetadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	Logo             string        `json:"Logo"`
	ConfigFields     []configField `json:"ConfigFields"`
}

type configField struct {
	Name        string `json:"Name"`
	Type        string `json:"Type"`
	Description string `json:"Description"`
}

type registrationCapability struct {
	RequestInterceptor     bool `json:"request_interceptor"`
	RequestLifecyclePlugin bool `json:"request_lifecycle_plugin"`
	ResponseInterceptor    bool `json:"response_interceptor"`
	StreamChunkInterceptor bool `json:"response_stream_interceptor"`
}

type requestInterceptRequest struct {
	RequestID      string         `json:"RequestID"`
	TraceID        string         `json:"TraceID"`
	SourceFormat   string         `json:"SourceFormat"`
	ToFormat       string         `json:"ToFormat"`
	Model          string         `json:"Model"`
	RequestedModel string         `json:"RequestedModel"`
	Stream         bool           `json:"Stream"`
	Headers        http.Header    `json:"Headers"`
	Body           []byte         `json:"Body"`
	Metadata       map[string]any `json:"Metadata"`
}

type requestInterceptResponse struct {
	Headers http.Header `json:"Headers,omitempty"`
}

type responseInterceptRequest struct {
	RequestID       string         `json:"RequestID"`
	ResponseHeaders http.Header    `json:"ResponseHeaders"`
	Metadata        map[string]any `json:"Metadata"`
}

type responseInterceptResponse struct{}

type streamChunkInterceptRequest struct {
	RequestID       string         `json:"RequestID"`
	ResponseHeaders http.Header    `json:"ResponseHeaders"`
	ChunkIndex      int            `json:"ChunkIndex"`
	Metadata        map[string]any `json:"Metadata"`
}

type streamChunkInterceptResponse struct{}

type requestCompletion struct {
	RequestID string         `json:"RequestID"`
	Outcome   string         `json:"Outcome"`
	Metadata  map[string]any `json:"Metadata"`
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case methodPluginRegister, methodPluginReconfigure:
		if errConfigure := runtime.configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case methodPluginQuiesce:
		runtime.setAccepting(false)
		return okEnvelope(struct{}{})
	case methodPluginShutdown:
		runtime.shutdown()
		return okEnvelope(struct{}{})
	case methodRequestInterceptBefore:
		return okEnvelope(requestInterceptResponse{})
	case methodRequestInterceptAfter:
		return runtime.interceptAfter(request)
	case methodResponseInterceptAfter:
		return runtime.interceptResponse(request)
	case methodResponseInterceptStreamChunk:
		return runtime.interceptStreamChunk(request)
	case methodRequestComplete:
		return runtime.complete(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginSchema,
		Metadata: registrationMetadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "Tonkic",
			GitHubRepository: "https://github.com/Tonkic/cpa-codex-turn-state-plugin",
			ConfigFields: []configField{
				{Name: "auto_update", Type: "boolean", Description: "Promote a newer normal Fernet state after a successful request."},
				{Name: "inject_expired", Type: "boolean", Description: "Allow injection after the one-hour Fernet TTL."},
				{Name: "state_file", Type: "string", Description: "Optional private JSON file used to persist refreshed states."},
				{Name: "credentials", Type: "object", Description: "Per-auth state, plan, model scope, and baseline configuration."},
			},
		},
		Capabilities: registrationCapability{
			RequestInterceptor:     true,
			RequestLifecyclePlugin: true,
			ResponseInterceptor:    true,
			StreamChunkInterceptor: true,
		},
	}
}

func (state *runtimeState) configure(raw []byte) error {
	var req lifecycleRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return fmt.Errorf("decode lifecycle request: %w", errUnmarshal)
	}
	if req.SchemaVersion < pluginSchema {
		return fmt.Errorf("%s requires host schema version %d or newer", pluginName, pluginSchema)
	}

	cfg := pluginConfig{Enabled: true, MaxStateBytes: defaultMaxBytes}
	if len(req.ConfigYAML) > 0 {
		decoder := yaml.NewDecoder(bytes.NewReader(req.ConfigYAML))
		decoder.KnownFields(true)
		if errDecode := decoder.Decode(&cfg); errDecode != nil {
			return fmt.Errorf("decode plugin config: %w", errDecode)
		}
	}
	if cfg.MaxStateBytes == 0 {
		cfg.MaxStateBytes = defaultMaxBytes
	}
	if cfg.MaxStateBytes < 256 || cfg.MaxStateBytes > 16384 {
		return errors.New("max_state_bytes must be between 256 and 16384")
	}
	if cfg.Credentials == nil {
		cfg.Credentials = make(map[string]credentialConfig)
	}

	current := make(map[string]storedState)
	for rawAuthID, credential := range cfg.Credentials {
		authID := strings.TrimSpace(rawAuthID)
		if authID == "" || authID != rawAuthID {
			return errors.New("credential IDs must be non-empty and must not have surrounding whitespace")
		}
		credential.Plan = strings.ToLower(strings.TrimSpace(credential.Plan))
		credential.Models = canonicalModels(credential.Models)
		cfg.Credentials[authID] = credential
		if strings.TrimSpace(credential.State) == "" {
			continue
		}
		parsed, errParse := parseTurnState(credential.State, cfg.MaxStateBytes)
		if errParse != nil {
			return fmt.Errorf("credential %q state: %w", authID, errParse)
		}
		if !normalBlockCount(credential, parsed.Blocks) {
			return fmt.Errorf("credential %q state has %d blocks, which does not match its normal baseline", authID, parsed.Blocks)
		}
		current[authID] = storedState{Value: strings.TrimSpace(credential.State), IssuedAt: parsed.IssuedAt, Blocks: parsed.Blocks}
	}

	persisted, errLoad := loadPersisted(cfg.StateFile, cfg.MaxStateBytes)
	if errLoad != nil {
		return errLoad
	}
	for authID, candidate := range persisted {
		credential, configured := cfg.Credentials[authID]
		if !configured || !normalBlockCount(credential, candidate.Blocks) {
			continue
		}
		if existing, exists := current[authID]; !exists || candidate.IssuedAt.After(existing.IssuedAt) {
			current[authID] = candidate
		}
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	state.config = cfg
	state.current = current
	state.requests = make(map[string]requestBinding)
	state.candidates = make(map[string]stateCandidate)
	state.accepting = cfg.Enabled
	return nil
}

func (state *runtimeState) interceptAfter(raw []byte) ([]byte, error) {
	var req requestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode after-auth request: %w", errUnmarshal)
	}
	if !strings.EqualFold(strings.TrimSpace(req.ToFormat), "codex") {
		return okEnvelope(requestInterceptResponse{})
	}
	authID := metadataString(req.Metadata, "selected_auth_id")
	if authID == "" {
		return okEnvelope(requestInterceptResponse{})
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.accepting {
		return okEnvelope(requestInterceptResponse{})
	}
	credential, exists := state.config.Credentials[authID]
	if !exists || !matchesModels(credential.Models, req.Model, req.RequestedModel) {
		return okEnvelope(requestInterceptResponse{})
	}
	if req.RequestID != "" && autoUpdateEnabled(state.config, credential) {
		// A host retry may reuse the request ID with another credential. Discard any
		// candidate from the previous attempt before binding the new selected auth.
		delete(state.candidates, req.RequestID)
		state.requests[req.RequestID] = requestBinding{AuthID: authID}
	}
	current, exists := state.current[authID]
	if !exists {
		return okEnvelope(requestInterceptResponse{})
	}
	if !state.config.InjectExpired && !state.now().Before(current.IssuedAt.Add(turnStateTTL)) {
		return okEnvelope(requestInterceptResponse{})
	}
	return okEnvelope(requestInterceptResponse{Headers: http.Header{turnStateHeader: {current.Value}}})
}

func (state *runtimeState) interceptResponse(raw []byte) ([]byte, error) {
	var req responseInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode response interception: %w", errUnmarshal)
	}
	state.captureCandidate(req.RequestID, req.ResponseHeaders)
	return okEnvelope(responseInterceptResponse{})
}

func (state *runtimeState) interceptStreamChunk(raw []byte) ([]byte, error) {
	var req streamChunkInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode stream interception: %w", errUnmarshal)
	}
	if req.ChunkIndex == -1 {
		state.captureCandidate(req.RequestID, req.ResponseHeaders)
	}
	return okEnvelope(streamChunkInterceptResponse{})
}

func (state *runtimeState) captureCandidate(requestID string, headers http.Header) {
	value := strings.TrimSpace(headers.Get(turnStateHeader))
	if requestID == "" || value == "" {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	binding, exists := state.requests[requestID]
	if !exists || !state.accepting {
		return
	}
	credential, exists := state.config.Credentials[binding.AuthID]
	if !exists || !autoUpdateEnabled(state.config, credential) {
		return
	}
	parsed, errParse := parseTurnState(value, state.config.MaxStateBytes)
	if errParse != nil || !normalBlockCount(credential, parsed.Blocks) {
		return
	}
	if parsed.IssuedAt.After(state.now().Add(5 * time.Minute)) {
		return
	}
	if existing, existsCurrent := state.current[binding.AuthID]; existsCurrent && !parsed.IssuedAt.After(existing.IssuedAt) {
		return
	}
	state.candidates[requestID] = stateCandidate{
		AuthID: binding.AuthID,
		State:  storedState{Value: value, IssuedAt: parsed.IssuedAt, Blocks: parsed.Blocks},
	}
}

func (state *runtimeState) complete(raw []byte) ([]byte, error) {
	var completion requestCompletion
	if errUnmarshal := json.Unmarshal(raw, &completion); errUnmarshal != nil {
		return nil, fmt.Errorf("decode request completion: %w", errUnmarshal)
	}

	state.mu.Lock()
	candidate, hasCandidate := state.candidates[completion.RequestID]
	delete(state.candidates, completion.RequestID)
	delete(state.requests, completion.RequestID)
	promoted := false
	if hasCandidate && completion.Outcome == "succeeded" {
		if existing, exists := state.current[candidate.AuthID]; !exists || candidate.State.IssuedAt.After(existing.IssuedAt) {
			state.current[candidate.AuthID] = candidate.State
			promoted = true
		}
	}
	stateFile := state.config.StateFile
	state.mu.Unlock()

	if promoted && strings.TrimSpace(stateFile) != "" {
		if errPersist := state.persistCurrent(stateFile); errPersist != nil {
			return nil, errPersist
		}
	}
	return okEnvelope(struct{}{})
}

func (state *runtimeState) setAccepting(accepting bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.accepting = accepting
}

func (state *runtimeState) shutdown() {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.accepting = false
	state.requests = make(map[string]requestBinding)
	state.candidates = make(map[string]stateCandidate)
}

func autoUpdateEnabled(cfg pluginConfig, credential credentialConfig) bool {
	if credential.AutoUpdate != nil {
		return *credential.AutoUpdate
	}
	if cfg.AutoUpdate != nil {
		return *cfg.AutoUpdate
	}
	return true
}

func normalBlockCount(credential credentialConfig, blocks int) bool {
	want := credential.NormalBlocks
	if want == 0 {
		switch strings.ToLower(strings.TrimSpace(credential.Plan)) {
		case "pro", "plus":
			want = 10
		case "team":
			want = 12
		default:
			return false
		}
	}
	return blocks == want
}

func canonicalModels(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.ToLower(strings.TrimSpace(model))
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	sort.Strings(out)
	return out
}

func matchesModels(patterns []string, models ...string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, rawModel := range models {
		model := strings.ToLower(strings.TrimSpace(rawModel))
		if model == "" {
			continue
		}
		for _, pattern := range patterns {
			if strings.HasSuffix(pattern, "*") {
				if strings.HasPrefix(model, strings.TrimSuffix(pattern, "*")) {
					return true
				}
				continue
			}
			if model == pattern {
				return true
			}
		}
	}
	return false
}

func metadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func loadPersisted(path string, maxBytes int) (map[string]storedState, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	fileHandle, errOpen := os.Open(path)
	if errors.Is(errOpen, os.ErrNotExist) {
		return nil, nil
	}
	if errOpen != nil {
		return nil, fmt.Errorf("open state file: %w", errOpen)
	}
	defer func() { _ = fileHandle.Close() }()
	raw, errRead := io.ReadAll(io.LimitReader(fileHandle, maxStateFileBytes+1))
	if errors.Is(errRead, os.ErrNotExist) {
		return nil, nil
	}
	if errRead != nil {
		return nil, fmt.Errorf("read state file: %w", errRead)
	}
	if len(raw) > maxStateFileBytes {
		return nil, fmt.Errorf("state file exceeds %d bytes", maxStateFileBytes)
	}
	var file persistedFile
	if errUnmarshal := json.Unmarshal(raw, &file); errUnmarshal != nil {
		return nil, fmt.Errorf("decode state file: %w", errUnmarshal)
	}
	if file.Version != 1 {
		return nil, fmt.Errorf("unsupported state file version %d", file.Version)
	}
	out := make(map[string]storedState, len(file.Credentials))
	for authID, stored := range file.Credentials {
		if authID == "" || authID != strings.TrimSpace(authID) {
			continue
		}
		parsed, errParse := parseTurnState(stored.Value, maxBytes)
		if errParse != nil {
			continue
		}
		stored.IssuedAt = parsed.IssuedAt
		stored.Blocks = parsed.Blocks
		out[authID] = stored
	}
	return out, nil
}

func (state *runtimeState) persistCurrent(path string) error {
	state.persistMu.Lock()
	defer state.persistMu.Unlock()
	state.mu.Lock()
	snapshot := cloneStoredStates(state.current)
	state.mu.Unlock()
	return persistStates(path, snapshot)
}

func persistStates(path string, states map[string]storedState) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == "" {
		return errors.New("state_file path is empty")
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return fmt.Errorf("create state directory: %w", errMkdir)
	}
	raw, errMarshal := json.MarshalIndent(persistedFile{Version: 1, Credentials: states}, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("encode state file: %w", errMarshal)
	}
	raw = append(raw, '\n')
	tempFile, errCreate := os.CreateTemp(filepath.Dir(path), ".codex-turn-state-*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create temporary state file: %w", errCreate)
	}
	tempPath := tempFile.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if errChmod := tempFile.Chmod(0o600); errChmod != nil {
		_ = tempFile.Close()
		return fmt.Errorf("secure temporary state file: %w", errChmod)
	}
	if _, errWrite := tempFile.Write(raw); errWrite != nil {
		_ = tempFile.Close()
		return fmt.Errorf("write temporary state file: %w", errWrite)
	}
	if errSync := tempFile.Sync(); errSync != nil {
		_ = tempFile.Close()
		return fmt.Errorf("sync temporary state file: %w", errSync)
	}
	if errClose := tempFile.Close(); errClose != nil {
		return fmt.Errorf("close temporary state file: %w", errClose)
	}
	if errRename := os.Rename(tempPath, path); errRename != nil {
		return fmt.Errorf("replace state file: %w", errRename)
	}
	removeTemp = false
	return nil
}

func cloneStoredStates(source map[string]storedState) map[string]storedState {
	out := make(map[string]storedState, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func okEnvelope(value any) ([]byte, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, errMarshal := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	if errMarshal != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"encode error"}}`)
	}
	return raw
}
