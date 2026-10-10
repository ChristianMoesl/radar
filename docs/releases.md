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
installation. The one-command macOS bootstrap embeds the same trust roots in
root `install.sh`; update both roots during rotation (tests enforce alignment).
Publishing changes to main makes that script available at the README's installer
URL; no immutable CLI/component assets need to be replaced. This documentation and a key accompanying an untrusted download
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
- From a **clean `main` checkout on macOS**, run:

  ```sh
  make release VERSION=vX.Y.Z
  ```

  Do **not** manually bump the version files or run `npm version` / `pnpm version`
  first: those commands can create a tag before Radar validates the release.
  An authenticated `gh` session and working Git tag signing are required.
- The command validates the target version, fetches refs, rejects used tags,
  and verifies notifier history/ref identity **before modifying files**. Local
  `main` may be ahead of `origin/main`; behind/diverged branches and staged,
  unstaged or untracked changes are rejected. It never merges or resets work.
- It synchronizes `package.json` and `extensions/pi-radar/version.ts`, repairing
  even a previously committed partial bump, and creates `chore: release vX.Y.Z`
  only when those files change. That commit contains only the two version files.
  The module still captures the actually loaded version for Pi sessions;
  `pnpm check:release vX.Y.Z` remains a **read-only** check in CI.
- It installs locked development dependencies and runs `pnpm check`, `make test`
  and release builds against the prepared commit. Only after validation succeeds
  does it sign the CLI tag (and a notifier tag for a genuinely new component),
  then atomically push **main, `notifier-v<version>`, and `vX.Y.Z`** to `origin`.
  The exact validated commit is used for build metadata and tags. Repository
  changes during validation abort publication. A remote advance rejects the
  non-forced atomic push; there is no separate-push fallback.
- A validation/build failure leaves the version commit **local**, without tags
  or a push. Fix the failure, commit any fixes, and rerun the same command: an
  already-aligned version does not produce a duplicate bump commit. A failed
  version write or Git commit may leave edits for inspection; nothing is silently
  discarded. If tagging/pushing failed, inspect the remaining local/remote refs
  first; never move a used tag or force publication.
- Change `macos/RadarNotifier/VERSION` only for an actual component change,
  including deliberate SDK/compiler/security rebuilds. Existing component refs
  and published artifacts are reused exactly, never retagged or rebuilt at HEAD.
  Swift/plist/icon/build inputs cannot change under an existing component version.
- `make test` limits Go package concurrency to two so subprocess/PTY fixtures are
  not starved during release checks. The release script restores the caller's
  exact terminal modes after each validation stage and on exit. If the terminal
  was already left in raw mode, run `stty sane` before retrying. Humans run this
  publishing command; agents may prepare changes but must not execute its push.
  Verify that the tags arrive on GitHub through the development forge's mirror.
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

## Homebrew tap publishing

The public tap is `ChristianMoesl/homebrew-tap`, with `Formula/radar.rb`. Users
install with `brew install ChristianMoesl/tap/radar`. The formula source template
and generator live in this repository under `scripts/homebrew/`; do not maintain
a second hand-written copy of the packaging logic in the tap.

### First publication

Create the public `ChristianMoesl/homebrew-tap` repository and add a short README
with the install command. **The first formula must use a new Radar release that
includes Homebrew support**, including setup's default-agent-instruction creation.
Older releases such as `v0.1.2` do not provide that setup behavior. Do not announce
the README's Homebrew option until the tap formula has been published and tested.
No tap repository or formula is created by the normal CLI release workflow.

### Each release

1. Publish a new stable Radar release normally and approve its exact npm package.
   Do not bypass staged npm approval or regenerate immutable archives.
2. Run the **Homebrew formula** Actions workflow with `vX.Y.Z`. It checks out that
   release's source, runs the generator tests, and generates a formula only when
   the requested version is the latest coordinated public release. A missing
   matching npm package, invalid signature, incompatible state epoch, unpublished
   architecture or wrong archive hash stops the job. Both macOS archives are
   downloaded and authenticated. No older-version fallback is emitted.
3. The workflow installs/tests the candidate in a temporary runner-local tap and
   uploads `Formula/radar.rb` as the `homebrew-formula` artifact. It does **not**
   create/push a tap repository. It needs no cross-repository token or npm secret.
4. Review the formula diff (version, both URLs/hashes, prerequisite minimums and
   install hooks), copy it into `ChristianMoesl/homebrew-tap/Formula/radar.rb`, and
   have a human commit/push it. Never overwrite an existing version's artifacts.
