#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <output.app> <version> <amd64|arm64>" >&2
  exit 2
fi

output=$1
version=${2#v}
goarch=$3
root=$(cd "$(dirname "$0")/.." && pwd)
source_dir="$root/macos/RadarNotifier"

if [[ $(uname -s) != Darwin ]]; then
  echo "RadarNotifier.app must be built on macOS" >&2
  exit 1
fi

case "$goarch" in
  amd64) swift_arch=x86_64 ;;
  arm64) swift_arch=arm64 ;;
  *)
    echo "unsupported notifier architecture: $goarch" >&2
    exit 2
    ;;
esac

if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  version=0.0.0
fi

rm -rf "$output"
mkdir -p "$output/Contents/MacOS" "$output/Contents/Resources"
sed "s/__VERSION__/$version/g" "$source_dir/Info.plist" > "$output/Contents/Info.plist"

iconset=$(mktemp -d)/RadarIcon.iconset
mkdir -p "$iconset"
for size in 16 32 128 256 512; do
  sips -z "$size" "$size" "$source_dir/Assets/RadarIcon-1024.png" --out "$iconset/icon_${size}x${size}.png" >/dev/null
  double=$((size * 2))
  sips -z "$double" "$double" "$source_dir/Assets/RadarIcon-1024.png" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$iconset" -o "$output/Contents/Resources/RadarIcon.icns"
rm -rf "${iconset%/RadarIcon.iconset}"

xcrun --sdk macosx swiftc \
  -target "${swift_arch}-apple-macosx13.0" \
  -O \
  -whole-module-optimization \
  -framework AppKit \
  -framework UserNotifications \
  -o "$output/Contents/MacOS/radar-notifier" \
  "$source_dir/Sources/main.swift"

codesign --force --sign - "$output"
