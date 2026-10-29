#!/bin/sh
# Aven server installer and upgrader.
#
#   curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh | sudo sh
#   ... | sudo sh -s -- --yes --public-url https://aven.example.com     (unattended)
#
# First run: installs aven-server, the CLI, agent/CLI downloads and Railpack, writes /etc/aven/aven.env
# with a generated admin password, installs the systemd unit and starts it.
# Later runs: upgrade in place. The env file is kept, the database is copied to $DATA/backups first,
# and the previous binary is kept as /usr/local/bin/aven-server.prev. Running containers keep running.
#
# Options:
#   --public-url URL       dashboard URL, https:// (first install; prompted on a terminal). Without one
#                          the dashboard is http://localhost:7070, reachable from this server only
#   --base-domain DOMAIN   wildcard domain for service hostnames (default: parent of the public URL host)
#   --acme-email EMAIL     contact email for Let's Encrypt (optional)
#   --admin-password PW    password for admin@local (default: generated; or env AVEN_ADMIN_PASSWORD, else KEEL_ADMIN_PASSWORD)
#   --version TAG          release to install, e.g. v0.1.0 (default: latest)
#   --from-dir DIR         install from a local checkout built with 'make all' instead of a release
#   --railpack-version V   Railpack release to install if railpack is missing (default: v0.42.0)
#   --no-railpack          do not install Railpack
#   --install-docker       install Docker with get.docker.com if it is missing
#   --no-start             install only; do not enable or start aven-server
#   -y, --yes              never prompt
#
# On a terminal the script asks for every first-install setting it was not given, shows a summary and
# asks before changing anything. Upgrades ask once for confirmation. With -y or without a terminal it
# never asks. NO_COLOR turns colors off; TERM=dumb gives plain output.
#
# Environment: GITHUB_TOKEN, when set, is sent with GitHub requests (API rate limits). AVEN_REPO overrides the
# owner/repo to download releases from (forks).
# Existing installs may still have /etc/keel/aven.env, /var/lib/keel and KEEL_* settings.
# Those are used when the new paths are absent. AVEN_* is written for a new install.
set -eu

REPO=${AVEN_REPO:-turin-dev/aven}
CONF_DIR=/etc/aven
ENV_FILE=$CONF_DIR/aven.env
BIN_DIR=/usr/local/bin
LIB_DIR=/usr/local/lib/aven
UNIT=/etc/systemd/system/aven-server.service

# ---- terminal UI ----

ESC=$(printf '\033')
TAB=$(printf '\t')
FANCY=0 # glyphs, colors and spinners on stdout
[ -t 1 ] && [ "${TERM:-dumb}" != dumb ] && FANCY=1
ACC="" BOLD="" DIM="" RED="" GRN="" YEL="" RST=""
if [ "${TERM:-dumb}" != dumb ] && [ -z "${NO_COLOR:-}" ]; then
	case "${COLORTERM:-}" in
		truecolor|24bit) ACC="$ESC[38;2;215;119;87m" ;;
		*) ACC="$ESC[38;5;173m" ;;
	esac
	BOLD="$ESC[1m" DIM="$ESC[2m" RED="$ESC[31m" GRN="$ESC[32m" YEL="$ESC[33m" RST="$ESC[0m"
fi
case "${LC_ALL:-${LC_CTYPE:-${LANG:-}}}" in
	*[Uu][Tt][Ff]-8*|*[Uu][Tt][Ff]8*)
		G_DOT='●' G_ELB='⎿' G_PTR='❯' G_STAR='✻' G_WARN='⚠' G_ERR='✗' G_MASK='•' G_ELL='…'
		B_TL='╭' B_TR='╮' B_BL='╰' B_BR='╯' B_H='─' B_V='│'
		SPIN='· ✢ ✳ ✶ ✻ ✽ ✻ ✶ ✳ ✢' KEYS='Enter to select · ↑/↓ to move · Ctrl+C to cancel' ;;
	*)
		G_DOT='*' G_ELB='\_' G_PTR='>' G_STAR='*' G_WARN='!' G_ERR='x' G_MASK='*' G_ELL='...'
		B_TL='+' B_TR='+' B_BL='+' B_BR='+' B_H='-' B_V='|'
		SPIN='- \ | /' KEYS='Enter to select, up/down to move, Ctrl+C to cancel' ;;
esac

say() {
	if [ "$FANCY" -eq 1 ]; then printf '%s%s%s %s\n' "$ACC" "$G_DOT" "$RST" "$*"
	else printf 'aven: %s\n' "$*"; fi
}
detail() { # a dim result line under the previous step
	if [ "$FANCY" -eq 1 ]; then printf '  %s%s  %s%s\n' "$DIM" "$G_ELB" "$*" "$RST"
	else printf 'aven:   %s\n' "$*"; fi
}
warn() {
	if [ "$FANCY" -eq 1 ] && [ -t 2 ]; then printf '%s%s %s%s\n\n' "$YEL" "$G_WARN" "$*" "$RST" >&2
	else printf 'aven: warning: %s\n' "$*" >&2; fi
}
die() {
	if [ "$FANCY" -eq 1 ] && [ -t 2 ]; then printf '%s%s Error: %s%s\n' "$RED" "$G_ERR" "$*" "$RST" >&2
	else printf 'aven: error: %s\n' "$*" >&2; fi
	exit 1
}
need_arg() { [ "$2" -ge 2 ] || die "$1 needs a value"; }
rep() { # rep STRING N
	_rs="" _rn=$2
	while [ "$_rn" -gt 0 ]; do _rs="$_rs$1"; _rn=$((_rn - 1)); done
	printf '%s' "$_rs"
}

