<?php

return [
    'install_host' => env('AVEN_INSTALL_HOST', 'install.aven.turin.my'),

    'fallback_release' => env('AVEN_FALLBACK_RELEASE', 'v0.1.2'),

    // Trimmed output of the `aven up` that deployed this site.
    'transcript' => [
        ['cmd', 'aven up'],
        ['out', 'uploading 9074 files (15391 KiB) to production/website...'],
        ['out', 'deployment dep_fpmcakh2bq5k6'],
        ['phase', '04:07:53 ==> building'],
        ['out', '04:07:53 builder=auto → dockerfile'],
        ['dim', '#14 [stage-1 3/4] COPY --from=vendor /app /app'],
        ['dim', '#16 naming to docker.io/keel/svc-lhfpqo7x46vmo:dep-fpmcakh2bq5k6 done'],
        ['phase', '04:08:01 ==> deploying'],
        ['phase', '04:08:01 ==> healthchecking'],
        ['out', '04:08:03 healthcheck OK: GET http://[fd8b:ca60:ccbd:c::4]:8080/ → 200'],
        ['ok', 'status: live'],
    ],
];
