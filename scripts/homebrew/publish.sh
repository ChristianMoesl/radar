#!/usr/bin/env bash
set -euo pipefail
# Used only after all native jobs and signed candidate reauthentication pass.
# Keep the deploy key in runner-local temporary files, never in the checkout.
[[ $# == 2 ]] || { echo 'usage: publish.sh <vX.Y.Z> <authenticated-formula>' >&2; exit 2; }
: "${HOMEBREW_TAP_SSH_KEY:?HOMEBREW_TAP_SSH_KEY must contain the private deploy key}"
root=$(cd "$(dirname "$0")/../.." && pwd)
umask 077
key_dir=$(mktemp -d "${RUNNER_TEMP:?RUNNER_TEMP must be set}/radar-homebrew-ssh.XXXXXX")
trap 'rm -rf -- "$key_dir"' EXIT
printf '%s\n' "$HOMEBREW_TAP_SSH_KEY" > "$key_dir/id_ed25519"
unset HOMEBREW_TAP_SSH_KEY
# Fail on malformed or passphrase-protected keys without an interactive prompt.
ssh-keygen -y -P '' -f "$key_dir/id_ed25519" > /dev/null
cp "$root/scripts/homebrew/github_known_hosts" "$key_dir/known_hosts"
# Do not use ambient SSH identities/configuration or learn a host key on first use.
export GIT_SSH_COMMAND="ssh -F /dev/null -i \"$key_dir/id_ed25519\" -o IdentitiesOnly=yes -o IdentityAgent=none -o BatchMode=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=\"$key_dir/known_hosts\" -o GlobalKnownHostsFile=/dev/null -o HostKeyAlgorithms=ssh-ed25519 -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no"
export GIT_SSH_VARIANT=ssh
export GIT_TERMINAL_PROMPT=0
node "$root/scripts/homebrew/automation.mjs" publish "$1" "$2"
