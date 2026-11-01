"use client";

import { toast } from "@/lib/uiState";
import { useRef, useState, type DragEvent } from "react";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Download, Eye, File as FileIcon, Folder, FolderPlus, Image as ImageIcon, Lock, RefreshCw, Search, Trash2, Upload } from "lucide-react";
import type { CanvasService, Environment } from "@/lib/api";
import { bucketApi, type BucketObject } from "@/lib/bucket";
import { useCaps } from "@/lib/queries";
import { fmtRelative } from "@/lib/format";
import { dateLocale, t, tr } from "@/lib/i18n";
import { Button, Card, CenterSpinner, CopyButton, CopyField, Dialog, EmptyState, ErrorBox, IconButton, Input, LABEL_CLS, cx, useConfirm } from "../ui";

function Label({ children }: { children: string }) {
  return <div className={LABEL_CLS}>{children}</div>;
}

function fmtBytes(n: number) {
  if (n < 1024) return `${n} B`;
  const u = ["KiB", "MiB", "GiB", "TiB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${u[i]}`;
}

const tail = (key: string, prefix: string) => key.slice(prefix.length) || key;

/** Connection details. Viewers get masked credentials from the server. */
function Credentials({ svc, env }: { svc: CanvasService; env: Environment }) {
  const info = useQuery({ queryKey: ["bucket-info", svc.id, env.id], queryFn: () => bucketApi.info(svc.id, env.id), staleTime: 10_000 });
  const d = info.data;
  return (
    <Card className="!max-w-none" title={t("{name}에 연결", { name: svc.name })} description={t("S3 호환 API입니다. AWS SDK는 path-style 주소를 쓰세요(forcePathStyle).")}>
      <div className="space-y-5">
        {info.error ? (
          <ErrorBox error={info.error} />
        ) : !d ? (
          <CenterSpinner />
        ) : (
          <>
            <div className="grid gap-4 sm:grid-cols-2">
              <div>
                <Label>Bucket</Label>
                <CopyField value={d.bucket} label="Bucket" />
              </div>
              <div>
                <Label>Region</Label>
                <CopyField value={d.region} label="Region" />
              </div>
              <div>
                <Label>{t("Endpoint (비공개 네트워크)")}</Label>
                <CopyField value={d.endpoint} label="Endpoint" />
              </div>
              <div>
                <Label>Public endpoint</Label>
                {d.public_endpoint ? (
                  <CopyField value={d.public_endpoint} label="Public endpoint" />
                ) : (
                  <div className="flex h-[42px] items-center text-sm text-gray-500">{t("공개 도메인이 없습니다(설정에서 추가).")}</div>
                )}
              </div>
              {d.masked ? (
                <div className="flex items-start gap-2 rounded-md bg-gray-50 px-3 py-2.5 text-sm text-gray-600 sm:col-span-2">
                  <Lock className="mt-0.5 size-3.5 shrink-0 text-gray-400" />
                  <span>{t("보기 권한으로는 access key와 secret key를 볼 수 없습니다.")}</span>
                </div>
              ) : (
                <>
                  <div>
                    <Label>Access key ID</Label>
                    <CopyField value={d.access_key_id} secret label="Access key ID" />
                  </div>
                  <div>
                    <Label>Secret access key</Label>
                    <CopyField value={d.secret_access_key} secret label="Secret access key" />
                  </div>
                </>
              )}
            </div>
            <div className="flex items-start gap-2 rounded-md bg-gray-50 px-3 py-2.5 text-sm text-gray-600">
              <Lock className="mt-0.5 size-3.5 shrink-0 text-gray-400" />
              <span>
                {tr("같은 환경의 서비스에서는 {endpoint}, {bucket} 같은 참조를 쓰세요(AWS_* 별칭도 있습니다).", {
                  endpoint: (
                    <span className="font-mono text-pink-600">
                      {"${{"}
                      {svc.name}.ENDPOINT
                      {"}}"}
                    </span>
                  ),
                  bucket: <span className="font-mono text-pink-600">{"${{" + svc.name + ".BUCKET}}"}</span>,
                })}
              </span>
            </div>
          </>
        )}
      </div>
    </Card>
  );
}

function Breadcrumbs({ prefix, onGo }: { prefix: string; onGo: (p: string) => void }) {
  const parts = prefix.split("/").filter(Boolean);
  return (
    <nav aria-label={t("경로")} className="flex min-w-0 flex-wrap items-center gap-1 text-sm">
      <button type="button" className={cx("cursor-pointer rounded px-1 hover:text-foreground", parts.length ? "text-gray-600" : "text-foreground")} onClick={() => onGo("")}>
        /
      </button>
      {parts.map((p, i) => {
        const to = parts.slice(0, i + 1).join("/") + "/";
        const last = i === parts.length - 1;
        return (
          <span key={to} className="flex items-center gap-1">
            <ChevronRight className="size-3.5 text-gray-400" aria-hidden />
            <button
              type="button"
              aria-current={last ? "page" : undefined}
              className={cx("cursor-pointer truncate rounded px-1 font-mono text-sm hover:text-foreground", last ? "text-foreground" : "text-gray-600")}
              onClick={() => onGo(to)}
            >
              {p}
            </button>
          </span>
        );
      })}
    </nav>
  );
}

const IMAGE_EXT = /\.(png|jpe?g|gif|webp|avif|svg|ico|bmp)$/i;
const TEXT_EXT = /\.(txt|md|json|ya?ml|toml|csv|tsv|log|xml|html?|css|js|mjs|ts|tsx|sql|sh|env|ini|conf)$/i;
const previewKind = (key: string): "image" | "text" | null => (IMAGE_EXT.test(key) ? "image" : TEXT_EXT.test(key) ? "text" : null);

function Preview({ svc, env, obj, onClose }: { svc: CanvasService; env: Environment; obj: BucketObject; onClose: () => void }) {
  // Big images are not pulled into the page just to look at them.
  const kind = previewKind(obj.key) === "image" && obj.size > 20 << 20 ? null : previewKind(obj.key);
  const url = bucketApi.downloadUrl(svc.id, env.id, obj.key);
  const text = useQuery({
    queryKey: ["bucket-preview", svc.id, env.id, obj.key, obj.etag],
    queryFn: () => bucketApi.readText(svc.id, env.id, obj.key),
    enabled: kind === "text",
    staleTime: 60_000,
    retry: false,
  });
  return (
    <Dialog
      open
      wide
      onClose={onClose}
      title={obj.key.split("/").pop() || obj.key}
      footer={
        <>
          <a href={url} download className="inline-flex h-[34px] items-center gap-1.5 rounded-md border border-gray-200 px-3 text-sm hover:bg-gray-100">
            <Download className="size-4" /> {t("내려받기")}
          </a>
          <Button variant="outline" onClick={onClose}>
            {t("닫기")}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500">
          <span className="break-all font-mono text-foreground">{obj.key}</span>
          <span className="tabular-nums">{fmtBytes(obj.size)}</span>
          {obj.last_modified && <span>{new Date(obj.last_modified).toLocaleString(dateLocale())}</span>}
          <CopyButton value={obj.key} label={t("키 복사")} className="size-6" />
        </div>
        {kind === "image" ? (
          <div className="flex max-h-[60vh] justify-center overflow-auto rounded-md border border-gray-200 bg-[repeating-conic-gradient(var(--color-gray-100)_0_25%,transparent_0_50%)] bg-[length:16px_16px] p-2">
            {/* eslint-disable-next-line @next/next/no-img-element -- same-origin object, no optimisation wanted */}
            <img src={url} alt={obj.key} className="max-h-[56vh] max-w-full object-contain" />
          </div>
        ) : kind === "text" ? (
          text.error ? (
            <ErrorBox error={text.error} />
          ) : !text.data ? (
            <CenterSpinner />
          ) : (
            <>
              <pre className="max-h-[60vh] overflow-auto whitespace-pre-wrap break-all rounded-md border border-gray-200 bg-gray-50 p-3 font-mono text-xs text-foreground">{text.data.text}</pre>
              {text.data.truncated && <p className="text-xs text-gray-500">{t("앞 256KiB만 보여 줍니다.")}</p>}
            </>
          )
        ) : (
          <p className="text-sm text-gray-500">{t("이 형식은 미리 볼 수 없습니다. 내려받아서 여세요.")}</p>
        )}
      </div>
    </Dialog>
  );
}

type UploadState = { files: number; loaded: number; total: number; abort: AbortController };

function Browser({ svc, env }: { svc: CanvasService; env: Environment }) {
  const qc = useQueryClient();
  const caps = useCaps(svc.project_id);
  const canEdit = caps.canEdit;
  const [prefix, setPrefix] = useState("");
  const [folder, setFolder] = useState<string | null>(null);
  const [upload, setUpload] = useState<UploadState | null>(null);
  const [drag, setDrag] = useState(false);
  const [filter, setFilter] = useState("");
  const [preview, setPreview] = useState<BucketObject | null>(null);
  const [error, setError] = useState<unknown>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const dragDepth = useRef(0);
  const { confirm, node } = useConfirm();
  const running = svc.instance.status === "online" || svc.instance.status === "crashed";
  const key = ["bucket-objects", svc.id, env.id, prefix];
  const info = useQuery({ queryKey: ["bucket-info", svc.id, env.id], queryFn: () => bucketApi.info(svc.id, env.id), staleTime: 10_000 });
  const maxUpload = info.data?.max_upload_bytes || 100 << 20;

  const list = useInfiniteQuery({
    queryKey: key,
    queryFn: ({ pageParam }) => bucketApi.list(svc.id, env.id, prefix, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next_cursor,
    enabled: running,
    retry: false,
    staleTime: 5_000,
  });
  const q = filter.trim().toLowerCase();
  const allPrefixes = list.data?.pages.flatMap((p) => p.prefixes) ?? [];
  const allObjects: BucketObject[] = list.data?.pages.flatMap((p) => p.objects) ?? [];
  const prefixes = q ? allPrefixes.filter((p) => tail(p, prefix).toLowerCase().includes(q)) : allPrefixes;
  const objects = q ? allObjects.filter((o) => tail(o.key, prefix).toLowerCase().includes(q)) : allObjects;
  const totalSize = allObjects.reduce((n, o) => n + o.size, 0);
  const refresh = () => qc.invalidateQueries({ queryKey: ["bucket-objects", svc.id, env.id] });
  const go = (p: string) => {
    setPrefix(p);
    setFilter("");
  };

  const send = async (files: File[]) => {
    const abort = new AbortController();
    const total = files.reduce((n, f) => n + f.size, 0);
    setUpload({ files: files.length, loaded: 0, total, abort });
    try {
      const res = await bucketApi.uploadWithProgress(svc.id, env.id, prefix, files, (loaded, tot) => setUpload((u) => (u ? { ...u, loaded, total: tot } : u)), abort.signal);
      toast(t("{count}개 파일을 올렸습니다", { count: res.uploaded.length }), "success");
      await refresh();
    } catch (e) {
      if (e instanceof DOMException && e.name === "AbortError") toast(t("업로드를 취소했습니다"));
      else setError(e);
    } finally {
      setUpload(null);
      if (fileInput.current) fileInput.current.value = "";
    }
  };

  const startUpload = (files: File[]) => {
    if (!files.length || upload) return;
    setError(null);
    const total = files.reduce((n, f) => n + f.size, 0);
    if (total > maxUpload) {
      setError(new Error(t("한 번에 {max}까지 올릴 수 있습니다. 고른 파일은 {size}입니다.", { max: fmtBytes(maxUpload), size: fmtBytes(total) })));
      if (fileInput.current) fileInput.current.value = "";
      return;
    }
    // Only the pages loaded so far are known; S3 PUT silently replaces an existing key.
    const existing = new Set(allObjects.map((o) => o.key));
    const clash = files.filter((f) => existing.has(prefix + f.name)).map((f) => f.name);
    if (!clash.length) return void send(files);
    confirm({
      title: t("같은 이름의 파일을 덮어쓸까요?"),
      message: (
        <div className="space-y-2">
          <p>{t("이 폴더에 이미 있는 파일 {count}개를 새 파일로 바꿉니다. 되돌릴 수 없습니다.", { count: clash.length })}</p>
          <ul className="max-h-40 list-disc overflow-auto pl-5 font-mono text-xs text-foreground">
            {clash.slice(0, 50).map((n) => (
              <li key={n} className="break-all">
                {n}
              </li>
            ))}
          </ul>
        </div>
      ),
      confirmLabel: t("덮어쓰기"),
      danger: true,
      run: () => send(files),
    });
  };

  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    dragDepth.current = 0;
    setDrag(false);
    if (!canEdit || !running) return;
    const items = Array.from(e.dataTransfer.items ?? []);
    const dirs = items.filter((it) => it.kind === "file" && it.webkitGetAsEntry?.()?.isDirectory).length;
    const files = Array.from(e.dataTransfer.files).filter((_, i) => !items[i]?.webkitGetAsEntry?.()?.isDirectory);
    if (dirs) toast(t("폴더는 끌어 놓아 올릴 수 없습니다. 폴더를 열고 안의 파일을 올리세요."), "error");
    startUpload(files);
  };
  const dropProps = canEdit && running
    ? {
        onDragEnter: (e: DragEvent<HTMLDivElement>) => {
          if (!e.dataTransfer.types.includes("Files")) return;
          e.preventDefault();
          dragDepth.current++;
          setDrag(true);
        },
        onDragOver: (e: DragEvent<HTMLDivElement>) => {
          if (e.dataTransfer.types.includes("Files")) e.preventDefault();
        },
        onDragLeave: () => {
          dragDepth.current = Math.max(0, dragDepth.current - 1);
          if (dragDepth.current === 0) setDrag(false);
        },
        onDrop,
      }
    : {};

  const remove = (o: BucketObject) =>
    confirm({
      title: t("객체 삭제"),
      message: tr("{key}를 지웁니다. 되돌릴 수 없습니다.", {
        key: <span className="break-all font-mono text-foreground">{o.key}</span>,
      }),
      confirmLabel: t("삭제"),
      danger: true,
      run: async () => {
        try {
          await bucketApi.remove(svc.id, env.id, o.key);
          toast(t("{name}을 지웠습니다", { name: tail(o.key, prefix) }));
          await refresh();
        } catch (e) {
          setError(e);
        }
      },
    });

  const folderName = (folder ?? "").trim().replace(/^\/+|\/+$/g, "");
  const folderValid = !!folderName && !folderName.split("/").some((s) => s === "" || s === "." || s === "..") && !folderName.includes("\\");
  const goFolder = () => {
    if (!folderValid) return;
    go(prefix + folderName + "/");
    setFolder(null);
  };

  const pct = upload && upload.total ? Math.round((upload.loaded / upload.total) * 100) : 0;
  return (
    <Card
      className="!max-w-none"
      title={t("객체")}
      description={t("bucket 안의 파일입니다. 폴더는 키의 / 앞부분입니다.")}
      action={
        <>
          <IconButton label={t("새로 고침")} onClick={refresh} disabled={!running}>
            <RefreshCw className={cx(list.isFetching && "animate-spin motion-reduce:animate-none")} />
          </IconButton>
          {canEdit && (
            <>
              <Button variant="outline" onClick={() => setFolder("")} disabled={!running}>
                <FolderPlus /> {t("폴더")}
              </Button>
              <Button variant="primary" onClick={() => fileInput.current?.click()} loading={!!upload} disabled={!running}>
                <Upload /> {t("업로드")}
              </Button>
              <input ref={fileInput} type="file" multiple className="hidden" aria-label={t("올릴 파일")} onChange={(e) => startUpload(Array.from(e.target.files ?? []))} />
            </>
          )}
        </>
      }
    >
      <div {...dropProps} className="relative">
        {drag && (
          <div className="pointer-events-none absolute inset-0 z-20 flex flex-col items-center justify-center gap-2 rounded-lg border-2 border-dashed border-pink-400 bg-primary-surface/90 text-sm text-pink-600" aria-hidden>
            <Upload className="size-6" />
            {tr("{prefix}에 놓아서 올리기", { prefix: <span className="font-mono">/{prefix}</span> })}
          </div>
        )}
        <div className="mb-3 flex flex-wrap items-center gap-3">
          <Breadcrumbs prefix={prefix} onGo={go} />
          {running && (allObjects.length > 0 || allPrefixes.length > 0) && (
            <div className="relative ml-auto w-full max-w-[220px]">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 z-20 size-3.5 -translate-y-1/2 text-gray-400" aria-hidden />
              <Input size="sm" value={filter} onChange={(e) => setFilter(e.target.value)} placeholder={t("이 폴더에서 찾기")} aria-label={t("이 폴더에서 찾기")} className="!pl-8" />
            </div>
          )}
        </div>
        {folder !== null && (
          <form
            className="mb-4 flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              goFolder();
            }}
          >
            <Input
              mono
              autoFocus
              aria-label={t("폴더 이름")}
              placeholder="folder-name"
              value={folder}
              invalid={!!folder.trim() && !folderValid}
              onChange={(e) => setFolder(e.target.value)}
              onKeyDown={(e) => e.key === "Escape" && (e.stopPropagation(), setFolder(null))}
            />
            <Button type="submit" variant="outline" className="h-[42px]" disabled={!folderValid}>
              {t("열기")}
            </Button>
            <Button variant="ghost" className="h-[42px]" onClick={() => setFolder(null)}>
              {t("취소")}
            </Button>
          </form>
        )}
        {folder !== null && <div className="-mt-2 mb-4 text-xs text-gray-500">{t("S3에는 빈 폴더가 없습니다. 폴더를 열고 파일을 올리면 만들어집니다.")}</div>}
        {upload && (
          <div className="mb-3 rounded-md border border-gray-200 px-3 py-2" role="status" aria-live="polite">
            <div className="flex items-center gap-2 text-sm">
              <span className="min-w-0 flex-1 truncate">
                {t("{count}개 파일 올리는 중 · {loaded} / {total}", { count: upload.files, loaded: fmtBytes(upload.loaded), total: fmtBytes(upload.total) })}
              </span>
              <span className="tabular-nums text-gray-500">{pct}%</span>
              <Button size="sm" variant="ghost" onClick={() => upload.abort.abort()}>
                {t("취소")}
              </Button>
            </div>
            <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-gray-100" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct} aria-label={t("업로드 진행률")}>
              <div className="h-full bg-pink-500 transition-[width] motion-reduce:transition-none" style={{ width: `${pct}%` }} />
            </div>
          </div>
        )}
        {error ? <ErrorBox className="mb-3" error={error} /> : null}
        {!running ? (
          <EmptyState icon={<Folder className="size-5" />} title={t("bucket이 실행 중이 아닙니다")}>{t("배포가 끝나면 여기서 파일을 보고 올릴 수 있습니다.")}</EmptyState>
        ) : list.error ? (
          <ErrorBox error={list.error} />
        ) : list.isPending ? (
          <CenterSpinner />
        ) : allPrefixes.length === 0 && allObjects.length === 0 ? (
          <EmptyState icon={<Folder className="size-5" />} title={prefix ? t("이 폴더는 비어 있습니다.") : t("bucket이 비어 있습니다.")}>
            {canEdit && t("업로드하거나 파일을 여기로 끌어 놓으세요(한 번에 {max}까지).", { max: fmtBytes(maxUpload) })}
          </EmptyState>
        ) : (
          <div className="overflow-hidden rounded-lg border border-gray-200 bg-secondaryBg">
            <table className="w-full table-fixed text-sm [&_td:first-child]:pl-4 [&_td:last-child]:pr-2 [&_th:first-child]:pl-4">
              <thead>
                <tr className="h-9 border-b border-gray-200 bg-gray-50 text-left text-xs text-gray-600">
                  <th className="py-2 font-semibold">{t("이름")}</th>
                  <th className="w-24 py-2 text-right font-semibold">{t("크기")}</th>
                  <th className="hidden w-32 py-2 pl-4 font-semibold sm:table-cell">{t("수정|column")}</th>
                  <th className="w-[108px] py-2" aria-label={t("작업")} />
                </tr>
              </thead>
              <tbody>
                {prefix && !q && (
                  <tr className="h-12 border-b border-gray-200 last:border-0">
                    <td colSpan={4}>
                      <button type="button" className="cursor-pointer font-mono text-sm text-gray-600 hover:text-foreground" onClick={() => go(prefix.replace(/[^/]+\/$/, ""))} aria-label={t("상위 폴더")}>
                        ../
                      </button>
                    </td>
                  </tr>
                )}
                {prefixes.map((p) => (
                  <tr key={p} className="h-12 border-b border-gray-200 last:border-0 hover:bg-gray-100">
                    <td colSpan={4}>
                      <button type="button" className="flex min-w-0 cursor-pointer items-center gap-2 hover:text-foreground" onClick={() => go(p)}>
                        <Folder className="size-4 shrink-0 text-gray-400" aria-hidden />
                        <span className="truncate font-mono text-sm">{tail(p, prefix)}</span>
                      </button>
                    </td>
                  </tr>
                ))}
                {objects.map((o) => {
                  const name = tail(o.key, prefix);
                  return (
                    <tr key={o.key} className="h-12 border-b border-gray-200 last:border-0 hover:bg-gray-100">
                      <td>
                        <div className="flex min-w-0 items-center gap-2">
                          {IMAGE_EXT.test(o.key) ? <ImageIcon className="size-4 shrink-0 text-gray-400" aria-hidden /> : <FileIcon className="size-4 shrink-0 text-gray-400" aria-hidden />}
                          {canEdit ? (
                            <button type="button" onClick={() => setPreview(o)} className="min-w-0 cursor-pointer truncate text-left font-mono text-sm hover:text-pink-600 hover:underline" title={o.key}>
                              {name}
                            </button>
                          ) : (
                            <span className="truncate font-mono text-sm" title={o.key}>
                              {name}
                            </span>
                          )}
                        </div>
                      </td>
                      <td className="text-right tabular-nums text-gray-600">{fmtBytes(o.size)}</td>
                      <td className="hidden pl-4 text-sm tabular-nums text-gray-500 sm:table-cell" title={o.last_modified ? new Date(o.last_modified).toLocaleString(dateLocale()) : undefined}>
                        {o.last_modified ? fmtRelative(o.last_modified) : ""}
                      </td>
                      <td>
                        {canEdit && (
                          <div className="flex justify-end gap-0.5">
                            <IconButton label={t("{name} 미리 보기", { name })} onClick={() => setPreview(o)}>
                              <Eye className="size-4" />
                            </IconButton>
                            <a
                              href={bucketApi.downloadUrl(svc.id, env.id, o.key)}
                              download
                              aria-label={t("{name} 내려받기", { name })}
                              title={t("내려받기")}
                              className="inline-flex size-[34px] items-center justify-center rounded-md text-gray-500 hover:bg-gray-100 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-gray-600"
                            >
                              <Download className="size-4" />
                            </a>
                            <IconButton label={t("{name} 삭제", { name })} onClick={() => remove(o)}>
                              <Trash2 className="size-4" />
                            </IconButton>
                          </div>
                        )}
                      </td>
                    </tr>
                  );
                })}
                {q && prefixes.length === 0 && objects.length === 0 && (
                  <tr>
                    <td colSpan={4} className="py-6 text-center text-sm text-gray-500">
                      {t("\"{filter}\"와 맞는 항목이 없습니다", { filter })}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        )}
        <div className="mt-3 flex flex-wrap items-center justify-between gap-2 text-xs text-gray-500">
          <span className="tabular-nums">
            {running && list.data && t("폴더 {folders}개 · 파일 {files}개 · {size}{more}", {
              folders: allPrefixes.length,
              files: allObjects.length,
              size: fmtBytes(totalSize),
              more: list.hasNextPage ? t(" (더 있음)") : "",
            })}
          </span>
          {list.hasNextPage && (
            <Button size="sm" variant="ghost" onClick={() => list.fetchNextPage()} loading={list.isFetchingNextPage}>
              {t("더 보기")}
            </Button>
          )}
        </div>
        {q && list.hasNextPage && <div className="mt-1 text-xs text-gray-500">{t("검색은 지금까지 불러온 항목에만 적용됩니다.")}</div>}
        {canEdit && running && <div className="mt-1 text-xs text-gray-500">{t("파일을 이 목록으로 끌어 놓아도 올라갑니다.")}</div>}
        {!canEdit && running && <div className="mt-3 text-xs text-gray-500">{t("보기 권한으로는 목록만 볼 수 있습니다(내려받기/업로드/삭제는 member 이상).")}</div>}
      </div>
      {preview && <Preview svc={svc} env={env} obj={preview} onClose={() => setPreview(null)} />}
      {node}
    </Card>
  );
}

export function BucketTab({ svc, env }: { svc: CanvasService; env: Environment }) {
  return (
    <div className="pb-12" data-noscroll>
      <Credentials svc={svc} env={env} />
      <Browser svc={svc} env={env} />
    </div>
  );
}
