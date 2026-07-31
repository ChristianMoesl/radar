#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")" && pwd)
prefix=${PREFIX:-"$HOME/.local"}
bindir=${BINDIR:-"$prefix/bin"}
libexecdir=${LIBEXECDIR:-"$prefix/libexec/radar"}

if [[ ! -x "$root/bin/radar" ]]; then
  echo "install.sh must be run from an extracted Radar release archive" >&2
  exit 1
fi

install -d "$bindir"
install -m 0755 "$root/bin/radar" "$bindir/radar"

notifier="$root/libexec/radar/RadarNotifier.app"
if [[ -d "$notifier" ]]; then
  if [[ $(uname -s) != Darwin ]]; then
    echo "the Radar notifier app can only be installed on macOS" >&2
    exit 1
  fi

  rm -rf "$libexecdir/RadarNotifier.app"
  install -d "$libexecdir"
  cp -R "$notifier" "$libexecdir/RadarNotifier.app"

  lsregister="/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
  "$lsregister" -f "$libexecdir/RadarNotifier.app"
fi

printf 'Installed Radar at %s\n' "$bindir/radar"
if [[ -d "$libexecdir/RadarNotifier.app" ]]; then
  printf 'Installed Radar notifier at %s\n' "$libexecdir/RadarNotifier.app"
fi
printf 'Restart a running daemon with: radar restart\n'
