# Pi registered-runtime observations

See [Pi feedback diagnostics](pi-diagnostics.md) for scope and other sections.

## Explicit registered-runtime observations

Use `section: "runtime_probe", parent_id: "<registered workspace ID>"` only for
requested live runtime evidence. The usual origin (default), feedback and
installation scopes still apply; even installation scope requires one exact
registered workspace. Task-state filters are rejected. Every request/page needs
fresh interactive confirmation naming its parent, scope and optional runtime ID.
Headless, declined or aborted confirmation never starts a probe. The explicit
CLI invocation is `radar feedback-diagnostics --workspace <feedback workspace>
--section runtime_probe --parent-id <registered workspace ID> --json`; the normal
cached diagnostics RPC rejects probes, with no older-daemon fallback.

The provider-owned adapters run only native `sbx ls --json` and
`tmux list-sessions` (including native SBX/WSL executable selection, environment
and listing path normalization). One request has a shared five-second deadline;
stdout is capped at 20 MiB per command, stderr/provider errors are withheld, and
cancellation terminates command process groups. No collection, pane content,
startup, login, Git/network refresh, provider cleanup preview, ownership/lease
acquisition or maintenance runs. A runtime CLI may contact its own backend;
this is not a cached or offline observation.

Each item reports registered name, provider, bounded presence/state, opaque
runtime-lifetime ID and comparison against active cached native identities for
that exact workspace/name. Full native identities are compared before hashing
or clipping; reuse of a name with a different lifetime is explicit. A complete
identifiable listing may establish absence; malformed, ambiguous, unsupported,
failed or timed-out evidence is **unknown**, never absence. Native SBX listings
must have unique names and lifetime IDs across **all** rows, even unrelated rows.
An aliased target or foreign duplicate therefore establishes neither presence
nor absence. The same native validation guards cleanup/recreation reads and
retirement outcomes; a successful stop with an ambiguous follow-up listing is
not a verified retirement and is never replayed. Native tmux listings must
likewise contain one server PID and globally unique nonempty session names/IDs,
even across unrelated rows or conflicting creation timestamps. Their shared
parser rejects the complete listing before collection or registered retirement
can use a valid prefix; diagnostics report unknown and retirement sends no kill.
Shared registered names are not probed. Changed registration/cache evidence
rejects the result;
changed observations invalidate pagination cursors, and every page repeats the
bounded listing. The observation timestamp is not a guarantee of future state.

The complete runtime probe envelope, page, rows and observations are allowlisted
before model output. Private siblings, wrong targets and inconsistent evidence
fail closed, including unavailable replies. CLI, daemon and loaded Pi must match
diagnostics schema 44; older runtime wire shapes fail closed without fallback.

The same explicit approval also covers the complete bounded native agent
registration scan before and after the observations and workspace revalidation.
Only matching private registry fingerprints on an uncancelled request produce
`ownership_evidence: "fresh_bounded_registration"` and `agent_owner_count`
(0..1024), using the production workspace-removal owner predicate. Stopped
registrations still count; nested owners count and sibling paths do not. This is
an exact-workspace registration hold, **not runtime ownership**, orphan status,
process or lease-holder identity, or execution eligibility. Invalid, unsafe,
overbudget or changed registrations produce `ownership_evidence: "unavailable"`
and omit the count, never zero. No registration payload, fingerprint, runner,
checkpoint, token or lease is returned or probed. Leases remain
`unknown_not_checked`. Cancellation/expiry clears the count across the entire
selected page; changed public counts invalidate cursors. Each page repeats both
bounded registration observations, not a held reservation or atomic snapshot.
Persisted formats are unchanged; no daemon or loaded Pi rollout is implied.

Runtime matches, absence or mismatches establish neither registration ownership,
lease safety, orphan absence nor cleanup eligibility/consent. Unrelated listing
rows, native lifetime IDs, mounts, environment, pane contents and raw errors are
not returned. Unregistered runtimes are not classified. Cached inventories never
start these probes implicitly; use existing retained references for cached
history, not as proof of liveness.

