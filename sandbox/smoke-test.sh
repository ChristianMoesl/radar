#!/usr/bin/env bash
# Run inside the image as agent, with or without SBX. No downloads required.
set -euo pipefail

test "$(id -un)" = agent
test "$(id -u)" != 0

for tool in bash sh node npm pnpm corepack fnm go gcc g++ make pkg-config python3 \
    git ssh curl gh jq rg fd file unzip zip rsync less ps \
    cat mkdir base64 sed grep find head tail stat sort xargs \
    docker dockerd containerd; do
    command -v "$tool"
done
test -s /etc/ssl/certs/ca-certificates.crt
docker compose version
docker buildx version

test "$(command -v go)" = /usr/local/go/bin/go
go version
fnm --version
node --version
npm --version
# The default package manager must already be cached for offline use.
export COREPACK_ENABLE_NETWORK=0
test "$(pnpm --version | cut -d. -f1)" = 12
test "$(node -p 'process.versions.node.split(".")[0]')" = 24

# pi-sbx starts Node directly, then invokes bash -lc or sh for operations.
# Also check non-login and interactive Bash, including fnm's writable state.
sh -c 'node --version; pnpm --version; go version'
for mode in -c -lc -ic; do
    bash "$mode" 'set -e; fnm use 24; test "$(node -p "process.versions.node.split(\".\")[0]")" = 24; pnpm --version; go version'
done
fnm alias "$(node --version)" radar-smoke
fnm use radar-smoke
fnm use default
fnm unalias radar-smoke

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$tmp"

# Pi/pi-sbx filesystem, search and image-detection prerequisites.
mkdir -p project
printf 'sandbox smoke test\n' > project/example.txt
test "$(sh -c 'cat -- "$1"' smoke project/example.txt)" = 'sandbox smoke test'
test "$(fd --glob '*.txt' project)" = project/example.txt
rg --json 'sandbox smoke test' project | jq -e 'select(.type == "match")' >/dev/null
rg --files --hidden --glob '!.git' project | grep -Fx project/example.txt
printf '%s' 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j6xoAAAAASUVORK5CYII=' | base64 -d > image.png
test "$(file --mime-type -b image.png)" = image/png

cat > main.c <<'EOF'
#include <stdio.h>
int main(void) { puts("native build works"); return 0; }
EOF
gcc -Wall -Werror main.c -o native
test "$(./native)" = 'native build works'
cat > main.cpp <<'EOF'
#include <iostream>
int main() { std::cout << "C++ build works\n"; }
EOF
g++ -Wall -Werror main.cpp -o native-cpp
test "$(./native-cpp)" = 'C++ build works'

mkdir go-test
cd go-test
cat > go.mod <<'EOF'
module example.com/sandbox-smoke

go 1.26.4
EOF
cat > main.go <<'EOF'
package main

// #include <stdlib.h>
import "C"

func main() {
	if C.abs(-42) != 42 {
		panic("cgo failed")
	}
}
EOF
GOTOOLCHAIN=local CGO_ENABLED=1 go run .
GOTOOLCHAIN=local go test ./...

# A small native Node addon proves the fnm installation retains Node headers.
cd "$tmp"
cat > addon.c <<'EOF'
#include <node_api.h>
static napi_value init(napi_env env, napi_value exports) { return exports; }
NAPI_MODULE(NODE_GYP_MODULE_NAME, init)
EOF
node_root=$(dirname "$(dirname "$(readlink -f "$(command -v node)")")")
gcc -shared -fPIC -I "$node_root/include/node" addon.c -o addon.node
node -e 'require("./addon.node"); console.log("Node addon works")'

echo 'Sandbox toolchain smoke tests passed'
