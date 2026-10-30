<?php

namespace App\Http\Controllers;

use App\Support\DiscordEmbed;
use App\Support\StructuredData;
use Illuminate\Support\Facades\App;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Http;
use Illuminate\Http\Response;
use Illuminate\View\View;

class SiteController extends Controller
{
    public const REPO = 'https://github.com/turin-dev/aven';

    public function home(string $locale = 'en'): View
    {
        App::setLocale($locale);

        $release = $this->latestRelease();
        $install = 'curl -fsSL https://'.config('site.install_host').' | sh';

        return view('home', [
            'locale' => $locale,
            'repo' => self::REPO,
            'release' => $release,
            'install' => $install,
            'discord' => DiscordEmbed::json(DiscordEmbed::home($locale, self::REPO, $release, $install)),
            'jsonld' => StructuredData::home($locale, self::REPO, $release, $install),
        ]);
    }

    /** /llms.txt and /llms-full.txt: the site as Markdown for AI crawlers (llmstxt.org). */
    public function llms(bool $full = false): Response
    {
        App::setLocale('en');

        return response()
            ->view($full ? 'llms-full' : 'llms', [
                'repo' => self::REPO,
                'release' => $this->latestRelease(),
                'install' => 'curl -fsSL https://'.config('site.install_host').' | sh',
                'compare' => config('compare'),
                'ko' => fn (string $key) => trans($key, [], 'ko'),
            ])
            ->header('Content-Type', 'text/markdown; charset=utf-8');
    }

    public function sitemap(): Response
    {
        $pages = [route('home'), route('home.ko')];
        // Content changes with each deploy, so the page template's mtime is a fair lastmod.
        $lastmod = date('Y-m-d', filemtime(resource_path('views/home.blade.php')));

        return response()
            ->view('sitemap', compact('pages', 'lastmod'))
            ->header('Content-Type', 'application/xml; charset=utf-8');
    }

    private function latestRelease(): string
    {
        return Cache::remember('aven.release', now()->addHours(6), function () {
            try {
                $tag = Http::timeout(3)
                    ->acceptJson()
                    ->get('https://api.github.com/repos/turin-dev/aven/releases/latest')
                    ->json('tag_name');
            } catch (\Throwable) {
                $tag = null;
            }

            return is_string($tag) && $tag !== '' ? $tag : config('site.fallback_release');
        });
    }
}
