import { siDocker, siGhost, siGithub, siMinio, siMongodb, siMysql, siN8n, siPlausibleanalytics, siPostgresql, siRedis, siUmami, siUptimekuma } from "simple-icons";
import { Archive, Box, HardDrive, SquareFunction } from "lucide-react";
import type { Service } from "@/lib/api";
import { cx } from "./ui";

type SI = { path: string; hex: string; title: string };

const ICONS: Record<string, SI> = {
  postgres: siPostgresql,
  postgresql: siPostgresql,
  pgbouncer: siPostgresql,
  mysql: siMysql,
  redis: siRedis,
  mongo: siMongodb,
  mongodb: siMongodb,
  github: siGithub,
  docker: siDocker,
  minio: siMinio,
  n8n: siN8n,
  umami: siUmami,
  uptimekuma: siUptimekuma,
  "uptime-kuma": siUptimekuma,
  plausible: siPlausibleanalytics,
  ghost: siGhost,
};

/** Near-black brand colors (GitHub, Ghost, ...) follow the theme foreground so they stay visible on dark and light surfaces. */
function visible(hex: string): string {
  const n = parseInt(hex, 16);
  const [r, g, b] = [(n >> 16) & 255, (n >> 8) & 255, n & 255];
  const lum = (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
  return lum < 0.3 ? "var(--foreground)" : `#${hex}`;
}

export function iconKey(s: Pick<Service, "icon" | "database_kind" | "source_type">): string {
  return s.icon ?? s.database_kind ?? (s.source_type === "repo" ? "github" : s.source_type === "function" ? "function" : "docker");
}

export function BrandIcon({ name, className, mono }: { name: string; className?: string; mono?: boolean }) {
  const si = ICONS[name];
  if (name === "volume") return <HardDrive aria-hidden="true" className={cx("size-5 text-gray-600", className)} />;
  if (name === "bucket") return <Archive aria-hidden="true" className={cx("size-5 text-yellow-500", className)} />;
  if (name === "function") return <SquareFunction aria-hidden="true" className={cx("size-5 text-pink-500", className)} />;
  if (!si) return <Box aria-hidden="true" className={cx("size-5 text-gray-600", className)} />;
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" className={cx("size-5 shrink-0", className)} fill={mono ? "currentColor" : visible(si.hex)}>
      <path d={si.path} />
    </svg>
  );
}

export function ServiceIcon({ service, className }: { service: Pick<Service, "icon" | "database_kind" | "source_type">; className?: string }) {
  return <BrandIcon name={iconKey(service)} className={className} />;
}
