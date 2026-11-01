// English translations added by the ui-auth overhaul.
const en: Record<string, string> = {
  // login
  "서버에 연결할 수 없습니다. 네트워크 연결을 확인하세요.": "Can't reach the server. Check your network connection.",
  "로그인 시도가 너무 많습니다. {seconds}초 후 다시 시도하세요.": "Too many sign-in attempts. Try again in {seconds} seconds.",
  "로그인 시도가 너무 많습니다. 잠시 후 다시 시도하세요.": "Too many sign-in attempts. Try again in a moment.",
  "다른 주소에서 보낸 로그인 요청이라 거부했습니다. 대시보드 주소로 직접 접속해 다시 시도하세요.": "The sign-in request came from a different address and was rejected. Open the dashboard address directly and try again.",
  "비활성화된 계정입니다. 관리자에게 문의하세요.": "This account is disabled. Contact an administrator.",
  "인증 코드가 올바르지 않습니다. 다시 입력하세요.": "That code is not valid. Enter it again.",
  "패스키를 확인하지 못했습니다. 이 계정에 등록된 패스키인지 확인하세요.": "The passkey could not be verified. Make sure it is registered to this account.",
  "등록한 패스키나 인증 앱으로 본인임을 한 번 더 확인하세요.": "Confirm it's you with a registered passkey or your authenticator app.",
  "인증 앱으로 본인임을 한 번 더 확인하세요.": "Confirm it's you with your authenticator app.",

  // password fields
  "비밀번호 숨기기": "Hide password",
  "비밀번호 보기": "Show password",
  "Caps Lock이 켜져 있습니다.": "Caps Lock is on.",
  "매우 약함": "Very weak",
  "약함": "Weak",
  "강함": "Strong",
  "매우 강함": "Very strong",
  "8자 이상으로 늘리세요.": "Use at least 8 characters.",
  "흔히 쓰는 비밀번호입니다.": "This is a commonly used password.",
  "이름이나 이메일이 들어 있습니다.": "It contains your name or email.",
  "같은 문자를 반복하지 마세요.": "Avoid repeated characters.",
  "abc, 123, qwerty 같은 연속된 문자는 추측하기 쉽습니다.": "Sequences like abc, 123 or qwerty are easy to guess.",
  "단어를 몇 개 이어 붙이거나 숫자와 기호를 섞으세요.": "Join a few words or mix in numbers and symbols.",
  "비밀번호 강도: {level}": "Password strength: {level}",
  "비밀번호가 너무 깁니다.": "The password is too long.",

  // invite
  "초대 링크가 올바르지 않거나 만료되었습니다. 초대한 사람에게 새 링크를 요청하세요.": "This invite link is invalid or has expired. Ask the person who invited you for a new link.",
  "이미 계정이 있나요?": "Already have an account?",

  // OAuth consent
  "Aven 계정으로 로그인하고 고유 ID를 확인합니다.": "Sign in with your Aven account and read its unique ID.",
  "계정의 표시 이름을 확인합니다.": "Read your account's display name.",
  "계정 이메일 주소를 확인합니다.": "Read your account's email address.",
  "다시 로그인하지 않아도 계속 접근할 수 있도록 refresh token을 발급합니다.": "Issue a refresh token so the app keeps access without you signing in again.",
  "내가 접근할 수 있는 작업 공간의 프로젝트를 Viewer 권한으로 조회합니다.": "View projects in workspaces you can access, with Viewer permissions.",
  "내가 멤버인 작업 공간에서 내 권한 안으로 프로젝트와 리소스를 만들고 변경합니다.": "Create and change projects and resources in workspaces you belong to, within your own permissions.",
  "내가 관리하는 작업 공간의 프로젝트를 Admin 권한으로 관리합니다.": "Manage projects in workspaces you administer, with Admin permissions.",
  "선택한 프로젝트를 Viewer 권한으로 조회합니다.": "View the selected projects with Viewer permissions.",
  "선택한 프로젝트의 리소스를 Member 권한으로 만들고 변경합니다.": "Create and change resources in the selected projects with Member permissions.",
  "앱의 리디렉션 주소가 안전하지 않아 이동하지 않았습니다. 앱 관리자에게 알려 주세요.": "The app's redirect address is not safe, so you were not sent there. Let the app's administrator know.",
  "승인하면 {host}의 앱이 아래 권한을 얻습니다.": "If you approve, the app at {host} gets the permissions below.",
  "오래 유지됨": "Long-lived",
  "변경 가능": "Can make changes",
  "승인하려면 프로젝트를 하나 이상 고르거나 프로젝트 scope를 해제하세요.": "To approve, pick at least one project or uncheck the project scope.",
  "이 요청은 만료되었습니다. 앱에서 다시 연결해 주세요.": "This request has expired. Connect again from the app.",

  // account
  "현재 비밀번호와 같습니다.": "This is the same as your current password.",
  "Aven 복구 코드": "Aven recovery codes",
  "텍스트 파일로 저장": "Save as text file",
  "안전한 곳에 저장했습니다": "I've saved them somewhere safe",
  "다른 세션 모두 끊기": "Sign out all other sessions",
  "이 브라우저를 뺀 {count}개 세션이 로그아웃됩니다.": "{count} sessions other than this browser will be signed out.",
  "{name} 세션 끊기": "Sign out {name} session",
  "새 토큰: {name}": "New token: {name}",
  "복사했습니다": "I've copied it",
  "이 창을 닫으면 다시 볼 수 없습니다. CI 비밀 변수나 비밀번호 관리자에 바로 넣고, 코드나 채팅에는 붙여 넣지 마세요.": "Once you close this, the token can't be shown again. Put it straight into a CI secret or password manager, and don't paste it into code or chat.",
  "1에서 3650 사이의 일 수를 입력하세요.": "Enter a whole number of days from 1 to 3650.",

  // GitHub onboarding
  "GitHub 연결을 누르면 GitHub 승인 화면으로 이동합니다.": "Press Connect GitHub to open GitHub's authorization page.",
  "GitHub에서 Aven 앱을 승인합니다.": "Authorize the Aven app on GitHub.",
  "계정 화면으로 돌아오면 바로 저장소를 배포할 수 있습니다.": "Back on your account page, you can deploy your repositories right away.",
};

export default en;
