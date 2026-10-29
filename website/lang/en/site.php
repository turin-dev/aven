<?php

return [
    'title' => 'Aven: your own PaaS on your own servers',
    'description' => 'Aven is an open-source, self-hosted platform for apps, databases and background jobs. Connect a repository, get a domain with TLS, add Postgres in one click.',

    'nav' => [
        'features' => 'Features',
        'compare' => 'Compare',
        'install' => 'Install',
        'docs' => 'Docs',
        'github' => 'GitHub',
        'switch' => '한국어',
    ],

    'hero' => [
        'title' => 'Your own PaaS, on your own servers.',
        'lead' => 'Connect a repository, get a domain with TLS, add Postgres in one click. Aven is open source and runs on any Linux server with Docker.',
        'primary' => 'Install Aven',
        'secondary' => 'Star on GitHub',
        'copy' => 'Copy',
        'copied' => 'Copied',
        'release' => 'Latest: :version',
    ],

    'canvas' => [
        'label' => 'Animated example of a project on the Aven canvas',
        'building' => 'Building',
        'deploying' => 'Deploying',
        'online' => 'Online',
        'toast' => 'api deployed in 41s',
        'hint' => 'Drag the services around.',
        'commit' => 'fix: cache home page queries',
    ],

    'features' => [
        [
            'key' => 'deploy',
            'title' => 'Push, and it is live',
            'text' => 'Point Aven at a GitHub repository. Every push builds with your Dockerfile or with Railpack, waits for the health check and switches traffic without downtime. Pull requests get their own preview environment.',
            'points' => ['Dockerfile or Railpack, no config needed', 'Zero-downtime switch and one-click rollback', 'Preview environment per pull request'],
        ],
        [
            'key' => 'data',
            'title' => 'Databases that look after themselves',
            'text' => 'Start PostgreSQL, MySQL, Redis or MongoDB next to your app. Aven wires the connection string into your variables and keeps backups on S3.',
            'points' => ['Point-in-time recovery for Postgres and MySQL', 'PgBouncer, major version upgrades, HA clusters', 'S3-compatible buckets'],
        ],
        [
            'key' => 'network',
            'title' => 'Domains, TLS and IPv6 handled',
            'text' => 'Add a domain and Aven issues the certificate. Every domain can have its own public IPv6 address, and services talk to each other over private DNS names.',
            'points' => ['Automatic Let\'s Encrypt certificates', 'A dedicated IPv6 address per domain', 'TCP proxies, path rules, redirects'],
        ],
        [
            'key' => 'servers',
            'title' => 'One server today, ten tomorrow',
            'text' => 'Install the agent on another machine with one command. Place services where you want, spread replicas, autoscale, and move a service together with its volume.',
            'points' => ['Encrypted mesh over Tailscale or WireGuard', 'Autoscaling on CPU, memory or requests', 'Move services with their data'],
        ],
    ],

    'also' => [
        'title' => 'Also in the box',
        'items' => ['Cron jobs', 'Sleeping apps', 'Sealed variables', 'Logs and metrics', 'OpenTelemetry traces', 'Alerts to Slack and Discord', 'Passkeys and 2FA', 'SAML SSO', 'Audit log', 'REST and GraphQL API', 'MCP server', 'Infrastructure as code', 'railway.json support', 'Import from Dokploy'],
    ],

    'compare' => [
        'title' => 'How it compares',
        'lead' => 'Aven next to the platforms people usually move from.',
        'note' => 'Competitor cells come from their official pages, checked on :date. "Not documented" means those pages did not describe the feature, not that it is missing. Every cell links to its source; the full table with footnotes is in the <a href=":readme">README</a>.',
        'legend' => ['yes' => 'Yes', 'no' => 'No', 'partial' => 'Partly', 'unknown' => 'Not documented'],
    ],

    'shots' => [
        'title' => 'The dashboard',
        'items' => [
            ['img' => 'canvas', 'name' => 'Canvas', 'alt' => 'A project on the Aven canvas with services and databases'],
            ['img' => 'deployments', 'name' => 'Deployments', 'alt' => 'Service panel with the active deployment and history'],
            ['img' => 'variables', 'name' => 'Variables', 'alt' => 'Service variables with references to PostgreSQL and Redis'],
            ['img' => 'networking', 'name' => 'Networking', 'alt' => 'Public and private networking settings of a service'],
        ],
    ],

    'install' => [
        'title' => 'Up and running in a few minutes',
        'lead' => 'You need a Linux server (amd64 or arm64) with Docker and ports 80 and 443 free.',
        'steps' => [
            ['name' => 'Run the installer', 'text' => 'It asks for your dashboard URL, a base domain for services, an email for Let\'s Encrypt and an admin password.'],
            ['name' => 'Point your DNS', 'text' => 'The installer prints the records to create. Add a wildcard if you want generated service domains.'],
            ['name' => 'Deploy something', 'text' => 'Sign in, press Add on the canvas and pick a repository, an image or a database.'],
        ],
        'transcript' => 'This website was deployed the same way:',
        'guide' => 'Self-hosting guide',
    ],

    'faq' => [
        'title' => 'Questions',
        'items' => [
            ['q' => 'Is Aven a Railway fork?', 'a' => 'No. It is written from scratch and contains no Railway code. The dashboard follows Railway\'s layout because many people know it, and Aven reads railway.json and answers Railway-shaped GraphQL queries, so moving over is easy.'],
            ['q' => 'Can I run production on it?', 'a' => 'Its authors do. It is young, though: read the release notes before upgrading and back up /var/lib/aven, which holds the database and the key that encrypts secrets.'],
            ['q' => 'Do I need GitHub?', 'a' => 'Only for push-to-deploy and preview environments. Docker images, Compose files, templates and aven up work without it.'],
            ['q' => 'I use Dokploy. Can I move?', 'a' => 'aven import dokploy recreates services, variables, domains and volumes. Database contents move with your usual dump and restore.'],
            ['q' => 'Why AGPL?', 'a' => 'So that a modified Aven offered to others stays open. Running it for your own apps carries no obligations.'],
        ],
    ],

    'cta' => [
        'title' => 'Bring your own server.',
        'text' => 'Aven is free and open source.',
    ],

    'footer' => [
        'line' => 'Aven is open source under AGPL-3.0. This site runs on Aven. Railway is a trademark of Railway Corporation and is named here only to describe compatibility.',
        'source' => 'Source',
        'releases' => 'Releases',
        'security' => 'Security',
    ],
];
