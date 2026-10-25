"use client";

import Link from "next/link";
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type SelfServerInfo } from "@/lib/api";
import { canvasHref } from "@/lib/nav";
import { qk } from "@/lib/queries";
import { toast } from "@/lib/uiState";
import { t, tr } from "@/lib/i18n";
import { AvenMark, Button, useConfirm } from "./ui";
import { RefreshCw } from "lucide-react";
import { fmtRelative } from "@/lib/format";

// Aven on Aven: aven-server 자신을 캔버스의 서비스로 다룬다(GET/POST/DELETE /api/owner/self-service, docs/selfhost.md).

export const SELFHOST_GUIDE_URL = "https://github.com/turin-dev/aven/blob/main/docs/selfhost.md";

const Mono = ({ children }: { children: string }) => <span className="font-mono text-xs">{children}</span>;

function modeLine(s: SelfServerInfo) {
  switch (s.mode) {
    case "container":
      return tr("aven-server 컨테이너로 돕니다({image}). 배포하면 새 이미지로 바꿉니다.", { image: <Mono>{s.image || "?"}</Mono> });
    case "systemd":
      return tr("지금은 systemd 유닛 {unit}으로 돕니다. 처음 배포하면 aven-server 컨테이너로 옮기고, 유닛은 비활성화해 비상용으로 남깁니다.", { unit: <Mono>{s.unit || "aven-server"}</Mono> });
    default:
      return t("systemd나 컨테이너 밖에서 돌고 있어 배포로 바꿀 수 없습니다. 이미지는 빌드됩니다.");
  }
}

/** 캔버스 카드의 이름 옆 표시(이름이 잘리지 않게 아이콘만). 새 버전이 있으면 분홍 점을 붙인다. */
export function SelfServiceBadge({ self }: { self: SelfServerInfo }) {
  const label = t("이 대시보드를 돌리는 aven-server입니다");
  const update = self.update_available
    ? self.update_behind
      ? t("업데이트 있음 · 커밋 {count}개", { count: self.update_behind })
      : t("업데이트 있음")
    : null;
  return (
    <span title={[label, self.version, update].filter(Boolean).join(" · ")} aria-label={update ? `${label} · ${update}` : label} className="relative flex items-center">
      <AvenMark className="size-4" />
      {update && <span className="absolute -right-1 -top-1 size-2 rounded-full bg-pink-500 ring-2 ring-background" aria-hidden />}
    </span>
  );
}

/** 서비스 패널 위쪽 안내: 배포하면 서버가 바뀐다는 것, 지금 도는 방식, 업데이트 확인 결과. */
export function SelfServiceNotice({ self, svcId, envId }: { self: SelfServerInfo; svcId: string; envId: string }) {
  return (
    <div role="note" className="mb-6 rounded-lg border border-purple-200 bg-purple-50 text-sm text-gray-600 dark:border-purple-800 dark:bg-purple-950/30">
      <div className="flex gap-3 p-4">
        <AvenMark className="mt-0.5 size-5 shrink-0" />
        <div className="min-w-0 space-y-1.5">
          <p className="font-medium text-foreground">{t("이 서비스는 지금 이 대시보드를 돌리는 aven-server입니다")}</p>
          <p>
            {t("배포하면 저장소의 Dockerfile로 새 버전을 빌드해 서버를 바꿉니다. 새 서버가 4분 안에 정상이 되지 않으면 이전 버전으로 되돌립니다. 바꾸는 몇 초 동안 대시보드와 API가 끊기지만 다른 서비스는 계속 돕니다.")}
          </p>
          <p>{modeLine(self)}</p>
          <p className="text-xs text-gray-500">
            {t("지금 버전 {version}", { version: self.version })} ·{" "}
            <a href={SELFHOST_GUIDE_URL} target="_blank" rel="noreferrer" className="underline">{t("Aven on Aven 안내")}</a>
          </p>
        </div>
      </div>
      <UpdateStatus svcId={svcId} envId={envId} />
    </div>
  );
}

/** Go duration("30m0s", "1h0m0s")을 "30분마다 자동 확인"처럼 읽는다. */
function everyLabel(d: string) {
  const h = Number(/(\d+)h/.exec(d)?.[1] ?? 0);
  const m = Number(/(\d+)m/.exec(d)?.[1] ?? 0);
  return h && !m ? t("{n}시간마다 자동 확인", { n: h }) : t("{n}분마다 자동 확인", { n: h * 60 + m });
}

