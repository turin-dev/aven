# Windows Server에서 Hyper-V 리전 노드 설치

Windows Server는 Hyper-V 호스트로 사용하고, Aven 리전 에이전트와 앱 컨테이너는 Ubuntu VM 안에서 실행합니다. 메인 Aven 서버는 기존 서버에 둡니다.

## 1. Hyper-V 준비

관리자 PowerShell에서 Hyper-V 역할을 설치합니다. 서버가 재부팅됩니다.

```powershell
Install-WindowsFeature -Name Hyper-V -IncludeManagementTools -Restart
```

Hyper-V 관리자에서 **외부 가상 스위치**를 하나 만드세요. VM이 메인 Aven 서버에 연결되고 리전의 앱 트래픽을 받을 수 있도록 실제 네트워크 어댑터에 연결해야 합니다. 클라우드 VPS 안의 Windows Server라면 중첩 가상화와 외부 네트워크 연결을 제공업체가 지원해야 합니다.

## 2. Ubuntu VM 만들기

관리자 PowerShell에서 설치기를 내려받아 실행합니다.

```powershell
Invoke-WebRequest https://github.com/turin-dev/aven/releases/latest/download/install-region-vm.ps1 -OutFile .\install-region-vm.ps1
powershell -ExecutionPolicy Bypass -File .\install-region-vm.ps1
```

설치기는 VM 이름, vCPU, 메모리, 디스크를 대화형으로 고릅니다. 기존 외부 스위치가 하나면 자동으로 쓰고, 여러 개면 선택하게 합니다. Ubuntu Server 24.04 LTS ISO를 공식 사이트에서 내려받아 SHA-256을 확인한 뒤 Hyper-V VM을 생성하고 자동 시작을 설정합니다. VMConnect로 Ubuntu 설치를 마무리하세요.

```powershell
vmconnect.exe localhost "AvenRegion"
```

## 3. Aven 리전 에이전트 설치

Ubuntu VM에서 터미널을 열고 아래 명령을 실행합니다.

```sh
curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh | sh
```

설치기에서 **Region server**를 고른 뒤 메인 서버 HTTPS URL, *Admin → Server nodes → Add node*에서 만든 1회용 참여 토큰, Docker 설치 여부를 입력합니다. `sudo sh`로 파이프하지 마세요. 설치기가 대화형 권한 상승을 처리합니다.

노드가 연결되면 Aven은 Ubuntu VM 안의 Docker 컨테이너를 배포하고 관리합니다. Aven 대시보드의 **어드민 → 서버 노드**에서 해당 노드의 작업 메뉴를 열고 **Hyper-V 호스트 연결**을 선택하세요. 화면에 나온 명령을 Windows Server의 관리자 PowerShell에서 실행하면 호스트 제어기가 설치됩니다. 그 뒤 같은 메뉴에서 VM을 시작하거나 정상 종료할 수 있습니다. Windows Server가 재부팅되면 Hyper-V가 VM을 자동 시작합니다.

VM 종료는 Ubuntu에 정상 종료를 요청합니다. VM이 꺼져 있는 동안 해당 리전에 배포된 앱은 응답하지 않습니다.

## 네트워크

- Ubuntu VM에서 메인 Aven 서버로 아웃바운드 HTTPS가 가능해야 합니다.
- 서비스 도메인을 이 리전에서 제공하려면 DNS가 VM의 공개 IP 또는 그 VM으로 전달되는 주소를 가리켜야 합니다. 외부 방화벽과 게스트 방화벽에서 TCP 80·443을 허용하세요. QUIC을 쓰면 UDP 443도 허용합니다.
- TCP proxy를 사용하면 설정된 공개 포트 범위도 VM까지 전달해야 합니다(기본값 20000–29999).
