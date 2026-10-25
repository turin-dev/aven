"use client";

import { useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Unplug, Webhook } from "lucide-react";
import { api } from "@/lib/api";
import { qk } from "@/lib/queries";
import { toast } from "@/lib/uiState";
import { fmtRelative } from "@/lib/format";
import { t, tr } from "@/lib/i18n";
import { BrandIcon } from "@/components/ServiceIcon";
import { Button, Card, CopyField, ErrorBox, Input, Spinner, errorMessage } from "@/components/ui";

/** 통합 › GitHub 연결: 내 OAuth 승인으로 받은 토큰으로 비공개 저장소를 클론하고 커밋 상태를 올리고 웹훅을 만든다(사용자별 연결). */
export function GitHubConnectionCard(_props: { canAdmin?: boolean } = {}) {
  const qc = useQueryClient();
  const params = useSearchParams();
  const router = useRouter();
  const conn = useQuery({ queryKey: qk.githubConnection, queryFn: api.githubConnection, staleTime: 30_000 });
  const [busy, setBusy] = useState(false);
  const [repo, setRepo] = useState("");
  const [hookBusy, setHookBusy] = useState(false);

  // OAuth 콜백이 ?github=connected|error&message=… 로 돌려보낸다. 한 번 알리고 주소에서 지운다.
  useEffect(() => {
    const result = params.get("github");
    if (!result) return;
    if (result === "connected") toast(t("GitHub를 연결했습니다"), "success");
    else toast(t("GitHub 연결 실패: {message}", { message: params.get("message") ?? t("알 수 없는 오류") }), "error");
    qc.invalidateQueries({ queryKey: qk.github });
    const next = new URLSearchParams(params.toString());
    next.delete("github");
    next.delete("message");
    router.replace(`?${next.toString()}`, { scroll: false });
  }, [params, qc, router]);

  const c = conn.data;
  return (
    <Card
      title={
        <span className="flex items-center gap-2.5">
          <BrandIcon name="github" className="size-5" /> {t("GitHub 연결")}
        </span>
      }
      description={t("내 GitHub 계정을 연결하면 내가 소유한 프로젝트가 비공개 저장소를 클론하고, 커밋에 배포 상태를 표시하고, 저장소에 웹훅을 자동으로 만들 수 있습니다. 이 연결은 당신 계정에만 적용됩니다.")}
    >
      {conn.error ? (
        <ErrorBox error={conn.error} />
      ) : !c ? (
        <Spinner />
      ) : (
        <div className="space-y-5">
          {c.connected ? (
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex items-center gap-2 text-sm">
                <Check className="size-4 text-success" />
                <span>
                  <b className="text-foreground">{c.login}</b> {t("계정으로 연결됨")}
                  {c.connected_at && <span className="text-gray-500"> · {fmtRelative(c.connected_at)}</span>}
                </span>
              </div>
              <Button
                variant="outline"
                loading={busy}
                onClick={async () => {
                  setBusy(true);
                  try {
                    await api.githubDisconnect();
                    toast(t("GitHub 연결을 해제했습니다"));
                    qc.invalidateQueries({ queryKey: qk.github });
                  } catch (e) {
                    toast(errorMessage(e), "error");
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                <Unplug className="size-4" /> {t("연결 해제")}
              </Button>
            </div>
          ) : c.app_configured ? (
            <div className="flex flex-wrap items-center justify-between gap-3">
              <p className="text-sm text-gray-600">
                {c.source === "env" ? (
                  <>
                    {tr("지금은 서버의 {token}을 씁니다. 내 계정을 연결하면 그 대신 내 계정을 씁니다.", { token: <span className="font-mono">KEEL_GITHUB_TOKEN</span> })}
                  </>
                ) : (
                  t("연결된 계정이 없어 비공개 저장소를 클론할 수 없습니다.")
                )}
              </p>
              {/* OAuth 승인은 GitHub 화면으로 이동해야 하므로 API 경로로 바로 간다(세션 쿠키로 인증). */}
              <a href={`/api/github/oauth/start${typeof window !== "undefined" && window.location.pathname.startsWith("/account") ? "?from=account" : ""}`}>
                <Button variant="primary">
                  <BrandIcon name="github" className="size-4" /> {t("GitHub 연결")}
                </Button>
              </a>
            </div>
          ) : (
            <div className="space-y-3 text-sm text-gray-600">
              <p>{t("GitHub OAuth App이 서버에 설정되지 않았습니다.")}</p>
              <ol className="list-decimal space-y-1.5 pl-5 marker:text-gray-400">
                <li>
                  {tr("GitHub {path}을 엽니다.", { path: <b className="text-foreground">Settings › Developer settings › OAuth Apps › New OAuth App</b> })}
                </li>
                <li>
                  {tr("{field}에 아래 주소를 넣습니다.", { field: <b className="text-foreground">Authorization callback URL</b> })}
                  <div className="mt-1.5">
                    <CopyField value={c.callback_url} label={t("콜백 URL")} />
                  </div>
                </li>
                <li>
                  {tr("만든 앱의 Client ID와 Client secret을 서버의 {id}, {secret}에 넣고 aven-server를 다시 시작합니다.", {
                    id: <span className="font-mono">KEEL_GITHUB_CLIENT_ID</span>,
                    secret: <span className="font-mono">KEEL_GITHUB_CLIENT_SECRET</span>,
                  })}
                </li>
              </ol>
            </div>
          )}

          {(c.connected || c.source === "env") && (
            <form
              className="space-y-2"
              onSubmit={async (e) => {
                e.preventDefault();
                if (!repo.trim()) return;
                setHookBusy(true);
                try {
                  const r = await api.githubWebhook(repo.trim());
                  toast(t("{repo}: 웹훅을 {action}", { repo: r.repo, action: r.created ? t("만들었습니다") : t("업데이트했습니다") }), "success");
                  setRepo("");
                } catch (err) {
                  toast(errorMessage(err), "error");
                } finally {
                  setHookBusy(false);
                }
              }}
            >
              <div className="text-sm font-semibold">{t("저장소 웹훅 만들기")}</div>
              <p className="text-sm text-gray-500">
                {t("push, pull request, CI 이벤트를 aven로 보내는 웹훅을 저장소에 만듭니다. 이미 있으면 설정을 맞춥니다.")}
                {!c.secret_configured && (
                  <span className="text-warn">
                    {" "}
                    {tr("서버에 {name}이 없어 만들 수 없습니다.", { name: <span className="font-mono">KEEL_GITHUB_WEBHOOK_SECRET</span> })}
                  </span>
                )}
              </p>
              <div className="flex gap-2">
                <Input mono value={repo} onChange={(e) => setRepo(e.target.value)} placeholder={t("owner/repo 또는 https://github.com/owner/repo")} />
                <Button type="submit" loading={hookBusy} disabled={!c.secret_configured || !repo.trim()}>
                  <Webhook className="size-4" /> {t("만들기")}
                </Button>
              </div>
            </form>
          )}
        </div>
      )}
    </Card>
  );
}
