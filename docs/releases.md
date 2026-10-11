# Releases and macOS updates

Radar's CLI and `@christianmoesl/pi-radar` share a release version. The small
macOS notifier has its own version and immutable artifacts. Homebrew is the
recommended Linux installation/update owner after the [publication gate](#linux-homebrew-rollout).
Linux manual archives/source builds and Windows/WSL remain available. The built-in
`radar update` transaction remains macOS-only.

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
  release, builds the existing macOS/Linux archives, signs `release.json`,
  retains the authenticated assets privately, and validates native Linux
  Homebrew install/test/uninstall on amd64 and arm64 before publishing the CLI
  release from a draft. npm staging depends on this publication gate.
- Approve the staged exact npm version with 2FA. Until it is publicly available,
  the coordinated updater skips that release and may offer an older eligible
  stable version. Drafts/prereleases are never automatic update candidates;
  prerelease archives and npm's `next` staging remain available manually.

`release.json.sig` signs the exact bytes of `release.json` using Ed25519. Metadata
binds versions, the macOS arm64 and Linux amd64/arm64 artifacts, minimum
macOS/Pi/Node requirements, archive size/hash, and binary hashes. The macOS entry
also binds notifier version/tree identity. Schema 1 retains the existing `arm64`
macOS key; `linux_amd64` and `linux_arm64` are explicit signed artifact keys with
no notifier fields. macOS updater/bootstrap eligibility still uses only `arm64`.
The formula generator requires all three keys and refuses old manifests lacking
Linux artifacts; it never falls back to unsigned `checksums.txt`. SHA-256 checksum lists alone
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

### Linux Homebrew rollout

This source change does **not** publish Linux support to the public tap. Before
announcing Linux Homebrew availability:

1. Cut a **new** CLI release containing Linux sidecars in signed `release.json`.
   Never overwrite immutable old release metadata/archives or regenerate the
   unchanged macOS notifier component.
2. Require native amd64/arm64 Linux Homebrew release jobs to pass before GitHub
   publication, then approve the exact staged npm package normally.
3. Run the manual Homebrew workflow for that release. Require all three native
   platform jobs to pass, review the single generated formula, and have a human
   publish it to the tap. Source CI alone does not validate public metadata/npm.
4. Smoke-test public tap install/test/uninstall on Linux amd64/arm64 and macOS
   Apple Silicon; test upgrades from the prior tap formula, PATH precedence and
   daemon restart separately. macOS launch approval/delivery/click still needs a
   GUI machine. Only then remove the pending-publication notices in user docs.

Existing state/config/notes formats and updater receipts are unchanged. No data
migration or automatic adoption of another installer is introduced.

Source CI tests real Homebrew on native Linux runners using a local-archive
fixture derived from the production formula. For a disposable Linux environment
with Homebrew and Node already available, run:

```sh
make dist VERSION=v1.2.3 DIST_TARGETS="linux/arm64" # use linux/amd64 on x86_64
scripts/homebrew/smoke-test.sh dist/radar_v1.2.3_linux_arm64.tar.gz
```

The script uses a unique temporary tap and runs style/install/test/uninstall,
then removes the tap. It never installs Homebrew or publishes a formula. Its
`file://` formula is **CI-only**, not publisher-authenticated. Release CI first
verifies all archives against signed metadata using committed trust roots; only
the production generator can emit a tap candidate after public/npm readiness.

### Each release

1. Publish a new stable Radar release normally and approve its exact npm package.
   Do not bypass staged npm approval or regenerate immutable archives.
2. Run the **Homebrew formula** Actions workflow with `vX.Y.Z`. It checks out that
   release's source, runs the generator tests, and generates a formula only when
   the requested version is the latest coordinated public release. A missing
   matching npm package, invalid signature, incompatible state epoch, unpublished
   architecture or wrong archive hash stops the job. All three archives (macOS arm64, Linux amd64/arm64) are
   downloaded and authenticated. No older-version fallback is emitted.
3. The workflow updates Homebrew, installs/tests/uninstalls the candidate in
   a temporary runner-local tap on Apple Silicon and native Linux amd64/arm64, and uploads
   `Formula/radar.rb` in the macOS job's `homebrew-formula` artifact. All three jobs must
   pass before publication. It does **not**
   create/push a tap repository. It needs no cross-repository token or npm secret.
4. Review the formula diff (version, all three platform URLs/hashes, prerequisite minimums and
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

Homebrew owns the binary under its Cellar, and on macOS also owns the notifier.
macOS runtime notifier lookup resolves the `bin/radar` symlink and uses the same
`libexec/radar` layout as release archives. macOS installation verifies the app's
code signature but does not register it:
current Homebrew sandboxes its post-install hooks and intentionally blocks Launch
Services registration. The foreground `radar setup notifications` flow verifies
and registers the bundle only after the user selects **Set up & test**. It does
not modify Gatekeeper or grant notification permission. Cellar paths change on
upgrade, so users should rerun notification setup and app-specific launch approval
may need repeating even if component bytes are unchanged. The standalone updater's
unchanged-app optimization is not promised for Homebrew. The formula declares only
fd, tmux and gh as tool dependencies. Git is expected on PATH; Node/npm and Pi are
user-managed and verified during setup, never installed or upgraded by Radar's
Homebrew formula. User configuration and Pi profile changes belong to the
interactive setup/review, never formula hooks.

The Actions smoke tests cover native macOS Apple Silicon and Linux amd64/arm64
installation, CLI/manual checks and uninstall with current Homebrew. macOS also
checks notifier lookup and code signing. They do not prove interactive launch
approval or delivery/clicks. Before general distribution
validate first-run setup, existing/symlinked `AGENTS.md` preservation,
notifications/approval/clicks, upgrade/daemon restart, existing `~/.local` PATH
conflicts, and uninstall leaving user files untouched. Review data rollout instructions before a brew upgrade:
Homebrew does not offer Radar's managed updater's migration guard or rollback.
Linux Homebrew installations use `brew upgrade radar` after the publication gate;
manual Linux and Windows/WSL installations retain their existing update route.

## macOS architecture policy

macOS support is Apple Silicon (arm64) only. Linux amd64 and arm64 releases and
sandbox images are unchanged. Build/install entry points reject Intel macOS;
Rosetta shells must be replaced with a native arm64 terminal.

New CLI and notifier releases publish only arm64 macOS archives. Existing signed
releases and component artifacts stay immutable, including their historical
Intel assets. The macOS updater/bootstrap validate and select only the supported
macOS arm64 artifact; they never download or install the Linux entries. The
Homebrew generator separately validates all three supported platform artifacts.

Older standalone Radar updaters and bootstrap scripts required both macOS
architectures. They cannot select a new arm64-only release: use the current
bootstrap or source installer for the one-time upgrade. Homebrew installations
continue to use `brew upgrade radar`. This changes no configuration, receipt or
recovery-journal layout; arm64 journals remain readable, and Intel journals are
intentionally unsupported. Recover any interrupted update before changing binaries.

The notifier build inputs changed, so the next release uses component **1.1.0**
rather than replacing 1.0.0. A source install or later binary upgrade can therefore
require macOS launch/notification approval again. No approval is bypassed.

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
The dashboard never installs updates in the background. Running `radar update`
also updates eligible unpinned Pi packages without a separate confirmation. An
offline/rate-limited dashboard check does not block Radar.

The managed layout is deliberately restricted to the user's regular, owned
`~/.local/bin/radar` and `~/.local/libexec/radar/RadarNotifier.app`. Symlinks,
Homebrew/other package managers, custom prefixes, and checkout executables are not
adopted. Homebrew installs use `brew upgrade radar`; other layouts remain manual. An existing source/archive install requires explicit adoption. `make
install` and the archive installer remain manual operations and remove the
managed receipt; they do not silently enroll into release management.

After confirmation, Radar verifies bounded downloads and safely extracts on the
destination filesystem. It checks that both CLI and notifier Mach-O binaries are arm64 and verifies the notifier's
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

`radar update` automatically updates installed, unfiltered user-level packages
with these standard unpinned declarations, without a separate Pi confirmation:

```sh
npm:@christianmoesl/pi-radar
npm:@christianmoesl/pi-sbx
```

It runs targeted `pi update <source> --no-approve` commands in the detected host
profile, leaving the declarations unpinned and ignoring project-local files in
the update subprocess. All pins are skipped, regardless of how they were created.
Disabled/filtered, project-scoped, Git/local/development, missing and custom
sources are left alone. Remove a pin manually if you want automatic updates;
Radar does not adopt or migrate pins.

Package updates follow npm's latest version independently. `pi-radar` should
match the Radar CLI release; a mismatch after updating is reported for separate
repair/retry, never resolved by silently creating a pin. `pi-sbx` has its own
versioning. Both eligible packages are checked even when the CLI is already
current, so rerunning `radar update` can repair a failed package update or pick
up a separately released `pi-sbx`. No broad package update, Pi-core update,
sandbox-image update or forced session reload occurs.

Pi updates happen after the CLI is healthy and are not an atomic part of that
transaction. Both packages are attempted independently; failures are reported as
partial success with targeted repair guidance. Do not assume a failed package
operation rolled back.

Interactive Radar Pi sessions check the locally installed `pi-radar` version
every 30 seconds. When it is newer than the loaded version, a red statusbar message
says **pi-radar updated · /reload to apply**. Otherwise no status is shown.
Reloading updates the loaded version and clears the notice. `pi-sbx` has no such
notice. No npm lookup, idle detection, automatic reload or subagent interruption
is involved: choose a safe moment for `/reload` yourself.

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
