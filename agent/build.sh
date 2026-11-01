#!/usr/bin/env sh
# Builds the Windows agent from macOS or Linux. Output: agent/dist/homeydeck-ha-agent.exe
set -eu
cd "$(dirname "$0")"
VERSION="${1:-$(git describe --tags --always 2>/dev/null || echo dev)}"
mkdir -p dist
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -H windowsgui -X main.version=$VERSION" \
  -o dist/homeydeck-ha-agent.exe .
echo "built dist/homeydeck-ha-agent.exe ($VERSION)"
