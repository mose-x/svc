#!/usr/bin/env bash
# Build the Linux bare binary, .deb, and .rpm for svc.
#
# Usage:   ./scripts/build-linux.sh <version> [arch]
#   version:  release version, e.g. 1.1.0
#   arch:     amd64 | arm64  (default: native GOARCH)
#
# Produces in build/bin/:
#   svc-<ver>-linux-<x64|arm64>          (self-update)
#   svc-<ver>-linux-<x64|arm64>.deb      (first-install)
#   svc-<ver>-linux-<x64|arm64>.rpm
#
# Prereqs: Go 1.25+, Node 18+, Wails CLI, jq, fpm (ruby gem),
#   libgtk-3-dev libwebkit2gtk-4.0-dev libayatana-appindicator3-dev librsvg2-dev,
#   python3-pil. Wails v2 targets webkit2gtk-4.0 (ubuntu-22.04/jammy); 4.1 is
#   Wails v3 only. Linux ARM64 cannot be cross-compiled (webkit2gtk CGO), so run
#   this script on a native arm64 host for arm64.
#
# This script is the single source of truth for the Linux packaging steps;
# .github/workflows/build.yml calls it so CI and local builds produce identical
# artifacts.
set -euo pipefail

VERSION="${1:?version required, e.g. 1.1.0}"
# Package-metadata version: apt/dnf sort "2.0.2-rc2" ABOVE the final "2.0.2",
# which would strand rc users (no upgrade path to the stable release). The
# tilde sorts below everything (incl. the empty revision), so "2.0.2~rc2"
# upgrades cleanly to "2.0.2" in both dpkg and rpmvercmp (rpm >= 4.10).
# Stable tags contain no hyphen and pass through unchanged. Asset file names
# and about.json/wails.json keep the full version; only the deb/rpm metadata
# uses PKG_VER.
PKG_VER="$(printf '%s' "$VERSION" | sed 's/-/~/')"
ARCH="${2:-$(go env GOARCH)}"
case "$ARCH" in
  amd64) ASSET_ARCH="x64" ;;
  arm64) ASSET_ARCH="arm64" ;;
  *) echo "unsupported arch: $ARCH (want amd64|arm64)" >&2; exit 1 ;;
esac
DEB_ARCH="$ARCH"
if [ "$ARCH" = "amd64" ]; then
  RPM_ARCH="x86_64"
else
  RPM_ARCH="aarch64"
fi

# --- System dependencies (idempotent; safe to re-run). Requires sudo.
if ! pkg-config --exists webkit2gtk-4.0 2>/dev/null; then
  echo "Installing system dependencies (requires sudo)..."
  sudo apt-get update
  sudo apt-get install -y \
    libgtk-3-dev libwebkit2gtk-4.0-dev \
    libayatana-appindicator3-dev librsvg2-dev \
    ruby ruby-dev python3-pil
fi
command -v fpm >/dev/null 2>&1 || sudo gem install fpm

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

# --- Build the bare binary.
# -o sets the exact output name (wails does NOT append an arch suffix when
# -o is given), so build/bin/svc is ready to rename as-is.
wails build -platform "linux/$ARCH" -o svc
ASSET_NAME="svc-${VERSION}-linux-${ASSET_ARCH}"
mv "build/bin/svc" "build/bin/${ASSET_NAME}"

# --- Build .deb and .rpm with a .desktop entry + hicolor icons so the app
# shows up in application launchers with a proper icon.
DEB_NAME="svc-${VERSION}-linux-${ASSET_ARCH}.deb"
RPM_NAME="svc-${VERSION}-linux-${ASSET_ARCH}.rpm"
STAGING="$(mktemp -d)"
mkdir -p "$STAGING/share/applications"
cat > "$STAGING/share/applications/sdkversioncontrol.desktop" <<'DESKTOP'
[Desktop Entry]
Type=Application
Name=svc
Comment=Manage SDK versions in one place
Exec=/usr/bin/svc
Icon=sdkversioncontrol
Terminal=false
Categories=Development;
DESKTOP
SRC_ICON="build/desktop-icons/icon-white-bg.png"
for sz in 16 32 48 64 128 256 512; do
  d="$STAGING/share/icons/hicolor/${sz}x${sz}/apps"
  mkdir -p "$d"
  python3 -c "from PIL import Image; img=Image.open('$SRC_ICON'); res=getattr(getattr(Image,'Resampling',Image),'LANCZOS',1); img.resize(($sz,$sz),res).save('$d/sdkversioncontrol.png')"
done

fpm -s dir -t deb \
  -n svc -v "${PKG_VER}" -a "${DEB_ARCH}" \
  --depends libgtk-3-0 \
  --depends libwebkit2gtk-4.0-37 \
  --depends libayatana-appindicator3-1 \
  --depends librsvg2-2 \
  --description "svc" \
  -p "build/bin/${DEB_NAME}" \
  "build/bin/${ASSET_NAME}=/usr/bin/svc" \
  "$STAGING/share/=/usr/share/"

fpm -s dir -t rpm \
  -n svc -v "${PKG_VER}" -a "${RPM_ARCH}" \
  --depends gtk3 \
  --depends webkit2gtk3 \
  --depends libayatana-appindicator3 \
  --depends librsvg2 \
  --description "svc" \
  -p "build/bin/${RPM_NAME}" \
  "build/bin/${ASSET_NAME}=/usr/bin/svc" \
  "$STAGING/share/=/usr/share/"

echo
echo "Built:"
echo "  build/bin/${ASSET_NAME}"
echo "  build/bin/${DEB_NAME}"
echo "  build/bin/${RPM_NAME}"
