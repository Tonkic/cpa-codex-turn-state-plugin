# CPA Codex Turn State Plugin

A trusted in-process dynamic plugin for CLIProxyAPI (CPA) that injects and safely refreshes the opaque `X-Codex-Turn-State` header per selected Codex credential.

The plugin does not decrypt or generate turn state. It treats the value as an opaque Fernet token, reads only the public timestamp and ciphertext length, and never logs the token.

## Behavior

- Resolves the selected CPA credential from `Metadata["selected_auth_id"]` after authentication.
- Injects an unexpired state only for Codex requests and an optional model scope.
- Observes HTTP and SSE response headers.
- Stages a newer response state as a candidate and promotes it only after the request completes successfully.
- Rejects malformed, stale, future-dated, out-of-order, or unexpected-block-count candidates.
- Keeps credentials isolated; a state observed for one auth ID is never used by another.
- Optionally persists refreshed states using an atomic private JSON file.

WebSocket state refresh is intentionally deferred in v0.1. HTTP and SSE Responses are supported.

## Compatibility

- Native plugin ABI: 1
- JSON RPC schema: 4
- Target host: CLIProxyAPI versions with the standard dynamic plugin host and schema 4 or newer
- Target artifact: Windows amd64 DLL

Schema 4 is deliberately used as the compatibility floor shared by the deployed CPA versions for which this plugin was designed.

## Configuration

The DLL filename is the plugin ID. Install it as `cpa-codex-turn-state.dll` under `plugins/windows/amd64` or the configured plugin root.

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-codex-turn-state:
      enabled: true
      priority: 100
      auto_update: true
      inject_expired: false
      state_file: "state/codex-turn-state.json"
      defaults:
        accepted_blocks: [10, 12]
      credentials:
        "<selected_auth_id>":
          plan: plus
          state: "<known-good-fernet-token>"
          models:
            - "gpt-5.6-*"
            - "gpt-6-astra"
```

Supported plans and default normal ciphertext block counts:

| Plan | Normal blocks | Typical padded token length |
| --- | ---: | ---: |
| Pro / Plus | 10 | 292 |
| Team | 12 | 332 |

An observed Pro/Plus state with 11 blocks or Team state with 13 blocks is not promoted. For another account type, set `normal_blocks` explicitly. Unknown plans without `normal_blocks` never auto-promote.

`defaults` applies only to selected auth IDs that are not explicitly listed. It is useful when auth IDs contain private account identifiers: `accepted_blocks: [10, 12]` bootstraps both Plus/Pro and Team states without copying those IDs into configuration. A shared `defaults.state` is rejected so one credential's state can never seed another credential. Explicit `credentials` entries override the fallback policy.

Per-credential `auto_update` overrides the global setting.

## Security

- This is trusted in-process code. Treat the DLL like the CPA executable itself.
- The state file contains opaque account state. Keep it private, exclude it from backups that are not already authorized to hold credential material, and never commit it.
- The plugin does not log complete state values.
- A configured seed state is part of CPA configuration. Prefer the private state file once initial validation is complete.

## Build

Requirements:

- Go 1.26 or newer
- A Windows amd64 GCC toolchain available to cgo, such as MinGW-w64

```powershell
go test -race ./...
./scripts/build-windows.ps1
```

The artifact is written to `dist/windows-amd64/cpa-codex-turn-state.dll`.

## Installation safety

Validate the DLL against a non-primary CPA instance first. Confirm plugin registration, unchanged CPA PID during hot reload, correct auth isolation, header injection, state promotion after successful completion, and absence of token values in logs before enabling it on production traffic.

This repository does not modify or restart a running CPA service.
