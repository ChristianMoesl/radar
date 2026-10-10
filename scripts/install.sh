#!/usr/bin/env bash
set -euo pipefail

# make dist embeds the shared helper here, keeping the archive layout stable.
source "$(dirname "${BASH_SOURCE[0]}")/install-prerequisites.sh"

radar_install_archive() {
  local root=$1
  local prefix=${PREFIX:-"$HOME/.local"}
  local bindir=${BINDIR:-"$prefix/bin"}
  local mandir=${MANDIR:-"$prefix/share/man"}
  local libexecdir=${LIBEXECDIR:-"$prefix/libexec/radar"}

  if [[ ! -x "$root/bin/radar" ]]; then
    echo "install.sh must be run from an extracted Radar release archive" >&2
    exit 1
  fi

  local page
  for page in man1/radar.1 man5/radar-config.5; do
    [[ -s "$root/share/man/$page" ]] || { echo "release archive is missing manual $page" >&2; return 1; }
  done

  install -d "$bindir"
  install -m 0755 "$root/bin/radar" "$bindir/radar"
  install -d "$mandir/man1" "$mandir/man5"
  install -m 0644 "$root/share/man/man1/radar.1" "$mandir/man1/radar.1"
  install -m 0644 "$root/share/man/man5/radar-config.5" "$mandir/man5/radar-config.5"
  install -d "$prefix/share/radar"
  install -m 0644 "$root/LICENSE" "$prefix/share/radar/LICENSE"
  "$root/install-agent-instructions.sh" "$root/share/radar/AGENTS.md"

  local notifier="$root/libexec/radar/RadarNotifier.app"
  if [[ -d "$notifier" ]]; then
    if [[ $(uname -s) != Darwin ]]; then
      echo "the Radar notifier app can only be installed on macOS" >&2
      exit 1
    fi

    "$root/install-notifier.sh" "$notifier" "$libexecdir/RadarNotifier.app"
    if [[ "$bindir" = "$HOME/.local/bin" && "$libexecdir" = "$HOME/.local/libexec/radar" ]]; then
      rm -f "$libexecdir/install.json"
    fi
  fi

  printf 'Manuals: man radar; man radar-config (if not found, use man -M "%s" radar)\n' "$mandir"
  printf 'Installed Radar at %s\n' "$bindir/radar"
  printf 'Radar agent instructions are available at %s\n' "${XDG_CONFIG_HOME:-$HOME/.config}/radar/AGENTS.md"
  if [[ -d "$libexecdir/RadarNotifier.app" ]]; then
    printf 'Installed Radar notifier at %s\n' "$libexecdir/RadarNotifier.app"
  fi
  printf 'Restart a running daemon with: radar restart\n'

  printf "On macOS: run radar setup notifications; use radar update for explicit managed-release adoption/updates.\n"
}

if [[ ${BASH_SOURCE[0]} = "$0" ]]; then
  root=$(cd "$(dirname "$0")" && pwd)
  [[ -x "$root/bin/radar" ]] || { echo 'install.sh must be run from an extracted Radar release archive' >&2; exit 1; }
  radar_install_prerequisites
  radar_install_archive "$root"
fi
