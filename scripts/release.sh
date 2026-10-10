#!/usr/bin/env bash
set -euo pipefail

# Validation includes terminal subprocesses. Preserve the caller's exact modes,
# including newline processing, even if a fixture exits while still in raw mode.
terminal_state=
if [[ -t 0 ]]; then
  terminal_state=$(stty -g)
fi
restore_terminal() {
  if [[ -n "$terminal_state" ]]; then
    stty "$terminal_state" || true
  fi
}
trap restore_terminal EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
validate() {
  local status=0
  "$@" || status=$?
  restore_terminal
  return "$status"
}

usage() {
  echo "usage: make release VERSION=vX.Y.Z" >&2
}

version="${1:-}"
if [[ -z "$version" ]]; then
  usage
  exit 2
fi

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "release version must look like vX.Y.Z, got: $version" >&2
  exit 2
fi

root="$(git rev-parse --show-toplevel)"
cd "$root"

validate node scripts/release-version.mjs validate "$version"

branch="$(git branch --show-current)"
if [[ "$branch" != "main" ]]; then
  echo "releases must be cut from main; current branch is $branch" >&2
  exit 1
fi

worktree_status="$(git status --porcelain)"
if [[ -n "$worktree_status" ]]; then
  echo "working tree must be clean before releasing" >&2
  git status --short >&2
  exit 1
fi

starting_commit="$(git rev-parse HEAD)"
git fetch origin main --tags

if ! git merge-base --is-ancestor origin/main HEAD; then
  echo "local main is behind or diverged from origin/main; reconcile it before releasing" >&2
  exit 1
fi

if git rev-parse -q --verify "refs/tags/$version" >/dev/null; then
  echo "tag already exists locally: $version" >&2
  exit 1
fi

if git ls-remote --exit-code --tags origin "refs/tags/$version" >/dev/null 2>&1; then
  echo "tag already exists on origin: $version" >&2
  exit 1
else
  status=$?
  if [[ $status != 2 ]]; then
    echo "could not verify that $version is unused on origin" >&2
    exit "$status"
  fi
fi

notifier_version=$(cat macos/RadarNotifier/VERSION)
notifier_tag="notifier-v$notifier_version"
notifier_ref="refs/tags/$notifier_tag"
notifier_state=$(mktemp -d)
trap 'restore_terminal; rm -rf "$notifier_state"' EXIT

# Keep component refs in the authoritative origin, not only on its GitHub mirror.
# An existing GitHub-only ref must be imported exactly, never recreated at HEAD.
gh api repos/ChristianMoesl/radar/releases --paginate --slurp >"$notifier_state/releases.json"
notifier_release=$(node scripts/notifier-release-state.mjs "$notifier_state/releases.json" "$notifier_tag")
if gh api "repos/ChristianMoesl/radar/git/ref/tags/$notifier_tag" >"$notifier_state/ref.json" 2>"$notifier_state/error"; then
  github_ref=$(node -e 'const r = require(process.argv[1]); if (!/^[a-f0-9]{40}$/.test(r.object?.sha)) throw new Error("invalid notifier tag"); console.log(r.object.sha)' "$notifier_state/ref.json")
  local_ref=$(git rev-parse -q --verify "$notifier_ref" || true)
  if [[ "$github_ref" != "$local_ref" ]]; then
    echo "$notifier_tag exists on GitHub but is missing or different locally; import its exact ref into origin before releasing" >&2
    exit 1
  fi
elif ! grep -q 'HTTP 404' "$notifier_state/error" || [[ "$notifier_release" != new ]]; then
  cat "$notifier_state/error" >&2
  echo "cannot establish an unused notifier version; restore its exact tag/release, never rebuild it" >&2
  exit 1
fi

# Preflight includes network requests; don't overwrite edits made while waiting.
worktree_status="$(git status --porcelain)"
if [[ "$(git branch --show-current)" != main || "$(git rev-parse HEAD)" != "$starting_commit" || -n "$worktree_status" ]]; then
  echo "repository changed during preflight; refusing to prepare release versions" >&2
  exit 1
fi
# A failed validation leaves this local commit available for inspection/retry;
# no reset, tag or remote update is performed to hide a failure.
validate node scripts/release-version.mjs prepare "$version"
worktree_status="$(git status --porcelain -- . ':!package.json' ':!extensions/pi-radar/version.ts')"
if [[ "$(git branch --show-current)" != main || "$(git rev-parse HEAD)" != "$starting_commit" || -n "$worktree_status" ]]; then
  echo "repository changed while preparing the version; inspect local changes before retrying" >&2
  exit 1
fi
if ! git diff --quiet HEAD -- package.json extensions/pi-radar/version.ts; then
  git commit --only -m "chore: release $version" -- package.json extensions/pi-radar/version.ts
fi
release_commit="$(git rev-parse HEAD)"
commit="$(git rev-parse --short=12 HEAD)"
validate pnpm check:release "$version"

validate pnpm install --frozen-lockfile
validate pnpm check
validate make test
validate make dist VERSION="$version" COMMIT="$commit"

worktree_status="$(git status --porcelain)"
if [[ "$(git branch --show-current)" != main || "$(git rev-parse HEAD)" != "$release_commit" || -n "$worktree_status" ]]; then
  echo "repository changed during validation; refusing to tag or push unvalidated changes" >&2
  exit 1
fi
if ! git rev-parse -q --verify "$notifier_ref" >/dev/null; then
  git tag -s "$notifier_tag" "$release_commit" -m "Radar notifier $notifier_version"
fi
git tag -s "$version" "$release_commit" -m "$version"
# Main and both tags reach origin in one transaction before mirror hooks run.
# Pin the tested commit explicitly; a concurrent remote update rejects the push.
git push --atomic origin "$release_commit:refs/heads/main" "$notifier_ref" "refs/tags/$version"

echo "released $version from $commit"
echo "GitHub Actions will publish the binaries and stage the Pi package for npm review."
echo "Approve the staged package with 2FA on npmjs.com before it becomes publicly available."
