#!/bin/sh
# Install ssh-client from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/pyjhoop/ssh-client/main/install.sh | sh
#
# Environment:
#   VERSION      tag to install (default: the latest release)
#   INSTALL_DIR  where to put the binary (default: ~/.local/bin)
#
# This script never calls sudo. A script you piped into a shell asking for root
# is asking for a privilege you cannot inspect first; install somewhere you own,
# or run it yourself with INSTALL_DIR=/usr/local/bin and your own sudo.
set -eu

REPO="pyjhoop/ssh-client"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

# ── platform ───────────────────────────────────────────────────────────────
os=$(uname -s)
case "$os" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) die "unsupported system '$os'. Build it yourself with: go install github.com/$REPO@latest" ;;
esac

arch=$(uname -m)
case "$arch" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) die "unsupported architecture '$arch'. Build it yourself with: go install github.com/$REPO@latest" ;;
esac

# ── downloader ─────────────────────────────────────────────────────────────
if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL "$1" -o "$2"; }
    fetch_stdout() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -qO "$2" "$1"; }
    fetch_stdout() { wget -qO- "$1"; }
else
    die "neither curl nor wget is available"
fi

# ── version ────────────────────────────────────────────────────────────────
# Parsed with grep/sed rather than jq: requiring a JSON parser to install a
# terminal program is a dependency too many.
if [ -z "${VERSION:-}" ]; then
    VERSION=$(fetch_stdout "https://api.github.com/repos/$REPO/releases/latest" |
        grep -m1 '"tag_name"' | sed -e 's/.*"tag_name"[[:space:]]*:[[:space:]]*"//' -e 's/".*//')
fi
[ -n "$VERSION" ] || die "could not determine the latest version; set VERSION=vX.Y.Z"

# goreleaser's archive names carry the version without its leading v.
num=${VERSION#v}
archive="ssh-client_${num}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$VERSION"

# ── checksum tool, before downloading anything ─────────────────────────────
# Piping a script from the network and then running an unverified binary is
# worse than not installing at all, so this is checked up front and there is
# no flag to skip it.
if command -v sha256sum >/dev/null 2>&1; then
    verify() { sha256sum -c -; }
elif command -v shasum >/dev/null 2>&1; then
    verify() { shasum -a 256 -c -; }
else
    die "no sha256sum or shasum found — refusing to install without verifying the download"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading ssh-client $VERSION ($os/$arch)"
fetch "$base/$archive" "$tmp/$archive" || die "download failed: $base/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "download failed: $base/checksums.txt"

say "verifying checksum"
line=$(grep " $archive\$" "$tmp/checksums.txt" || true)
[ -n "$line" ] || die "$archive is not listed in checksums.txt"
(cd "$tmp" && printf '%s\n' "$line" | verify >/dev/null) || die "checksum mismatch for $archive — not installing"

# ── install ────────────────────────────────────────────────────────────────
tar -xzf "$tmp/$archive" -C "$tmp"
[ -f "$tmp/ssh-client" ] || die "no ssh-client binary inside $archive"

mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp/ssh-client" "$INSTALL_DIR/ssh-client" 2>/dev/null ||
    { cp "$tmp/ssh-client" "$INSTALL_DIR/ssh-client" && chmod 0755 "$INSTALL_DIR/ssh-client"; } ||
    die "could not write to $INSTALL_DIR (set INSTALL_DIR to somewhere you can write)"

say "installed $INSTALL_DIR/ssh-client"

case ":$PATH:" in
    *":$INSTALL_DIR:"*) "$INSTALL_DIR/ssh-client" --version ;;
    *)
        say ""
        say "$INSTALL_DIR is not on your PATH. Add it with:"
        say ""
        say "    echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.profile"
        say ""
        say "then open a new shell, or run it directly: $INSTALL_DIR/ssh-client"
        ;;
esac
