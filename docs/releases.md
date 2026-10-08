# Releases and macOS updates

Radar's CLI and `@christianmoesl/pi-radar` share a release version. The small
macOS notifier has its own version and immutable artifacts. Linux/Windows retain
manual `make install`; existing Linux release archives are still generated.

## Maintainer trust and publishing setup

`internal/update/keys.json` contains the embedded public trust roots. The initial
key ID is **`release-1`**; the SHA-256 fingerprint of its raw 32-byte public key is:

```text
c0d9b82fb205070a4d26a1f18b42c4bef1c355292a3fa2d72c8211346a61c6e9
```

Verify that fingerprint through a trusted channel before bootstrapping an
installation. This documentation and a key accompanying an untrusted download
cannot themselves establish trust. The private key is never stored in this
repository. Builds with an empty trust store still fail closed: no unsigned
metadata, release-supplied trust root, or silent enrollment is accepted.
Configuring CI alone does not update an older binary's embedded keys.

For initial key provisioning or an intentional rotation (follow the bridge-release
policy below), and when configuring the publishing repository:

1. Create an Ed25519 private key in an owner-only directory outside **all** Git
   repositories and unencrypted sync folders. Resolve symlinked parent directories
   first; a dotfiles-managed `~/.config` may itself point into a Git checkout. Set
   directory permissions to `0700`, use `umask 077`, and create a new private-key
   file without overwriting an existing key, for example with
   `openssl genpkey -algorithm ED25519 -out <new-private-key.pem>`.
   Keep a secure encrypted backup. Never commit it or paste it into logs/issue notes.
2. Export its **raw 32-byte public key**, base64 encoded, and commit it in
   `internal/update/keys.json`, keyed by a stable key ID, for example:

   ```json
   { "release-1": "<base64 raw Ed25519 public key>" }
   ```

   Node's `createPublicKey(privateKey).export({type: 'spki', format: 'der'})`
   returns the Ed25519 SPKI; its final 32 bytes are the raw public key. Verify the
   fingerprint through a trusted channel before distributing the bootstrap CLI.
3. Set the GitHub repository variable `RADAR_RELEASE_KEY_ID` to that ID and the
   Actions secret `RADAR_RELEASE_SIGNING_KEY` to the private key's PEM contents.
   Restrict workflow/tag editing and access to these credentials. The scripts
   verify that the signing key matches the committed public key.
4. Configure the [stage-only npm trusted publisher](npm-publishing.md). Keep npm's
   human review/2FA approval. No npm token or automated approval is introduced.
5. Ensure the development forge mirrors **both CLI and notifier tags** into
   `github.com/ChristianMoesl/radar`. All component refs must also exist in the
   authoritative development origin: a full mirror can delete GitHub-only tags
   and turn their releases into drafts. CI never creates component refs. A push
   to the development origin alone is not proof that GitHub Actions ran. Humans
   perform pushes for this repository.

For rotation, first ship a CLI trusting both old and new public keys, signed
with the old key. Only switch signing to the new key after users can receive
that bridge release. Removing an old key from a future build does not remotely
revoke it in already-installed binaries; compromised-key recovery needs a
separately trusted manual reinstall. No environment variable bypasses trust.

These project signatures authenticate artifacts. They are **not** Apple
Developer ID signatures or notarization. The v1 helper stays ad-hoc signed;
macOS may require app-specific launch approval. Paid Apple signing can be added
later without replacing the release contract.

## Cut a release

- Choose an unused version. `0.1.0` exists on npm, `v0.1.1` was used for a failed
  publication, and the corrected **`v0.1.2` CLI is published**. Finish its npm
  staging/approval by retrying only the failed npm job, not by cutting another
  version. Never move a used tag or republish an existing version.
- Update `package.json` and `extensions/pi-radar/version.ts` together. The latter
  is the version actually loaded into Pi, not a late read of a replaced manifest.
  `pnpm check:release vX.Y.Z` verifies alignment before tagging/publishing.
- Change `macos/RadarNotifier/VERSION` only for an actual component change,
  including deliberate SDK/compiler/security rebuilds. Swift/plist/icon/build
  inputs cannot change under an existing component version.
- Run `pnpm check`, `make test`, and release builds on macOS. An authenticated
  `gh` session is required to inventory all component releases, including drafts.
  `make release VERSION=vX.Y.Z` remains the human-operated workflow: it verifies
  component history/ref identity, signs a new component tag only for an unused
  version, then atomically pushes **both** `notifier-v<version>` and `vX.Y.Z` to
  `origin`. Existing component refs are reused exactly, never retagged at HEAD.
  A failed atomic push stops; there is no separate-push fallback. `make test`
  limits Go package concurrency to two so subprocess/PTY fixtures are not starved
  during release checks. The release script restores the caller's exact terminal
  modes after each validation stage and on exit; validation failures stop before
  tagging/pushing. If the terminal was already left in raw mode, run `stty sane`
  before retrying. Verify that the tag arrives on GitHub. Never move or reuse a
  published tag/version.
