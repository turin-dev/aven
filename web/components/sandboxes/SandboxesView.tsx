"use client";

import { useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  ArrowUp,
  Box,
  Camera,
  File as FileIcon,
  Folder,
  GitFork,
  HeartPulse,
  Pencil,
  Play,
  Plus,
  RefreshCcw,
  Save,
  Trash2,
  Upload,
} from "lucide-react";
import type { ID } from "@/lib/api";
import { fmtBytes, fmtRelative, fmtTime, fmtUntil } from "@/lib/format";
import { useCaps, useNodes } from "@/lib/queries";
import {
  b64ToBytes,
  b64ToText,
  bytesToB64,
  parseVariableLines,
  sandboxApi,
  sbxKeys,
  useSandboxCheckpoints,
  useSandboxFiles,
  useSandboxes,
  type Sandbox,
  type SandboxCheckpoint,
  type SandboxExecResult,
  type SandboxOptions,
} from "@/lib/sandboxes";
import { toast } from "@/lib/uiState";
import { lock } from "../Perm";
import {
  Button,
  CopyButton,
  Dialog,
  EmptyState,
  ErrorBox,
  Field,
  IconButton,
  Input,
  RowList,
  Segmented,
  Select,
  Spinner,
  Textarea,
  Toggle,
  cx,
  errorMessage,
  useConfirm,
} from "../ui";
import { SandboxTerminal } from "./SandboxTerminal";
import { t, tr } from "@/lib/i18n";

const MAX_UPLOAD = 256 * 1024;

function idleLabel(s: Sandbox): string {
  if (!s.expires_at) return t("유휴 삭제 안 함");
  return t("{time} 유휴 삭제", { time: fmtUntil(s.expires_at) });
}

function StatusDot({ s }: { s: Sandbox }) {
  const running = s.status === "running";
  return (
    <span className="flex items-center gap-1.5 text-xs text-gray-600">
      <span aria-hidden className={cx("size-2 rounded-full", running ? "bg-green-500" : "animate-pulse bg-yellow-500")} />
      {running ? t("실행 중") : t("만드는 중")}
    </span>
  );
}

function Section({ title, description, action, children }: { title: ReactNode; description?: ReactNode; action?: ReactNode; children?: ReactNode }) {
  return (
    <section className="min-w-0 [&+&]:border-t [&+&]:border-gray-200 [&+&]:pt-8">
      <div className={cx("flex flex-wrap items-start gap-x-6 gap-y-3", !!children && "mb-4")}>
        <div className="min-w-0 flex-1">
          <h3 className="text-[17px] font-semibold leading-6">{title}</h3>
          {description && <p className="mt-1 text-sm text-gray-600">{description}</p>}
        </div>
        {action && <div className="flex shrink-0 items-center gap-2">{action}</div>}
      </div>
      {children}
    </section>
  );
}

/* ── Create / fork dialog ─────────────────────────────────────────────── */

type Mode = { kind: "create" } | { kind: "fork"; source: Sandbox } | { kind: "checkpoint"; checkpoint: SandboxCheckpoint };

