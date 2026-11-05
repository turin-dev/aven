"use client";

import { toast } from "@/lib/uiState";
import { t, tr } from "@/lib/i18n";
import { useState } from "react";
import Link from "next/link";
import { useCtxHref } from "@/lib/nav";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, Mail, Pencil, Plus, Send, Trash2, Webhook } from "lucide-react";
import { api, type ChannelKind, type ID, type NotificationChannel, type NotificationChannelInput, type Project, type UsageAlert } from "@/lib/api";
import { qk, useChannels, useSystem } from "@/lib/queries";
import { fmtRelative, fmtTime } from "@/lib/format";
import { useUnsavedGuard } from "./SettingsGuard";
import { Button, Card, Dialog, EmptyState, ErrorBox, Field, IconButton, Input, RowList, Select, Spinner, Textarea, Toggle, cx, errorMessage, useConfirm } from "./ui";

const KIND_LABEL: Record<ChannelKind, string> = { webhook: "Webhook", slack: "Slack", discord: "Discord", email: t("이메일") };
const DEFAULT_EVENTS = ["deploy.failed", "deploy.crashed", "alert.*", "volume.limit_exceeded", "usage.threshold_reached"];

/** Common subscriptions offered as one-click chips; the textarea still accepts any kind or pattern. */
const EVENT_PRESETS: { value: string; label: string }[] = [
  { value: "deploy.failed", label: t("배포 실패") },
  { value: "deploy.crashed", label: t("크래시") },
  { value: "deploy.live", label: t("배포 성공") },
  { value: "alert.*", label: t("지표 알림") },
  { value: "volume.limit_exceeded", label: t("볼륨 상한 초과") },
  { value: "volume.near_limit", label: t("볼륨 90% 도달") },
  { value: "backup.failed", label: t("백업 실패") },
  { value: "cron.failed", label: t("크론 실패") },
  { value: "usage.threshold_reached", label: t("사용량 기준 도달") },
  { value: "usage.limit_reached", label: t("사용량 한도 도달") },
  { value: "node.unavailable", label: t("노드 연결 끊김") },
  { value: "*", label: t("모든 이벤트") },
];

// Same rule as the server (internal/notify ValidPattern).
const PATTERN_RE = /^[a-z0-9_.*]{1,100}$/;
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

function validUrl(s: string) {
  try {
    const u = new URL(s);
    return u.protocol === "https:" || u.protocol === "http:";
  } catch {
    return false;
  }
}

const splitList = (s: string) =>
  s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean);

