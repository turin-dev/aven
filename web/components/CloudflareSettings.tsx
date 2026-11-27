"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Cloud, RefreshCw } from "lucide-react";
import { api, type CloudflareHost, type CloudflareMode, type CloudflareSettings as Settings, type CloudflareTest } from "@/lib/api";
import { qk, useNodes } from "@/lib/queries";
import { fmtRelative } from "@/lib/format";
import { toast } from "@/lib/uiState";
import { Badge, Button, Card, CenterSpinner, ErrorBox, Field, Input, Notice, RowList, Segmented, Textarea, Toggle, useConfirm } from "./ui";
import { SETUP_GUIDE_URL } from "./SetupChecklist";
import { t, tr } from "@/lib/i18n";

// Cloudflare 연동(owner 전용): API 토큰, 방식, 프록시, 관리할 존, 동기화 상태.
// 서버: GET/PUT /api/owner/cloudflare, POST …/test, …/sync, …/cleanup. 노드 위치(pool 좌표)는 노드 라벨 geo다.

const GUIDE = SETUP_GUIDE_URL + "#10-cloudflare-optional";

export function CloudflareSettings() {
  const q = useQuery({ queryKey: qk.cloudflare, queryFn: api.cloudflare, refetchInterval: (d) => (d.state.data?.status.syncing ? 3000 : 30_000) });
  if (q.isPending) return <CenterSpinner />;
  if (q.error) return <ErrorBox error={q.error} />;
  return (
    <div className="space-y-8">
      <Connection s={q.data!} />
      {q.data!.has_token && <Options s={q.data!} />}
      {q.data!.enabled && <SyncStatus s={q.data!} />}
      {q.data!.has_token && <Danger s={q.data!} />}
    </div>
  );
}

function useSave() {
  const qc = useQueryClient();
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const run = async (key: string, f: () => Promise<Settings | unknown>, done?: string) => {
    setBusy(key);
    setError(null);
    try {
      const v = await f();
      if (v && typeof v === "object" && "status" in v) qc.setQueryData(qk.cloudflare, v);
      else await qc.invalidateQueries({ queryKey: qk.cloudflare });
      if (done) toast(done, "success");
      return true;
    } catch (e) {
      setError(e);
      return false;
    } finally {
      setBusy(null);
    }
  };
  return { busy, error, run };
}

function TestResult({ r }: { r: CloudflareTest }) {
  if (!r.ok) return <ErrorBox error={r.error ?? t("토큰을 확인하지 못했습니다")} />;
  return (
    <div className="space-y-1 rounded-lg border border-gray-200 p-3 text-sm">
      <div>{t("볼 수 있는 존 {count}개: {names}", { count: r.zones.length, names: r.zones.map((z) => z.name).join(", ") || "—" })}</div>
      {r.load_balancing === "available"
        ? <div className="text-success">{t("Load Balancing을 쓸 수 있습니다. 여러 노드가 서비스하는 호스트는 가까운 노드로 보냅니다.")}</div>
        : <div className="text-warn">{t("Load Balancing을 쓸 수 없습니다. 여러 노드 호스트도 DNS 레코드(round-robin)로 만듭니다.")} <span className="text-xs text-gray-500">{r.load_balancing_error}</span></div>}
    </div>
  );
}

function Connection({ s }: { s: Settings }) {
  const { busy, error, run } = useSave();
  const [token, setToken] = useState("");
  const [test, setTest] = useState<CloudflareTest | null>(null);
  const [testing, setTesting] = useState(false);
  const doTest = async () => {
    setTesting(true);
    try {
      setTest(await api.testCloudflare(token.trim() || undefined));
    } catch (e) {
      setTest({ ok: false, error: e instanceof Error ? e.message : String(e), zones: [], load_balancing: "unavailable" });
    } finally {
      setTesting(false);
    }
  };
  return (
    <Card title={<span className="flex items-center gap-2"><Cloud className="size-4" />Cloudflare</span>}
      description={t("연동한 존 안의 서비스 도메인을, 그 서비스가 도는 노드로 Cloudflare에 맞춥니다. 여러 노드가 서비스하는 호스트는 Load Balancing으로 가까운 노드에 보내고, 쓸 수 없으면 DNS 레코드를 만듭니다.")}
      action={s.has_token ? <Toggle label={t("연동 사용")} checked={s.enabled} disabled={busy !== null} onChange={(v) => run("enabled", () => api.setCloudflare({ enabled: v }), v ? t("Cloudflare 연동을 켰습니다") : t("Cloudflare 연동을 껐습니다"))} /> : undefined}>
      <div className="space-y-3">
        <Field label={t("API 토큰")}
          hint={tr("필요한 권한: Zone · DNS · Edit, Zone · Load Balancers · Edit, Account · Load Balancing: Monitors and Pools · Edit. 토큰은 서버에 암호화해 저장하고 다시 보여 주지 않습니다. {guide}", {
            guide: <a href={GUIDE} target="_blank" rel="noreferrer" className="text-pink-600 hover:underline">{t("설정 안내")}</a>,
          })}>
          <Input mono type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)}
            placeholder={s.has_token ? t("저장됨 {hint} · 바꾸려면 새 토큰 입력", { hint: s.token_hint }) : t("Cloudflare API 토큰")} />
        </Field>
        <div className="flex flex-wrap gap-2">
          <Button variant="primary" size="sm" disabled={!token.trim()} loading={busy === "token"}
            onClick={async () => { if (await run("token", () => api.setCloudflare({ token: token.trim() }), t("토큰을 저장했습니다"))) setToken(""); }}>
            {t("토큰 저장")}
          </Button>
          <Button size="sm" disabled={!token.trim() && !s.has_token} loading={testing} onClick={doTest}>{t("연결 확인")}</Button>
        </div>
        {test && <TestResult r={test} />}
        <ErrorBox error={error} />
        {!s.has_token && <p className="text-xs text-gray-500">{t("토큰을 저장한 뒤 연동을 켤 수 있습니다.")}</p>}
        {s.has_token && s.zones.length === 0 && <p className="text-xs text-warn">{t("연동을 켜려면 관리할 존을 하나 이상 저장하세요.")}</p>}
      </div>
    </Card>
  );
}

