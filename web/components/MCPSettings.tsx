"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { Card, CopyButton, CopyField, Segmented } from "@/components/ui";
import { t, tr } from "@/lib/i18n";

type Mode = "cli" | "http";

function Snippet({ value, label }: { value: string; label: string }) {
  return (
    <div className="relative mt-3 first:mt-0">
      <pre className="overflow-x-auto rounded-lg border border-gray-200 bg-gray-50 p-4 pr-12 font-mono text-xs leading-5 text-gray-700">{value}</pre>
      <CopyButton value={value} label={label} className="absolute right-2 top-2" />
    </div>
  );
}

const json = (v: unknown) => JSON.stringify(v, null, 2);

/** 프로젝트 설정 → AI 도구 · MCP: MCP 클라이언트(Claude Code, Cursor, VS Code)를 Aven에 연결하는 방법. */
export function MCPSettings({ project, env }: { project?: string; env?: string }) {
  const [origin, setOrigin] = useState("https://aven.example.com");
  useEffect(() => setOrigin(window.location.origin), []);
  const [mode, setMode] = useState<Mode>("cli");

  const endpoint = `${origin}/mcp`;
  const q = (s: string) => (/^[\w.-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`);
  const tokenCmd = `aven tokens create --name mcp${project ? ` --project ${q(project)}` : ""}${env ? ` --environment ${q(env)}` : ""} --role member`;
  const stdio = { command: "aven", args: ["mcp"] };
  const auth = { Authorization: "Bearer ${AVEN_TOKEN}" };

  const claude =
    mode === "cli"
      ? `aven login ${origin}\nclaude mcp add aven -- aven mcp`
      : `export AVEN_TOKEN=$(${tokenCmd})\nclaude mcp add --transport http aven ${endpoint} \\\n  --header "Authorization: Bearer $AVEN_TOKEN"`;
  const claudeJson = json({ mcpServers: { aven: mode === "cli" ? stdio : { type: "http", url: endpoint, headers: auth } } });
  const cursor = json({ mcpServers: { aven: mode === "cli" ? stdio : { url: endpoint, headers: { Authorization: "Bearer ${env:AVEN_TOKEN}" } } } });
  const vscode = json({
    servers: { aven: mode === "cli" ? { type: "stdio", ...stdio } : { type: "http", url: endpoint, headers: { Authorization: "Bearer ${env:AVEN_TOKEN}" } } },
  });

  return (
    <>
      <Card
        title={t("AI 도구 · MCP")}
        description={t("Aven MCP 서버로 Claude Code, Cursor 같은 AI 에이전트가 프로젝트·서비스·배포·로그·변수를 조회하고, 배포·재시작·롤백, 변수 설정, 서비스와 도메인 추가를 할 수 있습니다. 모든 도구는 연결한 계정이나 API 토큰의 범위와 역할(RBAC)을 그대로 따르고, 봉인된 변수 값은 돌려주지 않습니다.")}
      />

      <Card title={t("MCP 주소")} description={t("Streamable HTTP(무상태). 인증은 Authorization: Bearer 헤더의 Aven API 토큰입니다.")}>
        <CopyField value={endpoint} label={t("MCP 주소")} />
      </Card>

      <Card title={t("연결 방식")}>
        <div className="space-y-3">
        <Segmented<Mode>
          label={t("연결 방식")}
          value={mode}
          onChange={setMode}
          options={[
            { value: "cli", label: t("Aven CLI (기본)") },
            { value: "http", label: t("HTTP + API 토큰") },
          ]}
        />
        <p className="text-sm text-gray-600">
          {mode === "cli"
            ? tr("에디터가 {mcp}를 실행하고, CLI가 {login} 세션으로 요청마다 인증합니다. 설정 파일에 비밀 값이 남지 않습니다. 프로젝트 디렉터리에서 {install}을 실행하면 아래 설정을 자동으로 추가합니다. 권한을 좁히려면 {token}에 범위를 정한 API 토큰을 넣으세요.", {
                mcp: <code className="font-mono">aven mcp</code>,
                login: <code className="font-mono">aven login</code>,
                install: <code className="font-mono">aven mcp install</code>,
                token: <code className="font-mono">AVEN_TOKEN</code>,
              })
            : env
              ? tr("CLI 없이 에디터가 {endpoint}에 직접 연결합니다. 이 프로젝트의 {env} 환경으로 범위를 정한 API 토큰을 만들어 {token} 환경 변수에 넣으세요. 토큰은 설정 파일에 직접 적지 마세요.", {
                  endpoint: <code className="font-mono">/mcp</code>,
                  env,
                  token: <code className="font-mono">AVEN_TOKEN</code>,
                })
              : tr("CLI 없이 에디터가 {endpoint}에 직접 연결합니다. 이 프로젝트로 범위를 정한 API 토큰을 만들어 {token} 환경 변수에 넣으세요. 토큰은 설정 파일에 직접 적지 마세요.", {
                  endpoint: <code className="font-mono">/mcp</code>,
                  token: <code className="font-mono">AVEN_TOKEN</code>,
                })}
        </p>
        {mode === "http" && <Snippet value={tokenCmd} label={t("토큰 생성 명령 복사")} />}
        </div>
      </Card>

      <Card
        title="Claude Code"
        description={tr("명령으로 추가하거나, 프로젝트의 {file}에 넣으세요.", {
          file: <code className="font-mono">.mcp.json</code>,
        })}
      >
        <Snippet value={claude} label={t("Claude Code 명령 복사")} />
        <Snippet value={claudeJson} label={t("Claude Code 설정 복사")} />
      </Card>

      <Card
        title="Cursor"
        description={tr("프로젝트의 {file}(전체 적용은 {globalFile})에 추가하세요.", {
          file: <code className="font-mono">.cursor/mcp.json</code>,
          globalFile: <code className="font-mono">~/.cursor/mcp.json</code>,
        })}
      >
        <Snippet value={cursor} label={t("Cursor 설정 복사")} />
      </Card>

      <Card
        title="VS Code"
        description={tr("프로젝트의 {file}에 추가하세요.", {
          file: <code className="font-mono">.vscode/mcp.json</code>,
        })}
      >
        <Snippet value={vscode} label={t("VS Code 설정 복사")} />
      </Card>

      <Card title={t("보안")}>
        <ul className="list-disc space-y-1 pl-5 text-sm text-gray-600 marker:text-gray-400">
          <li>{t("배포·재배포·롤백·재시작·변수 변경 도구는 파괴적 작업으로 표시되어, 이를 따르는 클라이언트는 실행 전에 확인을 받습니다.")}</li>
          <li>{t("에이전트에게는 필요한 프로젝트·환경과 역할(viewer 또는 member)로 범위를 정한 토큰을 주세요. viewer 토큰은 조회만 할 수 있습니다.")}</li>
          <li>
            {tr("토큰은 {link}에서 만들고 언제든 폐기할 수 있습니다.", {
              link: (
                <Link href="/account/?section=tokens" className="text-pink-600 hover:underline">
                  {t("계정 설정 → API 토큰")}
                </Link>
              ),
            })}
          </li>
        </ul>
      </Card>
    </>
  );
}
