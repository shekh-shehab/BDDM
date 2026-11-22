#!/bin/sh
# Official Windows build for BDDM. Always uses the GUI subsystem
# (-H=windowsgui) so Windows 11's terminal does not open a console tab
# when the installer, updater, a shortcut, or autostart launches BDDM.
set -e
cd "$(dirname "$0")"
if command -v go-winres >/dev/null 2>&1; then
  go-winres make --in winres/winres.json --arch amd64,386,arm64 --out rsrc
elif [ -x "$HOME/go/bin/go-winres" ]; then
  "$HOME/go/bin/go-winres" make --in winres/winres.json --arch amd64,386,arm64 --out rsrc
fi
mkdir -p build
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-H=windowsgui -s -w" -o build/BDDM.exe .
