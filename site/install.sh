#!/bin/sh
set -eu

BASE_URL="${PORTLIGHT_BASE_URL:-https://portlight.616.pub}"
INSTALL_DIR="${PORTLIGHT_INSTALL_DIR:-/usr/local/bin}"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "portlight install: missing required command: $1" >&2
    exit 1
  }
}

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  darwin) os="darwin" ;;
  linux) os="linux" ;;
  *) echo "portlight install: unsupported OS: $os" >&2; exit 1 ;;
esac

machine="$(uname -m | tr '[:upper:]' '[:lower:]')"
case "$machine" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "portlight install: unsupported architecture: $machine" >&2; exit 1 ;;
esac

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
url="$BASE_URL/downloads/portlight-$os-$arch"
latest_url="$BASE_URL/releases/latest.json"

if command -v curl >/dev/null 2>&1; then
  metadata="$(curl -fsSL "$latest_url")"
  curl -fsSL "$url" -o "$tmp"
elif command -v wget >/dev/null 2>&1; then
  metadata="$(wget -q "$latest_url" -O -)"
  wget -q "$url" -O "$tmp"
else
  echo "portlight install: curl or wget required" >&2
  exit 1
fi

want_sha="$(printf '%s\n' "$metadata" | awk -v want_os="$os" -v want_arch="$arch" '
  /"os"[[:space:]]*:/ {
    current_os=$0
    sub(/^.*"os"[[:space:]]*:[[:space:]]*"/, "", current_os)
    sub(/".*$/, "", current_os)
  }
  /"arch"[[:space:]]*:/ {
    current_arch=$0
    sub(/^.*"arch"[[:space:]]*:[[:space:]]*"/, "", current_arch)
    sub(/".*$/, "", current_arch)
  }
  /"sha256"[[:space:]]*:/ {
    sha=$0
    sub(/^.*"sha256"[[:space:]]*:[[:space:]]*"/, "", sha)
    sub(/".*$/, "", sha)
    if (current_os == want_os && current_arch == want_arch) {
      print sha
      exit
    }
  }
')"
if [ -z "$want_sha" ]; then
  echo "portlight install: release metadata missing checksum for $os/$arch" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  got_sha="$(sha256sum "$tmp" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  got_sha="$(shasum -a 256 "$tmp" | awk '{print $1}')"
else
  echo "portlight install: sha256sum or shasum required" >&2
  exit 1
fi

if [ "$(printf '%s' "$got_sha" | tr '[:upper:]' '[:lower:]')" != "$(printf '%s' "$want_sha" | tr '[:upper:]' '[:lower:]')" ]; then
  echo "portlight install: checksum mismatch" >&2
  exit 1
fi

chmod +x "$tmp"

if [ -d "$INSTALL_DIR" ] && [ -w "$INSTALL_DIR" ]; then
  mv "$tmp" "$INSTALL_DIR/portlight"
elif command -v sudo >/dev/null 2>&1 && [ -d "$INSTALL_DIR" ]; then
  sudo install -m 755 "$tmp" "$INSTALL_DIR/portlight"
else
  INSTALL_DIR="$HOME/.local/bin"
  mkdir -p "$INSTALL_DIR"
  mv "$tmp" "$INSTALL_DIR/portlight"
  case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *) echo "portlight installed to $INSTALL_DIR. Add it to PATH if your shell cannot find it." ;;
  esac
fi

"$INSTALL_DIR/portlight" --version
