#!/bin/sh
# Aven server installer and upgrader.
#
#   curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh | sudo sh -s -- --public-url https://aven.example.com
#
# First run: installs aven-server, the CLI, agent/CLI downloads and Railpack, writes /etc/keel/aven.env
# with a generated admin password, installs the systemd unit and starts it.
# Later runs: upgrade in place. The env file is kept, the database is copied to $DATA/backups first,
# and the previous binary is kept as /usr/local/bin/aven-server.prev. Running containers keep running.
#
# Options:
#   --public-url URL       dashboard URL, https:// (first install; prompted on a terminal)
#   --base-domain DOMAIN   wildcard domain for service hostnames (default: parent of the public URL host)
#   --acme-email EMAIL     contact email for Let's Encrypt (optional)
#   --admin-password PW    password for admin@local (default: generated; or env KEEL_ADMIN_PASSWORD)
#   --version TAG          release to install, e.g. v0.1.0 (default: latest)
#   --from-dir DIR         install from a local checkout built with 'make all' instead of a release
#   --railpack-version V   Railpack release to install if railpack is missing (default: v0.42.0)
#   --no-railpack          do not install Railpack
#   --install-docker       install Docker with get.docker.com if it is missing
#   --no-start             install only; do not enable or start aven-server
#   -y, --yes              never prompt
#
# Environment: GITHUB_TOKEN, when set, is sent with GitHub requests (API rate limits). AVEN_REPO overrides the
# owner/repo to download releases from (forks).
# Runtime names keep the "keel" prefix for compatibility: KEEL_* settings, /etc/keel, /var/lib/keel.
set -eu

REPO=${AVEN_REPO:-turin-dev/aven}
CONF_DIR=/etc/keel
ENV_FILE=$CONF_DIR/aven.env
BIN_DIR=/usr/local/bin
LIB_DIR=/usr/local/lib/keel
UNIT=/etc/systemd/system/aven-server.service

PUBLIC_URL=""
BASE_DOMAIN=""
ACME_EMAIL=""
ADMIN_PASSWORD="${KEEL_ADMIN_PASSWORD:-}"
VERSION=latest
FROM_DIR=""
RAILPACK_VERSION=v0.42.0
RAILPACK=1
INSTALL_DOCKER=0
START=1
YES=0

say() { printf 'aven: %s\n' "$*"; }
warn() { printf 'aven: warning: %s\n' "$*" >&2; }
die() { printf 'aven: error: %s\n' "$*" >&2; exit 1; }
need_arg() { [ "$2" -ge 2 ] || die "$1 needs a value"; }

while [ $# -gt 0 ]; do
	case "$1" in
		--public-url) need_arg "$1" $#; PUBLIC_URL=$2; shift 2 ;;
		--public-url=*) PUBLIC_URL=${1#*=}; shift ;;
		--base-domain) need_arg "$1" $#; BASE_DOMAIN=$2; shift 2 ;;
		--base-domain=*) BASE_DOMAIN=${1#*=}; shift ;;
		--acme-email) need_arg "$1" $#; ACME_EMAIL=$2; shift 2 ;;
		--acme-email=*) ACME_EMAIL=${1#*=}; shift ;;
		--admin-password) need_arg "$1" $#; ADMIN_PASSWORD=$2; shift 2 ;;
		--admin-password=*) ADMIN_PASSWORD=${1#*=}; shift ;;
		--version) need_arg "$1" $#; VERSION=$2; shift 2 ;;
		--version=*) VERSION=${1#*=}; shift ;;
		--from-dir) need_arg "$1" $#; FROM_DIR=$2; shift 2 ;;
		--from-dir=*) FROM_DIR=${1#*=}; shift ;;
		--railpack-version) need_arg "$1" $#; RAILPACK_VERSION=$2; shift 2 ;;
		--railpack-version=*) RAILPACK_VERSION=${1#*=}; shift ;;
		--no-railpack) RAILPACK=0; shift ;;
		--install-docker) INSTALL_DOCKER=1; shift ;;
		--no-start) START=0; shift ;;
		-y|--yes) YES=1; shift ;;
		-h|--help)
			if [ -f "$0" ] && head -n1 "$0" | grep -q '^#!'; then sed -n '2,/^set -eu/p' "$0" | sed -e '$d' -e 's/^# \{0,1\}//'
			else echo "usage: install-server.sh --public-url https://... [options]; see https://github.com/$REPO/blob/main/install-server.sh"; fi
			exit 0 ;;
		*) die "unknown option: $1 (see --help)" ;;
	esac
done

# ---- checks ----

[ "$(id -u)" -eq 0 ] || die "run as root (pipe into 'sudo sh -s -- ...')"
[ "$(uname -s)" = Linux ] || die "only Linux is supported"
case "$(uname -m)" in
	x86_64|amd64) ARCH=amd64 ;;
	aarch64|arm64) ARCH=arm64 ;;
	*) die "unsupported architecture: $(uname -m) (amd64 and arm64 only)" ;;
