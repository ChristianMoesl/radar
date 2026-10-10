#!/usr/bin/env bash
set -euo pipefail
tag=${1:?release tag required}
repo=ChristianMoesl/radar
set --
if [[ "$tag" == *-* ]]; then set -- --prerelease; fi
# A retry never clobbers published bytes. If CI rebuilt something differently,
# fail and retain the original release rather than silently replacing it.
if gh release view "$tag" --repo "$repo" >/dev/null 2>&1; then
  existing=$(mktemp -d)
  trap 'rm -rf "$existing"' EXIT
  gh release download "$tag" --repo "$repo" --dir "$existing"
  for file in dist/*.tar.gz dist/checksums.txt dist/release.json dist/release.json.sig; do
    cmp "$file" "$existing/$(basename "$file")" || { echo 'Release already exists with different/incomplete assets; inspect it manually. Nothing overwritten.' >&2; exit 1; }
  done
else
  notes=$(mktemp)
  trap 'rm -f "$notes"' EXIT
  node "$(dirname "$0")/release-notes.mjs" "$tag" > "$notes"
  gh release create "$tag" dist/*.tar.gz dist/checksums.txt dist/release.json dist/release.json.sig \
    --repo "$repo" --verify-tag --title "$tag" --notes-file "$notes" --draft "$@"
fi
gh release edit "$tag" --repo "$repo" --draft=false
