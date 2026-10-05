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
mkdir "$tmp/workspace" "$tmp/startup.d"
cat > "$tmp/startup.d/10-fixture" <<'EOF'
#!/bin/sh
set -eu
# Runtime state, not baked into the image or exposed through the host mount.
sleep 1
printf '10\n' >> "$HOME/startup-fixture.log"
EOF
cat > "$tmp/startup.d/20-fixture" <<'EOF'
#!/bin/sh
set -eu
printf '20\n' >> "$HOME/startup-fixture.log"
EOF
chmod 0755 "$tmp/startup.d/10-fixture" "$tmp/startup.d/20-fixture"
printf 'SBX_STARTUP_DIR=%s\n' "$tmp/startup.d" > "$tmp/sandbox.env"
sbx create --name "$name" --env-file "$tmp/sandbox.env" "$tmp/kit" "$tmp/workspace" "$tmp/startup.d:ro"
# Native startup is asynchronous; direct exec waits explicitly before using it.
sbx exec "$name" sandbox-startup wait --timeout 30
sbx exec "$name" sh -c 'test "$(cat "$HOME/startup-fixture.log")" = "$(printf "10\n20")"; test "$(id -u)" = 1000'
sbx exec "$name" node -e 'const fs=require("fs"); if(fs.readFileSync(process.env.HOME+"/startup-fixture.log","utf8")!=="10\n20\n") process.exit(1)'
# Check startup reruns after stop/start and stale readiness cannot short-circuit.
sbx stop "$name"
sbx exec "$name" sandbox-startup wait --timeout 30
sbx exec "$name" sh -c 'test "$(cat "$HOME/startup-fixture.log")" = "$(printf "10\n20\n10\n20")"'
# Keep generic smoke checks independent of the configured startup directory.
sbx exec -e SBX_STARTUP_DIR= -i "$name" bash -s < "$root/sandbox/smoke-test.sh"

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

# Failed initialization must not inherit the preceding boot's ready state.
cat > "$tmp/startup.d/15-failure" <<'EOF'
#!/bin/sh
printf 'PRIVATE_FIXTURE_VALUE\n'
exit 17
EOF
chmod 0755 "$tmp/startup.d/15-failure"
sbx stop "$name"
if sbx exec "$name" sandbox-startup wait --timeout 30; then
    echo 'failed startup incorrectly reported ready' >&2
    exit 1
fi
sbx exec "$name" python3 -c 'import json, pathlib; p = pathlib.Path.home() / ".cache/sandbox-startup/status.json"; s = json.loads(p.read_text()); assert s["status"] == "failed" and s["script"] == "15-failure" and s["exit_code"] == 17'
sbx exec "$name" sh -c 'test "$(cat "$HOME/startup-fixture.log")" = "$(printf "10\n20\n10\n20\n10")"'
