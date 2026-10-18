#!/bin/bash
# Builds the Windows executable. Dependencies are vendored under third_party,
# so the build works offline.
set -euo pipefail
cd "$(dirname "$0")"
export GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod CGO_ENABLED=0

# icon, manifest and version info -> rsrc_windows_amd64.syso (needs Pillow and winresgen)
if [ "${1:-}" = "--resources" ]; then
  python3 winres/make_icon.py winres/icon.ico
  winresgen winres/icon.ico winres/app.manifest rsrc_windows_amd64.syso
fi

go vet .
go test -count=1 .
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H=windowsgui" -o build/용량탐정.exe .
ls -la build/
