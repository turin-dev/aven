# Aven

> {!! __('site.description') !!}

- Website: {!! route('home') !!} (Korean: {!! route('home.ko') !!})
- Source: {!! $repo !!} (AGPL-3.0)
- Latest release: {!! $release !!}
- Install: `{!! $install !!}`

## What Aven is

{!! __('site.hero.lead') !!}

Aven is a self-hosted platform for apps, databases and background jobs. The dashboard has a canvas per environment, like Railway. Underneath it is one Go binary, `aven-server`, running next to Docker with its state in SQLite. More servers join with `aven-agent` over an mTLS mesh (Tailscale or WireGuard addresses). There is no Kubernetes. Aven is free, has no usage fees and no paid feature tier; you pay only for your own servers.

Aven is not a fork, clone or redistribution of Railway and contains no Railway code. It reads `railway.json` / `railway.toml` and answers Railway-shaped GraphQL queries so projects can move over.

## Features

@foreach (__('site.features') as $f)
### {!! $f['title'] !!}

{!! $f['text'] !!}

@foreach ($f['points'] as $point)
- {!! $point !!}
@endforeach

@endforeach
### Also included

@foreach (__('site.also.items') as $item)
- {!! $item !!}
@endforeach

## Comparison with Railway, Coolify and Dokploy

Condensed from the comparison table in the Aven README. Competitor cells come from their official pages, checked on {!! $compare['checked'] !!}. "Not documented" means those pages did not describe the feature, not that it is missing. Full table with footnotes: {!! $compare['readme'] !!}

| | {!! implode(' | ', $compare['products']) !!} |
| --- | {!! implode(' | ', array_fill(0, count($compare['products']), '---')) !!} |
@foreach ($compare['rows'] as $row)
| {!! $row['label'][0] !!} | {!! implode(' | ', array_map(fn ($c) => __('site.compare.legend.'.$c[0]).'. '.$c[1].' ([source]('.$c[3].'))', $row['cells'])) !!} |
@endforeach

## Install

{!! __('site.install.lead') !!}

```sh
{!! $install !!}
```

@foreach (__('site.install.steps') as $i => $step)
{{ $i + 1 }}. **{!! $step['name'] !!}.** {!! $step['text'] !!}
@endforeach

Self-hosting guide: {!! $repo !!}/blob/main/docs/self-hosting.md

## Questions

@foreach (__('site.faq.items') as $item)
### {!! $item['q'] !!}

{!! $item['a'] !!}

@endforeach
## 한국어 요약

{!! $ko('site.description') !!}

@foreach ($ko('site.faq.items') as $item)
**{!! $item['q'] !!}** {!! $item['a'] !!}

@endforeach
