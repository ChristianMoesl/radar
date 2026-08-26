# SBX integration

SBX supplies local Docker sandbox resources and shell actions.

## Capabilities

`Source`, `StatusReporter`, `LocalSource`, `RuntimeProvider`, `ActionProvider`, `InteractiveAuthenticator`, and `CleanupProvider`.

## Configuration and authentication

`sbx.enabled`, `sbx.kit`, and `sbx.additional_mounts` configure managed runtimes. Repository-local settings use the same shape. The authentication capability recognizes login failures and runs the provider-owned interactive login flow when applicable.

## Collection and refs

Local refreshes parse `sbx ls --json` and emit stable sandbox refs linked by name, mark, and primary workspace. The runtime capability resolves provider-owned sandbox names. Shell actions use the registered multiplexer rather than invoking tmux themselves.

Cleanup descriptions and opaque resource IDs are provider-owned. Workspace reconciliation preserves mount, port, recreation, and failure behavior; failed runtime recreation does not roll back completed filesystem or Git changes.

## Validation

```sh
go test ./internal/integration/sbx
```