function Options({ s }: { s: Settings }) {
  const { busy, error, run } = useSave();
  const [zones, setZones] = useState(s.zones.join("\n"));
  const [prev, setPrev] = useState(s.zones);
  if (prev !== s.zones) {
    setPrev(s.zones);
    setZones(s.zones.join("\n"));
  }
  const list = zones.split(/[\s,]+/).map((z) => z.trim()).filter(Boolean);
  const dirty = list.join(",") !== s.zones.join(",");
  return (
    <Card title={t("동작")}>
      <div className="space-y-5">
        <Field label={t("방식")} hint={
          s.mode === "auto" ? t("여러 노드 호스트는 Load Balancing, 쓸 수 없으면 DNS 레코드입니다. 노드 하나뿐인 호스트는 DNS 레코드이고, 와일드카드는 아래에서 켠 경우에만 다룹니다.")
            : s.mode === "load_balancer" ? t("여러 노드 호스트는 Load Balancing만 씁니다. 쓸 수 없으면 그 호스트를 건너뛰고 오류로 보입니다.")
            : t("모든 호스트를 서비스하는 노드의 A/AAAA 레코드로 만듭니다(round-robin, 무료).")}>
          <Segmented<CloudflareMode> label={t("방식")} value={s.mode}
            onChange={(v) => run("mode", () => api.setCloudflare({ mode: v }))}
            options={[{ value: "auto", label: t("자동") }, { value: "load_balancer", label: "Load Balancing" }, { value: "dns", label: t("DNS 레코드") }]} />
        </Field>
        <div className="flex items-start justify-between gap-4">
          <div>
            <div className="text-sm font-medium">{t("Cloudflare 프록시(주황 구름)")}</div>
            <p className="mt-1 text-xs text-gray-600">{t("켜면 방문자는 Cloudflare 엣지를 거칩니다. 노드의 인증서는 지금처럼 노드마다 받습니다. 끄면 DNS만 쓰고 TTL은 레코드 60초, 로드밸런서 30초입니다.")}</p>
          </div>
          <Toggle label={t("Cloudflare 프록시")} checked={s.proxied} disabled={busy !== null} onChange={(v) => run("proxied", () => api.setCloudflare({ proxied: v }))} />
        </div>
        <div className="flex items-start justify-between gap-4">
          <div>
            <div className="text-sm font-medium">{t("와일드카드 도메인")}</div>
            <p className="mt-1 text-xs text-gray-600">{t("꺼 두면 *.example.com 같은 이름은 만들지 않습니다. 켜도 소유가 확인된 와일드카드만 동기화합니다.")}</p>
          </div>
          <Toggle label={t("와일드카드 도메인")} checked={s.wildcards} disabled={busy !== null} onChange={(v) => run("wildcards", () => api.setCloudflare({ wildcards: v }))} />
        </div>
        <Field label={t("관리할 존")} hint={t("한 줄에 하나씩 적습니다. 연동을 켜려면 존이 하나 이상 있어야 합니다. 존 밖의 도메인은 건드리지 않습니다.")}>
          <Textarea mono rows={3} value={zones} onChange={(e) => setZones(e.target.value)} placeholder="example.com" />
        </Field>
        {dirty && <Button size="sm" variant="primary" loading={busy === "zones"} onClick={() => run("zones", () => api.setCloudflare({ zones: list }), t("존 목록을 저장했습니다"))}>{t("존 목록 저장")}</Button>}
        <ErrorBox error={error} />
      </div>
    </Card>
  );
}

