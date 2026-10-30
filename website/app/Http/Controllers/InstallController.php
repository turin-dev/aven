<?php

namespace App\Http\Controllers;

use App\Support\DiscordEmbed;
use Illuminate\Http\Request;
use Illuminate\Http\Response;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Http;

/**
 * Serves install-server.sh from the aven repository's main branch, so
 * `curl -fsSL https://install.aven.turin.my | sh` always runs the current installer.
 */
class InstallController extends Controller
{
    private const SOURCE = 'https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh';

    // Link preview crawlers get an HTML card instead of the script.
    private const CRAWLERS = '/Discordbot|Slackbot|Twitterbot|facebookexternalhit|TelegramBot|LinkedInBot|WhatsApp|kakaotalk-scrap|Embedly|redditbot/i';

    public function __invoke(Request $request): Response
    {
        if (preg_match(self::CRAWLERS, (string) $request->userAgent())) {
            return $this->preview();
        }

        $script = Cache::get('install.fresh') ?? $this->fetch();

        if ($script === null) {
            return response("install-server.sh is unavailable right now. Get it from ".self::SOURCE."\n", 503)
                ->header('Content-Type', 'text/plain; charset=utf-8')
                ->header('Retry-After', '60');
        }

        return response($script)
            ->header('Content-Type', 'text/plain; charset=utf-8')
            ->header('Cache-Control', 'public, max-age=300')
            ->header('X-Source', self::SOURCE)
            ->header('Vary', 'User-Agent');
    }

    private function preview(): Response
    {
        $install = 'curl -fsSL https://'.config('site.install_host').' | sh';

        return response()->view('install-preview', [
            'install' => $install,
            'discord' => DiscordEmbed::json(DiscordEmbed::install(SiteController::REPO, $install, self::SOURCE)),
        ])->header('Vary', 'User-Agent');
    }

    /** Fetch the script; fall back to the last good copy when GitHub fails. */
    private function fetch(): ?string
    {
        try {
            $res = Http::timeout(5)->get(self::SOURCE);
            $body = $res->successful() ? $res->body() : null;
        } catch (\Throwable) {
            $body = null;
        }

        if ($body !== null && str_starts_with($body, '#!')) {
            Cache::put('install.fresh', $body, now()->addMinutes(5));
            Cache::forever('install.last', $body);

            return $body;
        }

        return Cache::get('install.last');
    }
}
