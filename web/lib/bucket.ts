/* Bucket (S3-compatible object storage) browser API (v1.5). See docs/api-v1.md "### Buckets". */

import { ApiError, type ID } from "./api";

export type BucketInfo = {
  bucket: string;
  region: string;
  /** private endpoint: http://<service>.<env>.aven.internal:9000 */
  endpoint: string;
  public_endpoint: string | null;
  /** "•••" for viewers (masked = true) */
  access_key_id: string;
  secret_access_key: string;
  masked: boolean;
  running: boolean;
  max_upload_bytes: number;
};

export type BucketObject = { key: string; size: number; last_modified: string; etag: string };

export type BucketPage = {
  bucket: string;
  prefix: string;
  /** "folders" directly under prefix (each ends with "/") */
  prefixes: string[];
  objects: BucketObject[];
  next_cursor: string | null;
};

const enc = encodeURIComponent;
const base = (sid: ID, eid: ID) => `/api/services/${enc(sid)}/instances/${enc(eid)}/bucket`;
/** Encodes an object key segment by segment ('/' stays a path separator). */
const keyPath = (key: string) => key.split("/").map(enc).join("/");

async function call<T>(method: string, url: string, body?: BodyInit): Promise<T> {
  const res = await fetch(url, { method, credentials: "include", body });
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    let data: unknown;
    try {
      const j = (await res.json()) as { error?: string };
      data = j;
      if (j?.error) msg = j.error;
    } catch {
      /* ignore */
    }
    if (res.status === 401 && typeof window !== "undefined" && !window.location.pathname.startsWith("/login")) {
      window.location.href = `/login/?next=${enc(window.location.pathname + window.location.search)}`;
    }
    throw new ApiError(res.status, msg, data);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

export const bucketApi = {
  info: (sid: ID, eid: ID) => call<BucketInfo>("GET", base(sid, eid)),
  list: (sid: ID, eid: ID, prefix = "", cursor?: string | null, limit = 200) => {
    const q = new URLSearchParams({ prefix, limit: String(limit) });
    if (cursor) q.set("cursor", cursor);
    return call<BucketPage>("GET", `${base(sid, eid)}/objects?${q}`);
  },
  /** Multipart upload: every file goes to prefix + file name (max 100 MiB per request). */
  upload: (sid: ID, eid: ID, prefix: string, files: File[]) => {
    const fd = new FormData();
    fd.append("prefix", prefix);
    for (const f of files) fd.append("file", f, f.name);
    return call<{ uploaded: { key: string; size: number }[] }>("PUT", `${base(sid, eid)}/objects`, fd);
  },
  remove: (sid: ID, eid: ID, key: string) => call<void>("DELETE", `${base(sid, eid)}/objects/${keyPath(key)}`),
  /** Same-origin download link (served as an attachment; member+). */
  downloadUrl: (sid: ID, eid: ID, key: string) => `${base(sid, eid)}/objects/${keyPath(key)}`,
};
