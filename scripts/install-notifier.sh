#!/usr/bin/env bash
set -euo pipefail
[[ $# = 2 ]] || { echo "usage: $0 source.app destination.app" >&2; exit 2; }
source=$1
destination=$2
# Do not touch an identical installed bundle: even harmless copies can force
# launch approval again. Compare executable modes as well as every file's bytes.
bundle_modes() { (cd "$1" && find . ! -type d -exec stat -f "%Sp %N" {} + | LC_ALL=C sort); }
if [[ -d "$destination" ]] && diff -qr "$source" "$destination" >/dev/null && [[ "$(bundle_modes "$source")" = "$(bundle_modes "$destination")" ]]; then
  echo "Radar notifier unchanged; preserving installed app"
  exit 0
fi
mkdir -p "$(dirname "$destination")"
staged="$(mktemp -d "$(dirname "$destination")/.notifier-install.XXXXXX")"
trap 'rm -rf "$staged"' EXIT
cp -R "$source" "$staged/RadarNotifier.app"
codesign --verify --strict "$staged/RadarNotifier.app"
# Manual make/archive installation is not a managed upgrade transaction. The
# in-app updater owns staged activation and recovery for managed installations.
rm -rf "$destination"
mv "$staged/RadarNotifier.app" "$destination"
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$destination"
echo "Radar notifier installed; run radar setup notifications if macOS approval is needed"
