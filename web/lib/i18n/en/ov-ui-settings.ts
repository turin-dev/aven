// English translations added by the ui-settings overhaul.
const en: Record<string, string> = {
  // settings nav, search, unsaved-changes guard
  "검색 결과": "Search results",
  "검색어 지우기": "Clear search",
  "설정 검색": "Search settings",
  "일치하는 설정이 없습니다": "No matching settings",
  "알림·사용량": "Alerts & usage",
  "저장하지 않은 변경 사항": "Unsaved changes",
  "이 화면에 저장하지 않은 변경 사항이 있습니다. 나가면 변경 사항이 사라집니다.": "This page has unsaved changes. They will be lost if you leave.",
  "저장하지 않고 나가기": "Leave without saving",

  // project settings page
  "옮기기": "Move",
  "프로젝트 옮기기": "Move project",
  "{project} 프로젝트를 {workspace} 작업 공간으로 옮깁니다. 멤버와 접근 권한은 대상 작업 공간을 따르게 되어, 지금 이 프로젝트를 보는 사람 중 일부는 접근을 잃을 수 있습니다.":
    "Moves the {project} project to the {workspace} workspace. Members and access then follow the target workspace, so some people who see this project now may lose access.",
  "이름이 올바르지 않은 변수 {count}개는 건너뛰었습니다: {keys}": "Skipped {count} variables with invalid names: {keys}",

  // members
  "역할별 권한": "What each role can do",
  "멤버·설정·위험 구역까지 모두 관리하고 제한된 환경에 접근합니다.": "Manages everything including members, settings and the danger zone, and can access restricted environments.",
  "서비스, 변수, 배포, 도메인을 바꿀 수 있습니다. 멤버와 프로젝트 설정은 바꿀 수 없습니다.": "Can change services, variables, deployments and domains. Cannot change members or project settings.",
  "모든 것을 읽기만 합니다. 변수 값은 가려집니다.": "Read-only access to everything. Variable values are hidden.",
  "작업 공간 삭제와 Owner 지정을 포함해 모든 것을 관리합니다.": "Manages everything, including deleting the workspace and assigning owners.",
  "모든 프로젝트와 멤버, 접근 그룹을 관리합니다.": "Manages all projects, members and access groups.",
  "프로젝트를 만들 수 있고, 만들었거나 추가된 프로젝트만 봅니다.": "Can create projects and sees only projects they created or were added to.",
  "추가된 프로젝트만 읽기 전용으로 봅니다.": "Sees only projects they were added to, read-only.",
  "올바른 이메일 주소를 입력하세요.": "Enter a valid email address.",
  "내 역할 낮추기": "Lower my role",
  "역할 낮추기": "Lower role",
  "이 프로젝트의 Admin 권한을 잃습니다. 다른 Admin이 다시 올려 주기 전까지 멤버와 설정을 바꿀 수 없습니다.":
    "You will lose Admin rights on this project. You cannot change members or settings until another Admin restores them.",
  "이 작업 공간에서 지금 권한을 잃습니다. 다른 Owner나 Admin이 다시 올려 주기 전까지 되돌릴 수 없습니다.":
    "You will lose your current rights in this workspace. Only another Owner or Admin can restore them.",
  "Owner 지정": "Make owner",
  "{email}에게 작업 공간 삭제를 포함한 모든 권한을 줍니다.": "Gives {email} full rights, including deleting the workspace.",
  "초대 취소": "Revoke invite",
  "{email}에게 보낸 초대 링크를 더 이상 쓸 수 없게 됩니다.": "The invite link sent to {email} will stop working.",
  "만료됨 · {date}": "Expired · {date}",
  "사용자 비활성화": "Disable user",
  "{email}의 세션이 끝나고 다시 활성화할 때까지 로그인할 수 없습니다. API 토큰도 쓸 수 없습니다.":
    "{email} is signed out and cannot sign in until re-enabled. Their API tokens stop working too.",
  "{email}의 인증 앱과 패스키를 모두 지웁니다. 2FA가 필수인 서버라면 다음 로그인 때 다시 설정해야 합니다.":
    "Removes all authenticator apps and passkeys of {email}. If 2FA is required, they must set it up again at next sign-in.",
  // notification channels
  "0보다 큰 값을 입력하거나 비워 두세요.": "Enter a value above 0 or leave it empty.",
  "노드 연결 끊김": "Node unavailable",
  "모든 이벤트": "All events",
  "받는 사람은 최대 20명입니다.": "Up to 20 recipients.",
  "받는 사람을 한 명 이상 입력하세요.": "Enter at least one recipient.",
  "사용량 기준 도달": "Usage threshold reached",
  "사용량 한도 도달": "Usage limit reached",
  "올바르지 않은 이메일: {list}": "Invalid email: {list}",
  "올바르지 않은 이벤트 패턴: {list}": "Invalid event pattern: {list}",
  "이벤트를 하나 이상 고르세요.": "Pick at least one event.",
  "이벤트 패턴": "Event patterns",
  "지표 알림": "Metric alerts",
  "크론 실패": "Cron failed",
  "http:// 또는 https://로 시작하는 URL을 입력하세요.": "Enter a URL starting with http:// or https://.",
  "{name} 사용": "Enable {name}",
  "{name} 채널을 삭제했습니다.": "Deleted the {name} channel.",
  "{name} 채널을 저장했습니다.": "Saved the {name} channel.",
  "{name} 채널을 추가했습니다. 테스트 전송으로 확인해 보세요.": "Added the {name} channel. Send a test to check it.",
  "URL을 입력하세요.": "Enter a URL.",
  // OAuth apps
  "같은 redirect URI가 두 번 있습니다.": "The same redirect URI is listed twice.",
  "공백, #, *는 쓸 수 없습니다: {uri}": "Spaces, # and * are not allowed: {uri}",
  "사용자 정보가 든 URI는 쓸 수 없습니다: {uri}": "URIs with user info are not allowed: {uri}",
  "사용자 지정 scheme은 네이티브 앱에서 역도메인 형식(com.example.app:/callback)만 쓸 수 있습니다: {uri}":
    "Custom schemes are only allowed for native apps, in reverse-domain form (com.example.app:/callback): {uri}",
  "절대 URI가 아닙니다: {uri}": "Not an absolute URI: {uri}",
  "허용하지 않는 scheme입니다: {uri}": "Scheme not allowed: {uri}",
  "HTTP는 localhost 또는 loopback 주소만 쓸 수 있습니다: {uri}": "HTTP is only allowed for localhost or loopback addresses: {uri}",
  "HTTPS redirect URI에 호스트가 필요합니다: {uri}": "HTTPS redirect URIs need a host: {uri}",
  "redirect URI는 최대 {n}개입니다.": "Up to {n} redirect URIs.",
  "redirect URI를 하나 이상 입력하세요.": "Enter at least one redirect URI.",

  // SAML
  "검증된 도메인의 사용자는 이제 SAML로만 로그인할 수 있고, SAML로 로그인하지 않은 기존 세션은 바로 다시 인증해야 합니다. IdP 설정이 잘못되어 있으면 admin@local 비상 계정으로만 들어올 수 있습니다.":
    "Users of verified domains can then sign in only with SAML, and existing sessions not signed in with SAML must re-authenticate right away. If the IdP is misconfigured, only the admin@local emergency account can get in.",
  "메타데이터 파일이 너무 큽니다 (최대 1 MiB).": "The metadata file is too large (max 1 MiB).",
  "메타데이터 URL은 http:// 또는 https://로 시작해야 합니다.": "The metadata URL must start with http:// or https://.",
  "올바른 도메인을 입력하세요 (예: example.com).": "Enter a valid domain (for example example.com).",
  "이미 추가한 도메인입니다.": "This domain is already added.",
  "{domain} 도메인을 검증했습니다.": "Verified {domain}.",
  "IdP 연결 해제": "Disconnect IdP",
  "SAML 메타데이터 XML(EntityDescriptor)이 아닙니다.": "This is not SAML metadata XML (EntityDescriptor).",
  "TXT 레코드를 아직 찾지 못했습니다. DNS 전파에는 몇 분 걸릴 수 있습니다.": "The TXT record was not found yet. DNS changes can take a few minutes to propagate.",
  // feature flags, alert rules, usage
  "100%를 넘는 기준은 발생하지 않습니다.": "A threshold above 100% never fires.",
  "꺼 둔 규칙은 평가하지 않습니다.": "Disabled rules are not evaluated.",
  "일치하는 플래그가 없습니다": "No matching flags",
  "즉시": "Immediately",
  "플래그 이름으로 찾기": "Find by flag name",
  "플래그 찾기": "Find flags",
  "{label} 할당률": "{label} allocation",
  "{target}에서 {metric}이(가) {cmp} {value} 상태로 {dur} 지속되면 {firing}을 보내고, 돌아오면 {resolved}를 보냅니다.":
    "When {metric} stays {cmp} {value} for {dur} on {target}, sends {firing}, and {resolved} once it recovers.",
  "{target}에서 {metric}이(가) {cmp} {value}이(가) 되는 즉시 {firing}을 보내고, 돌아오면 {resolved}를 보냅니다.":
    "As soon as {metric} is {cmp} {value} on {target}, sends {firing}, and {resolved} once it recovers.",
  // IaC, agent, 2FA
  "2단계 인증을 설정하지 않은 멤버는 다음 접속 때 설정을 마쳐야 대시보드와 CLI를 쓸 수 있습니다. API 토큰을 쓰는 자동화는 영향을 받지 않습니다.":
    "Members without two-factor authentication must finish setting it up at their next visit before using the dashboard or CLI. Automation with API tokens is not affected.",
  "먼저 모델 제공자를 연결하세요": "Connect a model provider first",
  "먼저 변경 사항을 저장하세요": "Save your changes first",
  "설정을 불러오지 못했습니다.": "Could not load the settings.",
  "클립보드에 복사하지 못했습니다. 직접 선택해 복사하세요.": "Could not copy to the clipboard. Select and copy it manually.",
  "편집 버리기": "Discard edits",
  "편집한 정의를 버리고 서버의 현재 설정으로 바꿉니다.": "Discards your edited definition and replaces it with the server's current configuration.",
  // permission notes
  "알림 채널은 프로젝트 admin만 바꿀 수 있습니다.": "Only project admins can change notification channels.",
  "사용량 알림 기준과 한도는 프로젝트 admin만 바꿀 수 있습니다.": "Only project admins can change usage alerts and limits.",
};

export default en;
