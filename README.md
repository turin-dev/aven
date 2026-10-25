# Aven

**Deploy and run your apps on your own servers, with the experience of a hosted platform.**

[![Release](https://img.shields.io/github/v/release/turin-dev/aven)](https://github.com/turin-dev/aven/releases)
[![CI](https://github.com/turin-dev/aven/actions/workflows/ci.yml/badge.svg)](https://github.com/turin-dev/aven/actions/workflows/ci.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE)

[Quick start](#quick-start) · [Highlights](#highlights) · [How it works](#how-it-works) · [Docs](#documentation) · [FAQ](#faq) · [한국어](README.ko.md)

![A project on the Aven canvas: a storefront and an API with generated domains, a worker, PostgreSQL and Redis](docs/images/canvas-en.png)

Aven is a self-hosted platform for applications, databases and background jobs. Connect a GitHub repository, pick a Docker image or start a database, and Aven builds it, gives it a domain with TLS, wires it to its neighbours over a private network and keeps it running. Everything lives on a canvas per environment, like a hosted PaaS, except that the servers are yours.

One `aven-server` process runs next to Docker on your first server. More servers join as nodes with a small agent. The dashboard, HTTP and GraphQL APIs, CLI and MCP server all use the same permission checks. The dashboard is available in English and Korean.

> [!IMPORTANT]
> **Aven is not Railway and contains no Railway code.** It is an independent project, written from scratch, with no affiliation to Railway Corporation. The dashboard's look and workflow are modeled on Railway's, and Aven reads `railway.json` and answers Railway-shaped GraphQL queries so projects can move over easily. See [Relationship to Railway](#relationship-to-railway).

> [!NOTE]
> Aven is young and moves quickly. Interfaces and settings can change between releases, so read the release notes and back up the data directory before upgrading.

## Screenshots

| Deployments | Variables | Networking |
| --- | --- | --- |
| ![Service panel with the active deployment and history](docs/images/deployments-en.png) | ![Service variables with references to PostgreSQL and Redis](docs/images/variables-en.png) | ![Public and private networking settings of a service](docs/images/networking-en.png) |

## Highlights

**Deploy**
- Services from GitHub repositories (Dockerfile or [Railpack](https://github.com/railwayapp/railpack), no Dockerfile needed), Docker images, Docker Compose files, templates, single-file functions or a local directory (`aven up`).
- Push-to-deploy with watch paths, wait-for-CI, commit statuses and pull request preview environments.
- Environments with variable references (`${{ db.DATABASE_URL }}`), shared and sealed variables, a raw editor and config as code (`railway.json`, `keel.json`).
- Pre-deploy commands, health checks, zero-downtime overlap, rollbacks, cron jobs, app sleeping and image auto-updates.

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

## Quick start

You need:

- a Linux server (amd64 or arm64) with systemd and Docker, ports 80 and 443 free;
- a DNS name for the dashboard (for example `aven.example.com`) and, for generated service domains, a wildcard (`*.apps.example.com`);
- optionally more servers for multi-node setups, reachable from each other over a private network such as Tailscale or WireGuard.

```sh
curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh \
  | sudo sh -s -- --public-url https://aven.example.com
```

The script verifies the release checksum, installs `aven-server`, the CLI and Railpack, writes `/etc/keel/aven.env` with a generated admin password, starts the service and prints the sign-in details and the DNS records to create. Point `aven.example.com` (and, for generated service domains, `*.apps.example.com` with `KEEL_BASE_DOMAIN`) at the server, sign in, and follow the first-run checklist under *Admin → Owner · 초기 설정*.

Then open a project, press **Add** on the canvas and deploy your first service. The [self-hosting guide](docs/self-hosting.md) covers DNS, configuration, manual installation, backups, upgrades and adding servers.

## How it works

```
                 Internet (IPv6 / IPv4)
                          │
               ┌──────────▼──────────┐            ┌───────────────────────┐
               │ keel-proxy (Caddy)  │            │ node 2                │
               │ TLS, routing, edge  │            │ aven-agent            │
               └──────────┬──────────┘            │ keel-proxy · services │
   dashboard / API        │                       └───────────▲───────────┘
 ┌────────────────────────▼────────────┐   mesh (mTLS over    │
 │ aven-server                         │   Tailscale/WireGuard)│
 │ API · scheduler · builds · SQLite ◄─┼──────────────────────┘
 └────────────────────────┬────────────┘
                          │ Docker
               your services, databases, volumes
```

- `aven-server` is a single Go binary with the dashboard embedded. It keeps its state in SQLite (`/var/lib/keel`), builds images with Docker and Railpack, and runs containers through the Docker API.
- `keel-proxy` is a Caddy container that Aven configures. It terminates TLS, routes domains to replicas on its own node and serves the edge features.
- `aven-agent` runs on additional servers, connects back to the server with a certificate and runs the same Docker operations there. Services on different nodes reach each other through an mTLS mesh on the private addresses you provide.

## Build from source

Requirements: Go 1.26+, Node.js 20.9+ with npm, and Docker for running the server.

```sh
make all        # web UI, aven-server, agents and CLI binaries in ./bin
```

Run a local instance for evaluation:

```sh
mkdir -p ./data
KEEL_ADMIN_PASSWORD='replace-with-a-long-password' \
KEEL_HTTP_PORT=18080 KEEL_HTTPS_PORT=18443 KEEL_PROXY_BIND=127.0.0.1 \
./bin/aven-server -listen 127.0.0.1:7070 -data ./data
```

Sign in at `http://127.0.0.1:7070` as `admin@local`. The server starts its proxy container through Docker; change the ports and bind address before running it on a network. For production, use [`deploy/aven.env.example`](deploy/aven.env.example) and [`deploy/aven-server.service`](deploy/aven-server.service), or `docker build -t aven-server .` for the container image.

## Upgrading from Keel

Aven is the continuation of Keel and keeps its runtime names, so existing installations upgrade in place: `KEEL_*` settings, `/var/lib/keel`, `/var/lib/keel-agent`, the `keel-agent` unit, `.keel` CLI links and `keel.*` container labels all stay. Copy the environment file to `/etc/keel/aven.env`, install `deploy/aven-server.service` and the `aven-server` binary, stop `keel-server` and enable `aven-server`. Never run both against the same data directory.

## Relationship to Railway

Aven is **not** a fork, clone or redistribution of Railway. Railway's platform is closed source, and this repository contains none of its source code: the server, agents, CLI, proxy configuration and dashboard were all written for Aven.

What Aven shares with Railway is on the surface:

- **Look and workflow.** The canvas, service panel and settings are designed after Railway's dashboard, because many people already know how to use it.
- **Compatibility.** Aven accepts `railway.json` / `railway.toml` config files and offers a GraphQL endpoint shaped like Railway's public API, so existing configuration and scripts keep working when you move.
- **Railpack.** Builds without a Dockerfile use [Railpack](https://github.com/railwayapp/railpack), an open-source (MIT) builder published by Railway, run as a separate program.

Railway is a trademark of Railway Corporation and is used here only to describe compatibility.

## Documentation

- [Self-hosting guide](docs/self-hosting.md): install, DNS, configuration, backups, upgrades, nodes
- [Aven on Aven](docs/selfhost.md): let Aven manage and upgrade its own server
- [CLI reference](docs/cli.md)
- [HTTP API](docs/api-v1.md) and [GraphQL API](docs/graphql.md)
- [Permissions and workspaces](docs/permissions.md)
- [Feature flags SDK](sdk/README.md)

## FAQ

**Does it need Kubernetes?** No. Aven runs containers with plain Docker on each server and keeps its own state in SQLite. There is no cluster to install or operate.

**Is it production-ready?** It runs real workloads for its authors, but it is young: expect rough edges and read the release notes before upgrading. Back up the data directory (`/var/lib/keel`), which holds the database and the key that encrypts secrets.

**Do I need GitHub?** No. You can deploy Docker images, Compose files, templates and local directories (`aven up`). GitHub is needed for push-to-deploy and preview environments.

**How do I upgrade?** Run the install script again, or let Aven upgrade itself from its own canvas ([Aven on Aven](docs/selfhost.md)). Database migrations run on start.

**Can I move from Railway or Dokploy?** Aven reads `railway.json` / `railway.toml` and answers Railway-shaped GraphQL queries, and `aven import dokploy` recreates a Dokploy installation's services, variables, domains and volumes. Data is not copied: move database contents with your usual dump and restore tools.

**Why AGPL?** So that improvements to a hosted Aven stay open. Running Aven for your own apps, at any scale, has no extra obligations; offering a modified Aven to others over a network means sharing those modifications.

## Contributing

Bug reports, documentation fixes and pull requests are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) and the [code of conduct](CODE_OF_CONDUCT.md) first. Report security vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

## License

Aven is licensed under the [GNU Affero General Public License v3.0 only](LICENSE) (AGPL-3.0-only). If you run a modified version for others over a network, you must offer them its source. Bundled third-party software and fonts are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
