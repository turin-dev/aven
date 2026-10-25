import type { NextConfig } from "next";

const isDev = process.env.NODE_ENV === "development";
const devApiUrl = (process.env.AVEN_DEV_API_URL || process.env.KEEL_DEV_API_URL || "http://127.0.0.1:7070").replace(/\/+$/, "");

const nextConfig: NextConfig = {
  trailingSlash: true,
  images: { unoptimized: true },
  ...(isDev
    ? {
        // gzip would buffer proxied text/event-stream responses (logs, events) until they end.
        compress: false,
        // Otherwise trailingSlash would 308 /api/foo -> /api/foo/ before proxying.
        skipTrailingSlashRedirect: true,
        async rewrites() {
          return [
            {
              source: "/api/:path*",
              destination: `${devApiUrl}/api/:path*`,
            },
          ];
        },
      }
    : { output: "export" as const }),
};

export default nextConfig;
