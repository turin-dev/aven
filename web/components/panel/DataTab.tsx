"use client";

import { useState } from "react";
import { createPortal } from "react-dom";
import { useQueryClient } from "@tanstack/react-query";
import { Lock, Plug, Unplug, X } from "lucide-react";
import { api, type CanvasService, type DatabaseKind, type Environment } from "@/lib/api";
import { qk, useResolvedVariables } from "@/lib/queries";
import { Button, CenterSpinner, IconButton, CopyField, EmptyState, ErrorBox, LABEL_CLS, Segmented, cx, useEscape } from "../ui";
import { DataBrowser } from "./DataBrowser";
import { DbConfig, DbStats } from "./DbExtra";
import { PostgresOps } from "./PostgresOps";
import { HASection } from "./HASection";
import { t, tr } from "@/lib/i18n";

type Sub = "data" | "stats" | "config";

const SPEC: Record<
  DatabaseKind,
  { url: string; pub: string; fields: [string, string][]; cli: (u: string) => string; client: string }
> = {
  postgres: {
    url: "DATABASE_URL",
    pub: "DATABASE_PUBLIC_URL",
    fields: [[t("호스트"), "PGHOST"], [t("포트"), "PGPORT"], [t("사용자"), "PGUSER"], [t("비밀번호"), "PGPASSWORD"], [t("데이터베이스"), "PGDATABASE"]],
    cli: (u) => `psql "${u}"`,
    client: "psql",
  },
  mysql: {
    url: "MYSQL_URL",
    pub: "MYSQL_PUBLIC_URL",
    fields: [[t("호스트"), "MYSQLHOST"], [t("포트"), "MYSQLPORT"], [t("사용자"), "MYSQLUSER"], [t("비밀번호"), "MYSQLPASSWORD"], [t("데이터베이스"), "MYSQLDATABASE"]],
    cli: (u) => `mysql --uri "${u}"`,
    client: "mysql",
  },
  redis: {
    url: "REDIS_URL",
    pub: "REDIS_PUBLIC_URL",
    fields: [[t("호스트"), "REDISHOST"], [t("포트"), "REDISPORT"], [t("사용자"), "REDISUSER"], [t("비밀번호"), "REDISPASSWORD"]],
    cli: (u) => `redis-cli -u "${u}"`,
    client: "redis-cli",
  },
  mongo: {
    url: "MONGO_URL",
    pub: "MONGO_PUBLIC_URL",
    fields: [[t("호스트"), "MONGOHOST"], [t("포트"), "MONGOPORT"], [t("사용자"), "MONGOUSER"], [t("비밀번호"), "MONGOPASSWORD"]],
    cli: (u) => `mongosh "${u}"`,
    client: "mongosh",
  },
  // bucket은 데이터 탭 대신 Bucket 탭(BucketTab)을 쓴다. 타입을 채우려고 둔다.
  bucket: {
    url: "ENDPOINT",
    pub: "PUBLIC_ENDPOINT",
    fields: [["Bucket", "BUCKET"], ["Region", "REGION"], ["Access key", "ACCESS_KEY_ID"], [t("비밀번호"), "SECRET_ACCESS_KEY"]],
    cli: (u) => `aws s3 ls --endpoint-url "${u}"`,
    client: "aws",
  },
};

function Label({ children }: { children: string }) {
  return <div className={LABEL_CLS}>{children}</div>;
}

