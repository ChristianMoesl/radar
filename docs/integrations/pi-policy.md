# Pi configuration and workspace policy

[Documentation](../README.md) · [Pi integration](pi.md) · [Pi feedback diagnostics](pi-diagnostics.md)

## Configuration policy and origins

Overview `settings` are **CLI-process global defaults**. With a matching daemon,
`daemon.settings` separately reports **daemon-process global defaults** from the
same native bounded reader executed in the running daemon process. The existing
`diagnostics` RPC's selector-free `overview` returns this typed schema-35
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

## Recorded workspace policy (diagnostics schema 35)

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

Registration inventories, this recipe projection and before/after repository-policy
registration reads share a diagnostic-only capture: at most 1 MiB and 1024 workspace
registrations, no-follow/nonblocking root/file descriptors and observed identity,
size/time/completeness checks. Absent roots/files remain empty without creation;
unsafe, invalid, oversized or changed evidence is unavailable, never partial.
No native mutex or coordination file is opened, created or locked, so an active
writer does not make inspection wait. Native writers atomically replace the file;
this observation is not a serializable transaction, ABA protection or execution
admission. Normal native loading/writing/locking and mutation guards are unchanged.
The shared native validator retains complete recipe and identity/path validation.
Missing/invalid/ambiguous/unsupported evidence fails closed, without falling back
to current files or another scope. No repository, referenced file, provider,
runtime, ownership or lease checks, collection or maintenance are requested.
The snapshot binds the **public shape**, not private recipe bytes; an unchanged
shape does not attest unchanged commands or future resources. A single fresh
registration observation is not atomic with other inventories or future state.
Pi validates the complete envelope, page, row and nested recipe before output,
including unavailable responses. The native publication boundary also requires
nonnegative safe-integer mount/port counts and copies the provider's public recipe
before validation and snapshot assembly. Later provider writes cannot change the
validated page. Invalid counts return a sanitized refusal, never clipping, guessed
zero or fallback reads. This finite projection is not effective policy or a
reservation against concurrent/future native changes.
`registered_workspace_recipe_policy` advertises this bounded capability;
`workspace_effective_policy` remains false.

CLI, daemon and loaded Pi must match schema 35. This is a transport change only,
not a registry/data migration; older schema-29 peers fail closed. `make install`
does not update/reload a separately installed Pi extension.

## Current registered member repository declarations (schema 35)

For an explicitly requested file inspection, use `repository_policy` with
`parent_id=<exact registered workspace ID>` and `id=<member row ID from
workspace_resources>`. Normal origin/feedback/installation scope applies.
Fresh interactive confirmation covers that exact parent, member and scope.
Only the separate `diagnostics-probe` RPC accepts this section; ordinary
registration-only `workspace_policy` remains unchanged and never reads files.
No raw path selector, state filter, cursor, fallback scan or implicit file access
is accepted. A duplicate member occurrence is ambiguous, not permission to pick one.

The native reader resolves the member's **registered source repository**, not
its worktree. It captures only its fixed `.radar.yaml`, at most 64 KiB, rejecting
symlink directory components and leaf symlinks, FIFOs, nonregular, oversized,
malformed or replaced/incomplete files. Unavailable repositories/configuration
have no settings; an actually absent configuration in an accessible repository
has repository defaults, without creating a file. Before/after file identity,
size/modification-time and complete registration checks reject observed drift.
These are point-in-time checks, not detection of every intervening/ABA change,
a filesystem transaction or a future-state reservation. Capture time is page
assembly time, not when production loaded configuration.

The same native YAML decoder and validation supply ten finite settings:
`copy_files`, `setup` and `sbx.additional_mounts` are counts; `model`, `thinking`,
`sbx.ready_command`, `sbx.env_file`, `sbx.kit.name` and `sbx.kit.path` are presence
booleans; `sbx.enabled` is `inherit`, `required` or `disabled`. `inherit` means
no repository override, not a claim about global policy or runtime enablement.
Each carries current-document `absent`/`null`/`value` declaration evidence and
`repository_default`/`repository_file` origin, including native aliases/merges.
An explicit value equal to a default is still a file declaration.

Raw values, paths, commands and private file hashes are withheld. No referenced
environment, script, kit, copy or mount contents, global merge, provider/runtime
probe, collection, lease or maintenance executor is invoked. These are **current
repository declarations**, not historical recipe origins, operation-effective
policy, observed live configuration, eligibility or publication consent.
The snapshot binds public shape only, not private configuration bytes.
Pi validates the complete envelope/page/row/settings, including unavailable
evidence, before output. The daemon also validates the complete finite native
declaration/unavailable projection before publishing it to any client, and copies
the provider's settings so later provider writes cannot change the validated page.
Invalid evidence yields a sanitized refusal, not clipped private values, guessed
defaults or a fallback read. This checks the public contract, not effective policy
or future file stability. `current_member_repository_declarations` advertises
this capability; `workspace_effective_policy` remains false. Matching schema-35
CLI/daemon/loaded Pi is required; schema-29 peers fail closed, with no persisted
data migration. Separately installed Pi still requires an update/reload.
