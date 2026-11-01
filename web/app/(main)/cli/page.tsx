"use client";

import { Suspense, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Download, SquareTerminal } from "lucide-react";
import { api, type CliDownload } from "@/lib/api";
import { fmtBytes } from "@/lib/format";
import { t, tr } from "@/lib/i18n";
import { useCtxHref } from "@/lib/nav";
import { Card, CopyButton, CopyField, EmptyState, ErrorBox, LABEL_CLS, PageBody, PageShell, RowList, Segmented, Spinner, cx } from "@/components/ui";

type OS = CliDownload["os"];
type Arch = CliDownload["arch"];

const OS_LABEL: Record<OS, string> = { linux: "Linux", darwin: "macOS", windows: "Windows" };
const ARCH_LABEL: Record<Arch, string> = { amd64: "x86_64 (amd64)", arm64: "ARM64" };
const OS_ORDER: OS[] = ["darwin", "linux", "windows"];

/** Best guess of the visitor's platform. Only picks the default tab and highlights a row. */
function detectOS(): OS | null {
  if (typeof navigator === "undefined") return null;
  const ua = navigator.userAgent;
  if (/Windows/i.test(ua)) return "windows";
  if (/Mac OS X|Macintosh/i.test(ua)) return "darwin";
  if (/Linux|X11/i.test(ua) && !/Android/i.test(ua)) return "linux";
  return null;
}

/** Browsers hide the CPU on most platforms; Macs report "Intel" even on Apple silicon, which most Macs now are. */
function guessArch(os: OS): Arch {
  if (typeof navigator === "undefined") return "amd64";
  if (/aarch64|arm64|armv8/i.test(navigator.userAgent)) return "arm64";
  return os === "darwin" ? "arm64" : "amd64";
}

/** Copyable command block with a label, matching the install card rows. */
function Snippet({ label, value, copyLabel }: { label: string; value: string; copyLabel: string }) {
  return (
    <div>
      <div className={LABEL_CLS}>{label}</div>
      <CopyField value={value} label={copyLabel} />
    </div>
  );
}

function InstallSteps({ os, arch, files, origin }: { os: OS; arch: Arch; files: CliDownload[]; origin: string }) {
  const https = origin.startsWith("https://");
  const file = files.find((f) => f.os === os && f.arch === arch) ?? files.find((f) => f.os === os);
  if (!file) return <p className="text-sm text-gray-500">{t("이 서버에는 {os}용 CLI 바이너리가 없습니다.", { os: OS_LABEL[os] })}</p>;
  const url = `${origin}${file.url}`;

  if (os === "windows") {
    return (
      <div className="space-y-5">
        <Snippet label={t("1. 내려받기 (PowerShell)")} value={`Invoke-WebRequest ${url} -OutFile aven.exe`} copyLabel={t("다운로드 명령")} />
        <Snippet label={t("2. 체크섬 확인")} value={`(Get-FileHash aven.exe -Algorithm SHA256).Hash -eq "${file.sha256.toUpperCase()}"`} copyLabel={t("체크섬 확인 명령")} />
        <p className="text-xs text-gray-500">{t("True가 나오면 aven.exe를 PATH에 있는 폴더로 옮기세요.")}</p>
        <Snippet label={t("3. 로그인")} value={`.\\aven.exe login ${origin}`} copyLabel={t("로그인 명령")} />
      </div>
    );
  }

  const sum = os === "darwin" ? "shasum -a 256" : "sha256sum";
  return (
    <div className="space-y-5">
      {https ? (
        <div>
          <Snippet label={t("설치 스크립트")} value={`curl -fsSL ${origin}/install.sh | sh -s -- --server ${origin}`} copyLabel={t("설치 명령")} />
          <p className="mt-2 text-xs text-gray-500">
            {tr("root가 아니면 {path}에 설치합니다({flag}로 변경). SHA-256 체크섬을 확인한 뒤 설치합니다.", {
              path: <code className="font-mono">~/.local/bin/aven</code>,
              flag: <code className="font-mono">--dir</code>,
            })}
          </p>
        </div>
      ) : (
        <p className="text-sm text-gray-500">{t("설치 스크립트는 HTTPS로 접속한 서버에서만 동작합니다. 아래 명령으로 바이너리를 직접 설치하세요.")}</p>
      )}
      <Snippet
        label={https ? t("직접 설치") : t("1. 내려받기")}
        value={`curl -fLo aven ${url} && echo "${file.sha256}  aven" | ${sum} -c - && chmod +x aven && sudo mv aven /usr/local/bin/aven`}
        copyLabel={t("다운로드 명령")}
      />
      <Snippet label={https ? t("설치 후 로그인") : t("2. 로그인")} value={`aven login ${origin}`} copyLabel={t("로그인 명령")} />
    </div>
  );
}

const NEXT_STEPS: [string, string][] = [
  ["aven link", t("이 디렉터리를 프로젝트·환경·서비스에 연결")],
  ["aven up", t("이 디렉터리를 올려 빌드하고 배포")],
  ["aven logs", t("실행 중인 배포의 로그 보기")],
  ["aven variables", t("서비스 변수 보기·바꾸기")],
  ["aven run -- <cmd>", t("서비스 변수를 넣고 로컬 명령 실행")],
];

