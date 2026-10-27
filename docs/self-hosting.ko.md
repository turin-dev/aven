# Aven 셀프 호스팅

[English](self-hosting.md)

이 문서는 Linux 서버 한 대에 Aven을 설치하고, 처음 로그인해서 초기 설정 체크리스트를 마치는 과정을 안내합니다. 서버를 더 붙이는 방법은 마지막에 있습니다.

Aven은 Docker 옆에서 서버 프로세스 하나(`aven-server`)로 동작합니다. 80/443 포트를 맡는 reverse proxy 컨테이너(`aven-proxy`, Caddy)를 직접 띄워 TLS 인증서를 발급하고 서비스로 트래픽을 보냅니다. 이미지는 [Railpack](https://github.com/railwayapp/railpack)이나 Dockerfile로 빌드합니다.

> 설정 이름은 `AVEN_*`입니다(`/etc/aven/aven.env`, 데이터 `/var/lib/aven`, proxy `aven-proxy`). 기존 설치는 `KEEL_*`, `/etc/keel`, `/var/lib/keel`, 돌고 있는 `keel-proxy`를 그대로 둬도 됩니다. Aven은 새 이름을 먼저 읽고, 없으면 예전 이름을 씁니다.

## 1. 준비물

- 공인 IPv4 또는 IPv6 주소가 있는 Linux 서버(amd64 또는 arm64). vCPU 2개, RAM 4 GB면 넉넉하게 시작할 수 있습니다.
- systemd 서비스로 도는 최신 Docker Engine
- 비어 있는 80, 443 포트(Aven proxy가 사용)
- 관리할 수 있는 도메인. 예를 들어 `example.com`이라면:
  - `aven.example.com`: 대시보드와 API
  - `*.example.com`(또는 `*.apps.example.com`): 서비스에 자동으로 붙는 주소
- 소스에서 빌드하려면 Go 1.26 이상, Node.js 20.9 이상과 npm, `make`
- 서버에 [Railpack](https://github.com/railwayapp/railpack/releases): Dockerfile 없이 소스 코드로 빌드할 때 필요합니다.

## 2. DNS

proxy가 첫 요청 때 인증서를 받을 수 있도록 미리 만들어 둡니다.

| 이름 | 종류 | 값 |
| --- | --- | --- |
| `aven.example.com` | A / AAAA | 서버의 공인 IPv4 / IPv6 |
| `*.example.com` | A / AAAA | 서버의 공인 IPv4 / IPv6 |

대시보드 주소는 아무 이름이나 써도 되며 `AVEN_PUBLIC_URL`과 같기만 하면 됩니다. 와일드카드는 `AVEN_BASE_DOMAIN`과 맞아야 합니다.

## 스크립트로 설치

DNS를 준비한 새 서버라면 명령 하나로 최신 릴리스를 설치할 수 있습니다.

```sh
curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh \
  | sudo sh -s -- --public-url https://aven.example.com --acme-email you@example.com
```

스크립트는 시스템(root, systemd, amd64/arm64, Docker, 80/443 포트)을 확인하고, GitHub Releases에서 아키텍처에 맞는 릴리스를 내려받아 SHA-256 체크섬을 검증합니다. `aven-server`, `aven` CLI, agent와 CLI 다운로드 파일, Railpack을 설치합니다. 관리자 비밀번호와 웹훅 비밀값을 생성해 `/etc/aven/aven.env`를 만들고, systemd unit을 설치해 시작한 뒤, 서버가 응답할 때까지 기다렸다가 로그인 정보와 만들어야 할 DNS 이름을 출력합니다. 그다음 [첫 로그인](#7-첫-로그인과-온보딩)으로 넘어가세요.

| 옵션 | 뜻 |
| --- | --- |
| `--public-url URL` | 대시보드 주소, `https://`(생략하면 터미널에서 묻습니다) |
| `--base-domain DOMAIN` | 서비스용 와일드카드 도메인(기본값: 공개 URL의 상위 도메인) |
| `--acme-email EMAIL` | Let's Encrypt 연락처(선택) |
| `--admin-password PW` | `admin@local` 비밀번호를 생성하지 않고 이 값 사용 |
| `--version TAG` | 최신 대신 특정 릴리스 설치 |
| `--from-dir DIR` | `make all`로 빌드한 로컬 체크아웃에서 설치 |
| `--install-docker` | Docker가 없으면 get.docker.com으로 설치 |
| `--no-railpack`, `--no-start`, `--yes` | Railpack 건너뛰기, 서비스 시작 안 함, 묻지 않기 |

같은 명령을 다시 실행하면 **업그레이드**합니다. `/etc/aven/aven.env`는 그대로 두고, DB를 `/var/lib/aven/backups/`에 복사하고, 이전 바이너리를 `/usr/local/bin/aven-server.prev`로 남긴 뒤 서버를 재시작합니다. 실행 중인 서비스는 계속 돌아갑니다.

직접 설치하려면 3~6단계를 따르세요.

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

서버는 시작할 때 Docker를 확인하고 네트워크를 만든 뒤 `keel-proxy`를 띄웁니다. `AVEN_ADMIN_PASSWORD`가 없거나 Docker에 연결할 수 없으면 journal에 오류를 남기고 종료합니다.

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

## 8. 백업과 업그레이드

Aven이 아는 모든 것(사용자, 프로젝트, 변수, 암호화된 비밀값)은 `AVEN_DATA`에 있습니다.

- `keel.db`: SQLite 데이터베이스
- `master.key`: DB 안의 비밀값을 암호화하는 키. **이 파일이 없으면 DB 백업을 복호화할 수 없습니다.**
- `session.key`, `logs/`, 로컬 백업

디렉터리 전체를 백업하세요. *어드민 → Owner · DB 백업*에서 일관된 DB 사본을 만들어 내려받을 수 있습니다. 직접 복사하려면 다음과 같이 합니다.

```sh
sudo sqlite3 /var/lib/aven/keel.db ".backup '/var/lib/aven/backups/keel-$(date +%F).db'"
sudo cp /var/lib/aven/master.key /안전한/위치/
```

업그레이드는 새 리비전을 빌드하고, 백업하고, 바이너리를 바꾼 뒤 재시작합니다.

```sh
git pull && make all
sudo cp /usr/local/bin/aven-server /usr/local/bin/aven-server.prev
sudo install -m 0755 bin/aven-server /usr/local/bin/aven-server
sudo install -m 0755 bin/aven-agent-linux-* bin/aven-linux-* bin/aven-darwin-* bin/aven-windows-*.exe /usr/local/lib/aven/
sudo systemctl restart aven-server
```

DB 마이그레이션은 시작할 때 자동으로 실행됩니다. 되돌리려면 서버를 멈추고 이전 바이너리와 DB 백업을 복원한 뒤 다시 시작하세요.

직접 업그레이드하는 대신 Aven이 자기 자신을 배포하게 할 수도 있습니다. [Aven on Aven](selfhost.ko.md)을 보세요.

## 9. 서버 추가

다른 서버에서는 `aven-agent`가 HTTPS로 `aven-server`에 접속합니다. *어드민 → 서버 노드 → 노드 추가*에서 참여 토큰을 만든 뒤, Docker가 설치된 새 서버에서 실행합니다.

```sh
curl -fsSL https://aven.example.com/install-agent.sh | sudo sh -s -- --server https://aven.example.com --token <TOKEN>
```

토큰은 한 번만 쓸 수 있고 만료됩니다. agent는 자기 키를 만들고 Aven 내부 CA에서 인증서를 받습니다. 나중에 같은 명령을 다시 실행하면 저장된 자격 증명을 유지한 채 agent를 업그레이드합니다.

서버들이 사설망(WireGuard, Tailscale)으로 연결되어 있다면 각 서버에 `AVEN_PRIVATE_ADDRESS`를 설정해 서로 다른 노드의 서비스끼리 그 망으로 통신하게 하세요.

## 문제 해결

- **`AVEN_ADMIN_PASSWORD(8자 이상)가 필요합니다`**: `/etc/aven/aven.env`에 8자 이상의 비밀번호를 넣으세요.
- **시작할 때 Docker 오류**: `systemctl status docker`를 확인하고 `aven-server`가 root로 실행되는지 확인하세요(unit 파일은 root로 실행합니다).
- **인증서가 발급되지 않음**: DNS가 이 서버를 가리키고 인터넷에서 80/443에 접속할 수 있어야 합니다. ACME 오류는 `docker logs keel-proxy`에서 볼 수 있습니다.
- **빌드가 `railpack을 찾을 수 없습니다`로 실패**: Railpack을 `/usr/local/bin/railpack`에 설치하거나 `AVEN_RAILPACK_BIN`을 설정하세요.
- **LAN에서 일반 HTTP로 로그인**(평가용만): 세션 쿠키는 기본적으로 `Secure`이므로 `AVEN_INSECURE_COOKIE=1`을 설정하세요.
