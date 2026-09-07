#!/bin/sh
# Installs the swarm desktop app (GUI) on macOS or Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/corpeningc/swarm/main/scripts/install.sh | sh
#
# Env: SWARM_VERSION=0.1.0 to pin a release.
set -eu

REPO="corpeningc/swarm"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

need() { command -v "$1" >/dev/null 2>&1 || { echo "error: $1 is required" >&2; exit 1; }; }
need curl

case "$(uname -s)" in
  Darwin) SUFFIX="macos-universal.zip" ;;
  Linux)
    [ "$(uname -m)" = "x86_64" ] || { echo "error: only x86_64 Linux builds are published; build from source" >&2; exit 1; }
    SUFFIX="linux-amd64.tar.gz"
    ;;
  *) echo "error: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac

if [ -n "${SWARM_VERSION:-}" ]; then
  API="https://api.github.com/repos/$REPO/releases/tags/v$SWARM_VERSION"
else
  API="https://api.github.com/repos/$REPO/releases/latest"
fi

# Pull the asset URL out of the release JSON without requiring jq.
URL="$(curl -fsSL "$API" | grep -o "https://[^\"]*$SUFFIX" | head -1)"
[ -n "$URL" ] || { echo "error: no $SUFFIX asset in the requested release" >&2; exit 1; }

echo "Downloading $(basename "$URL")..."
curl -fsSL "$URL" -o "$TMP/pkg"

if [ "$(uname -s)" = "Darwin" ]; then
  need unzip
  unzip -q "$TMP/pkg" -d "$TMP/app"
  APP="$(find "$TMP/app" -maxdepth 1 -name '*.app' | head -1)"
  [ -n "$APP" ] || { echo "error: no .app in the archive" >&2; exit 1; }

  DEST="/Applications"
  [ -w "$DEST" ] || DEST="$HOME/Applications"
  mkdir -p "$DEST"
  rm -rf "$DEST/$(basename "$APP")"
  cp -R "$APP" "$DEST/"

  # The build is unsigned; without this Gatekeeper refuses to open it at all.
  xattr -dr com.apple.quarantine "$DEST/$(basename "$APP")" 2>/dev/null || true

  echo "Installed to $DEST/$(basename "$APP") — open it from Launchpad or Spotlight."
else
  tar xzf "$TMP/pkg" -C "$TMP"
  BIN="$TMP/swarm-desktop"
  [ -f "$BIN" ] || { echo "error: swarm-desktop missing from the archive" >&2; exit 1; }

  mkdir -p "$HOME/.local/bin" "$HOME/.local/share/applications" "$HOME/.local/share/icons"
  install -m 755 "$BIN" "$HOME/.local/bin/swarm-desktop"
  curl -fsSL "https://raw.githubusercontent.com/$REPO/main/desktop/build/appicon.png" \
    -o "$HOME/.local/share/icons/swarm-desktop.png" 2>/dev/null || true

  # A .desktop entry is what puts it in the applications menu / launcher.
  cat > "$HOME/.local/share/applications/swarm-desktop.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=swarm
Comment=Run multiple AI coding agents in parallel
Exec=$HOME/.local/bin/swarm-desktop
Icon=swarm-desktop
Terminal=false
Categories=Development;
DESKTOP
  update-desktop-database "$HOME/.local/share/applications" >/dev/null 2>&1 || true

  echo "Installed to ~/.local/bin/swarm-desktop — it should appear in your app launcher."
  case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) echo "note: add ~/.local/bin to PATH to launch it from a shell." ;; esac
fi