# box < rows: a rounded box like Claude Code's welcome screen; plain lines when not on a terminal.
# Rows are ASCII: "text", "key<TAB>value", "" or "*text" (heading with a star).
box() {
	_rows=$(cat)
	_kw=0 _w=44
	while IFS= read -r _r; do
		case "$_r" in *"$TAB"*) _k=${_r%%"$TAB"*}; [ ${#_k} -le "$_kw" ] || _kw=${#_k} ;; esac
	done <<EOF
$_rows
EOF
	while IFS= read -r _r; do
		case "$_r" in
			*"$TAB"*) _k=${_r%%"$TAB"*}; _v=${_r#*"$TAB"}; _n=$((2 + _kw + 2 + ${#_v})) ;;
			"*"*) _n=$((${#_r} + 1)) ;;
			*) _n=$((2 + ${#_r})) ;;
		esac
		[ "$_n" -le "$_w" ] || _w=$_n
	done <<EOF
$_rows
EOF
	_bv=$B_V
	_cols=$( (stty size <&1) 2>/dev/null | cut -d' ' -f2) || _cols=""
	[ -n "$_cols" ] || _cols=$COLS
	if [ $((_w + 4)) -gt "$_cols" ] 2>/dev/null; then
		# too wide for the terminal: drop the side borders so nothing wraps
		_bv="" _w=$((_cols - 5))
	fi
	if [ -t 1 ] && [ "${TERM:-dumb}" != dumb ]; then
		printf '%s%s%s%s%s\n' "$ACC" "$B_TL" "$(rep "$B_H" $((_w + 2)))" "$B_TR" "$RST"
		while IFS= read -r _r; do
			case "$_r" in
				*"$TAB"*)
					_k=${_r%%"$TAB"*}; _v=${_r#*"$TAB"}
					_c="  $DIM$_k$RST$(rep ' ' $((_kw - ${#_k} + 2)))$_v"; _n=$((2 + _kw + 2 + ${#_v})) ;;
				"*"*) _c="$ACC$G_STAR$RST $BOLD${_r#?}$RST"; _n=$((${#_r} + 1)) ;;
				*) _c="  $_r"; _n=$((2 + ${#_r})) ;;
			esac
			if [ -n "$_bv" ]; then printf '%s%s%s %s%s %s%s%s\n' "$ACC" "$_bv" "$RST" "$_c" "$(rep ' ' $((_w - _n)))" "$ACC" "$_bv" "$RST"
			else printf ' %s\n' "$_c"; fi
		done <<EOF
$_rows
EOF
		printf '%s%s%s%s%s\n' "$ACC" "$B_BL" "$(rep "$B_H" $((_w + 2)))" "$B_BR" "$RST"
	else
		while IFS= read -r _r; do
			case "$_r" in
				*"$TAB"*) _k=${_r%%"$TAB"*}; printf '  %s:%s%s\n' "$_k" "$(rep ' ' $((_kw - ${#_k} + 2)))" "${_r#*"$TAB"}" ;;
				"*"*) printf '%s\n' "${_r#?}" ;;
				*) printf '  %s\n' "$_r" ;;
			esac
		done <<EOF
$_rows
EOF
	fi
}

# spin "doing" "done" command...: runs the command with a spinner, its output kept out of the way
# (shown when it fails). Without a terminal it prints the step and runs the command as is.
spin() {
	_s1=$1 _s2=$2
	shift 2
	if [ "$FANCY" -ne 1 ]; then
		say "$_s1"
		"$@"
		return
	fi
	"$@" >"$TMP/step.log" 2>&1 &
	_sp=$!
	_t0=$(date +%s) _si=0
	printf '%s' "$ESC[?25l"
	while kill -0 "$_sp" 2>/dev/null; do
		set -- $SPIN
		shift $((_si % $#))
		printf '\r%s%s%s %s%s%s %s(%ss)%s%s' "$ACC" "$1" "$RST" "$ACC" "$_s1" "$G_ELL" "$DIM" $(($(date +%s) - _t0)) "$RST" "$ESC[K"
		_si=$((_si + 1))
		sleep 0.12
	done
	_st=0
	wait "$_sp" || _st=$?
	printf '\r%s%s' "$ESC[K" "$ESC[?25h"
	if [ "$_st" -eq 0 ]; then
		printf '%s%s%s %s %s(%ss)%s\n' "$GRN" "$G_DOT" "$RST" "$_s2" "$DIM" $(($(date +%s) - _t0)) "$RST"
	else
		printf '%s%s%s %s\n' "$RED" "$G_DOT" "$RST" "$_s1"
		tail -n 15 "$TMP/step.log" | while IFS= read -r _l; do printf '  %s%s  %s%s\n' "$DIM" "$G_ELB" "$_l" "$RST"; done
	fi
	return "$_st"
}

TMP=$(mktemp -d)
TTY=0 STTY_SAVED=""
cleanup() {
	if [ "$TTY" -eq 1 ]; then
		[ -z "$STTY_SAVED" ] || stty "$STTY_SAVED" </dev/tty 2>/dev/null || true
		printf '%s' "$ESC[?25h" >/dev/tty 2>/dev/null || true
	elif [ "$FANCY" -eq 1 ]; then
		printf '%s' "$ESC[?25h"
	fi
	rm -rf "$TMP"
}
IN_BOX=0 # the cursor is inside an input box: move below it before exiting
trap cleanup EXIT
trap '[ "$IN_BOX" -eq 0 ] || printf "%s[2B\n" "$ESC" >/dev/tty; exit 130' INT TERM

PUBLIC_URL=""
BASE_DOMAIN=""
ACME_EMAIL=""
ADMIN_PASSWORD="${AVEN_ADMIN_PASSWORD:-${KEEL_ADMIN_PASSWORD:-}}"
if [ -z "${AVEN_ADMIN_PASSWORD:-}" ] && [ -n "${KEEL_ADMIN_PASSWORD:-}" ]; then
	warn "KEEL_ADMIN_PASSWORD is set; AVEN_ADMIN_PASSWORD is the new name (the old name still works)"
fi
VERSION=latest
FROM_DIR=""
RAILPACK_VERSION=v0.42.0
RAILPACK=1
INSTALL_DOCKER=0
START=1
YES=0

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
			else echo "usage: curl -fsSL .../install-server.sh | sudo sh [-s -- options]; see https://github.com/$REPO/blob/main/install-server.sh"; fi
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

if systemctl is-active --quiet keel-server 2>/dev/null; then
	die "keel-server.service is running. Switch it to aven-server first (README: 'Upgrading from Keel'); do not run both on the same data directory"
fi

# 새 경로가 없으면 예전 설치의 env 파일을 그대로 쓴다.
if [ ! -f "$ENV_FILE" ]; then
	for old in /etc/keel/aven.env /etc/keel/keel.env; do
		if [ -f "$old" ]; then
			ENV_FILE=$old
			CONF_DIR=$(dirname "$old")
			warn "$old is in use; /etc/aven/aven.env is the new path (the old file still works)"
			break
		fi
	done
fi

UPGRADE=0
[ -f "$ENV_FILE" ] && UPGRADE=1

# ---- questions (on a terminal only) ----

# /dev/tty exists even without a controlling terminal (cron, CI, ssh without -t); only opening it tells.
[ "$YES" -eq 0 ] && (exec </dev/tty >/dev/tty) 2>/dev/null && TTY=1
tty_ok() { [ "$TTY" -eq 1 ]; }
PRETTY=0 # boxes, menus and redrawn answers on the terminal
if tty_ok; then
	STTY_SAVED=$(stty -g </dev/tty 2>/dev/null || true)
	[ "${TERM:-dumb}" = dumb ] || PRETTY=1
fi
COLS=$( (stty size </dev/tty) 2>/dev/null | cut -d' ' -f2) || COLS=""
[ -n "$COLS" ] || COLS=$(tput cols 2>/dev/null || echo 80)
[ "$COLS" -gt 20 ] 2>/dev/null || COLS=80

rows_of() { echo $(((${#1} + $2 - 1) / COLS + 1)); } # rows_of TEXT INDENT: terminal rows the line takes

key() { # read one key from the terminal into KEY: up, down, enter, bs, other, or the character
	_kc=$(dd bs=1 count=1 2>/dev/null </dev/tty | od -An -tu1 | tr -d ' \n')
	case "$_kc" in
		10|13) KEY=enter ;;
		127|8) KEY=bs ;;
		27)
			stty min 0 time 1 </dev/tty
			_ks=$(dd bs=2 count=1 2>/dev/null </dev/tty | od -An -tu1 | tr -s ' ')
			stty min 1 time 0 </dev/tty
			case "$_ks" in *"91 65"*|*"79 65"*) KEY=up ;; *"91 66"*|*"79 66"*) KEY=down ;; *) KEY=other ;; esac ;;
		"") KEY=other ;;
		*)
			if [ "$_kc" -ge 32 ] && [ "$_kc" -le 126 ]; then KEY=$(printf "\\$(printf %03o "$_kc")")
			else KEY=other; fi ;;
	esac
}
raw() { stty -icanon -echo min 1 time 0 </dev/tty; }
cooked() { [ -z "$STTY_SAVED" ] || stty "$STTY_SAVED" </dev/tty; }

UI_ERR=""
# ui_input VAR "title" "hint" "default" "footer" [secret]: one question in an input box. The box is
# erased after the answer; the caller validates, sets UI_ERR and asks again, or calls ui_done.
ui_input() {
	_iv=$1 _it=$2 _ih=$3 _id=$4 _if=$5 _is=${6:-}
	[ -n "$_if" ] || { [ -z "$_id" ] || _if="Enter to use $_id"; }
	if [ "$PRETTY" -ne 1 ]; then
		[ -z "$UI_ERR" ] || printf '  %s\n' "$UI_ERR" >/dev/tty
		printf '%s' "$_it" >/dev/tty
		[ -z "$_id" ] || printf ' [%s]' "$_id" >/dev/tty
		printf ': ' >/dev/tty
		[ -z "$_is" ] || stty -echo </dev/tty
		_a=""
		read -r _a </dev/tty || { cooked; die "no answer (input closed); pass the options on the command line"; }
		[ -z "$_is" ] || { cooked; printf '\n' >/dev/tty; }
		[ -n "$_a" ] || _a=$_id
		eval "$_iv=\$_a"
		return
	fi
	_bw=$((COLS - 1))
	[ "$_bw" -le 100 ] || _bw=100
	_above=$(($(rows_of "$_it" 1) + 1))
	{
		printf ' %s%s%s\n' "$BOLD" "$_it" "$RST"
		if [ -n "$_ih" ]; then
			printf ' %s%s%s\n' "$DIM" "$_ih" "$RST"
		fi
		printf '%s%s%s%s%s\n' "$DIM" "$B_TL" "$(rep "$B_H" $((_bw - 2)))" "$B_TR" "$RST"
		printf '%s%s%s %s%s%s %s%s%s%s\n' "$DIM" "$B_V" "$RST" "$ACC" "$G_PTR" "$RST" "$(rep ' ' $((_bw - 5)))" "$DIM" "$B_V" "$RST"
		printf '%s%s%s%s%s\n' "$DIM" "$B_BL" "$(rep "$B_H" $((_bw - 2)))" "$B_BR" "$RST"
		if [ -n "$UI_ERR" ]; then printf '  %s%s %s%s\n' "$RED" "$G_ELB" "$UI_ERR" "$RST"
		else printf '  %s%s%s\n' "$DIM" "$_if" "$RST"; fi
		printf '%s[%dA%s[5G' "$ESC" $((2 + $(rows_of "${UI_ERR:-$_if}" 4))) "$ESC"
	} >/dev/tty
	[ -z "$_ih" ] || _above=$((_above + $(rows_of "$_ih" 1)))
	_a=""
	IN_BOX=1
	if [ -z "$_is" ]; then
		read -r _a </dev/tty || die "no answer (input closed); pass the options on the command line"
	else
		raw
		while :; do
			key
			case "$KEY" in
				enter) break ;;
				bs) [ -z "$_a" ] || { _a=${_a%?}; printf '\b \b' >/dev/tty; } ;;
				up|down|other) ;;
				*) _a="$_a$KEY"; printf '%s' "$G_MASK" >/dev/tty ;;
			esac
		done
		cooked
		printf '\n' >/dev/tty
	fi
	IN_BOX=0
	printf '%s[%dA%s[J' "$ESC" $((_above + $(rows_of "$_a" 4))) "$ESC" >/dev/tty
	[ -n "$_a" ] || _a=$_id
	eval "$_iv=\$_a"
}

# ui_select "title" "hint" DEFAULT option...: an arrow-key menu; sets SEL to the chosen number.
ui_select() {
	_st=$1 _sh=$2 SEL=$3
	shift 3
	_sn=$#
	if [ "$PRETTY" -ne 1 ]; then
		printf '%s\n' "$_st" >/dev/tty
		[ -z "$_sh" ] || printf '%s\n' "$_sh" >/dev/tty
		_i=1
		for _o in "$@"; do printf '  %d. %s\n' "$_i" "$_o" >/dev/tty; _i=$((_i + 1)); done
		while :; do
			printf 'Choose 1-%d [%d]: ' "$_sn" "$SEL" >/dev/tty
			_a=""
			read -r _a </dev/tty || die "no answer (input closed); pass the options on the command line"
			[ -n "$_a" ] || _a=$SEL
			case "$_a" in [1-9]) [ "$_a" -gt "$_sn" ] || { SEL=$_a; break; } ;; esac
		done
		return
	fi
	_above=$(rows_of "$_st" 1)
	{
		printf ' %s%s%s\n' "$BOLD" "$_st" "$RST"
		[ -z "$_sh" ] || printf ' %s%s%s\n' "$DIM" "$_sh" "$RST"
	} >/dev/tty
	[ -z "$_sh" ] || _above=$((_above + $(rows_of "$_sh" 1)))
	raw
	printf '%s' "$ESC[?25l" >/dev/tty
	_first=1
	while :; do
		[ "$_first" -eq 1 ] || printf '%s[%dA' "$ESC" $((_sn + 1)) >/dev/tty
		_first=0
		_i=1
		for _o in "$@"; do
			if [ "$_i" -eq "$SEL" ]; then printf '\r%s[K %s%s %d. %s%s\n' "$ESC" "$ACC" "$G_PTR" "$_i" "$_o" "$RST"
			else printf '\r%s[K   %d. %s\n' "$ESC" "$_i" "$_o"; fi
			_i=$((_i + 1))
		done >/dev/tty
		printf '\r%s[K  %s%s%s\n' "$ESC" "$DIM" "$KEYS" "$RST" >/dev/tty
		key
		case "$KEY" in
			up|k) [ "$SEL" -le 1 ] || SEL=$((SEL - 1)) ;;
			down|j) [ "$SEL" -ge "$_sn" ] || SEL=$((SEL + 1)) ;;
			enter) break ;;
			[1-9]) [ "$KEY" -gt "$_sn" ] || { SEL=$KEY; break; } ;;
			y|Y) [ "$_sn" -ne 2 ] || { SEL=1; break; } ;;
			n|N) [ "$_sn" -ne 2 ] || { SEL=2; break; } ;;
		esac
	done
	cooked
	printf '%s%s[%dA%s[J' "$ESC[?25h" "$ESC" $((_above + _sn + 1)) "$ESC" >/dev/tty
}
nth() { shift "$1"; printf '%s' "$1"; } # nth N args...: the Nth argument

