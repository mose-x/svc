#!/usr/bin/env bash
# Build the macOS .app, DMG, and self-update .bin for svc.
#
# Usage:   ./scripts/build-macos.sh <version> [arch]
#   version:  release version, e.g. 1.1.0
#   arch:     amd64 | arm64  (default: native GOARCH)
#
# Produces in build/bin/:
#   svc-<ver>-macos-<x64|arm64>.dmg   (first-install)
#   svc-<ver>-macos-<x64|arm64>.bin   (in-app self-update)
#
# Prereqs: Go 1.25+, Node 18+, Wails CLI, jq, create-dmg (brew), Pillow (pip3).
# macOS-only: hdiutil/create-dmg/iconutil/sips are macOS tools.
#
# This script is the single source of truth for the macOS packaging steps;
# .github/workflows/build.yml calls it so CI and local builds produce
# byte-identical artifacts.
set -euo pipefail

VERSION="${1:?version required, e.g. 1.1.0}"
ARCH="${2:-$(go env GOARCH)}"
case "$ARCH" in
  amd64) ASSET_ARCH="x64" ;;
  arm64) ASSET_ARCH="arm64" ;;
  *) echo "unsupported arch: $ARCH (want amd64|arm64)" >&2; exit 1 ;;
esac

APP="build/bin/svc.app"

# --- Bump about.json so the binary reports the correct version to CheckUpdate.
jq --arg v "$VERSION" '.version = $v' about.json > about.json.tmp && mv about.json.tmp about.json
jq --arg v "$VERSION" '.info.productVersion = $v' wails.json > wails.json.tmp && mv wails.json.tmp wails.json
echo "Version bumped to $VERSION in about.json, wails.json"

# R9: Restore exactly the files this script bumped (about.json, wails.json only
# -- winres/winres.json is Windows-specific and never touched here, so
# restoring it would silently discard unrelated uncommitted changes).
# Guard with [ -n "$VERSION" ] so the checkout only runs when the script got
# past the version-bump step (early exit before VERSION is set would otherwise
# try to restore files that were never modified).
trap '[ -n "$VERSION" ] && git checkout about.json wails.json 2>/dev/null' EXIT

# --- Build .app
# SVC_SKIP_BINDINGS=1 (set by scripts/build-macos-local.sh) passes
# -skipbindings so Wails does not run the ad-hoc-signed binding-generator
# binary (amfid kills it under MDM). CI leaves it unset -> bindings are
# regenerated each build (catches drift). When skipping, frontend/wailsjs/
# must be current (commit refreshed bindings before changing Go App methods).
WAILS_FLAGS=""
[ "${SVC_SKIP_BINDINGS:-0}" = "1" ] && WAILS_FLAGS="-skipbindings"
wails build $WAILS_FLAGS -platform "darwin/$ARCH" -o svc

# Strip the ad-hoc signature Wails/Go applies to the .app. An ad-hoc-signed
# .app under Gatekeeper (browser quarantine) shows "damaged" with no
# right-click -> Open bypass. Strip BOTH the bundle signature AND the inner
# executable's ad-hoc: removing only the bundle seal while leaving the inner
# binary ad-hoc-signed leaves a signature mismatch that Gatekeeper still
# rejects (confirmed on a strict MDM Mac: bundle-only strip still blocked;
# stripping both let the app launch). The arm64 kernel re-applies an ad-hoc
# signature to the inner binary at exec time, so stripping it is safe (the
# binary still runs). See note/t.md.
codesign --remove-signature "$APP" 2>/dev/null || true
codesign --remove-signature "$APP/Contents/MacOS/svc" 2>/dev/null || true

# --- Apply rounded desktop icon (Dock/Launchpad readability).
ICONSET="$(mktemp -d)/icon.iconset"
mkdir -p "$ICONSET"
SRC="build/desktop-icons/icon-white-bg.png"
for sz in 16 32 64 128 256 512; do
  sips -z "$sz" "$sz" "$SRC" --out "$ICONSET/icon_${sz}x${sz}.png" >/dev/null
  sips -z "$((sz*2))" "$((sz*2))" "$SRC" --out "$ICONSET/icon_${sz}x${sz}@2x.png" >/dev/null
