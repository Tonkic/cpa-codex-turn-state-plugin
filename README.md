# CPA Codex Turn State Plugin v0.3.0

A native CLIProxyAPI DLL that acquires and refreshes opaque X-Codex-Turn-State values per selected credential **and actual upstream model**. Business requests keep their existing CPA proxy; independent lightweight probes use a separate HTTP/SOCKS proxy pool with optional chaining.

See [中文配置及使用说明](README_CN.md) for generic configuration and complete operating behavior. No deployment-specific proxy addresses or credentials are included.

## Compatibility

- Native ABI 1, JSON schema 4 or newer.
- Windows amd64, Go 1.26+ and a cgo-compatible GCC.
- CPA must support request/response interception, completion hooks and standard host.auth.list / host.auth.get callbacks.
- DLL filename/plugin ID: cpa-codex-turn-state.dll / cpa-codex-turn-state.
- HTTP and SSE supported. WebSocket capture is not implemented.

## Behavior

State keys contain both selected_auth_id and the actual upstream Model, never the client alias. Missing or nearly expired states trigger an on-demand ping for that exact account/model. A valid state requires a successful response.completed event and accepted token structure, timestamp and ciphertext block count. This is an opaque-token heuristic, not a compute-quality test.

The first request may wait for the configured probe budget. Concurrent requests for the same key reuse the existing valid cache or proceed without state. A maximum of four account/model probes run concurrently. A background worker checks known cached keys every five seconds and proactively refreshes at issuance + 55 minutes by default, without requiring business traffic. Persisted v2 cache restores the schedule on restart. Failed probes have a cooldown, and no configured proxy can silently fall back to a direct connection.

HTTP 429, 502/503/504, overload and matching SSE failures queue early refresh without blocking completion hooks. Credential retries queue the previous selected key as well, including failed attempts hidden by host failover. Assistant content and client cancellations do not trigger refresh. Signals coalesce per account/model and respect retry_seconds. Explicit quota-exhaustion probe responses back off for quota_backoff_seconds (default 900); refreshing state cannot restore quota. New accepted state replaces old state immediately and resets the refresh deadline. Quiesce/reconfiguration cancels and drains the background worker before host callbacks are retired.

background_refresh and refresh_on_errors default to true when probes are enabled. Set them false to opt out. Background work only targets known cache keys or queued failures, not undiscovered account/model combinations. Status includes next_refresh_at, refresh_pending, last_probe_reason and last_probe_at. A v0.2.0 upgrade preserves v2 cached state.

Probe payloads never contain the business prompt. OAuth credentials are read via the trusted host callback for the selected auth ID; the plugin never updates auth files. OAuth refresh remains CPA's responsibility. Probes only support standard Codex OAuth credentials without custom base_url.

Successful business responses also stage replacement state, promoted only on successful request completion. Tokens are never shared across accounts or models.

## Configuration

See [examples/proxy-pool.yaml](examples/proxy-pool.yaml). Merge its plugin block into CPA configuration. The chain runs in the DLL:

```text
probe → optional first_proxy → proxy_pool entry → Codex
business → existing CPA route
```

No local sidecar/listening port is necessary. Set CPA_STATE_FIRST_PROXY_URL and CPA_STATE_PROXY_URL privately in the service environment; omit first_proxy if chaining is unnecessary. Never commit their values. connect_host optionally overrides only the HTTP proxy CONNECT Host header while preserving the request-target authority and origin TLS hostname; use it only if your first-hop proxy requires this sniffing workaround.

Each endpoint has exactly one of url, url_file, or url_env. Secret files and environment variables are resolved per dial. Proxy schemes: http, https, socks5, socks5h. Both SOCKS schemes forward hostnames remotely. IPv6 literals require URL brackets; public IPv6 egress depends on the provider and has not been live-verified.

Proxy pool selection rotates per probe. max_attempts defaults to 1 (range 1–5); failed or rejected attempts advance through the pool, sharing timeout_seconds (default 15, range 1–60). retry_seconds defaults to 60; refresh_before_seconds defaults to 300. A literal {session} in a secret URL is replaced with eight random hexadecimal characters on each connection.

Pro/Plus default to 10 ciphertext blocks (normally 292 padded characters), Team to 12 (332). Known abnormal 11/13-block values (312/356) are rejected. defaults.accepted_blocks: [10,12] allows discovery without copying account IDs into configuration; explicit per-account plans offer stricter filtering. Unknown plans require a configured baseline.

## Upgrade from v0.1.x

Version 1 state files contain no model identity and are intentionally not reused. Back up the old file and use a new state_file path, for example state/codex-turn-state-v2.json. New format version 2 stores JSON-encoded [authID,model] keys.

Configured seeds require an exact state_model. A models wildcard is a policy filter, not permission to reuse a token across models. inject_expired remains an opt-in compatibility setting; keep it false. Future-dated values beyond clock tolerance are never injected.

## Status and security

GET /v0/management/codex-turn-state/status is management-authenticated. It exposes version, anonymous account digest, model, state length, timestamps, validity and sanitized probe results. It never returns credentials, full state, or proxy URLs.

Treat the DLL as trusted code with access to host credentials. Keep state files and proxy secrets private and outside Git. TLS certificate verification stays enabled, redirects are disabled, and proxy authentication is scoped to the corresponding handshake.

## Build and install

```powershell
go test -race ./...
go vet ./...
./scripts/build-windows.ps1
```

Build artifact: dist/windows-amd64/cpa-codex-turn-state.dll. Install under plugins/windows/amd64 or the configured plugin root. Back up the prior DLL, configuration and state before upgrading; follow the host's plugin reload workflow and check registration/status. This repository does not automatically replace production plugins.

For a running host, deploy as cpa-codex-turn-state-v0.3.0.dll. CPA recognizes the version suffix while preserving the plugin ID. Its hot replacement depends on a changed selected file path; overwriting the same path may leave the old module loaded. Retain the previous artifact for rollback. Run python scripts/smoke-dll.py to exercise the actual native ABI before deployment.

Tests cover account/model isolation, expiry, cooldown, pool fallback, concurrency/reconfiguration, SSE completion validation, chained CONNECT headers, credential isolation, cancellation, IPv6 encoding and buffered tunnel data. Optional live tests require explicit CPA_LIVE_PROXY_URL; Codex tests additionally require CPA_LIVE_AUTH_FILE. Set CPA_LIVE_FIRST_PROXY_URL for a first hop and CPA_LIVE_CONNECT_HOST only when necessary. They do not print exit IP addresses, credentials or full state. Normal CI uses synthetic credentials only; do not provide production secrets to CI.