ui_done() { # ui_done "title" "answer": what the question collapses into, like a finished step
	UI_ERR=""
	[ "$PRETTY" -eq 1 ] || return 0
	printf '%s%s%s %s%s%s\n  %s%s%s  %s\n\n' "$ACC" "$G_DOT" "$RST" "$BOLD" "$1" "$RST" "$DIM" "$G_ELB" "$RST" "$2" >/dev/tty
}

valid() { case "$1" in *[!A-Za-z0-9.:/_@+-]*) return 1 ;; esac; }
safe() { # reject characters that would break the env file
	valid "$2" || die "$1 contains unexpected characters: $2"
}
url_problem() { # prints what is wrong with dashboard URL $1, nothing if it is fine
	case "$1" in
		http://localhost|http://localhost:*|http://127.0.0.1|http://127.0.0.1:*)
			case "${1##*:}" in *[!0-9]*|"") echo "needs a port, e.g. http://localhost:7070" ;; esac ;;
		https://?*) valid "$1" || echo "contains unexpected characters" ;;
		*) echo "must be an https:// URL (or http://localhost:PORT)" ;;
	esac
}
domain_problem() {
	case "$1" in
		"") echo "is required" ;;
		.*|*.|*..*|*[!A-Za-z0-9.-]*) echo "must be a domain name such as apps.example.com" ;;
	esac
}
email_problem() {
	case "$1" in
		"") ;;
		?*@?*.?*) valid "$1" || echo "contains unexpected characters" ;;
		*) echo "must be an email address" ;;
	esac
}
password_problem() {
	[ ${#1} -ge 8 ] || { echo "The admin password must have at least 8 characters."; return; }
	case "$1" in *[\"\'\\\$\`\ ]*) echo "The admin password must not contain quotes, backslashes, \$, backticks or spaces." ;; esac
}
have_railpack() { command -v railpack >/dev/null 2>&1 || [ -x "$BIN_DIR/railpack" ]; }
gh_curl() {
	if [ -n "${GITHUB_TOKEN:-}" ]; then
		curl --proto =https --tlsv1.2 -fsSL --retry 3 -H "Authorization: Bearer $GITHUB_TOKEN" "$@"
	else
		curl --proto =https --tlsv1.2 -fsSL --retry 3 "$@"
	fi
}

if tty_ok || [ "$FANCY" -eq 1 ]; then
	if [ "$UPGRADE" -eq 1 ]; then mode="upgrade ($ENV_FILE)"; else mode="first install"; fi
	echo
	box <<EOF
*Welcome to the Aven installer

Self-hosted deploys on your own servers.

Server${TAB}$(uname -n) ($ARCH)
Mode${TAB}$mode
EOF
	if tty_ok && [ "$UPGRADE" -eq 0 ]; then
		printf ' %sA few questions first. Nothing changes until you confirm.%s\n\n' "$DIM" "$RST" >/dev/tty
	fi
fi

NEED_DOCKER=0
if ! command -v docker >/dev/null 2>&1; then
	if [ "$INSTALL_DOCKER" -eq 0 ] && tty_ok; then
		ui_select "Docker is not installed. Install it?" "Aven runs every service in a Docker container." 1 \
			"Yes, install it with get.docker.com" "No, exit"
		[ "$SEL" -eq 1 ] && INSTALL_DOCKER=1
		ui_done "Docker" "$(nth "$SEL" "install with get.docker.com" "not installed")"
	fi
	[ "$INSTALL_DOCKER" -eq 1 ] || die "Docker is required. Install it first (curl -fsSL https://get.docker.com | sh) or pass --install-docker"
	NEED_DOCKER=1
else
	docker info >/dev/null 2>&1 || die "Docker is installed but not running (systemctl start docker)"
fi

LOCAL=0 # no domain: the dashboard is only on this server, at http://localhost:PORT
LISTEN_ADDR=127.0.0.1:7070
if [ "$UPGRADE" -eq 0 ]; then
	if [ -z "$PUBLIC_URL" ] && tty_ok; then
		while :; do
			ui_input PUBLIC_URL "Dashboard URL" "The address you open Aven at, on its own name." "" "e.g. aven.example.com, or Enter for localhost only"
			case "$PUBLIC_URL" in ""|*://*) ;; *) PUBLIC_URL="https://$PUBLIC_URL" ;; esac
			PUBLIC_URL=${PUBLIC_URL%/}
			[ -n "$PUBLIC_URL" ] || break
			p=$(url_problem "$PUBLIC_URL")
			[ -z "$p" ] && break
			UI_ERR="The URL $p."
		done
		if [ -n "$PUBLIC_URL" ]; then ui_done "Dashboard URL" "$PUBLIC_URL"
		else ui_done "Dashboard URL" "no domain: http://localhost:7070 on this server"; fi
	fi
	if [ -z "$PUBLIC_URL" ]; then
		PUBLIC_URL=http://localhost:7070
		tty_ok || warn "No --public-url: the dashboard will be at http://localhost:7070 on this server only."
	fi
	PUBLIC_URL=${PUBLIC_URL%/}
	p=$(url_problem "$PUBLIC_URL")
	[ -z "$p" ] || die "--public-url $p"
	case "$PUBLIC_URL" in http://*) LOCAL=1; LISTEN_ADDR="127.0.0.1:${PUBLIC_URL##*:}" ;; esac
	host=${PUBLIC_URL#*://}
	host=${host%%/*}
	host=${host%%:*}
	if [ "$LOCAL" -eq 0 ] && command -v getent >/dev/null 2>&1 && ! getent ahosts "$host" >/dev/null 2>&1; then
		warn "$host does not resolve yet. Point it at this server; Let's Encrypt needs it."
	fi

	if [ -z "$BASE_DOMAIN" ] && [ "$LOCAL" -eq 0 ]; then
		def=${host#*.}
		[ "$def" = "$host" ] && def=$host
		if tty_ok; then
			while :; do
				ui_input BASE_DOMAIN "Base domain for services" "Services get names like web-production.$def (needs a *.$def DNS record)." "$def" ""
				p=$(domain_problem "$BASE_DOMAIN")
				[ -z "$p" ] && break
				UI_ERR="The base domain $p."
			done
			ui_done "Base domain" "*.$BASE_DOMAIN"
		else
			BASE_DOMAIN=$def
		fi
	fi
	if [ -n "$BASE_DOMAIN" ]; then
		p=$(domain_problem "$BASE_DOMAIN")
		[ -z "$p" ] || die "--base-domain $p"
	fi

	if [ -z "$ACME_EMAIL" ] && [ "$LOCAL" -eq 0 ] && tty_ok; then
		while :; do
			ui_input ACME_EMAIL "Email for Let's Encrypt" "Certificate expiry notices go here. Optional." "" "Enter to skip"
			p=$(email_problem "$ACME_EMAIL")
			[ -z "$p" ] && break
			UI_ERR="The email $p."
		done
		ui_done "Let's Encrypt email" "${ACME_EMAIL:-none}"
	fi
	p=$(email_problem "$ACME_EMAIL")
	[ -z "$p" ] || die "--acme-email $p"

	if [ -z "$ADMIN_PASSWORD" ] && tty_ok; then
		while :; do
			ui_input pw "Password for admin@local" "You sign in with the email admin@local. At least 8 characters." "" "Enter to generate one" secret
			[ -n "$pw" ] || break
			p=$(password_problem "$pw")
			if [ -n "$p" ]; then
				UI_ERR="$p"
				continue
			fi
			ui_input pw2 "Repeat the password" "" "" "" secret
			if [ "$pw" = "$pw2" ]; then
				ADMIN_PASSWORD=$pw
				break
			fi
			UI_ERR="The passwords do not match."
		done
		pw="" pw2=""
		if [ -n "$ADMIN_PASSWORD" ]; then ui_done "admin@local password" "set (hidden)"; else ui_done "admin@local password" "generated, shown at the end"; fi
	fi
	if [ -n "$ADMIN_PASSWORD" ]; then
		p=$(password_problem "$ADMIN_PASSWORD")
		[ -z "$p" ] || die "$p"
	fi

	if [ "$RAILPACK" -eq 1 ] && ! have_railpack && tty_ok; then
		ui_select "Install Railpack $RAILPACK_VERSION?" "Railpack builds apps that have no Dockerfile." 1 "Yes" "No, skip it"
		[ "$SEL" -eq 1 ] || RAILPACK=0
		ui_done "Railpack" "$(nth "$SEL" "install $RAILPACK_VERSION" "skip")"
	fi

	if command -v ss >/dev/null 2>&1; then
		busy=""
		for p in 80 443; do
			if ss -Htln "sport = :$p" 2>/dev/null | grep -q .; then busy="$busy $p"; fi
		done
		if [ -n "$busy" ]; then
			case "$busy" in *" "*" "*) busy="Ports 80 and 443 are" ;; *) busy="Port${busy} is" ;; esac
			warn "$busy in use. Aven's proxy needs 80 and 443 (or set AVEN_HTTP_PORT / AVEN_HTTPS_PORT)."
		fi
	fi
fi

if [ -z "$FROM_DIR" ] && [ "$VERSION" = latest ]; then
	spin "Finding the latest release" "Found the latest release" \
		gh_curl -H 'Accept: application/vnd.github+json' -o "$TMP/release.json" "https://api.github.com/repos/$REPO/releases/latest" ||
		die "could not reach the GitHub API for $REPO (pass --version, or --from-dir for a local build)"
	VERSION=$(sed -n 's/^ *"tag_name": *"\([^"]*\)".*/\1/p' "$TMP/release.json" | head -n1)
	[ -n "$VERSION" ] || die "could not find the latest release of $REPO (pass --version, or --from-dir for a local build)"
	detail "$VERSION"
	[ "$FANCY" -ne 1 ] || echo
fi
[ -n "$FROM_DIR" ] || safe --version "$VERSION"

if tty_ok; then
	if [ -n "$FROM_DIR" ]; then src="local build in $FROM_DIR"; else src="$VERSION from github.com/$REPO"; fi
	if [ "$UPGRADE" -eq 1 ]; then
		echo >/dev/tty
		box >/dev/tty <<EOF
*Upgrade aven-server

Version${TAB}$src
Settings${TAB}$ENV_FILE is kept
Database${TAB}backed up before the upgrade
Services${TAB}keep running while aven-server restarts
EOF
		echo >/dev/tty
		ui_select "Upgrade now?" "" 1 "Yes, upgrade" "No, exit without changes"
	else
		if [ "$NEED_DOCKER" -eq 1 ]; then d_line="install with get.docker.com"; else d_line="installed"; fi
		if have_railpack; then r_line="already installed"; elif [ "$RAILPACK" -eq 1 ]; then r_line="install $RAILPACK_VERSION"; else r_line="skip"; fi
		if [ -n "$ADMIN_PASSWORD" ]; then pw_line="the one you chose"; else pw_line="generated, shown at the end"; fi
		if [ "$LOCAL" -eq 1 ]; then dash_line="$PUBLIC_URL (this server only)"; else dash_line=$PUBLIC_URL; fi
		if [ -n "$BASE_DOMAIN" ]; then svc_line="*.$BASE_DOMAIN"; else svc_line="no generated domains (add later)"; fi
		if [ "$LOCAL" -eq 1 ]; then le_line="not used"; else le_line=${ACME_EMAIL:-no email}; fi
		if [ "$START" -eq 1 ]; then s_line="start aven-server, enable at boot"; else s_line="install only (--no-start)"; fi
		box >/dev/tty <<EOF
*Ready to install

Version${TAB}$src
Dashboard${TAB}$dash_line
Services${TAB}$svc_line
Let's Encrypt${TAB}$le_line
admin@local${TAB}$pw_line
Docker${TAB}$d_line
Railpack${TAB}$r_line
Settings${TAB}$ENV_FILE
Then${TAB}$s_line
EOF
		echo >/dev/tty
		ui_select "Install now?" "" 1 "Yes, install" "No, exit without changes"
	fi
	if [ "$SEL" -ne 1 ]; then
		say "Nothing was changed."
		exit 0
	fi
fi

if [ "$NEED_DOCKER" -eq 1 ]; then
	spin "Installing Docker" "Installed Docker" sh -c 'curl -fsSL https://get.docker.com | sh' || die "Docker installation failed"
	systemctl enable --now docker >/dev/null 2>&1 || systemctl enable --now docker
	docker info >/dev/null 2>&1 || die "Docker was installed but is not running (systemctl start docker)"
fi

# ---- fetch ----

PKG=$TMP/pkg
mkdir -p "$PKG/lib"

if [ -n "$FROM_DIR" ]; then
	[ -x "$FROM_DIR/bin/aven-server" ] || die "$FROM_DIR/bin/aven-server not found (run 'make all' first)"
	say "Installing from $FROM_DIR"
	cp "$FROM_DIR/bin/aven-server" "$PKG/aven-server"
	if [ -f "$FROM_DIR/bin/aven-linux-$ARCH" ]; then cp "$FROM_DIR/bin/aven-linux-$ARCH" "$PKG/aven"; else cp "$FROM_DIR/bin/aven" "$PKG/aven"; fi
	for f in "$FROM_DIR"/bin/aven-agent-linux-* "$FROM_DIR"/bin/aven-linux-* "$FROM_DIR"/bin/aven-darwin-* "$FROM_DIR"/bin/aven-windows-*; do
		[ -f "$f" ] && [ ! -L "$f" ] && cp "$f" "$PKG/lib/"
	done
	cp "$FROM_DIR/deploy/aven-server.service" "$FROM_DIR/deploy/aven.env.example" "$PKG/"
	VERSION=$(cd "$FROM_DIR" && git describe --tags --always 2>/dev/null || echo local)
else
	NAME="aven_${VERSION}_linux_${ARCH}"
	BASE="https://github.com/$REPO/releases/download/$VERSION"
	spin "Downloading $NAME.tar.gz" "Downloaded $NAME.tar.gz" gh_curl -o "$TMP/$NAME.tar.gz" "$BASE/$NAME.tar.gz" ||
		die "download failed: $BASE/$NAME.tar.gz"
	gh_curl -o "$TMP/checksums.txt" "$BASE/checksums.txt" || die "checksum download failed"
	want=$(awk -v n="$NAME.tar.gz" '$2 == n || $2 == "*"n { print $1 }' "$TMP/checksums.txt")
	[ -n "$want" ] && [ "$want" = "$(sha256 "$TMP/$NAME.tar.gz")" ] || die "checksum mismatch for $NAME.tar.gz"
	detail "SHA-256 checksum verified"
	tar -xzf "$TMP/$NAME.tar.gz" -C "$TMP"
	cp -R "$TMP/$NAME/." "$PKG/"
fi
for f in aven-server aven aven-server.service aven.env.example; do
	[ -f "$PKG/$f" ] || die "package is missing $f"
done

# ---- upgrade: stop and back up ----

env_get() { sed -n "s/^$1=//p" "$ENV_FILE" | tail -n1; }
DATA=/var/lib/aven
if [ "$UPGRADE" -eq 1 ]; then
	d=$(env_get AVEN_DATA)
	if [ -z "$d" ]; then
		d=$(env_get KEEL_DATA)
		[ -n "$d" ] && warn "KEEL_DATA is set; AVEN_DATA is the new name (the old name still works)"
	fi
	[ -n "$d" ] && DATA=$d
	lib=$(env_get AVEN_AGENT_BIN_DIR)
	if [ -z "$lib" ]; then
		lib=$(env_get KEEL_AGENT_BIN_DIR)
		[ -n "$lib" ] && warn "KEEL_AGENT_BIN_DIR is set; AVEN_AGENT_BIN_DIR is the new name (the old name still works)"
	fi
	[ -n "$lib" ] && LIB_DIR=$lib
	say "Upgrading to $VERSION"
	detail "keeping $ENV_FILE"
	if systemctl is-active --quiet aven-server; then
		systemctl stop aven-server # containers keep running (KillMode=process)
	fi
	for dbname in aven.db keel.db; do
		if [ -f "$DATA/$dbname" ]; then
			mkdir -p "$DATA/backups"
			b="$DATA/backups/${dbname%.db}-pre-upgrade-$(date +%Y%m%d-%H%M%S).db"
			cp -p "$DATA/$dbname" "$b"
			for x in wal shm; do [ -f "$DATA/$dbname-$x" ] && cp -p "$DATA/$dbname-$x" "$b-$x"; done
			detail "database backup: $b"
		fi
	done
	[ -f "$BIN_DIR/aven-server" ] && cp -p "$BIN_DIR/aven-server" "$BIN_DIR/aven-server.prev"
else
	say "Installing $VERSION"
	if [ -d /var/lib/keel ] && [ ! -e /var/lib/aven/aven.db ] && [ ! -e /var/lib/aven/keel.db ]; then
		if [ -f /var/lib/keel/keel.db ] || [ -f /var/lib/keel/aven.db ]; then
			DATA=/var/lib/keel
			warn "/var/lib/keel is in use; /var/lib/aven is the new path (the old directory still works)"
		fi
	fi
fi

# ---- install ----

install -d -m 0755 "$BIN_DIR" "$LIB_DIR"
install -m 0755 "$PKG/aven-server" "$BIN_DIR/aven-server.new" && mv -f "$BIN_DIR/aven-server.new" "$BIN_DIR/aven-server"
install -m 0755 "$PKG/aven" "$BIN_DIR/aven.new" && mv -f "$BIN_DIR/aven.new" "$BIN_DIR/aven"
for f in "$PKG"/lib/*; do
	[ -f "$f" ] && install -m 0755 "$f" "$LIB_DIR/"
done
install -m 0644 "$PKG/aven-server.service" "$UNIT"
if [ "$ENV_FILE" != /etc/aven/aven.env ]; then
	sed -i "s|^EnvironmentFile=.*|EnvironmentFile=$ENV_FILE|" "$UNIT"
fi
install -d -m 0700 "$CONF_DIR"
install -m 0600 "$PKG/aven.env.example" "$CONF_DIR/aven.env.example"
detail "$BIN_DIR/aven-server, $BIN_DIR/aven and downloads in $LIB_DIR"

if [ "$RAILPACK" -eq 1 ] && ! have_railpack; then
	case "$ARCH" in amd64) rp=x86_64 ;; arm64) rp=arm64 ;; esac
	RP="railpack-$RAILPACK_VERSION-$rp-unknown-linux-musl.tar.gz"
	RPBASE="https://github.com/railwayapp/railpack/releases/download/$RAILPACK_VERSION"
	spin "Downloading Railpack $RAILPACK_VERSION" "Downloaded Railpack $RAILPACK_VERSION" \
		curl --proto =https --tlsv1.2 -fsSL --retry 3 -o "$TMP/$RP" "$RPBASE/$RP" || die "Railpack download failed (pass --no-railpack to skip)"
	curl --proto =https --tlsv1.2 -fsSL --retry 3 -o "$TMP/rp-checksums.txt" "$RPBASE/checksums.txt" || die "Railpack checksum download failed"
	want=$(awk -v n="$RP" '$2 == n { print $1 }' "$TMP/rp-checksums.txt")
	[ -n "$want" ] && [ "$want" = "$(sha256 "$TMP/$RP")" ] || die "checksum mismatch for $RP"
	mkdir -p "$TMP/rp" && tar -xzf "$TMP/$RP" -C "$TMP/rp"
	install -m 0755 "$TMP/rp/railpack" "$BIN_DIR/railpack"
	detail "$BIN_DIR/railpack"
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
AVEN_DATA=$DATA
AVEN_LISTEN=$LISTEN_ADDR
AVEN_PUBLIC_URL=$PUBLIC_URL
AVEN_BASE_DOMAIN=$BASE_DOMAIN
AVEN_TLS=auto
AVEN_HTTP_PORT=80
AVEN_HTTPS_PORT=443
AVEN_ACME_EMAIL=$ACME_EMAIL
# Password for the built-in owner account admin@local (at least 8 characters, needed on every start).
AVEN_ADMIN_PASSWORD=$ADMIN_PASSWORD
# GitHub OAuth App (callback: $PUBLIC_URL/api/github/oauth/callback). Optional.
AVEN_GITHUB_CLIENT_ID=
AVEN_GITHUB_CLIENT_SECRET=
AVEN_GITHUB_WEBHOOK_SECRET=$(rand 64)
AVEN_AGENT_BIN_DIR=$LIB_DIR
EOF
	if [ "$LOCAL" -eq 1 ]; then
		cat >> "$ENV_FILE.new" <<EOF
# No domain yet: the dashboard is plain http on $LISTEN_ADDR. Safari drops Secure cookies on http://localhost.
# Remove this line when AVEN_PUBLIC_URL becomes https.
AVEN_INSECURE_COOKIE=1
EOF
	fi
	mv -f "$ENV_FILE.new" "$ENV_FILE"
	chmod 600 "$ENV_FILE"
	say "Wrote $ENV_FILE"
fi

systemctl daemon-reload
if [ "$START" -eq 0 ]; then
	say "Installed. Start with: systemctl enable --now aven-server"
	exit 0
fi
systemctl enable --now aven-server >/dev/null 2>&1 || systemctl enable --now aven-server

LISTEN=$(env_get AVEN_LISTEN)
[ -n "$LISTEN" ] || LISTEN=$(env_get KEEL_LISTEN)
[ -n "$LISTEN" ] || LISTEN='[::1]:7070'
case "$LISTEN" in :*) LISTEN="127.0.0.1$LISTEN" ;; 0.0.0.0:*) LISTEN="127.0.0.1:${LISTEN##*:}" ;; \[::\]:*) LISTEN="[::1]:${LISTEN##*:}" ;; esac
wait_up() {
	i=0
	until curl -fsS -o /dev/null "http://$LISTEN/api/cli/downloads" 2>/dev/null; do
		i=$((i + 1))
		if [ $i -ge 60 ] || ! systemctl is-active --quiet aven-server; then
			return 1
		fi
		sleep 1
	done
}
if ! spin "Starting aven-server on $LISTEN" "aven-server $VERSION is running" wait_up; then
	journalctl -u aven-server -n 30 --no-pager >&2 || true
	die "aven-server did not come up; see: journalctl -u aven-server"
fi

URL=$(env_get AVEN_PUBLIC_URL)
[ -n "$URL" ] || URL=$(env_get KEEL_PUBLIC_URL)
if [ "$UPGRADE" -eq 0 ]; then
	BD=$(env_get AVEN_BASE_DOMAIN)
	[ -n "$BD" ] || BD=$(env_get KEEL_BASE_DOMAIN)
	host=${URL#*://}; host=${host%%/*}; host=${host%%:*}
	if [ -n "$GENERATED" ]; then pw_row=$ADMIN_PASSWORD; else pw_row="the one you chose"; fi
	if [ "$LOCAL" -eq 1 ]; then
		port=${URL##*:}
		next="From another computer, tunnel it first:
  ssh -L $port:localhost:$port root@$(uname -n)
To add a domain later, set AVEN_PUBLIC_URL=https://...
and AVEN_BASE_DOMAIN in $ENV_FILE, then restart."
	else
		next="Point these names at this server (A and/or AAAA):
  $host"
		[ -z "$BD" ] || next="$next
  *.$BD"
	fi
	echo
	box <<EOF
*Aven is running

Dashboard${TAB}$URL
Email${TAB}admin@local
Password${TAB}$pw_row

$next
EOF
	if [ -n "$GENERATED" ]; then
		detail "The password is also in $ENV_FILE as AVEN_ADMIN_PASSWORD."
	fi
	detail "After signing in, Admin > Owner > Setup lists the remaining steps."
	detail "Guide: https://github.com/$REPO/blob/main/docs/self-hosting.md"
fi