function SandboxDialog({
  mode,
  eid,
  checkpoints,
  onClose,
  onCreated,
}: {
  mode: Mode | null;
  eid: ID;
  checkpoints: SandboxCheckpoint[];
  onClose: () => void;
  onCreated: (s: Sandbox) => void;
}) {
  const nodes = useNodes();
  const [source, setSource] = useState<"image" | "checkpoint">("image");
  const [image, setImage] = useState("");
  const [checkpoint, setCheckpoint] = useState("");
  const [node, setNode] = useState("");
  const [idle, setIdle] = useState("30");
  const [memory, setMemory] = useState("1024");
  const [cpu, setCpu] = useState("1");
  const [priv, setPriv] = useState(false);
  const [vars, setVars] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    if (!mode) return;
    setError(null);
    setBusy(false);
    setSource(mode.kind === "checkpoint" ? "checkpoint" : "image");
    setCheckpoint(mode.kind === "checkpoint" ? mode.checkpoint.name : (checkpoints[0]?.name ?? ""));
    setImage("");
    setNode("");
    setVars("");
    setPriv(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode]);

  const isFork = mode?.kind === "fork";
  const parsed = parseVariableLines(vars);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!mode) return;
    if (parsed.bad.length) {
      setError(new Error(t("KEY=VALUE 형식이 아닌 줄: {line}", { line: parsed.bad[0] })));
      return;
    }
    const opts: SandboxOptions = {
      idle_timeout_minutes: Math.max(0, Number(idle) || 0),
      memory_mb: Number(memory) || undefined,
      cpu_cores: Number(cpu) || undefined,
      network: priv ? "private" : "isolated",
      variables: Object.keys(parsed.vars).length ? parsed.vars : undefined,
    };
    setBusy(true);
    setError(null);
    try {
      const s = isFork
        ? await sandboxApi.fork(mode.source.id, opts)
        : await sandboxApi.create(eid, {
            ...opts,
            ...(source === "checkpoint" ? { checkpoint } : { image: image.trim() || undefined, node_id: node || undefined }),
          });
      onCreated(s);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const schedulable = (nodes.data ?? []).filter((n) => n.schedulable && n.status === "online");

  return (
    <Dialog
      open={!!mode}
      onClose={() => !busy && onClose()}
      title={isFork ? `${mode.source.id} fork` : t("새 샌드박스")}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" type="submit" form="sbx-form" loading={busy} disabled={source === "checkpoint" && !isFork && !checkpoint}>
            {isFork ? "Fork" : t("만들기")}
          </Button>
        </>
      }
    >
      <form id="sbx-form" onSubmit={submit} className="space-y-4">
        {isFork ? (
          <p className="text-sm text-gray-600">
            {t("실행 중인 샌드박스의 파일시스템을 복사해 새 샌드박스를 띄웁니다. 파일은 그대로지만 실행 중인 프로세스는 옮겨지지 않고, 설정과 변수도 물려받지 않습니다.")}
          </p>
        ) : (
          <>
            <Segmented
              label={t("시작 방법")}
              value={source}
              onChange={setSource}
              options={[
                { value: "image", label: t("이미지") },
                { value: "checkpoint", label: "Checkpoint" },
              ]}
            />
            {source === "image" ? (
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={t("이미지")} hint={t("/bin/sh가 있는 이미지. 비우면 ubuntu:24.04")}>
                  <Input mono value={image} onChange={(e) => setImage(e.target.value)} placeholder="ubuntu:24.04" />
                </Field>
                <Field label={t("노드")}>
                  <Select value={node} onChange={(e) => setNode(e.target.value)}>
                    <option value="">{t("서버 노드")}</option>
                    {schedulable
                      .filter((n) => !n.is_local)
                      .map((n) => (
                        <option key={n.id} value={n.id}>
                          {n.name}
                        </option>
                      ))}
                  </Select>
                </Field>
              </div>
            ) : (
              <Field label="Checkpoint" hint={t("checkpoint는 캡처한 노드에서만 띄울 수 있습니다")}>
                <Select value={checkpoint} onChange={(e) => setCheckpoint(e.target.value)}>
                  {checkpoints.length === 0 && <option value="">{t("저장된 checkpoint가 없습니다")}</option>}
                  {checkpoints.map((c) => (
                    <option key={c.id} value={c.name}>
                      {c.name}
                    </option>
                  ))}
                </Select>
              </Field>
            )}
          </>
        )}
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={t("유휴 제한(분)")} hint={t("0 = 끄기")}>
            <Input type="number" min={0} max={10080} value={idle} onChange={(e) => setIdle(e.target.value)} />
          </Field>
          <Field label={t("메모리(MiB)")}>
            <Input type="number" min={128} max={16384} step={128} value={memory} onChange={(e) => setMemory(e.target.value)} />
          </Field>
          <Field label={t("CPU(코어)")}>
            <Input type="number" min={0.1} max={16} step={0.1} value={cpu} onChange={(e) => setCpu(e.target.value)} />
          </Field>
        </div>
        <div className="flex items-start justify-between gap-4 rounded-md border border-gray-200 px-3 py-3">
          <div>
            <div className="text-sm font-medium">{t("비공개 네트워크에 연결")}</div>
            <div className="mt-0.5 text-xs text-gray-500">
              {tr("끄면 외부 인터넷으로만 나갈 수 있습니다. 켜면 환경의 서비스에 닿을 수 있고, 서비스는 이 샌드박스를 {name} 이름으로 찾습니다.", {
                name: <code>sbx-…</code>,
              })}
            </div>
          </div>
          <Toggle checked={priv} onChange={setPriv} label={t("비공개 네트워크에 연결")} />
        </div>
        <Field label={t("변수")} hint={t("줄마다 KEY=VALUE. 모든 명령과 셸에서 쓸 수 있고, 저장된 값은 다시 보이지 않습니다")}>
          <Textarea mono rows={3} className="w-full" value={vars} onChange={(e) => setVars(e.target.value)} placeholder={"NODE_ENV=production\nGH_TOKEN=…"} />
        </Field>
        <ErrorBox error={error} />
      </form>
    </Dialog>
  );
}

/* ── Command runner ───────────────────────────────────────────────────── */