function ChannelDialog({ pid, channel, onClose }: { pid: ID | null; channel: NotificationChannel | null; onClose: () => void }) {
  const qc = useQueryClient();
  const [kind, setKind] = useState<ChannelKind>(channel?.kind ?? "webhook");
  const [name, setName] = useState(channel?.name ?? "");
  const [url, setUrl] = useState(channel?.url ?? "");
  const [secret, setSecret] = useState("");
  const [clearSecret, setClearSecret] = useState(false);
  const [recipients, setRecipients] = useState((channel?.recipients ?? []).join("\n"));
  const [events, setEvents] = useState((channel?.events ?? DEFAULT_EVENTS).join("\n"));
  const [enabled, setEnabled] = useState(channel?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const evList = splitList(events);
  const badEvents = evList.filter((e) => !PATTERN_RE.test(e));
  const rcpts = splitList(recipients);
  const badRcpts = rcpts.filter((r) => !EMAIL_RE.test(r));
  const urlBad = kind !== "email" && url.trim() !== "" && !validUrl(url.trim());
  const problem =
    !name.trim()
      ? t("이름을 입력하세요.")
      : kind === "email"
        ? rcpts.length === 0
          ? t("받는 사람을 한 명 이상 입력하세요.")
          : rcpts.length > 20
            ? t("받는 사람은 최대 20명입니다.")
            : badRcpts.length
              ? t("올바르지 않은 이메일: {list}", { list: badRcpts.slice(0, 3).join(", ") })
              : null
        : !url.trim()
          ? t("URL을 입력하세요.")
          : urlBad
            ? t("http:// 또는 https://로 시작하는 URL을 입력하세요.")
            : null;
  const eventProblem = evList.length === 0 ? t("이벤트를 하나 이상 고르세요.") : badEvents.length ? t("올바르지 않은 이벤트 패턴: {list}", { list: badEvents.slice(0, 3).join(", ") }) : null;
  const toggleEvent = (v: string) => setEvents((cur) => {
    const list = splitList(cur);
    return (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]).join("\n");
  });
  const save = async () => {
    if (problem || eventProblem) return setError(problem ?? eventProblem);
    setBusy(true);
    setError(null);
    const input: NotificationChannelInput = { name: name.trim(), events: evList, enabled };
    if (kind === "email") input.recipients = splitList(recipients);
    else {
      input.url = url.trim();
      if (kind === "webhook" && (secret || clearSecret)) input.secret = clearSecret ? "" : secret;
    }
    try {
      if (channel) await api.updateChannel(pid, channel.id, input);
      else await api.createChannel(pid, { ...input, kind });
      await qc.invalidateQueries({ queryKey: qk.channels(pid) });
      toast(channel ? t("{name} 채널을 저장했습니다.", { name: name.trim() }) : t("{name} 채널을 추가했습니다. 테스트 전송으로 확인해 보세요.", { name: name.trim() }));
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      title={channel ? t("알림 채널 편집") : t("알림 채널 추가")}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" loading={busy} disabled={!!problem || !!eventProblem} onClick={save}>
            {t("저장")}
          </Button>
        </>
      }
    >
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <div className="grid grid-cols-[140px_1fr] gap-3">
          <Field label={t("종류")}>
            <Select value={kind} disabled={!!channel} onChange={(e) => setKind(e.target.value as ChannelKind)}>
              {(Object.keys(KIND_LABEL) as ChannelKind[]).map((k) => (
                <option key={k} value={k}>
                  {KIND_LABEL[k]}
                </option>
              ))}
            </Select>
          </Field>
          <Field label={t("이름")}>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="ops" maxLength={100} autoFocus={!channel} />
          </Field>
        </div>
        {kind === "email" ? (
          <Field label={t("받는 사람")} hint={t("한 줄에 하나씩, 최대 20개. 서버에 AVEN_SMTP_*가 설정되어 있어야 합니다.")}>
            <Textarea rows={3} mono className="w-full" value={recipients} onChange={(e) => setRecipients(e.target.value)} placeholder="ops@example.com" />
          </Field>
        ) : (
          <Field
            label="URL"
            hint={
              urlBad ? (
                <span className="text-danger">{t("http:// 또는 https://로 시작하는 URL을 입력하세요.")}</span>
              ) : kind === "webhook"
                ? t("JSON payload를 POST합니다. 사설·루프백 주소는 AVEN_NOTIFY_ALLOW_PRIVATE=1일 때만 허용됩니다.")
                : kind === "slack"
                  ? "Slack Incoming Webhook URL"
                  : t("Discord 채널 webhook URL")
            }
          >
            <Input mono value={url} invalid={urlBad} onChange={(e) => setUrl(e.target.value)} placeholder="https://" autoComplete="off" />
          </Field>
        )}
        {kind === "webhook" && (
          <Field
            label={t("서명 키 (선택)")}
            hint={
              <>
                {tr("설정하면 {header} 헤더를 붙입니다. 예전 이름 {legacy}도 같이 붙습니다.", {
                  header: <code className="font-mono">X-Aven-Signature: sha256=&lt;HMAC&gt;</code>,
                  legacy: <code className="font-mono">X-Keel-Signature</code>,
                })}
                {channel?.secret_set && t(" 지금 키가 설정되어 있습니다. 비워 두면 그대로 둡니다.")}
              </>
            }
          >
            <Input type="password" autoComplete="new-password" value={secret} onChange={(e) => (setSecret(e.target.value), setClearSecret(false))} />
            {channel?.secret_set && (
              <label className="mt-2 flex items-center gap-2 text-sm text-gray-600">
                <input type="checkbox" className="accent-pink-500" checked={clearSecret} onChange={(e) => setClearSecret(e.target.checked)} /> {t("서명 키 지우기")}
              </label>
            )}
          </Field>
        )}
        <fieldset>
          <legend className="mb-2 block text-xs font-semibold leading-[18px] text-gray-600">{t("이벤트")}</legend>
          <div className="mb-2 flex flex-wrap gap-1.5">
            {EVENT_PRESETS.map((p) => {
              const on = evList.includes(p.value);
              return (
                <button
                  key={p.value}
                  type="button"
                  aria-pressed={on}
                  title={p.value}
                  onClick={() => toggleEvent(p.value)}
                  className={cx(
                    "cursor-pointer rounded-md border px-2 py-1 text-xs focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-500",
                    on ? "border-pink-500 bg-pink-500/10 text-foreground" : "border-gray-200 text-gray-600 hover:border-gray-300 hover:text-foreground",
                  )}
                >
                  {p.label}
                </button>
              );
            })}
          </div>
          <Textarea rows={4} mono className="w-full" aria-label={t("이벤트 패턴")} value={events} onChange={(e) => setEvents(e.target.value)} />
          <span className="mt-2 block text-xs leading-[18px] text-gray-600">{t("한 줄에 하나씩. 정확한 kind 또는 패턴(deploy.*, *.failed, *).")}</span>
          {eventProblem && evList.length > 0 && <span className="mt-1 block text-xs text-danger">{eventProblem}</span>}
        </fieldset>
        <div className="flex items-center gap-3">
          <Toggle checked={enabled} onChange={setEnabled} label={t("사용")} />
          <span className="text-sm text-gray-600">{t("사용")}</span>
        </div>
        {problem && (name.trim() || url.trim() || recipients.trim()) && <p className="text-xs text-gray-500" role="status">{problem}</p>}
        <ErrorBox error={error} />
        <button type="submit" className="hidden" aria-hidden tabIndex={-1} />
      </form>
    </Dialog>
  );
}

