// Aven on Aven (self service): canvas badge, service panel notice, first-run checklist.
const en: Record<string, string> = {
  "aven-server 컨테이너로 돕니다({image}). 배포하면 새 이미지로 바꿉니다.": "Runs as the aven-server container ({image}). Deploying replaces it with the new image.",
  "지금은 systemd 유닛 {unit}으로 돕니다. 처음 배포하면 aven-server 컨테이너로 옮기고, 유닛은 비활성화해 비상용으로 남깁니다.": "Currently runs as the systemd unit {unit}. The first deployment moves it into the aven-server container and disables the unit, which stays installed for emergencies.",
  "systemd나 컨테이너 밖에서 돌고 있어 배포로 바꿀 수 없습니다. 이미지는 빌드됩니다.": "Runs outside systemd and containers, so a deployment cannot replace it. The image is still built.",
  "이 대시보드를 돌리는 aven-server입니다": "The aven-server running this dashboard",
  "이 서비스는 지금 이 대시보드를 돌리는 aven-server입니다": "This service is the aven-server running this dashboard",
  "배포하면 저장소의 Dockerfile로 새 버전을 빌드해 서버를 바꿉니다. 새 서버가 4분 안에 정상이 되지 않으면 이전 버전으로 되돌립니다. 바꾸는 몇 초 동안 대시보드와 API가 끊기지만 다른 서비스는 계속 돕니다.": "Deploying builds a new version from the repository's Dockerfile and replaces this server. If the new server is not healthy within 4 minutes, the previous version comes back. The dashboard and API pause for a few seconds during the swap; other services keep running.",
  "지금 버전 {version}": "Current version {version}",
  "Aven on Aven 안내": "Aven on Aven guide",
  "Aven 프로젝트를 만들었습니다. 캔버스에서 aven-server를 배포하면 이 서버가 바뀝니다.": "Created the aven project. Deploying aven-server from the canvas replaces this server.",
  "캔버스에 Aven 추가": "Add Aven to the canvas",
  "캔버스에서 열기": "Open on the canvas",
  "Aven 관리 해제": "Stop managing Aven",
  "aven-server 서비스가 평범한 서비스로 남고, 배포해도 이 서버를 바꾸지 않습니다. 프로젝트와 서비스는 지우지 않습니다.": "The aven-server service stays as an ordinary service and deploying it no longer replaces this server. The project and service are not deleted.",
  "Aven을 캔버스에서 관리": "Manage Aven on the canvas",
  "aven-server 자신을 Aven 프로젝트의 서비스로 둡니다. 캔버스에서 배포하면 저장소의 새 버전으로 이 서버를 바꾸고, 정상이 되지 않으면 이전 버전으로 되돌립니다. 이 프로젝트는 서버 owner만 볼 수 있습니다.": "Keeps aven-server itself as a service in the aven project. Deploying it from the canvas replaces this server with the repository's new version and rolls back if it does not become healthy. Only server owners can see this project.",
  "이 서버 · {version}": "This server · {version}",
};
export default en;
