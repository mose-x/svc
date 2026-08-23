#!/usr/bin/env bash
# Build the Windows .exe (self-update) and NSIS installer for svc.
#
# Usage:   ./scripts/build-windows.sh <version> [arch]
#   version:  release version, e.g. 1.1.0
#   arch:     amd64 | arm64  (default: native GOARCH)
#
# Produces in build/bin/:
#   svc-<ver>-windows-<x64|arm64>.exe            (self-update)
#   svc-<ver>-windows-<x64|arm64>-installer.exe  (first-install)
#
# Prereqs: Go 1.25+, Node 18+, Wails CLI, jq, go-winres, NSIS (makensis).
# Wails Windows builds are CGO-free (WebView2 loaded at runtime via COM), so
# arm64 can be cross-compiled on an amd64 host.
#
# This script is the single source of truth for the Windows packaging steps;
# .github/workflows/build.yml calls it so CI and local builds produce identical
# artifacts. Run from Git Bash on Windows (the CI uses bash on windows-latest).
set -euo pipefail

VERSION="${1:?version required, e.g. 1.1.0}"
ARCH="${2:-$(go env GOARCH)}"
case "$ARCH" in
  amd64) ASSET_ARCH="x64" ;;
  arm64) ASSET_ARCH="arm64" ;;
  *) echo "unsupported arch: $ARCH (want amd64|arm64)" >&2; exit 1 ;;
esac

# --- Bump about.json so the binary reports the correct version to CheckUpdate.
jq --arg v "$VERSION" '.version = $v' about.json > about.json.tmp && mv about.json.tmp about.json
# H11: Also bump wails.json and winres.json so all version sources stay in sync.
# NSIS VIProductVersion/VIFileVersion (fed by wails.json productVersion via
# wails_tools.nsh) and the winres VERSIONINFO "fixed" fields only accept
# numeric X.X.X.X, so strip any prerelease/build suffix first
# ("2.0.1-rc1" -> "2.0.1"); about.json and the asset names keep the full
# version. Without this, tags like v2.0.1-rc1 abort makensis with
# "invalid VIFileVersion format".
BASE_VER="${VERSION%%[-+]*}"
WIN_VER=$(echo "$BASE_VER" | awk -F. '{printf "%s.%s.%s.0", $1, $2, $3}')
jq --arg v "$BASE_VER" '.info.productVersion = $v' wails.json > wails.json.tmp && mv wails.json.tmp wails.json
jq --arg v "$WIN_VER" --arg full "$VERSION" '.RT_VERSION["#1"]["0000"].fixed.file_version = $v | .RT_VERSION["#1"]["0000"].fixed.product_version = $v | .RT_VERSION["#1"]["0000"].info["0409"].FileVersion = $full | .RT_VERSION["#1"]["0000"].info["0409"].ProductVersion = $full' winres/winres.json > winres/winres.json.tmp && mv winres/winres.json.tmp winres/winres.json
echo "Version bumped to $VERSION in about.json, wails.json, winres.json"

# R9: Restore exactly the files this script modifies so local builds don't leave
# tracked files dirty: about.json/wails.json/winres.json (version bumps) and
# internal/shimmanager/svc-shim.windows.exe (committed placeholder overwritten
# by the real shim build below; never committed). Do NOT restore any other
# path, so unrelated uncommitted changes are never discarded.
# Guard with [ -n "$VERSION" ] so the checkout only runs when the script got
# past the version-bump step (early exit before VERSION is set would otherwise
# try to restore files that were never modified).
trap '[ -n "$VERSION" ] && git checkout about.json wails.json winres/winres.json internal/shimmanager/svc-shim.windows.exe 2>/dev/null' EXIT

# --- Generate Windows resources (icon + manifest) -> resource.syso.
# go-winres --out resource produces a file named `resource` (no extension) with
# --no-suffix; rename it to resource.syso so the Go linker embeds it.
# R3: Remove stale .syso first so the linker always embeds fresh resources.
rm -f resource.syso
go-winres make --arch "$ARCH" --out resource --no-suffix
# R2: Use mv -f to always replace, no conditional guard that skips on stale presence.
mv -f resource resource.syso

# --- makensis (NSIS) must be on PATH for `wails build -nsis` to produce the
# installer. On CI, the build.yml "Setup NSIS" step adds it to GITHUB_PATH
# (Test-Path + choco fallback). Locally, install NSIS and ensure makensis is on
# PATH. If makensis is absent, wails skips the installer and the guard below
# ships the bare .exe (self-update asset) without failing the build.
if command -v makensis >/dev/null 2>&1; then
  makensis -VERSION
else
  echo "warning: makensis not on PATH; installer will be skipped" >&2
fi

# --- Build the console-subsystem svc-shim binary that the app //go:embeds.
# This MUST run before `wails build` so the bytes are captured at compile time.
# The committed file is an empty placeholder (so dev `wails build` works without
# this step); this overwrites it with the real binary and is never committed.
GOOS=windows GOARCH="$ARCH" go build -o internal/shimmanager/svc-shim.windows.exe ./cmd/svc-shim
echo "Built console shim binary:"
ls -la internal/shimmanager/svc-shim.windows.exe

# --- Build the app + NSIS installer.
# -nsis produces the installer alongside the bare .exe; the bare .exe stays the
# self-update asset (ApplyUpdate swaps just the executable).
wails build -nsis -nopackage -platform "windows/$ARCH" -o svc.exe

# -o sets the exact output name (wails does NOT append an arch suffix when
# -o is given), so build/bin/svc.exe is ready to rename as-is;
# each CI job starts from a fresh checkout, so no stale-file risk.
ASSET_NAME="svc-${VERSION}-windows-${ASSET_ARCH}.exe"
mv "build/bin/svc.exe" "build/bin/${ASSET_NAME}"

# NSIS writes *-installer.exe next to the binary; the literal name varies with
# INFO_PRODUCTNAME (wails.json "name" has spaces), so glob to a stable name.
# Guard: if makensis was missing, wails produced no installer -- ship the bare
# .exe (self-update asset) without failing the build.
INSTALLER_NAME="svc-${VERSION}-windows-${ASSET_ARCH}-installer.exe"
shopt -s nullglob
installers=(build/bin/*-installer.exe)
shopt -u nullglob
if [ ${#installers[@]} -gt 0 ]; then
  mv "${installers[0]}" "build/bin/${INSTALLER_NAME}"
else
  echo "warning: no NSIS installer produced (makensis missing); shipping bare .exe only" >&2
fi

echo
echo "Built:"
echo "  build/bin/${ASSET_NAME}"
echo "  build/bin/${INSTALLER_NAME}"