- CI serializes releases, authenticates/reuses the `notifier-v<version>` component
  release, builds the existing macOS/Linux archives, signs `release.json`, and
  publishes the CLI release from a draft only after uploading its assets.
- Approve the staged exact npm version with 2FA. Until it is publicly available,
  the coordinated updater skips that release and may offer an older eligible
  stable version. Drafts/prereleases are never automatic update candidates;
  prerelease archives and npm's `next` staging remain available manually.

`release.json.sig` signs the exact bytes of `release.json` using Ed25519. Metadata
binds versions, both macOS architectures, minimum OS/Pi/Node requirements, archive
size/hash, binary hash and notifier tree identity. SHA-256 checksum lists alone
are not publisher authentication. Initial installation still requires trusting
the bootstrap source/key through a trusted channel; a key shipped alongside an
otherwise untrusted download cannot establish that trust by itself.

The `StateEpoch` in `internal/update/manifest.go` is the persisted-data rollout
boundary. **Bump it for incompatible configuration/state formats.** An updater
with a different epoch rejects the release; implement/document a read-only
preflight and obtain an explicit migration/reset decision for a manual rollout.
Binary rollback never rolls back task notes, configuration or other user data.

## Immutable notification component

`scripts/prepare-release-notifier.sh` creates or retrieves a dedicated component
release with signed `notifier.json` and per-architecture archives. It requires an
existing mirrored component tag and uses `--verify-tag`, never `--target` to
create a GitHub-only ref. The CLI workflow is still triggered only by `v*` tags;
pushing a notifier tag does not launch another release job. Default source builds
also derive the CLI version only from `v*` tags, not the independent component.

Before deciding to build, `scripts/notifier-release-state.mjs` inspects **every
page** of GitHub releases, including drafts. A missing tag or previously published
release that became a draft is a repair condition, not permission to rebuild.
An ambiguous history is rejected. A single currently published release is reused
by its immutable **asset IDs**, even when retired drafts with that tag remain.
A unique never-published draft can be resumed only after all expected assets,
Ed25519 authentication, source fingerprints, archive hashes and extracted tree
identities pass. Incomplete/tampered drafts are not repaired by overwriting them.
Reusing a published component does not edit its release metadata.

Rebuilding from identical source is not a substitute for reusing original
artifacts: tar/gzip metadata can change even when the helper tree is identical.

`scripts/release-metadata.mjs` produces/verifies the component metadata and signs
release metadata. Main CI passes `NOTIFIER_ARTIFACT_DIR` into `make dist`, which
copies the verified app bytes rather than rebuilding them. Local `make dist`
without that directory remains a manual source build, not the published component.

