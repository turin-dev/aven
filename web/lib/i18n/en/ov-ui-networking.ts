// English translations added by the ui-networking overhaul.
const en: Record<string, string> = {
  // Domain add flow: live validation
  "도메인을 입력하세요": "Enter a domain",
  "포트 없이 도메인만 입력하세요. 대상 포트는 아래에서 고릅니다": "Enter the domain without a port. Pick the target port below",
  "IP 주소는 도메인으로 추가할 수 없습니다. DNS 이름을 입력하세요": "An IP address can't be added as a domain. Enter a DNS name",
  "도메인에는 영문 소문자, 숫자, 하이픈(-), 점(.)만 쓸 수 있습니다": "A domain can only contain lowercase letters, digits, hyphens (-) and dots (.)",
  "점(.)이 연달아 있거나 앞뒤에 있습니다": "The domain has two dots in a row, or starts or ends with a dot",
  "점 사이의 각 부분은 63자를 넘을 수 없습니다": "Each part between dots can be at most 63 characters",
  "각 부분은 하이픈(-)으로 시작하거나 끝날 수 없습니다": "A part can't start or end with a hyphen (-)",
  "마지막 부분(최상위 도메인)은 영문자 2자 이상이어야 합니다": "The last part (top-level domain) must be at least 2 letters",
  "example.com처럼 점(.)이 있는 전체 도메인을 입력하세요": "Enter a full domain with a dot, like example.com",
  "와일드카드(*)는 맨 앞에 *. 형태로만 쓸 수 있습니다": "A wildcard (*) is only allowed at the start, as *.",
  "와일드카드는 *.example.com처럼 *. 뒤에 두 부분 이상이 필요합니다": "A wildcard needs at least two parts after *., like *.example.com",
  "도메인은 253자를 넘을 수 없습니다": "A domain can be at most 253 characters",
  "이 서비스에 이미 추가된 도메인입니다": "This domain is already added to this service",
  "{host}으로 저장합니다": "Saved as {host}",
  "기본 도메인({domain}) 아래 이름이라 DNS 레코드 없이 생성 도메인으로 동작합니다": "This name is under the base domain ({domain}), so it works as a generated domain without DNS records",
  "와일드카드 도메인은 DNS에 * 레코드가 필요하고, 같은 이름의 구체적인 도메인이 있으면 그쪽이 우선합니다": "A wildcard domain needs a * record in DNS. A specific domain with the same name takes precedence",

  // Port picker
  "포트는 1부터 65535 사이의 숫자입니다": "A port is a number from 1 to 65535",
  "실행 중인 컨테이너에서 {port} 포트를 listen하는 프로세스를 찾지 못했습니다. 지금 listen 중인 포트: {ports}":
    "No process in the running containers listens on port {port}. Listening now: {ports}",

  // Domain status and DNS records
  "DNS 확인됨": "DNS verified",
  "DNS 제공자에 아래 레코드를 추가하세요. CNAME을 권장합니다. 필요하면 TXT 레코드나 Cloudflare 토큰으로 소유를 확인할 수 있습니다.":
    "Add the records below at your DNS provider. CNAME is recommended. If needed, verify ownership with a TXT record or Cloudflare token.",
  "TXT 레코드는 선택입니다. 위 주소 레코드가 맞으면 소유 확인 없이도 연결됩니다.": "The TXT record is optional. When the address records above match, the domain connects without it.",
  "Cloudflare 토큰으로 소유 확인": "Verify ownership with a Cloudflare token",
  "Cloudflare API 토큰": "Cloudflare API token",
  "이 도메인의 Cloudflare 존에 Zone:Read 권한이 필요합니다. 토큰은 확인에만 쓰고 저장하지 않으며 DNS를 변경하지 않습니다.":
    "The token needs Zone:Read access to this domain's Cloudflare zone. It is only used for verification, is not stored, and does not change DNS.",
  "토큰 만들기": "Create a token",
  "토큰으로 확인": "Verify with token",
  "마지막 확인 오류: {reason}": "Last check failed: {reason}",
  "{time}에 마지막 확인": "last checked {time}",
  "DNS가 퍼지는 데 몇 분에서 몇 시간이 걸릴 수 있습니다. 이 화면은 10초마다 다시 확인합니다.": "DNS changes can take minutes to hours to spread. This page checks again every 10 seconds.",
  "이름 복사": "Copy name",

  // Domain advanced settings
  "경로는 /로 시작하고 공백 없이 256자 이하여야 합니다": "A path starts with /, has no spaces and is at most 256 characters",
  "같은 경로 규칙이 이미 있습니다": "There is already a rule for this path",
  "접두어 제거는 /*로 끝나는 경로에만 쓸 수 있습니다": "Strip prefix only works on paths ending in /*",
  "HTTP만 사용": "Use HTTP only",
  "{host}의 인증서를 쓰지 않고 https 리다이렉트도 하지 않습니다. 앞단(Cloudflare, 터널)이 TLS를 끝내는 경우에만 쓰세요.":
    "{host} will have no certificate and no redirect to https. Use this only when something in front (Cloudflare, a tunnel) terminates TLS.",
  "저장 중…": "Saving…",
  "리다이렉트 도메인에는 경로 규칙을 둘 수 없습니다. 먼저 리다이렉트를 해제하세요.": "A redirecting domain can't have path rules. Remove the redirect first.",
  "리다이렉트 해제": "Remove redirect",
  "{host}로 오는 요청을 다시 이 서비스로 보냅니다.": "Requests to {host} will go to this service again.",
  "자기 자신으로 리다이렉트하면 무한 반복됩니다": "Redirecting a domain to itself loops forever",
  "호스트 이름이나 http(s) 주소를 입력하세요(공백 없이 300자 이하)": "Enter a host name or an http(s) URL (no spaces, at most 300 characters)",
  "경로 규칙이 있는 도메인은 리다이렉트할 수 없습니다. 먼저 경로 규칙을 지우세요.": "A domain with path rules can't redirect. Remove the path rules first.",
  "basic auth 해제": "Remove basic auth",
  "{host}에 누구나 비밀번호 없이 접속할 수 있게 됩니다.": "Anyone will be able to open {host} without a password.",
  "사용자 이름은 영문, 숫자, . _ @ - 로 64자까지 쓸 수 있습니다": "A user name is up to 64 letters, digits or . _ @ -",

  // Private networking, TCP proxy, outbound IPv6
  "같은 환경의 서비스에서": "From services in this environment",
  "전체 이름": "Full name",
  "변수 참조(호스트)": "Variable reference (host)",
  "변수 참조(연결 URL)": "Variable reference (connection URL)",
  "연결 예시": "Connection examples",
  "비공개 네트워크는 인터넷에 노출되지 않아 TLS 없이 http로 접속해도 됩니다. 변수 참조를 쓰면 이름이나 포트가 바뀌어도 다음 배포에 자동으로 따라갑니다. 앱은 0.0.0.0이나 ::에서 listen해야 합니다.":
    "The private network is not exposed to the internet, so plain http without TLS is fine. With a variable reference, a renamed endpoint or changed port is picked up on the next deploy. The app must listen on 0.0.0.0 or ::.",
  "인터넷 어디서나 이 주소로 접속할 수 있습니다. HTTP가 아닌 TCP(데이터베이스 등)를 그대로 전달하므로 서비스 자체의 비밀번호와 TLS로 보호하세요.":
    "Anyone on the internet can connect to this address. Raw TCP (databases and the like) is passed through as is, so protect it with the service's own password and TLS.",
  "주소 복사": "Copy address",
  "IPv6 주소가 있는 외부 서버에는 IPv6로, 나머지는 IPv4로 연결합니다. 외부 API가 IPv6에서 이상하게 동작할 때만 끄세요.":
    "Servers with an IPv6 address are reached over IPv6, everything else over IPv4. Turn this off only if an external API misbehaves over IPv6.",

  // Network diagnostics
  "이 이미지에는 getent나 nslookup이 없어 DNS를 확인하지 못했습니다.": "This image has neither getent nor nslookup, so DNS could not be checked.",
  "이 이미지에는 nc나 bash가 없어 TCP 연결을 확인하지 못했습니다.": "This image has neither nc nor bash, so the TCP connection could not be checked.",
  "{host} → {count}개 주소로 풀립니다.": "{host} resolves to {count} address(es).",
  "{host} 이름을 찾았습니다.": "{host} was found.",
  "{host} 이름을 찾지 못했습니다. 서비스 이름, 환경, 철자를 확인하세요.": "{host} was not found. Check the service name, the environment and the spelling.",
  "{host}:{port}에 연결했습니다.": "Connected to {host}:{port}.",
  "{host}:{port}에 연결하지 못했습니다. 대상이 이 포트에서 0.0.0.0이나 ::로 listen하는지, 같은 환경인지 확인하세요.":
    "Could not connect to {host}:{port}. Check that the target listens on this port on 0.0.0.0 or :: and is in the same environment.",
  "도구 출력": "Tool output",
  "호스트 이름이나 IP 주소만 입력하세요(공백, 경로, 특수 문자 없이)": "Enter only a host name or IP address (no spaces, paths or special characters)",
  // Edge: under attack, WAF, rules
  "공격 방어 모드 켜기": "Turn on Under Attack Mode",
  "새 방문자는 모두 브라우저 확인을 거쳐야 하고, 브라우저가 아닌 API 클라이언트와 웹훅은 막힙니다. 이 서비스의 모든 공개 도메인에 적용됩니다.":
    "Every new visitor has to pass a browser check, and non-browser API clients and webhooks are blocked. This applies to all public domains of this service.",
  "요청 수는 1부터 1,000,000 사이의 정수입니다": "The request count is a whole number from 1 to 1,000,000",
  "클라이언트 IP 하나가 {window} 동안 {count}회를 넘게 요청하면 429로 거절합니다.": "A client IP that sends more than {count} requests in {window} gets 429.",
  "허용·차단 목록은 각각 256개까지입니다": "The allow and deny lists hold up to 256 entries each",
  "IP 주소나 CIDR이 아닙니다: {entries}": "Not an IP address or CIDR: {entries}",
  "다음이 아닐 때: {cond}": "not ({cond})",
  "{status} 리다이렉트 → {to}": "{status} redirect → {to}",
  "경로를 {path}로 다시 씀": "rewrite path to {path}",
  "만약": "if",
  "규칙 삭제": "Delete rule",
  "“{name}” 규칙을 목록에서 뺍니다. 저장을 눌러야 실제로 적용됩니다.": "Removes the rule “{name}” from the list. It takes effect when you save.",
  "저장하지 않은 규칙 변경이 있습니다": "You have unsaved rule changes",
  "JSON은 version 1과 rules 배열이 있는 객체여야 합니다": "The JSON must be an object with version 1 and a rules array",
  "값을 입력하세요": "Enter a value",
  "목록은 64개까지입니다": "A list holds up to 64 entries",
  "값은 256자까지입니다": "A value can be at most 256 characters",
  "패턴에는 *를 8개까지 쓸 수 있습니다": "A pattern can contain up to 8 *",
  "헤더 이름을 입력하세요(영문, 숫자, - 등 공백 없이)": "Enter a header name (letters, digits, - and so on, no spaces)",
  "IP 주소나 CIDR을 입력하세요": "Enter IP addresses or CIDRs",
  "IPv4 주소나 CIDR을 입력하세요": "Enter IPv4 addresses or CIDRs",
  "GET, POST 같은 HTTP 메서드를 입력하세요": "Enter HTTP methods such as GET or POST",
  "차단 상태 코드는 400부터 499 사이입니다": "A block status code is from 400 to 499",
  "응답 본문은 4096바이트까지입니다": "The response body can be at most 4096 bytes",
  "리다이렉트할 호스트나 URL을 입력하세요": "Enter the host or URL to redirect to",
  "https://로 시작하는 전체 URL을 입력하세요": "Enter a full URL starting with https://",
  "올바른 호스트 이름을 입력하세요": "Enter a valid host name",
  "리다이렉트 상태 코드는 301, 302, 307, 308 중 하나입니다": "A redirect status code is 301, 302, 307 or 308",
  "캐시 기간은 1초부터 30일(2592000초)까지입니다": "The cache duration is from 1 second to 30 days (2592000 seconds)",
  "새 경로는 /로 시작하고 공백이나 #이 없어야 합니다": "The new path starts with / and has no spaces or #",
  "이 헤더는 규칙으로 바꿀 수 없습니다": "This header can't be changed by a rule",
  "헤더 값에는 줄바꿈을 쓸 수 없고 2048자까지입니다": "A header value can't contain line breaks and is at most 2048 characters",
  "조건 {n}: {problem}": "Condition {n}: {problem}",
};

export default en;
