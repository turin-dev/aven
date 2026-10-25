"use client";

/* Private networking, modeled on Railway's dashboard: endpoint row (green check, `<name>.<env>.keel.internal` + "IPv4 & IPv6" pill,
   hover copy/edit, and a rename action) with the sliding rename panel
   (dnsName input + suffix, live "Endpoint name available!"). keel's cross-node mesh ports sit below the list and only
   show when the environment runs on two or more nodes. */

import { useEffect, useMemo, useState } from "react";
import { Check, CheckCircle2, Globe, Loader2, SquarePen, X } from "lucide-react";
import type { CanvasService, Environment, InstanceNetworking, ServiceInstance } from "@/lib/api";
import type { SettingsPatch } from "@/lib/staged";
import { useCanvas, usePrivateDnsAvailable } from "@/lib/queries";
import { t } from "@/lib/i18n";
import { Input } from "../../ui";
import { Changed, Hint } from "../settings/primitives";
import { CopyBtn, IconBtn, NetSection, PanelFooter, Sliding, SuffixInput } from "./parts";

type Stage = { set: <K extends keyof SettingsPatch>(k: K, v: SettingsPatch[K]) => void; changed: (k: keyof SettingsPatch) => boolean };

const NAME_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;
const parsePorts = (t: string) => [...new Set(t.split(/[ ,]+/).filter(Boolean).map(Number))].filter((p) => p >= 1 && p <= 65535).slice(0, 16);

export function PrivateNetworking({
  svc,
  env,
  inst,
  draft,
  view,
  stage,
}: {
  svc: CanvasService;
  env: Environment;
  inst: ServiceInstance;
  draft: ServiceInstance;
  view: InstanceNetworking;
  stage: Stage;
}) {
  const [editing, setEditing] = useState(false);
  const name = draft.private_dns_name || svc.name;
  const suffix = view.private.fqdn.startsWith(`${view.private.dns_name}.`) ? view.private.fqdn.slice(view.private.dns_name.length) : `.${env.name}.keel.internal`;
  const fqdn = `${name}${suffix}`;
  const v4 = view.private.ipv4 ?? [];
  const v6 = view.private.ipv6 ?? [];
  const family = v4.length && !v6.length ? "IPv4" : v6.length && !v4.length ? "IPv6" : "IPv4 & IPv6";
  const addrs = [...v4, ...v6];
  const canvas = useCanvas(env.id);
  const nodeCount = useMemo(() => {
    const ids = new Set<string>();
    for (const s of canvas.data?.services ?? []) {
      const i = s.instance;
      if (!i) continue;
      ids.add(i.node_id);
      for (const p of i.placement ?? []) ids.add(p.node_id);
    }
    return ids.size;
  }, [canvas.data]);

  return (
    <NetSection
      id="networking-private"
      title={t("비공개 네트워킹")}
      keywords={`private internal ipv6 ipv4 dns endpoint mesh node ${t("노드 간 포트 엔드포인트")}`}
      badge={<Changed on={stage.changed("private_dns_name") || stage.changed("private_ports")} />}
      description={t("aven 비공개 네트워크 안에서 이 서비스와 통신합니다.")}
    >
      <div className="flex flex-col space-y-4">
        <div className="divide-y divide-gray-200 rounded-lg border border-gray-200">
          <div className="group flex items-center py-3 pl-4 pr-1.5 first:rounded-t-lg last:rounded-b-lg hover:bg-secondaryBg dark:hover:bg-gray-100">
            <div className="mr-3">
              <span className="icon-container icon-md text-2xl text-green-600" aria-hidden="true">
                <CheckCircle2 />
              </span>
            </div>
            <div className="flex min-w-0 flex-grow flex-col space-y-1">
              <div className="relative flex w-full min-w-0 items-center space-x-0 pr-4">
                <div className="flex min-w-0 flex-1 items-center gap-1.5">
                  <span className="truncate" title={fqdn}>
                    {fqdn}
                  </span>
                  <div
                    className="flex shrink-0 cursor-help items-center gap-1 whitespace-nowrap rounded bg-pink-100 px-0.5 py-0.5 text-xs font-semibold text-pink-600"
                    title={`${t("같은 노드 안에서는 IPv4와 IPv6로 접속합니다. 다른 노드의 서비스와는 IPv4로만 연결됩니다.")}${addrs.length ? `\n${addrs.join("\n")}` : ""}`}
                  >
                    <span aria-hidden="true" className="icon-container icon-xs text-lg text-current">
                      <Globe />
                    </span>
                    {family}
                  </div>
                </div>
                <div className="pointer-events-none absolute right-0 top-1/2 flex -translate-y-1/2 items-center space-x-1 opacity-0 group-focus-within:pointer-events-auto group-focus-within:opacity-100 group-hover:pointer-events-auto group-hover:opacity-100">
                  <CopyBtn value={fqdn} small />
                  <IconBtn title={t("수정")} small pressed={editing} onClick={() => setEditing((e) => !e)}>
                    <SquarePen />
                  </IconBtn>
                </div>
              </div>
              <p className="text-xs font-medium text-gray-600" />
              <div className="flex flex-wrap items-center space-x-0">
                <span>{t("비공개 통신 준비됨")} · </span>
                <div className="pr-0.5" />
                <div className="flex min-w-0 flex-wrap items-center gap-0.5">
                  <span className="shrink-0 text-pink-600">{t("이렇게만 불러도 됩니다")}</span>
                  <span className="min-w-0 truncate rounded-md border border-gray-100 bg-pink-100 px-[3px] font-mono text-xs text-pink-600">{name}</span>
                </div>
              </div>
              <p />
            </div>
          </div>
          <Sliding open={editing}>
            <RenameForm
              svc={svc}
              env={env}
              current={name}
              serverName={view.private.dns_name}
              suffix={suffix}
              onDone={() => setEditing(false)}
              onSave={(n) => {
                stage.set("private_dns_name", n === svc.name ? null : n);
                setEditing(false);
              }}
            />
          </Sliding>
        </div>
        {nodeCount >= 2 && <MeshPorts inst={inst} draft={draft} stage={stage} />}
      </div>
    </NetSection>
  );
}