/** 업데이트 확인 결과(GET /api/owner/self-service의 update)와 업데이트·지금 확인 버튼. */
function UpdateStatus({ svcId, envId }: { svcId: string; envId: string }) {
  const qc = useQueryClient();
  const query = useQuery({ queryKey: qk.selfService, queryFn: api.selfService });
  const [busy, setBusy] = useState<"" | "check" | "deploy">("");
  const u = query.data?.update;
  if (!u) return null;
  const check = async () => {
    setBusy("check");
    try {
      qc.setQueryData(qk.selfService, await api.checkSelfUpdate());
      void qc.invalidateQueries({ queryKey: ["canvas"] });
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), "error");
    } finally {
      setBusy("");
    }
  };
  const deploy = async () => {
    setBusy("deploy");
    try {
      await api.deploy(svcId, envId);
      toast(t("aven-server 업데이트를 시작했습니다. 빌드가 끝나면 잠시 대시보드가 끊깁니다."));
      void qc.invalidateQueries({ queryKey: ["deployments"] });
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), "error");
    } finally {
      setBusy("");
    }
  };
  const checked = u.checked_at && !u.checked_at.startsWith("0001") ? fmtRelative(u.checked_at) : null;
  const repo = u.repo.replace(/^https:\/\/github\.com\//, "").replace(/\.git$/, "");
  const compare = u.current_sha && u.latest ? `https://github.com/${repo}/compare/${u.current_sha}...${u.latest.sha}` : null;
  const shown = (u.commits?.length ? u.commits : u.latest ? [u.latest] : []).slice(0, 5);
  return (
    <div className="border-t border-purple-200 px-4 py-3 dark:border-purple-800">
      <div className="flex flex-wrap items-center gap-2">
        {u.available ? (
          <span className="flex items-center gap-2 font-medium text-foreground">
            <span className="size-2 rounded-full bg-pink-500" aria-hidden />
            {u.behind ? t("새 버전이 있습니다 · 커밋 {count}개", { count: u.behind }) : t("새 버전이 있습니다")}
          </span>
        ) : u.error ? (
          <span className="text-red-600">{t("업데이트를 확인하지 못했습니다: {error}", { error: u.error })}</span>
        ) : checked ? (
          <span>{t("최신 버전입니다")}</span>
        ) : (
          <span>{t("아직 업데이트를 확인하지 않았습니다")}</span>
        )}
        <span className="text-xs text-gray-500">
          {checked && t("{when} 확인", { when: checked })}
          {u.interval !== "off" && <> · {everyLabel(u.interval)}</>}
        </span>
        <span className="ml-auto flex gap-2">
          <Button size="sm" variant="ghost" loading={busy === "check"} disabled={!!busy} onClick={() => void check()}>
            <RefreshCw className="size-3.5" />
            {t("지금 확인")}
          </Button>
          {u.available && (
            <Button size="sm" variant="primary" loading={busy === "deploy"} disabled={!!busy} onClick={() => void deploy()}>
              {t("업데이트")}
            </Button>
          )}
        </span>
      </div>
      {u.available && shown.length > 0 && (
        <ul className="mt-2 grid gap-1">
          {shown.map((c) => (
            <li key={c.sha} className="flex min-w-0 items-center gap-2 text-xs">
              <a href={`https://github.com/${repo}/commit/${c.sha}`} target="_blank" rel="noreferrer" className="shrink-0 font-mono text-purple-700 hover:underline dark:text-purple-300">
                {c.sha.slice(0, 7)}
              </a>
              <span className="min-w-0 truncate text-foreground">{c.message}</span>
              <span className="ml-auto shrink-0 text-gray-500">{c.author} · {fmtRelative(c.at)}</span>
            </li>
          ))}
          {compare && (u.behind ?? 0) > shown.length && (
            <li className="text-xs">
              <a href={compare} target="_blank" rel="noreferrer" className="text-gray-500 underline">{t("나머지 {count}개 보기", { count: u.behind - shown.length })}</a>
            </li>
          )}
        </ul>
      )}
    </div>
  );
}

/** 어드민 › 초기 설정의 "Aven을 캔버스에서 관리" 항목 버튼. */
export function SelfServiceSetupAction() {
  const qc = useQueryClient();
  const query = useQuery({ queryKey: qk.selfService, queryFn: api.selfService });
  const [busy, setBusy] = useState(false);
  const { confirm, node } = useConfirm();
  const s = query.data;
  if (!s) return null;
  const refresh = () => Promise.all([
    qc.invalidateQueries({ queryKey: qk.selfService }),
    qc.invalidateQueries({ queryKey: qk.setupStatus }),
    qc.invalidateQueries({ queryKey: qk.projects }),
  ]);
  const create = async () => {
    setBusy(true);
    try {
      await api.createSelfService();
      await refresh();
      toast(t("Aven 프로젝트를 만들었습니다. 캔버스에서 aven-server를 배포하면 이 서버가 바뀝니다."));
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), "error");
    } finally {
      setBusy(false);
    }
  };
  if (!s.enabled) {
    return <Button size="sm" variant="primary" loading={busy} onClick={() => void create()}>{t("캔버스에 Aven 추가")}</Button>;
  }
  const href = s.service_id ? canvasHref({ svc: s.service_id }, { project: s.project_id, env: s.environment_id, ws: s.workspace_id }) : "/projects/";
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Link href={href}><Button size="sm">{t("캔버스에서 열기")}</Button></Link>
      {!s.from_env && (
        <Button
          size="sm"
          variant="outline"
          onClick={() => confirm({
            title: t("Aven 관리 해제"),
            message: t("aven-server 서비스가 평범한 서비스로 남고, 배포해도 이 서버를 바꾸지 않습니다. 프로젝트와 서비스는 지우지 않습니다."),
            confirmLabel: t("해제"),
            run: async () => {
              try {
                await api.unlinkSelfService();
                await refresh();
              } catch (e) {
                toast(e instanceof Error ? e.message : String(e), "error");
              }
            },
          })}
        >
          {t("해제")}
        </Button>
      )}
      {s.repo_url && <span className="font-mono text-xs text-gray-500">{s.repo_url}{s.branch ? `@${s.branch}` : ""}</span>}
      {node}
    </div>
  );
}
