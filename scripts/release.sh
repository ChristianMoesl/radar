#!/usr/bin/env bash
set -euo pipefail

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

branch="$(git branch --show-current)"
if [[ "$branch" != "main" ]]; then
  echo "releases must be cut from main; current branch is $branch" >&2
  exit 1
fi

if [[ -n "$(git status --porcelain)" ]]; then
  echo "working tree must be clean before releasing" >&2
  git status --short >&2
  exit 1
fi

git fetch origin main --tags

if [[ "$(git rev-parse HEAD)" != "$(git rev-parse origin/main)" ]]; then
  echo "local main must match origin/main before releasing" >&2
  echo "run: git pull --ff-only origin main" >&2
  exit 1
fi

if git rev-parse -q --verify "refs/tags/$version" >/dev/null; then
  echo "tag already exists locally: $version" >&2
  exit 1
fi

if git ls-remote --exit-code --tags origin "refs/tags/$version" >/dev/null 2>&1; then
  echo "tag already exists on origin: $version" >&2
  exit 1
fi

commit="$(git rev-parse --short=12 HEAD)"

make test
make dist VERSION="$version" COMMIT="$commit"

git tag -a "$version" -m "$version"
git push origin main
git push origin "$version"

echo "released $version from $commit"
echo "GitHub Actions will publish the binaries from the tag workflow."
