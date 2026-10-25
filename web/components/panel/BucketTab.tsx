"use client";

import { toast } from "@/lib/uiState";
import { useRef, useState } from "react";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Download, File as FileIcon, Folder, FolderPlus, Lock, RefreshCw, Trash2, Upload } from "lucide-react";
import type { CanvasService, Environment } from "@/lib/api";
import { bucketApi, type BucketObject } from "@/lib/bucket";
import { useCaps } from "@/lib/queries";
import { fmtRelative } from "@/lib/format";
import { t, tr } from "@/lib/i18n";
import { Button, Card, CenterSpinner, CopyField, EmptyState, ErrorBox, IconButton, Input, LABEL_CLS, cx, useConfirm } from "../ui";

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

function Browser({ svc, env }: { svc: CanvasService; env: Environment }) {
  const qc = useQueryClient();
  const caps = useCaps(svc.project_id);
  const canEdit = caps.canEdit;
  const [prefix, setPrefix] = useState("");
  const [folder, setFolder] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const { confirm, node } = useConfirm();
  const running = svc.instance.status === "online" || svc.instance.status === "crashed";
  const key = ["bucket-objects", svc.id, env.id, prefix];

  const list = useInfiniteQuery({
    queryKey: key,
    queryFn: ({ pageParam }) => bucketApi.list(svc.id, env.id, prefix, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next_cursor,
    enabled: running,
    retry: false,
    staleTime: 5_000,
  });
  const prefixes = list.data?.pages.flatMap((p) => p.prefixes) ?? [];
  const objects: BucketObject[] = list.data?.pages.flatMap((p) => p.objects) ?? [];
  const refresh = () => qc.invalidateQueries({ queryKey: ["bucket-objects", svc.id, env.id] });

  const upload = async (files: File[]) => {
    if (!files.length) return;
    setBusy(true);
    setError(null);
    try {
      const total = files.reduce((n, f) => n + f.size, 0);
      if (total > 100 << 20) throw new Error(t("한 번에 100MiB까지 올릴 수 있습니다"));
      const res = await bucketApi.upload(svc.id, env.id, prefix, files);
      toast(t("{count}개 파일을 올렸습니다", { count: res.uploaded.length }));
      await refresh();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
      if (fileInput.current) fileInput.current.value = "";
    }
  };

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
          await refresh();
        } catch (e) {
          setError(e);
        }
      },
    });

  const goFolder = () => {
    const name = (folder ?? "").trim().replace(/^\/+|\/+$/g, "");
    if (!name || name.split("/").some((s) => s === "." || s === "..")) return;
    setPrefix(prefix + name + "/");
    setFolder(null);
  };

  return (
    <Card
      className="!max-w-none"
      title={t("객체")}
      description={t("bucket 안의 파일입니다. 폴더는 키의 / 앞부분입니다.")}
      action={
        <>
        <IconButton label={t("새로 고침")} onClick={refresh} disabled={!running}>
          <RefreshCw className={cx(list.isFetching && "animate-spin")} />
        </IconButton>
        {canEdit && (
          <>
            <Button variant="outline" onClick={() => setFolder("")} disabled={!running}>
              <FolderPlus /> {t("폴더")}
            </Button>
            <Button variant="primary" onClick={() => fileInput.current?.click()} loading={busy} disabled={!running}>
              <Upload /> {t("업로드")}
            </Button>
            <input
              ref={fileInput}
              type="file"
              multiple
              className="hidden"
              aria-label={t("올릴 파일")}
              onChange={(e) => upload(Array.from(e.target.files ?? []))}
            />
          </>
        )}
        </>
      }
    >
      <div>
        <div className="mb-3">
          <Breadcrumbs prefix={prefix} onGo={setPrefix} />
        </div>
        {folder !== null && (
          <form
            className="mb-4 flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              goFolder();
            }}
          >
            <Input mono autoFocus aria-label={t("폴더 이름")} placeholder="folder-name" value={folder} onChange={(e) => setFolder(e.target.value)} />
            <Button type="submit" variant="outline" className="h-[42px]">
              {t("열기")}
            </Button>
            <Button variant="ghost" className="h-[42px]" onClick={() => setFolder(null)}>
              {t("취소")}
            </Button>
          </form>
        )}
        {folder !== null && <div className="-mt-2 mb-4 text-xs text-gray-500">{t("S3에는 빈 폴더가 없습니다. 폴더를 열고 파일을 올리면 만들어집니다.")}</div>}
        {error ? <ErrorBox className="mb-3" error={error} /> : null}
        {!running ? (
          <EmptyState icon={<Folder className="size-5" />} title={t("bucket이 실행 중이 아닙니다")}>{t("배포가 끝나면 여기서 파일을 보고 올릴 수 있습니다.")}</EmptyState>
        ) : list.error ? (
          <ErrorBox error={list.error} />
        ) : list.isPending ? (
          <CenterSpinner />
        ) : prefixes.length === 0 && objects.length === 0 ? (
          <EmptyState icon={<Folder className="size-5" />} title={prefix ? t("이 폴더는 비어 있습니다.") : t("bucket이 비어 있습니다.")}>
            {canEdit && t("업로드로 파일을 올리세요(한 번에 100MiB까지).")}
          </EmptyState>
        ) : (
          <div className="overflow-hidden rounded-lg border border-gray-200 bg-secondaryBg">
          <table className="w-full table-fixed text-sm [&_td:first-child]:pl-4 [&_td:last-child]:pr-2 [&_th:first-child]:pl-4">
            <thead>
              <tr className="h-9 border-b border-gray-200 bg-gray-50 text-left text-xs text-gray-600">
                <th className="py-2 font-semibold">{t("이름")}</th>
                <th className="w-24 py-2 text-right font-semibold">{t("크기")}</th>
                <th className="hidden w-32 py-2 pl-4 font-semibold sm:table-cell">{t("수정|column")}</th>
                <th className="w-20 py-2" aria-label={t("작업")} />
              </tr>
            </thead>
            <tbody>
              {prefix && (
                <tr className="h-12 border-b border-gray-200 last:border-0">
                  <td colSpan={4}>
                    <button type="button" className="cursor-pointer font-mono text-sm text-gray-600 hover:text-foreground" onClick={() => setPrefix(prefix.replace(/[^/]+\/$/, ""))}>
                      ../
                    </button>
                  </td>
                </tr>
              )}
              {prefixes.map((p) => (
                <tr key={p} className="h-12 border-b border-gray-200 last:border-0 hover:bg-gray-100">
                  <td colSpan={4}>
                    <button type="button" className="flex min-w-0 cursor-pointer items-center gap-2 hover:text-foreground" onClick={() => setPrefix(p)}>
                      <Folder className="size-4 shrink-0 text-gray-400" aria-hidden />
                      <span className="truncate font-mono text-sm">{tail(p, prefix)}</span>
                    </button>
                  </td>
                </tr>
              ))}
              {objects.map((o) => (
                <tr key={o.key} className="h-12 border-b border-gray-200 last:border-0 hover:bg-gray-100">
                  <td>
                    <div className="flex min-w-0 items-center gap-2">
                      <FileIcon className="size-4 shrink-0 text-gray-400" aria-hidden />
                      <span className="truncate font-mono text-sm" title={o.key}>
                        {tail(o.key, prefix)}
                      </span>
                    </div>
                  </td>
                  <td className="text-right tabular-nums text-gray-600">{fmtBytes(o.size)}</td>
                  <td className="hidden pl-4 text-sm tabular-nums text-gray-500 sm:table-cell" title={o.last_modified ? new Date(o.last_modified).toLocaleString() : undefined}>{o.last_modified ? fmtRelative(o.last_modified) : ""}</td>
                  <td>
                    {canEdit && (
                      <div className="flex justify-end gap-0.5">
                        <a
                          href={bucketApi.downloadUrl(svc.id, env.id, o.key)}
                          download
                          aria-label={t("{name} 내려받기", { name: tail(o.key, prefix) })}
                          title={t("내려받기")}
                          className="inline-flex size-[34px] items-center justify-center rounded-md text-gray-500 hover:bg-gray-100 hover:text-foreground"
                        >
                          <Download className="size-4" />
                        </a>
                        <IconButton label={t("{name} 삭제", { name: tail(o.key, prefix) })} onClick={() => remove(o)}>
                          <Trash2 className="size-4" />
                        </IconButton>
                      </div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        )}
        {list.hasNextPage && (
          <div className="mt-3 flex justify-center">
            <Button size="sm" variant="ghost" onClick={() => list.fetchNextPage()} loading={list.isFetchingNextPage}>
              {t("더 보기")}
            </Button>
          </div>
        )}
        {!canEdit && running && <div className="mt-3 text-xs text-gray-500">{t("보기 권한으로는 목록만 볼 수 있습니다(내려받기/업로드/삭제는 member 이상).")}</div>}
      </div>
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
