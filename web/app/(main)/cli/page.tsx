"use client";

import { Suspense, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Download, SquareTerminal } from "lucide-react";
import { api, type CliDownload } from "@/lib/api";
import { t, tr } from "@/lib/i18n";
import { useCtxHref } from "@/lib/nav";
import { Card, CopyButton, CopyField, EmptyState, ErrorBox, LABEL_CLS, PageBody, PageShell, RowList, Spinner, cx } from "@/components/ui";

const OS_LABEL: Record<CliDownload["os"], string> = { linux: "Linux", darwin: "macOS", windows: "Windows" };
const ARCH_LABEL: Record<CliDownload["arch"], string> = { amd64: "x86_64 (amd64)", arm64: "ARM64" };

/** Best guess of the visitor's platform, used only to highlight a row. */
function detectOS(): CliDownload["os"] | null {
  if (typeof navigator === "undefined") return null;
  const ua = navigator.userAgent;
  if (/Windows/i.test(ua)) return "windows";
  if (/Mac OS X|Macintosh/i.test(ua)) return "darwin";
  if (/Linux|X11/i.test(ua)) return "linux";
  return null;
}

function formatSize(bytes: number): string {
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function CliInner() {
  const href = useCtxHref();
  const q = useQuery({ queryKey: ["cli-downloads"], queryFn: api.cliDownloads, staleTime: 60_000 });
  // window is only available after hydration (static export).
  const [origin, setOrigin] = useState("");
  const [os, setOS] = useState<CliDownload["os"] | null>(null);
  useEffect(() => {
    setOrigin(window.location.origin);
    setOS(detectOS());
  }, []);

  const files = q.data?.files ?? [];
  const https = origin.startsWith("https://");
  const has = (name: string) => files.some((f) => f.name === name);
  const unixCmd = `curl -fsSL ${origin}/install.sh | sh -s -- --server ${origin}`;
  const winCmd = `Invoke-WebRequest ${origin}/downloads/aven-windows-amd64.exe -OutFile keel.exe`;

  return (
    <PageShell title="CLI" close={{ href: href("/"), label: t("CLI 닫기") }}>
      <PageBody>
        <ErrorBox error={q.error} />
        {q.isPending ? (
          <div className="flex justify-center py-16">
            <Spinner />
          </div>
        ) : files.length === 0 ? (
          <EmptyState icon={<SquareTerminal className="size-6" />} title={t("이 서버에 CLI 바이너리가 없습니다")}>
            {tr("서버 관리자가 {cmd}로 빌드한 {bin} 파일을 aven-server 실행 파일 옆(또는 {env})에 두면 여기에 나타납니다.", {
              cmd: <code className="font-mono">make clis</code>,
              bin: <code className="font-mono">bin/aven-*</code>,
              env: <code className="font-mono">KEEL_AGENT_BIN_DIR</code>,
            })}
          </EmptyState>
        ) : (
          <>
            <Card
              title={t("aven CLI 설치")}
              description={
                <>
                  {t("터미널에서 프로젝트를 만들고, 배포하고, 로그와 변수를 다룹니다.")}
                  {q.data?.version ? ` ${t("서버 버전 {version}.", { version: q.data.version })}` : ""}
                </>
              }
            >
              <div className="space-y-5">
                <div>
                  <div className={LABEL_CLS}>Linux · macOS</div>
                  {https ? (
                    <CopyField value={unixCmd} label={t("설치 명령")} />
                  ) : (
                    <p className="text-sm text-gray-500">{t("설치 스크립트는 HTTPS로 접속한 서버에서만 동작합니다. 아래에서 바이너리를 직접 내려받으세요.")}</p>
                  )}
                  <p className="mt-2 text-xs text-gray-500">
                    {tr("root가 아니면 {path}에 설치합니다({flag}로 변경). SHA-256 체크섬을 확인한 뒤 설치합니다.", {
                      path: <code className="font-mono">~/.local/bin/aven</code>,
                      flag: <code className="font-mono">--dir</code>,
                    })}
                  </p>
                </div>
                {has("aven-windows-amd64.exe") && (
                  <div>
                    <div className={LABEL_CLS}>Windows (PowerShell)</div>
                    <CopyField value={winCmd} label={t("다운로드 명령")} />
                  </div>
                )}
                <div>
                  <div className={LABEL_CLS}>{t("설치 후 로그인")}</div>
                  <CopyField value={`aven login ${origin}`} label={t("로그인 명령")} />
                </div>
              </div>
            </Card>

            <Card title={t("직접 다운로드")} description={t("플랫폼별 단일 실행 파일입니다. 내려받은 뒤 PATH에 있는 디렉터리로 옮기고 실행 권한을 주세요(chmod +x).")} flush>
              <RowList bare className="border-t-0">
                {files.map((f) => (
                  <li key={f.name} className={cx("flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3", f.os === os && "bg-gray-100")}>
                    <div className="min-w-[180px] flex-1">
                      <div className="text-sm font-medium">
                        {OS_LABEL[f.os]} <span className="text-gray-500">{ARCH_LABEL[f.arch]}</span>
                        {f.os === os && <span className="ml-2 rounded bg-pink-100 px-1.5 py-0.5 text-xs font-medium text-pink-600">{t("이 기기")}</span>}
                      </div>
                      <div className="mt-0.5 flex items-center gap-1 font-mono text-xs tabular-nums text-gray-500">
                        <span className="truncate" title={f.sha256}>
                          {f.name} · {formatSize(f.size)} · sha256 {f.sha256.slice(0, 12)}…
                        </span>
                        <CopyButton value={f.sha256} label={t("{name} SHA-256 복사", { name: f.name })} className="size-6" />
                      </div>
                    </div>
                    <a
                      href={f.url}
                      download={f.name}
                      className="flex h-[34px] shrink-0 items-center gap-2 rounded-md border border-gray-200 bg-gray-100 px-3 text-sm hover:border-gray-300 hover:bg-gray-200 focus:outline-none focus-visible:ring-2 focus-visible:ring-gray-600"
                    >
                      <Download className="size-4" aria-hidden="true" />
                      {t("다운로드")}
                    </a>
                  </li>
                ))}
              </RowList>
            </Card>
          </>
        )}
      </PageBody>
    </PageShell>
  );
}

export default function CliPage() {
  return (
    <Suspense fallback={<div className="dot-grid min-h-full" />}>
      <CliInner />
    </Suspense>
  );
}