function RenameForm({
  svc,
  env,
  current,
  serverName,
  suffix,
  onDone,
  onSave,
}: {
  svc: CanvasService;
  env: Environment;
  current: string;
  serverName: string;
  suffix: string;
  onDone: () => void;
  onSave: (name: string) => void;
}) {
  const [text, setText] = useState(current);
  const [debounced, setDebounced] = useState(current);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(text.trim()), 300);
    return () => clearTimeout(timer);
  }, [text]);
  const n = text.trim();
  const valid = NAME_RE.test(n);
  // The service's own names are always available to it.
  const own = n === serverName || n === svc.name;
  const check = usePrivateDnsAvailable(svc.id, env.id, debounced, valid && !own && debounced === n);
  const changed = n !== current;
  let tone: "info" | "ok" | "bad" = "info";
  let icon = undefined as React.ReactNode;
  let msg: React.ReactNode = t("같은 환경의 다른 서비스가 이 이름으로 접속합니다");
  if (!n) msg = t("엔드포인트 이름을 입력하세요");
  else if (!valid) (tone = "bad"), (icon = <X />), (msg = t("소문자, 숫자, 하이픈(-)만 쓸 수 있고 하이픈으로 시작하거나 끝날 수 없습니다"));
  else if (!changed) msg = t("같은 환경의 다른 서비스가 이 이름으로 접속합니다");
  else if (own) (tone = "ok"), (icon = <Check />), (msg = t("사용 가능한 이름입니다"));
  else if (debounced !== n || check.isFetching) (icon = <Loader2 className="animate-spin" />), (msg = t("확인 중…"));
  else if (check.data?.available) (tone = "ok"), (icon = <Check />), (msg = t("사용 가능한 이름입니다"));
  else if (check.data) (tone = "bad"), (icon = <X />), (msg = check.data.reason ?? t("이미 쓰는 이름입니다"));
  else if (check.error) (tone = "bad"), (icon = <X />), (msg = t("사용 가능 여부를 확인하지 못했습니다"));
  const ok = changed && valid && (own || (debounced === n && !!check.data?.available));
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (ok) onSave(n);
      }}
    >
      <SuffixInput label="DNS" name="dnsName" value={text} onChange={(v) => setText(v.toLowerCase())} suffix={suffix} pl4 autoFocus invalid={!!n && !valid} className="mb-4" />
      <PanelFooter message={msg} tone={tone} icon={icon} onCancel={onDone} canSubmit={ok} />
    </form>
  );
}

/** aven mesh: ports opened to services on other nodes (was "다른 노드에 여는 포트"). Applies without a redeploy. */
function MeshPorts({ inst, draft, stage }: { inst: ServiceInstance; draft: ServiceInstance; stage: Stage }) {
  const value = draft.private_ports ?? [];
  const key = JSON.stringify(value);
  const [text, setText] = useState(value.join(", "));
  useEffect(() => {
    setText((cur) => (JSON.stringify(parsePorts(cur)) === key ? cur : value.join(", ")));
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <div>
      <p className="mb-3 flex items-center gap-2 text-sm font-semibold">
        {t("노드 간 포트")}
        <Changed on={stage.changed("private_ports")} />
      </p>
      <Input
        size="sm"
        mono
        value={text}
        placeholder={String(draft.port ?? inst.port ?? "PORT")}
        onChange={(e) => {
          const raw = e.target.value.replace(/[^0-9, ]/g, "");
          setText(raw);
          stage.set("private_ports", parsePorts(raw));
        }}
        className="px-3 tabular-nums"
        aria-label={t("노드 간 포트")}
      />
      <Hint>
        {t("이 환경은 여러 노드에서 실행됩니다. 다른 노드의 서비스가 이 서비스에 접속할 포트를 쉼표로 구분해 적으세요(최대 16개). 비워 두면 포트(PORT) 하나만 엽니다. 같은 노드 안에서는 모든 포트가 열려 있고, 변경 사항을 적용하면 재배포 없이 바로 반영됩니다.")}
      </Hint>
    </div>
  );
}
