<?php

return [
    'title' => 'Aven: 내 서버에 올리는 나만의 PaaS',
    'description' => 'Aven은 앱, 데이터베이스, 백그라운드 작업을 위한 오픈소스 셀프호스트 플랫폼입니다. 저장소를 연결하면 TLS 도메인이 붙고, Postgres는 클릭 한 번이면 됩니다.',

    'nav' => [
        'features' => '기능',
        'compare' => '비교',
        'install' => '설치',
        'docs' => '문서',
        'github' => 'GitHub',
        'switch' => 'English',
    ],

    'hero' => [
        'title' => '내 서버에 올리는 나만의 PaaS.',
        'lead' => '저장소를 연결하면 TLS 도메인이 붙고, Postgres는 클릭 한 번이면 됩니다. Aven은 오픈소스이고 Docker가 있는 Linux 서버면 어디서든 돕니다.',
        'primary' => 'Aven 설치하기',
        'secondary' => 'GitHub에서 Star',
        'copy' => '복사',
        'copied' => '복사됨',
        'release' => '최신 :version',
    ],

    'canvas' => [
        'label' => 'Aven 캔버스의 프로젝트 예시 애니메이션',
        'building' => '빌드 중',
        'deploying' => '배포 중',
        'online' => '온라인',
        'toast' => 'api 배포 완료 (41초)',
        'hint' => '서비스 카드를 끌어서 옮겨 보세요.',
        'commit' => 'fix: cache home page queries',
    ],

    'features' => [
        [
            'key' => 'deploy',
            'title' => '푸시하면 바로 배포',
            'text' => 'GitHub 저장소를 연결하세요. 푸시할 때마다 Dockerfile이나 Railpack으로 빌드하고, 헬스체크를 통과하면 중단 없이 트래픽을 넘깁니다. PR마다 미리보기 환경도 따로 생깁니다.',
            'points' => ['Dockerfile 또는 Railpack, 설정 없이', '무중단 전환, 클릭 한 번으로 롤백', 'PR마다 미리보기 환경'],
        ],
        [
            'key' => 'data',
            'title' => '알아서 관리되는 데이터베이스',
            'text' => '앱 옆에 PostgreSQL, MySQL, Redis, MongoDB를 띄우세요. 접속 주소는 변수로 자동 연결되고, 백업은 S3에 쌓입니다.',
            'points' => ['Postgres, MySQL 시점 복구', 'PgBouncer, 메이저 버전 업그레이드, HA 클러스터', 'S3 호환 버킷'],
        ],
        [
            'key' => 'network',
            'title' => '도메인, TLS, IPv6까지',
            'text' => '도메인을 추가하면 인증서는 Aven이 발급합니다. 도메인마다 공인 IPv6 주소를 따로 줄 수 있고, 서비스끼리는 사설 DNS 이름으로 통신합니다.',
            'points' => ['Let\'s Encrypt 인증서 자동 발급', '도메인마다 전용 IPv6 주소', 'TCP 프록시, 경로 규칙, 리디렉션'],
        ],
        [
            'key' => 'servers',
            'title' => '오늘은 한 대, 내일은 열 대',
            'text' => '다른 머신에 명령 한 줄로 에이전트를 설치하세요. 서비스를 원하는 서버에 두고, 레플리카를 나누고, 오토스케일하고, 볼륨째 옮길 수 있습니다.',
            'points' => ['Tailscale, WireGuard 위의 암호화 메시', 'CPU, 메모리, 요청 수 기준 오토스케일', '데이터째 서비스 이동'],
        ],
    ],

    'also' => [
        'title' => '그 밖에 들어 있는 것',
        'items' => ['cron 작업', '유휴 앱 슬립', '봉인 변수', '로그와 메트릭', 'OpenTelemetry 트레이스', 'Slack, Discord 알림', '패스키, 2FA', 'SAML SSO', '감사 로그', 'REST, GraphQL API', 'MCP 서버', '코드형 인프라', 'railway.json 지원', 'Dokploy에서 가져오기'],
    ],

    'compare' => [
        'title' => '다른 플랫폼과 비교',
        'lead' => '많이들 옮겨 오는 플랫폼과 나란히 놓고 봤습니다.',
        'note' => '다른 제품 칸은 각 공식 페이지에서 가져왔고, 확인 날짜는 :date입니다. "문서에 없음"은 그 페이지가 기능을 설명하지 않았다는 뜻이지 기능이 없다는 뜻이 아닙니다. 칸마다 출처 링크가 있고, 각주가 달린 전체 표는 <a href=":readme">README</a>에 있습니다.',
        'legend' => ['yes' => '있음', 'no' => '없음', 'partial' => '일부', 'unknown' => '문서에 없음'],
    ],

    'shots' => [
        'title' => '대시보드',
        'items' => [
            ['img' => 'canvas', 'name' => '캔버스', 'alt' => '서비스와 데이터베이스가 놓인 Aven 캔버스'],
            ['img' => 'deployments', 'name' => '배포', 'alt' => '활성 배포와 기록이 보이는 서비스 패널'],
            ['img' => 'variables', 'name' => '변수', 'alt' => 'PostgreSQL과 Redis를 참조하는 서비스 변수'],
            ['img' => 'networking', 'name' => '네트워킹', 'alt' => '서비스의 공개·사설 네트워킹 설정'],
        ],
    ],

    'install' => [
        'title' => '몇 분이면 끝나는 설치',
        'lead' => 'Docker가 있고 80, 443 포트가 비어 있는 Linux 서버(amd64, arm64)가 필요합니다.',
        'steps' => [
            ['name' => '설치 스크립트 실행', 'text' => '대시보드 주소, 서비스용 기본 도메인, Let\'s Encrypt 이메일, 관리자 비밀번호를 물어봅니다.'],
            ['name' => 'DNS 연결', 'text' => '만들어야 할 레코드를 스크립트가 알려줍니다. 서비스 도메인을 자동으로 받으려면 와일드카드도 추가하세요.'],
            ['name' => '배포하기', 'text' => '로그인해서 캔버스의 추가 버튼을 누르고 저장소, 이미지, 데이터베이스 중 하나를 고르세요.'],
        ],
        'transcript' => '이 웹사이트도 같은 방식으로 배포했습니다:',
        'guide' => '셀프호스팅 가이드',
    ],

    'faq' => [
        'title' => '자주 묻는 질문',
        'items' => [
            ['q' => 'Railway 포크인가요?', 'a' => '아닙니다. 처음부터 새로 작성했고 Railway 코드는 없습니다. 많은 사람이 익숙해서 대시보드 배치를 Railway에서 따왔고, railway.json을 읽고 Railway 형태의 GraphQL 질의에 답하기 때문에 옮겨오기 쉽습니다.'],
            ['q' => '프로덕션에 써도 되나요?', 'a' => '만든 사람들은 쓰고 있습니다. 다만 아직 초기라서 업그레이드 전에 릴리스 노트를 읽고, 데이터베이스와 비밀값 암호화 키가 있는 /var/lib/aven을 백업하세요.'],
            ['q' => 'GitHub가 있어야 하나요?', 'a' => '푸시 배포와 미리보기 환경에만 필요합니다. Docker 이미지, Compose 파일, 템플릿, aven up은 GitHub 없이 됩니다.'],
            ['q' => 'Dokploy를 쓰고 있는데 옮길 수 있나요?', 'a' => 'aven import dokploy가 서비스, 변수, 도메인, 볼륨을 다시 만듭니다. 데이터베이스 내용은 평소처럼 덤프하고 복원하면 됩니다.'],
            ['q' => '왜 AGPL인가요?', 'a' => '수정한 Aven을 남에게 제공할 때 그 코드가 공개되도록 하려고요. 내 앱을 위해 돌리는 데는 아무 의무가 없습니다.'],
        ],
    ],

    'cta' => [
        'title' => '서버만 가져오세요.',
        'text' => 'Aven은 무료 오픈소스입니다.',
    ],

    'footer' => [
        'line' => 'Aven은 AGPL-3.0 오픈소스입니다. 이 사이트도 Aven 위에서 돌아갑니다. Railway는 Railway Corporation의 상표이며 호환성 설명에만 썼습니다.',
        'source' => '소스',
        'releases' => '릴리스',
        'security' => '보안',
    ],
];