The updater checks the signed tree digest (paths, executable bits and every
file's bytes). An identical app is not copied, renamed, re-signed, or registered
again. The manual installer also leaves byte/mode-identical apps untouched.
Keep the bundle ID `net.moesl.radar.notifier` and normal installed location stable.

Publication retries never clobber assets. If an existing CLI release has differing
or incomplete bytes, publication stops for maintainer inspection rather than
replacing it. A rebuilt archive can differ because of compiler/build timestamps.
Do not delete a published release to force a retry: use a new version. Inspect
incomplete **drafts** explicitly. After binary publication succeeds, retry only
the failed npm job, and review an existing successful npm stage instead of
creating another one. Component artifacts similarly must not be overwritten.
If a publication fix requires source changes after a tag was pushed, cut a new
version rather than moving that tag. Reuse an already-published notifier component
unchanged; a failed CLI release does not invalidate its immutable component.

The publication script is tested with macOS system Bash, including stable releases
with no prerelease flag. The workflow's **Record release trigger** step logs only
run/ref/commit and push before/after hashes and creation/deletion/force flags. For
duplicate mirrored tag events, compare those fields: the commit alone can hide
changes to an annotated tag object. Concurrency serializes runs but is not event
deduplication. Inspect mirror settings/delivery evidence before changing mirroring;
do not bypass immutable-asset verification to make a duplicate run appear successful.

### Repair a component tag missing from the source forge

Do not delete releases, overwrite artifacts, move the GitHub tag, or disable
mirror behavior to work around this problem. Inspect the current published
component and its exact GitHub ref first. Historical drafts remain evidence; a
previously published draft is never silently republished.

If the authoritative origin is missing the currently published component's tag,
import that **exact ref**, without force, and publish it with the fix:

```sh
tag="notifier-v$(cat macos/RadarNotifier/VERSION)"
git fetch https://github.com/ChristianMoesl/radar "refs/tags/$tag:refs/tags/$tag"
git rev-parse "refs/tags/$tag" # Compare the object ID with GitHub before pushing.
git push --atomic origin main "refs/tags/$tag"
```

These are human-operated commands. If a local/source ref differs, stop for
inspection; never use `+`, `--force`, or a replacement tag. Verify the source
forge and GitHub retain the same object ID and release/asset IDs after mirroring.
This preserves the existing public component without creating another release.
After the October 2026 incident, `notifier-v1.0.0` has one active published
release and a retired historical draft; keep both asset sets untouched and reuse
the active published release. Fixing ref ownership does not rewrite that history
or claim the earlier archive regeneration never happened.

No new CLI/component version is needed for this ref repair. Complete any pending
npm approval separately; the binary job for an already-published CLI must not be
rebuilt merely to retry npm authentication.

## User flow and recovery

On macOS, run `radar update` in a terminal to review and confirm a release.
The dashboard shows a non-blocking update notice pointing to this command, without
a dedicated shortcut. The notice is cached for an hour; `radar update` checks
trusted metadata and npm again.
There are no silent installs. An offline/rate-limited check does not block Radar.

The managed layout is deliberately restricted to the user's regular, owned
`~/.local/bin/radar` and `~/.local/libexec/radar/RadarNotifier.app`. Symlinks,
Homebrew/other package managers, custom prefixes, and checkout executables remain
manual. An existing source/archive install requires explicit adoption. `make
install` and the archive installer remain manual operations and remove the
managed receipt; they do not silently enroll into release management.

After confirmation, Radar verifies bounded downloads and safely extracts on the
destination filesystem. It checks both Mach-O architectures and the notifier's
code signature before stopping anything. It declines while accepted workspace
mutations are running. During activation, new mutations get a retry message.
The updater does not kill tmux, Pi, sandboxes, or alter workspaces.

`libexec/radar/.upgrade` contains the journal and previous files. The binary is
atomically renamed into place; the helper is replaced only if changed. The new
daemon's actual version/hash is checked before the receipt is committed. Failure
stops the new daemon and restores previous files when possible. If interrupted,
run `radar update` again: it offers recovery **before** looking for releases.
Keep the recovery directory if it reports an error; do not delete it to bypass
verification. Recover only from the expected trusted previous binary if the
installed executable itself cannot run. After a successful update, previous files
are retained until the next update; they are recovery evidence, not an automatic
rollback of user data or Pi packages. Other resident clients must reopen; clients
with this updater-capable code refuse mutations after replacement. Before the
first adoption, close dashboards that predate this guard. No shim can retrofit
new locking into already-running old code; leave tmux/Pi/workspaces running.

A helper needing macOS approval is a separate **notification setup required**
state, not failure of the whole dashboard update. Run
`radar setup notifications` for the setup/test flow. See [approval directions](installation.md#macos-notification-setup).

## Pi coordination

Radar shows the detected host profile, installed package version, and separately
reported loaded session versions. Reporting is informational; older extensions
and non-Radar sessions may not report. Reports use per-session files under
`<agent-dir>/radar/loaded`, heartbeat once a minute, expire after three minutes,
and are removed on session shutdown. No prompts, conversation content, or tokens
are recorded.

Only a standard user-level npm declaration is eligible for coordinated adoption.
Pi's targeted `update` follows the configured source, not a requested exact
version. Radar therefore asks separately before using:

```sh
PI_CODING_AGENT_DIR=/path/to/profile pi install npm:@christianmoesl/pi-radar@X.Y.Z
```

This creates an **explicit Radar-managed pin**; `<agent-dir>/radar/release-pin.json`
records that ownership so subsequent updates can move it with confirmation.
If the user changes the pin, or the declaration is disabled/filtered, project-
scoped, Git/local/development, it is left alone. No broad `pi update`, Pi-core
update, pi-sbx update, sandbox-image update or forced session reload occurs.

Pi installation happens after the CLI is healthy and is not an atomic part of
that transaction. Its failure is reported as partial success, with a targeted
repair command. Inspect `pi list`/the shown profile if a package operation was
interrupted; do not assume it rolled back. Existing Pi sessions still need
`/reload` or restart at a safe point, even after the on-disk version is correct.

## Validation before general distribution

Unit/integration fixtures cover signatures, release/npm readiness, hostile
archives, architecture, unchanged apps, crash boundaries, concurrent operations,
Pi declarations and loaded-version reporting. They do not establish Gatekeeper
behavior across every macOS version or delivery route.

Before shipping, test an actual GitHub/browser download and the real updater's
N→N+1 route on supported macOS architectures: unchanged and changed notifier,
manual launch approval, notification authorization/delivery/click, daemon/TUI
restart, active Pi sessions, interrupted recovery and Pi failure. The prior
synthetic-quarantine experiment on one Apple Silicon Mac confirmed reapproval
friction and retained notification permission; it is not a replacement for those
release tests. Disclose any architecture/minimum-OS paths not yet exercised.
