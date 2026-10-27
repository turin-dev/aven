# Aven on Aven (self 서비스)

Aven은 `aven-server` 자신을 빌드하고 배포할 수 있습니다. Aven 저장소(또는 포크)를 평범한 repo 서비스로 배포하고 그 서비스를 **self 서비스**로 지정하면, 배포할 때마다 새 `aven-server` 이미지를 빌드해 돌고 있는 서버와 바꿉니다. 새 서버가 healthy가 되지 않으면 자동으로 되돌립니다.

새로 설치하면 서버 owner의 캔버스에 이 서비스가 생기고(`aven` 프로젝트), 기존 설치는 *어드민 → Owner · 초기 설정*에서 추가합니다. 배포하기 전까지는 아무것도 바뀌지 않습니다. 기본 설치(`install-server.sh`, systemd 유닛 `aven-server`)는 그대로 쓸 수 있고 언제든 돌아갈 수 있습니다.

## 동작 방식

- `aven-server`가 systemd 서비스 대신 `aven-server`라는 Docker 컨테이너로 돕니다. 서버는 호스트 자체를 다루므로(호스트 주소에서 listen, 도메인별 IPv6 주소 추가, egress 규칙 작성, `/proc`에서 컨테이너 포트 확인) 컨테이너는 호스트 네트워크와 호스트 PID 네임스페이스, `/var/run/docker.sock`을 쓰는 특권 컨테이너입니다. 컨테이너 스펙은 코드(`internal/selfhost`)에 고정되어 있고 서비스 설정으로 바꿀 수 없습니다.
- self 서비스는 Dockerfile로 빌드하는 평범한 repo 서비스입니다(저장소의 `Dockerfile`). 빌드, 로그, GitHub 웹훅, watch paths가 평소처럼 동작합니다.
- self 서비스 배포는 이미지가 준비되면 컨테이너를 띄우지 않습니다. 대신 **새** 이미지로 일회성 `aven-selfupgrade` 컨테이너를 띄우고, 이 컨테이너가 다음을 합니다.
  1. 돌고 있는 `aven-server`의 이름을 `aven-server-prev`로 바꾸고 멈춥니다(두 서버가 DB를 동시에 열지 않습니다).
  2. 새 이미지로 `aven-server`를 띄우고 헬스체크(`aven-server selfhost health`, `/healthz` 호출)가 통과할 때까지 기다립니다.
  3. 성공하면 `aven-server-prev`를 지웁니다. 새 서버가 시작하면서 배포를 live로 끝냅니다.
  4. 실패하면 새 컨테이너를 지우고 데이터 디렉터리에 `selfhost-result.json`을 남긴 뒤 이전 서버를 다시 띄웁니다. 이전 서버는 그 배포를 실패(`rolled back: …`)로 끝내고 이전 배포를 계속 현재 배포로 둡니다.
- 교체하기 전에 진행 중인 다른 배포가 끝나기를 최대 15분 기다립니다. 그때까지 끝나지 않은 배포는 다른 재시작 때와 마찬가지로 실패 처리됩니다.
- 교체하는 몇 초 동안 대시보드와 API가 끊깁니다. `keel-proxy`와 앱 컨테이너는 건드리지 않으므로 서비스는 계속 돕니다.
- 대시보드는 특별 서비스로 다룹니다. 패널에는 배포와 설정(소스·빌드)만 있고, 배포 상세에는 상세와 빌드 로그만 있으며, 캔버스 메뉴에는 열기·설정·배포·롤백·복사만 있습니다. 카드에는 지금 도는 버전이 나옵니다.
- **업데이트 확인.** 30분마다(그리고 시작 1분 뒤) 서비스 브랜치의 최신 커밋을 지금 배포와 GitHub API로 비교합니다(프로젝트의 GitHub 연결을 씁니다). 뒤처져 있으면 캔버스 카드에 분홍 점이 붙고, 서비스 패널에 새 커밋 목록과 **업데이트**(브랜치 최신 커밋 배포), **지금 확인**(`POST /api/owner/self-service/check`)이 나오며, 활동 기록에 새 버전마다 한 번 `aven.update_available`이 남습니다. 업데이트를 자동으로 적용하지는 않습니다.
- **서버 설정.** 서비스의 설정 탭 맨 위 *서버 설정*에서 env 파일의 `AVEN_*` 값을 그룹(일반, 네트워크, GitHub, 메일, 고급)별로 보고 고칩니다. 비밀값은 설정 여부만 보입니다(`GET/PUT /api/owner/server-config`, 서버 owner 전용). 저장하면 파일만 바뀌고, 지금 서버와 다른 값은 *적용 대기*로 표시되며 aven-server를 다시 배포할 때 적용됩니다(**저장하고 배포**는 지금 이미지로 다시 배포하므로 빌드하지 않습니다). 그 배포가 되돌려지면 env 파일도 마지막으로 정상 동작한 서버의 사본(`AVEN_DATA/selfhost-env.good`)으로 되돌리므로 잘못된 값이 남지 않습니다. 데이터 경로와 컨테이너가 덮어쓰는 값(`AVEN_DATA`, `AVEN_GATEWAY_SECRET_FILE`, `AVEN_AGENT_BIN_DIR`, `AVEN_RAILPACK_BIN`, `AVEN_SELF_*` 파일·서비스)은 읽기 전용입니다. 이를 위해 컨테이너는 env 파일 디렉터리를 읽기·쓰기로 마운트합니다.
- self 서비스는 Aven에서 중지, 재시작, scale, sleep할 수 없습니다(`409`). 항상 서버 자신의 노드에 replica 1개로 돕니다. 수동 작업은 호스트에서 `docker`로 합니다.

