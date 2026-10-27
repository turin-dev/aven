"use client";

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Box, CheckCircle2, Cpu, Globe, HardDrive, Loader2, MemoryStick, MoreVertical, Network, Pencil, Plus, Server, Tag, Trash2 } from "lucide-react";
import { api, type JoinToken, type KeelNode, type NodeIngressMode, type NodeIPv6Status } from "@/lib/api";
import { qk, useMe, useNodeMetrics, useNodes } from "@/lib/queries";
import { lock, NO_PERM } from "../Perm";
import { fmtBytes, fmtRelative, fmtTime } from "@/lib/format";
import { t, tr } from "@/lib/i18n";
import { Button, CopyButton, Dialog, EmptyState, ErrorBox, Field, IconButton, Input, Menu, MenuItem, RowList, Select, Spinner, Textarea, Toggle, cx } from "../ui";

const NAME_RE = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;
// 노드 단위 시계열은 서버가 아직 저장하지 않는다(GET /nodes/{nid}/metrics → 501). mock 미리보기에서만 그린다.
const NODE_METRICS = process.env.NODE_ENV === "development" && process.env.NEXT_PUBLIC_KEEL_MOCK === "1";

/** What a remote node cannot do yet (kept in sync with docs/api-v1.md "v1.2 deviations"). */
function RemoteLimits({ className }: { className?: string }) {
  return (
    <div className={cx("rounded-md bg-gray-50 px-3.5 py-3 text-xs leading-relaxed text-gray-500", className)}>
      <div className="mb-1 font-semibold text-gray-600">{t("원격 노드에서 아직 안 되는 것")}</div>
      <ul className="list-disc space-y-0.5 pl-4">
        <li>{t("TCP 프록시, 유휴 시 잠자기(sleep), 볼륨 백업·복원")}</li>
        <li>{t("HTTP 접근 로그와 노드 지표(서비스별 지표와 로그는 됩니다)")}</li>
        <li>{t("다른 노드에 있는 서비스와의 비공개 네트워크 연결")}</li>
        <li>{t("노드를 옮겨도 볼륨 데이터는 옮겨지지 않습니다(새 노드에서 빈 볼륨으로 시작)")}</li>
      </ul>
    </div>
  );
}

export const NODE_STATUS: Record<KeelNode["status"], { label: string; dot: string; text: string }> = {
  online: { label: t("온라인"), dot: "bg-green-500", text: "text-success" },
  offline: { label: t("오프라인"), dot: "bg-red-500", text: "text-danger" },
  draining: { label: t("비우는 중"), dot: "bg-yellow-500 animate-soft-pulse", text: "text-yellow-600" },
};

/** Tiny line chart without axes for list rows. */
export function Sparkline({ values, max, color = "var(--pink-600)", label }: { values: number[]; max?: number; color?: string; label: string }) {
  const w = 96;
  const h = 26;
  if (values.length < 2) return <span className="inline-block h-[26px] w-[96px]" aria-hidden="true" />;
  const top = Math.max(max ?? 0, ...values) || 1;
  const pts = values.map((v, i) => `${((i / (values.length - 1)) * w).toFixed(1)},${(h - 2 - (v / top) * (h - 4)).toFixed(1)}`).join(" ");
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} role="img" aria-label={label} className="shrink-0">
      <polyline points={`0,${h} ${pts} ${w},${h}`} fill={color} fillOpacity={0.12} stroke="none" />
      <polyline points={pts} fill="none" stroke={color} strokeWidth={1.25} strokeLinejoin="round" />
    </svg>
  );
}

function addr(n: KeelNode) {
  return n.address.ipv6 ?? n.address.ipv4 ?? n.address.private ?? t("주소 없음");
}

/* ── add node ───────────────────────────────────────────────────────── */

function useCountdown(until: string | null) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!until) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [until]);
  if (!until) return null;
  const s = Math.max(0, Math.round((Date.parse(until) - now) / 1000));
  return { s, text: `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}` };
}

