@php
    $ko = $locale === 'ko';
    $home = $ko ? route('home.ko') : route('home');
    $docs = $repo.'/blob/main/docs/self-hosting'.($ko ? '.ko' : '').'.md';
    $col = $ko ? 2 : 1; // index of the localized text in config/compare.php cells
    $compare = config('compare');
    $marks = ['yes' => '✓', 'no' => '✕', 'partial' => '◐', 'unknown' => '?'];
@endphp
<!DOCTYPE html>
<html lang="{{ $locale }}">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>{{ __('site.title') }}</title>
    <meta name="description" content="{{ __('site.description') }}">
    <link rel="canonical" href="{{ $home }}">
    <link rel="alternate" hreflang="en" href="{{ route('home') }}">
    <link rel="alternate" hreflang="ko" href="{{ route('home.ko') }}">
    <link rel="alternate" hreflang="x-default" href="{{ route('home') }}">
    <meta property="og:type" content="website">
    <meta property="og:site_name" content="Aven">
    <meta property="og:url" content="{{ $home }}">
    <meta property="og:title" content="{{ __('site.title') }}">
    <meta property="og:description" content="{{ __('site.description') }}">
    <meta property="og:locale" content="{{ $ko ? 'ko_KR' : 'en_US' }}">
    <meta property="og:image" content="{{ asset('img/og-'.$locale.'.png') }}">
    <meta property="og:image:width" content="1200">
    <meta property="og:image:height" content="630">
    <meta name="twitter:card" content="summary_large_image">
    <meta name="theme-color" content="#ffffff">
    <script id="discord:component-embed" type="application/json">{!! $discord !!}</script>
    <script type="application/ld+json">{!! $jsonld !!}</script>
    <link rel="alternate" type="text/markdown" href="{{ route('llms.full') }}" title="Aven (Markdown, for AI tools)">
    <link rel="icon" href="{{ asset('img/icon-32.png') }}" sizes="32x32" type="image/png">
    <link rel="icon" href="{{ asset('img/icon-192.png') }}" sizes="192x192" type="image/png">
    <link rel="apple-touch-icon" href="{{ asset('apple-touch-icon.png') }}">
    <link rel="preload" href="{{ asset('fonts/pretendard/PretendardVariable.subset.91.woff2') }}" as="font" type="font/woff2" crossorigin>
    <link rel="stylesheet" href="{{ asset('css/fonts.css') }}?v={{ filemtime(public_path('css/fonts.css')) }}">
    <link rel="stylesheet" href="{{ asset('css/site.css') }}?v={{ filemtime(public_path('css/site.css')) }}">
</head>
<body class="lang-{{ $locale }}">

<header class="nav">
    <div class="wrap nav-in">
        <a class="logo" href="{{ $home }}"><img src="{{ asset('img/logo.webp') }}" alt="Aven" width="222" height="52"></a>
        <nav aria-label="Primary">
            <a class="hide-sm" href="#features">{{ __('site.nav.features') }}</a>
            <a class="hide-sm" href="#compare">{{ __('site.nav.compare') }}</a>
            <a class="hide-sm" href="#install">{{ __('site.nav.install') }}</a>
            <a class="hide-sm" href="{{ $docs }}">{{ __('site.nav.docs') }}</a>
            <a href="{{ $ko ? route('home') : route('home.ko') }}" hreflang="{{ $ko ? 'en' : 'ko' }}">{{ __('site.nav.switch') }}</a>
            <a class="btn btn-dark btn-sm" href="{{ $repo }}">{{ __('site.nav.github') }}</a>
        </nav>
    </div>
</header>

