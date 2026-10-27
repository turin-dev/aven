# aven-server image for running Aven on Aven (docs/selfhost.md).
# Aven builds this image itself when the repository is deployed as the self service (AVEN_SELF_SERVICE).
# A plain `docker build -t aven-server .` works too (bootstrap).

ARG GO_VERSION=1.26.10
ARG NODE_VERSION=24
ARG DOCKER_VERSION=29
ARG RAILPACK_VERSION=v0.42.0

FROM node:${NODE_VERSION}-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:${GO_VERSION}-bookworm AS go
WORKDIR /src
# VERSION wins; otherwise Aven passes the deployed commit as AVEN_GIT_COMMIT_SHA (short SHA).
# KEEL_GIT_COMMIT_SHA still works for images built by an older server.
ARG VERSION=
ARG AVEN_GIT_COMMIT_SHA=
ARG KEEL_GIT_COMMIT_SHA=
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/out ./internal/webui/dist
RUN if [ -z "$AVEN_GIT_COMMIT_SHA" ] && [ -n "$KEEL_GIT_COMMIT_SHA" ]; then echo "warning: KEEL_GIT_COMMIT_SHA is set; use AVEN_GIT_COMMIT_SHA (the old name still works)" >&2; fi \
 && v="${VERSION:-$(printf '%.7s' "${AVEN_GIT_COMMIT_SHA:-$KEEL_GIT_COMMIT_SHA}")}" \
 && touch internal/webui/dist/.gitkeep \
 && make server cli agents clis VERSION="${v:-dev}" \
 && find bin -type l -delete

FROM docker:${DOCKER_VERSION}-cli AS docker

FROM debian:bookworm-slim
ARG RAILPACK_VERSION
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git iproute2 iptables openssh-client \
 && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    case "$TARGETARCH" in amd64) rp=x86_64 ;; arm64) rp=arm64 ;; *) echo "unsupported arch $TARGETARCH" >&2; exit 1 ;; esac; \
    f="railpack-${RAILPACK_VERSION}-${rp}-unknown-linux-musl.tar.gz"; \
    base="https://github.com/railwayapp/railpack/releases/download/${RAILPACK_VERSION}"; \
    cd /tmp; \
    curl --proto =https --tlsv1.2 -fsSL --retry 3 -o "$f" "$base/$f"; \
    curl --proto =https --tlsv1.2 -fsSL --retry 3 -o rp-checksums.txt "$base/checksums.txt"; \
    awk -v n="$f" '$2 == n { print $1 "  " n }' rp-checksums.txt | grep . | sha256sum -c -; \
    tar -xzf "$f" -C /usr/local/bin railpack; \
    rm -f "$f" rp-checksums.txt; \
    railpack --version
COPY --from=docker /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/docker-buildx
COPY --from=go /src/bin/ /usr/local/bin/
RUN ln -s aven-server /usr/local/bin/keel-server && ln -s aven /usr/local/bin/keel
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/doc/aven/
# The agent and CLI downloads are served from next to aven-server (AVEN_AGENT_BIN_DIR is overridden in the container).
CMD ["aven-server"]