## 준비물

- [셀프 호스팅 가이드](self-hosting.ko.md)대로 설치해 systemd 유닛 `aven-server`로 도는 서버.
- `/etc/aven/aven.env` 환경 파일(다른 경로면 `AVEN_SELF_ENV_FILE`). 컨테이너는 이 파일을 `docker --env-file`로 읽는데, 이 방식은 따옴표를 벗기지 **않습니다**. `KEY="value"`가 아니라 `KEY=value`로 쓰세요. 배포할 때 이를 확인해, 따옴표로 감싼 값이 있으면 서버를 건드리기 전에 실패합니다.
- 환경 파일에 적힌 경로는 컨테이너 안에도 있어야 합니다. 데이터 디렉터리(`AVEN_DATA`), 환경 파일 디렉터리(서버 설정 화면이 고치므로 읽기·쓰기), Docker 볼륨, `/root/.docker`는 같은 경로로 마운트됩니다. `AVEN_RAILPACK_BIN`과 `AVEN_AGENT_BIN_DIR`은 이미지 안의 파일로 덮어씁니다. 그 밖의 경로(예: `AVEN_TLS_CERT`)는 마운트되는 경로 아래에 두어야 합니다.
- `AVEN_LISTEN`이 사설 주소(Tailscale/WireGuard)면 Docker가 컨테이너를 띄우기 전에 그 인터페이스가 올라와 있어야 합니다. Docker가 컨테이너를 다시 시도하므로(`--restart unless-stopped`) 인터페이스가 늦으면 시작이 늦어질 뿐입니다.

## 대시보드에서 설정

1. **aven 프로젝트.** 새로 설치한 서버(서비스가 하나도 없음)는 처음 시작할 때 `Aven` 작업 공간에 `aven` 프로젝트와 `aven-server` 서비스를 만듭니다. 저장소는 `https://github.com/turin-dev/aven`, 브랜치는 `main`, Dockerfile로 빌드하며, 서버 자신의 노드에 replica 1개로 두고 도메인은 붙이지 않으며 자동 배포는 끕니다(업그레이드는 배포를 눌러서 합니다. GitHub 웹훅과 함께 켜면 push할 때마다 서버가 바뀝니다). 기존 설치에서는 *어드민 → Owner · 초기 설정*의 *Aven을 캔버스에서 관리*에서 **캔버스에 Aven 추가**를 누릅니다(`POST /api/owner/self-service`, 본문 `{"repo_url", "branch"}`는 선택). 포크를 쓰려면 서비스 설정에서 저장소를 바꾸세요.

   전용 작업 공간이라 기본 작업 공간의 멤버나 자원 한도와 섞이지 않습니다(self 서비스는 컨테이너를 띄우지 않으므로 한도를 두지 않습니다). 이 프로젝트는 작업 공간의 admin이라도 서버 owner가 아니면 보이지 않습니다. 배포하면 호스트에서 root로 코드가 돌기 때문입니다. self 서비스로 지정된 동안에는 서비스와 그 환경, 프로젝트를 지울 수 없습니다(`409`).

2. 캔버스에서 `aven-server`를 **배포**합니다. 캔버스 카드에는 Aven 표시가 붙고, 서비스 패널에서 배포하면 무엇이 일어나는지 안내합니다.
   - 서버가 아직 systemd 유닛으로 돌면, 첫 배포는 이미지를 빌드한 뒤 서버를 `aven-server` 컨테이너로 옮깁니다. transient 유닛(`systemd-run`, `aven-selfbootstrap-<배포>`)에서 `aven-server selfhost bootstrap`을 돌려 systemd 유닛을 멈추고 비활성화한 뒤 컨테이너를 띄웁니다. 컨테이너가 healthy가 되지 않으면 bootstrap이 컨테이너를 지우고 systemd 유닛을 다시 시작하며, 다시 시작한 서버가 배포를 실패로 끝냅니다.
   - 서버가 컨테이너로 돌면 배포할 때마다(push, 재배포, 이전 배포로 롤백) 위에서 설명한 대로 서버를 바꿉니다.

