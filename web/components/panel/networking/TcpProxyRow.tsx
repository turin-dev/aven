"use client";

/* TCP proxy row in the public networking list (modeled on Railway's dashboard: "host:port" + "→ Port N", copy/edit/delete) with the
   sliding edit panel that changes the container port behind the same public port and the client IP allowlist. */

import { useState } from "react";
import { Plug, SquarePen, Trash2 } from "lucide-react";
import { api, type CanvasService, type DetectedPort, type Environment, type TcpProxyView } from "@/lib/api";
import { t } from "@/lib/i18n";
import { ErrorBox, cx } from "../../ui";
import { PortPicker, initialPortChoice, type PortChoice } from "./PortPicker";
import { CopyBtn, IconBtn, INPUT, PanelFooter, PortMeta, Sliding, portOk, useNetRun } from "./parts";
import { TCP_ALLOW_MAX, parseTcpAllow } from "./validate";

const ALL = "0.0.0.0/0";
const sameList = (a: string[], b: string[]) => a.length === b.length && a.every((x, i) => x === b[i]);

export const tcpAddress = (t: Pick<TcpProxyView, "host" | "public_port">) => `${t.host.includes(":") ? `[${t.host}]` : t.host || "—"}:${t.public_port ?? "—"}`;

export function TcpProxyRow({
  svc,
  env,
  proxy,
  detected,
  servicePort,
  onDelete,
}: {
  svc: CanvasService;
  env: Environment;
  proxy: TcpProxyView;
  detected: DetectedPort[];
  servicePort: number;
  onDelete: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const addr = tcpAddress(proxy);
  return (
    <>
      <div className="group flex w-full items-center px-4 py-3 first:rounded-t-lg last:rounded-b-lg hover:bg-secondaryBg dark:hover:bg-gray-100">
        <div className="mr-3">
          <span className="icon-container icon-md text-2xl text-gray-500" aria-hidden="true">
            <Plug />
          </span>
        </div>
        <div className="grid min-w-0 flex-1 gap-1">
          <span className="block truncate text-left text-sm" title={addr}>
            {addr}
          </span>
          <PortMeta port={proxy.container_port ?? "—"}>
            {" · "}{t("TCP 프록시")}
            {!proxy.available && proxy.reason ? <span className="text-yellow-600"> · {proxy.reason}</span> : null}
          </PortMeta>
          <AllowSummary proxy={proxy} />
        </div>
        <div className="ml-auto flex flex-shrink-0 space-x-1">
          <CopyBtn value={addr} title={t("주소 복사")} />
          <IconBtn title={t("수정")} pressed={editing} onClick={() => setEditing((e) => !e)}>
            <SquarePen />
          </IconBtn>
          <IconBtn title={t("TCP 프록시 삭제")} onClick={onDelete}>
            <Trash2 />
          </IconBtn>
        </div>
      </div>
      <Sliding open={editing}>
        <TcpEditForm svc={svc} env={env} proxy={proxy} detected={detected} servicePort={servicePort} onDone={() => setEditing(false)} />
      </Sliding>
    </>
  );
}

function TcpEditForm({
  svc,
  env,
  proxy,
  detected,
  servicePort,
  onDone,
}: {
  svc: CanvasService;
  env: Environment;
  proxy: TcpProxyView;
  detected: DetectedPort[];
  servicePort: number;
  onDone: () => void;
}) {
  const [port, setPort] = useState<PortChoice>(() => initialPortChoice(proxy.container_port, detected));
  const supported = proxy.allow_cidrs !== undefined; // older servers do not know allowlists
  const own = proxy.allow_cidrs ?? [];
  const [allowText, setAllowText] = useState(() => own.join("\n"));
  const { run, busy, error } = useNetRun(svc, env);
  const { list: allow, bad } = parseTcpAllow(allowText);
  const tooMany = allow.length > TCP_ALLOW_MAX;
  const allowOk = bad.length === 0 && !tooMany;
  const portChanged = portOk(port.text) && Number(port.text) !== proxy.container_port;
  const allowChanged = supported && allowOk && !sameList(allow, own);
  const canSubmit = (portChanged || allowChanged) && allowOk;
  const defaults = proxy.default_allow_cidrs ?? [];
  const problem = bad.length
    ? t("IPv4 주소나 CIDR이 아닙니다: {entries}", { entries: bad.slice(0, 3).join(", ") })
    : tooMany
      ? t("허용 대역은 {max}개까지 넣을 수 있습니다", { max: TCP_ALLOW_MAX })
      : null;
  return (
    <form
      className="space-y-4"
      onSubmit={async (e) => {
        e.preventDefault();
        if (!canSubmit) return;
        const body = { ...(portChanged ? { container_port: Number(port.text) } : {}), ...(allowChanged ? { allow_cidrs: allow } : {}) };
        if (await run("save", () => api.patchTcpProxy(svc.id, env.id, body))) onDone();
      }}
    >
      <PortPicker id="tcp-edit" value={port} onChange={setPort} detected={detected} servicePort={servicePort} />
      {supported ? (
        <div>
          <label htmlFor="tcp-allow" className="mb-1.5 block text-sm font-medium text-foreground">
            {t("접속을 허용할 IP")}
          </label>
          <textarea
            id="tcp-allow"
            value={allowText}
            onChange={(e) => setAllowText(e.target.value)}
            rows={3}
            spellCheck={false}
            autoComplete="off"
            placeholder={"203.0.113.7\n10.0.0.0/8"}
            aria-invalid={problem ? true : undefined}
            aria-describedby="tcp-allow-help"
            className={cx(INPUT, "resize-y font-mono text-sm", problem && "!border-red-500")}
          />
          <p id="tcp-allow-help" className={cx("mt-1.5 text-xs leading-[18px]", problem ? "text-red-500" : "text-gray-500")} aria-live="polite">
            {problem ??
              (defaults.length
                ? t("IPv4 주소나 CIDR을 한 줄에 하나씩 적습니다. 비우면 서버 기본값({list})을 따르고, 0.0.0.0/0은 모든 IP를 허용합니다.", { list: defaults.join(", ") })
                : t("IPv4 주소나 CIDR을 한 줄에 하나씩 적습니다. 비우면 모든 IP가 접속할 수 있습니다."))}
          </p>
        </div>
      ) : null}
      {error ? <ErrorBox error={error} /> : null}
      <PanelFooter message={t("공개 포트는 그대로 두고 연결할 컨테이너 포트와 허용 IP를 바꿉니다")} onCancel={onDone} canSubmit={canSubmit} busy={busy === "save"} />
    </form>
  );
}

/** Who may connect: the open-to-everyone warning, or the ranges the forwarder admits. */
function AllowSummary({ proxy }: { proxy: TcpProxyView }) {
  const eff = proxy.effective_allow_cidrs ?? [];
  if (!eff.length) {
    return (
      <p className="text-xs leading-[18px] text-gray-500">
        {t("인터넷 어디서나 이 주소로 접속할 수 있습니다. HTTP가 아닌 TCP(데이터베이스 등)를 그대로 전달하므로 서비스 자체의 비밀번호와 TLS로 보호하세요.")}
        {proxy.allow_cidrs !== undefined && !(proxy.allow_cidrs.length === 1 && proxy.allow_cidrs[0] === ALL)
          ? ` ${t("수정에서 접속을 허용할 IP를 정할 수 있습니다.")}`
          : null}
      </p>
    );
  }
  const fromDefault = !(proxy.allow_cidrs ?? []).length;
  return (
    <p className="text-xs leading-[18px] text-gray-500">
      <span className="text-success">{t("허용된 IP만 접속할 수 있습니다")}</span>
      {fromDefault ? ` (${t("서버 기본값")})` : null}
      {": "}
      <span className="font-mono">{eff.join(", ")}</span>
    </p>
  );
}
