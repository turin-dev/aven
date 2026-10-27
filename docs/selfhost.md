# Aven on Aven (self service)

Aven can build and deploy `aven-server` itself. You deploy the Aven repository (or your fork) as an ordinary repo service, mark that service as the **self service**, and every deployment of it builds a new `aven-server` image and swaps the running server for it, with an automatic rollback when the new server does not become healthy.

A fresh install puts this service on the canvas for the server owner (project `aven`); existing installs add it from *Admin → Owner · 초기 설정*. Nothing changes until you deploy it: the default install (`install-server.sh`, systemd unit `aven-server`) keeps working, and you can go back to it at any time.

## How it works

- `aven-server` runs as a Docker container named `aven-server` instead of a systemd service. The container uses the host network, host PID namespace and `/var/run/docker.sock`, and it is privileged, because the server manages the host itself: it listens on host addresses, adds per-domain IPv6 addresses, writes egress rules and reads container ports from `/proc`. The container spec is fixed in code (`internal/selfhost`); a service's own settings cannot change it.
- The self service is a normal repo service with a Dockerfile build (the repository's `Dockerfile`). Building, logs, GitHub webhooks and watch paths work as usual.
- When a self-service deployment has its image, Aven does not start containers for it. It starts a one-shot `aven-selfupgrade` container from the **new** image, which:
  1. renames the running `aven-server` to `aven-server-prev` and stops it (the database is never opened by two servers at once),
  2. starts a new `aven-server` from the new image and waits until its health check (`aven-server selfhost health`, which calls `/healthz`) passes,
  3. on success removes `aven-server-prev`; the new server finishes the deployment as live when it starts,
  4. on failure removes the new container, writes `selfhost-result.json` to the data directory and starts the previous server again, which marks the deployment as failed (`rolled back: …`) and keeps the previous deployment current.
- Before the swap Aven waits up to 15 minutes for other running deployments to finish. Deployments still running after that fail when the server restarts, as with any restart.
- The dashboard and API are unavailable for the few seconds of the swap. Your services keep running: `keel-proxy` and the app containers are not touched.
- The dashboard treats it as a special service: the panel shows only Deployments and Settings (source and build), deployments show only details and build logs, and the canvas menu offers open, settings, deploy, rollback and copy. The card shows the running version.
- **Update checks.** Every 30 minutes (and a minute after start) Aven compares the newest commit of the service's branch with the running deployment through the GitHub API, using the project's GitHub connection. When it is behind, the canvas card gets a pink dot, the service panel lists the new commits with **Update** (deploys the branch head) and **Check now** (`POST /api/owner/self-service/check`), and the activity feed gets one `aven.update_available` event per new version. Updates are never applied automatically.
- **Server settings.** The service's Settings tab starts with *Server settings*: the `AVEN_*` values of the env file, grouped (general, network, GitHub, mail, advanced), secrets shown only as set/unset (`GET/PUT /api/owner/server-config`, server owners only). Saving writes the file and nothing else; values that differ from the running server are marked *pending* and apply when aven-server is deployed again (**Save and deploy** redeploys the current image, so no build is needed). If that deployment rolls back, the env file is restored to the copy the last healthy server started with (`AVEN_DATA/selfhost-env.good`), so a bad value cannot stick. Data paths and the values the container overrides (`AVEN_DATA`, `AVEN_GATEWAY_SECRET_FILE`, `AVEN_AGENT_BIN_DIR`, `AVEN_RAILPACK_BIN`, `AVEN_SELF_*` file/service) are read-only. The container mounts the env file's directory read-write for this.
- The self service cannot be stopped, restarted, scaled or put to sleep from Aven (`409`). It always runs one replica on the server's own node. Use `docker` on the host for manual operations.

## Requirements

- A server installed as described in [Self-hosting](self-hosting.md), running as the systemd unit `aven-server`.
- The env file at `/etc/aven/aven.env` (or set `AVEN_SELF_ENV_FILE`). The container reads it with `docker --env-file`, which does **not** strip quotes: write `KEY=value`, not `KEY="value"`. Deployments check this and fail before touching the server if a value is quoted.
- Paths in the env file must exist inside the container. The data directory (`AVEN_DATA`), the env file's directory (read-write, for the server settings page), Docker volumes and `/root/.docker` are mounted at the same paths. `AVEN_RAILPACK_BIN` and `AVEN_AGENT_BIN_DIR` are overridden to the copies in the image. Anything else (for example `AVEN_TLS_CERT`) must live under one of the mounted paths.
- If `AVEN_LISTEN` is a private address (Tailscale/WireGuard), that interface must come up before Docker starts the container. Docker retries the container (`--restart unless-stopped`), so a late interface only delays startup.

## Setup from the dashboard

1. **The aven project.** On a fresh install (no services yet) Aven creates a workspace `Aven` with a project `aven` and a service `aven-server` on first start: repository `https://github.com/turin-dev/aven`, branch `main`, Dockerfile build, the server's own node, one replica, no domain, automatic deploys off (upgrade by pressing deploy; turning it on with a GitHub webhook replaces the server on every push). On an existing install, open *Admin → Owner · 초기 설정* and press **캔버스에 Aven 추가** under *Aven을 캔버스에서 관리* (`POST /api/owner/self-service`, optional body `{"repo_url", "branch"}`). To use your fork, change the repository in the service settings.

   The dedicated workspace keeps the service out of the default workspace's members and resource limits (the self service runs no containers, so the workspace has no limits). The project is visible only to server owners, even to admins of its workspace: deploying it runs code as root on the host. While it is the self service, the service, its environment and its project cannot be deleted (`409`).

2. **Deploy** `aven-server` from the canvas. The canvas card carries the Aven mark and the service panel explains what a deployment does.
   - If the server still runs as the systemd unit, the first deployment builds the image and then moves the server into the `aven-server` container: it starts a transient unit (`systemd-run`, `aven-selfbootstrap-<deployment>`) that runs `aven-server selfhost bootstrap`, which stops and disables the systemd unit and starts the container. If the container does not become healthy, bootstrap removes it, restarts the systemd unit, and that server marks the deployment as failed.
   - Once the server runs as the container, every deployment (push, redeploy, rollback to an earlier deployment) swaps it as described above.

3. **Stop managing Aven** with **해제** in the same checklist item (`DELETE /api/owner/self-service`). The project and service stay as ordinary ones and deploying them no longer replaces the server. The server keeps running however it runs now.

Settings:

| Variable | Default | Meaning |
| --- | --- | --- |
| `AVEN_SELF_SERVICE` | | Pins the self service by ID. Overrides the dashboard, which then cannot change it. |
| `AVEN_SELF_PROJECT` | | `off` skips creating the aven project on a fresh install. |
| `AVEN_SELF_REPO`, `AVEN_SELF_BRANCH` | `https://github.com/turin-dev/aven`, `main` | Source of the project created on first start or from the checklist. |
| `AVEN_SELF_UPDATE_INTERVAL` | `30m` | How often to check the repository branch for new commits (`off` disables). |
| `AVEN_SELF_ENV_FILE` | `/etc/aven/aven.env` | Env file passed to the container. |

## Manual bootstrap

Without the dashboard, build an image (`docker build -t aven-server .` in the repository, or deploy the self service once while the server cannot swap itself) and run, as root:

```sh
sudo aven-server selfhost bootstrap --image aven-server
```

Bootstrap stops the systemd unit `aven-server`, starts the `aven-server` container and waits for it to become healthy. If it does not, it removes the container and starts the systemd unit again. On success it disables the systemd unit, which stays installed for emergencies. Options: `--systemd-unit` (default `aven-server`, empty for none), `--env-file`, `--data`, `--timeout` (default 4m).

To see the exact `docker run` command without starting anything:

```sh
aven-server selfhost run-args --image aven-server
```

## Operations

```sh
docker ps --filter label=keel.self           # aven-server and, during a swap, aven-selfupgrade / aven-server-prev
docker logs -f aven-server                   # server logs
docker logs aven-selfupgrade                 # last swap (removed after the new server collects it)
```

**Manual rollback.** Redeploy an earlier deployment of the self service from the dashboard. If the dashboard is not reachable, print the `docker run` command for a previous image, then remove the current container and run the printed command:

```sh
aven-server selfhost run-args --image <previous image>
docker rm -f aven-server
# paste the printed docker run command
```

**Back to systemd.**

```sh
docker rm -f aven-server
sudo systemctl enable --now aven-server
```

Then stop managing Aven in the checklist (or remove `AVEN_SELF_SERVICE`); otherwise the next deployment moves the server into a container again.

**Upgrading Docker** restarts the `aven-server` container like any other container with a restart policy.

## Troubleshooting

- **The deployment failed with `rolled back: …`**: the new server did not become healthy within 4 minutes. The deployment log contains the last 40 lines of the new server's output.
- **`… 스스로 교체할 수 없습니다`**: the server runs neither as the container nor as a systemd unit (for example in a terminal). Run it under systemd or bootstrap manually.
- **The first deployment failed with `bootstrap이 … 옮기지 못하고 끝났습니다`**: the deployment log contains the bootstrap unit's journal. `journalctl -u 'aven-selfbootstrap-*'` shows it too.
- **`… 로컬 노드에 replica 1개로만 배포할 수 있습니다`**: remove placement and extra replicas from the self service.
- **No server is running after a failed rollback**: `docker rename aven-server-prev aven-server && docker start aven-server`, or go back to systemd as above.