export function AddNodeDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const nodes = useNodes(open);
  const [name, setName] = useState("");
  const [tok, setTok] = useState<JoinToken | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const known = useRef<Set<string> | null>(null);
  const left = useCountdown(tok?.expires_at ?? null);

  const joined = useMemo(() => {
    if (!tok || !known.current) return null;
    return (nodes.data ?? []).find((n) => !known.current!.has(n.id) && n.status === "online" && (!name.trim() || n.name === name.trim())) ?? null;
  }, [nodes.data, tok, name]);

  const close = () => {
    setTok(null);
    setName("");
    setError(null);
    known.current = null;
    onClose();
  };

  const generate = async () => {
    if (busy) return;
    if (name.trim() && !NAME_RE.test(name.trim())) return setError(t("이름에는 영문 소문자, 숫자, 하이픈(-)만 쓸 수 있습니다."));
    setBusy(true);
    setError(null);
    try {
      const snapshot = await api.nodes();
      known.current = new Set(snapshot.map((n) => n.id));
      qc.setQueryData(qk.nodes, snapshot);
      setTok(await api.createJoinToken({ name: name.trim() || undefined }));
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  const expired = left !== null && left.s === 0 && !joined;

  return (
    <Dialog
      open={open}
      onClose={() => !busy && close()}
      title={t("노드 추가")}
      wide
      footer={
        !tok ? (
          <>
            <Button variant="ghost" onClick={close} disabled={busy}>
              {t("취소")}
            </Button>
            <Button variant="primary" loading={busy} onClick={generate}>
              {t("설치 명령 만들기")}
            </Button>
          </>
        ) : (
          <Button
            disabled={busy}
            variant={joined ? "primary" : "ghost"}
            onClick={() => {
              qc.invalidateQueries({ queryKey: qk.nodes });
              close();
            }}
          >
            {joined ? t("완료") : t("닫기")}
          </Button>
        )
      }
    >
      <div className="space-y-5">
        {!tok ? (
          <>
            <p>
              {t("다른 서버를 노드로 연결하면 서비스를 그 서버에 배포할 수 있습니다. 새 서버에서 실행할 설치 명령을 만들어 드립니다. Docker와 systemd가 있는 Linux(amd64, arm64)가 필요하고, 그 서버에서 이 Aven 서버로 HTTPS 연결이 되어야 합니다. 노드의 keel-proxy가 그 서버의 80/443 포트를 씁니다.")}
            </p>
            <RemoteLimits />
            <Field label={t("노드 이름")} hint={t("비워 두면 서버의 hostname을 씁니다.")}>
              <Input mono autoFocus value={name} onChange={(e) => setName(e.target.value.toLowerCase())} placeholder="hel-2" onKeyDown={(e) => e.key === "Enter" && generate()} />
            </Field>
            {error ? <ErrorBox error={error} /> : null}
          </>
        ) : (
          <>
            <ol className="space-y-5">
              <li>
                <StepTitle n={1}>{t("새 서버에서 이 명령을 실행하세요 (sudo 권한 필요)")}</StepTitle>
                <div className="relative rounded-md border border-gray-200 bg-background">
                  <pre className="max-h-[120px] overflow-auto whitespace-pre-wrap break-all px-3.5 py-3 pr-12 font-mono text-xs leading-relaxed text-foreground">{tok.command}</pre>
                  <CopyButton value={tok.command} label={t("설치 명령 복사")} className="absolute right-1.5 top-1.5" />
                </div>
                <div className={cx("mt-2 text-xs", expired ? "text-danger" : "text-gray-500")}>
                  {expired ? (
                    <>
                      {tr("토큰이 만료되었습니다. {action}", {
                        action: (
                          <button type="button" onClick={generate} disabled={busy} className="cursor-pointer underline underline-offset-2 hover:text-foreground disabled:opacity-50">
                            {t("새로 만들기")}
                          </button>
                        ),
                      })}
                    </>
                  ) : (
                    <>
                      {tr("명령에 한 번만 쓸 수 있는 가입 토큰이 들어 있습니다. 다른 사람과 공유하지 마세요 · {left} 후 만료 ({time})", {
                        left: <span className="tabular-nums">{left?.text}</span>,
                        time: fmtTime(tok.expires_at),
                      })}
                    </>
                  )}
                </div>
              </li>
              <li>
                <StepTitle n={2}>{t("노드가 연결되기를 기다리는 중")}</StepTitle>
                {joined ? (
                  <div className="flex items-center gap-3 rounded-md bg-success-surface px-3.5 py-3 text-sm">
                    <CheckCircle2 className="size-5 shrink-0 text-success" />
                    <div className="min-w-0 flex-1">
                      <div className="font-semibold text-foreground">{t("{name} 연결됨", { name: joined.name })}</div>
                      <div className="text-gray-600">
                        {tr("{cores}코어 · {memory} 메모리 · {addr}", {
                          cores: joined.cpu_cores,
                          memory: fmtBytes(joined.memory_bytes),
                          addr: <span className="font-mono text-xs">{addr(joined)}</span>,
                        })}
                      </div>
                    </div>
                  </div>
                ) : (
                  <div className="flex items-center gap-3 rounded-md bg-gray-50 px-3.5 py-3 text-sm text-gray-600">
                    <Loader2 className="size-4 shrink-0 animate-spin text-pink-600" />
                    <span>
                      {tr("에이전트가 서버에 접속하면 자동으로 표시됩니다. 문제가 있으면 새 서버에서 {cmd}를 확인하세요.", {
                        cmd: <code className="font-mono text-xs">journalctl -u aven-agent</code>,
                      })}
                    </span>
                  </div>
                )}
              </li>
            </ol>
            {error || nodes.error ? <ErrorBox error={error ?? nodes.error} /> : null}
          </>
        )}
      </div>
    </Dialog>
  );
}

function StepTitle({ n, children }: { n: number; children: ReactNode }) {
  return (
    <div className="mb-2 flex items-center gap-2 text-sm font-medium text-foreground">
      <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-gray-100 text-xs text-gray-600">{n}</span>
      {children}
    </div>
  );
}

/* ── edit dialogs ───────────────────────────────────────────────────── */

function RenameNode({ node, onClose }: { node: KeelNode | null; onClose: () => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState(node?.name ?? "");
  const [prev, setPrev] = useState(node);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  if (prev !== node) {
    setPrev(node);
    setName(node?.name ?? "");
    setError(null);
  }
  const save = async () => {
    if (!node) return;
    if (!NAME_RE.test(name.trim())) return setError(t("이름에는 영문 소문자, 숫자, 하이픈(-)만 쓸 수 있습니다."));
    setBusy(true);
    try {
      await api.updateNode(node.id, { name: name.trim() });
      await qc.invalidateQueries({ queryKey: qk.nodes });
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={!!node}
      onClose={onClose}
      title={t("노드 이름 바꾸기")}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("취소")}
          </Button>
          <Button variant="primary" loading={busy} onClick={save}>
            {t("저장")}
          </Button>
        </>
      }
    >
      <div className="space-y-3 pt-1">
        <Input mono autoFocus value={name} onChange={(e) => setName(e.target.value.toLowerCase())} onKeyDown={(e) => e.key === "Enter" && save()} aria-label={t("노드 이름")} />
        {error ? <ErrorBox error={error} /> : null}
      </div>
    </Dialog>
  );
}

function LabelsNode({ node, onClose }: { node: KeelNode | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toText = (l: Record<string, string>) =>
    Object.entries(l)
      .map(([k, v]) => `${k}=${v}`)
      .join("\n");
  const [text, setText] = useState(toText(node?.labels ?? {}));
  const [prev, setPrev] = useState(node);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  if (prev !== node) {
    setPrev(node);
    setText(toText(node?.labels ?? {}));
    setError(null);
  }
  const save = async () => {
    if (!node) return;
    const labels: Record<string, string> = {};
    for (const line of text.split("\n").map((l) => l.trim()).filter(Boolean)) {
      const i = line.indexOf("=");
      if (i <= 0) return setError(t("\"{line}\": 키=값 형식으로 적으세요.", { line }));
      labels[line.slice(0, i).trim()] = line.slice(i + 1).trim();
    }
    setBusy(true);
    try {
      await api.updateNode(node.id, { labels });
      await qc.invalidateQueries({ queryKey: qk.nodes });
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={!!node}
      onClose={onClose}
      title={t("{name} 라벨", { name: node?.name ?? "" })}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("취소")}
          </Button>
          <Button variant="primary" loading={busy} onClick={save}>
            {t("저장")}
          </Button>
        </>
      }
    >
      <div className="space-y-3 pt-1">
        <p>{t("한 줄에 하나씩 키=값으로 적으세요. 노드를 구분하는 메모로 쓰입니다.")}</p>
        <Textarea mono rows={5} value={text} onChange={(e) => setText(e.target.value)} placeholder={"region=example-region\ndisk=nvme"} aria-label={t("라벨")} />
        {error ? <ErrorBox error={error} /> : null}
      </div>
    </Dialog>
  );
}

const HOST_RE = /^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/;

/** 공개 진입(ingress) 설정: 방식, CNAME 대상 이름, TCP 프록시 호스트. 로컬 노드는 서버 env(KEEL_INGRESS_*)가 있으면 그 값이 우선한다. */
function IngressNode({ node, onClose }: { node: KeelNode | null; onClose: () => void }) {
  const qc = useQueryClient();
  const [mode, setMode] = useState<NodeIngressMode>(node?.ingress_mode ?? "direct");
  const [host, setHost] = useState(node?.ingress_hostname ?? "");
  const [tcp, setTcp] = useState(node?.tcp_hostname ?? "");
  const [pool, setPool] = useState(node?.ipv6_pool ?? "");
  const [prev, setPrev] = useState(node);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  if (prev !== node) {
    setPrev(node);
    setMode(node?.ingress_mode ?? "direct");
    setHost(node?.ingress_hostname ?? "");
    setTcp(node?.tcp_hostname ?? "");
    setPool(node?.ipv6_pool ?? "");
    setError(null);
  }
  const save = async () => {
    if (!node) return;
    const h = host.trim().toLowerCase().replace(/\.$/, "");
    const tcpHost = tcp.trim().toLowerCase().replace(/\.$/, "");
    for (const v of [h, tcpHost]) if (v && !HOST_RE.test(v)) return setError(t("\"{host}\": 올바른 호스트 이름이 아닙니다.", { host: v }));
    const p = pool.trim().toLowerCase();
    const pm = /^([0-9a-f:]+)\/(\d{1,3})$/.exec(p);
    if (p && (!pm || !pm[1].includes(":") || +pm[2] < 64 || +pm[2] > 120)) return setError(t("\"{pool}\": IPv6 풀은 /64 ~ /120 IPv6 CIDR이어야 합니다.", { pool: p }));
    setBusy(true);
    try {
      // 풀은 바뀐 경우에만 보낸다(서버가 노드의 주소와 겹치는지 확인한다).
      await api.updateNode(node.id, { ingress_mode: mode, ingress_hostname: h, tcp_hostname: tcpHost, ...(p !== (node.ipv6_pool ?? "") ? { ipv6_pool: p } : {}) });
      await qc.invalidateQueries({ queryKey: qk.nodes });
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={!!node}
      onClose={onClose}
      title={t("{name} 공개 진입", { name: node?.name ?? "" })}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("취소")}
          </Button>
          <Button variant="primary" loading={busy} onClick={save}>
            {t("저장")}
          </Button>
        </>
      }
    >
      <div className="space-y-3 pt-1">
        <Field label={t("진입 방식")} hint={t("Cloudflare 터널 노드에 새로 만드는 도메인은 TLS 끔과 X-Forwarded-Proto 신뢰로 만들어집니다.")}>
          <Select value={mode} onChange={(e) => setMode(e.target.value as NodeIngressMode)} aria-label={t("진입 방식")}>
            <option value="direct">{t("직접(공인 IP)")}</option>
            <option value="cloudflare_tunnel">{t("Cloudflare 터널")}</option>
          </Select>
        </Field>
        <Field label={t("Ingress 호스트 이름")} hint={t("커스텀 도메인이 CNAME으로 가리킬 이름입니다. 비우면 A/AAAA 레코드를 안내합니다.")}>
          <Input mono value={host} onChange={(e) => setHost(e.target.value)} placeholder="main.edge.example.com" aria-label={t("Ingress 호스트 이름")} />
        </Field>
        <Field label={t("TCP 프록시 호스트")} hint={t("TCP 프록시 접속 주소의 호스트입니다. 비우면 기존 방식으로 정합니다.")}>
          <Input mono value={tcp} onChange={(e) => setTcp(e.target.value)} placeholder="tcp.example.com" aria-label={t("TCP 프록시 호스트")} />
        </Field>
        <Field
          label={t("IPv6 풀")}
          hint={t("도메인마다 이 대역에서 전용 공개 IPv6(/128)를 하나씩 배정해 노드 인터페이스에 붙입니다. /64 ~ /120, 노드의 기본 IPv6와 겹치면 안 됩니다. 비우면 모든 도메인이 노드 기본 IPv6를 같이 씁니다.")}
        >
          <Input mono value={pool} onChange={(e) => setPool(e.target.value)} placeholder="2001:db8:1:d::/80" aria-label={t("IPv6 풀")} />
        </Field>
        {node?.ipv6_status ? <IPv6PoolStatus st={node.ipv6_status} /> : null}
        {node?.is_local ? (
          <p className="text-xs text-gray-500">
            {t("로컬 노드는 서버 환경 변수(KEEL_INGRESS_MODE, KEEL_INGRESS_HOSTNAME, KEEL_TCP_PROXY_DOMAIN, KEEL_IPV6_POOL)가 있으면 그 값이 우선합니다.")}
          </p>
        ) : null}
        {error ? <ErrorBox error={error} /> : null}
      </div>
    </Dialog>
  );
}