function Deliveries({ pid, cid }: { pid: ID | null; cid: ID }) {
  const q = useQuery({ queryKey: qk.deliveries(pid, cid), queryFn: () => api.channelDeliveries(pid, cid), refetchInterval: 15_000 });
  if (q.isPending) return <Spinner />;
  if (q.error) return <ErrorBox error={q.error} />;
  const list = q.data ?? [];
  if (!list.length) return <p className="py-2 text-xs text-gray-500">{t("아직 보낸 기록이 없습니다")}</p>;
  return (
    <ul className="max-h-[220px] space-y-1 overflow-y-auto text-xs tabular-nums">
      {list.slice(0, 30).map((d) => (
        <li key={d.id} className="flex items-center gap-2">
          <span className={cx("size-[6px] shrink-0 rounded-full", d.status === "ok" ? "bg-green-600" : "bg-red-600")} />
          <span className="w-[150px] shrink-0 truncate font-mono text-gray-600">{d.event_kind}</span>
          <span className="min-w-0 flex-1 truncate text-gray-500" title={d.error}>
            {d.status === "ok" ? `${d.status_code ?? "ok"} · ${d.duration_ms}ms` : d.error}
            {d.attempts > 1 && t(" · {count}회 시도", { count: d.attempts })}
          </span>
          <span className="shrink-0 text-gray-500" title={fmtTime(d.created_at)}>
            {fmtRelative(d.created_at)}
          </span>
        </li>
      ))}
    </ul>
  );
}

