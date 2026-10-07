#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
mkdir -p "$root/build"
directory=$(mktemp -d "$root/build/release-notifier.XXXXXX")
version=$(cat macos/RadarNotifier/VERSION)
tag="notifier-v$version"
repo="ChristianMoesl/radar"
mkdir -p "$directory"
response=$(mktemp)
trap 'rm -f "$response"; rm -rf "$directory"' EXIT
if gh api "repos/$repo/releases/tags/$tag" >"$response" 2>&1; then
  gh release download "$tag" --repo "$repo" --dir "$directory" --pattern 'notifier.json*' --pattern '*.tar.gz'
  node scripts/release-metadata.mjs verify-component "$directory"
  for arch in amd64 arm64; do
    mkdir -p "$directory/$arch"
    tar -xzf "$directory/radar-notifier_${version}_${arch}.tar.gz" -C "$directory/$arch"
  done
  node scripts/release-metadata.mjs verify-component "$directory" extracted
  gh release edit "$tag" --repo "$repo" --draft=false --latest=false
elif grep -q 'HTTP 404' "$response"; then
  for arch in amd64 arm64; do
    scripts/build-notifier-app.sh "$directory/$arch/RadarNotifier.app" "$arch"
    COPYFILE_DISABLE=1 tar -czf "$directory/radar-notifier_${version}_${arch}.tar.gz" -C "$directory/$arch" RadarNotifier.app
  done
  node scripts/release-metadata.mjs component "$directory"
  gh release create "$tag" "$directory"/*.tar.gz "$directory/notifier.json" "$directory/notifier.json.sig" \
    --repo "$repo" --target "${GITHUB_SHA:?}" --title "Radar notifier $version" \
    --notes 'Immutable notification component; not a CLI release.' --draft
  gh release edit "$tag" --repo "$repo" --draft=false --latest=false
else
  cat "$response" >&2
  exit 1
fi

rm -rf "$root/build/release-notifier"
mv "$directory" "$root/build/release-notifier"