function CliInner() {
  const href = useCtxHref();
  const q = useQuery({ queryKey: ["cli-downloads"], queryFn: api.cliDownloads, staleTime: 60_000 });
  // window is only available after hydration (static export).
  const [origin, setOrigin] = useState("");
  const [detected, setDetected] = useState<OS | null>(null);
  const [os, setOS] = useState<OS | null>(null);
  const [arch, setArch] = useState<Arch | null>(null);
  useEffect(() => {
    setOrigin(window.location.origin);
    setDetected(detectOS());
  }, []);

  const files = useMemo(() => q.data?.files ?? [], [q.data]);
  const oses = OS_ORDER.filter((o) => files.some((f) => f.os === o));
  const curOS: OS | null = os ?? (detected && oses.includes(detected) ? detected : oses[0] ?? null);
  const arches = curOS ? (["amd64", "arm64"] as Arch[]).filter((a) => files.some((f) => f.os === curOS && f.arch === a)) : [];
  const guessed = curOS ? guessArch(curOS) : "amd64";
  const curArch: Arch | null = arch && arches.includes(arch) ? arch : arches.includes(guessed) ? guessed : arches[0] ?? null;

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
              env: <code className="font-mono">AVEN_AGENT_BIN_DIR</code>,
            })}
          </EmptyState>
        ) : (
          <>
            <Card
              title={t("Aven CLI 설치")}
              description={
                <>
                  {t("터미널에서 프로젝트를 만들고, 배포하고, 로그와 변수를 다룹니다.")}
                  {q.data?.version ? ` ${t("서버 버전 {version}.", { version: q.data.version })}` : ""}
                </>
              }
            >
              {curOS && (
                <div className="space-y-5">
                  <div className="flex flex-wrap items-center gap-3">
                    <Segmented label={t("운영체제")} value={curOS} onChange={(v) => setOS(v)} options={oses.map((o) => ({ value: o, label: OS_LABEL[o] }))} />
                    {arches.length > 1 && curArch && (
                      <Segmented size="sm" label={t("CPU 아키텍처")} value={curArch} onChange={(v) => setArch(v)} options={arches.map((a) => ({ value: a, label: a === "amd64" ? "x86_64" : "ARM64" }))} />
                    )}
                  </div>
                  {curArch && <InstallSteps os={curOS} arch={curArch} files={files} origin={origin} />}
                </div>
              )}
            </Card>

            <Card title={t("시작하기")} description={t("로그인한 뒤 자주 쓰는 명령입니다. 전체 목록은 aven --help로 봅니다.")} flush>
              <RowList bare className="border-t-0">
                {NEXT_STEPS.map(([cmd, what]) => (
                  <li key={cmd} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-2.5">
                    <code className="min-w-[180px] font-mono text-sm text-foreground">{cmd}</code>
                    <span className="flex-1 text-sm text-gray-600">{what}</span>
                  </li>
                ))}
              </RowList>
            </Card>

            <Card title={t("직접 다운로드")} description={t("플랫폼별 단일 실행 파일입니다. 내려받은 뒤 PATH에 있는 디렉터리로 옮기고 실행 권한을 주세요(chmod +x).")} flush>
              <RowList bare className="border-t-0">
                {files.map((f) => {
                  const mine = f.os === curOS && f.arch === curArch;
                  return (
                    <li key={f.name} className={cx("flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3", mine && "bg-gray-100")}>
                      <div className="min-w-[180px] flex-1">
                        <div className="text-sm font-medium">
                          {OS_LABEL[f.os]} <span className="text-gray-500">{ARCH_LABEL[f.arch]}</span>
                          {f.os === detected && f.arch === guessArch(f.os) && (
                            <span className="ml-2 rounded bg-pink-100 px-1.5 py-0.5 text-xs font-medium text-pink-600">{t("이 기기")}</span>
                          )}
                        </div>
                        <div className="mt-0.5 flex min-w-0 items-center gap-1 font-mono text-xs tabular-nums text-gray-500">
                          <span className="truncate" title={f.sha256}>
                            {f.name} · {fmtBytes(f.size)} · sha256 {f.sha256.slice(0, 12)}…
                          </span>
                          <CopyButton value={f.sha256} label={t("{name} SHA-256 복사", { name: f.name })} className="size-6" />
                        </div>
                      </div>
                      <a
                        href={f.url}
                        download={f.name}
                        aria-label={t("{name} 다운로드", { name: f.name })}
                        className="flex h-[34px] shrink-0 items-center gap-2 rounded-md border border-gray-200 bg-gray-100 px-3 text-sm hover:border-gray-300 hover:bg-gray-200 focus:outline-none focus-visible:ring-2 focus-visible:ring-gray-600"
                      >
                        <Download className="size-4" aria-hidden="true" />
                        {t("다운로드")}
                      </a>
                    </li>
                  );
                })}
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
