{!! '<'.'?xml version="1.0" encoding="UTF-8"?'.'>' !!}
{!! '<'.'?xml-stylesheet type="text/xsl" href="/sitemap.xsl"?'.'>' !!}
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">
@foreach ($pages as $page)
    <url>
        <loc>{{ $page }}</loc>
        <lastmod>{{ $lastmod }}</lastmod>
        <xhtml:link rel="alternate" hreflang="en" href="{{ route('home') }}"/>
        <xhtml:link rel="alternate" hreflang="ko" href="{{ route('home.ko') }}"/>
        <xhtml:link rel="alternate" hreflang="x-default" href="{{ route('home') }}"/>
    </url>
@endforeach
</urlset>
