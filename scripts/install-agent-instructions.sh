#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: install-agent-instructions.sh <template>" >&2
  exit 2
fi

template=$1
if [[ ! -f "$template" ]]; then
  echo "Radar agent instruction template does not exist: $template" >&2
  exit 1
fi

config_home=${XDG_CONFIG_HOME:-"$HOME/.config"}
config_dir="$config_home/radar"
destination="$config_dir/AGENTS.md"

install -d -m 0700 "$config_dir"
if [[ -e "$destination" || -L "$destination" ]]; then
  exit 0
fi

temporary=$(mktemp "$config_dir/.AGENTS.md.XXXXXX")
trap 'rm -f "$temporary"' EXIT
install -m 0600 "$template" "$temporary"

if ! ln "$temporary" "$destination" 2>/dev/null && [[ ! -e "$destination" && ! -L "$destination" ]]; then
  echo "Could not install Radar agent instructions at $destination" >&2
  exit 1
fi