function CommandRunner({ s, canEdit }: { s: Sandbox; canEdit: boolean }) {
  const qc = useQueryClient();
  const [cmd, setCmd] = useState("");
  const [cwd, setCwd] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<(SandboxExecResult & { command: string }) | null>(null);
  const [error, setError] = useState<unknown>(null);

  const run = async (e: FormEvent) => {
    e.preventDefault();
    if (!cmd.trim()) return;
    setBusy(true);
    setError(null);
    try {
      const r = await sandboxApi.exec(s.id, { command: cmd, cwd: cwd.trim() || undefined });
      setResult({ ...r, command: cmd });
      qc.invalidateQueries({ queryKey: sbxKeys.list(s.environment_id) });
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const stdout = result ? b64ToText(result.stdout) : "";
  const stderr = result ? b64ToText(result.stderr) : "";
  return (
    <div className="space-y-3">
      <form onSubmit={run} className="flex flex-wrap items-end gap-2">
        <Field label={t("명령")} className="min-w-[240px] flex-[3]">
          <Input mono value={cmd} onChange={(e) => setCmd(e.target.value)} placeholder="npm test" aria-describedby="sbx-cmd-hint" />
        </Field>
        <Field label={t("작업 디렉터리")} className="min-w-[140px] flex-1">
          <Input mono value={cwd} onChange={(e) => setCwd(e.target.value)} placeholder="/" />
        </Field>
        <Button type="submit" variant="primary" loading={busy} disabled={!cmd.trim()} className="h-[38px]" {...lock(canEdit)}>
          <Play /> {t("실행")}
        </Button>
      </form>
      <p id="sbx-cmd-hint" className="text-xs text-gray-500">
        {tr("{code}로 실행합니다(파이프, 리다이렉트 가능). 최대 110초이고, 끝나면 출력을 보여 줍니다.", {
          code: <code className="font-mono">sh -lc</code>,
        })}
      </p>
      <ErrorBox error={error} />
      {result && (
        <div className="overflow-hidden rounded-lg border border-gray-200">
          <div className="flex flex-wrap items-center gap-3 border-b border-gray-200 bg-secondaryBg px-3 py-2 text-xs">
            <code className="min-w-0 flex-1 truncate font-mono text-gray-600">$ {result.command}</code>
            {result.timed_out ? (
              <span className="font-medium text-danger">{t("시간 초과")}</span>
            ) : (
              <span className={cx("font-medium", result.exit_code === 0 ? "text-success" : "text-danger")}>exit {result.exit_code}</span>
            )}
            {result.truncated && <span className="text-warn">{t("출력이 잘렸습니다")}</span>}
          </div>
          <pre className="max-h-[360px] overflow-auto whitespace-pre-wrap break-words bg-[#0f0e14] p-3 font-mono text-xs leading-5 text-[#e5e5e5]">
            {stdout}
            {stderr && <span className="text-red-600">{stderr}</span>}
            {!stdout && !stderr && <span className="text-gray-500">{t("(출력 없음)")}</span>}
          </pre>
        </div>
      )}
    </div>
  );
}

/* ── File browser ─────────────────────────────────────────────────────── */

function parentOf(p: string): string {
  if (p === "/") return "/";
  const i = p.replace(/\/+$/, "").lastIndexOf("/");
  return i <= 0 ? "/" : p.slice(0, i);
}

function FileBrowser({ s, canEdit }: { s: Sandbox; canEdit: boolean }) {
  const qc = useQueryClient();
  const [dir, setDir] = useState("/");
  const [dirInput, setDirInput] = useState("/");
  const [open, setOpen] = useState<{ path: string; text: string; binary: boolean; truncated: boolean } | null>(null);
  const [draft, setDraft] = useState("");
  const [newName, setNewName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const upload = useRef<HTMLInputElement>(null);
  const files = useSandboxFiles(s.id, dir, true);

  useEffect(() => setDirInput(dir), [dir]);

  const refresh = () => qc.invalidateQueries({ queryKey: sbxKeys.files(s.id, dir) });

  const openFile = async (path: string) => {
    setError(null);
    try {
      const r = await sandboxApi.readFile(s.id, path);
      const bytes = b64ToBytes(r.content_base64);
      const binary = bytes.includes(0);
      const text = binary ? "" : new TextDecoder().decode(bytes);
      setOpen({ path, text, binary, truncated: r.truncated });
      setDraft(text);
    } catch (err) {
      setError(err);
    }
  };

  const save = async (path: string, content: string) => {
    const bytes = new TextEncoder().encode(content);
    if (bytes.length > MAX_UPLOAD) {
      setError(new Error(t("파일은 256 KiB까지 쓸 수 있습니다")));
      return false;
    }
    setBusy(true);
    setError(null);
    try {
      await sandboxApi.writeFile(s.id, path, bytesToB64(bytes));
      toast(t("{path} 저장됨", { path }), "success");
      refresh();
      return true;
    } catch (err) {
      setError(err);
      return false;
    } finally {
      setBusy(false);
    }
  };

  const onUpload = async (f: File | undefined) => {
    if (!f) return;
    if (f.size > MAX_UPLOAD) {
      setError(new Error(t("파일은 256 KiB까지 올릴 수 있습니다")));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const path = `${dir.replace(/\/+$/, "")}/${f.name}`;
      await sandboxApi.writeFile(s.id, path, bytesToB64(new Uint8Array(await f.arrayBuffer())));
      toast(t("{path} 업로드됨", { path }), "success");
      refresh();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
      if (upload.current) upload.current.value = "";
    }
  };

  const download = () => {
    if (!open) return;
    sandboxApi.readFile(s.id, open.path).then((r) => {
      const url = URL.createObjectURL(new Blob([b64ToBytes(r.content_base64) as BlobPart]));
      const a = document.createElement("a");
      a.href = url;
      a.download = open.path.split("/").pop() || "file";
      a.click();
      URL.revokeObjectURL(url);
    }, setError);
  };

  if (open) {
    return (
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="ghost" onClick={() => setOpen(null)}>
            <ArrowUp /> {t("목록")}
          </Button>
          <code className="min-w-0 flex-1 truncate font-mono text-sm">{open.path}</code>
          <Button size="sm" variant="outline" onClick={download}>
            {t("다운로드")}
          </Button>
          {!open.binary && (
            <Button size="sm" variant="primary" loading={busy} disabled={draft === open.text} onClick={() => save(open.path, draft).then((ok) => ok && setOpen({ ...open, text: draft }))} {...lock(canEdit)}>
              <Save /> {t("저장")}
            </Button>
          )}
        </div>
        {open.truncated && <p className="text-xs text-warn">{t("2 MiB까지만 읽었습니다. 저장하면 잘린 내용으로 덮어씁니다.")}</p>}
        <ErrorBox error={error} />
        {open.binary ? (
          <p className="text-sm text-gray-500">{t("바이너리 파일이라 편집할 수 없습니다. 다운로드해서 확인하세요.")}</p>
        ) : (
          <Textarea mono rows={18} value={draft} onChange={(e) => setDraft(e.target.value)} aria-label={t("{path} 내용", { path: open.path })} readOnly={!canEdit} className="w-full" />
        )}
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <ErrorBox error={error ?? files.error} />
      <div className="overflow-hidden rounded-lg border border-gray-200 bg-secondaryBg">
        <form
          className="flex items-center gap-2 border-b border-gray-200 px-2 py-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (dirInput.startsWith("/")) setDir(dirInput);
          }}
        >
          <IconButton label={t("상위 디렉터리")} size="sm" onClick={() => setDir(parentOf(dir))} disabled={dir === "/"}>
            <ArrowUp />
          </IconButton>
          <Input mono size="sm" value={dirInput} onChange={(e) => setDirInput(e.target.value)} aria-label={t("디렉터리 경로")} className="min-w-0 flex-1" />
          <IconButton label={t("새로 고침")} size="sm" onClick={refresh}>
            <RefreshCcw />
          </IconButton>
          <input ref={upload} type="file" className="hidden" onChange={(e) => onUpload(e.target.files?.[0])} aria-label={t("업로드할 파일")} />
          <Button size="sm" variant="outline" onClick={() => upload.current?.click()} loading={busy} {...lock(canEdit)}>
            <Upload /> {t("업로드")}
          </Button>
        </form>
        {files.isPending ? (
          <div className="flex justify-center py-8">
            <Spinner />
          </div>
        ) : files.data && files.data.entries.length === 0 ? (
          <p className="py-8 text-center text-sm text-gray-500">{t("빈 디렉터리입니다.")}</p>
        ) : (
          <ul className="divide-y divide-gray-200">
            {[...(files.data?.entries ?? [])]
              .sort((a, b) => (a.type === "dir") === (b.type === "dir") ? a.name.localeCompare(b.name) : a.type === "dir" ? -1 : 1)
              .map((f) => (
                <li key={f.path}>
                  <button
                    type="button"
                    onClick={() => (f.type === "dir" ? setDir(f.path) : openFile(f.path))}
                    className="flex w-full cursor-pointer items-center gap-3 px-4 py-2 text-left text-sm hover:bg-gray-100"
                  >
                    {f.type === "dir" ? <Folder className="size-4 shrink-0 text-gray-500" /> : <FileIcon className="size-4 shrink-0 text-gray-400" />}
                    <span className="min-w-0 flex-1 truncate font-mono">
                      {f.name}
                      {f.type === "dir" ? "/" : f.type === "symlink" ? " ↪" : ""}
                    </span>
                    {f.type === "file" && <span className="text-xs tabular-nums text-gray-500">{fmtBytes(f.size)}</span>}
                    <span className="hidden w-32 text-right text-xs tabular-nums text-gray-400 sm:block">{fmtRelative(f.mod_time)}</span>
                  </button>
                </li>
              ))}
          </ul>
        )}
        <form
          className="flex items-center gap-2 border-t border-gray-200 px-2 py-2"
          onSubmit={async (e) => {
            e.preventDefault();
            const name = newName.trim();
            if (!name) return;
            const path = name.startsWith("/") ? name : `${dir.replace(/\/+$/, "")}/${name}`;
            if (await save(path, "")) {
              setNewName("");
              setOpen({ path, text: "", binary: false, truncated: false });
              setDraft("");
            }
          }}
        >
          <Input mono size="sm" value={newName} onChange={(e) => setNewName(e.target.value)} placeholder={t("새 파일 이름")} aria-label={t("새 파일 이름")} className="min-w-0 flex-1" />
          <Button size="sm" variant="outline" type="submit" disabled={!newName.trim()} {...lock(canEdit)}>
            <Plus /> {t("새 파일")}
          </Button>
        </form>
      </div>
    </div>
  );
}

/* ── Detail ───────────────────────────────────────────────────────────── */

type DetailTab = "terminal" | "exec" | "files";

function SandboxDetail({
  s,
  canEdit,
  onFork,
  onDestroyed,
}: {
  s: Sandbox;
  canEdit: boolean;
  onFork: () => void;
  onDestroyed: () => void;
}) {
  const qc = useQueryClient();
  const [tab, setTab] = useState<DetailTab>("terminal");
  const [cpName, setCpName] = useState("");
  const [cpOpen, setCpOpen] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const { confirm, node: confirmNode } = useConfirm();
  const running = s.status === "running";

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: sbxKeys.list(s.environment_id) });
    qc.invalidateQueries({ queryKey: sbxKeys.checkpoints(s.environment_id) });
  };

  const act = async (key: string, f: () => Promise<unknown>, done?: string) => {
    setBusy(key);
    setError(null);
    try {
      await f();
      if (done) toast(done, "success");
      invalidate();
      return true;
    } catch (err) {
      setError(err);
      return false;
    } finally {
      setBusy(null);
    }
  };

  return (
    <section aria-label={t("샌드박스 {id}", { id: s.id })} className="min-w-0 space-y-5">
      {confirmNode}
      <div className="flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1">
          <h2 className="flex items-center gap-2 font-mono text-lg font-semibold">
            {s.id}
            <CopyButton value={s.id} label="ID" />
          </h2>
          <p className="mt-1 text-sm text-gray-600">
            <code className="font-mono">{s.image}</code>
            {s.checkpoint_name && <> · checkpoint {s.checkpoint_name}</>}
            {s.parent_id && <> · {t("{id}에서 fork", { id: s.parent_id })}</>}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            loading={busy === "hb"}
            disabled={!running}
            title={t("명령을 실행하지 않고 유휴 타이머를 다시 시작합니다")}
            onClick={() => act("hb", () => sandboxApi.heartbeat(s.id), t("유휴 타이머를 다시 시작했습니다"))}
            {...lock(canEdit)}
          >
            <HeartPulse /> {t("유지")}
          </Button>
          <Button size="sm" variant="outline" disabled={!running} onClick={onFork} {...lock(canEdit)}>
            <GitFork /> Fork
          </Button>
          <Button size="sm" variant="outline" disabled={!running} onClick={() => setCpOpen(true)} {...lock(canEdit)}>
            <Camera /> Checkpoint
          </Button>
          <Button
            size="sm"
            variant="danger"
            onClick={() =>
              confirm({
                title: t("샌드박스 삭제"),
                message: (
                  <p className="text-sm text-gray-600">
                    {tr("{id}의 컨테이너와 파일시스템을 지웁니다. 되돌릴 수 없습니다(checkpoint는 남습니다).", { id: <code>{s.id}</code> })}
                  </p>
                ),
                confirmLabel: t("삭제"),
                danger: true,
                run: async () => {
                  if (await act("rm", () => sandboxApi.destroy(s.id), t("샌드박스를 삭제했습니다"))) onDestroyed();
                },
              })
            }
            {...lock(canEdit)}
          >
            <Trash2 /> {t("삭제")}
          </Button>
        </div>
      </div>

      <dl className="grid grid-cols-2 gap-x-6 gap-y-4 rounded-lg border border-gray-200 bg-secondaryBg px-4 py-3.5 text-sm sm:grid-cols-4">
        {[
          [t("노드"), s.node_id],
          [t("네트워크"), s.network_mode === "private" ? t("비공개 네트워크") : t("격리")],
          [t("리소스"), `${s.cpu_cores} vCPU · ${s.memory_mb} MiB`],
          [t("유휴 제한"), s.idle_timeout_minutes ? t("{minutes}분", { minutes: s.idle_timeout_minutes }) : t("끔")],
          [t("만든 시각"), fmtTime(s.created_at)],
          [t("마지막 활동"), fmtRelative(s.last_activity_at)],
          [t("삭제 예정"), s.expires_at ? `${fmtTime(s.expires_at)} (${fmtUntil(s.expires_at)})` : t("없음")],
          [t("컨테이너"), s.container_name],
        ].map(([k, v]) => (
          <div key={k} className="min-w-0">
            <dt className="text-xs text-gray-500">{k}</dt>
            <dd className="truncate tabular-nums" title={v}>
              {v}
            </dd>
          </div>
        ))}
      </dl>
      {s.network_mode === "private" && (
        <p className="text-xs text-gray-500">
          {tr("같은 환경의 서비스는 {name} 이름으로 이 샌드박스에 닿을 수 있습니다.", { name: <code>{s.id.replace("_", "-")}</code> })}
        </p>
      )}
      <ErrorBox error={error} />

      <Segmented
        label={t("샌드박스 보기")}
        value={tab}
        onChange={setTab}
        options={[
          { value: "terminal", label: t("터미널") },
          { value: "exec", label: t("명령 실행") },
          { value: "files", label: t("파일") },
        ]}
      />
      {!running ? (
        <p className="text-sm text-gray-500">{t("샌드박스를 만드는 중입니다…")}</p>
      ) : tab === "terminal" ? (
        <SandboxTerminal id={s.id} disabled={!canEdit} />
      ) : tab === "exec" ? (
        <CommandRunner s={s} canEdit={canEdit} />
      ) : (
        <FileBrowser key={s.id} s={s} canEdit={canEdit} />
      )}

      <Dialog
        open={cpOpen}
        onClose={() => busy !== "cp" && setCpOpen(false)}
        title={t("Checkpoint 저장")}
        footer={
          <>
            <Button variant="outline" onClick={() => setCpOpen(false)} disabled={busy === "cp"}>
              {t("취소")}
            </Button>
            <Button variant="primary" type="submit" form="sbx-cp-form" loading={busy === "cp"} disabled={!cpName.trim()}>
              {t("저장")}
            </Button>
          </>
        }
      >
        <form
          id="sbx-cp-form"
          className="space-y-3"
          onSubmit={async (e) => {
            e.preventDefault();
            if (await act("cp", () => sandboxApi.checkpoint(s.id, cpName.trim()), t("checkpoint {name}를 저장했습니다", { name: cpName.trim() }))) {
              setCpOpen(false);
              setCpName("");
            }
          }}
        >
          <p className="text-sm text-gray-600">
            {t("지금 파일시스템을 이름 붙여 저장합니다. 샌드박스는 계속 실행되고(저장하는 동안 잠깐 멈춥니다), 나중에 이 이름으로 새 샌드박스를 띄울 수 있습니다. 같은 이름이 있으면 새로 저장한 것으로 바뀝니다.")}
          </p>
          <Field label={t("이름")} hint={t("소문자, 숫자, . _ - (64자까지)")}>
            <Input mono value={cpName} onChange={(e) => setCpName(e.target.value.toLowerCase())} placeholder="after-deps" autoFocus />
          </Field>
          <ErrorBox error={error} />
        </form>
      </Dialog>
    </section>
  );
}