function HostRow({ h, names }: { h: CloudflareHost; names: Record<string, string> }) {
  return (
    <li className="flex flex-wrap items-start gap-x-4 gap-y-1 px-4 py-2.5 text-sm">
      <div className="min-w-0 flex-1">
        <div className="break-all font-mono">{h.host}</div>
        {h.reason === "conflict" && <p className="text-xs text-warn">{t("Aven이 만들지 않은 레코드가 있어 건드리지 않습니다: {detail}. 지우면 다음 동기화에서 만듭니다.", { detail: h.detail ?? "" })}</p>}
        {h.reason === "no_public_node" && <p className="text-xs text-gray-500">{t("이 호스트를 서비스하는 노드에 공인 주소가 없습니다(터널 노드 등).")}</p>}
        {h.error && <p className="break-words text-xs text-danger">{h.error}</p>}
      </div>
      <div className="flex flex-wrap items-center gap-1">
        {h.mode === "load_balancer" ? <Badge tone="info">Load Balancing</Badge> : h.mode === "dns" ? <Badge>{t("DNS 레코드")}</Badge> : <Badge tone="warning">{t("건너뜀")}</Badge>}
        {h.nodes.map((id) => <Badge key={id} outline>{names[id] ?? id}</Badge>)}
      </div>
    </li>
  );
}

function SyncStatus({ s }: { s: Settings }) {
  const st = s.status;
  const { busy, error, run } = useSave();
  const nodes = useNodes();
  const names = Object.fromEntries((nodes.data ?? []).map((n) => [n.id, n.name]));
  const [filter, setFilter] = useState("");
  const hosts = st.hosts.filter((h) => h.host.includes(filter.trim().toLowerCase()));
  const missingGeo = st.pools.filter((p) => p.latitude === null);
  return (
    <Card title={t("동기화 상태")}
      description={st.last_sync ? t("마지막 동기화 {time} · 바꾼 항목 {count}개", { time: fmtRelative(st.last_sync), count: st.changes }) : t("아직 동기화하지 않았습니다.")}
      action={<Button size="sm" loading={busy === "sync" || st.syncing} onClick={() => run("sync", api.syncCloudflare, t("동기화했습니다"))}><RefreshCw className="size-3.5" />{t("지금 동기화")}</Button>}>
      <div className="space-y-4">
        <ErrorBox error={error ?? st.error} />
        {st.warning && <p className="text-xs text-warn">{st.warning}</p>}
        {st.load_balancing === "unavailable" && s.mode !== "dns" && (
          <p className="text-xs text-warn">{t("Load Balancing을 쓸 수 없어 여러 노드 호스트도 DNS 레코드로 만들었습니다: {error}", { error: st.load_balancing_error ?? "" })}</p>
        )}
        {missingGeo.length > 0 && (
          <p className="text-xs text-warn">
            {tr("위치(노드 라벨 {geo})가 없는 노드가 있어 가까운 노드 대신 응답 시간으로 고릅니다: {names}. 어드민 › 서버 노드 › 라벨에서 정하세요.", {
              geo: <code className="font-mono">geo=37.57,126.98</code>, names: missingGeo.map((p) => names[p.node_id] ?? p.node_id).join(", "),
            })}
          </p>
        )}
        {st.zones.length > 0 && (
          <div className="flex flex-wrap gap-2">
            {st.zones.map((z) => (
              <div key={z.id} className="rounded-lg border border-gray-200 px-3 py-2 text-xs">
                <div className="font-mono text-sm">{z.name}</div>
                <div className="text-gray-500">{z.account} · {z.plan} · {t("레코드 {r} · 로드밸런서 {l}", { r: z.records, l: z.load_balancers })}</div>
              </div>
            ))}
          </div>
        )}
        {st.hosts.length === 0 ? (
          <p className="text-sm text-gray-500">{t("연동한 존 안의 도메인을 가진 서비스가 없습니다.")}</p>
        ) : (
          <>
            {st.hosts.length > 8 && <Input size="sm" className="w-56" value={filter} onChange={(e) => setFilter(e.target.value)} placeholder={t("호스트 찾기")} aria-label={t("호스트 찾기")} />}
            <RowList>{hosts.map((h) => <HostRow key={h.host} h={h} names={names} />)}</RowList>
          </>
        )}
      </div>
    </Card>
  );
}

function Danger({ s }: { s: Settings }) {
  const { busy, error, run } = useSave();
  const { confirm, node } = useConfirm();
  const [removed, setRemoved] = useState<number | null>(null);
  return (
    <Card danger title={t("Aven이 만든 Cloudflare 항목 지우기")}
      description={s.enabled ? t("연동을 먼저 끄세요. 켜 둔 채 지우면 다음 동기화에서 다시 만듭니다.") : t("이 서버가 만든 DNS 레코드, 로드밸런서, pool, monitor를 지웁니다. 다른 레코드는 그대로 둡니다.")}
      action={<Button size="sm" variant="danger" disabled={s.enabled} loading={busy === "cleanup"} onClick={() => confirm({
        title: t("Cloudflare 항목 지우기"), danger: true, confirmLabel: t("지우기"),
        message: <p>{t("이 도메인들은 Cloudflare에서 더 이상 노드를 가리키지 않게 됩니다.")}</p>,
        run: () => run("cleanup", async () => { const r = await api.cleanupCloudflare(); setRemoved(r.removed); return r; }),
      })}>{t("지우기")}</Button>}>
      {node}
      {removed !== null && <Notice>{t("{count}개를 지웠습니다.", { count: removed })}</Notice>}
      <ErrorBox error={error} />
    </Card>
  );
}