/** 도메인별 IPv6 반영 상태 한 줄. */
function IPv6PoolStatus({ st }: { st: NodeIPv6Status }) {
  const text =
    st.supported === false
      ? t("이 노드에서는 주소를 붙이지 못합니다: {reason}", { reason: st.reason ?? t("지원하지 않음") })
      : st.supported === null
        ? t("아직 반영 전이거나 노드가 오프라인입니다.")
        : t("{iface}에 {present}/{desired}개 주소가 붙어 있습니다.", { iface: st.iface ?? "", present: st.present, desired: st.desired });
  return (
    <div className={cx("text-xs", st.supported === false || st.last_error ? "text-yellow-700" : "text-gray-500")} data-testid="ipv6-pool-status">
      <p>{text}</p>
      {st.last_error ? <p>{t("오류: {error}", { error: st.last_error })}</p> : null}
      {st.foreign?.length ? <p>{t("풀 안의 다른 주소(건드리지 않음): {addrs}", { addrs: st.foreign.join(", ") })}</p> : null}
    </div>
  );
}

function RemoveNode({ node, placed, onClose }: { node: KeelNode | null; placed?: number; onClose: () => void }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [prev, setPrev] = useState(node);
  if (prev !== node) {
    setPrev(node);
    setError(null);
  }
  const run = async () => {
    if (!node) return;
    setBusy(true);
    setError(null);
    try {
      await api.deleteNode(node.id);
      await qc.invalidateQueries({ queryKey: qk.nodes });
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["canvas"] }),
        qc.invalidateQueries({ queryKey: ["instance"] }),
        qc.invalidateQueries({ queryKey: ["resolved"] }),
      ]);
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const online = node?.status === "online";
  const blocked = (placed ?? 0) > 0 || online;
  return (
    <Dialog
      open={!!node}
      onClose={() => !busy && onClose()}
      title={t("{name} 노드 제거", { name: node?.name ?? "" })}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="danger" loading={busy} disabled={blocked} onClick={run}>
            {t("제거")}
          </Button>
        </>
      }
    >
      <div className="space-y-3 pt-1">
        <p>{t("노드의 인증 정보가 지워지고 이 노드에는 더 이상 배포하지 않습니다. 그 서버의 컨테이너와 keel-proxy는 직접 정리해야 합니다.")}</p>
        {(placed ?? 0) > 0 && (
          <p className="rounded-md bg-danger-surface px-3 py-2 text-sm text-danger">
            {t("이 노드에 배치된 서비스가 {count}개 있습니다. 먼저 그 서비스를 다른 노드로 옮기거나 삭제하세요.", { count: placed ?? 0 })}
          </p>
        )}
        {online && (
          <p className="rounded-md bg-gray-50 px-3 py-2 text-sm text-gray-600">
            {tr("에이전트가 연결되어 있습니다. 먼저 그 서버에서 {cmd}로 에이전트를 멈추세요.", {
              cmd: <code className="font-mono">sudo systemctl disable --now aven-agent</code>,
            })}
          </p>
        )}
        {error ? <ErrorBox error={error} /> : null}
      </div>
    </Dialog>
  );
}