/** Notification channels of a project, or workspace-wide channels when pid is null (v1.5). */
export function NotificationChannels({ pid, canEdit, title }: { pid: ID | null; canEdit: boolean; title: string }) {
  const qc = useQueryClient();
  const channels = useChannels(pid);
  const [editing, setEditing] = useState<NotificationChannel | null | undefined>(undefined);
  const [open, setOpen] = useState<ID | null>(null);
  const [tested, setTested] = useState<{ id: ID; ok: boolean; msg: string } | null>(null);
  const [testing, setTesting] = useState<ID | null>(null);
  const [toggling, setToggling] = useState<ID | null>(null);
  const { confirm, node } = useConfirm();
  const setEnabled = async (c: NotificationChannel, enabled: boolean) => {
    setToggling(c.id);
    try {
      await api.updateChannel(pid, c.id, { enabled });
      await qc.invalidateQueries({ queryKey: qk.channels(pid) });
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setToggling(null);
    }
  };
  const test = async (c: NotificationChannel) => {
    setTesting(c.id);
    try {
      const r = await api.testChannel(pid, c.id);
      setTested({ id: c.id, ok: r.ok, msg: r.ok ? t("보냈습니다 ({code}, {ms}ms)", { code: r.status_code ?? "ok", ms: r.duration_ms }) : (r.error ?? t("실패")) });
      qc.invalidateQueries({ queryKey: qk.deliveries(pid, c.id) });
    } catch (e) {
      setTested({ id: c.id, ok: false, msg: errorMessage(e) });
    } finally {
      setTesting(null);
    }
  };
  return (
    <Card
      title={title}
      action={
        canEdit && (
          <Button size="sm" variant="primary" onClick={() => setEditing(null)}>
            <Plus className="size-3.5" /> {t("채널 추가")}
          </Button>
        )
      }
    >
      {node}
      {editing !== undefined && <ChannelDialog pid={pid} channel={editing} onClose={() => setEditing(undefined)} />}
      <ErrorBox error={channels.error} className="mb-3" />
      {channels.isPending ? (
        <Spinner />
      ) : (channels.data ?? []).length === 0 ? (
        <EmptyState icon={<Bell className="size-5" />} title={t("알림 채널이 없습니다")} />
      ) : (
        <RowList>
          {channels.data!.map((c) => (
            <li key={c.id} className={cx("px-4 py-3", !c.enabled && "opacity-60")}>
              <div className="flex items-center gap-3">
                {c.kind === "email" ? <Mail className="size-4 shrink-0 text-gray-600" /> : <Webhook className="size-4 shrink-0 text-gray-600" />}
                <button type="button" className="min-w-0 flex-1 cursor-pointer text-left" onClick={() => setOpen(open === c.id ? null : c.id)} aria-expanded={open === c.id}>
                  <div className="flex items-center gap-2">
                    <span className="truncate text-sm font-medium">{c.name}</span>
                    <span className="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-medium text-gray-600">{KIND_LABEL[c.kind]}</span>
                    {!c.enabled && <span className="text-xs text-gray-400">{t("꺼짐")}</span>}
                    {c.secret_set && <span className="text-xs text-gray-400">{t("서명")}</span>}
                  </div>
                  <div className="truncate font-mono text-xs text-gray-500">
                    {c.kind === "email" ? c.recipients.join(", ") : c.url} · {c.events.join(", ")}
                  </div>
                </button>
                {canEdit && (
                  <span className="flex shrink-0 items-center gap-1">
                    <Toggle checked={c.enabled} disabled={toggling === c.id} onChange={(v) => setEnabled(c, v)} label={t("{name} 사용", { name: c.name })} />
                    <IconButton label={t("테스트 전송")} size="sm" disabled={testing === c.id} onClick={() => test(c)}>
                      {testing === c.id ? <Spinner className="size-3.5" /> : <Send className="size-3.5" />}
                    </IconButton>
                    <IconButton label={t("편집")} size="sm" onClick={() => setEditing(c)}>
                      <Pencil className="size-3.5" />
                    </IconButton>
                    <IconButton
                      label={t("삭제")}
                      size="sm"
                      onClick={() =>
                        confirm({
                          title: t("알림 채널 삭제"),
                          message: t("{name} 채널과 전달 기록을 삭제합니다.", { name: c.name }),
                          confirmLabel: t("삭제"),
                          danger: true,
                          run: async () => {
                            try {
                              await api.deleteChannel(pid, c.id);
                              toast(t("{name} 채널을 삭제했습니다.", { name: c.name }));
                            } catch (e) {
                              toast(errorMessage(e), "error");
                            } finally {
                              await qc.invalidateQueries({ queryKey: qk.channels(pid) });
                            }
                          },
                        })
                      }
                    >
                      <Trash2 className="size-3.5" />
                    </IconButton>
                  </span>
                )}
              </div>
              {tested?.id === c.id && (
                <div className={cx("mt-2 text-xs", tested.ok ? "text-success" : "text-danger")} role="status">
                  {tested.msg}
                </div>
              )}
              {open === c.id && canEdit && (
                <div className="mt-3 border-t border-gray-200 pt-2">
                  <div className="mb-1 text-xs font-semibold text-gray-500">{t("최근 전달 (최대 100개 보관)")}</div>
                  <Deliveries pid={pid} cid={c.id} />
                </div>
              )}
            </li>
          ))}
        </RowList>
      )}
    </Card>
  );
}

