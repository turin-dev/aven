# Self-hosting Aven

[한국어](self-hosting.ko.md) · [Native Windows host](windows-native.md)

This guide installs Aven on one Linux server, signs in for the first time, and walks through the first-run checklist. Adding more servers comes at the end.

Aven runs as one server process (`aven-server`) next to Docker. It starts its own reverse proxy container (`aven-proxy`, Caddy) that owns ports 80 and 443, issues TLS certificates and routes traffic to your services. Build images with [Railpack](https://github.com/railwayapp/railpack) or from a Dockerfile.

> Settings use the `AVEN_*` names (`/etc/aven/aven.env`, data in `/var/lib/aven`, proxy container `aven-proxy`). Existing installs can keep `KEEL_*`, `/etc/keel`, `/var/lib/keel`, and a running `keel-proxy`: Aven reads the new name first and falls back to the old one.

## 1. Requirements

The installer checks the items it can see. It does not measure memory, disk, or a Docker version number.

- Linux, amd64 (`x86_64`) or arm64 (`aarch64`), with systemd (`systemctl` and `/run/systemd/system`). `curl`, `tar`, and `sha256sum` or `openssl`.
- Docker: the `docker` command, and `docker info` succeeding for root. `aven-server` runs as root because `deploy/aven-server.service` sets no `User=`. The proxy image is `caddy:2-alpine`. If Docker is missing, the installer can run get.docker.com (`--install-docker`, or confirm when it asks).
- Memory and disk are not checked. `aven-server` is one process, the proxy is one container, and each app or database is another container. Database services use a Docker volume. The control plane (`aven.db`, `master.key`, `session.key`, `logs/`) is under `AVEN_DATA`, `/var/lib/aven` on a new install. Size the disk for the images and volumes you will run.
- Ports 80/tcp, 443/tcp, and 443/udp free on the host. The proxy publishes all three. 443/udp is HTTP/3. The API listens on `127.0.0.1:7070` unless `AVEN_LISTEN` says otherwise, and that port is not published.
- A public IPv4 and/or IPv6 address, and a domain you control, when you want automatic TLS. See [DNS](#2-dns). Without a domain the dashboard can stay on `http://localhost:7070` of that machine.
- To build from source: Go 1.26+, Node.js 20.9+ with npm, `make`.
- [Railpack](https://github.com/railwayapp/railpack/releases) on the server, to build services that have no Dockerfile. The installer can place v0.42.0 at `/usr/local/bin/railpack`. `aven-server` uses `AVEN_RAILPACK_BIN` when it is set, otherwise `railpack` on `PATH` or `/usr/local/bin/railpack`. It does not download Railpack. `aven-agent` downloads the pinned release on first use when its setting is `auto`, which is the agent default.

## 2. DNS

Create these records before the first request that needs a certificate. The installer does not wait for one.

| Name | Type | Value |
| --- | --- | --- |
| `aven.example.com` | A and/or AAAA | the server's public IPv4 and/or IPv6 |
| `*.example.com` | A and/or AAAA | the same address |

Use whatever name you like for the dashboard. It has to match `AVEN_PUBLIC_URL`. The wildcard has to match `AVEN_BASE_DOMAIN`.

TLS for a wildcard base domain is on-demand. Aven does not ask Let's Encrypt for a certificate covering `*.example.com` itself (that would be a DNS-01 challenge, which this tree does not run). Each concrete hostname, such as `web-production.example.com`, gets its own certificate on the first TLS handshake, over HTTP-01 or TLS-ALPN. Ports 80 and 443 must be reachable from the public internet for that. An AAAA record is useful only when this host answers on that IPv6 address. A record that points at an address the host does not serve makes clients that prefer IPv6 fail while IPv4 still works.

You pay for the machine and for the domain. This repository does not charge a usage fee.

## Install with the script

Read the script before you run it. It is the file that changes the machine.

```sh
curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh -o install-server.sh
less install-server.sh
sh install-server.sh --help
sh install-server.sh
```

`sh install-server.sh --help` prints the option list when the script is on disk. Piped into `sh` with no file, `--help` prints a one-line pointer at the copy in the repository, because the script body is not a file the shell can show.

Run it as a normal user: the script runs itself again with `sudo`. Piped into `sudo sh` instead, sudo-rs (the default sudo since Ubuntu 25.10) keeps it in the background of the terminal, where it cannot ask questions, so it stops unless `--yes` is given.

On a terminal the script walks you through the first install: it asks for the dashboard URL (Enter without one installs on `http://localhost:7070`), the base domain for generated service names, an optional Let's Encrypt email and the `admin@local` password (Enter generates one), offers to install Docker and Railpack when they are missing, then shows a summary and changes nothing until you confirm. Invalid answers are asked again. Upgrades ask once. For unattended installs pass the settings as options and add `--yes`:

```sh
curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh \
  | sudo sh -s -- --yes --public-url https://aven.example.com --acme-email you@example.com
```

The script checks root, systemd, amd64/arm64, `curl`, `tar`, a checksum tool, and Docker. Ports 80 and 443 are only a warning: `ss` looks for a TCP listener, and the script continues. It downloads the release for your architecture from GitHub Releases and checks the SHA-256 in `checksums.txt`. It installs `aven-server`, the `aven` CLI, the agent and CLI downloads, and Railpack unless you skip it. It writes `/etc/aven/aven.env` with a generated admin password and webhook secret, installs and starts the systemd unit, and waits until `http://127.0.0.1:7070/api/cli/downloads` answers (up to about 60 seconds). Then it prints the sign-in details and the DNS names. Continue with [Check that it started](#check-that-it-started) and [First sign-in](#7-first-sign-in-and-onboarding).

### What the installer changes

Main server, first install:

| Path | What it is |
| --- | --- |
| `/usr/local/bin/aven-server` | the server binary |
| `/usr/local/bin/aven` | the CLI |
| `/usr/local/bin/railpack` | Railpack, when it was missing and you did not pass `--no-railpack` |
| `/usr/local/lib/aven/` | `aven-agent` and CLI downloads the server hands out |
| `/etc/systemd/system/aven-server.service` | systemd unit. `EnvironmentFile=/etc/aven/aven.env`, `KillMode=process` so stopping the server leaves containers running |
| `/etc/aven/aven.env` | settings, mode 0600. Created only on a first install; an upgrade keeps it |
| `/etc/aven/aven.env.example` | the commented example from the release |
| `/var/lib/aven/` | created when the service starts (`StateDirectory=aven` and `AVEN_DATA`). Holds `aven.db`, `master.key`, `session.key`, `logs/` |

The first start, not the shell script itself, also creates the Docker network `aven` and the container `aven-proxy` (`caddy:2-alpine`, restart `unless-stopped`) with volumes `aven-caddy-data`, `aven-caddy-config`, and `aven-proxy-wake`.

An upgrade (`/etc/aven/aven.env` already exists, or an older `/etc/keel/aven.env` / `/etc/keel/keel.env`) keeps that env file, stops `aven-server`, copies `aven.db` and `keel.db` when those files exist to `/var/lib/aven/backups/<name>-pre-upgrade-<timestamp>.db` (and the `-wal` / `-shm` files beside them), and copies the old binary to `/usr/local/bin/aven-server.prev`. It does not copy `master.key`. Running app containers stay up because the unit's `KillMode` is `process`.

A region server (`--role region`, or `--server` / `--token`) does not install `aven-server`. It downloads `install-agent.sh` from the main server and that script writes:

| Path | What it is |
| --- | --- |
| `/usr/local/bin/aven-agent` | the agent binary |
| `/etc/keel-agent/agent.env` | mode 0600. `AVEN_SERVER_URL`, and `AVEN_JOIN_TOKEN` only until enrollment finishes. Extra settings are stored as `AVEN_*` |
| `/var/lib/keel-agent/credentials.json` | the node key and certificate, mode 0600 |
| `/etc/systemd/system/keel-agent.service` | the unit. The name is still `keel-agent` so a re-run replaces the unit an existing agent is using |

Those agent paths are what a new install creates today. `AVEN_*` is the name to set. `KEEL_*` in an older env file is still read.

If `keel-server.service` is active, the script stops and tells you not to run both against one data directory. If a container labeled `aven.self=server` or `keel.self=server` is running, the script stops and tells you to upgrade that container from the dashboard instead.

<a id="check-that-it-started"></a>

### Check that it started

```sh
systemctl is-active aven-server
curl -fsS -o /dev/null http://127.0.0.1:7070/api/cli/downloads
docker ps --filter name=aven-proxy --format '{{.Names}} {{.Status}}'
journalctl -u aven-server -n 50 --no-pager
```

`systemctl is-active` prints `active`. The `curl` is the same request the installer waits for. `docker ps` should show `aven-proxy` as `Up`. Then sign in.

| Option | Meaning |
| --- | --- |
| `--public-url URL` | Dashboard URL, `https://` (asked on a terminal if omitted). Without a domain the dashboard is `http://localhost:7070` on the server only; open it through `ssh -L 7070:localhost:7070` and add a domain later |
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

On start the server checks Docker, creates its network, and starts `aven-proxy`. An install that already has a running `keel-proxy` keeps that container. A missing password, an unreachable Docker daemon, or ports 80/443 already taken are printed to the journal and the service exits. The password error is `AVEN_ADMIN_PASSWORD (or KEEL_ADMIN_PASSWORD) must be set to at least 8 characters`. The proxy error includes `start the proxy:` and `ports AVEN_HTTP_PORT/AVEN_HTTPS_PORT, default 80/443, must be free`. The Docker error includes `docker is not reachable` and asks whether this user may use the socket.

## 7. First sign-in and onboarding

1. Open `https://aven.example.com` and sign in with **email** `admin@local` (or leave it empty) and the `AVEN_ADMIN_PASSWORD`.
2. Aven shows the **first-run setup** prompt to server owners until the required items are done. You can open it any time at **Admin → Owner · Setup** (`/admin/?section=setup`). The Korean UI calls that item **어드민 → Owner · 초기 설정**. Each item is checked automatically from the server's settings:

   | Item | Required | How to finish it |
   | --- | --- | --- |
   | Public URL | yes | `AVEN_PUBLIC_URL` is an `https://` URL |
   | Base domain | yes | `AVEN_BASE_DOMAIN` is set and its wildcard DNS points here |
   | Personal owner account | yes | Invite yourself under *Admin → Users* (Korean: *어드민 → 사용자·초대*), accept, then make the account an owner under *Admin → Owner · Permissions* (Korean: *Owner · 권한*) |
   | Two-factor authentication | yes | Add a TOTP app or a passkey under *Account → Security* |
   | First project | yes | Create a project and deploy a service |
   | GitHub | no | OAuth App and webhook secret (see above) |
   | Email (SMTP) | no | `AVEN_SMTP_*` |
   | Backup storage | no | Add an S3-compatible destination under a database service's *Settings → DB backups* (Korean: *설정 → DB 백업*) |
   | Additional nodes | no | See [Adding servers](#9-adding-servers). The nav item is *Admin → Server nodes* (Korean: *서버 노드*) |

3. After you have a personal owner account, use it day to day and keep `admin@local` as a break-glass account with a long password.

Settings that come from environment variables take effect after `sudo systemctl restart aven-server`. Running services keep running while the server restarts.

## 8. Backups and upgrades

Everything Aven knows about users, projects, variables, and encrypted secrets lives in `AVEN_DATA` (`/var/lib/aven` on a new install, `/var/lib/keel` when that directory is the one still in use):

- `aven.db`: the SQLite database on a new install. An older file named `keel.db` is opened instead when `aven.db` is not beside it. Do not create a second empty `aven.db` next to a live `keel.db` and expect the old data to move.
- `master.key`: the key that encrypts secrets in that database. **Without the same key, a database backup cannot be decrypted.**
- `session.key`, `logs/`, and files under `backups/`

Copy the whole directory, and keep `master.key` with the database. *Admin → Owner · DB backups* (Korean: *Owner · DB 백업*) writes a consistent copy with SQLite `VACUUM INTO` while the server is running, and you can download it. A manual copy of a **running** database should use the SQLite backup API, not `cp`:

```sh
sudo mkdir -p /var/lib/aven/backups
sudo sqlite3 /var/lib/aven/aven.db ".backup '/var/lib/aven/backups/aven-$(date +%F).db'"
sudo cp -p /var/lib/aven/master.key /secure/place/master.key
```

If `ls /var/lib/aven/aven.db` fails and `keel.db` is there, back that file up under its own name. Stop the server first if `sqlite3` is not installed: `sudo systemctl stop aven-server`, copy `aven.db` (and `aven.db-wal` / `aven.db-shm` if they exist), copy `master.key`, then start it again.

**Restore the control plane.** Stop `aven-server`. Replace `aven.db` with the backup. Remove `aven.db-wal` and `aven.db-shm` so SQLite does not replay a log from a different file. Put back the `master.key` that belongs to that backup. Start `aven-server`. App containers keep running across this stop (`KillMode=process`), but the dashboard is down until the server is up. This restores users, projects, and settings. It does not restore the bytes inside a Postgres or MySQL volume.

**Database service backups.** A database service can take logical dumps to a local file or to an S3-compatible destination you add under *Settings → DB backups*. Restore writes into a database service of the same engine in the same project and replaces that service's current data. Nothing in the server turns this on by itself.

**Point-in-time recovery.** `aven pitr` (and *Settings* on a Postgres or MySQL service) archives Postgres WAL or MySQL binlogs to the same kind of backup storage, or locally when no destination is set. Enabling Postgres may restart that database once. `aven pitr restore --at <time>` restores to a new service named `<name>-restored-...` unless you pass `--in-place`, which overwrites the service. The time is RFC3339, a local `YYYY-MM-DD HH:MM[:SS]`, or a duration ago (`30m`, `2h`, `1d`, `1w`). The command reference is [docs/cli.md](cli.md). Redis and MongoDB are not covered by that PITR path.

**Upgrade with the installer.** Run `install-server.sh` again. It keeps the env file, stops `aven-server`, copies the database files as described in [What the installer changes](#what-the-installer-changes), replaces the binaries, and starts the server. Migrations run on start. Read the release notes first. Copy `master.key` yourself before you start. Running app containers stay up. Agents on other machines are not upgraded by the server installer. Re-run the agent install from the main server's `install-agent.sh` (the same command, without a token once the node has enrolled) after the server is up.

**Upgrade from a checkout.**

```sh
git pull && make all
sudo cp -p /usr/local/bin/aven-server /usr/local/bin/aven-server.prev
sudo systemctl stop aven-server
sudo mkdir -p /var/lib/aven/backups
sudo cp -p /var/lib/aven/aven.db "/var/lib/aven/backups/aven-pre-upgrade-$(date +%Y%m%d-%H%M%S).db"
sudo install -m 0755 bin/aven-server /usr/local/bin/aven-server
sudo install -m 0755 bin/aven-agent-linux-* bin/aven-linux-* bin/aven-darwin-* bin/aven-windows-*.exe /usr/local/lib/aven/
sudo systemctl start aven-server
```

Copy `keel.db` the same way when that is the file in use. Copy the `-wal` and `-shm` siblings when they exist.

**Roll an upgrade back.** Stop `aven-server`. Copy `/usr/local/bin/aven-server.prev` back to `/usr/local/bin/aven-server`. Restore the database files from the `pre-upgrade` copy, and remove any `-wal` / `-shm` that were not part of that copy. Start the server. The old binary may refuse a database a newer migration already rewrote, so restore the database together with the binary.

If `aven-server` is the Docker container from [Aven on Aven](selfhost.md) (label `aven.self=server`), the installer refuses to continue. Upgrade that container by deploying the `aven-server` service. Do not run the systemd unit and that container against the same data directory.

## 9. Adding servers

Other servers run `aven-agent`, which connects out to `aven-server` over HTTPS. The agent does not listen for the dashboard. In *Admin → Server nodes → Add node* (Korean: *어드민 → 서버 노드 → 노드 추가*) create a join token, then on the new server (with Docker installed):

```sh
curl -fsSL https://aven.example.com/install-agent.sh | sudo sh -s -- --server https://aven.example.com --token <TOKEN>
```

Read `https://aven.example.com/install-agent.sh` in a browser or with `curl` before you pipe it to a shell.

The server installer can do the same. Run `install-server.sh` on the new server and answer **Region server** to "What is this server?". It asks for the main server URL, the join token, and an optional private address, installs Docker if you want, and then runs the main server's `install-agent.sh`. Unattended:

```sh
curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh | sudo sh -s -- --yes --server https://aven.example.com --token <TOKEN>
```

The token is single-use. The default lifetime is 1 hour. The API accepts `ttl_s` from 60 to 86400. The agent creates its own key and receives a certificate from Aven's internal CA. The token line is removed from `/etc/keel-agent/agent.env` after `credentials.json` appears. The installer waits up to 60 seconds for that file, then checks that `keel-agent` is still active and has not restarted. Re-running the same command later upgrades the binary and keeps the credential. To join a different main server, run `install-agent.sh` with `--reenroll` and a new token.

`AVEN_PUBLIC_URL` on the main server has to be `https://` before a token can be created. Otherwise the API returns 503 and names `AVEN_PUBLIC_URL`. The agent does not follow HTTP redirects during enrollment, so the URL has to be the one that serves `/api/agent/enroll` directly.

If the servers share a private network (WireGuard, Tailscale), set `AVEN_PRIVATE_ADDRESS` to that address on each one, including the main server's `/etc/aven/aven.env`. The region installer writes `AVEN_PRIVATE_ADDRESS` into `/etc/keel-agent/agent.env` when you give it one. The mesh gateway then listens on that address only, port 7443 (`AVEN_MESH_PORT`).

<a id="uninstall-and-roll-back"></a>

## Uninstall and roll back

There is no uninstall command. This removes a main server the installer created. It does not delete volumes that belong to your apps. Do that only when you mean to delete their data.

```sh
sudo systemctl disable --now aven-server
sudo docker rm -f aven-proxy
sudo rm -f /usr/local/bin/aven-server /usr/local/bin/aven-server.prev /usr/local/bin/aven /etc/systemd/system/aven-server.service
sudo systemctl daemon-reload
```

`/etc/aven/aven.env` contains the admin password and the GitHub webhook secret. `/var/lib/aven` contains `aven.db` and `master.key`. Delete those two directories only after you have a copy you are willing to lose, or when you are sure you do not want the control plane back. The proxy volumes `aven-caddy-data`, `aven-caddy-config`, and `aven-proxy-wake` hold certificates and the proxy's sockets. `docker volume rm` of those three drops the certificates. The Docker network `aven` can be removed with `docker network rm aven` after the containers on it are gone. App containers and their volumes are labeled for Aven and are not removed by the commands above.

A region server:

```sh
sudo systemctl disable --now keel-agent
sudo rm -f /usr/local/bin/aven-agent /etc/systemd/system/keel-agent.service
sudo rm -rf /etc/keel-agent /var/lib/keel-agent
sudo systemctl daemon-reload
```

That node's `aven-proxy`, if the agent started one, is a separate container. `docker rm -f aven-proxy` removes it. Containers the agent started for your services stay until you remove them.

Rolling back an upgrade, without uninstalling, is [Roll an upgrade back](#8-backups-and-upgrades).

<a id="troubleshooting"></a>

## Troubleshooting

- **Port 80 or 443 is in use.** The installer prints `Port 80 is in use` or `Port 443 is in use` (or both) and continues. `aven-server` then exits. `journalctl -u aven-server` shows `start the proxy:` and `ports AVEN_HTTP_PORT/AVEN_HTTPS_PORT, default 80/443, must be free`. `ss -Htlpn 'sport = :80'` and the same for 443 show the listener. Free the ports, or set `AVEN_HTTP_PORT` and `AVEN_HTTPS_PORT` to a free pair and `AVEN_PROXY_BIND=127.0.0.1` when another proxy on the host owns 80/443. If that other proxy terminates TLS, set `AVEN_TLS=off` and forward to the ports you chose. Restart `aven-server`.
- **DNS has not propagated, or a certificate is not issued.** Install does not request a certificate. If `getent` cannot resolve the dashboard host, the installer warns: `<host> does not resolve yet`. After DNS points at this server, the first HTTPS request for a concrete name triggers issuance. `docker logs aven-proxy` shows ACME errors. Use `docker logs keel-proxy` only when that container is the one running. Ports 80 and 443 have to be reachable from the internet, not only from the host. A name that resolves to the wrong address, or an AAAA that this host does not answer, fails the challenge for clients that use that address. There is no wildcard certificate for `*.example.com` itself.
- **Docker permission or a stopped daemon.** The installer runs `docker info` as root. A permission failure says `cannot use its socket` and includes the `docker info` text. A stopped daemon says `not running` and the same text. `systemctl status docker` distinguishes "inactive" from a socket error. The unit runs `aven-server` as root. Starting the binary yourself as another user fails with `docker is not reachable` and `may this user use its socket?` unless that user can open the Docker socket. Rootless Docker is not what the unit file sets up.
- **Firewall or IPv6.** Publishing a port with Docker does not pass through ufw's INPUT chain. The env example says the same about TCP-proxy ports: a rule has to go in the `DOCKER-USER` chain if you want the host firewall to see it. A cloud security group in front of the machine is separate and does block 80/443. The default publish is `80:80` and `443:443` plus `443/udp`, on every interface Docker binds. If the host has no working public IPv6, do not advertise an AAAA for it. Per-domain IPv6 addresses are optional (`AVEN_IPV6_POOL`) and are not required for the first certificate.
- **The agent does not connect.** On the new machine, `systemctl status keel-agent` and `journalctl -u keel-agent`. The installer exits with `the agent did not join within 60s` when `/var/lib/keel-agent/credentials.json` never appears, and says the token may be invalid or expired. The agent log shows `enrollment HTTP 401`. The API body for that status is `invalid join token` (the token is not 64 characters) or `invalid or expired join token`. The agent records the status code, not that body. Create a new token. The main server must be reachable from the agent at the `https://` URL you passed (`curl -fsSI https://aven.example.com/install-agent.sh` from that machine). A redirect is not followed. Docker on the agent machine has to answer `docker info`. After enrollment, a later disconnect is logged as `disconnected from server; containers keep running` and the agent retries. The node is shown offline until that WebSocket is back. No inbound port is required on the agent machine for this connection.
- **`AVEN_ADMIN_PASSWORD (or KEEL_ADMIN_PASSWORD) must be set to at least 8 characters`:** set that in `/etc/aven/aven.env` and restart. The installer rejects a password shorter than 8 characters, and rejects tabs, line breaks, quotes, backslashes, `$`, backticks, and spaces, because the value is one line of the env file.
- **Builds fail with `railpack을 찾을 수 없습니다 (AVEN_RAILPACK_BIN (or KEEL_RAILPACK_BIN))`:** install Railpack to `/usr/local/bin/railpack` or set `AVEN_RAILPACK_BIN`.
- **Signing in over plain HTTP on a LAN** (evaluation only): set `AVEN_INSECURE_COOKIE=1`. Session cookies are `Secure` by default. The installer sets this itself when the dashboard URL is `http://localhost`.

## Known limitations

These match the code and the [feature stability](../README.md#feature-stability) table in the README.

- The control plane is one SQLite database on the main server. There is no second control-plane replica in this tree.
- This tree has no git receive-pack server. Push deploys are GitHub webhooks.
- App sleeping, cron schedules, the web dashboard, Cloudflare Tunnel ingress, and the AI assistant are experimental. The table says why. The dashboard is the UI you install. It has no component or end-to-end tests under `web/`.
- Logical backups and PITR do not run until you turn them on. PITR here is Postgres WAL and MySQL binlog, not every database.
- The agent unit and directories are still named `keel-agent` (`/etc/keel-agent`, `/var/lib/keel-agent`) on a new install. The settings inside the env file are `AVEN_*`.
- The installer does not check memory, disk, Docker's version, or that the DNS name points at this machine. It warns when the name does not resolve at all.
- A fresh install does not issue a certificate during install. TLS happens on the first handshake for each hostname, and only if 80 and 443 are reachable.
- `aven-server` on Aven (the self-upgrade container) is privileged and mounts the Docker socket. That path is documented in [Aven on Aven](selfhost.md), and it is optional.
