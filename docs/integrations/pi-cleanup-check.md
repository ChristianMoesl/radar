# Pi manual cleanup bundle guard checks

[Documentation](../README.md) · [Pi integration](pi.md) · [Pi feedback diagnostics](pi-diagnostics.md)

## Explicit manual cleanup bundle guard check

Use `section: "cleanup_check", parent_id: "<retained task record or numeric ID>"`
only when the user requests a fresh native guard check. Scope defaults to the
registered origin; `feedback` and `installation` are explicit alternatives.
Scope selects the task: the **entire ordered resource bundle, including other
workspaces**, is planned and checked. Subset IDs, pagination/state filters,
expiry/retirement modes, action/plan IDs and client-supplied native targets or
paths are rejected. This is not cleanup apply or a general command adapter.

Pi requires fresh interactive exact-task/scope confirmation before each call.
The adapter resolves fresh bounded retained inputs and registration, invokes
the existing confirmed-manual native planner, then `cleanup.Service.CheckBundle`
once. This shares Execute's admission and bundle preflight, but stops before
every provider `Cleanup`. It never calls Execute as a dry-run, collection,
ordinary refresh/GC, login, setup or startup. Direct host CLI callers are
responsible for the same explicit consent:

```sh
radar feedback-diagnostics --workspace <feedback-workspace> \
  --section cleanup_check --scope origin --parent-id <task-id> --json
```

Unlike `cleanup_preview`'s existing-inode probes, **real native admission may
create coordination files/directories and briefly contend with new work**.
Native tmux admission and protected workspace-tree checks can run. Existing
authenticated provider reads and Git fetch/prune may refresh metadata. All
acquired admission is released before return, including error/cancellation;
there is no retained lease or reservation. Existing `cleanup_preview` consent
and no-created-lease-files guarantees are unchanged.

At most 256 active refs and 512 native targets are accepted. Pending resource
publication, ambiguous/unsupported evidence, observed registration/revision
drift and cancellation fail closed. Execution has a cooperative 30-second
deadline (32-second CLI response deadline, 35-second Pi timeout), not forced
interruption of arbitrary callbacks. Client cancellation/disconnect is not a
rollback of coordination/Git metadata. Unknown results must not be replayed
or automatically retried.

The finite aggregate result is `guards_passed_at_check` or
`not_passed_or_unavailable`; raw provider errors and private targets, paths,
commands and owner identities are withheld. Non-passing evidence does not
invent a failed target or specific refusal reason. **Passed only means the
shared bundle guards returned successfully at check time.** SBX lifetime
observation and other provider-internal last-moment checks still run during
Cleanup, which this adapter never calls. Full execution eligibility, runtime
presence/lifetime and actual resource outcomes remain unknown. No apply token,
deletion/retry consent, resource absence or safe retry follows from this check.

CLI/daemon/loaded Pi must match **schema 39**, without old-peer fallback or a
persisted-format change. Pi validates the complete available/unavailable
envelope and finite result before text/details delivery. `make install` alone
does not update/reload separately installed Pi sources. Cleanup apply and runtime
controls remain unsupported; any later apply requires separate one-use native
freshness and fresh exact-target consent, never this guard result or a display
snapshot.
