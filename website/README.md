# Aven website

The website for Aven, a self-hosted PaaS. It runs at [aven.turin.my](https://aven.turin.my) and also serves the installer at `install.aven.turin.my`.

It is a small Laravel 13 app with no database, sessions or build step: Blade views, plain CSS and JS in `public/`, and self-hosted fonts.

## Layout

| Path | What it holds |
| --- | --- |
| `lang/en/site.php`, `lang/ko/site.php` | All page copy, English (`/`) and Korean (`/ko`) |
| `config/compare.php` | The Aven vs Railway / Coolify / Dokploy table, with a source link per cell |
| `config/site.php` | Install host, fallback release and the `aven up` transcript shown on the page |
| `resources/views/home.blade.php` | The landing page |
| `app/Http/Controllers/InstallController.php` | `install.aven.turin.my`: serves `install-server.sh` from Aven's `main` (cached for 5 minutes, last good copy kept as a fallback) and an HTML preview for link crawlers |
| `app/Support/DiscordEmbed.php`, `app/Support/StructuredData.php` | Discord link embeds and JSON-LD |
| `resources/views/llms*.blade.php`, `sitemap.blade.php` | `/llms.txt`, `/llms-full.txt`, `/sitemap.xml` |
| `public/` | CSS, JS, fonts, images and the demo videos |
| `Dockerfile`, `docker/Caddyfile` | FrankenPHP image listening on `:8080` |

## Run it locally

With PHP 8.3+ and Composer:

```sh
composer install
cp .env.example .env
php artisan key:generate
php artisan serve
```

Without PHP on the host, use Docker:

```sh
docker build -t aven-site .
docker run --rm -p 8080:8080 -e APP_KEY=base64:$(openssl rand -base64 32) aven-site
```

Run these from `website/`. Then open http://localhost:8080 (or http://localhost:8080/ko).

## Deploy

The production site runs on Aven itself and is deployed with `aven up` from this directory. It needs one variable, `APP_KEY`; everything else has defaults in the `Dockerfile`.

## Contributing

Corrections are welcome, especially to the comparison table: every cell links to its source, so please include a link when you change one. Use the same issues and pull requests as the rest of the repository; see [CONTRIBUTING.md](../CONTRIBUTING.md).

## License

[AGPL-3.0-only](../LICENSE), like the rest of Aven. Fonts and other bundled third-party files are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
