#!/usr/bin/env bash
# Install edgephone in Termux.
#
#   curl -fsSL https://github.com/jam-bm/edgephone/releases/latest/download/install.sh | bash
#
# Environment:
#   EDGEPHONE_VERSION   release tag to install (default: latest)
#   EDGEPHONE_REPO      GitHub repo (default: jam-bm/edgephone)
#   EDGEPHONE_NO_BOOT   set to 1 to skip installing the Termux:Boot script
set -euo pipefail

REPO="${EDGEPHONE_REPO:-jam-bm/edgephone}"
VERSION="${EDGEPHONE_VERSION:-latest}"
BIN_DIR="${PREFIX:-/usr/local}/bin"

say() { printf '\033[1m==>\033[0m %s\n' "$*"; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

case "$(uname -m)" in
  aarch64 | arm64) arch=arm64 ;;
  x86_64 | amd64) arch=amd64 ;;
  *) die "unsupported CPU $(uname -m); edgephone ships arm64 and amd64 builds" ;;
esac

if [ -n "${TERMUX_VERSION:-}" ] || [[ "${PREFIX:-}" == *com.termux* ]]; then
  os=android
else
  os=linux
fi
[ "$os" = android ] && [ "$arch" != arm64 ] && die "Android builds are arm64 only"

if [ "$VERSION" = latest ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi
archive="edgephone_${os}_${arch}.tar.gz"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

say "Downloading $archive ($VERSION)"
curl -fsSL -o "$tmp/$archive" "$base/$archive" || die "download failed: $base/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || die "could not download checksums.txt"

say "Verifying SHA-256"
(cd "$tmp" && grep " $archive\$" checksums.txt | sha256sum -c --quiet -) || die "checksum mismatch"

tar -xzf "$tmp/$archive" -C "$tmp"
install -m 0755 "$tmp/edgephone" "$BIN_DIR/edgephone"
say "Installed $("$BIN_DIR/edgephone" version) to $BIN_DIR"

config="${XDG_CONFIG_HOME:-$HOME/.config}/edgephone/config.yaml"
if [ ! -f "$config" ]; then
  "$BIN_DIR/edgephone" init -config "$config" >/dev/null
  say "Wrote example config to $config"
fi

if [ "$os" = android ] && [ "${EDGEPHONE_NO_BOOT:-0}" != 1 ]; then
  mkdir -p "$HOME/.termux/boot"
  install -m 0755 "$tmp/start-edgephone" "$HOME/.termux/boot/start-edgephone"
  say "Installed autostart script to ~/.termux/boot/start-edgephone"
  if ! command -v termux-battery-status >/dev/null 2>&1; then
    say "Installing termux-api (battery and Wi-Fi metrics)"
    pkg install -y termux-api || true
  fi
fi

echo
say "Next steps"
echo "  1. edgephone doctor          # fix anything it reports"
echo "  2. edit $config"
echo "  3. edgephone agent           # or reboot to start via Termux:Boot"