<main>
    {{-- Hero: the first screen is a canvas; the pitch is the main service card --}}
    @php
        $sats = [
            ['id' => 'storefront', 'ico' => 'S', 'sub' => 'shop.example.com', 'x' => 13, 'y' => 20, 'm' => [16, 9]],
            ['id' => 'api', 'ico' => 'A', 'sub' => 'api.example.com', 'x' => 87, 'y' => 18, 'm' => [84, 9]],
            ['id' => 'worker', 'ico' => 'W', 'sub' => 'cron */5 * * * *', 'x' => 10, 'y' => 74, 'm' => [14, 91]],
            ['id' => 'postgres', 'ico' => 'PG', 'sub' => 'pg-data 4.2 GB', 'x' => 89, 'y' => 66, 'm' => [50, 92]],
            ['id' => 'redis', 'ico' => 'RD', 'sub' => 'cache', 'x' => 76, 'y' => 92, 'm' => [86, 91]],
        ];
    @endphp
    <section class="stage" data-stage aria-label="{{ __('site.canvas.label') }}">
        <svg class="links" aria-hidden="true"></svg>
        @foreach ($sats as $i => $n)
            <div class="sat" style="left: {{ $n['x'] }}%; top: {{ $n['y'] }}%; --i: {{ $i }};" data-sat="{{ $n['id'] }}" data-mid="{{ $n['m'][0] }},{{ $n['m'][1] }}" data-state="building" aria-hidden="true">
                <span class="sat-top"><span class="ico">{{ $n['ico'] }}</span><b>{{ $n['id'] }}</b></span>
                <small>{{ $n['sub'] }}</small>
                <span class="state">
                    <i></i>
                    <span data-l="building">{{ __('site.canvas.building') }}</span>
                    <span data-l="deploying">{{ __('site.canvas.deploying') }}</span>
                    <span data-l="online">{{ __('site.canvas.online') }}</span>
                </span>
            </div>
        @endforeach

        <div class="core" data-core>
            <div class="core-head">
                <img class="core-icon" src="{{ asset('img/icon-48.png') }}" alt="" width="48" height="48">
                <b>aven</b>
                <span class="state"><i></i>{{ __('site.canvas.online') }}</span>
                <a class="core-ver" href="{{ $repo }}/releases/latest">{{ $release }}</a>
            </div>
            <h1>{{ __('site.hero.title') }}</h1>
            <p class="lead">{{ __('site.hero.lead') }}</p>
            <div class="cmd">
                <span class="ps" aria-hidden="true">$</span>
                <code>{{ $install }}</code>
                <button type="button" data-copy="{{ $install }}" data-done="{{ __('site.hero.copied') }}">{{ __('site.hero.copy') }}</button>
            </div>
            <div class="actions">
                <a class="btn btn-accent" href="#install">{{ __('site.hero.primary') }}</a>
                <a class="btn" href="{{ $repo }}">{{ __('site.hero.secondary') }}</a>
            </div>
        </div>
        <p class="drag-hint" aria-hidden="true">{{ __('site.canvas.hint') }}</p>
    </section>

    {{-- Features --}}
    <section id="features" class="features wrap">
        @foreach (__('site.features') as $f)
            <div class="feature" data-reveal>
                <div class="feature-copy">
                    <h2>{{ $f['title'] }}</h2>
                    <p>{{ $f['text'] }}</p>
                    <ul>
                        @foreach ($f['points'] as $point)
                            <li>{{ $point }}</li>
                        @endforeach
                    </ul>
                </div>
                <div class="viz viz-{{ $f['key'] }}" aria-hidden="true">
                    @switch($f['key'])
                        @case('deploy')
                            <div class="v-commit"><span class="v-avatar"></span><span><b>{{ __('site.canvas.commit') }}</b><small>main 3f9c2e1</small></span></div>
                            <ol class="v-steps">
                                <li style="--d: 0">Initializing</li>
                                <li style="--d: 1">Building <small>Dockerfile</small></li>
                                <li style="--d: 2">Deploying <small>replica 1/2</small></li>
                                <li style="--d: 3">Health check <small>GET / → 200</small></li>
                            </ol>
                            <div class="v-bar"><span></span></div>
                            <div class="v-live"><i></i>Active <small>api.example.com</small></div>
                            @break
                        @case('data')
                            @foreach ([['PG', 'postgres', 'PostgreSQL 17', 'pg'], ['MY', 'mysql', 'MySQL 8.4', 'my'], ['RD', 'redis', 'Redis 7', 'rd'], ['MG', 'mongo', 'MongoDB 8', 'mg']] as $j => [$ico, $name, $engine, $cls])
                                <div class="v-db" style="--d: {{ $j }}"><span class="v-dbico {{ $cls }}">{{ $ico }}</span><b>{{ $name }}</b><small>{{ $engine }}</small><i></i></div>
                            @endforeach
                            <div class="v-wal"><span>WAL → s3://backups</span><div class="v-ticks">@for ($t = 0; $t < 24; $t++)<i style="--t: {{ $t }}"></i>@endfor</div></div>
                            @break
                        @case('network')
                            <div class="v-domain"><span class="v-lock"></span><span class="v-type">shop.example.com</span></div>
                            <ul class="v-checks">
                                <li style="--d: 0"><b>DNS</b><small>AAAA 2001:db8:1943::15</small></li>
                                <li style="--d: 1"><b>Certificate</b><small>Let's Encrypt</small></li>
                                <li style="--d: 2"><b>IPv6</b><small>dedicated address</small></li>
                                <li style="--d: 3"><b>Private</b><small>api.production.aven.internal</small></li>
                            </ul>
                            @break
                        @case('servers')
                            <svg class="v-mesh" viewBox="0 0 300 180" preserveAspectRatio="none">
                                <path d="M75 45 L225 45 M75 45 L150 135 M225 45 L150 135"/>
                            </svg>
                            @foreach ([['eu-1', 25, 25], ['us-1', 75, 25], ['ap-1', 50, 75]] as [$name, $x, $y])
                                <div class="v-server" style="left: {{ $x }}%; top: {{ $y }}%;"><b>{{ $name }}</b><small>aven-agent</small></div>
                            @endforeach
                            <span class="v-move">api + volume</span>
                            @break
                    @endswitch
                </div>
            </div>
        @endforeach

        <div class="also" data-reveal>
            <h3>{{ __('site.also.title') }}</h3>
            <ul>
                @foreach (__('site.also.items') as $item)
                    <li>{{ $item }}</li>
                @endforeach
            </ul>
        </div>
    </section>

    {{-- Comparison --}}
    <section id="compare" class="compare wrap" data-reveal>
        <h2>{{ __('site.compare.title') }}</h2>
        <p class="sub">{{ __('site.compare.lead') }}</p>
        <div class="table-scroll">
            <table>
                <thead>
                    <tr>
                        <th scope="col"><span class="sr">Feature</span></th>
                        @foreach ($compare['products'] as $p)
                            <th scope="col" class="{{ $loop->first ? 'us' : '' }}">{{ $p }}</th>
                        @endforeach
                    </tr>
                </thead>
                <tbody>
                    @foreach ($compare['rows'] as $r => $row)
                        <tr style="--r: {{ $r }}">
                            <th scope="row">{{ $row['label'][$ko ? 1 : 0] }}</th>
                            @foreach ($row['cells'] as $c => $cell)
                                <td class="{{ $c === 0 ? 'us' : '' }}">
                                    <span class="mark m-{{ $cell[0] }}" title="{{ __('site.compare.legend.'.$cell[0]) }}">{{ $marks[$cell[0]] }}<span class="sr">{{ __('site.compare.legend.'.$cell[0]) }}</span></span>
                                    <a href="{{ $cell[3] }}" rel="nofollow">{{ $cell[$col] }}</a>
                                </td>
                            @endforeach
                        </tr>
                    @endforeach
                </tbody>
            </table>
        </div>
        <p class="legend">
            @foreach (__('site.compare.legend') as $k => $label)
                <span><span class="mark m-{{ $k }}">{{ $marks[$k] }}</span>{{ $label }}</span>
            @endforeach
        </p>
        <p class="note">{!! __('site.compare.note', ['date' => $compare['checked'], 'readme' => e($compare['readme'])]) !!}</p>
    </section>

    {{-- Screenshots --}}
    <section class="shots wrap" data-reveal>
        <h2>{{ __('site.shots.title') }}</h2>
        <div class="tabs" role="tablist" aria-label="{{ __('site.shots.title') }}">
            @foreach (__('site.shots.items') as $i => $shot)
                <button type="button" role="tab" id="tab-{{ $shot['img'] }}" aria-controls="panel-{{ $shot['img'] }}" aria-selected="{{ $i === 0 ? 'true' : 'false' }}" tabindex="{{ $i === 0 ? 0 : -1 }}">{{ $shot['name'] }}</button>
            @endforeach
        </div>
        <div class="frame">
            @foreach (__('site.shots.items') as $i => $shot)
                <div role="tabpanel" id="panel-{{ $shot['img'] }}" aria-labelledby="tab-{{ $shot['img'] }}" @if ($i !== 0) hidden @endif>
                    <img src="{{ asset('img/'.$shot['img'].'-'.$locale.'.png') }}" alt="{{ $shot['alt'] }}" width="1440" height="860" loading="lazy">
                </div>
            @endforeach
        </div>
    </section>

    {{-- Install --}}
    <section id="install" class="install wrap" data-reveal>
        <h2>{{ __('site.install.title') }}</h2>
        <p class="sub">{{ __('site.install.lead') }}</p>
        <ol class="steps">
            @foreach (__('site.install.steps') as $step)
                <li>
                    <h3>{{ $step['name'] }}</h3>
                    <p>{{ $step['text'] }}</p>
                </li>
            @endforeach
        </ol>
        <div class="cmd cmd-lg">
            <span class="ps" aria-hidden="true">$</span>
            <code>{{ $install }}</code>
            <button type="button" data-copy="{{ $install }}" data-done="{{ __('site.hero.copied') }}">{{ __('site.hero.copy') }}</button>
        </div>
        <p class="sub small">{{ __('site.install.transcript') }}</p>
        <pre class="term" data-term><code>@foreach (config('site.transcript') as [$kind, $line])<span class="t-{{ $kind }}">@if ($kind === 'cmd')<span class="t-ps">~/aven-site $ </span>@endif{{ $line }}