3. 같은 체크리스트 항목의 **해제**로 **Aven 관리를 그만둘** 수 있습니다(`DELETE /api/owner/self-service`). 프로젝트와 서비스는 평범한 것으로 남고, 배포해도 서버를 바꾸지 않습니다. 서버는 지금 도는 방식 그대로 계속 돕니다.

설정:

| 변수 | 기본값 | 뜻 |
| --- | --- | --- |
| `AVEN_SELF_SERVICE` | | self 서비스를 ID로 고정합니다. 대시보드보다 우선하며, 대시보드에서는 바꿀 수 없게 됩니다. |
| `AVEN_SELF_PROJECT` | | `off`면 새로 설치할 때 aven 프로젝트를 만들지 않습니다. |
| `AVEN_SELF_REPO`, `AVEN_SELF_BRANCH` | `https://github.com/turin-dev/aven`, `main` | 처음 시작할 때나 체크리스트에서 만드는 프로젝트의 소스. |
| `AVEN_SELF_UPDATE_INTERVAL` | `30m` | 저장소 브랜치의 새 커밋을 확인하는 주기(`off`면 끕니다). |
| `AVEN_SELF_ENV_FILE` | `/etc/aven/aven.env` | 컨테이너에 넘기는 환경 파일. |

## 직접 bootstrap

대시보드 없이 하려면 이미지를 만든 뒤(저장소에서 `docker build -t aven-server .`, 또는 서버가 스스로 바꿀 수 없을 때 self 서비스를 한 번 배포) root로 실행합니다.

```sh
sudo aven-server selfhost bootstrap --image aven-server
```

bootstrap은 systemd 유닛 `aven-server`를 멈추고 `aven-server` 컨테이너를 띄운 뒤 healthy가 되기를 기다립니다. 실패하면 컨테이너를 지우고 systemd 유닛을 다시 시작합니다. 성공하면 systemd 유닛을 비활성화합니다(비상용으로 설치는 남겨 둡니다). 옵션: `--systemd-unit`(기본 `aven-server`, 비우면 없음), `--env-file`, `--data`, `--timeout`(기본 4m).

아무것도 띄우지 않고 실제 `docker run` 명령만 보려면 다음을 실행합니다.

```sh
aven-server selfhost run-args --image aven-server
```

## 운영

```sh
docker ps --filter label=keel.self           # aven-server, 교체 중이면 aven-selfupgrade / aven-server-prev
docker logs -f aven-server                   # 서버 로그
docker logs aven-selfupgrade                 # 마지막 교체(새 서버가 로그를 거둬 가면 지워집니다)
```

**수동 롤백.** 대시보드에서 self 서비스의 이전 배포를 다시 배포합니다. 대시보드에 접속할 수 없으면 이전 이미지의 `docker run` 명령을 출력한 뒤, 지금 컨테이너를 지우고 출력된 명령을 실행합니다.

```sh
aven-server selfhost run-args --image <이전 이미지>
docker rm -f aven-server
# 출력된 docker run 명령을 붙여 넣습니다
```

**systemd로 돌아가기.**

```sh
docker rm -f aven-server
sudo systemctl enable --now aven-server
```

그다음 체크리스트에서 Aven 관리를 해제하세요(또는 `AVEN_SELF_SERVICE`를 지웁니다). 그대로 두면 다음 배포가 서버를 다시 컨테이너로 옮깁니다.

**Docker를 업그레이드**하면 재시작 정책이 있는 다른 컨테이너처럼 `aven-server` 컨테이너도 다시 시작됩니다.

## 문제 해결

- **배포가 `rolled back: …`로 실패함**: 새 서버가 4분 안에 healthy가 되지 않았습니다. 배포 로그에 새 서버 출력의 마지막 40줄이 있습니다.
- **`… 스스로 교체할 수 없습니다`**: 서버가 컨테이너로도 systemd 유닛으로도 돌지 않습니다(예: 터미널에서 실행). systemd로 돌리거나 직접 bootstrap하세요.
- **첫 배포가 `bootstrap이 … 옮기지 못하고 끝났습니다`로 실패함**: 배포 로그에 bootstrap 유닛의 journal이 있습니다. `journalctl -u 'aven-selfbootstrap-*'`로도 볼 수 있습니다.
- **`… 로컬 노드에 replica 1개로만 배포할 수 있습니다`**: self 서비스의 placement와 추가 replica를 없애세요.
- **되돌리기에 실패해 서버가 하나도 돌지 않음**: `docker rename aven-server-prev aven-server && docker start aven-server`를 실행하거나, 위처럼 systemd로 돌아갑니다.
