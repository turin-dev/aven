# Aven 셀프 호스팅

[English](self-hosting.md) · [Windows 네이티브 서버](windows-native.ko.md)

이 문서는 Linux 서버 한 대에 Aven을 설치하고, 처음 로그인해서 초기 설정 체크리스트를 마치는 과정을 안내합니다. 서버를 더 붙이는 방법은 마지막에 있습니다.

Aven은 Docker 옆에서 서버 프로세스 하나(`aven-server`)로 동작합니다. 80/443 포트를 맡는 reverse proxy 컨테이너(`aven-proxy`, Caddy)를 직접 띄워 TLS 인증서를 발급하고 서비스로 트래픽을 보냅니다. 이미지는 [Railpack](https://github.com/railwayapp/railpack)이나 Dockerfile로 빌드합니다.

> 설정 이름은 `AVEN_*`입니다(`/etc/aven/aven.env`, 데이터 `/var/lib/aven`, proxy `aven-proxy`). 기존 설치는 `KEEL_*`, `/etc/keel`, `/var/lib/keel`, 돌고 있는 `keel-proxy`를 그대로 둬도 됩니다. Aven은 새 이름을 먼저 읽고, 없으면 예전 이름을 씁니다.

## 1. 준비물

설치 스크립트가 볼 수 있는 것만 확인합니다. 메모리, 디스크, Docker 버전 번호는 재지 않습니다.

- Linux, amd64(`x86_64`) 또는 arm64(`aarch64`), systemd(`systemctl`과 `/run/systemd/system`). `curl`, `tar`, 그리고 `sha256sum` 또는 `openssl`.
- Docker: `docker` 명령이 있고, root로 `docker info`가 성공해야 합니다. `deploy/aven-server.service`에 `User=`가 없어서 `aven-server`는 root로 돕니다. 프록시 이미지는 `caddy:2-alpine`입니다. Docker가 없으면 설치 스크립트가 get.docker.com을 실행할 수 있습니다(`--install-docker`, 또는 물어볼 때 확인).
- 메모리와 디스크는 검사하지 않습니다. `aven-server`는 프로세스 하나, 프록시는 컨테이너 하나이고, 앱과 데이터베이스는 컨테이너가 더 생깁니다. 데이터베이스 서비스는 Docker 볼륨을 씁니다. 제어 평면(`aven.db`, `master.key`, `session.key`, `logs/`)은 `AVEN_DATA`에 있으며 새 설치의 기본값은 `/var/lib/aven`입니다. 올릴 이미지와 볼륨을 담을 디스크를 두세요.
- 호스트의 80/tcp, 443/tcp, 443/udp가 비어 있어야 합니다. 프록시가 셋을 모두 publish합니다. 443/udp는 HTTP/3입니다. API는 `AVEN_LISTEN`을 바꾸지 않으면 `127.0.0.1:7070`을 듣고, 그 포트는 publish하지 않습니다.
- 자동 TLS를 쓰려면 공인 IPv4 또는 IPv6와, 직접 관리하는 도메인이 필요합니다. [DNS](#2-dns)를 보세요. 도메인이 없으면 대시보드는 그 머신의 `http://localhost:7070`에 둘 수 있습니다.
- 소스에서 빌드하려면 Go 1.26 이상, Node.js 20.9 이상과 npm, `make`.
- Dockerfile 없이 서비스를 빌드하려면 서버에 [Railpack](https://github.com/railwayapp/railpack/releases)이 있어야 합니다. 설치 스크립트는 v0.42.0을 `/usr/local/bin/railpack`에 둘 수 있습니다. `aven-server`는 `AVEN_RAILPACK_BIN`이 있으면 그것을 쓰고, 없으면 `PATH`의 `railpack` 또는 `/usr/local/bin/railpack`을 찾습니다. 서버가 Railpack을 받아 오지는 않습니다. `aven-agent`는 설정이 `auto`일 때(agent 기본값) 지정된 릴리스를 처음 쓸 때 받습니다.

## 2. DNS

인증서가 필요한 첫 요청 전에 레코드를 만드세요. 설치 스크립트는 인증서를 기다리지 않습니다.

| 이름 | 종류 | 값 |
| --- | --- | --- |
| `aven.example.com` | A 그리고/또는 AAAA | 서버의 공인 IPv4 그리고/또는 IPv6 |
| `*.example.com` | A 그리고/또는 AAAA | 같은 주소 |

대시보드 주소는 아무 이름이나 써도 되며 `AVEN_PUBLIC_URL`과 같기만 하면 됩니다. 와일드카드는 `AVEN_BASE_DOMAIN`과 맞아야 합니다.

와일드카드 기본 도메인의 TLS는 요청이 올 때 발급합니다. Aven은 `*.example.com` 자체를 덮는 인증서를 Let's Encrypt에 요청하지 않습니다. 그건 DNS-01 챌린지인데, 이 트리는 그 챌린지를 하지 않습니다. `web-production.example.com` 같은 구체적 호스트 이름은 첫 TLS 핸드셰이크 때 HTTP-01 또는 TLS-ALPN으로 인증서를 받습니다. 그때 인터넷에서 80과 443에 닿아야 합니다. AAAA는 이 호스트가 그 IPv6 주소로 응답할 때만 의미가 있습니다. 호스트가 받지 않는 주소를 가리키면 IPv6를 선호하는 클라이언트만 실패하고 IPv4는 됩니다.

머신과 도메인 비용은 직접 냅니다. 이 저장소는 사용량 요금을 받지 않습니다.

## 스크립트로 설치

머신을 바꾸는 파일입니다. 실행하기 전에 읽으세요.

```sh
curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh -o install-server.sh
less install-server.sh
sh install-server.sh --help
sh install-server.sh
```

스크립트가 디스크에 있으면 `sh install-server.sh --help`가 옵션 목록을 출력합니다. 파일 없이 `sh`로 파이프한 상태에서 `--help`를 주면, 셸이 본문을 보여 줄 파일이 없으므로 저장소 사본을 가리키는 한 줄만 출력합니다.

일반 사용자로 실행하면 스크립트가 `sudo`로 스스로를 다시 실행합니다. `sudo sh`로 파이프하면 sudo-rs(Ubuntu 25.10부터 기본 sudo)가 스크립트를 터미널의 백그라운드에 두어 질문을 할 수 없으므로, `--yes`가 없으면 안내를 출력하고 멈춥니다.

터미널에서 실행하면 첫 설치를 대화형으로 진행합니다. 대시보드 주소(비워 두면 `http://localhost:7070`으로 설치), 생성 서비스 이름에 쓸 기본 도메인, Let's Encrypt 이메일(선택), `admin@local` 비밀번호(Enter를 누르면 생성)를 묻고, Docker나 Railpack이 없으면 설치할지 묻습니다. 마지막에 요약을 보여 주고, 확인하기 전에는 아무것도 바꾸지 않습니다. 잘못된 답은 다시 묻고, 업그레이드는 한 번만 확인합니다. 무인 설치는 설정을 옵션으로 넘기고 `--yes`를 붙입니다.

```sh
curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh \
  | sudo sh -s -- --yes --public-url https://aven.example.com --acme-email you@example.com
```

스크립트는 root, systemd, amd64/arm64, `curl`, `tar`, 체크섬 도구, Docker를 확인합니다. 80과 443 포트는 경고만 합니다. `ss`로 TCP 리스너를 보고, 그래도 설치는 계속합니다. GitHub Releases에서 아키텍처에 맞는 릴리스를 받아 `checksums.txt`의 SHA-256을 확인합니다. `aven-server`, `aven` CLI, agent와 CLI 다운로드 파일을 설치하고, 건너뛰지 않았다면 Railpack도 설치합니다. 관리자 비밀번호와 웹훅 비밀값을 만들어 `/etc/aven/aven.env`에 쓰고, systemd unit을 설치해 시작한 뒤 `http://127.0.0.1:7070/api/cli/downloads`가 응답할 때까지 기다립니다(약 60초). 그다음 로그인 정보와 DNS 이름을 출력합니다. [켜졌는지 확인](#check-that-it-started)과 [첫 로그인](#7-첫-로그인과-온보딩)으로 넘어가세요.

| 옵션 | 뜻 |
| --- | --- |
| `--public-url URL` | 대시보드 주소, `https://`(생략하면 터미널에서 묻습니다). 도메인 없이 설치하면 대시보드는 서버 안의 `http://localhost:7070`이 되고, `ssh -L 7070:localhost:7070`으로 열 수 있습니다. 도메인은 나중에 붙이면 됩니다 |
| `--base-domain DOMAIN` | 서비스용 와일드카드 도메인(기본값: 공개 URL의 상위 도메인) |
| `--acme-email EMAIL` | Let's Encrypt 연락처(선택) |
| `--admin-password PW` | `admin@local` 비밀번호를 생성하지 않고 이 값 사용 |
| `--version TAG` | 최신 대신 특정 릴리스 설치 |
| `--from-dir DIR` | `make all`로 빌드한 로컬 체크아웃에서 설치 |
| `--install-docker` | Docker가 없으면 get.docker.com으로 설치 |
| `--no-railpack`, `--no-start`, `--yes` | Railpack 건너뛰기, 서비스 시작 안 함, 묻지 않기 |

같은 명령을 다시 실행하면 **업그레이드**합니다. `/etc/aven/aven.env`는 그대로 두고, DB를 `/var/lib/aven/backups/`에 복사하고, 이전 바이너리를 `/usr/local/bin/aven-server.prev`로 남긴 뒤 서버를 재시작합니다. 실행 중인 서비스는 계속 돌아갑니다. 복사 범위는 아래 표와 같습니다. `master.key`는 복사하지 않습니다.

직접 설치하려면 3~6단계를 따르세요.

<a id="installer-changes"></a>

### 설치 스크립트가 바꾸는 것

메인 서버, 첫 설치:

| 경로 | 내용 |
| --- | --- |
| `/usr/local/bin/aven-server` | 서버 바이너리 |
| `/usr/local/bin/aven` | CLI |
| `/usr/local/bin/railpack` | Railpack. 없었고 `--no-railpack`도 없을 때 |
| `/usr/local/lib/aven/` | 서버가 나눠 주는 `aven-agent`와 CLI 다운로드 |
| `/etc/systemd/system/aven-server.service` | systemd unit. `EnvironmentFile=/etc/aven/aven.env`. `KillMode=process`라서 서버를 멈춰도 컨테이너는 남습니다 |
| `/etc/aven/aven.env` | 설정, 모드 0600. 첫 설치에만 만들고, 업그레이드는 유지합니다 |
| `/etc/aven/aven.env.example` | 릴리스에 들어 있는 주석 달린 예시 |
| `/var/lib/aven/` | 서비스가 시작할 때 생깁니다(`StateDirectory=aven`과 `AVEN_DATA`). `aven.db`, `master.key`, `session.key`, `logs/` |

셸 스크립트가 아니라 첫 기동이 Docker 네트워크 `aven`과 컨테이너 `aven-proxy`(`caddy:2-alpine`, restart `unless-stopped`)를 만들고, 볼륨 `aven-caddy-data`, `aven-caddy-config`, `aven-proxy-wake`를 붙입니다.

업그레이드(`/etc/aven/aven.env`가 이미 있거나, 예전 `/etc/keel/aven.env` 또는 `/etc/keel/keel.env`가 있는 경우)는 그 env 파일을 유지하고 `aven-server`를 멈춘 뒤, `aven.db`와 `keel.db`가 있으면 `/var/lib/aven/backups/<이름>-pre-upgrade-<시각>.db`로 복사합니다. 옆의 `-wal` / `-shm`도 복사합니다. 이전 바이너리는 `/usr/local/bin/aven-server.prev`로 둡니다. `master.key`는 복사하지 않습니다. unit의 `KillMode`가 `process`라서 앱 컨테이너는 계속 돕니다.

리전 서버(`--role region`, 또는 `--server` / `--token`)는 `aven-server`를 설치하지 않습니다. 메인 서버의 `install-agent.sh`를 받아 실행하고, 그 스크립트가 다음을 씁니다.

| 경로 | 내용 |
| --- | --- |
| `/usr/local/bin/aven-agent` | agent 바이너리 |
| `/etc/keel-agent/agent.env` | 모드 0600. `AVEN_SERVER_URL`. `AVEN_JOIN_TOKEN`은 등록이 끝날 때까지만. 추가 설정도 `AVEN_*`로 저장합니다 |
| `/var/lib/keel-agent/credentials.json` | 노드 키와 인증서, 모드 0600 |
| `/etc/systemd/system/keel-agent.service` | unit. 이름은 아직 `keel-agent`입니다. 다시 실행하면 이미 쓰던 agent unit을 그 자리에서 바꿉니다 |

agent 경로는 오늘 새 설치도 이렇게 만듭니다. 설정 이름은 `AVEN_*`입니다. 예전 env 파일의 `KEEL_*`도 읽습니다.

`keel-server.service`가 켜져 있으면 스크립트는 멈추고, 같은 데이터 디렉터리로 둘을 같이 돌리지 말라고 합니다. `aven.self=server` 또는 `keel.self=server` 라벨의 컨테이너가 돌고 있으면, 대시보드에서 그 컨테이너를 올리라고 하고 멈춥니다.

<a id="check-that-it-started"></a>

### 켜졌는지 확인

```sh
systemctl is-active aven-server
curl -fsS -o /dev/null http://127.0.0.1:7070/api/cli/downloads
docker ps --filter name=aven-proxy --format '{{.Names}} {{.Status}}'
journalctl -u aven-server -n 50 --no-pager
```

`systemctl is-active`는 `active`를 출력합니다. `curl`은 설치 스크립트가 기다리는 요청과 같습니다. `docker ps`에는 `aven-proxy`가 `Up`으로 보여야 합니다. 그다음 로그인합니다.

<a id="3-build"></a>

## 3. 빌드

```sh
git clone https://github.com/turin-dev/aven.git
cd aven
make all        # 웹 UI, aven-server, aven-agent(amd64, arm64), 모든 플랫폼용 CLI
```

결과물은 `bin/`에 생깁니다.

## 4. 설치

```sh
sudo install -d -m 0755 /usr/local/lib/aven /etc/aven
sudo install -m 0755 bin/aven-server /usr/local/bin/aven-server
sudo install -m 0755 bin/aven-linux-amd64 /usr/local/bin/aven             # arm64면 aven-linux-arm64
# 새 노드 설치와 대시보드의 CLI 다운로드 화면이 이 파일들을 내려받습니다.
sudo install -m 0755 bin/aven-agent-linux-amd64 bin/aven-agent-linux-arm64 /usr/local/lib/aven/
sudo install -m 0755 bin/aven-linux-* bin/aven-darwin-* bin/aven-windows-*.exe /usr/local/lib/aven/
# Railpack(아키텍처에 맞는 최신 릴리스)
sudo install -m 0755 /path/to/railpack /usr/local/bin/railpack

sudo install -m 0644 deploy/aven-server.service /etc/systemd/system/aven-server.service
sudo install -m 0600 deploy/aven.env.example /etc/aven/aven.env
```

## 5. 설정

`/etc/aven/aven.env`(root, 0600)를 고칩니다. 최소 설정은 다음과 같습니다.

```sh
AVEN_DATA=/var/lib/aven
AVEN_LISTEN=127.0.0.1:7070
AVEN_PUBLIC_URL=https://aven.example.com
AVEN_BASE_DOMAIN=example.com
AVEN_ACME_EMAIL=you@example.com
# 기본 owner 계정 admin@local의 비밀번호. 8자 이상이며 기동할 때마다 필요합니다.
AVEN_ADMIN_PASSWORD=change-me-to-a-long-random-password
```

비밀번호는 `openssl rand -base64 24`로 만들면 됩니다.

`AVEN_PUBLIC_URL`이 `https://` 주소이면 proxy가 그 호스트를 `aven-server` 자신에게 보내므로 별도의 reverse proxy가 필요 없습니다. 이미 80/443을 쓰는 reverse proxy가 있다면 `AVEN_PROXY_BIND=127.0.0.1`로 두고 `AVEN_HTTP_PORT`/`AVEN_HTTPS_PORT`를 다른 포트로 옮긴 뒤 `AVEN_TLS=off`로 설정하고 그 포트로 전달하세요.

선택 설정입니다. 모두 초기 설정 체크리스트에도 나옵니다.

| 기능 | 변수 |
| --- | --- |
| GitHub 저장소 연결과 push 자동 배포 | `AVEN_GITHUB_CLIENT_ID`, `AVEN_GITHUB_CLIENT_SECRET`, `AVEN_GITHUB_WEBHOOK_SECRET` |
| 메일(초대, 비밀번호 재설정, 알림) | `AVEN_SMTP_HOST`, `AVEN_SMTP_PORT`, `AVEN_SMTP_USERNAME`, `AVEN_SMTP_PASSWORD`, `AVEN_SMTP_FROM`, `AVEN_SMTP_TLS` |
| 이메일로 지정하는 추가 서버 owner | `AVEN_SUPERADMINS` |
| 노드 간 사설망 | `AVEN_PRIVATE_ADDRESS`, `AVEN_MESH` |

GitHub은 *GitHub → Settings → Developer settings → OAuth Apps*에서 OAuth App을 만들고 다음처럼 입력합니다.

- Homepage URL: `https://aven.example.com`
- Authorization callback URL: `https://aven.example.com/api/github/oauth/callback`

웹훅 비밀값은 `openssl rand -hex 32`로 만듭니다. 저장소 웹훅 주소는 `https://aven.example.com/api/webhooks/github`이며, GitHub을 연결하면 Aven이 대신 만들어 줄 수 있습니다.

## 6. 시작

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now aven-server
journalctl -u aven-server -f
```

서버는 시작할 때 Docker를 확인하고 네트워크를 만든 뒤 `aven-proxy`를 띄웁니다. 이미 `keel-proxy`가 돌고 있으면 그 컨테이너를 유지합니다. 비밀번호가 없거나, Docker에 닿지 않거나, 80/443을 누군가 쓰고 있으면 journal에 오류를 남기고 종료합니다. 비밀번호 오류는 `AVEN_ADMIN_PASSWORD (or KEEL_ADMIN_PASSWORD) must be set to at least 8 characters`입니다. 프록시 오류에는 `start the proxy:`와 `ports AVEN_HTTP_PORT/AVEN_HTTPS_PORT, default 80/443, must be free`가 들어 있습니다. Docker 오류에는 `docker is not reachable`이 있고, 이 사용자가 소켓을 써도 되는지 묻습니다.

## 7. 첫 로그인과 온보딩

1. `https://aven.example.com`을 열고 **이메일** `admin@local`(또는 빈칸)과 `AVEN_ADMIN_PASSWORD`로 로그인합니다.
2. 필수 항목이 남아 있는 동안 서버 owner에게 **초기 설정** 안내가 뜹니다. 언제든 **어드민 → Owner · 초기 설정**(`/admin/?section=setup`)에서 다시 볼 수 있습니다. 각 항목은 서버 설정을 읽어 자동으로 체크됩니다.

   | 항목 | 필수 | 마치는 방법 |
   | --- | --- | --- |
   | 공개 URL | 예 | `AVEN_PUBLIC_URL`이 `https://` 주소 |
   | 기본 도메인 | 예 | `AVEN_BASE_DOMAIN`을 설정하고 와일드카드 DNS가 이 서버를 가리킴 |
   | 개인 owner 계정 | 예 | *어드민 → 사용자·초대*에서 나를 초대해 수락한 뒤 *어드민 → Owner · 권한*에서 owner로 지정 |
   | 2단계 인증 | 예 | *계정 → 보안*에서 OTP 앱이나 패스키 등록 |
   | 첫 프로젝트 배포 | 예 | 프로젝트를 만들고 서비스 배포 |
   | GitHub 연동 | 아니요 | OAuth App과 웹훅 비밀값(위 참고) |
   | 메일(SMTP) | 아니요 | `AVEN_SMTP_*` |
   | 백업 저장소 | 아니요 | 데이터베이스 서비스의 *설정 → DB 백업*에서 S3 호환 저장소 추가 |
   | 서버 노드 추가 | 아니요 | [서버 추가](#9-서버-추가) 참고 |

3. 개인 owner 계정이 생기면 평소에는 그 계정을 쓰고, `admin@local`은 긴 비밀번호를 둔 비상용 계정으로 남겨 두세요.

환경 변수로 정하는 설정은 `sudo systemctl restart aven-server` 뒤에 반영됩니다. 서버를 재시작해도 실행 중인 서비스는 계속 돌아갑니다.

<a id="8-backups-and-upgrades"></a>

## 8. 백업과 업그레이드

사용자, 프로젝트, 변수, 암호화된 비밀값처럼 Aven이 아는 것은 `AVEN_DATA`에 있습니다. 새 설치는 `/var/lib/aven`이고, 그 디렉터리가 아직 쓰이는 설치는 `/var/lib/keel`입니다.

- `aven.db`: 새 설치의 SQLite 데이터베이스입니다. `aven.db`가 옆에 없으면 `keel.db`라는 예전 파일을 엽니다. 살아있는 `keel.db` 옆에 빈 `aven.db`를 만들어도 예전 데이터가 옮겨지지는 않습니다.
- `master.key`: 그 DB의 비밀값을 암호화하는 키입니다. **같은 키가 없으면 DB 백업을 복호화할 수 없습니다.**
- `session.key`, `logs/`, `backups/` 아래 파일

디렉터리 전체를 복사하고, `master.key`를 DB와 함께 보관하세요. *어드민 → Owner · DB 백업*은 서버가 도는 동안 SQLite `VACUUM INTO`로 일관된 사본을 만들고, 그것을 받을 수 있습니다. **돌고 있는** DB를 직접 복사할 때는 `cp`가 아니라 SQLite 백업 API를 쓰세요.

```sh
sudo mkdir -p /var/lib/aven/backups
sudo sqlite3 /var/lib/aven/aven.db ".backup '/var/lib/aven/backups/aven-$(date +%F).db'"
sudo cp -p /var/lib/aven/master.key /안전한/위치/master.key
```

`ls /var/lib/aven/aven.db`가 실패하고 `keel.db`가 있으면 그 파일을 그 이름으로 백업하세요. `sqlite3`가 없으면 서버를 먼저 멈추세요. `sudo systemctl stop aven-server` 뒤에 `aven.db`(있으면 `aven.db-wal` / `aven.db-shm`도)와 `master.key`를 복사하고 다시 시작합니다.

**제어 평면 복원.** `aven-server`를 멈춥니다. `aven.db`를 백업으로 바꿉니다. `aven.db-wal`과 `aven.db-shm`은 지우세요. 다른 파일의 로그를 SQLite가 다시 적용하지 않게 하려는 것입니다. 그 백업에 속한 `master.key`를 되돌려 놓고 `aven-server`를 시작합니다. 이 정지 동안 앱 컨테이너는 계속 돕니다(`KillMode=process`). 대시보드는 서버가 올라올 때까지 끊깁니다. 이렇게 하면 사용자, 프로젝트, 설정이 돌아옵니다. Postgres나 MySQL 볼륨 안의 바이트는 돌아오지 않습니다.

**데이터베이스 서비스 백업.** 데이터베이스 서비스는 논리 덤프를 로컬 파일이나, *설정 → DB 백업*에 추가한 S3 호환 저장소로 보낼 수 있습니다. 복원은 같은 프로젝트의 같은 엔진 서비스에 쓰며, 그 서비스의 현재 데이터를 바꿉니다. 서버가 이것을 저절로 켜지는 않습니다.

**시점 복구.** `aven pitr`(그리고 Postgres 또는 MySQL 서비스의 *설정*)은 Postgres WAL 또는 MySQL binlog를 같은 종류의 백업 저장소로 보냅니다. 저장소를 정하지 않으면 로컬입니다. Postgres를 켜면 그 데이터베이스가 한 번 재시작될 수 있습니다. `aven pitr restore --at <시각>`은 `--in-place`가 없으면 `<이름>-restored-...`라는 새 서비스로 복원합니다. `--in-place`는 그 서비스를 덮어씁니다. 시각은 RFC3339, 로컬 `YYYY-MM-DD HH:MM[:SS]`, 또는 지난 시간(`30m`, `2h`, `1d`, `1w`)입니다. 명령 설명은 [docs/cli.md](cli.md)에 있습니다. Redis와 MongoDB는 이 PITR 경로에 없습니다.

**설치 스크립트로 업그레이드.** `install-server.sh`를 다시 실행합니다. env 파일을 유지하고 `aven-server`를 멈춘 뒤, [설치 스크립트가 바꾸는 것](#installer-changes)에 적힌 대로 DB 파일을 복사하고 바이너리를 바꾼 다음 서버를 시작합니다. 마이그레이션은 시작할 때 실행됩니다. 먼저 릴리스 노트를 읽으세요. 시작하기 전에 `master.key`는 직접 복사하세요. 앱 컨테이너는 계속 돕니다. 다른 머신의 agent는 서버 설치 스크립트가 올리지 않습니다. 서버가 올라온 뒤 메인 서버의 `install-agent.sh`로 agent 설치를 다시 실행하세요. 노드가 이미 등록됐으면 토큰은 필요 없습니다.

**체크아웃에서 업그레이드.**

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

쓰이는 파일이 `keel.db`이면 같은 방식으로 복사하세요. `-wal`과 `-shm`이 있으면 함께 복사하세요.

**업그레이드 되돌리기.** `aven-server`를 멈춥니다. `/usr/local/bin/aven-server.prev`를 `/usr/local/bin/aven-server`로 되돌립니다. `pre-upgrade` 복사본에서 DB 파일을 복원하고, 그 복사본에 없던 `-wal` / `-shm`은 지웁니다. 서버를 시작합니다. 새 마이그레이션이 이미 DB를 고쳤다면 예전 바이너리가 그 DB를 거부할 수 있으므로, 바이너리와 DB를 함께 되돌리세요.

`aven-server`가 [Aven on Aven](selfhost.ko.md)의 Docker 컨테이너이면(라벨 `aven.self=server`) 설치 스크립트는 진행을 거부합니다. `aven-server` 서비스를 배포해서 그 컨테이너를 올리세요. systemd unit과 그 컨테이너를 같은 데이터 디렉터리로 같이 돌리지 마세요.

## 9. 서버 추가

다른 서버의 `aven-agent`는 HTTPS로 `aven-server`에 밖으로 접속합니다. agent는 대시보드 포트를 열지 않습니다. *어드민 → 서버 노드 → 노드 추가*에서 참여 토큰을 만든 뒤, Docker가 있는 새 서버에서 실행합니다.

```sh
curl -fsSL https://aven.example.com/install-agent.sh | sudo sh -s -- --server https://aven.example.com --token <TOKEN>
```

셸에 파이프하기 전에 브라우저나 `curl`로 `https://aven.example.com/install-agent.sh`를 읽으세요.

서버 설치 스크립트로도 됩니다. 새 서버에서 `install-server.sh`를 실행하고 "What is this server?"에서 **Region server**를 고르면 메인 서버 URL, 참여 토큰, 사설 주소(선택)를 묻고, 원하면 Docker를 설치한 뒤 메인 서버의 `install-agent.sh`를 실행합니다. 무인 설치:

```sh
curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh | sudo sh -s -- --yes --server https://aven.example.com --token <TOKEN>
```

토큰은 한 번만 씁니다. 기본 수명은 1시간입니다. API는 `ttl_s`로 60에서 86400까지 받습니다. agent는 자기 키를 만들고 Aven 내부 CA에서 인증서를 받습니다. `credentials.json`이 생기면 `/etc/keel-agent/agent.env`에서 토큰 줄을 지웁니다. 설치 스크립트는 그 파일을 최대 60초 기다리고, `keel-agent`가 아직 활성이고 재시작하지 않았는지 확인합니다. 나중에 같은 명령을 다시 실행하면 바이너리를 올리고 자격 증명은 유지합니다. 다른 메인 서버에 붙이려면 `--reenroll`과 새 토큰으로 `install-agent.sh`를 실행하세요.

토큰을 만들기 전에 메인 서버의 `AVEN_PUBLIC_URL`은 `https://`여야 합니다. 아니면 API가 503을 주고 `AVEN_PUBLIC_URL`을 이름에 넣습니다. agent는 등록 중 HTTP 리다이렉트를 따라가지 않으므로, URL은 `/api/agent/enroll`을 직접 제공하는 주소여야 합니다.

서버들이 사설망(WireGuard, Tailscale)으로 이어져 있으면 각 서버의 그 주소를 `AVEN_PRIVATE_ADDRESS`로 두세요. 메인 서버는 `/etc/aven/aven.env`입니다. 리전 설치 스크립트는 주소를 받으면 `/etc/keel-agent/agent.env`에 `AVEN_PRIVATE_ADDRESS`를 씁니다. mesh 게이트웨이는 그 주소만 듣고, 포트는 7443입니다(`AVEN_MESH_PORT`).

<a id="uninstall-and-roll-back"></a>

## 제거와 되돌리기

제거 명령은 없습니다. 아래는 설치 스크립트가 만든 메인 서버를 걷어 내는 절차입니다. 앱 볼륨은 지우지 않습니다. 데이터를 지울 생각일 때만 따로 지우세요.

```sh
sudo systemctl disable --now aven-server
sudo docker rm -f aven-proxy
sudo rm -f /usr/local/bin/aven-server /usr/local/bin/aven-server.prev /usr/local/bin/aven /etc/systemd/system/aven-server.service
sudo systemctl daemon-reload
```

`/etc/aven/aven.env`에는 관리자 비밀번호와 GitHub 웹훅 비밀값이 있습니다. `/var/lib/aven`에는 `aven.db`와 `master.key`가 있습니다. 이 두 디렉터리는, 버려도 되는 복사본을 갖고 있거나 제어 평면을 되돌릴 생각이 없을 때만 지우세요. 프록시 볼륨 `aven-caddy-data`, `aven-caddy-config`, `aven-proxy-wake`에는 인증서와 프록시 소켓이 있습니다. 그 셋을 `docker volume rm`하면 인증서가 사라집니다. 그 위의 컨테이너가 없으면 Docker 네트워크 `aven`은 `docker network rm aven`으로 지울 수 있습니다. Aven 라벨이 붙은 앱 컨테이너와 볼륨은 위 명령으로 지워지지 않습니다.

리전 서버:

```sh
sudo systemctl disable --now keel-agent
sudo rm -f /usr/local/bin/aven-agent /etc/systemd/system/keel-agent.service
sudo rm -rf /etc/keel-agent /var/lib/keel-agent
sudo systemctl daemon-reload
```

agent가 띄운 그 노드의 `aven-proxy`는 별도 컨테이너입니다. `docker rm -f aven-proxy`로 지웁니다. agent가 서비스용으로 띄운 컨테이너는 직접 지우기 전까지 남습니다.

제거하지 않고 업그레이드만 되돌리는 방법은 [업그레이드 되돌리기](#8-backups-and-upgrades)입니다.

<a id="troubleshooting"></a>

## 문제 해결

- **80 또는 443 포트를 쓰는 중.** 설치 스크립트는 `Port 80 is in use` 또는 `Port 443 is in use`(또는 둘 다)를 출력하고 계속합니다. 그다음 `aven-server`가 종료됩니다. `journalctl -u aven-server`에 `start the proxy:`와 `ports AVEN_HTTP_PORT/AVEN_HTTPS_PORT, default 80/443, must be free`가 있습니다. `ss -Htlpn 'sport = :80'`과 443에 대한 같은 명령으로 리스너를 봅니다. 포트를 비우거나, 호스트의 다른 프록시가 80/443을 쓰면 `AVEN_HTTP_PORT`와 `AVEN_HTTPS_PORT`를 빈 포트로 두고 `AVEN_PROXY_BIND=127.0.0.1`로 하세요. 그 프록시가 TLS를 끝내면 `AVEN_TLS=off`로 두고 고른 포트로 전달하세요. `aven-server`를 재시작합니다.
- **DNS가 퍼지지 않았거나 인증서가 안 나옴.** 설치는 인증서를 요청하지 않습니다. `getent`가 대시보드 호스트를 해석하지 못하면 설치 스크립트가 `<호스트> does not resolve yet`라고 경고합니다. DNS가 이 서버를 가리킨 뒤, 구체적 이름에 대한 첫 HTTPS 요청이 발급을 시작합니다. ACME 오류는 `docker logs aven-proxy`에 있습니다. `keel-proxy`가 실제로 돌 때만 `docker logs keel-proxy`를 보세요. 80과 443은 호스트 안에서만이 아니라 인터넷에서 닿아야 합니다. 이름이 다른 주소로 해석되거나, 이 호스트가 응답하지 않는 AAAA이면 그 주소를 쓰는 클라이언트의 챌린지가 실패합니다. `*.example.com` 자체에 대한 와일드카드 인증서는 없습니다.
- **Docker 권한, 또는 데몬이 멈춤.** 설치 스크립트는 root로 `docker info`를 실행합니다. 권한 실패는 `cannot use its socket`이라고 하고 `docker info` 출력을 붙입니다. 데몬이 멈춘 경우는 `not running`이라고 하고 같은 출력을 붙입니다. `systemctl status docker`로 "inactive"와 소켓 오류를 구분합니다. unit은 `aven-server`를 root로 실행합니다. 다른 사용자로 바이너리를 직접 시작하면, 그 사용자가 Docker 소켓을 열 수 없을 때 `docker is not reachable`과 `may this user use its socket?`으로 실패합니다. unit 파일이 구성하는 방식은 rootless Docker가 아닙니다.
- **방화벽 또는 IPv6.** Docker로 포트를 publish해도 ufw의 INPUT 체인을 타지 않습니다. env 예시도 TCP 프록시 포트에 대해 같은 말을 합니다. 호스트 방화벽이 보게 하려면 `DOCKER-USER` 체인에 규칙을 넣어야 합니다. 머신 앞의 클라우드 보안 그룹은 별개이고 80/443을 막을 수 있습니다. 기본 publish는 `80:80`, `443:443`, `443/udp`이고 Docker가 바인드하는 모든 인터페이스입니다. 호스트에 동작하는 공인 IPv6가 없으면 그 주소의 AAAA를 알리지 마세요. 도메인별 IPv6 주소(`AVEN_IPV6_POOL`)는 선택이고, 첫 인증서에 필요하지 않습니다.
- **agent가 연결되지 않음.** 새 머신에서 `systemctl status keel-agent`와 `journalctl -u keel-agent`를 보세요. `/var/lib/keel-agent/credentials.json`이 생기지 않으면 설치 스크립트는 `the agent did not join within 60s`로 끝나고, 토큰이 잘못됐거나 만료됐을 수 있다고 합니다. agent 로그는 `enrollment HTTP 401`입니다. 그 상태의 API 본문은 `invalid join token`(토큰이 64자가 아님) 또는 `invalid or expired join token`입니다. agent는 그 본문이 아니라 상태 코드만 기록합니다. 토큰을 새로 만드세요. agent가 있는 머신에서 넘긴 `https://` URL의 메인 서버에 닿아야 합니다(`curl -fsSI https://aven.example.com/install-agent.sh`). 리다이렉트는 따라가지 않습니다. agent 머신의 Docker는 `docker info`에 응답해야 합니다. 등록 이후 연결이 끊기면 `disconnected from server; containers keep running`이라고 기록하고 agent가 다시 시도합니다. 그 WebSocket이 돌아올 때까지 노드는 오프라인으로 보입니다. 이 연결을 위해 agent 머신에 들어오는 포트는 필요 없습니다.
- **`AVEN_ADMIN_PASSWORD (or KEEL_ADMIN_PASSWORD) must be set to at least 8 characters`:** `/etc/aven/aven.env`에 넣고 재시작하세요. 설치 스크립트는 8자보다 짧은 비밀번호를 거부하고, env 파일에서 값이 한 줄이기 때문에 탭, 줄바꿈, 따옴표, 백슬래시, `$`, 백틱, 공백도 거부합니다.
- **빌드가 `railpack을 찾을 수 없습니다 (AVEN_RAILPACK_BIN (or KEEL_RAILPACK_BIN))`로 실패:** Railpack을 `/usr/local/bin/railpack`에 설치하거나 `AVEN_RAILPACK_BIN`을 설정하세요.
- **LAN에서 일반 HTTP로 로그인**(평가용만): `AVEN_INSECURE_COOKIE=1`을 설정하세요. 세션 쿠키는 기본적으로 `Secure`입니다. 대시보드 URL이 `http://localhost`이면 설치 스크립트가 이 값을 직접 넣습니다.

## 알려진 한계

README의 [기능 안정성](../README.ko.md#기능-안정성) 표와 코드에 맞춘 내용입니다.

- 제어 평면은 메인 서버의 SQLite 데이터베이스 하나입니다. 이 트리에는 두 번째 제어 평면 복제본이 없습니다.
- 이 트리에는 git receive-pack 서버가 없습니다. push 배포는 GitHub 웹훅입니다.
- 앱 sleep, cron 일정, 웹 대시보드, Cloudflare Tunnel 진입, AI 어시스턴트는 실험적입니다. 이유는 그 표에 있습니다. 대시보드는 설치하면 쓰는 UI입니다. `web/`에는 컴포넌트 테스트나 종단간 테스트가 없습니다.
- 논리 백업과 PITR은 켜기 전에는 돌지 않습니다. 여기서 PITR은 Postgres WAL과 MySQL binlog이며, 모든 데이터베이스가 아닙니다.
- 새 설치에서도 agent unit과 디렉터리 이름은 `keel-agent`입니다(`/etc/keel-agent`, `/var/lib/keel-agent`). env 파일 안의 설정 이름은 `AVEN_*`입니다.
- 설치 스크립트는 메모리, 디스크, Docker 버전, DNS 이름이 이 머신을 가리키는지를 확인하지 않습니다. 이름이 아예 해석되지 않을 때만 경고합니다.
- 새 설치는 설치 중에 인증서를 발급하지 않습니다. TLS는 호스트 이름마다 첫 핸드셰이크 때 일어나고, 80과 443이 닿을 때만 됩니다.
- Aven 위의 `aven-server`(자기 업그레이드 컨테이너)는 특권 컨테이너이고 Docker 소켓을 마운트합니다. 그 경로는 [Aven on Aven](selfhost.ko.md)에 있고, 선택입니다.
