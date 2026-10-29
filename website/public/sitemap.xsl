<?xml version="1.0" encoding="UTF-8"?>
<!-- Renders sitemap.xml as a readable page in browsers. Search engines read the XML and ignore this. -->
<xsl:stylesheet version="1.0"
    xmlns:xsl="http://www.w3.org/1999/XSL/Transform"
    xmlns:s="http://www.sitemaps.org/schemas/sitemap/0.9"
    xmlns:xhtml="http://www.w3.org/1999/xhtml">
  <xsl:output method="html" encoding="UTF-8" indent="yes"/>
  <xsl:template match="/">
    <html lang="en">
      <head>
        <meta charset="utf-8"/>
        <meta name="viewport" content="width=device-width, initial-scale=1"/>
        <title>Sitemap · Aven</title>
        <style>
          body { margin: 0; padding: 3rem 1.5rem; font: 15px/1.6 system-ui, -apple-system, "Pretendard", sans-serif; color: #1d1d1f; background: #fff; }
          main { max-width: 56rem; margin: 0 auto; }
          img { height: 26px; }
          h1 { margin: 2rem 0 0.25rem; font-size: 1.75rem; letter-spacing: -0.03em; }
          p { margin: 0 0 2rem; color: #6e6e73; }
          table { width: 100%; border-collapse: collapse; border: 1px solid #e8e8ed; border-radius: 12px; }
          th, td { padding: 0.8rem 1rem; text-align: left; border-bottom: 1px solid #e8e8ed; vertical-align: top; }
          th { font-size: 0.8125rem; color: #6e6e73; font-weight: 600; background: #fafafa; }
          a { color: #000; }
          code { font: 0.8125rem ui-monospace, monospace; color: #6e6e73; }
        </style>
      </head>
      <body>
        <main>
          <a href="/"><img src="/img/logo.png" alt="Aven"/></a>
          <h1>Sitemap</h1>
          <p><xsl:value-of select="count(s:urlset/s:url)"/> pages. This is the XML sitemap read by search engines, shown here as a table.</p>
          <table>
            <tr><th>Page</th><th>Languages</th><th>Last modified</th></tr>
            <xsl:for-each select="s:urlset/s:url">
              <tr>
                <td><a href="{s:loc}"><xsl:value-of select="s:loc"/></a></td>
                <td>
                  <xsl:for-each select="xhtml:link">
                    <code><xsl:value-of select="@hreflang"/></code><xsl:if test="position() != last()">, </xsl:if>
                  </xsl:for-each>
                </td>
                <td><xsl:value-of select="s:lastmod"/></td>
              </tr>
            </xsl:for-each>
          </table>
        </main>
      </body>
    </html>
  </xsl:template>
</xsl:stylesheet>