esac
command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ] || die "systemd is required"
for c in curl tar; do command -v $c >/dev/null 2>&1 || die "$c is required"; done
if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v openssl >/dev/null 2>&1; then
	sha256() { openssl dgst -sha256 -r "$1" | cut -d' ' -f1; }
else
	die "sha256sum or openssl is required"
fi

if ! command -v docker >/dev/null 2>&1; then
	[ "$INSTALL_DOCKER" -eq 1 ] || die "Docker is required. Install it first (curl -fsSL https://get.docker.com | sh) or pass --install-docker"
	say "installing Docker with get.docker.com"
	curl -fsSL https://get.docker.com | sh
	systemctl enable --now docker
fi
docker info >/dev/null 2>&1 || die "Docker is installed but not running (systemctl start docker)"

if systemctl is-active --quiet keel-server 2>/dev/null; then
	die "keel-server.service is running. Switch it to aven-server first (README: 'Upgrading from Keel'); do not run both on the same data directory"
fi

UPGRADE=0
[ -f "$ENV_FILE" ] && UPGRADE=1

tty_ok() { [ "$YES" -eq 0 ] && [ -r /dev/tty ] && [ -w /dev/tty ]; }
ask() { # ask VAR "prompt" default
	printf '%s' "$2" >/dev/tty
	[ -n "$3" ] && printf ' [%s]' "$3" >/dev/tty
	printf ': ' >/dev/tty
	_a=""
	read -r _a </dev/tty || true
	[ -n "$_a" ] || _a=$3
	eval "$1=\$_a"
}
safe() { # reject characters that would break the env file
	case "$2" in *[!A-Za-z0-9.:/_@+-]*) die "$1 contains unexpected characters: $2" ;; esac
}

