<?php

use Illuminate\Support\Facades\Route;
use App\Http\Controllers\InstallController;
use App\Http\Controllers\SiteController;
use Illuminate\Cookie\Middleware\AddQueuedCookiesToResponse;
use Illuminate\Cookie\Middleware\EncryptCookies;
use Illuminate\Foundation\Http\Middleware\PreventRequestForgery;
use Illuminate\Session\Middleware\StartSession;
use Illuminate\View\Middleware\ShareErrorsFromSession;

// The site is static content: no sessions or cookies, so responses stay cacheable.
Route::withoutMiddleware([
    EncryptCookies::class,
    AddQueuedCookiesToResponse::class,
    StartSession::class,
    ShareErrorsFromSession::class,
    PreventRequestForgery::class,
])->group(function () {
    // install.aven.turin.my: the installer script itself, for `curl … | sh`.
    Route::domain(config('site.install_host'))->group(function () {
        Route::get('/', InstallController::class)->name('install');
        Route::get('/install-server.sh', InstallController::class);
    });

    Route::get('/', [SiteController::class, 'home'])->name('home');
    Route::get('/ko', fn (SiteController $site) => $site->home('ko'))->name('home.ko');
    Route::get('/sitemap.xml', [SiteController::class, 'sitemap'])->name('sitemap');
    Route::get('/llms.txt', fn (SiteController $site) => $site->llms())->name('llms');
    Route::get('/llms-full.txt', fn (SiteController $site) => $site->llms(true))->name('llms.full');
});
