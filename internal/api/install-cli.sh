#!/bin/sh
# aven CLI installer (Linux, macOS). Served by aven-server at GET /install.sh.
#
#   curl -fsSL https://aven.example.com/install.sh | sh -s -- --server https://aven.example.com
#
# Options:
#   --server URL   aven-server to download from (https:// only; or env KEEL_URL)
#   --dir DIR      install directory (default: /usr/local/bin as root, otherwise ~/.local/bin; or env KEEL_INSTALL_DIR)
#
# The binary is downloaded over HTTPS and checked against GET /downloads/aven-<os>-<arch>.sha256 before it is installed.
# No root is needed unless --dir points to a directory you cannot write.
set -eu

SERVER="${KEEL_URL:-}"
DIR="${KEEL_INSTALL_DIR:-}"

say() { printf 'aven: %s\n' "$*"; }
die() { printf 'aven: error: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
	case "$1" in
		--server) [ $# -ge 2 ] || die "--server needs a value"; SERVER=$2; shift 2 ;;
		--server=*) SERVER=${1#*=}; shift ;;
		--dir) [ $# -ge 2 ] || die "--dir needs a value"; DIR=$2; shift 2 ;;
		--dir=*) DIR=${1#*=}; shift ;;
		-h|--help) echo "usage: install.sh --server https://... [--dir DIR]"; exit 0 ;;
		*) die "unknown option: $1" ;;
	esac
done

case "$SERVER" in
	https://?*) ;;
	"") die "--server is required" ;;
	*) die "--server must be an https:// URL" ;;
esac
SERVER=${SERVER%/}
case "$SERVER" in *[!]A-Za-z0-9.:/_[-]*) die "--server contains unexpected characters" ;; esac

case "$(uname -s)" in
	Linux) OS=linux ;;
	Darwin) OS=darwin ;;
	*) die "unsupported OS: $(uname -s) (on Windows download aven-windows-amd64.exe from $SERVER/downloads/)" ;;
esac
case "$(uname -m)" in
	x86_64|amd64) ARCH=amd64 ;;
	aarch64|arm64) ARCH=arm64 ;;
	*) die "unsupported architecture: $(uname -m) (amd64 and arm64 only)" ;;
esac

if [ -z "$DIR" ]; then
	if [ "$(id -u)" -eq 0 ]; then DIR=/usr/local/bin; else DIR="$HOME/.local/bin"; fi
fi

command -v curl >/dev/null 2>&1 || die "curl is required"
if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	die "sha256sum or shasum is required"
fi

NAME="aven-$OS-$ARCH"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM

say "downloading $NAME from $SERVER"
curl -fsSL --proto =https --tlsv1.2 -o "$TMP/aven" "$SERVER/downloads/$NAME" || die "download failed: $SERVER/downloads/$NAME"
curl -fsSL --proto =https --tlsv1.2 -o "$TMP/aven.sha256" "$SERVER/downloads/$NAME.sha256" || die "checksum download failed"
WANT=$(cut -d' ' -f1 "$TMP/aven.sha256")
GOT=$(sha256 "$TMP/aven")
[ -n "$WANT" ] && [ "$WANT" = "$GOT" ] || die "checksum mismatch (want $WANT, got $GOT)"

mkdir -p "$DIR" || die "cannot create $DIR"
chmod 0755 "$TMP/aven"
mv -f "$TMP/aven" "$DIR/aven" 2>/dev/null || die "cannot write $DIR/aven (try --dir or sudo)"
say "installed $DIR/aven ($(KEEL_URL= "$DIR/aven" version 2>/dev/null | head -n 1))"

case ":$PATH:" in
	*":$DIR:"*) ;;
	*) say "$DIR is not on your PATH; add: export PATH=\"$DIR:\$PATH\"" ;;
esac
say "next: aven login $SERVER"