/* ── Checkpoints ──────────────────────────────────────────────────────── */

function Checkpoints({ eid, canEdit, onBoot }: { eid: ID; canEdit: boolean; onBoot: (c: SandboxCheckpoint) => void }) {
  const qc = useQueryClient();
  const list = useSandboxCheckpoints(eid);
  const { confirm, node } = useConfirm();
  const [renaming, setRenaming] = useState<SandboxCheckpoint | null>(null);
  const [name, setName] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const refresh = () => qc.invalidateQueries({ queryKey: sbxKeys.checkpoints(eid) });

  return (
    <Section title="Checkpoints" description={t("샌드박스 파일시스템을 이름 붙여 저장한 것입니다. 원본을 지워도 남고, 저장한 노드에서 새 샌드박스로 띄울 수 있습니다.")}>
      {node}
      <ErrorBox error={list.error} />
      {list.isPending ? (
        <Spinner />
      ) : !list.data?.length ? (
        <EmptyState icon={<Camera className="size-6" />} title={t("저장된 checkpoint가 없습니다")}>
          {tr("샌드박스에서 {checkpoint}를 누르거나 {cli}을 실행하세요.", {
            checkpoint: <b className="font-medium text-gray-600">Checkpoint</b>,
            cli: <code className="font-mono">aven sandbox checkpoint create &lt;name&gt;</code>,
          })}
        </EmptyState>
      ) : (
        <RowList>
          {list.data.map((c) => (
            <li key={c.id} className="flex flex-wrap items-center gap-3 px-4 py-3 text-sm">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-gray-100 text-gray-500">
                <Camera className="size-4" />
              </span>
              <span className="min-w-0 flex-1">
                <span className="block truncate font-mono font-medium">{c.name}</span>
                <span className="block text-xs tabular-nums text-gray-500">
                  <span className="font-mono">{c.node_id}</span> · {fmtRelative(c.created_at)}
                </span>
              </span>
              <Button size="sm" variant="outline" onClick={() => onBoot(c)} {...lock(canEdit)}>
                <Play /> {t("띄우기")}
              </Button>
              <IconButton
                label={t("이름 바꾸기")}
                size="sm"
                onClick={() => {
                  setRenaming(c);
                  setName(c.name);
                  setError(null);
                }}
                {...lock(canEdit)}
              >
                <Pencil />
              </IconButton>
              <IconButton
                label={t("삭제")}
                size="sm"
                onClick={() =>
                  confirm({
                    title: t("Checkpoint 삭제"),
                    message: <p className="text-sm text-gray-600">{t("{name}와 저장된 파일시스템 이미지를 지웁니다.", { name: c.name })}</p>,
                    confirmLabel: t("삭제"),
                    danger: true,
                    run: async () => {
                      try {
                        await sandboxApi.deleteCheckpoint(c.id);
                        toast(t("checkpoint {name}를 삭제했습니다", { name: c.name }), "success");
                      } catch (err) {
                        toast(errorMessage(err), "error");
                      }
                      refresh();
                    },
                  })
                }
                {...lock(canEdit)}
              >
                <Trash2 />
              </IconButton>
            </li>
          ))}
        </RowList>
      )}
      <Dialog
        open={!!renaming}
        onClose={() => !busy && setRenaming(null)}
        title={t("Checkpoint 이름 바꾸기")}
        footer={
          <>
            <Button variant="outline" onClick={() => setRenaming(null)} disabled={busy}>
              {t("취소")}
            </Button>
            <Button variant="primary" type="submit" form="sbx-rename-form" loading={busy} disabled={!name.trim() || name === renaming?.name}>
              {t("바꾸기")}
            </Button>
          </>
        }
      >
        <form
          id="sbx-rename-form"
          className="space-y-3"
          onSubmit={async (e) => {
            e.preventDefault();
            if (!renaming) return;
            setBusy(true);
            setError(null);
            try {
              await sandboxApi.renameCheckpoint(renaming.id, name.trim());
              setRenaming(null);
              refresh();
            } catch (err) {
              setError(err);
            } finally {
              setBusy(false);
            }
          }}
        >
          <Field label={t("새 이름")}>
            <Input mono value={name} onChange={(e) => setName(e.target.value.toLowerCase())} autoFocus />
          </Field>
          <ErrorBox error={error} />
        </form>
      </Dialog>
    </Section>
  );
}