</span>@endforeach</code></pre>
        <p class="sub small"><a href="{{ $docs }}">{{ __('site.install.guide') }}</a></p>
    </section>

    {{-- FAQ --}}
    <section class="faq wrap" data-reveal>
        <h2>{{ __('site.faq.title') }}</h2>
        <div>
            @foreach (__('site.faq.items') as $item)
                <details>
                    <summary>{{ $item['q'] }}</summary>
                    <p>{{ $item['a'] }}</p>
                </details>
            @endforeach
        </div>
    </section>

    <section class="cta" data-reveal>
        <div class="wrap">
            <img class="cta-mark" src="{{ asset('img/mark-dark.webp') }}" alt="" width="157" height="112" loading="lazy">
            <h2>{{ __('site.cta.title') }}</h2>
            <p>{{ __('site.cta.text') }}</p>
            <div class="cmd">
                <span class="ps" aria-hidden="true">$</span>
                <code>{{ $install }}</code>
                <button type="button" data-copy="{{ $install }}" data-done="{{ __('site.hero.copied') }}">{{ __('site.hero.copy') }}</button>
            </div>
        </div>
    </section>
</main>

<footer class="foot">
    <div class="wrap foot-in">
        <div>
            <img class="foot-logo" src="{{ asset('img/logo-dark.webp') }}" alt="Aven" width="189" height="44" loading="lazy">
            <p>{{ __('site.footer.line') }}</p>
        </div>
        <nav aria-label="Footer">
            <a href="{{ $repo }}">{{ __('site.footer.source') }}</a>
            <a href="{{ $repo }}/releases">{{ __('site.footer.releases') }}</a>
            <a href="{{ $repo }}/blob/main/SECURITY.md">{{ __('site.footer.security') }}</a>
        </nav>
    </div>
</footer>

<script src="{{ asset('js/site.js') }}?v={{ filemtime(public_path('js/site.js')) }}" defer></script>
</body>
</html>
