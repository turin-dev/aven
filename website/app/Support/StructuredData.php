<?php

namespace App\Support;

/**
 * schema.org JSON-LD for search engines and AI answer engines.
 */
class StructuredData
{
    public static function home(string $locale, string $repo, string $release, string $install): string
    {
        $home = $locale === 'ko' ? route('home.ko') : route('home');

        $graph = [
            [
                '@type' => 'SoftwareApplication',
                '@id' => route('home').'#software',
                'name' => 'Aven',
                'description' => __('site.description'),
                'url' => $home,
                'image' => asset('img/icon-512.png'),
                'applicationCategory' => 'DeveloperApplication',
                'applicationSubCategory' => 'Self-hosted PaaS',
                'operatingSystem' => 'Linux (amd64, arm64)',
                'softwareVersion' => ltrim($release, 'v'),
                'license' => 'https://www.gnu.org/licenses/agpl-3.0.html',
                'isAccessibleForFree' => true,
                'offers' => ['@type' => 'Offer', 'price' => '0', 'priceCurrency' => 'USD'],
                'installUrl' => 'https://'.config('site.install_host'),
                'downloadUrl' => $repo.'/releases/latest',
                'releaseNotes' => $repo.'/releases',
                'softwareHelp' => $repo.'/blob/main/docs/self-hosting.md',
                'codeRepository' => $repo,
                'sameAs' => [$repo],
                'featureList' => array_map(fn ($f) => $f['title'].': '.$f['text'], __('site.features')),
                'inLanguage' => ['en', 'ko'],
            ],
            [
                '@type' => 'WebSite',
                '@id' => route('home').'#website',
                'name' => 'Aven',
                'url' => route('home'),
                'inLanguage' => $locale,
                'about' => ['@id' => route('home').'#software'],
            ],
            [
                '@type' => 'FAQPage',
                'inLanguage' => $locale,
                'mainEntity' => array_map(fn ($item) => [
                    '@type' => 'Question',
                    'name' => $item['q'],
                    'acceptedAnswer' => ['@type' => 'Answer', 'text' => $item['a']],
                ], __('site.faq.items')),
            ],
        ];

        return json_encode(['@context' => 'https://schema.org', '@graph' => $graph],
            JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_HEX_TAG | JSON_THROW_ON_ERROR);
    }
}
