<?php

// Condensed from the comparison table in the aven README (sources checked 2026-10-27).
// Each cell: [mark, English, Korean, source URL]. mark: yes | no | partial | unknown ("not documented").

$repo = 'https://github.com/turin-dev/aven';

return [
    'checked' => '2026-10-27',
    'readme' => $repo.'#comparison',
    'products' => ['Aven', 'Railway', 'Coolify', 'Dokploy'],
    'rows' => [
        [
            'label' => ['Runs on your own servers', '내 서버에서 실행'],
            'cells' => [
                ['yes', 'aven-server, plus aven-agent on more hosts', 'aven-server, 추가 호스트는 aven-agent', $repo.'/blob/main/docs/self-hosting.md'],
                ['no', 'Hosted only. Enterprise gets dedicated Railway hosts', '호스팅 전용. Enterprise는 Railway 전용 호스트', 'https://docs.railway.com/maturity/enterprise'],
                ['yes', 'Self-hosted is the free edition', '셀프호스팅이 무료 에디션', 'https://coolify.io/pricing'],
                ['yes', 'Self-Hosted (OSS)', 'Self-Hosted (OSS)', 'https://docs.dokploy.com/docs/core/differences'],
            ],
        ],
        [
            'label' => ['Pricing', '요금'],
            'cells' => [
                ['yes', 'Free. You pay for your own machines', '무료. 서버 비용만 냅니다', $repo],
                ['partial', 'Per-second usage plus a plan fee', '초 단위 사용량 + 플랜 요금', 'https://railway.com/pricing'],
                ['yes', 'Self-hosted free. Cloud from $5/month', '셀프호스팅 무료. Cloud 월 $5부터', 'https://coolify.io/pricing'],
                ['yes', 'OSS free. Cloud from $4.50/server/month', 'OSS 무료. Cloud 서버당 월 $4.50부터', 'https://docs.dokploy.com/docs/core/differences'],
            ],
        ],
        [
            'label' => ['License', '라이선스'],
            'cells' => [
                ['yes', 'AGPL-3.0', 'AGPL-3.0', $repo.'/blob/main/LICENSE'],
                ['no', 'Platform closed source. CLI is open', '플랫폼 비공개. CLI만 오픈소스', 'https://docs.railway.com/cli'],
                ['yes', 'Apache-2.0', 'Apache-2.0', 'https://github.com/coollabsio/coolify/blob/main/LICENSE'],
                ['partial', 'Apache-2.0, a /proprietary directory excepted', 'Apache-2.0, /proprietary 디렉터리는 예외', 'https://github.com/Dokploy/dokploy/blob/canary/LICENSE.MD'],
            ],
        ],
        [
            'label' => ['SSO without paying', '무료로 SSO'],
            'cells' => [
                ['yes', 'SAML SSO in the core', '코어에 SAML SSO', $repo.'/blob/main/docs/permissions.md'],
                ['no', 'Enterprise and committed spend', 'Enterprise, committed spend만', 'https://railway.com/pricing'],
                ['yes', 'Self-hosted, from v4.3.0', '셀프호스팅 v4.3.0부터', 'https://coolify.io/docs/core/security/authentication/sso/overview'],
                ['no', 'Enterprise only', 'Enterprise 전용', 'https://docs.dokploy.com/docs/core/differences'],
            ],
        ],
        [
            'label' => ['Passkeys', '패스키'],
            'cells' => [
                ['yes', 'WebAuthn sign-in', 'WebAuthn 로그인', $repo],
                ['yes', 'As account MFA', '계정 MFA로', 'https://docs.railway.com/access/multi-factor-authentication'],
                ['unknown', 'Not documented (2FA and SSO are)', '문서에 없음 (2FA, SSO는 있음)', 'https://coolify.io/docs/troubleshoot/dashboard/disable-2fa-manually'],
                ['yes', 'Panel sign-in', '패널 로그인', 'https://docs.dokploy.com/docs/core/account-security'],
            ],
        ],
        [
            'label' => ['Several servers', '여러 서버'],
            'cells' => [
                ['yes', 'More servers join with aven-agent', 'aven-agent로 서버 추가', $repo.'/blob/main/docs/self-hosting.md'],
                ['unknown', 'Railway regions and replicas. Your own servers not documented', 'Railway 지역·replica. 내 서버 연결은 문서에 없음', 'https://docs.railway.com/deployments'],
                ['yes', 'Unlimited servers', '서버 무제한', 'https://coolify.io/pricing'],
                ['yes', 'Multi-server on OSS', 'OSS에서 멀티 서버', 'https://docs.dokploy.com/docs/core/differences'],
            ],
        ],
        [
            'label' => ['Preview environments', '미리보기 환경'],
            'cells' => [
                ['yes', 'Per pull request', 'PR마다', $repo],
                ['yes', 'Per pull request', 'PR마다', 'https://docs.railway.com/guides/preview-deployments-with-pr-environments'],
                ['yes', 'Pull and merge requests', 'PR, MR', 'https://coolify.io/docs/applications/deployments/preview-deployments'],
                ['yes', 'GitHub PRs, off by default', 'GitHub PR, 기본값 꺼짐', 'https://docs.dokploy.com/docs/core/applications/preview-deployments'],
            ],
        ],
        [
            'label' => ['Databases', '데이터베이스'],
            'cells' => [
                ['yes', 'Postgres, MySQL, Redis, MongoDB, S3 bucket', 'Postgres, MySQL, Redis, MongoDB, S3 버킷', $repo.'/blob/main/docs/cli.md'],
                ['yes', 'Postgres, MySQL, Redis, MongoDB', 'Postgres, MySQL, Redis, MongoDB', 'https://docs.railway.com/databases/postgresql'],
                ['yes', 'Eight engines, incl. MariaDB and ClickHouse', 'MariaDB, ClickHouse 포함 8종', 'https://coolify.io/docs/databases'],
                ['yes', 'MySQL, Postgres, MongoDB, Redis, MariaDB', 'MySQL, Postgres, MongoDB, Redis, MariaDB', 'https://docs.dokploy.com/docs/core/features'],
            ],
        ],
        [
            'label' => ['Point-in-time recovery', '시점 복구 (PITR)'],
            'cells' => [
                ['yes', 'Postgres WAL and MySQL binlog', 'Postgres WAL, MySQL binlog', $repo.'/blob/main/docs/cli.md'],
                ['yes', 'Postgres and MySQL, opt-in', 'Postgres, MySQL (직접 켬)', 'https://docs.railway.com/volumes/point-in-time-recovery'],
                ['partial', 'Scheduled dumps. PITR set up separately', '예약 덤프. PITR은 따로 구성', 'https://coolify.io/self-host-postgres-with-backups'],
                ['partial', 'Scheduled backups. PITR not documented', '예약 백업. PITR은 문서에 없음', 'https://docs.dokploy.com/docs/core/features'],
            ],
        ],
        [
            'label' => ['Deploy on git push', 'git push 배포'],
            'cells' => [
                ['yes', 'GitHub', 'GitHub', $repo.'/blob/main/docs/self-hosting.md'],
                ['yes', 'GitHub', 'GitHub', 'https://docs.railway.com/deployments/github-autodeploys'],
                ['yes', 'GitHub, GitLab, webhooks', 'GitHub, GitLab, webhook', 'https://coolify.io/docs/applications/deployments/automatic-deployments'],
                ['yes', 'GitHub, GitLab, Bitbucket, Gitea', 'GitHub, GitLab, Bitbucket, Gitea', 'https://docs.dokploy.com/docs/core/auto-deploy'],
            ],
        ],
        [
            'label' => ['Builds', '빌드'],
            'cells' => [
                ['yes', 'Dockerfile, Railpack', 'Dockerfile, Railpack', $repo.'/blob/main/docs/self-hosting.md'],
                ['yes', 'Railpack, Dockerfile', 'Railpack, Dockerfile', 'https://docs.railway.com/builds'],
                ['yes', 'Nixpacks, Dockerfile, Compose, static', 'Nixpacks, Dockerfile, Compose, static', 'https://coolify.io/docs/applications/builds/nixpacks/deploy'],
                ['yes', 'Nixpacks, Railpack, Dockerfile, buildpacks', 'Nixpacks, Railpack, Dockerfile, buildpack', 'https://docs.dokploy.com/docs/core/applications/build-type'],
            ],
        ],
        [
            'label' => ['CLI', 'CLI'],
            'cells' => [
                ['yes', 'aven', 'aven', $repo.'/blob/main/docs/cli.md'],
                ['yes', 'railway', 'railway', 'https://docs.railway.com/cli'],
                ['yes', 'coolify', 'coolify', 'https://coolify.io/docs/cli/command-reference/commands/app/create-deploy-key'],
                ['yes', '@dokploy/cli', '@dokploy/cli', 'https://docs.dokploy.com/docs/cli'],
            ],
        ],
    ],
];