export function DataTab({ svc, env }: { svc: CanvasService; env: Environment }) {
  const qc = useQueryClient();
  const kind = svc.database_kind ?? "postgres";
  const spec = SPEC[kind];
  const vars = useResolvedVariables(svc.id, env.id, true);
  const [net, setNet] = useState<"private" | "public">("private");
  const [busy, setBusy] = useState(false);
  const [sub, setSub] = useState<Sub>("data");
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<unknown>(null);
  // Esc closes the connect dialog (and not the service panel behind it).
  useEscape(() => setOpen(false), open);
  const tcp = svc.instance.tcp_proxy;
  const v: Record<string, string> = Object.fromEntries(Object.entries(vars.data ?? {}).map(([k, r]) => [k, r.value]));
  const url = net === "private" ? v[spec.url] : v[spec.pub];

  const enableTcp = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.addTcpProxy(svc.id, env.id, svc.instance.port ?? Number(v[spec.fields[1][1]] ?? 0));
      await qc.invalidateQueries({ queryKey: qk.canvas(env.id) });
      await qc.invalidateQueries({ queryKey: qk.resolved(svc.id, env.id) });
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  const connect = (
      <div>
        <Segmented
          label={t("네트워크")}
          value={net}
          onChange={setNet}
          options={[
            { value: "private", label: t("비공개 네트워크") },
            { value: "public", label: t("공개 네트워크") },
          ]}
        />
        <div className="mt-6 space-y-5">
          {vars.error ? (
            <ErrorBox error={vars.error} />
          ) : vars.isPending ? (
            <CenterSpinner />
          ) : net === "public" && !tcp ? (
            <>
              <EmptyState
                icon={<Unplug className="size-5" />}
                title={t("TCP 프록시가 꺼져 있어서 외부에서 접속할 수 없습니다.")}
                action={
                  <Button onClick={enableTcp} loading={busy}>
                    {t("TCP 프록시 켜기")}
                  </Button>
                }
              >
                {t("켜면 서버의 공개 포트 하나가 이 데이터베이스로 연결됩니다.")}
              </EmptyState>
              {error ? <ErrorBox error={error} /> : null}
            </>
          ) : (
            <>
              {net === "private" && (
                <ol className="relative space-y-6 pb-2 text-base">
                  <li className="flex gap-4"><span className="flex size-8 shrink-0 items-center justify-center rounded-full border border-gray-200 text-sm">1</span><span className="pt-1">{t("이 데이터베이스에 연결할 서비스에 새 변수를 만드세요.")}</span></li>
                  <li className="flex gap-4">
                    <span className="flex size-8 shrink-0 items-center justify-center rounded-full border border-gray-200 text-sm">2</span>
                    <div className="min-w-0 flex-1 space-y-3 pt-1"><span>{t("다음 값을 지정하세요.")}</span><CopyField value={`\${{ ${svc.name}.${spec.url} }}`} label={t("변수 참조")} /></div>
                  </li>
                  <li className="flex gap-4"><span className="flex size-8 shrink-0 items-center justify-center rounded-full border border-gray-200 text-sm">3</span><span className="pt-1">{t("애플리케이션 코드에서 그 변수를 사용하세요.")}</span></li>
                </ol>
              )}
              <div>
                <Label>{t("연결 URL ({name})", { name: net === "private" ? spec.url : spec.pub })}</Label>
                {url ? <CopyField value={url} secret label={t("연결 URL")} /> : <div className="text-sm text-gray-500">{t("변수 {name}가 없습니다.", { name: net === "private" ? spec.url : spec.pub })}</div>}
              </div>
              {url && (
                <div>
                  <Label>{t("{client} 명령", { client: spec.client })}</Label>
                  <CopyField value={spec.cli(url)} secret label={t("{client} 명령", { client: spec.client })} />
                </div>
              )}
              {net === "private" ? (
                <div className="flex items-start gap-2 rounded-md bg-gray-50 px-3 py-2.5 text-sm text-gray-600">
                  <Lock className="mt-0.5 size-3.5 shrink-0 text-gray-400" />
                  <span>
                    {tr("같은 환경의 서비스에서는 변수 값으로 {ref}를 쓰세요. 트래픽이 서버 밖으로 나가지 않습니다.", {
                      ref: (
                        <span className="font-mono text-pink-600">
                          {"${{"}
                          {svc.name}.{spec.url}
                          {"}}"}
                        </span>
                      ),
                    })}
                  </span>
                </div>
              ) : (
                <div className="text-sm text-gray-500">
                  {tr("공개 주소 {addr} → 컨테이너 포트 {port}", {
                    addr: <span className="font-mono text-gray-600">{`${v.AVEN_TCP_PROXY_DOMAIN ?? t("서버")}:${tcp?.public_port}`}</span>,
                    port: <span className="font-mono text-gray-600">{tcp?.container_port}</span>,
                  })}
                </div>
              )}
              <div className="grid gap-4 sm:grid-cols-2">
                {spec.fields.map(([label, key]) => {
                  let value = v[key] ?? "";
                  if (net === "public" && tcp) {
                    if (label === t("호스트")) value = v.AVEN_TCP_PROXY_DOMAIN ?? value;
                    if (label === t("포트")) value = String(tcp.public_port);
                  }
                  return (
                    <div key={key}>
                      <Label>{label}</Label>
                      <CopyField value={value} secret={label === t("비밀번호")} label={label} />
                    </div>
                  );
                })}
              </div>
            </>
          )}
        </div>
      </div>

  );

  const subs: [Sub, string][] = [
    ["data", t("데이터")],
    ["stats", t("통계")],
    ["config", t("설정")],
  ];
  return (
    <div data-noscroll>
      <div className="flex items-center gap-1 border-b border-gray-200 pb-3">
        <div role="tablist" aria-label={t("데이터베이스 보기")} className="flex items-center gap-1">
          {subs.map(([m, l]) => (
            <button
              key={m}
              type="button"
              role="tab"
              aria-selected={sub === m}
              onClick={() => setSub(m)}
              className={cx(
                "shrink-0 cursor-pointer rounded-md px-2.5 py-1 text-[13px] transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500",
                sub === m ? "bg-gray-100 font-medium text-foreground" : "text-gray-500 hover:text-foreground",
              )}
            >
              {l}
            </button>
          ))}
        </div>
        <Button variant="outline" size="sm" className="ml-auto" onClick={() => setOpen(true)}>
          <Plug /> {t("연결")}
        </Button>
      </div>
      <div className="pb-12">
        {sub === "data" ? (
          <DataBrowser svc={svc} env={env} />
        ) : sub === "stats" ? (
          <DbStats svc={svc} env={env} />
        ) : (
          <div className="pt-8">
            <DbConfig svc={svc} spec={spec} tcp={!!tcp} />
            {svc.database_kind === "postgres" && <PostgresOps svc={svc} env={env} />}
            <HASection svc={svc} env={env} />
          </div>
        )}
      </div>
      {open && createPortal(
        <div
          className="fixed inset-0 z-[10000] flex items-center justify-center bg-black/[.45] p-4 dark:bg-black/80"
          onMouseDown={(e) => e.target === e.currentTarget && setOpen(false)}
        >
          <div role="dialog" aria-modal="true" aria-label={t("{name}에 연결", { name: svc.name })} className="relative max-h-[calc(100vh-2rem)] w-full max-w-[719px] animate-modal-scale-in overflow-y-auto rounded-lg bg-background p-6 dark:bg-secondaryBg">
            <div className="flex items-center">
              <h2 className="text-xl font-semibold">{t("{name}에 연결", { name: svc.name })}</h2>
              <IconButton label={t("닫기")} onClick={() => setOpen(false)} className="ml-auto">
                <X />
              </IconButton>
            </div>
            <div className="mt-5">{connect}</div>
          </div>
        </div>,
        document.body,
      )}
    </div>
  );
}