5. Check `brew update && brew install ChristianMoesl/tap/radar` from the public tap.
   For existing installations check `brew upgrade radar`, daemon restart and the
   separately managed Pi extension. The standard release workflow cannot publish
   the formula early: npm approval happens after it finishes.

For local preparation, run from the **requested release's source checkout** with
Go available (the tag must match `package.json`):

```sh
mkdir -p build/homebrew/Formula
go run ./scripts/homebrew vX.Y.Z > build/homebrew/Formula/radar.rb.tmp &&
  mv build/homebrew/Formula/radar.rb.tmp build/homebrew/Formula/radar.rb
```

A failed generator writes no partial formula and does not alter a tap. Its network
endpoints/trust keys are the same committed ones as the updater, without an
unsigned mode or configurable production trust override. Formula users trust
Homebrew and the tap's reviewed Git history; Homebrew verifies the recorded
SHA-256, rather than running Radar's signed-manifest updater during installation.
This is a separate distribution trust path, not a claim that hashes alone prove
publisher identity. Maintainers authenticate those hashes before publishing.

Homebrew owns the binary and notifier under its Cellar; runtime notifier lookup
resolves the `bin/radar` symlink and uses the same `libexec/radar` layout as release
archives. Install verifies the app's code signature; post-install registers the
`opt` app path without launching it or modifying Gatekeeper. Cellar paths change
on upgrade, so app-specific launch approval may need repeating even if component
bytes are unchanged. The standalone updater's unchanged-app optimization is not
promised for Homebrew. The formula declares only fd, tmux and gh as tool dependencies. Git is expected
on PATH; Node/npm and Pi are user-managed and verified during setup, never
installed or upgraded by Radar's Homebrew formula. User configuration and Pi
profile changes belong to the interactive setup/review, never formula hooks.

The Actions smoke test covers the runner's architecture, not both native Macs.
Before general distribution validate Apple Silicon and Intel, first-run setup,
existing/symlinked `AGENTS.md` preservation, notifications/approval/clicks,
upgrade/daemon restart, existing `~/.local` PATH conflicts, and uninstall leaving
user files untouched. Review data rollout instructions before a brew upgrade:
Homebrew does not offer Radar's managed updater's migration guard or rollback.
Linux/WSL keep their existing manual install route; this formula is macOS-only.

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
Homebrew/other package managers, custom prefixes, and checkout executables are not
adopted. Homebrew installs use `brew upgrade radar`; other layouts remain manual. An existing source/archive install requires explicit adoption. `make
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

Before shipping, test the one-command bootstrap on a clean macOS GUI machine:
Homebrew absent/present, Node absent/old/present, each refusal, consented PATH edits,
and Apple launch/notification approval. Hermetic tests do not establish that real
Homebrew/native approval flow. Also test an actual GitHub/browser download and the real updater's
N→N+1 route on supported macOS architectures: unchanged and changed notifier,
manual launch approval, notification authorization/delivery/click, daemon/TUI
restart, active Pi sessions, interrupted recovery and Pi failure. The prior
synthetic-quarantine experiment on one Apple Silicon Mac confirmed reapproval
friction and retained notification permission; it is not a replacement for those
release tests. Disclose any architecture/minimum-OS paths not yet exercised.


## Documentation delivery and recovery layout

`make manpages` generates `radar(1)` and `radar-config(5)` from the Markdown
sources using a pinned Go build dependency. `make build`, `make install` and
`make dist` include that step; generated files live under `build/man` and are
not committed. Release archives contain `share/man/man1/radar.1` and
`share/man/man5/radar-config.5`. The binary embeds the public Markdown docs for
Pi's read-only documentation transport. The npm extension does not duplicate
the docs or rely on a sandbox-visible host path.

Both archive validators allow only these exact additional files. The archive
checksum/signature covers them. Managed updates require both pages, back up
existing ones, and restore them (or remove newly installed pages) on failure.
Symlinks, shared-writable manual installation paths and externally edited pages
during recovery are rejected rather than overwritten.

The update journal at `~/.local/libexec/radar/.upgrade/journal.json` now uses
**schema 2**, recording the previous/next digests for both manuals. Receipts,
workspace state, configuration and notes keep their existing formats. There is
no automatic journal migration. Before installing this version, inspect any
existing journal: finish/recover an **uncommitted** schema-1 transaction with
the previous Radar version; for a **committed** schema-1 transaction, explicitly
remove its `.upgrade` recovery directory only after deciding its retained backup
is no longer needed. Unsupported journals fail closed. Installations with no
journal need no reset. An older updater also rejects the new archive members;
use a reviewed manual/source installation or the current bootstrap for this
first upgrade, rather than expecting an old binary to accept the new contract.
