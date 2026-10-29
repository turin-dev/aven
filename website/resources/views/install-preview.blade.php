<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <title>Install Aven</title>
    <meta name="description" content="{{ $install }}">
    <meta property="og:type" content="website">
    <meta property="og:site_name" content="Aven">
    <meta property="og:url" content="https://{{ config('site.install_host') }}/">
    <meta property="og:title" content="Install Aven">
    <meta property="og:description" content="{{ $install }}">
    <meta property="og:image" content="{{ rtrim(config('app.url'), '/') }}/img/og-install.png">
    <meta property="og:image:width" content="1200">
    <meta property="og:image:height" content="630">
    <meta name="twitter:card" content="summary_large_image">
    <meta name="theme-color" content="#000000">
    <script id="discord:component-embed" type="application/json">{!! $discord !!}</script>
</head>
<body>
<pre>{{ $install }}</pre>
</body>
</html>
