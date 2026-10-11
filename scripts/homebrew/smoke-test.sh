#!/usr/bin/env bash
set -euo pipefail
# Run only on disposable CI/test machines with Homebrew already installed.
# Uses a unique local tap; never updates the public tap or runs user setup.
archive=${1:?local native Linux archive required}
[[ $(uname -s) == Linux ]] || { echo 'This smoke test requires Linux' >&2; exit 1; }
root=$(cd "$(dirname "$0")/../.." && pwd)
case $(uname -m) in
  x86_64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo 'Unsupported Linux architecture' >&2; exit 1 ;;
esac
[[ $archive == *"_linux_${arch}.tar.gz" ]] || { echo 'Archive must match the native runner architecture' >&2; exit 1; }
tap="radar-ci/smoke-$(date +%s)-$$"
formula="$tap/radar"
cleanup() {
  brew list --formula "$formula" >/dev/null 2>&1 && brew uninstall --formula "$formula" || true
  brew untap "$tap" || true
}
trap cleanup EXIT
brew tap-new --no-git "$tap"
node "$root/scripts/homebrew/smoke-formula.mjs" "$archive" "$(brew --repository "$tap")/Formula/radar.rb"
brew trust --formula "$formula"
brew style "$formula"
brew install --formula "$formula"
brew test "$formula"
installed_prefix=$(brew --prefix "$formula")
brew uninstall --formula "$formula"
[[ ! -e $installed_prefix ]] || { echo 'Uninstall left the Radar keg behind' >&2; exit 1; }
