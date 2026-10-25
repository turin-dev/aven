# Aven

**내 서버에서, 호스팅 플랫폼처럼 앱을 배포하고 운영하세요.**

[![CI](https://github.com/turin-dev/aven/actions/workflows/ci.yml/badge.svg)](https://github.com/turin-dev/aven/actions/workflows/ci.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE)

[English](README.md)

Aven은 애플리케이션, 데이터베이스, 백그라운드 작업을 위한 셀프호스트 플랫폼입니다. GitHub 저장소를 연결하거나 Docker 이미지를 고르거나 데이터베이스를 띄우면, Aven이 빌드하고 TLS가 붙은 도메인을 주고 다른 서비스와 사설망으로 이어 준 뒤 계속 살아 있게 관리합니다. 환경마다 캔버스 하나에 모든 것이 보입니다. 호스팅 PaaS와 같은 경험이지만 서버는 내 것입니다.

첫 서버에서 `aven-server` 프로세스 하나가 Docker 옆에서 돕니다. 서버를 더 붙이려면 작은 agent를 설치합니다. 대시보드, HTTP·GraphQL API, CLI, MCP 서버는 모두 같은 권한 검사를 거칩니다. 대시보드는 한국어와 영어를 지원합니다.

> [!IMPORTANT]
> **Aven은 Railway가 아니며 Railway의 코드를 담고 있지 않습니다.** 처음부터 새로 작성한 독립 프로젝트이고 Railway Corporation과 아무 관계가 없습니다. 대시보드의 모양과 사용 흐름은 Railway를 본떴고, 쉽게 옮겨 올 수 있도록 `railway.json`을 읽고 Railway 형태의 GraphQL 질의에 답합니다. [Railway와의 관계](#railway와의-관계)를 보세요.

> [!NOTE]
> Aven은 아직 젊고 빠르게 바뀝니다. 릴리스 사이에 인터페이스와 설정이 바뀔 수 있으니, 업그레이드 전에 릴리스 노트를 읽고 데이터 디렉터리를 백업하세요.

## 주요 기능

**배포**
- GitHub 저장소(Dockerfile 또는 Dockerfile 없이 [Railpack](https://github.com/railwayapp/railpack)), Docker 이미지, Docker Compose 파일, 템플릿, 단일 파일 function, 로컬 디렉터리(`aven up`)로 서비스를 만듭니다.
- push 자동 배포, watch paths, CI 대기, 커밋 상태 표시, PR 미리보기 환경.
- 변수 참조(`${{ db.DATABASE_URL }}`), 공유·봉인 변수, raw 편집기, config as code(`railway.json`, `keel.json`)를 갖춘 환경.
- pre-deploy 명령, 헬스체크, 무중단 교체, 롤백, cron 작업, 앱 sleep, 이미지 자동 업데이트.

**데이터**
- 클릭 한 번으로 PostgreSQL, MySQL, Redis, MongoDB, S3 호환 버킷. 볼륨, 데이터 브라우저, S3로 보내는 논리 백업.
- PostgreSQL의 시점 복구(PITR), PgBouncer 연결 풀링, 메이저 버전 업그레이드, 고가용성 클러스터.

**네트워크**
- 관리형 Caddy 프록시로 생성 도메인과 커스텀 도메인에 TLS 자동 발급. 경로 규칙, 리디렉션, basic auth, 와일드카드 도메인.
- IPv6 우선: 도메인별 공개 IPv6 주소(선택), 아웃바운드 IPv6 제어, 데이터베이스 같은 TCP 서비스를 위한 TCP 프록시.
- 서비스 사이 사설 DNS 이름. 암호화된 mesh(Tailscale 또는 WireGuard 주소)로 서버를 넘어서도 이어집니다.
- 엣지 캐싱, WAF 규칙, Cloudflare Tunnel 진입.

**여러 서버**
- 한 줄 agent 설치로 서버를 추가합니다. 서비스를 노드에 배치하고, replica를 여러 노드에 나누고, CPU·메모리·초당 요청 수로 자동 확장하고, 볼륨과 함께 서비스를 옮깁니다.

**관측**
- 빌드·배포·런타임 로그, HTTP와 네트워크 흐름 로그, 지표, OpenTelemetry 트레이싱, 대시보드, 지표 알림과 알림 채널(webhook, Slack, Discord, 이메일).

**팀과 보안**
- 역할이 있는 작업 공간·프로젝트·환경, 접근 그룹, 제한된 환경, 허용 소스를 정하는 guardrail.
- 패스키, TOTP, 2단계 인증 강제, SAML SSO, OAuth 2.0 / OpenID Connect 제공자("Aven으로 로그인"), API 토큰, 감사 기록, 사용량·자원 한도.

**자동화**
- `aven` CLI, REST·GraphQL API, AI 도구를 위한 MCP 서버, 선택형 AI 어시스턴트(모델은 직접 연결), 프로젝트 전체를 다루는 IaC, 샌드박스, feature flags SDK.
- `aven import dokploy`로 기존 Dokploy 설치를 옮깁니다.

**Aven on Aven**
- 서버가 자기 자신을 관리합니다. `aven-server`가 서버 owner의 캔버스에 나타나 저장소의 업데이트를 확인하고, 배포 한 번으로 스스로 업그레이드하며, 새 버전이 정상이 아니면 자동으로 되돌립니다. 서버 설정도 같은 패널에서 고칩니다. [Aven on Aven](docs/selfhost.ko.md)을 보세요.

## 빠른 시작

systemd와 Docker가 있고 80·443 포트가 비어 있는 Linux 서버(amd64 또는 arm64)와 대시보드용 DNS 이름이 필요합니다.

```sh
curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh \
  | sudo sh -s -- --public-url https://aven.example.com
```

스크립트는 릴리스 체크섬을 확인하고 `aven-server`, CLI, Railpack을 설치합니다. 관리자 비밀번호를 만들어 `/etc/keel/aven.env`에 쓰고, 서비스를 시작한 뒤 로그인 정보와 만들어야 할 DNS 레코드를 알려 줍니다. `aven.example.com`(생성 도메인을 쓰려면 `KEEL_BASE_DOMAIN`과 함께 `*.apps.example.com`도)을 서버로 향하게 하고 로그인한 다음, *어드민 → Owner · 초기 설정*의 체크리스트를 따라가세요.

그다음 프로젝트를 열고 캔버스에서 **추가**를 눌러 첫 서비스를 배포합니다. DNS, 설정, 직접 설치, 백업, 업그레이드, 서버 추가는 [셀프 호스팅 가이드](docs/self-hosting.ko.md)에 있습니다.

## 구조

```
                 인터넷 (IPv6 / IPv4)
                          │
               ┌──────────▼──────────┐            ┌───────────────────────┐
               │ keel-proxy (Caddy)  │            │ 노드 2                │
               │ TLS, 라우팅, 엣지   │            │ aven-agent            │
               └──────────┬──────────┘            │ keel-proxy · 서비스   │
   대시보드 / API         │                       └───────────▲───────────┘
 ┌────────────────────────▼────────────┐   mesh (Tailscale/   │
 │ aven-server                         │   WireGuard 위 mTLS) │
 │ API · 스케줄러 · 빌드 · SQLite ◄────┼──────────────────────┘
 └────────────────────────┬────────────┘
                          │ Docker
               서비스, 데이터베이스, 볼륨
```

- `aven-server`는 대시보드가 들어 있는 Go 바이너리 하나입니다. 상태는 SQLite(`/var/lib/keel`)에 두고, Docker와 Railpack으로 이미지를 빌드하고, Docker API로 컨테이너를 돌립니다.
- `keel-proxy`는 Aven이 설정하는 Caddy 컨테이너입니다. TLS를 끝내고 도메인을 자기 노드의 replica로 보내며 엣지 기능을 처리합니다.
- `aven-agent`는 추가 서버에서 돌며 인증서로 서버에 연결해 그 서버의 Docker 작업을 맡습니다. 다른 노드의 서비스끼리는 지정한 사설 주소 위의 mTLS mesh로 통신합니다.

## 소스에서 빌드

Go 1.26 이상, Node.js 20.9 이상과 npm, 서버 실행용 Docker가 필요합니다.

```sh
make all        # ./bin에 웹 UI, aven-server, agent, CLI 바이너리
```

로컬에서 시험해 보려면 다음을 실행합니다.

```sh
mkdir -p ./data
KEEL_ADMIN_PASSWORD='replace-with-a-long-password' \
KEEL_HTTP_PORT=18080 KEEL_HTTPS_PORT=18443 KEEL_PROXY_BIND=127.0.0.1 \
./bin/aven-server -listen 127.0.0.1:7070 -data ./data
```

`http://127.0.0.1:7070`에서 `admin@local`로 로그인합니다. 서버는 Docker로 프록시 컨테이너를 띄우므로, 네트워크에서 돌리기 전에 포트와 bind 주소를 바꾸세요. 운영에는 [`deploy/aven.env.example`](deploy/aven.env.example)과 [`deploy/aven-server.service`](deploy/aven-server.service)를 쓰거나, 컨테이너 이미지는 `docker build -t aven-server .`로 만듭니다.

## Keel에서 업그레이드

Aven은 Keel을 잇는 프로젝트이고 런타임 이름을 그대로 쓰므로 기존 설치를 그 자리에서 올릴 수 있습니다. `KEEL_*` 설정, `/var/lib/keel`, `/var/lib/keel-agent`, `keel-agent` 유닛, `.keel` CLI 연결, `keel.*` 컨테이너 라벨이 모두 유지됩니다. 환경 파일을 `/etc/keel/aven.env`로 복사하고 `deploy/aven-server.service`와 `aven-server` 바이너리를 설치한 뒤 `keel-server`를 멈추고 `aven-server`를 켜세요. 두 서버를 같은 데이터 디렉터리로 동시에 돌리면 안 됩니다.

## Railway와의 관계

Aven은 Railway의 포크, 복제본, 재배포판이 **아닙니다**. Railway 플랫폼은 소스가 공개되지 않았고, 이 저장소에는 그 소스 코드가 전혀 들어 있지 않습니다. 서버, agent, CLI, 프록시 설정, 대시보드 모두 Aven을 위해 새로 작성했습니다.

Railway와 닮은 점은 겉모습뿐입니다.

- **모양과 사용 흐름.** 많은 사람이 이미 익숙한 Railway 대시보드를 본떠 캔버스, 서비스 패널, 설정을 설계했습니다.
- **호환성.** `railway.json` / `railway.toml` 설정 파일을 읽고, Railway 공개 API 형태의 GraphQL 엔드포인트를 제공합니다. 옮겨 와도 기존 설정과 스크립트가 그대로 동작합니다.
- **Railpack.** Dockerfile 없는 빌드에는 Railway가 MIT로 공개한 오픈소스 빌더 [Railpack](https://github.com/railwayapp/railpack)을 별도 프로그램으로 실행해 씁니다.

Railway는 Railway Corporation의 상표이며, 여기서는 호환성을 설명하는 데만 씁니다.

## 문서

- [셀프 호스팅 가이드](docs/self-hosting.ko.md): 설치, DNS, 설정, 백업, 업그레이드, 노드
- [Aven on Aven](docs/selfhost.ko.md): Aven이 자기 서버를 관리하고 업그레이드하기
- [CLI 안내](docs/cli.md)
- [HTTP API](docs/api-v1.md)와 [GraphQL API](docs/graphql.md)
- [권한과 작업 공간](docs/permissions.md)
- [Feature flags SDK](sdk/README.md)

## 기여

버그 제보, 문서 수정, PR 모두 환영합니다. 먼저 [CONTRIBUTING.md](CONTRIBUTING.md)와 [행동 강령](CODE_OF_CONDUCT.md)을 읽어 주세요. 보안 취약점은 [SECURITY.md](SECURITY.md)대로 비공개로 알려 주세요.

## 라이선스

Aven은 [GNU Affero General Public License v3.0 only](LICENSE)(AGPL-3.0-only)로 배포됩니다. 수정한 버전을 네트워크로 다른 사람에게 제공하면 그 소스도 제공해야 합니다. 함께 배포하는 제3자 소프트웨어와 글꼴은 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)에 있습니다.
