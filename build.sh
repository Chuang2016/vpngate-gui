#!/bin/bash
# Cross-compile Win / macOS / Linux binaries (pure Go, no CGO)
set -e
cd "$(dirname "$0")"
export PATH="$HOME/go-sdk/go/bin:$PATH"
export CGO_ENABLED=0
mkdir -p dist
echo "== windows/amd64 =="
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/vpgui.exe .
echo "== darwin/arm64 (Apple Silicon) =="
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/vpgui-darwin-arm64 .
echo "== darwin/amd64 (Intel Mac) =="
GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/vpgui-darwin-amd64 .
echo "== linux/amd64 =="
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/vpgui-linux-amd64 .
echo "== linux/arm64 =="
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/vpgui-linux-arm64 .
VER=$(grep -o 'const version = "[^"]*"' main.go | cut -d'"' -f2)
for f in dist/vpgui.exe dist/vpgui-darwin-arm64 dist/vpgui-darwin-amd64 dist/vpgui-linux-amd64 dist/vpgui-linux-arm64; do
  grep -aq "v$VER" "$f" || { echo "FAIL: $f does not contain version $VER"; exit 1; }
done
echo "all binaries carry v$VER"
ls -la dist/
