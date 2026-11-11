# Aven: a self-hosted, Railway-style PaaS

English | [한국어](README.ko.md)

**Use it like Railway, but on your own server, with no usage-based pricing.**

[![Release](https://img.shields.io/github/v/release/turin-dev/aven)](https://github.com/turin-dev/aven/releases)
[![CI](https://github.com/turin-dev/aven/actions/workflows/ci.yml/badge.svg)](https://github.com/turin-dev/aven/actions/workflows/ci.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE)

[Quick start](#quick-start) · [Comparison](#comparison) · [Feature stability](#feature-stability) · [Highlights](#highlights) · [How it works](#how-it-works) · [Docs](#documentation) · [FAQ](#faq)

Railway, Coolify, and Dokploy are trademarks of their owners. Aven is not affiliated with them. Those names appear here only to describe what Aven is like and how it compares.

> [!IMPORTANT]
> Aven is an independent project and this repository contains no Railway source code. The canvas and service workflow follow Railway's, and Aven reads `railway.json` / `railway.toml` and answers Railway-shaped GraphQL queries so a project can move over. See [Relationship to Railway](#relationship-to-railway).

- Run it on your own servers. One `aven-server` process sits next to Docker; more machines join with `aven-agent`. There is no Kubernetes cluster to operate.
- The software in this repository has no usage fees and no paid feature tier. You pay for the machines you run.
- The license is [AGPL-3.0-only](LICENSE). SAML SSO and passkeys ship in that same core.
- The canvas, `railway.json`, and a Railway-shaped GraphQL API follow a Railway-style workflow.
- GitHub push deploys, pull-request preview environments, and one-click Postgres, MySQL, Redis, and MongoDB are included.

![Demo: open the api service, add a variable, deploy it, and follow the build log until the new deployment is active](docs/assets/demo.gif)

> [!NOTE]
> Aven is young and moves quickly. Interfaces and settings can change between releases, so read the release notes and back up the data directory before upgrading. The [stability labels](#feature-stability) below say what the tests in this tree cover. They are not a support commitment.

## Quick start

The Linux installer is [`install-server.sh`](install-server.sh). One command starts the install. It still needs a machine you operate, Docker, and, for automatic TLS, a domain whose DNS you control. You pay for the server and for the domain. This repository does not charge a usage fee.

A native Windows service on Windows 10/11 is a different path: [Windows host guide](docs/windows-native.md). Windows Server editions are not supported by Docker Desktop. The steps below are the Linux path.

### What you need

The installer checks the first list. It does not measure memory, disk, or a Docker version.

- **Machine.** Linux, amd64 (`x86_64`) or arm64 (`aarch64`), with systemd (`systemctl`, and `/run/systemd/system`). `curl`, `tar`, and `sha256sum` or `openssl`.
- **Docker.** The `docker` command, and a daemon that answers `docker info` for root. The unit file does not set `User=`, so `aven-server` runs as root. If Docker is missing, the script can install it from get.docker.com when you confirm or pass `--install-docker`. The proxy image it starts is `caddy:2-alpine`.
- **Memory and disk.** Not checked. `aven-server` is one process. The proxy is one container. Each app and database is another container and, for databases, a volume. The control-plane files (`aven.db`, `master.key`) live under `/var/lib/aven`. Leave disk for the images and volumes you are going to run.
- **Ports on this host.** 80/tcp, 443/tcp, and 443/udp free. The proxy publishes all three (443/udp is HTTP/3). The dashboard API listens on `127.0.0.1:7070` and is not published. With more than one server, the mesh listens only on that node's private address, port 7443, when the address is a private IP.
- **A name and DNS, for automatic TLS.** An `https://` name for the dashboard, for example `aven.example.com`. For generated service hostnames, a base domain such as `apps.example.com`. Point them at this server before the first request that needs a certificate:

  | Name | Type | Value |
  | --- | --- | --- |
  | `aven.example.com` | A and/or AAAA | this server's public address |
  | `*.apps.example.com` | A and/or AAAA | the same address |

  Each concrete hostname gets its own certificate on the first TLS handshake (HTTP-01 or TLS-ALPN). Aven does not request one certificate for the wildcard name itself. Ports 80 and 443 have to be reachable from the public internet. The base domain you type becomes `AVEN_BASE_DOMAIN`.

  With no domain, the installer can serve the dashboard only at `http://localhost:7070` on that machine.

More than one server is optional. Extra machines need Docker and must reach the main server over HTTPS. A private network such as Tailscale or WireGuard is how services on different machines reach each other.

### Read the script, then run it

```sh
curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh -o install-server.sh
less install-server.sh          # or: sh install-server.sh --help
sh install-server.sh
```

Run that as a normal user. The script starts itself again with `sudo`. Piped into `sudo sh`, sudo-rs (the default sudo since Ubuntu 25.10) keeps the script in the background of the terminal, where it cannot ask questions, and it stops unless `--yes` is set.

On a terminal the first question is the role: main server (dashboard and API) or region server (joins an existing main server). A main server then asks for the dashboard URL, the base domain, an optional Let's Encrypt email, and the `admin@local` password. It shows a summary and changes nothing until you confirm. It checks the release checksum, installs the files below, writes `/etc/aven/aven.env`, starts the service, and prints the sign-in details and the DNS names.

Unattended, after you have read the script:

```sh
sudo sh install-server.sh --yes \
  --public-url https://aven.example.com \
  --base-domain apps.example.com \
  --acme-email you@example.com
```

### What a main-server install changes

- `/usr/local/bin/aven-server` and `/usr/local/bin/aven`
- Railpack at `/usr/local/bin/railpack`, unless you pass `--no-railpack` or `railpack` is already on `PATH` (the script's default Railpack release is v0.42.0)
- agent and CLI downloads in `/usr/local/lib/aven/`
- `/etc/systemd/system/aven-server.service`
- `/etc/aven/aven.env` (mode 0600) and `/etc/aven/aven.env.example`
- on first start, `/var/lib/aven` (`aven.db`, `master.key`, `session.key`, `logs/`), a Docker network named `aven`, and the `aven-proxy` container with volumes `aven-caddy-data`, `aven-caddy-config`, and `aven-proxy-wake`

A region server does not install `aven-server`. It installs `/usr/local/bin/aven-agent`, writes `/etc/keel-agent/agent.env`, stores the node credential in `/var/lib/keel-agent/credentials.json`, and enables `keel-agent.service`. Those are the paths and the unit name the agent installer still creates, including on a machine that never ran Keel, so an existing agent is replaced in place. Settings inside that env file use the `AVEN_*` names. `KEEL_*` is still read.

An install that already has `/etc/keel/aven.env` or `/var/lib/keel` keeps those paths. See the [self-hosting guide](docs/self-hosting.md).

### Check that it started

```sh
systemctl is-active aven-server
curl -fsS -o /dev/null http://127.0.0.1:7070/api/cli/downloads
docker ps --filter name=aven-proxy --format '{{.Names}} {{.Status}}'
```

The installer waits until that URL answers, then prints the dashboard URL and the password if it generated one. Sign in as `admin@local`. The English UI label is **Admin → Owner · Setup** (`/admin/?section=setup`). The Korean UI label is **어드민 → Owner · 초기 설정**.

Open a project, press **Add** on the canvas, and deploy a service. The [self-hosting guide](docs/self-hosting.md) is the long version: every installer option, a manual install, backups and restore (including PITR), upgrades, uninstall, and the failure modes below.

### If it does not come up

- **Port 80 or 443 is taken.** The installer warns and continues. `aven-server` then exits. `journalctl -u aven-server` contains `start the proxy:` and `ports AVEN_HTTP_PORT/AVEN_HTTPS_PORT, default 80/443, must be free`. `ss -Htlpn 'sport = :80'` shows the listener.
- **DNS has not propagated, or TLS is not issued.** The install does not wait for a certificate. If the dashboard name does not resolve, the installer warns. Certificates are requested on the first HTTPS request. `docker logs aven-proxy` shows ACME errors. On an install that still runs `keel-proxy`, read that container instead.
- **Docker.** `docker info` has to succeed as root. "not running" and "cannot use its socket" are different errors. The second one is a permissions failure.
- **Firewall or IPv6.** Docker-published ports are not filtered by ufw's INPUT chain. A cloud firewall in front of the host still is. An AAAA record for an address this host does not answer on makes clients that prefer IPv6 fail while IPv4 works.
- **The agent does not join.** `journalctl -u keel-agent`. The join token is single-use. The default lifetime is 1 hour (the API accepts 60 seconds to 24 hours). `AVEN_PUBLIC_URL` on the main server has to be an `https://` URL.

Commands and what to change are in [Troubleshooting](docs/self-hosting.md#troubleshooting).

### Install by hand, or remove it

Hand install is [steps 3–6 of the self-hosting guide](docs/self-hosting.md#3-build). There is no uninstall script. [Uninstall and roll back](docs/self-hosting.md#uninstall-and-roll-back) lists the units, containers, and files to stop and delete, and how an upgrade copy under `/var/lib/aven/backups/` plus `/usr/local/bin/aven-server.prev` is put back.

## Screenshots

| Canvas | Deployments |
| --- | --- |
| ![A project on the Aven canvas: a storefront and an API with generated domains, a worker, PostgreSQL and Redis](docs/images/canvas-en.png) | ![Service panel with the active deployment and history](docs/images/deployments-en.png) |
| **Variables** | **Networking** |
| ![Service variables with references to PostgreSQL and Redis](docs/images/variables-en.png) | ![Public and private networking settings of a service](docs/images/networking-en.png) |

## Comparison

Aven cells come from this repository. Competitor cells come from the official pages linked under the table, checked on 2026-10-27. “Not documented” means those pages did not describe the feature. It is not a claim that the feature is missing.

| | Aven | Railway | Coolify | Dokploy |
| --- | --- | --- | --- | --- |
| Self-hosted | Yes. `aven-server` on your Linux host; more hosts run `aven-agent`. [^aven] | Hosted plans from Free through Enterprise. Enterprise adds Railway-managed dedicated hosts. An installer for your own servers is not documented. [^r-price] [^r-ent] | Yes. Self-hosted is the free edition. [^c-price] | Yes. Self-Hosted (OSS). [^d-diff] |
| Pricing model | No usage fees and no paid-feature gate in this repository. You pay for your own machines. [^aven] | Per-second CPU, memory, and volume usage, plus a plan fee. The Free plan is $0 with $1 of usage credit per month. [^r-price] | Self-hosted is free and includes all current and upcoming features. Cloud is $5/month for two servers, then $3/month per extra server. [^c-price] | Self-Hosted (OSS) is free. Cloud starts at $4.50/month per server. Enterprise is a license (contact). [^d-diff] |
| License | AGPL-3.0-only. [^a-lic] | The platform source is not published. The CLI is open source. [^r-cli] | Apache-2.0. [^c-lic] | Apache-2.0, except a `/proprietary` directory if present, which `LICENSE.MD` places under a separate proprietary license. [^d-lic] |
| SSO in the free tier | Yes. SAML SSO is in the core. [^a-sso] | No on the Free plan. SSO is listed for Enterprise and for committed spend. [^r-price] | Yes on self-hosted (the free edition), from v4.3.0. Coolify Cloud manages its own sign-in. [^c-sso] | No. SSO is Enterprise only. The OSS and non-Enterprise Cloud editions do not include it. [^d-diff] |
| Passkeys | Yes. WebAuthn passkeys for dashboard sign-in. [^a-pk] | Yes, as account MFA. The MFA page names no plan restriction. [^r-mfa] | Not documented for Coolify's own sign-in (2FA and SSO are). [^c-pk] | Yes, for panel sign-in. The Cloud plan table does not list them for Hobby. [^d-pk] |
| Multi-node | Yes. Additional servers join with `aven-agent`. [^a-nodes] | Horizontal replicas (plan limits) and geographic regions, on Railway's platform. Attaching your own servers is not documented. [^r-nodes] | Yes. Self-hosted can connect unlimited servers. [^c-price] | Yes. Multi-server / remote servers on the OSS edition. [^d-diff] |
| Preview environments | Yes. Pull-request environments. [^a-prev] | Yes. PR environments, created when a pull request opens and removed when it merges or closes. [^r-prev] | Yes. Pull-request and merge-request previews. [^c-prev] | Yes. GitHub pull-request previews, off by default. [^d-prev] |
| Managed databases | One-click Postgres, MySQL, Redis, MongoDB, and an S3-compatible bucket, on your servers. [^a-db] | Postgres, MySQL, Redis, and MongoDB templates. Railway provisions each one. HA is a conversion you turn on. [^r-db] | One-click Postgres, MySQL, MariaDB, MongoDB, Redis, Dragonfly, KeyDB, and ClickHouse, plus SQLite marked experimental, on your servers. [^c-db] | MySQL, PostgreSQL, MongoDB, Redis, and MariaDB, on your servers. [^d-feat] |
| PITR / backups | Postgres WAL and MySQL binlog PITR to backup storage, logical database backups, and a copy of Aven's own SQLite database. [^a-pitr] | Volume backups, and PITR for Postgres and MySQL, which you turn on. [^r-pitr] | Scheduled dumps (Postgres uses `pg_dump` / `pg_dumpall`) with optional S3. The Postgres guide says PITR is continuous WAL archiving you set up separately. [^c-pitr] | Manual and scheduled database backups. PITR is not documented. [^d-feat] |
| Git push deploy | GitHub push webhooks. This tree has no git receive-pack server. [^a-git] | GitHub autodeploy when the connected branch is pushed. [^r-git] | Deploy on push through a GitHub App, GitLab, or a repository webhook. [^c-git] | GitHub autodeploy on push. Webhooks for GitHub, GitLab, Bitbucket, and Gitea. [^d-git] |
| Dockerfile / buildpack / Nixpacks | Dockerfile and Railpack. Unknown `railway.json` keys such as `nixpacksPlan` are ignored. A Dokploy nixpacks build is imported as Railpack. [^a-build] | Railpack and Dockerfile. Heroku and Paketo buildpacks were retired in 2025, and the build docs no longer mention Nixpacks. [^r-build] [^r-bp] | Nixpacks, Dockerfile, Docker Compose, and static. Buildpacks are not among the documented build packs. [^c-build] | Nixpacks (the default), Railpack, Dockerfile, Heroku buildpacks, Paketo buildpacks, and static. [^d-build] |
| Web UI | Yes. Embedded dashboard, English and Korean. [^aven] | Yes. Project canvas and service settings. [^r-ui] | Yes. Dashboard. [^c-price] | Yes. Dashboard. [^d-diff] |
| CLI | Yes. `aven`. [^a-cli] | Yes. Railway CLI, documented as open source. [^r-cli] | Yes. `coolify`. [^c-cli] | Yes. `@dokploy/cli`. [^d-cli] |

[^aven]: This repository: [README.md](README.md), [docs/self-hosting.md](docs/self-hosting.md), [install-server.sh](install-server.sh). A search of the Go sources and docs found no billing, price, or license-key gate.
[^a-lic]: [LICENSE](LICENSE) and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) (AGPL-3.0-only).
[^a-sso]: SAML routes in `internal/api/api.go`, [docs/permissions.md](docs/permissions.md), [docs/cli.md](docs/cli.md), tests in `internal/api/saml_test.go`.
[^a-pk]: `internal/api/auth.go`, `internal/api/mfa_test.go`, `internal/store/mfa_test.go`.
[^a-nodes]: [docs/self-hosting.md](docs/self-hosting.md) (“Adding servers”), `internal/store/nodes_test.go`, `internal/deploy/mesh_test.go`.
[^a-prev]: `internal/api/github_preview_test.go`, `internal/store/preview_test.go`.
[^a-db]: `internal/catalog/catalog.go` (Postgres, MySQL, Redis, MongoDB) and the bucket service in [docs/cli.md](docs/cli.md).
[^a-pitr]: `aven pitr` in [docs/cli.md](docs/cli.md) (Postgres WAL, MySQL binlog), `internal/api/dbpitr_test.go`, `internal/agent/dbbackup_test.go`, and the server database backup in [docs/self-hosting.md](docs/self-hosting.md).
[^a-git]: GitHub webhook setup in [docs/self-hosting.md](docs/self-hosting.md). No git receive-pack server showed up in the Go tree.
[^a-build]: [docs/self-hosting.md](docs/self-hosting.md), `internal/config/config.go` (unknown keys including `nixpacksPlan` are ignored), `internal/dokploy/translate.go` (a nixpacks build is replaced by Railpack).
[^a-cli]: [docs/cli.md](docs/cli.md), `cmd/aven`.

[^r-price]: [railway.com/pricing](https://railway.com/pricing) and [railway.com/enterprise](https://railway.com/enterprise), fetched 2026-10-27. The pricing page lists Free through Enterprise, per-second usage rates, and SSO under Enterprise and committed spend.
[^r-cli]: [docs.railway.com/cli](https://docs.railway.com/cli) (“The Railway CLI is open source”), fetched 2026-10-27.
[^r-mfa]: [docs.railway.com/access/multi-factor-authentication](https://docs.railway.com/access/multi-factor-authentication), fetched 2026-10-27. The page documents passkeys and does not name a plan.
[^r-nodes]: Replica limits on [railway.com/pricing](https://railway.com/pricing). Regions are listed on [docs.railway.com/deployments](https://docs.railway.com/deployments) (“Deploy to specific geographic regions”). Neither page describes attaching your own server.
[^r-prev]: [docs.railway.com/guides/preview-deployments-with-pr-environments](https://docs.railway.com/guides/preview-deployments-with-pr-environments), fetched 2026-10-27.
[^r-db]: [Postgres](https://docs.railway.com/databases/postgresql), [MySQL](https://docs.railway.com/databases/mysql), [Redis](https://docs.railway.com/databases/redis), and [MongoDB](https://docs.railway.com/databases/mongodb), fetched 2026-10-27.
[^r-pitr]: [docs.railway.com/volumes/point-in-time-recovery](https://docs.railway.com/volumes/point-in-time-recovery) (Postgres and MySQL) and the backup sections of the Postgres and MySQL pages above, fetched 2026-10-27.
[^r-git]: [docs.railway.com/deployments/github-autodeploys](https://docs.railway.com/deployments/github-autodeploys), fetched 2026-10-27.
[^r-bp]: [Heroku Buildpacks & Paketo deprecation notice](https://station.railway.com/feedback/important-heroku-buildpacks-paketo-de-8e60f54d), posted by Railway staff on 2025-01-13, fetched 2026-10-27. Railway retired both builders on 2025-02-21.
[^r-ent]: [docs.railway.com/maturity/enterprise](https://docs.railway.com/maturity/enterprise), fetched 2026-10-27 (“Your workloads run on dedicated Railway hosts for complete resource isolation”).
[^r-build]: [docs.railway.com/builds](https://docs.railway.com/builds), fetched 2026-10-27. The page documents Railpack and Dockerfiles. It does not mention Nixpacks or buildpacks.
[^r-ui]: Project canvas on the Postgres page and service settings on the autodeploy page, same fetches as above.

[^c-price]: [coolify.io/pricing](https://coolify.io/pricing), fetched 2026-10-27. Self-hosted is “Free forever” with “Full access to all features” and unlimited connected servers. Cloud is “$5 /month base price (connect 2 servers)” plus “$3 /month per additional server”.
[^c-lic]: [github.com/coollabsio/coolify `LICENSE`](https://github.com/coollabsio/coolify/blob/main/LICENSE) (Apache License 2.0), fetched 2026-10-27.
[^c-sso]: [coolify.io/docs/core/security/authentication/sso/overview](https://coolify.io/docs/core/security/authentication/sso/overview), fetched 2026-10-27. SSO is for self-hosted Coolify (v4.3.0+). The page says Coolify Cloud manages its own authentication.
[^c-pk]: Searched Coolify’s docs on 2026-10-27. Dashboard docs describe two-factor authentication ([disable 2FA manually](https://coolify.io/docs/troubleshoot/dashboard/disable-2fa-manually)) and SSO. [Pocket ID](https://coolify.io/docs/services/pocket-id) is a separate app you can deploy. No page was found that adds passkeys to Coolify’s own sign-in.
[^c-db]: [coolify.io/docs/databases](https://coolify.io/docs/databases), fetched 2026-10-27. The page lists the engines above and marks SQLite experimental.
[^c-pitr]: [coolify.io/self-host-postgres-with-backups](https://coolify.io/self-host-postgres-with-backups), fetched 2026-10-27. Scheduled backups are `pg_dump` / `pg_dumpall`. The page’s PITR question says that is a separate WAL-archiving setup.
[^c-prev]: [coolify.io/docs/applications/deployments/preview-deployments](https://coolify.io/docs/applications/deployments/preview-deployments), fetched 2026-10-27.
[^c-git]: [Automatic deployments](https://coolify.io/docs/applications/deployments/automatic-deployments), fetched 2026-10-27. Deploy on push is documented for a GitHub App, GitLab, and a repository webhook.
[^c-build]: [Deploy with Nixpacks](https://coolify.io/docs/applications/builds/nixpacks/deploy) and the `coolify` CLI `--build-pack` values `nixpacks`, `static`, `dockerfile`, `dockercompose` on [create deploy key](https://coolify.io/docs/cli/command-reference/commands/app/create-deploy-key), fetched 2026-10-27. Those pages do not list a buildpack builder.
[^c-cli]: [coolify app create deploy-key](https://coolify.io/docs/cli/command-reference/commands/app/create-deploy-key), fetched 2026-10-27.

[^d-diff]: [docs.dokploy.com/docs/core/differences](https://docs.dokploy.com/docs/core/differences), fetched 2026-10-27. Self-Hosted (OSS) is free and includes multi-server. SSO is marked off for OSS and for non-Enterprise Cloud, and on for Enterprise.
[^d-lic]: [github.com/Dokploy/dokploy `LICENSE.MD`](https://github.com/Dokploy/dokploy/blob/canary/LICENSE.MD) on the default branch (`canary`), fetched 2026-10-27.
[^d-pk]: [docs.dokploy.com/docs/core/account-security](https://docs.dokploy.com/docs/core/account-security), fetched 2026-10-27. The Cloud plan table on the differences page lists 2FA as unavailable on Hobby and does not mention passkeys.
[^d-prev]: [docs.dokploy.com/docs/core/applications/preview-deployments](https://docs.dokploy.com/docs/core/applications/preview-deployments), fetched 2026-10-27.
[^d-feat]: [docs.dokploy.com/docs/core/features](https://docs.dokploy.com/docs/core/features) (MySQL, PostgreSQL, MongoDB, Redis, MariaDB; manual and scheduled backups), fetched 2026-10-27. A search of the published docs corpus found no Dokploy PITR page. A template such as Databasus that mentions PITR is a separate app, not Dokploy’s database backups.
[^d-git]: [docs.dokploy.com/docs/core/auto-deploy](https://docs.dokploy.com/docs/core/auto-deploy), fetched 2026-10-27.
[^d-build]: [docs.dokploy.com/docs/core/applications/build-type](https://docs.dokploy.com/docs/core/applications/build-type), fetched 2026-10-27.
[^d-cli]: [docs.dokploy.com/docs/cli](https://docs.dokploy.com/docs/cli) (`npm install -g @dokploy/cli`), fetched 2026-10-27.

## Feature stability

**Stable** means the feature is described for operators in this repository and has dedicated tests here. **Experimental** means that bar was not met. The reason is in the last column. Either label can change, and a Stable feature can still move between releases.

| Feature | Label | Why |
| --- | --- | --- |
| Single-server install (`aven-server`, Docker, Caddy proxy) | Stable | [`install-server.sh`](install-server.sh), [docs/self-hosting.md](docs/self-hosting.md), `internal/selfhost/*_test.go` |
| Extra servers (`aven-agent`) and the private mesh | Stable | Self-hosting guide, “Adding servers”; `internal/agent/*_test.go`, `internal/deploy/mesh_test.go` |
| GitHub push-to-deploy and pull-request previews | Stable | `internal/api/github_preview_test.go`, `internal/watch/watch_test.go` |
| Dockerfile and Railpack builds | Stable | `internal/node/railpack_test.go`, `internal/config/config_test.go` |
| Image services, Compose, templates, functions, `aven up` | Stable | `internal/compose/compose_test.go`, `internal/api/templates_test.go`, `internal/api/functions_test.go`, `internal/agent/extra_test.go` (`TestSourceUploadExtractsSafely`) |
| Environments, variable references, sealed variables, config as code | Stable | `internal/vars/vars_test.go`, `internal/api/sealed_test.go`, `internal/config/config_test.go` |
| One-click Postgres, MySQL, Redis, MongoDB, and S3-compatible buckets | Stable | `internal/catalog/catalog.go` and its tests, `internal/api/bucket_test.go` |
| Data browser | Stable | `internal/api/dbdata_test.go`, `internal/api/dbdata_edit_test.go` |
| Logical backups and PITR (Postgres WAL, MySQL binlog) | Stable | [docs/cli.md](docs/cli.md) `aven pitr`; `internal/agent/dbbackup_test.go`, `internal/api/dbpitr_test.go` |
| PgBouncer, Postgres major upgrades, database HA | Stable | `internal/api/postgres_test.go`, `internal/catalog/ha_test.go`, `cmd/aven/ha_test.go` |
| Domains, automatic TLS, path rules, redirects, basic auth, wildcards | Stable | `internal/proxy/caddy_test.go` (`TestPathRulesOrdering`, `TestRedirectAndAuthAndTrust`, `TestBuildWildcardAutoTLS`) |
| Per-domain IPv6 and TCP proxies | Stable | `internal/deploy/ipv6addr_test.go`, `internal/deploy/ports_test.go` |
| Private DNS between services | Stable | `internal/deploy/privatedns_test.go` |
| Edge cache and WAF rules | Stable | `internal/api/edge_test.go`, `internal/proxy/edge_test.go` |
| Cloudflare Tunnel ingress | Experimental | The ingress mode is stored and validated in `internal/api/domainstate_test.go`. No test starts a tunnel. |
| Replicas, autoscaling, rollbacks, deploy overlap | Stable | `internal/deploy/scale_test.go`, overlap settings in `internal/api/batch2_test.go` |
| Moving a volume onto another node | Stable | `internal/deploy/remote_test.go` (`TestCopyVolumesCopiesDataAndReportsOldLocation`) |
| App sleeping (wake on the next request) | Experimental | Implemented in `internal/deploy/sleep.go`. Config parsing is tested. No test covers the idle-sleep and wake path. |
| Cron schedules | Experimental | Expressions are parsed and stored (`internal/cronx/cron_test.go`, API tests). No test was found that fires a scheduled run. |
| Image auto-updates | Stable | `internal/deploy/imageupdates_test.go` |
| Logs, metrics, tracing, alerts | Stable | `internal/tracing/tracing_test.go`, `internal/alerts/alerts_test.go`, `internal/metrics/` |
| Workspaces, roles, access groups, audit log | Stable | [docs/permissions.md](docs/permissions.md), `internal/store/permissions_test.go`, `internal/api/access_controls_test.go` |
| Passkeys, TOTP, enforced 2FA | Stable | `internal/api/mfa_test.go`, `internal/store/mfa_test.go` |
| SAML SSO | Stable | `internal/api/saml_test.go` |
| OAuth 2.0 / OpenID Connect provider | Stable | [docs/api-v1.md](docs/api-v1.md), `internal/store/oauth_test.go`, `internal/api/oauth_test.go` |
| CLI, HTTP API, GraphQL, MCP | Stable | [docs/cli.md](docs/cli.md), [docs/api-v1.md](docs/api-v1.md), `internal/api/graphql_test.go`, `cmd/aven/mcp_test.go` |
| Infrastructure as code | Stable | `internal/deploy/configapply_test.go`, `internal/iac/` |
| Feature flags | Stable | `internal/api/featureflags_test.go`, `sdk/index.test.ts` |
| Sandboxes | Stable | `internal/api/sandboxes_test.go` |
| Dokploy import | Stable | `cmd/aven/importdokploy_test.go` |
| Aven on Aven (self-upgrade) | Stable | [docs/selfhost.md](docs/selfhost.md), `internal/deploy/selfhost_test.go`, `internal/api/selfservice_test.go` |
| AI assistant (bring your own model) | Experimental | Settings and tool calls are documented in [docs/api-v1.md](docs/api-v1.md). That page lists limits, including logs sent to the model without redaction, and no service creation. |
| Web dashboard | Experimental | English and Korean UI sources and the screenshots above are in the tree. `web/` has no automated component or end-to-end tests. |

## How this was built

Aven continues an earlier project named Keel. This public repository starts at the first Aven release (`755a737`, 2026-10-25) and does not include Keel's history.

New installs use Aven names (`AVEN_*` settings, `/var/lib/aven`, `aven-proxy`, `aven.*` labels). An install that started as Keel keeps working without changes: the old `KEEL_*` names, paths and containers are still read, and private addresses stay on `<service>.<env>.keel.internal`. See the [self-hosting guide](docs/self-hosting.md).

AI coding agents were used substantially in development. Changes went through human review and testing.

## Highlights

**Deploy**
- Services from GitHub repositories (Dockerfile or [Railpack](https://github.com/railwayapp/railpack), no Dockerfile needed), Docker images, Docker Compose files, templates, single-file functions or a local directory (`aven up`).
- Push-to-deploy with watch paths, wait-for-CI, commit statuses and pull request preview environments.
- Environments with variable references (`${{ db.DATABASE_URL }}`), shared and sealed variables, a raw editor and config as code (`railway.json`, `aven.json`).
- Pre-deploy commands, health checks, zero-downtime overlap, rollbacks, and image auto-updates. Cron schedules and app sleeping are implemented and marked experimental in the table above.

**Data**
- One-click PostgreSQL, MySQL, Redis, MongoDB and S3-compatible buckets, with volumes, a data browser and logical backups to S3.
- Point-in-time recovery, PgBouncer pooling, major version upgrades and high-availability clusters for PostgreSQL.

**Networking**
- Generated and custom domains with automatic TLS through a managed Caddy proxy, path rules, redirects, basic auth and wildcard domains.
- IPv6 first: optional per-domain public IPv6 addresses, outbound IPv6 control and TCP proxies for databases and other raw TCP services.
- Private DNS names between services, also across servers over an encrypted mesh (Tailscale or WireGuard addresses).
- Edge caching, WAF rules and Cloudflare Tunnel ingress.

**Multiple servers**
- Add servers with a one-line agent install. Place services on a node, spread replicas across nodes, autoscale on CPU, memory or requests per second, and move services with their volumes.

**Observability**
- Build, deploy and runtime logs, HTTP and network flow logs, metrics, OpenTelemetry tracing, dashboards, metric alerts and notifications (webhooks, Slack, Discord, email).

**Teams and security**
- Workspaces, projects and environments with roles, access groups, restricted environments and guardrails for allowed sources.
- Passkeys, TOTP, enforced 2FA, SAML SSO, an OAuth 2.0 / OpenID Connect provider ("Login with Aven"), API tokens, audit logs, usage and resource limits.

**Automation**
- `aven` CLI, REST and GraphQL APIs, an MCP server for AI tools, an optional AI assistant (bring your own model), infrastructure as code for whole projects, sandboxes and a feature flags SDK.
- `aven import dokploy` moves an existing Dokploy installation.

**Aven on Aven**
- The server manages itself: `aven-server` appears on the server owner's canvas, checks its repository for updates and upgrades itself from a deployment, with an automatic rollback if the new version is not healthy. Server settings are edited from the same panel. See [Aven on Aven](docs/selfhost.md).

## How it works

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/architecture-dark.svg">
  <img alt="Aven architecture: the Internet reaches aven-proxy on each server; aven-server on the first server configures the proxy and runs services through the Docker API; aven-agent on extra servers connects back over mTLS, and services talk over a private mesh" src="docs/assets/architecture-light.svg" width="900">
</picture>

- `aven-server` is a single Go binary with the dashboard embedded. It keeps its state in SQLite (`/var/lib/aven`), builds images with Docker and Railpack, and runs containers through the Docker API.
- `aven-proxy` is a Caddy container that Aven configures (`keel-proxy` on installs that started as Keel). It terminates TLS, routes domains to replicas on its own node and serves the edge features.
- `aven-agent` runs on additional servers, connects back to the server with a certificate and runs the same Docker operations there. Services on different nodes reach each other through an mTLS mesh on the private addresses you provide.

## Build from source

Requirements: Go 1.26+, Node.js 20.9+ with npm, and Docker for running the server.

```sh
make all        # web UI, aven-server, agents and CLI binaries in ./bin
```

Run a local instance for evaluation:

```sh
mkdir -p ./data
AVEN_ADMIN_PASSWORD='replace-with-a-long-password' \
AVEN_HTTP_PORT=18080 AVEN_HTTPS_PORT=18443 AVEN_PROXY_BIND=127.0.0.1 \
./bin/aven-server -listen 127.0.0.1:7070 -data ./data
```

Sign in at `http://127.0.0.1:7070` as `admin@local`. The server starts its proxy container through Docker; change the ports and bind address before running it on a network. For production, use [`deploy/aven.env.example`](deploy/aven.env.example) and [`deploy/aven-server.service`](deploy/aven-server.service), or `docker build -t aven-server .` for the container image.

## Relationship to Railway

Aven is an independent implementation. This repository contains none of Railway's source code: the server, agents, CLI, proxy configuration and dashboard were written for Aven.

What Aven shares with Railway is on the surface:

- **Look and workflow.** The canvas, service panel and settings follow Railway's dashboard, because many people already know that workflow.
- **Compatibility.** Aven accepts `railway.json` / `railway.toml` config files and offers a GraphQL endpoint shaped like Railway's public API, so existing configuration and scripts keep working when you move.
- **Railpack.** Builds without a Dockerfile use [Railpack](https://github.com/railwayapp/railpack), an open-source builder published by Railway (MIT, [license](https://github.com/railwayapp/railpack/blob/main/LICENSE)), run as a separate program.

Railway is a trademark of its owner and is used here only to describe compatibility. The [comparison](#comparison) records what the Railway docs say, and notes that Railway has not published the platform's source.

## Documentation

- [Self-hosting guide](docs/self-hosting.md): install, DNS, what the installer changes, backups and restore, upgrades, uninstall, troubleshooting
- [Aven on Aven](docs/selfhost.md): let Aven manage and upgrade its own server
- [CLI reference](docs/cli.md)
- [HTTP API](docs/api-v1.md) and [GraphQL API](docs/graphql.md)
- [Permissions and workspaces](docs/permissions.md)
- [Feature flags SDK](sdk/README.md)
- [Website](website/README.md): the source of [aven.turin.my](https://aven.turin.my) and the installer host

## FAQ

**Does it need Kubernetes?** No. Aven runs containers with plain Docker on each server and keeps its own state in SQLite. There is no cluster to install or operate.

**Is it production-ready?** It runs real workloads for its authors, but it is young: expect rough edges and read the release notes before upgrading. Back up the data directory (`/var/lib/aven`, or `/var/lib/keel` on installs that started as Keel), which holds the database and the key that encrypts secrets. See [Feature stability](#feature-stability) for what the tests in this tree cover.

**Do I need GitHub?** No. You can deploy Docker images, Compose files, templates and local directories (`aven up`). GitHub is needed for push-to-deploy and preview environments.

**How do I upgrade?** Run the install script again, or let Aven upgrade itself from its own canvas ([Aven on Aven](docs/selfhost.md)). The script keeps `/etc/aven/aven.env`, copies `aven.db` (and `keel.db` if that file is still the database) to `/var/lib/aven/backups/` after stopping the server, and keeps the old binary as `/usr/local/bin/aven-server.prev`. Migrations run on start. Copy `master.key` somewhere else yourself: the script does not. Putting the previous binary back without the database copy can fail if a migration already ran. The steps are in the [self-hosting guide](docs/self-hosting.md#8-backups-and-upgrades).

**Can I move from Railway or Dokploy?** Aven reads `railway.json` / `railway.toml` and answers Railway-shaped GraphQL queries, and `aven import dokploy` recreates a Dokploy installation's services, variables, domains and volumes. Data is not copied: move database contents with your usual dump and restore tools.

**Why AGPL?** So that improvements to a hosted Aven stay open. Running Aven for your own apps, at any scale, has no extra obligations; offering a modified Aven to others over a network means sharing those modifications.

## Contributing

Bug reports, documentation fixes and pull requests are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) and the [code of conduct](CODE_OF_CONDUCT.md) first. Report security vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

## License

Aven is licensed under the [GNU Affero General Public License v3.0 only](LICENSE) (AGPL-3.0-only). If you run a modified version for others over a network, you must offer them its source. Bundled third-party software and fonts are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
