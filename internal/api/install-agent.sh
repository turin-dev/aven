#!/bin/sh
# aven-agent installer. Served by aven-server at GET /install-agent.sh.
#
#   curl -fsSL https://aven.example.com/install-agent.sh | sudo sh -s -- --server https://aven.example.com --token <TOKEN>
#
# Options:
#   --server URL        aven-server URL (https:// only)
#   --token TOKEN       one-time join token (or env KEEL_JOIN_TOKEN, or prompted on a terminal)
#   --ca-file PATH      PEM CA certificate to trust for the server (private CA)
#   --env KEY=VALUE     extra agent setting written to the env file (KEY must start with KEEL_), repeatable
#   --reenroll          forget the saved node credentials and join again with --token
#
# The join token is never put on the agent's command line or in the unit file: it goes into
# /etc/keel-agent/agent.env (root, 0600) and is removed from there once the agent has enrolled.
# Re-running the script upgrades the binary and rewrites the unit; saved credentials are kept. The agent generates its own
# private key at enrollment (credentials.json, root 0600) and gets a keel-CA certificate; a node enrolled by an older agent
# upgrades itself to a certificate on the agent's next start.
set -eu

BIN=/usr/local/bin/aven-agent
CONF_DIR=/etc/keel-agent
ENV_FILE=$CONF_DIR/agent.env
CA_DEST=$CONF_DIR/ca.pem
DATA_DIR=/var/lib/keel-agent
CRED=$DATA_DIR/credentials.json
# Keep the established unit name so upgrades replace the running Keel agent in place.
UNIT=/etc/systemd/system/keel-agent.service

SERVER=""
TOKEN="${KEEL_JOIN_TOKEN:-}"
CA_FILE=""
REENROLL=0
EXTRA=""
NL='
'

say() { printf 'aven-agent: %s\n' "$*"; }
die() { printf 'aven-agent: error: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
	case "$1" in
		--server) [ $# -ge 2 ] || die "--server needs a value"; SERVER=$2; shift 2 ;;
		--server=*) SERVER=${1#*=}; shift ;;
		--token) [ $# -ge 2 ] || die "--token needs a value"; TOKEN=$2; shift 2 ;;
		--token=*) TOKEN=${1#*=}; shift ;;
		--ca-file) [ $# -ge 2 ] || die "--ca-file needs a value"; CA_FILE=$2; shift 2 ;;
		--ca-file=*) CA_FILE=${1#*=}; shift ;;
		--env) [ $# -ge 2 ] || die "--env needs KEY=VALUE"; EXTRA="$EXTRA$2$NL"; shift 2 ;;
		--env=*) EXTRA="$EXTRA${1#*=}$NL"; shift ;;
		--reenroll) REENROLL=1; shift ;;
		-h|--help) echo "usage: install-agent.sh --server https://... [--token TOKEN] [--ca-file PEM] [--env KEEL_X=V]... [--reenroll]"; exit 0 ;;
		*) die "unknown option: $1" ;;
	esac
done

[ "$(id -u)" -eq 0 ] || die "run as root (pipe into 'sudo sh -s -- ...')"
[ "$(uname -s)" = Linux ] || die "only Linux is supported"
case "$(uname -m)" in
	x86_64|amd64) ARCH=amd64 ;;
	aarch64|arm64) ARCH=arm64 ;;
	*) die "unsupported architecture: $(uname -m) (amd64 and arm64 only)" ;;
esac

case "$SERVER" in
	https://?*) ;;
	"") die "--server is required" ;;
	*) die "--server must be an https:// URL" ;;
esac
SERVER=${SERVER%/}
case "$SERVER" in *[!]A-Za-z0-9.:/_[-]*) die "--server contains unexpected characters" ;; esac
case "$TOKEN" in *[!A-Za-z0-9]*) die "the join token has unexpected characters" ;; esac

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ] || die "systemd is required"
command -v docker >/dev/null 2>&1 || die "Docker is required (for example: curl -fsSL https://get.docker.com | sh)"
docker info >/dev/null 2>&1 || die "Docker is installed but not running"

umask 077
mkdir -p "$CONF_DIR" "$DATA_DIR"
chmod 700 "$CONF_DIR" "$DATA_DIR"

if [ -n "$CA_FILE" ]; then
	[ -r "$CA_FILE" ] || die "cannot read --ca-file $CA_FILE"
	grep -q 'BEGIN CERTIFICATE' "$CA_FILE" || die "--ca-file is not a PEM certificate"
	if [ "$CA_FILE" != "$CA_DEST" ]; then
		cp "$CA_FILE" "$CA_DEST.tmp" && mv -f "$CA_DEST.tmp" "$CA_DEST"
	fi
	chmod 644 "$CA_DEST"
elif [ -f "$ENV_FILE" ] && grep -q '^KEEL_CA_FILE=' "$ENV_FILE" && [ -f "$CA_DEST" ]; then
	CA_FILE=$CA_DEST # keep the CA from an earlier run
fi
set -- --proto =https --tlsv1.2 -fsSL --retry 3
[ -n "$CA_FILE" ] && set -- "$@" --cacert "$CA_DEST"

if [ "$REENROLL" -eq 1 ]; then
	systemctl stop keel-agent 2>/dev/null || true
	systemctl stop aven-agent 2>/dev/null || true
	rm -f "$CRED"
fi
if [ -f "$CRED" ]; then
	[ -n "$TOKEN" ] && say "already enrolled ($CRED exists); the token is not used (pass --reenroll to join again)"
	TOKEN=""
