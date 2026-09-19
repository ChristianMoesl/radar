#!/usr/bin/env bash
# Run on a disposable Linux CI runner with Docker, SBX and /dev/kvm available.
# SBX must already be signed in. Never mount the host Docker socket.
set -euo pipefail

if [ "$#" -ne 1 ]; then
    echo "usage: $0 <locally-built-image>" >&2
    exit 2
fi
image=$1
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
name="radar-image-test-$$"
cleanup() {
    sbx rm --force "$name" || true
    rm -rf "$tmp"
}
trap cleanup EXIT

# SBX has its own image store, separate from the host Docker daemon.
docker save "$image" -o "$tmp/image.tar"
sbx template load "$tmp/image.tar"
mkdir "$tmp/kit"
sed "s|^  image: .*|  image: $image|" "$root/sandbox/kit/spec.yaml" > "$tmp/kit/spec.yaml"
sbx kit validate "$tmp/kit"
sbx create --name "$name" "$tmp/kit"
sbx exec -i "$name" bash -s < "$root/sandbox/smoke-test.sh"

# Check automatic daemon startup and container execution, not just CLI presence.
sbx exec "$name" bash -lc '
    set -e
    for attempt in $(seq 1 30); do
        if docker info >/dev/null 2>&1; then break; fi
        sleep 1
    done
    docker info
    docker run --rm hello-world
'
