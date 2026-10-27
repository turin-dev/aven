# Self-hosting Aven

[한국어](self-hosting.ko.md)

This guide installs Aven on one Linux server, signs in for the first time, and walks through the first-run checklist. Adding more servers comes at the end.

Aven runs as one server process (`aven-server`) next to Docker. It starts its own reverse proxy container (`aven-proxy`, Caddy) that owns ports 80 and 443, issues TLS certificates and routes traffic to your services. Build images with [Railpack](https://github.com/railwayapp/railpack) or from a Dockerfile.

> Settings use the `AVEN_*` names (`/etc/aven/aven.env`, data in `/var/lib/aven`, proxy container `aven-proxy`). Existing installs can keep `KEEL_*`, `/etc/keel`, `/var/lib/keel`, and a running `keel-proxy`: Aven reads the new name first and falls back to the old one.

## 1. Requirements

- A Linux server (amd64 or arm64) with a public IPv4 and/or IPv6 address. 2 vCPU and 4 GB RAM is a comfortable start.
- A recent Docker Engine, running as a systemd service.
- Ports 80 and 443 free on the host (Aven's proxy binds them).
- A domain you control, for example `example.com`:
  - `aven.example.com` points at the server (dashboard and API).
  - `*.example.com` (or `*.apps.example.com`) points at the server, so services get automatic hostnames.
- To build from source: Go 1.26+, Node.js 20.9+ with npm, `make`.
- [Railpack](https://github.com/railwayapp/railpack/releases) on the server, to build services from source code without a Dockerfile.

## 2. DNS

Create these records before you start, so the proxy can get certificates on first request:

| Name | Type | Value |
| --- | --- | --- |
| `aven.example.com` | A / AAAA | the server's public IPv4 / IPv6 |
| `*.example.com` | A / AAAA | the server's public IPv4 / IPv6 |

Use whatever name you like for the dashboard; it only has to match `AVEN_PUBLIC_URL`. The wildcard has to match `AVEN_BASE_DOMAIN`.

## Install with the script

On a fresh server with DNS in place, one command installs the latest release:

```sh
curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh \
  | sudo sh -s -- --public-url https://aven.example.com --acme-email you@example.com
```

The script checks the system (root, systemd, amd64/arm64, Docker, ports 80/443), downloads the release for your architecture from GitHub Releases and verifies its SHA-256 checksum. It installs `aven-server`, the `aven` CLI, the agent and CLI downloads, and Railpack. It writes `/etc/aven/aven.env` with a generated admin password and webhook secret, installs and starts the systemd unit, waits until the server answers, and prints the sign-in details and the DNS names to create. Then continue with [First sign-in](#7-first-sign-in-and-onboarding).

| Option | Meaning |
| --- | --- |
| `--public-url URL` | Dashboard URL, `https://` (prompted on a terminal if omitted) |
| `--base-domain DOMAIN` | Wildcard domain for services (default: parent domain of the public URL) |
| `--acme-email EMAIL` | Let's Encrypt contact (optional) |
| `--admin-password PW` | Use this password for `admin@local` instead of generating one |
| `--version TAG` | Install a specific release instead of the latest |
| `--from-dir DIR` | Install from a local checkout built with `make all` |
| `--install-docker` | Install Docker with get.docker.com if it is missing |
| `--no-railpack`, `--no-start`, `--yes` | Skip Railpack, don't start the service, never prompt |

Run the same command again to **upgrade**: the script keeps `/etc/aven/aven.env`, copies the database to `/var/lib/aven/backups/`, keeps the old binary as `/usr/local/bin/aven-server.prev`, and restarts the server. Running services stay up.

To install by hand instead, follow steps 3 to 6.

## 3. Build

```sh
git clone https://github.com/turin-dev/aven.git
cd aven
make all        # web UI, aven-server, aven-agent (amd64, arm64), CLI for every platform
```

Binaries land in `bin/`.

## 4. Install

```sh
sudo install -d -m 0755 /usr/local/lib/aven /etc/aven
sudo install -m 0755 bin/aven-server /usr/local/bin/aven-server
sudo install -m 0755 bin/aven-linux-amd64 /usr/local/bin/aven             # or aven-linux-arm64
# The server serves these to new nodes and to the dashboard's CLI download page.
sudo install -m 0755 bin/aven-agent-linux-amd64 bin/aven-agent-linux-arm64 /usr/local/lib/aven/
sudo install -m 0755 bin/aven-linux-* bin/aven-darwin-* bin/aven-windows-*.exe /usr/local/lib/aven/
# Railpack (pick the latest release for your architecture)
sudo install -m 0755 /path/to/railpack /usr/local/bin/railpack

sudo install -m 0644 deploy/aven-server.service /etc/systemd/system/aven-server.service
sudo install -m 0600 deploy/aven.env.example /etc/aven/aven.env
```

## 5. Configure

Edit `/etc/aven/aven.env` (root, mode 0600). The minimum:

```sh
AVEN_DATA=/var/lib/aven
AVEN_LISTEN=127.0.0.1:7070
AVEN_PUBLIC_URL=https://aven.example.com
AVEN_BASE_DOMAIN=example.com
AVEN_ACME_EMAIL=you@example.com
# Password for the built-in owner account admin@local. At least 8 characters; required on every start.
AVEN_ADMIN_PASSWORD=change-me-to-a-long-random-password
```

Generate a strong password with `openssl rand -base64 24`.

When `AVEN_PUBLIC_URL` is an `https://` URL, the proxy routes that hostname to `aven-server` itself, so you don't need a separate reverse proxy. If you already run one on 80/443, set `AVEN_PROXY_BIND=127.0.0.1`, move `AVEN_HTTP_PORT`/`AVEN_HTTPS_PORT` to other ports, set `AVEN_TLS=off`, and forward to them.

Optional settings, each also listed in the first-run checklist:

| Feature | Variables |
| --- | --- |
| GitHub repositories and push-to-deploy | `AVEN_GITHUB_CLIENT_ID`, `AVEN_GITHUB_CLIENT_SECRET`, `AVEN_GITHUB_WEBHOOK_SECRET` |
| Email (invites, password reset, alerts) | `AVEN_SMTP_HOST`, `AVEN_SMTP_PORT`, `AVEN_SMTP_USERNAME`, `AVEN_SMTP_PASSWORD`, `AVEN_SMTP_FROM`, `AVEN_SMTP_TLS` |
| Extra server owners by email | `AVEN_SUPERADMINS` |
| Private network between nodes | `AVEN_PRIVATE_ADDRESS`, `AVEN_MESH` |

For GitHub, create an OAuth App under *GitHub → Settings → Developer settings → OAuth Apps* with:

- Homepage URL: `https://aven.example.com`
- Authorization callback URL: `https://aven.example.com/api/github/oauth/callback`

Generate the webhook secret with `openssl rand -hex 32`. Repository webhooks point at `https://aven.example.com/api/webhooks/github`; once GitHub is connected, Aven can create them for you.

## 6. Start

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now aven-server
journalctl -u aven-server -f
```

On start the server checks Docker, creates its network and starts `keel-proxy`. Errors such as a missing `AVEN_ADMIN_PASSWORD` or an unreachable Docker daemon are printed to the journal and the service exits.

## 7. First sign-in and onboarding

1. Open `https://aven.example.com` and sign in with **email** `admin@local` (or leave it empty) and the `AVEN_ADMIN_PASSWORD`.
2. Aven shows the **first-run setup** prompt to server owners until the required items are done. You can open it any time at **Admin → Owner · 초기 설정** (`/admin/?section=setup`). Each item is checked automatically from the server's settings:

   | Item | Required | How to finish it |
   | --- | --- | --- |
   | Public URL | yes | `AVEN_PUBLIC_URL` is an `https://` URL |
   | Base domain | yes | `AVEN_BASE_DOMAIN` is set and its wildcard DNS points here |
   | Personal owner account | yes | Invite yourself under *Admin → Users*, accept, then make the account an owner under *Admin → Owner · 권한* |
   | Two-factor authentication | yes | Add a TOTP app or a passkey under *Account → Security* |
   | First project | yes | Create a project and deploy a service |
   | GitHub | no | OAuth App and webhook secret (see above) |
   | Email (SMTP) | no | `AVEN_SMTP_*` |
   | Backup storage | no | Add an S3-compatible destination under a database service's *Settings → DB backups* |
   | Additional nodes | no | See [Adding servers](#9-adding-servers) |

3. After you have a personal owner account, use it day to day and keep `admin@local` as a break-glass account with a long password.

Settings that come from environment variables take effect after `sudo systemctl restart aven-server`. Running services keep running while the server restarts.

## 8. Backups and upgrades

Everything Aven knows (users, projects, variables, encrypted secrets) lives in `AVEN_DATA`:

- `keel.db`: SQLite database
- `master.key`: key that encrypts secrets in the database. **Without it, a database backup cannot be decrypted.**
- `session.key`, `logs/`, local backups

Back up the whole directory. *Admin → Owner · DB 백업* creates a consistent copy of the database you can download. For a manual copy:

```sh
sudo sqlite3 /var/lib/aven/keel.db ".backup '/var/lib/aven/backups/keel-$(date +%F).db'"
sudo cp /var/lib/aven/master.key /secure/place/
```

To upgrade, build the new revision, back up, replace the binaries and restart:

```sh
git pull && make all
sudo cp /usr/local/bin/aven-server /usr/local/bin/aven-server.prev
sudo install -m 0755 bin/aven-server /usr/local/bin/aven-server
sudo install -m 0755 bin/aven-agent-linux-* bin/aven-linux-* bin/aven-darwin-* bin/aven-windows-*.exe /usr/local/lib/aven/
sudo systemctl restart aven-server
```

Database migrations run automatically on start. To roll back, stop the server, restore the previous binary and the database backup, and start again.

Instead of upgrading by hand you can let Aven deploy itself: see [Aven on Aven](selfhost.md).

## 9. Adding servers

Other servers run `aven-agent`, which connects back to `aven-server` over HTTPS. In *Admin → 서버 노드 → 노드 추가* create a join token, then on the new server (with Docker installed):

```sh
curl -fsSL https://aven.example.com/install-agent.sh | sudo sh -s -- --server https://aven.example.com --token <TOKEN>
```

The token is single-use and expires. The agent creates its own key and receives a certificate from Aven's internal CA. Re-running the same command later upgrades the agent and keeps its credentials.

If your servers share a private network (WireGuard, Tailscale), set `AVEN_PRIVATE_ADDRESS` on each so services on different nodes can reach each other over it.

## Troubleshooting

- **`AVEN_ADMIN_PASSWORD(8자 이상)가 필요합니다`**: set a password of at least 8 characters in `/etc/aven/aven.env`.
- **Docker errors on start**: `systemctl status docker`, and make sure `aven-server` runs as root (the unit does).
- **Certificates are not issued**: DNS must point at this server and ports 80/443 must be reachable from the internet. `docker logs keel-proxy` shows ACME errors.
- **Builds fail with `railpack을 찾을 수 없습니다`**: install Railpack to `/usr/local/bin/railpack` or set `AVEN_RAILPACK_BIN`.
- **Signing in over plain HTTP on a LAN** (evaluation only): set `AVEN_INSECURE_COOKIE=1`; session cookies are `Secure` by default.
