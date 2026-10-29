<?php

namespace App\Support;

/**
 * Discord component embeds (link previews rendered as Components V2).
 * Spec: https://docs.discord.com/developers/link-previews/component-embeds
 * The payload must stay under 3,000 bytes; Discord rejects invalid payloads silently.
 */
class DiscordEmbed
{
    private const ACCENT = 0x107DFC; // logo blue

    public static function home(string $locale, string $repo, string $release, string $install): array
    {
        $ko = $locale === 'ko';
        $docs = $repo.'/blob/main/docs/self-hosting'.($ko ? '.ko' : '').'.md';

        return self::container([
            ['type' => 9,
                'components' => [self::text("## Aven\n**".__('site.hero.title')."**\n".__('site.hero.lead'))],
                'accessory' => ['type' => 11, 'media' => ['url' => asset('img/icon-512.png')]],
            ],
            ['type' => 12, 'items' => [[
                'media' => ['url' => asset('img/demo-'.$locale.'.mp4')],
                'description' => __('site.shots.items')[0]['alt'],
            ]]],
            self::text("```sh\n{$install}\n```"),
            ['type' => 14, 'divider' => true, 'spacing' => 1],
            self::buttons([
                [$ko ? '웹사이트' : 'Website', $ko ? route('home.ko') : route('home')],
                ['GitHub', $repo],
                [$ko ? '셀프호스팅 가이드' : 'Self-hosting guide', $docs],
                [$release, $repo.'/releases/latest'],
            ]),
        ]);
    }

    public static function install(string $repo, string $install, string $source): array
    {
        return self::container([
            self::text("## Install Aven\n```sh\n{$install}\n```\nRun it on a Linux server (amd64 or arm64) with Docker, ports 80 and 443 free. The script asks for the dashboard URL, the base domain for services, an email for Let's Encrypt and the admin password, checks the release checksum, then installs `aven-server`, the CLI and Railpack."),
            ['type' => 14, 'divider' => true, 'spacing' => 1],
            self::buttons([
                ['Read the script', $source],
                ['Self-hosting guide', $repo.'/blob/main/docs/self-hosting.md'],
                ['aven.turin.my', config('app.url')],
            ]),
        ]);
    }

    /** JSON for an inline <script id="discord:component-embed"> element. */
    public static function json(array $payload): string
    {
        return json_encode($payload, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_HEX_TAG | JSON_THROW_ON_ERROR);
    }

    private static function container(array $components): array
    {
        return ['component' => ['type' => 17, 'accent_color' => self::ACCENT, 'components' => $components]];
    }

    private static function text(string $content): array
    {
        return ['type' => 10, 'content' => $content];
    }

    /** An Action Row of link buttons (style 5 is the only style Discord allows here). */
    private static function buttons(array $links): array
    {
        return ['type' => 1, 'components' => array_map(
            fn (array $link) => ['type' => 2, 'style' => 5, 'label' => $link[0], 'url' => $link[1]],
            $links,
        )];
    }
}