/** SMTP status and test mail (workspace admin). */
export function SmtpStatus() {
  const sys = useSystem();
  const [to, setTo] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; msg: string } | null>(null);
  const configured = !!sys.data?.smtp_configured;
  return (
    <Card
      title={t("메일 (SMTP)")}
      description={
        configured
          ? t("AVEN_SMTP_*로 설정되어 있습니다. 이메일 채널과 초대 메일에 씁니다.")
          : t("설정되지 않았습니다. AVEN_SMTP_HOST, AVEN_SMTP_FROM(필수)과 AVEN_SMTP_PORT, AVEN_SMTP_USERNAME, AVEN_SMTP_PASSWORD, AVEN_SMTP_TLS(starttls|tls|none)를 aven-server 환경 변수로 지정하세요.")
      }
    >
      {configured && (
        <form
          className="flex gap-2"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setResult(null);
            try {
              const r = await api.testSmtp(to.trim() || undefined);
              setResult({ ok: true, msg: t("{to}로 보냈습니다", { to: r.to }) });
            } catch (err) {
              setResult({ ok: false, msg: err instanceof Error ? err.message : String(err) });
            } finally {
              setBusy(false);
            }
          }}
        >
          <Input type="email" value={to} onChange={(e) => setTo(e.target.value)} placeholder={t("받는 사람 (비우면 내 이메일)")} aria-label={t("테스트 메일 받는 사람")} />
          <Button type="submit" loading={busy}>
            {t("테스트 메일")}
          </Button>
        </form>
      )}
      {result && (
        <div className={cx("mt-2 text-xs", result.ok ? "text-success" : "text-danger")} role="status">
          {result.msg}
        </div>
      )}
    </Card>
  );
}

/** Monthly usage alert thresholds of a project (usage.threshold_reached at 80% and 100%). */
export function UsageAlertSettings({ project, canEdit }: { project: Project; canEdit: boolean }) {
  const qc = useQueryClient();
  const href = useCtxHref();
  const cur = project.usage_alert ?? {};
  const [cpu, setCpu] = useState(cur.cpu_core_hours?.toString() ?? "");
  const [mem, setMem] = useState(cur.memory_gb_hours?.toString() ?? "");
  const [egress, setEgress] = useState(cur.egress_gb?.toString() ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);
  const parse = (s: string) => (s.trim() === "" ? undefined : Number(s));
  const vals = [parse(cpu), parse(mem), parse(egress)];
  const valid = vals.every((v) => v === undefined || (Number.isFinite(v) && v > 0));
  const dirty = vals[0] !== (cur.cpu_core_hours ?? undefined) || vals[1] !== (cur.memory_gb_hours ?? undefined) || vals[2] !== (cur.egress_gb ?? undefined);
  useUnsavedGuard(dirty && canEdit);
  const field = (label: string, unit: string, v: string, set: (s: string) => void) => {
    const n = parse(v);
    const bad = n !== undefined && !(Number.isFinite(n) && n > 0);
    return (
      <Field label={label} hint={bad ? <span className="text-danger">{t("0보다 큰 값을 입력하거나 비워 두세요.")}</span> : unit}>
        <Input type="number" min={0} step="any" value={v} invalid={bad} disabled={!canEdit} onChange={(e) => (set(e.target.value), setSaved(false))} placeholder={t("없음")} />
      </Field>
    );
  };
  return (
    <form
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        setError(null);
        const ua: UsageAlert = {};
        if (vals[0] !== undefined) ua.cpu_core_hours = vals[0];
        if (vals[1] !== undefined) ua.memory_gb_hours = vals[1];
        if (vals[2] !== undefined) ua.egress_gb = vals[2];
        try {
          await api.updateProject(project.id, { usage_alert: Object.keys(ua).length ? ua : null });
          await qc.invalidateQueries({ queryKey: qk.project(project.id) });
          setSaved(true);
          toast(t("저장했습니다."));
        } catch (err) {
          setError(err);
        } finally {
          setBusy(false);
        }
      }}
    >
      <Card
        title={t("사용량 알림 기준")}
        description={tr("월간 사용량이 기준에 닿으면 알림을 보냅니다. 현재 사용량은 {link}에서 봅니다.", {
          link: (
            <Link href={href("/usage/")} className="text-pink-600 hover:underline">
              {t("사용량")}
            </Link>
          ),
        })}
        footer={
          canEdit ? (
            <Button type="submit" variant="primary" loading={busy} disabled={!valid || !dirty}>
              {t("저장")}
            </Button>
          ) : undefined
        }
      >
        <div className="space-y-5">
          <div className="grid gap-4 sm:grid-cols-3">
            {field("CPU", t("코어·시간 / 월"), cpu, setCpu)}
            {field(t("메모리"), t("GB·시간 / 월"), mem, setMem)}
            {field(t("송신 트래픽"), t("GB / 월"), egress, setEgress)}
          </div>
          <p className="text-sm text-gray-500">
            {t("UTC 달력 월 기준입니다. 사용량이 기준의 80%와 100%에 닿으면 usage.threshold_reached 이벤트를 달마다 한 번씩 냅니다(알림 채널·알림함으로 전달). 집계는 매시 갱신됩니다.")}
          </p>
          <ErrorBox error={error} />
        </div>
      </Card>
    </form>
  );
}
