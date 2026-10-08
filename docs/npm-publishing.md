# Publishing the Pi package

Radar releases its CLI binaries and stages `@christianmoesl/pi-radar` from the same `vX.Y.Z` tags. The npm package version is `X.Y.Z`. Development dependencies and artifact creation use the pnpm version pinned in `package.json`; npm CLI uses trusted publishing to stage the artifact for review. An npm maintainer must approve it with 2FA before it becomes public.

The package is MIT-licensed and contains `LICENSE`, `package.json`, `README.md`, and the modules under `extensions/pi-radar/` (entrypoint, loaded-version reporter, and version constant). Pi loads the TypeScript directly and supplies its runtime imports. The package neither bundles Pi nor installs the Radar binary.

## Prerequisites

- The package was bootstrap-published as `0.1.0`. The failed `v0.1.1` release tag is already used; prepare `0.1.2` for the corrected baseline. Use a new unpublished version and never move a used tag or republish the bootstrap version.
- The approving npm account needs publish access to `@christianmoesl/pi-radar` and 2FA enabled.
- Staged publishing requires npm **11.15.0 or newer** and Node **22.14.0 or newer**. The workflow uses Node 24 and npm `^11.15.0`; use a compatible npm CLI for local review and approval.
- Configure the package's trusted publisher below. Do not add an npm publishing token to GitHub secrets.

## Configure npm's trusted publisher

Open `@christianmoesl/pi-radar` on npmjs.com, then **Settings → Trusted publishing**, and select GitHub Actions:

| Field | Value |
| --- | --- |
| Organization or user | `ChristianMoesl` |
| Repository | `radar` |
| Workflow filename | `release.yml` |
| Environment name | Leave empty for the shipped workflow |
| Allowed action | Stage only: leave **Allow npm publish** unchecked |

Use the filename only, not `.github/workflows/release.yml`. Values are case-sensitive. The workflow and `package.json.repository.url` must identify the actual GitHub repository. If the connection already permits direct publishing, update its allowed actions so CI can only stage packages.

The shipped workflow uses a dedicated `stage-npm` job with `contents: read` and `id-token: write`. npm exchanges that job's GitHub-issued OIDC identity for short-lived, package-scoped staging credentials. It needs neither `NPM_TOKEN` nor `NODE_AUTH_TOKEN`. The job uses a GitHub-hosted runner, a fresh locked dependency installation without caching, version validation, and Pi checks before packing and running `npm stage publish`. It runs only after binary release validation and publication succeed. Stable versions target `latest`; prereleases target `next`, taking effect only after approval.

A green release workflow means the binaries are published and the npm artifact is staged. The GitHub Actions summary explicitly states that npm review and 2FA approval are still required. Existing npm installs continue to receive the previously approved version until then. CLI binary releases do not wait for npm approval. The coordinated macOS updater, however, offers a release only once the exact matching npm version is public. See [release signatures and notifier artifact reuse](releases.md).

GitHub-hosted Actions runners are supported; custom forge OIDC and self-hosted runners are not. If development pushes to another forge, its mirror must forward release tags to `github.com/ChristianMoesl/radar`, where this workflow must actually run.

## Review and approve

Review the staged version in the **Staged Packages** tab on npmjs.com. Verify its version, intended dist-tag, source/provenance, file contents, and test results. Click **Approve** and complete 2FA to publish it. CI does not approve stages automatically.

For CLI review, log in interactively on your own machine:

```sh
npm login --registry=https://registry.npmjs.org
npm stage list @christianmoesl/pi-radar
```

Use the stage ID from that list, not the package name or release tag:

```sh
stage_id='<stage-id-from-list>'
npm stage view "$stage_id"
npm stage download "$stage_id"
npm stage approve "$stage_id"
```

Approval requires 2FA. If the artifact is wrong, use `npm stage reject "$stage_id"` instead of approving it. Never ask CI to handle an OTP or add an automated approval step: OIDC supports `npm stage publish`, but review/approval commands require interactive authentication.

If staging fails after the binary release succeeds, fix the cause and re-run only the failed `stage-npm` job. If it succeeds, review that existing stage rather than re-running CI to stage it again. Rejection does not retract the already-published binaries; use a new release version for corrected content and never move an existing tag.

## Protect the publishing path

- Restrict release tag creation and protect changes to `.github/workflows/release.yml`.
- Keep the trusted publisher stage-only so CI cannot bypass npm's approval step. No protected GitHub environment is required for the shipped workflow; npm supplies the manual approval gate.
- After a successful OIDC staging run, consider npm's **Publishing access → Require two-factor authentication and disallow tokens** setting and revoke unused publishing tokens. This still permits trusted publishing. Verify the new path before changing existing access controls.

Trusted publishing generates provenance for this public package from its public GitHub repository; inspect it during review and after approval. No `--provenance` flag is needed. Provenance records the source and workflow; it does not replace protection of release tags, source, or dependencies.

## Verify after approval

After approval, inspect the public npm version/provenance and smoke-test an isolated Pi configuration with the separately installed Radar CLI:

```sh
version=0.1.2 # Replace with the version just approved.
agent="$(mktemp -d)"
PI_CODING_AGENT_DIR="$agent" pi install "npm:@christianmoesl/pi-radar@$version"
```

Use that same agent directory when launching Pi inside a registered Radar workspace and outside it. Verify that Radar tools appear only inside the workspace and remain available after `/reload`. Do not leave both the old Git source and npm source configured in an existing Pi installation; see [Pi integration](../README.md#pi-integration).

If staging fails to authenticate, check the workflow filename, repository identity, allowed action, hosted runner, OIDC permission, npm version, and version/tag match. npm does not validate publisher settings when they are saved. If staging succeeds but an install cannot find the new version, confirm it has been approved and allow time for registry availability. Never replace published versions or move release tags.

See [npm staged publishing](https://docs.npmjs.com/staged-publishing) and [trusted publishing](https://docs.npmjs.com/trusted-publishers) for current provider requirements and configuration options.