done
sips -z 1024 1024 "$SRC" --out "$ICONSET/icon_512x512@2x.png" >/dev/null
ICNS="$(dirname "$ICONSET")/iconfile.icns"
iconutil -c icns "$ICONSET" -o "$ICNS"
cp "$ICNS" "$APP/Contents/Resources/iconfile.icns"

# NOTE: No ad-hoc codesign here. Under strict MDM, ad-hoc (--sign -) is
# rejected by amfid with -423 and is strictly worse than leaving the bundle
# unsigned (see note/t.md). A browser-downloaded DMG carries
# com.apple.quarantine, which the .app inherits on drag-install, routing the
# launch through Gatekeeper (right-click -> Open) instead of the provenance
# path that kills unsigned/ad-hoc binaries with SIGKILL 137.

# --- Create DMG with a pure-white background + English red security hint.
command -v create-dmg >/dev/null 2>&1 || brew install create-dmg

# Create a throwaway venv for Pillow (DMG background image generation).
VENV="$(mktemp -d)"
python3 -m venv "$VENV"
"$VENV/bin/pip" install --quiet Pillow

BG_PATH="build/bin/dmg_bg.png"
export BG_PATH VERSION
"$VENV/bin/python" << 'PYEOF'
import os
from PIL import Image, ImageDraw, ImageFont

w, h = 660, 400
img = Image.new("RGBA", (w, h), (255, 255, 255, 255))
draw = ImageDraw.Draw(img)

def load_font(size):
    for path in [
        "/System/Library/Fonts/Helvetica.ttc",
        "/System/Library/Fonts/Supplemental/Arial.ttf",
    ]:
        try:
            return ImageFont.truetype(path, size)
        except Exception:
            pass
    return ImageFont.load_default()

title_font = load_font(24)
hint_font = load_font(14)
small_font = load_font(12)
cx = w / 2

draw.text((cx, 40), "svc", fill=(30, 30, 46, 255),
          font=title_font, anchor="mm")
draw.text((cx, 130),
          "Drag the app icon to the Applications folder on the right",
          fill=(90, 90, 90, 255), font=hint_font, anchor="mm")

# Security hint in RED: the correct unsigned-app bypass. NOT `xattr -cr`,
# which strips quarantine and routes MDM users back to the provenance path
# (SIGKILL). English per spec.
RED = (200, 0, 0, 255)
draw.text((cx, 290), 'If macOS says "cannot be opened":',
          fill=RED, font=small_font, anchor="mm")
draw.text((cx, 320), 'Right-click the app  ->  Open  ->  "Open"',
          fill=RED, font=hint_font, anchor="mm")

img.save(os.environ["BG_PATH"])
PYEOF

DMG_NAME="svc-${VERSION}-macos-${ASSET_ARCH}.dmg"
create-dmg \
  --volname "svc" \
  --background "$BG_PATH" \
  --window-pos 200 120 \
  --window-size 660 400 \
  --icon-size 100 \
  --app-drop-link 480 200 \
  --icon "svc.app" 180 200 \
  --no-internet-enable \
  "build/bin/${DMG_NAME}" \
  "$APP"

# Stamp the DMG with com.apple.quarantine so a drag-installed .app inherits
# it and launches via Gatekeeper (right-click -> Open), not the provenance
# path that SIGKILLs unsigned binaries under MDM. Browser-downloaded DMGs get
# quarantine automatically; this covers locally-built / non-browser DMGs
# (the xattr is lost when CI uploads the artifact, but release downloaders get
# it from the browser anyway -- see note/t.md).
xattr -w com.apple.quarantine "0083;00000000;SVC;|com.mose-x.sdkversioncontrol" \
  "build/bin/${DMG_NAME}" 2>/dev/null || true

# --- Extract bare inner binary for in-app self-update.
# ApplyUpdate swaps the executable inside the existing .app bundle, not the
# bundle itself, so ship the inner binary as a separate .bin asset.
BIN_NAME="svc-${VERSION}-macos-${ASSET_ARCH}.bin"
cp "$APP/Contents/MacOS/svc" "build/bin/${BIN_NAME}"

echo
echo "Built:"
echo "  build/bin/${DMG_NAME}"
echo "  build/bin/${BIN_NAME}"
