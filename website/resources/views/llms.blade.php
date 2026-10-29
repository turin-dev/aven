# Aven

> {!! __('site.description') !!} Licensed AGPL-3.0, free, no paid tier. Latest release: {!! $release !!}.

Aven is a self-hosted PaaS (platform as a service) in the style of Railway: a dashboard with a canvas per environment, GitHub push-to-deploy, managed databases, domains with automatic TLS and multi-server support. It is one Go binary (`aven-server`) next to Docker with state in SQLite; more servers join with `aven-agent`. It is not a Railway fork and contains no Railway code.

Install on a Linux server (amd64 or arm64) with Docker and ports 80/443 free:

```sh
{!! $install !!}
```

## Docs

- [Website (English)]({!! route('home') !!}): overview, features, comparison, install
- [Website (Korean)]({!! route('home.ko') !!}): 한국어 소개 페이지
- [Full site as Markdown]({!! route('llms.full') !!}): everything on the website, including the comparison table with sources
- [Self-hosting guide]({!! $repo !!}/blob/main/docs/self-hosting.md): install, DNS, configuration, backups, upgrades, adding servers
- [CLI reference]({!! $repo !!}/blob/main/docs/cli.md): the `aven` command
- [HTTP API]({!! $repo !!}/blob/main/docs/api-v1.md) and [GraphQL API]({!! $repo !!}/blob/main/docs/graphql.md)
- [README]({!! $repo !!}#readme): highlights, architecture, comparison with Railway, Coolify and Dokploy, FAQ

## Optional

- [Source code]({!! $repo !!}): GitHub, AGPL-3.0
- [Releases]({!! $repo !!}/releases)
- [Security policy]({!! $repo !!}/blob/main/SECURITY.md)