/* ── list ───────────────────────────────────────────────────────────── */

const PEER_DOT = { ok: "bg-green-500", pending: "bg-gray-400", down: "bg-yellow-500" } as const;

/** Cross-node private networking (mesh gateway) status: listen address and per-peer health (mTLS ping every 30 s). */
function MeshLine({ n }: { n: KeelNode }) {
  const nodes = useNodes();
  const mesh = n.mesh;
  if (!mesh) return null;
  const name = (id: string) => nodes.data?.find((x) => x.id === id)?.name ?? id;
  if (!mesh.enabled) {
    return (
      <div className="flex items-center gap-1.5 text-xs text-gray-500" title={mesh.reason}>
        <Network className="size-3.5 shrink-0" aria-hidden />
        {t("노드 간 사설 네트워크 꺼짐")}{mesh.reason ? ` · ${mesh.reason}` : ""}
      </div>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-gray-500">
      <span className="flex items-center gap-1.5">
        <Network className="size-3.5 shrink-0" aria-hidden />
        {t("노드 간 사설 네트워크")} <span className="font-mono text-gray-600">{mesh.listen}</span>
      </span>
      {mesh.peers.map((p) => (
        <span key={p.node_id} className="flex items-center gap-1.5" title={p.error ?? (p.last_ok_at ? t("마지막 응답 {time}", { time: fmtTime(p.last_ok_at) }) : undefined)}>
          <span aria-hidden className={cx("size-1.5 rounded-full", PEER_DOT[p.status === "ok" ? "ok" : p.status === "pending" ? "pending" : "down"])} />
          <span className="text-gray-600">{name(p.node_id)}</span>
          <span className={cx(p.status === "ok" ? "tabular-nums" : p.status === "pending" ? "" : "text-warn")}>
            {p.status === "ok" && p.rtt_ms != null ? `${p.rtt_ms.toFixed(1)}ms` : p.status === "ok" ? t("연결됨") : p.status === "pending" ? t("확인 중") : t("연결 안 됨")}
          </span>
        </span>
      ))}
      {mesh.error ? (
        <span className="text-warn" title={mesh.error}>
          {t("오류")}
        </span>
      ) : null}
    </div>
  );
}

/** One metric cell in a node row: small label, value, optional sparkline. */
function Stat({ icon, label, value, sub, children }: { icon: ReactNode; label: string; value: ReactNode; sub?: ReactNode; children?: ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="flex items-center gap-1.5 text-xs text-gray-500">
        {icon}
        {label}
      </div>
      <div className="mt-1 truncate text-sm tabular-nums text-foreground">
        {value}
        {sub ? <span className="ml-1 text-xs text-gray-500">{sub}</span> : null}
      </div>
      {children ? <div className="mt-1">{children}</div> : null}
    </div>
  );
}

function NodeRow({
  n,
  placed,
  admin,
  onRename,
  onLabels,
  onIngress,
  onRemove,
}: {
  n: KeelNode;
  placed?: number;
  admin: boolean;
  onRename: () => void;
  onLabels: () => void;
  onIngress: () => void;
  onRemove: () => void;
}) {
  const qc = useQueryClient();
  const m = useNodeMetrics(n.id, "1h", NODE_METRICS);
  const pts = m.data?.points ?? [];
  const last = pts[pts.length - 1];
  const st = NODE_STATUS[n.status];
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const labels = Object.entries(n.labels);
  return (
    <li className="px-5 py-4">
      <div className="flex items-center gap-3">
        <span className="relative flex size-9 shrink-0 items-center justify-center rounded-md bg-gray-100 text-gray-600">
          <Server className="size-4" />
          <span className={cx("absolute -bottom-0.5 -right-0.5 size-2.5 rounded-full border-2 border-secondaryBg", st.dot)} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
            <span className="truncate font-medium">{n.name}</span>
            {n.is_local && <span className="rounded-md bg-pink-100 px-1.5 py-0.5 text-xs font-medium text-pink-600">{t("이 서버")}</span>}
            <span className={cx("flex items-center gap-1.5 text-xs", st.text)}>
              <span aria-hidden className={cx("size-1.5 rounded-full", st.dot)} />
              {st.label}
            </span>
            {!n.schedulable && <span className="rounded-md bg-gray-100 px-1.5 py-0.5 text-xs text-gray-500">{t("새 배포 안 받음")}</span>}
            {n.ingress_mode === "cloudflare_tunnel" && <span className="rounded-md bg-gray-100 px-1.5 py-0.5 text-xs text-gray-500">{t("Cloudflare 터널")}</span>}
          </div>
          <div className="mt-0.5 truncate font-mono text-xs text-gray-500" title={[n.address.ipv6, n.address.ipv4, n.address.private].filter(Boolean).join(" · ")}>
            {addr(n)}
            {n.address.private && <span className="text-gray-400"> · {t("사설 {address}", { address: n.address.private })}</span>}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <label className="flex items-center gap-2.5 pr-2 text-sm text-gray-600" title={admin ? t("끄면 새 배포가 이 노드로 가지 않습니다") : NO_PERM}>
            <span className="hidden sm:inline">{t("배포 받기")}</span>
            <Toggle
              label={t("{name} 새 배포 받기", { name: n.name })}
              checked={n.schedulable}
              disabled={busy || !admin}
              onChange={async (v) => {
                setBusy(true);
                setError(null);
                try {
                  await api.updateNode(n.id, { schedulable: v });
                  await qc.invalidateQueries({ queryKey: qk.nodes });
                } catch (e) {
                  setError(e);
                } finally {
                  setBusy(false);
                }
              }}
            />
          </label>
          <Menu
            label={t("{name} 작업", { name: n.name })}
            trigger={(p) => (
              <IconButton {...p} label={t("{name} 작업 더 보기", { name: n.name })}>
                <MoreVertical className="size-4" />
              </IconButton>
            )}
          >
            {(close) => (
              <>
                <MenuItem icon={<Pencil className="size-3.5" />} disabled={!admin} reason={NO_PERM} onSelect={() => (close(), onRename())}>
                  {t("이름 바꾸기")}
                </MenuItem>
                <MenuItem icon={<Tag className="size-3.5" />} disabled={!admin} reason={NO_PERM} onSelect={() => (close(), onLabels())}>
                  {t("라벨 편집")}
                </MenuItem>
                <MenuItem icon={<Globe className="size-3.5" />} disabled={!admin} reason={NO_PERM} onSelect={() => (close(), onIngress())}>
                  {t("공개 진입 설정")}
                </MenuItem>
                {!n.is_local && (
                  <>
                    <div className="my-2 h-px w-full bg-gray-100" />
                    <MenuItem danger icon={<Trash2 className="size-3.5" />} disabled={!admin} reason={NO_PERM} onSelect={() => (close(), onRemove())}>
                      {t("노드 제거")}
                    </MenuItem>
                  </>
                )}
              </>
            )}
          </Menu>
        </div>
      </div>

      <div className="mt-4 grid grid-cols-2 gap-x-6 gap-y-4 sm:ml-12 sm:grid-cols-4">
        <Stat icon={<Cpu className="size-3.5" />} label="CPU" value={last && n.cpu_cores > 0 ? `${Math.round(last.cpu_percent / n.cpu_cores)}%` : "—"} sub={t("/ {cores}코어", { cores: n.cpu_cores })}>
          {NODE_METRICS && <Sparkline values={pts.map((p) => p.cpu_percent)} max={n.cpu_cores * 100 * 0.25} label={t("{name} CPU", { name: n.name })} />}
        </Stat>
        <Stat icon={<MemoryStick className="size-3.5" />} label={t("메모리")} value={last ? fmtBytes(last.memory_bytes) : "—"} sub={`/ ${fmtBytes(n.memory_bytes)}`}>
          {NODE_METRICS && <Sparkline values={pts.map((p) => p.memory_bytes)} max={n.memory_bytes} color="var(--blue-500)" label={t("{name} 메모리", { name: n.name })} />}
        </Stat>
        <Stat icon={<HardDrive className="size-3.5" />} label={t("디스크 여유")} value={fmtBytes(n.disk_free_bytes)} />
        {placed !== undefined && <Stat icon={<Box className="size-3.5" />} label={t("인스턴스|count")} value={t("{count}개", { count: placed })} sub={t("이 프로젝트")} />}
      </div>

      <div className="mt-4 space-y-2 sm:ml-12">
        <div className="flex flex-wrap items-center gap-1.5 text-xs text-gray-500">
          {labels.map(([k, v]) => (
            <span key={k} className="rounded-md bg-gray-100 px-1.5 py-0.5 font-mono text-gray-600">
              {k}={v}
            </span>
          ))}
          <span className={cx(labels.length > 0 && "ml-1")}>
            aven <span className="font-mono">{n.version}</span> · Docker <span className="font-mono">{n.docker_version}</span>
            {n.status !== "online" && ` · ${t("마지막 응답 {time}", { time: fmtRelative(n.last_seen_at) })}`}
          </span>
        </div>
        <MeshLine n={n} />
        {error ? <ErrorBox error={error} /> : null}
        {m.error ? <p className="text-xs text-warn">{t("노드 지표를 가져오지 못했습니다.")}</p> : null}
      </div>
    </li>
  );
}

/** Node list with add/rename/labels/remove. `placedCount` maps node id → instances placed there. */
export function NodesView({ placedCount, titled = true }: { placedCount?: Record<string, number>; titled?: boolean }) {
  const nodes = useNodes();
  const admin = useMe().data?.instance_admin === true;
  const [adding, setAdding] = useState(false);
  const [renaming, setRenaming] = useState<KeelNode | null>(null);
  const [labeling, setLabeling] = useState<KeelNode | null>(null);
  const [ingressing, setIngressing] = useState<KeelNode | null>(null);
  const [removing, setRemoving] = useState<KeelNode | null>(null);
  const list = [...(nodes.data ?? [])].sort((a, b) => Number(b.is_local) - Number(a.is_local) || a.name.localeCompare(b.name));
  return (
    <section className="min-w-0">
      <div className="mb-4 flex flex-wrap items-start gap-x-6 gap-y-3">
        <div className="min-w-0 flex-1">
          <h3 className="text-[17px] font-semibold leading-6">
            {titled ? t("노드") : nodes.data ? t("노드 {count}개", { count: list.length }) : t("노드")}
            {titled && nodes.data ? <span className="ml-2 font-normal tabular-nums text-gray-500">{list.length}</span> : null}
          </h3>
          <p className="mt-1 text-sm text-gray-600">
            {t("서비스를 배포할 수 있는 서버입니다. 서비스마다 설정 › 배포에서 노드를 고릅니다. 두 노드 모두 사설 주소(Tailscale/WireGuard)가 있으면 다른 노드의 서비스도 같은 환경 안에서 비공개 주소로 연결됩니다(노드 간 사설 네트워크).")}
          </p>
        </div>
        <Button variant="primary" size="sm" onClick={() => setAdding(true)} {...lock(admin)}>
          <Plus className="size-3.5" /> {t("노드 추가")}
        </Button>
      </div>
      {nodes.error ? (
        <ErrorBox error={nodes.error} />
      ) : nodes.isPending ? (
        <Spinner />
      ) : list.length === 0 ? (
        <EmptyState icon={<Server className="size-6" />} title={t("노드가 없습니다")} />
      ) : (
        <RowList>
          {list.map((n) => (
            <NodeRow
              key={n.id}
              n={n}
              placed={placedCount ? placedCount[n.id] ?? 0 : undefined}
              admin={admin}
              onRename={() => setRenaming(n)}
              onLabels={() => setLabeling(n)}
              onIngress={() => setIngressing(n)}
              onRemove={() => setRemoving(n)}
            />
          ))}
        </RowList>
      )}
      <AddNodeDialog open={adding} onClose={() => setAdding(false)} />
      <RenameNode node={renaming} onClose={() => setRenaming(null)} />
      <LabelsNode node={labeling} onClose={() => setLabeling(null)} />
      <IngressNode node={ingressing} onClose={() => setIngressing(null)} />
      <RemoveNode node={removing} placed={removing && placedCount ? placedCount[removing.id] ?? 0 : undefined} onClose={() => setRemoving(null)} />
    </section>
  );
}