elif [ -z "$TOKEN" ]; then
	# Only in the foreground of the terminal: piped into `sudo sh`, sudo-rs (Ubuntu 25.10+) keeps the
	# script in the background, where stty and read on /dev/tty stop it (SIGTTOU/SIGTTIN).
	fg_tty() { # stat after "comm) ": state ppid pgrp session tty_nr tpgid
		_st=$(cat /proc/$$/stat 2>/dev/null) || return 1
		set -- ${_st##*) }
		[ "${6:--1}" = "${3:-}" ]
	}
	if fg_tty && [ -r /dev/tty ] && [ -w /dev/tty ]; then
		printf 'Join token: ' >/dev/tty
		stty -echo </dev/tty 2>/dev/null || true
		read -r TOKEN </dev/tty || true
		stty echo </dev/tty 2>/dev/null || true
		printf '\n' >/dev/tty
		case "$TOKEN" in *[!A-Za-z0-9]*) die "the join token has unexpected characters" ;; esac
	fi
	[ -n "$TOKEN" ] || die "--token is required for the first install"
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM
URL="$SERVER/downloads/aven-agent-linux-$ARCH"
say "downloading $URL"
curl "$@" -o "$TMP/aven-agent" "$URL" || die "download failed"
curl "$@" -o "$TMP/aven-agent.sha256" "$URL.sha256" || die "checksum download failed"
want=$(cut -d' ' -f1 "$TMP/aven-agent.sha256")
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$TMP/aven-agent" | cut -d' ' -f1)
elif command -v openssl >/dev/null 2>&1; then
	got=$(openssl dgst -sha256 -r "$TMP/aven-agent" | cut -d' ' -f1)
else
	die "sha256sum or openssl is required"
fi
[ -n "$want" ] && [ "$want" = "$got" ] || die "checksum mismatch for $URL"
chmod 755 "$TMP/aven-agent"
cp "$TMP/aven-agent" "$BIN.new"
chmod 755 "$BIN.new"
mv -f "$BIN.new" "$BIN"
say "installed $BIN"

# Env file: managed keys are rewritten, other lines (e.g. settings added by hand) are kept.
: > "$TMP/agent.env"
if [ -f "$ENV_FILE" ]; then
	grep -v -e '^KEEL_SERVER_URL=' -e '^KEEL_JOIN_TOKEN=' -e '^KEEL_CA_FILE=' "$ENV_FILE" >> "$TMP/agent.env" || true
fi
printf '%s' "$EXTRA" | while IFS= read -r kv; do
	[ -n "$kv" ] || continue
	key=${kv%%=*}
	case "$key" in
		KEEL_SERVER_URL|KEEL_JOIN_TOKEN|KEEL_CA_FILE) die "--env cannot set $key" ;;
		KEEL_*) ;;
		*) die "--env key must start with KEEL_: $key" ;;
	esac
	case "$key" in *[!A-Z0-9_]*) die "invalid --env key: $key" ;; esac
	grep -v "^$key=" "$TMP/agent.env" > "$TMP/agent.env.2" || true
	mv -f "$TMP/agent.env.2" "$TMP/agent.env"
	printf '%s\n' "$kv" >> "$TMP/agent.env"
done
{
	printf 'KEEL_SERVER_URL=%s\n' "$SERVER"
	[ -n "$CA_FILE" ] && printf 'KEEL_CA_FILE=%s\n' "$CA_DEST"
	[ -n "$TOKEN" ] && printf 'KEEL_JOIN_TOKEN=%s\n' "$TOKEN"
} >> "$TMP/agent.env"
chmod 600 "$TMP/agent.env"
chown root:root "$TMP/agent.env" 2>/dev/null || true
mv -f "$TMP/agent.env" "$ENV_FILE"

cat > "$UNIT.tmp" <<'EOF'
[Unit]
Description=aven agent
Documentation=https://github.com/turin-dev/aven
After=network-online.target docker.service
Wants=network-online.target docker.service

[Service]
Type=simple
EnvironmentFile=/etc/keel-agent/agent.env
ExecStart=/usr/local/bin/aven-agent
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
chmod 644 "$UNIT.tmp"
# Retire any unit created by an intermediate Aven build before enabling the compatible unit.
systemctl stop aven-agent 2>/dev/null || true
systemctl disable aven-agent >/dev/null 2>&1 || true
systemctl stop keel-agent 2>/dev/null || true
mv -f "$UNIT.tmp" "$UNIT"
systemctl daemon-reload
systemctl enable keel-agent >/dev/null 2>&1 || die "systemctl enable keel-agent failed"
systemctl restart keel-agent || die "aven-agent did not start (journalctl -u keel-agent)"

if [ -n "$TOKEN" ]; then
	say "waiting for the agent to join"
	i=0
	while [ ! -s "$CRED" ] && [ $i -lt 60 ]; do sleep 1; i=$((i + 1)); done
	# The token is single-use; drop it from the env file once it has been exchanged.
	grep -v '^KEEL_JOIN_TOKEN=' "$ENV_FILE" > "$ENV_FILE.tmp" || true
	chmod 600 "$ENV_FILE.tmp"
	mv -f "$ENV_FILE.tmp" "$ENV_FILE"
	[ -s "$CRED" ] || die "the agent did not join within 60s (journalctl -u keel-agent); the token may be invalid or expired"
	say "joined"
fi
# A crash right after start (e.g. Docker or keel-proxy problems) shows up as automatic restarts.
sleep 8
restarts=$(systemctl show -p NRestarts --value keel-agent 2>/dev/null || echo 0)
if ! systemctl is-active --quiet keel-agent || [ "${restarts:-0}" != 0 ]; then
	journalctl -u keel-agent -n 5 --no-pager 2>/dev/null || true
	die "aven-agent is not running steadily (journalctl -u keel-agent)"
fi
say "aven-agent is running"
