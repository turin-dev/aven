// English translations added by the ui-deployments overhaul.
const en: Record<string, string> = {
  // LogTable
  "경고 이상": "Warnings+",
  "로그 레벨": "Log level",
  "긴 줄 바꿈": "Wrap long lines",
  "줄 바꿈": "Wrap",
  "시간 표시": "Show timestamps",
  "필터된 로그 복사": "Copy filtered logs",
  "로그 복사": "Copy logs",
  "로그 내려받기": "Download logs",
  "맨 아래로 (새 로그 {count}줄)": "Scroll to bottom ({count} new lines)",
  "최신 로그를 따라가는 중": "Following the latest logs",
  "맨 아래로 이동해 최신 로그 따라가기": "Jump to the bottom and follow new logs",
  // DeploymentPanel / DeploymentsTab
  "빌드 단계 {n}/{of}": "Build step {n}/{of}",
  "빌드 진행률": "Build progress",
  "중단됨": "Cancelled",
  "이미지를 레지스트리에서 다시 받아 새 배포를 만듭니다. 새 배포가 정상이면 현재 배포를 대체합니다.":
    "Pulls the image from the registry again and creates a new deployment. It replaces the current deployment once it is healthy.",
  "연결된 브랜치의 최신 커밋을 빌드해 새 배포를 만듭니다. 새 배포가 정상이면 현재 배포를 대체합니다.":
    "Builds the latest commit of the connected branch as a new deployment. It replaces the current deployment once it is healthy.",
  "빌드부터 활성화까지 걸린 시간": "Time from build start to active",
  // MetricsTab
  "현재 {now} · 최대 {peak}": "Now {now} · peak {peak}",
  "일시정지됨 · 화면의 값이 갱신되지 않습니다": "Paused · values on screen are not updating",
  // ShellTab
  "다시 연결됨 · 새 셸 세션": "Reconnected · new shell session",
  "재연결 취소": "Cancel reconnect",
  "다시 연결하는 중… ({n}/{max})": "Reconnecting… ({n}/{max})",
};

export default en;
