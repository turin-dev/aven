# Windows 네이티브 서버

[English](windows-native.md) · [Linux 셀프 호스팅](self-hosting.ko.md)

Aven 서버를 Windows 실행 파일과 Windows 서비스로 실행할 수 있습니다. 지원 구성은 x64 Windows 10 22H2(build 19045) 또는 Windows 11 23H2(build 22631) 이상과 **Linux 컨테이너 모드**의 Docker Desktop입니다. Aven 프로세스는 Windows에서 실행되고, Docker Desktop은 설정된 VM 백엔드에서 Linux 컨테이너 엔진을 실행합니다.

> Windows Server 에디션은 이 설치 프로그램에서 지원하지 않습니다. Docker Desktop은 Windows Server를 지원하지 않고, Aven의 proxy와 앱 컨테이너에는 Linux Docker 엔진이 필요합니다.

## 설치

Docker Desktop이 없으면 설치 프로그램이 공식 x64 설치 파일을 내려받아 서명을 확인한 뒤 모든 사용자용으로 설치합니다. 설치 후 Docker Desktop을 실행하고 Linux 엔진이 준비될 때까지 기다립니다. Docker의 라이선스 동의 화면이 표시되면 직접 동의하세요. Docker Desktop이 Windows 재시작을 요청하면 재시작한 뒤 Aven 설치 명령을 다시 실행합니다. 설치 프로그램은 관리자 권한 PowerShell에서 실행해야 합니다.

```powershell
Invoke-WebRequest https://github.com/turin-dev/aven/releases/latest/download/install-server.ps1 -OutFile .\install-server.ps1
powershell -ExecutionPolicy Bypass -File .\install-server.ps1 -PublicUrl https://aven.example.com -BaseDomain example.com
```

로컬에서 먼저 사용하려면 `-PublicUrl`과 `-BaseDomain`을 생략할 수 있습니다. 설치 프로그램은 릴리스 파일의 SHA-256을 확인하고 Windows용 Railpack을 내려받은 뒤 `AvenServer` 서비스를 만들고, 실패 시 자동 재시작을 설정합니다. 설정은 `%ProgramData%\Aven\aven.env`, 데이터는 `%ProgramData%\Aven\data`, 로그는 `%ProgramData%\Aven\logs\aven-server.log`에 저장합니다.

처음 설치하면 `-AdminPassword`를 지정하지 않은 경우 관리자 비밀번호를 생성해 마지막에 한 번 보여 줍니다. 이를 저장하세요. 공개 URL을 지정했다면 그 주소로 접속하고, 생략했다면 호스트에서 `http://localhost:7070`을 엽니다.

특정 릴리스 설치와 업그레이드에도 `-Version vX.Y.Z`를 사용합니다. 기존 설정과 데이터는 유지됩니다. `aven.env`를 수정한 뒤 다음처럼 반영하고 로그를 확인할 수 있습니다.

```powershell
Restart-Service AvenServer
Get-Content "$env:ProgramData\Aven\logs\aven-server.log" -Wait
```

방화벽은 TCP 80·443과 QUIC용 UDP 443을 허용하고 API, proxy 전용 서버 경로, trace 수신기, 앱 깨우기 포트(7070, 7071, 4318, 5381)는 로컬 서브넷으로 제한합니다. TCP proxy를 켜면 설정한 공개 포트 범위(기본 20000–29999)를 Windows와 네트워크 방화벽에서도 허용해야 합니다. 대시보드와 와일드카드 서비스 도메인의 DNS는 [셀프 호스팅 가이드](self-hosting.ko.md)를 참고하세요.

## Windows 호스트 제한

- 워크로드는 Linux 컨테이너 이미지여야 합니다. Windows 서비스는 Docker Desktop named pipe로 연결합니다.
- Windows 호스트에서 Linux 컨테이너의 `/proc`를 읽을 수 없어 앱 포트를 자동 탐지하지 못합니다. Aven에서 포트를 직접 지정하거나 앱의 `PORT` 변수를 설정하세요.
- 호스트 IPv6 주소 할당, 아웃바운드 IPv6 차단, 사설 mesh, Unix socket 기반 edge 엔진은 이 호스트에서 사용할 수 없습니다.
- Aven을 컨테이너로 배포해 자체 업그레이드하는 흐름은 네이티브 Windows 서비스에 적용되지 않습니다. 이 설치 프로그램으로 실행 파일을 업그레이드하세요.

전체 Linux 네트워킹 기능이나 Windows Server 에디션이 필요하면 Linux Docker 호스트에서 Aven을 실행하세요.
