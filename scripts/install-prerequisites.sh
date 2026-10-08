#!/usr/bin/env bash
# Shared by source and release installers. Safe to source to retain Homebrew PATH.
# The HTTPS bootstrap obtains this helper only from an authenticated archive.

radar_tool_ready() {
  local tool=$1 version binary=$1
  if [[ $tool = fd ]] && ! command -v fd >/dev/null 2>&1 && [[ $(uname -s) = Linux ]]; then
    binary=fdfind
  fi
  command -v "$binary" >/dev/null 2>&1 || return 1
  if [[ $tool = tmux ]]; then
    version=$("$binary" -V 2>/dev/null) || return 1
    [[ $version =~ ^tmux\ (next-)?([0-9]+)\.([0-9]+) ]] || return 1
    (( BASH_REMATCH[2] > 3 || (BASH_REMATCH[2] == 3 && BASH_REMATCH[3] >= 2) ))
  else
    version=$(env -u NODE_OPTIONS -u NODE_PATH "$binary" --version 2>/dev/null) || return 1
    case "$tool" in
      node|pi)
        [[ $version =~ ^v?([0-9]+)\.([0-9]+)\.([0-9]+)$ ]] || return 1
        if [[ $tool = node ]]; then
          (( BASH_REMATCH[1] >= 24 ))
        else
          (( BASH_REMATCH[1] > 0 || BASH_REMATCH[2] > 85 || (BASH_REMATCH[2] == 85 && BASH_REMATCH[3] >= 1) ))
        fi
        ;;
      *) return 0;;
    esac
  fi
}

radar_install_prerequisites() {
  local tool package answer native_brew brew_prefix
  local -a command_args
  # Activate an existing native Homebrew before judging tools on PATH.
  if [[ $(uname -s) = Darwin ]]; then
    native_brew=/opt/homebrew/bin/brew
    [[ $(uname -m) != x86_64 ]] || native_brew=/usr/local/bin/brew
    if command -v brew >/dev/null 2>&1; then
      native_brew=$(command -v brew)
    fi
    if [[ -x $native_brew ]]; then
      brew_prefix=$("$native_brew" --prefix) || return 1
      export PATH="$brew_prefix/bin:$brew_prefix/sbin:$PATH"
    fi
  fi
  echo 'Checking required tools: Git, tmux 3.2+, fd, Node.js 24+, npm, Pi 0.85.1+, GitHub CLI…' >&2
  for tool in git tmux fd node npm pi gh; do
    if radar_tool_ready "$tool"; then
      printf '✓ %s ready\n' "$tool" >&2
      continue
    fi
    if [[ $tool = pi ]]; then
      command_args=(npm install --global --ignore-scripts @earendil-works/pi-coding-agent)
    elif command -v brew >/dev/null 2>&1; then
      package=$tool
      [[ $tool != npm ]] || package=node
      command_args=(brew install "$package")
    elif [[ $(uname -s) = Linux ]] && command -v apt-get >/dev/null 2>&1; then
      package=$tool
      [[ $tool != fd ]] || package=fd-find
      [[ $tool != node ]] || package=nodejs
      command_args=(apt-get install -y "$package")
      [[ $tool != node ]] || command_args+=(npm)
      [[ $(id -u) = 0 ]] || command_args=(sudo "${command_args[@]}")
    else
      printf '%s is missing or too old. Install it on PATH (automatic installation needs Homebrew or apt-get), then rerun this installer.\n' "$tool" >&2
      return 1
    fi
    printf '! %s is missing or too old.\n  Command:' "$tool" >&2
    printf ' %s' "${command_args[@]}" >&2
    printf '\n' >&2
    # Piped input is never approval, including when called through make install.
    if ! { exec 9</dev/tty; } 2>/dev/null; then
      echo 'Missing prerequisites need approval in an interactive terminal. Install the tools on PATH or rerun this installer in a terminal.' >&2
      return 1
    fi
    while :; do
      printf 'Install or update %s now? [Y/n] ' "$tool" >&2
      answer=
      if ! read -r answer <&9; then exec 9<&-; return 1; fi
      case "$answer" in
        ''|y|Y|yes|Yes|YES) break;;
        n|N|no|No|NO) exec 9<&-; echo 'Required installation declined.' >&2; return 1;;
        *) echo 'Please answer yes or no.' >&2;;
      esac
    done
    if ! env -u NODE_OPTIONS -u NODE_PATH "${command_args[@]}" <&9; then
      exec 9<&-
      printf 'Installing %s failed. Fix the command above, then rerun this installer.\n' "$tool" >&2
      return 1
    fi
    exec 9<&-
    if ! radar_tool_ready "$tool"; then
      printf '%s is still missing or too old on PATH. Install the required version, check PATH, then rerun this installer.\n' "$tool" >&2
      return 1
    fi
  done
}
