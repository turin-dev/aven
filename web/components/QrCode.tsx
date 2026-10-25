"use client";

import { useMemo } from "react";
import { encodeQr, qrPath } from "@/lib/qr";

/** QR code drawn locally as SVG (dark modules on white with a 4-module quiet zone). Renders nothing if the text is too long. */
export function QrCode({ value, alt, size = 184, className }: { value: string; alt: string; size?: number; className?: string }) {
  const qr = useMemo(() => {
    try {
      return encodeQr(value);
    } catch {
      return null;
    }
  }, [value]);
  if (!qr) return null;
  const dim = qr.size + 8;
  return (
    <svg
      role="img"
      aria-label={alt}
      viewBox={`0 0 ${dim} ${dim}`}
      width={size}
      height={size}
      shapeRendering="crispEdges"
      className={className}
    >
      <title>{alt}</title>
      <rect width={dim} height={dim} fill="#fff" />
      <path d={qrPath(qr.modules)} fill="#000" />
    </svg>
  );
}
