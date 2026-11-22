# Pi configuration and workspace policy

[Documentation](../README.md) · [Pi integration](pi.md) · [Pi feedback diagnostics](pi-diagnostics.md)

## Configuration policy and origins

Overview `settings` are **CLI-process global defaults**. With a matching daemon,
`daemon.settings` separately reports **daemon-process global defaults** from the
same native bounded reader executed in the running daemon process. The existing
`diagnostics` RPC's selector-free `overview` returns this typed schema-29
projection; it never uses task visibility filters or provider calls. No new public
tool or general file/configuration endpoint is added.

Both projections reread their process's current fixed configuration location.
They have independent capture times/scopes, and neither is an environment dump,
startup/loaded-settings attestation, repository/workspace-effective configuration,
current provider access or execution permission. Differences help diagnose process
configuration drift; matching allowlisted revisions do not prove the same file,
environment or private configuration. `daemon_environment` and
`workspace_effective_policy` remain false; `daemon_process_configuration` advertises
only this narrow projection. Missing/old/mismatched daemon support is explicitly
unavailable with no legacy summary fallback. A valid daemon response can contain
unavailable policy evidence; it must not be replaced with CLI settings or defaults. The native defaults, YAML decoder and validation are
shared with production. Values and declarations come from the same bounded read
of the configured `config.yaml`; no second file is used to guess origins.

The finite ten-setting allowlist contains:
- `workspace.auto_confirm`: `true`/`false`.
- `sbx.enabled`, `github.enabled`, `jira.enabled`, `datadog.enabled`,
  `terraform.enabled`: `automatic`, `required` or `disabled`. `required` expresses
  configuration intent, not usable tools/authentication or an enabled runtime.
- `workspace.cleanup.disposable_entries` and `sbx.additional_mounts`: counts only,
  not names, paths, accepted deletion targets or permission to delete anything.
- `sbx.env_file` and `sbx.ready_command`: configured presence only; paths,
  command/argument values and referenced file contents are never read or returned.

Each setting identifies `origin` (`compiled_default` or `configuration_file`) and
`declaration` (`absent`, `null` or `value`). Explicit values equal to defaults still
have file provenance; native null/default behavior, aliases and YAML merges are
preserved. `location_origin` identifies only `xdg_config_home` or `user_home`, never
the path or an environment dump. Model/thinking strings and arbitrary old settings
maps are no longer returned. The revision hashes only these allowlisted values
and declarations, not private configuration bytes. `captured_at` is read assembly
time, not evidence that a daemon loaded this configuration. The daemon overview
also carries at most 32 bounded cached provider status rows, with only detail
presence (never detail text), and the current resource-refresh-pending marker.
Cached status is installation-wide, not live access, collection or workspace health.

At most 64 KiB is read from the fixed native configuration file. Leaf symlinks
(including legitimate user-managed configuration symlinks), FIFOs, nonregular,
oversized, invalid, replaced or incomplete reads are unavailable, not valid
policy or authorization. An actually absent file yields compiled defaults without
creating it. Raw parser/validation errors are withheld. A prior workspace
registration lookup still uses its native configuration-loading rules; this
bounded projection does not change normal configuration or startup behavior.
Provider credentials, secret/environment files, tools, network access, repository
configuration, daemon environment and maintenance are never inspected or invoked
by this projection. No persisted configuration or policy was changed.

The originating workspace is projected from its registration only: it does not
run SBX, tmux, Git, remote probes or workspace startup. A registered resource is
not proof that it exists or is running. Missing daemon/origin evidence is
unavailable, not healthy, empty, repaired or enrolled.

## Recorded workspace policy (diagnostics schema 29)

Use `workspace_policy` with `parent_id=<registered workspace ID>` and the normal
`origin` (default), `feedback` or explicitly requested `installation` scope. This
is a read-only exact-target inspection; it needs no runtime-probe/apply approval.
Row, state and cursor selectors are rejected. The single complete row cannot be
used as an execution plan or pagination token for another target.

`workspace_policy` reports the **recorded registration recipe**, not a new merge
of current global and repository configuration. `origin` is the fixed label
`recorded_workspace_registration`; historical per-setting file/default origins
were not stored and remain unknown. `sandbox_registered=false` omits `sandbox`:
there is no registered recipe, not proof of an absent/stopped runtime. A registered
recipe exposes only kit/kit-path/environment-file/readiness-command presence and
complete managed-mount, additional-mount and port counts. Names, paths, argv,
models, environment contents and other raw recipe values are withheld.

The native registration reader validates the registry and exact scoped identity.
Missing/invalid/ambiguous/unsupported evidence fails closed, without falling back
to current files or another scope. No repository, referenced file, provider,
runtime, ownership or lease checks, collection or maintenance are requested.
The snapshot binds the **public shape**, not private recipe bytes; an unchanged
shape does not attest unchanged commands or future resources. A single fresh
registration observation is not atomic with other inventories or future state.
Pi validates the complete envelope, page, row and nested recipe before output,
including unavailable responses. `registered_workspace_recipe_policy` advertises
this bounded capability; `workspace_effective_policy` remains false.

CLI, daemon and loaded Pi must match schema 29. This is a transport change only,
not a registry/data migration; older schema-28 peers fail closed. `make install`
does not update/reload a separately installed Pi extension.
