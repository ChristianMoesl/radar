#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p "$root/build"
directory=$(mktemp -d "$root/build/release-notifier.XXXXXX")
version=$(cat macos/RadarNotifier/VERSION)
tag="notifier-v$version"
repo="ChristianMoesl/radar"
trap 'rm -rf "$directory"' EXIT

# Tags deleted by mirroring leave historical drafts behind. Never interpret a
# tag lookup's 404 as permission to rebuild a previously published component.
gh api "repos/$repo/releases" --paginate --slurp >"$directory/releases.json"
release_id=$(node scripts/notifier-release-state.mjs "$directory/releases.json" "$tag")

# Only the human-operated release flow creates refs in the authoritative origin.
# CI must not create a GitHub-only tag that a later full mirror can delete.
gh api "repos/$repo/git/ref/tags/$tag" >/dev/null
if [[ "$release_id" == new ]]; then
  for arch in amd64 arm64; do
    scripts/build-notifier-app.sh "$directory/$arch/RadarNotifier.app" "$arch"
    COPYFILE_DISABLE=1 tar -czf "$directory/radar-notifier_${version}_${arch}.tar.gz" -C "$directory/$arch" RadarNotifier.app
  done
  node scripts/release-metadata.mjs component "$directory"
  gh release create "$tag" "$directory"/*.tar.gz "$directory/notifier.json" "$directory/notifier.json.sig" \
    --repo "$repo" --verify-tag --title "Radar notifier $version" \
    --notes 'Immutable notification component; not a CLI release.' --draft
  gh release edit "$tag" --repo "$repo" --draft=false --latest=false
else
  node scripts/notifier-release-state.mjs "$directory/releases.json" "$tag" assets >"$directory/assets.tsv"
  while IFS=$'\t' read -r asset_id filename; do
    # Bind downloads to the selected release's immutable asset IDs, not an
    # ambiguous tag shared with historical drafts. Names are strictly checked.
    gh api "repos/$repo/releases/assets/$asset_id" --header 'Accept: application/octet-stream' >"$directory/$filename"
  done <"$directory/assets.tsv"
  node scripts/release-metadata.mjs verify-component "$directory"
  for arch in amd64 arm64; do
    mkdir -p "$directory/$arch"
    tar -xzf "$directory/radar-notifier_${version}_${arch}.tar.gz" -C "$directory/$arch"
  done
  node scripts/release-metadata.mjs verify-component "$directory" extracted
  if [[ "$(node scripts/notifier-release-state.mjs "$directory/releases.json" "$tag" draft)" == true ]]; then
    # Only a unique, never-published, complete and authenticated draft is resumable.
    gh api "repos/$repo/releases/$release_id" --method PATCH -F draft=false -f make_latest=false >/dev/null
  fi
fi

rm -f "$directory/releases.json" "$directory/assets.tsv"
rm -rf "$root/build/release-notifier"
mv "$directory" "$root/build/release-notifier"