const CLI_EXAMPLES: [string, string][] = [
  ["aven sandbox create --idle-timeout-minutes 30", t("만들기")],
  ['aven sandbox exec -- "npm install && npm test"', t("명령 실행")],
  ["aven sandbox checkpoint create after-deps", t("Checkpoint 저장")],
  ["aven sandbox create --checkpoint after-deps", t("Checkpoint에서 띄우기")],
  ["aven sandbox destroy", t("삭제")],
];

/* ── Page body ────────────────────────────────────────────────────────── */

export function SandboxesView({
  pid,
  eid,
  envName,
  selected,
  onSelect,
}: {
  pid: ID | null;
  eid: ID;
  envName: string;
  selected: ID | null;
  onSelect: (id: ID | null) => void;
}) {
  const qc = useQueryClient();
  const caps = useCaps(pid);
  const list = useSandboxes(eid);
  const checkpoints = useSandboxCheckpoints(eid);
  const [dialog, setDialog] = useState<Mode | null>(null);
  const items = useMemo(() => list.data ?? [], [list.data]);
  const current = items.find((s) => s.id === selected) ?? null;

  // 목록에서 본 적 있는 선택 샌드박스가 없어지면(삭제/유휴 만료) 선택을 푼다. 막 만든 것은 목록이 갱신되기 전이라 건드리지 않는다.
  const seen = useRef<string | null>(null);
  useEffect(() => {
    if (current) seen.current = current.id;
    else if (selected && seen.current === selected && list.data) {
      seen.current = null;
      onSelect(null);
    }
  }, [selected, list.data, current, onSelect]);

  const created = (s: Sandbox) => {
    setDialog(null);
    qc.invalidateQueries({ queryKey: sbxKeys.list(eid) });
    toast(t("샌드박스 {id}를 만들었습니다", { id: s.id }), "success");
    onSelect(s.id);
  };

  return (
    <div className="space-y-8">
      <Section
        title={list.data ? t("샌드박스 {count}개", { count: items.length }) : t("샌드박스")}
        description={tr("{env} 환경에서 쓰는 일회용 격리 컨테이너입니다. 명령 실행, 파일 읽기/쓰기, fork와 checkpoint를 할 수 있고, 유휴 제한 동안 쓰지 않으면 자동으로 삭제됩니다.", {
          env: <span className="font-medium text-foreground">{envName}</span>,
        })}
        action={
          <Button variant="primary" size="sm" onClick={() => setDialog({ kind: "create" })} {...lock(caps.canEdit)}>
            <Plus /> {t("새 샌드박스")}
          </Button>
        }
      >
      <ErrorBox error={list.error} />
      {list.isPending ? (
        <div className="flex justify-center py-10">
          <Spinner />
        </div>
      ) : items.length === 0 ? (
        <EmptyState
          icon={<Box className="size-6" />}
          title={t("샌드박스가 없습니다")}
          action={
            <Button variant="primary" onClick={() => setDialog({ kind: "create" })} {...lock(caps.canEdit)}>
              <Plus /> {t("새 샌드박스")}
            </Button>
          }
        >
          {tr("에이전트 작업, 의존성 설치, 테스트처럼 격리해서 돌릴 일을 위한 컨테이너입니다. CLI: {cli}", {
            cli: <code className="font-mono">aven sandbox create</code>,
          })}
        </EmptyState>
      ) : (
        <div className="grid gap-6 lg:grid-cols-[minmax(240px,320px)_1fr]">
          <nav aria-label={t("샌드박스 목록")}>
            <RowList>
              {items.map((s) => (
                <li key={s.id}>
                  <button
                    type="button"
                    aria-current={s.id === selected ? "true" : undefined}
                    onClick={() => onSelect(s.id)}
                    className={cx(
                      "flex w-full cursor-pointer flex-col gap-1 px-3 py-2.5 text-left hover:bg-gray-100",
                      s.id === selected && "bg-gray-100 shadow-[inset_2px_0_0] shadow-pink-500",
                    )}
                  >
                    <span className="flex items-center gap-2">
                      <span className="min-w-0 flex-1 truncate font-mono text-sm font-medium">{s.id}</span>
                      <StatusDot s={s} />
                    </span>
                    <span className="truncate text-xs text-gray-500">
                      {s.image}
                      {s.network_mode === "private" ? t(" · 비공개 네트워크") : ""}
                    </span>
                    <span className="text-xs tabular-nums text-gray-400">
                      {fmtRelative(s.created_at)} · {idleLabel(s)}
                    </span>
                  </button>
                </li>
              ))}
            </RowList>
          </nav>
          {current ? (
            <SandboxDetail
              key={current.id}
              s={current}
              canEdit={caps.canEdit}
              onFork={() => setDialog({ kind: "fork", source: current })}
              onDestroyed={() => onSelect(null)}
            />
          ) : (
            <div className="flex items-center justify-center rounded-lg border border-dashed border-gray-200 p-10 text-sm text-gray-500">
              {t("왼쪽에서 샌드박스를 고르세요.")}
            </div>
          )}
        </div>
      )}
      </Section>

      <Checkpoints eid={eid} canEdit={caps.canEdit} onBoot={(c) => setDialog({ kind: "checkpoint", checkpoint: c })} />

      <Section title={t("CLI와 SDK")} description={t("터미널이나 코딩 에이전트에서도 같은 샌드박스를 다룰 수 있습니다.")}>
        <RowList>
          {CLI_EXAMPLES.map(([cmd, what]) => (
            <li key={cmd} className="flex items-center gap-4 py-1.5 pl-4 pr-1.5">
              <span className="w-36 shrink-0 text-sm text-gray-600">{what}</span>
              <code className="min-w-0 flex-1 truncate font-mono text-sm">{cmd}</code>
              <CopyButton value={cmd} label={t("{cmd} 복사", { cmd })} />
            </li>
          ))}
        </RowList>
        <div className="mt-4 flex flex-wrap items-center gap-x-2 gap-y-2 text-sm text-gray-600">
          <span>
            {tr("TypeScript SDK는 저장소의 {dir}에 있습니다. 필요한 변수:", { dir: <code className="font-mono">sdk/</code> })}
          </span>
          {["AVEN_URL", "AVEN_TOKEN"].map((k) => (
            <code key={k} className="rounded-md bg-gray-100 px-1.5 py-0.5 font-mono text-xs text-gray-700">
              {k}
            </code>
          ))}
          <code className="flex items-center gap-1 rounded-md bg-gray-100 py-0.5 pl-1.5 pr-0.5 font-mono text-xs text-gray-700">
            AVEN_ENVIRONMENT_ID={eid}
          </code>
          <CopyButton value={`AVEN_ENVIRONMENT_ID=${eid}`} label={t("AVEN_ENVIRONMENT_ID 복사")} />
        </div>
      </Section>

      <SandboxDialog mode={dialog} eid={eid} checkpoints={checkpoints.data ?? []} onClose={() => setDialog(null)} onCreated={created} />
    </div>
  );
}
