# Publishing the Pi package

Radar releases its CLI binaries and `@christianmoesl/pi-radar` together from `vX.Y.Z` tags. The npm package version is `X.Y.Z`. Development dependencies and artifact creation use the pnpm version pinned in `package.json`; registry publication uses npm CLI's trusted publishing support.

The package contains `package.json`, `README.md`, and `extensions/pi-radar/index.ts`. Pi loads the TypeScript directly and supplies its runtime imports. The package neither bundles Pi nor installs the Radar binary.

## One-time bootstrap

Trusted publisher configuration is package-specific. Since a new package has no settings page yet, its npm owner must publish the first reviewed version interactively before configuring automated publication.

1. Confirm control of the `@christianmoesl` npm scope and decide the package's licensing before its first public release. Do not invent a license as part of release automation.
2. From the reviewed release commit, use Node 24 and npm 11.5.1 or newer. Install dependencies with `pnpm install --frozen-lockfile`, run `pnpm check` and `make test`, and verify the version with `pnpm check:release v0.1.0` (substitute the intended bootstrap version).
3. Pack and publish the reviewed artifact interactively:

   ```sh
   package="$(mktemp -d)/pi-radar.tgz"
   pnpm pack --out "$package"
   npm login
   npm publish "$package" --access public
   ```

   Complete npm's authentication/2FA prompts yourself. A prerelease must use `--tag next` instead of updating `latest`. No npm credential needs to be added to GitHub secrets.
4. Configure the trusted publisher below. The first automated release must use the next **unpublished** version; do not push a release tag that would attempt to republish the bootstrap npm version.

## Configure npm's trusted publisher

Open `@christianmoesl/pi-radar` on npmjs.com, then **Settings → Trusted publishing**, and select GitHub Actions:

| Field | Value |
| --- | --- |
| Organization or user | `ChristianMoesl` |
| Repository | `radar` |
| Workflow filename | `release.yml` |
| Environment name | Leave empty for the shipped workflow |
| Allowed action | Allow direct `npm publish` |

Use the filename only, not `.github/workflows/release.yml`. Values are case-sensitive. The workflow and `package.json.repository.url` must identify the actual GitHub repository.

The shipped workflow uses a dedicated `publish-npm` job with `contents: read` and `id-token: write`. npm exchanges that job's GitHub-issued OIDC identity for short-lived, package-scoped publishing credentials. It needs neither `NPM_TOKEN` nor `NODE_AUTH_TOKEN`. The job uses a GitHub-hosted runner, a fresh locked dependency installation without caching, version validation, and Pi checks before packing and publishing. It runs only after binary release validation and publication succeed. Stable versions publish to `latest`; prereleases publish to `next`.

GitHub-hosted Actions runners are supported; custom forge OIDC and self-hosted runners are not. If development pushes to another forge, its mirror must forward release tags to `github.com/ChristianMoesl/radar`, where this workflow must actually run.

## Protect the publishing path

- Restrict release tag creation and protect changes to `.github/workflows/release.yml`.
- For a manual approval gate, configure a protected GitHub environment, add `environment: npm` to the publishing job, and set the same environment name in npm's trusted publisher. Change both together; the shipped workflow does not require an environment.
- After a successful OIDC publication, consider npm's **Publishing access → Require two-factor authentication and disallow tokens** setting and revoke unused publishing tokens. This still permits trusted publishing. Verify the new path before changing existing access controls.

Trusted publishing automatically generates provenance for this public package when published from its public GitHub repository. No `--provenance` flag is needed. Provenance records the source and workflow; it does not replace protection of release tags, source, or dependencies.

## Verify and recover

After publication, inspect the npm version/provenance and smoke-test an isolated Pi configuration with the separately installed Radar CLI:

```sh
version=0.1.0 # Replace with the version just published.
agent="$(mktemp -d)"
PI_CODING_AGENT_DIR="$agent" pi install "npm:@christianmoesl/pi-radar@$version"
```

Use that same agent directory when launching Pi inside a registered Radar workspace and outside it. Verify that Radar tools appear only inside the workspace and remain available after `/reload`. Do not leave both the old Git source and npm source configured in an existing Pi installation; see [Pi integration](../README.md#pi-integration).

If publication fails, check the workflow filename, repository identity, allowed action, hosted runner, OIDC permission, and version/tag match. npm does not validate publisher settings when they are saved. If the GitHub binary release already succeeded, re-run only the failed npm job after fixing the cause. Never replace published versions or move release tags; an already published npm version requires a new version for changed content.

See [npm trusted publishing](https://docs.npmjs.com/trusted-publishers) for provider requirements and current configuration options.