if [ "$UPGRADE" -eq 0 ]; then
	if [ -z "$PUBLIC_URL" ] && tty_ok; then
		ask PUBLIC_URL "Dashboard URL (https://...)" ""
	fi
	case "$PUBLIC_URL" in
		https://?*) ;;
		"") die "--public-url is required for the first install" ;;
		*) die "--public-url must be an https:// URL" ;;
	esac
	PUBLIC_URL=${PUBLIC_URL%/}
	safe --public-url "$PUBLIC_URL"
	host=${PUBLIC_URL#https://}
	host=${host%%/*}
	host=${host%%:*}
	if [ -z "$BASE_DOMAIN" ]; then
		def=${host#*.}
		[ "$def" = "$host" ] && def=$host
		if tty_ok; then ask BASE_DOMAIN "Base domain for services (*.domain)" "$def"; else BASE_DOMAIN=$def; fi
	fi
	safe --base-domain "$BASE_DOMAIN"
	if [ -z "$ACME_EMAIL" ] && tty_ok; then
		ask ACME_EMAIL "Email for Let's Encrypt (optional)" ""
	fi
	[ -z "$ACME_EMAIL" ] || safe --acme-email "$ACME_EMAIL"
	if [ -n "$ADMIN_PASSWORD" ]; then
		[ ${#ADMIN_PASSWORD} -ge 8 ] || die "the admin password must have at least 8 characters"
		case "$ADMIN_PASSWORD" in *[\"\'\\\$\`\ ]*) die "the admin password must not contain quotes, backslashes, \$, backticks or spaces" ;; esac
	fi
	if command -v ss >/dev/null 2>&1; then
		for p in 80 443; do
			if ss -Htln "sport = :$p" 2>/dev/null | grep -q .; then
				warn "port $p is already in use; Aven's proxy needs 80/443 (or set KEEL_PROXY_BIND / KEEL_HTTP_PORT / KEEL_HTTPS_PORT, see the guide)"
			fi
		done
	fi
fi

# ---- fetch ----

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM
PKG=$TMP/pkg
mkdir -p "$PKG/lib"

gh_curl() {
	if [ -n "${GITHUB_TOKEN:-}" ]; then
		curl --proto =https --tlsv1.2 -fsSL --retry 3 -H "Authorization: Bearer $GITHUB_TOKEN" "$@"
	else
		curl --proto =https --tlsv1.2 -fsSL --retry 3 "$@"
	fi
}

if [ -n "$FROM_DIR" ]; then
	[ -x "$FROM_DIR/bin/aven-server" ] || die "$FROM_DIR/bin/aven-server not found (run 'make all' first)"
	say "installing from $FROM_DIR"
	cp "$FROM_DIR/bin/aven-server" "$PKG/aven-server"
	if [ -f "$FROM_DIR/bin/aven-linux-$ARCH" ]; then cp "$FROM_DIR/bin/aven-linux-$ARCH" "$PKG/aven"; else cp "$FROM_DIR/bin/aven" "$PKG/aven"; fi
	for f in "$FROM_DIR"/bin/aven-agent-linux-* "$FROM_DIR"/bin/aven-linux-* "$FROM_DIR"/bin/aven-darwin-* "$FROM_DIR"/bin/aven-windows-*; do
		[ -f "$f" ] && [ ! -L "$f" ] && cp "$f" "$PKG/lib/"
	done
	cp "$FROM_DIR/deploy/aven-server.service" "$FROM_DIR/deploy/aven.env.example" "$PKG/"
	VERSION=$(cd "$FROM_DIR" && git describe --tags --always 2>/dev/null || echo local)
else
	if [ "$VERSION" = latest ]; then
		VERSION=$(gh_curl -H 'Accept: application/vnd.github+json' "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/^ *"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
		[ -n "$VERSION" ] || die "could not find the latest release of $REPO (pass --version, or --from-dir for a local build)"
	fi
	safe --version "$VERSION"
	NAME="aven_${VERSION}_linux_${ARCH}"
	BASE="https://github.com/$REPO/releases/download/$VERSION"
	say "downloading $NAME.tar.gz"
	gh_curl -o "$TMP/$NAME.tar.gz" "$BASE/$NAME.tar.gz" || die "download failed: $BASE/$NAME.tar.gz"
	gh_curl -o "$TMP/checksums.txt" "$BASE/checksums.txt" || die "checksum download failed"
	want=$(awk -v n="$NAME.tar.gz" '$2 == n || $2 == "*"n { print $1 }' "$TMP/checksums.txt")
	[ -n "$want" ] && [ "$want" = "$(sha256 "$TMP/$NAME.tar.gz")" ] || die "checksum mismatch for $NAME.tar.gz"
	tar -xzf "$TMP/$NAME.tar.gz" -C "$TMP"
	cp -R "$TMP/$NAME/." "$PKG/"
fi
for f in aven-server aven aven-server.service aven.env.example; do
	[ -f "$PKG/$f" ] || die "package is missing $f"
done

# ---- upgrade: stop and back up ----

DATA=/var/lib/keel
if [ "$UPGRADE" -eq 1 ]; then
	d=$(sed -n 's/^KEEL_DATA=//p' "$ENV_FILE" | tail -n1)
	[ -n "$d" ] && DATA=$d
	say "upgrading to $VERSION (keeping $ENV_FILE)"
	if systemctl is-active --quiet aven-server; then
		systemctl stop aven-server # containers keep running (KillMode=process)
	fi
	if [ -f "$DATA/keel.db" ]; then
		mkdir -p "$DATA/backups"
		b="$DATA/backups/keel-pre-upgrade-$(date +%Y%m%d-%H%M%S).db"
		cp -p "$DATA/keel.db" "$b"
		for x in wal shm; do [ -f "$DATA/keel.db-$x" ] && cp -p "$DATA/keel.db-$x" "$b-$x"; done
		say "database backup: $b"
	fi
	[ -f "$BIN_DIR/aven-server" ] && cp -p "$BIN_DIR/aven-server" "$BIN_DIR/aven-server.prev"
else
	say "installing $VERSION"
fi

# ---- install ----

install -d -m 0755 "$BIN_DIR" "$LIB_DIR"
install -m 0755 "$PKG/aven-server" "$BIN_DIR/aven-server.new" && mv -f "$BIN_DIR/aven-server.new" "$BIN_DIR/aven-server"
install -m 0755 "$PKG/aven" "$BIN_DIR/aven.new" && mv -f "$BIN_DIR/aven.new" "$BIN_DIR/aven"
for f in "$PKG"/lib/*; do
	[ -f "$f" ] && install -m 0755 "$f" "$LIB_DIR/"
done
install -m 0644 "$PKG/aven-server.service" "$UNIT"
install -d -m 0700 "$CONF_DIR"
install -m 0600 "$PKG/aven.env.example" "$CONF_DIR/aven.env.example"
say "installed $BIN_DIR/aven-server, $BIN_DIR/aven and downloads in $LIB_DIR"

if [ "$RAILPACK" -eq 1 ] && ! command -v railpack >/dev/null 2>&1 && [ ! -x "$BIN_DIR/railpack" ]; then
	case "$ARCH" in amd64) rp=x86_64 ;; arm64) rp=arm64 ;; esac
	RP="railpack-$RAILPACK_VERSION-$rp-unknown-linux-musl.tar.gz"
	RPBASE="https://github.com/railwayapp/railpack/releases/download/$RAILPACK_VERSION"
	say "installing Railpack $RAILPACK_VERSION"
	curl --proto =https --tlsv1.2 -fsSL --retry 3 -o "$TMP/$RP" "$RPBASE/$RP" || die "Railpack download failed (pass --no-railpack to skip)"
	curl --proto =https --tlsv1.2 -fsSL --retry 3 -o "$TMP/rp-checksums.txt" "$RPBASE/checksums.txt" || die "Railpack checksum download failed"
	want=$(awk -v n="$RP" '$2 == n { print $1 }' "$TMP/rp-checksums.txt")
	[ -n "$want" ] && [ "$want" = "$(sha256 "$TMP/$RP")" ] || die "checksum mismatch for $RP"
	mkdir -p "$TMP/rp" && tar -xzf "$TMP/$RP" -C "$TMP/rp"
	install -m 0755 "$TMP/rp/railpack" "$BIN_DIR/railpack"
fi

GENERATED=""
if [ "$UPGRADE" -eq 0 ]; then
	rand() { head -c 64 /dev/urandom | od -An -tx1 | tr -d ' \n' | cut -c1-"$1"; }
	if [ -z "$ADMIN_PASSWORD" ]; then
		ADMIN_PASSWORD=$(rand 32)
		GENERATED=1
	fi
	umask 077
	cat > "$ENV_FILE.new" <<EOF
# Aven server settings (root, 0600). Every option is described in $CONF_DIR/aven.env.example
# and docs/self-hosting.md. Restart after changes: systemctl restart aven-server
KEEL_DATA=$DATA
KEEL_LISTEN=127.0.0.1:7070
KEEL_PUBLIC_URL=$PUBLIC_URL
KEEL_BASE_DOMAIN=$BASE_DOMAIN
KEEL_TLS=auto
KEEL_HTTP_PORT=80
KEEL_HTTPS_PORT=443
KEEL_ACME_EMAIL=$ACME_EMAIL
# Password for the built-in owner account admin@local (at least 8 characters, needed on every start).
KEEL_ADMIN_PASSWORD=$ADMIN_PASSWORD
# GitHub OAuth App (callback: $PUBLIC_URL/api/github/oauth/callback). Optional.
KEEL_GITHUB_CLIENT_ID=
KEEL_GITHUB_CLIENT_SECRET=
KEEL_GITHUB_WEBHOOK_SECRET=$(rand 64)
KEEL_AGENT_BIN_DIR=$LIB_DIR
EOF
	mv -f "$ENV_FILE.new" "$ENV_FILE"
	chmod 600 "$ENV_FILE"
	say "wrote $ENV_FILE"
fi

systemctl daemon-reload
if [ "$START" -eq 0 ]; then
	say "installed; start with: systemctl enable --now aven-server"
	exit 0
fi
systemctl enable --now aven-server >/dev/null 2>&1 || systemctl enable --now aven-server

LISTEN=$(sed -n 's/^KEEL_LISTEN=//p' "$ENV_FILE" | tail -n1)
[ -n "$LISTEN" ] || LISTEN='[::1]:7070'
case "$LISTEN" in :*) LISTEN="127.0.0.1$LISTEN" ;; 0.0.0.0:*) LISTEN="127.0.0.1:${LISTEN##*:}" ;; \[::\]:*) LISTEN="[::1]:${LISTEN##*:}" ;; esac
say "waiting for aven-server on $LISTEN"
i=0
until curl -fsS -o /dev/null "http://$LISTEN/api/cli/downloads" 2>/dev/null; do
	i=$((i + 1))
	if [ $i -ge 60 ] || ! systemctl is-active --quiet aven-server; then
		journalctl -u aven-server -n 30 --no-pager >&2 || true
		die "aven-server did not come up; see: journalctl -u aven-server"
	fi
	sleep 1
done

URL=$(sed -n 's/^KEEL_PUBLIC_URL=//p' "$ENV_FILE" | tail -n1)
echo
say "aven-server $VERSION is running"
if [ "$UPGRADE" -eq 0 ]; then
	BD=$(sed -n 's/^KEEL_BASE_DOMAIN=//p' "$ENV_FILE" | tail -n1)
	host=${URL#https://}; host=${host%%/*}; host=${host%%:*}
	cat <<EOF

  Dashboard:  $URL
  Sign in:    email admin@local
EOF
	if [ -n "$GENERATED" ]; then
		echo "  Password:   $ADMIN_PASSWORD"
		echo "              (generated; also in $ENV_FILE as KEEL_ADMIN_PASSWORD)"
	else
		echo "  Password:   the one you passed (KEEL_ADMIN_PASSWORD in $ENV_FILE)"
	fi
	cat <<EOF

  DNS: point these names at this server (A and/or AAAA):
    $host
    *.$BD

  After signing in, Admin > Owner · 초기 설정 lists the remaining setup steps.
  Guide: https://github.com/$REPO/blob/main/docs/self-hosting.md
EOF
fi
