FROM composer:2 AS vendor
WORKDIR /app
COPY composer.json composer.lock ./
RUN composer install --no-dev --no-scripts --no-autoloader --prefer-dist --no-interaction
COPY . .
RUN composer dump-autoload --optimize --classmap-authoritative --no-dev

FROM dunglas/frankenphp:1-php8.4-alpine
WORKDIR /app
ENV SERVER_NAME=":8080" \
    APP_ENV=production \
    APP_DEBUG=false \
    LOG_CHANNEL=stderr \
    SESSION_DRIVER=array \
    CACHE_STORE=file \
    DB_CONNECTION=sqlite
COPY --from=vendor /app /app
COPY docker/Caddyfile /etc/frankenphp/Caddyfile
RUN rm -f .env \
 && mkdir -p storage/framework/cache/data storage/framework/views storage/logs bootstrap/cache /data/caddy /config/caddy \
 && chown -R www-data:www-data storage bootstrap/cache /data /config
USER www-data
EXPOSE 8080
CMD ["sh", "-c", "php artisan config:cache && php artisan route:cache && php artisan view:cache && exec frankenphp run --config /etc/frankenphp/Caddyfile"]
