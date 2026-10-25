"use client";

import { useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowUpCircle, Box, ChevronDown, GitBranch, PenLine } from "lucide-react";
import { api, type CanvasService, type Environment, type ImageUpdate } from "@/lib/api";
import { t } from "@/lib/i18n";
import { qk, useCaps, useImageUpdates } from "@/lib/queries";
import { BrandIcon } from "../../ServiceIcon";
import { Button, ErrorBox, Input, Menu, MenuItem, cx } from "../../ui";
import { CARD } from "./primitives";

/* Source image / source repo card, modeled on Railway's dashboard (`min-h-[66px]` raised surface: icon, name link, sub-link, pencil, Disconnect)
   and the pink update strip under it. */

const tagOf = (image: string) => {
  const noDigest = image.split("@")[0];
  const i = noDigest.lastIndexOf(":");
  return i > noDigest.lastIndexOf("/") ? noDigest.slice(i + 1) : "latest";
};

const semver = (tag: string) => {
  const m = /^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?/.exec(tag);
  return m ? [Number(m[1]), Number(m[2] ?? 0), Number(m[3] ?? 0)] : null;
};

/** "패치 업데이트" / "마이너 업데이트" / "메이저 업데이트" / "새 이미지" by comparing the tags. */
export function updateKind(u: ImageUpdate) {
  if (u.current_image === u.target_image) return t("새 이미지 사용 가능");
  const a = semver(tagOf(u.current_image));
  const b = semver(tagOf(u.target_image));
  if (!a || !b) return t("업데이트 사용 가능");
  if (b[0] !== a[0]) return t("메이저 업데이트 사용 가능");
  if (b[1] !== a[1]) return t("마이너 업데이트 사용 가능");
  return t("패치 업데이트 사용 가능");
}

function imageHref(image: string) {
  const name = image.split("@")[0].replace(/:[^/:]+$/, "");
  const first = name.split("/")[0];
  if (name.includes("/") && (first.includes(".") || first.includes(":") || first === "localhost")) return null; // private / other registry
  return name.includes("/") ? `https://hub.docker.com/r/${name}` : `https://hub.docker.com/_/${name}`;
}
export function repoName(url: string) {
  return url.replace(/^(https?:\/\/|ssh:\/\/)?(git@)?github\.com[/:]/, "").replace(/\.git$/, "");
}
/** Shared shell: the raised card row with icon, title (link), optional sub line and the right-hand actions. */
export function SourceRow({
  icon,
  title,
  href,
  sub,
  actions,
  flatBottom,
}: {
  icon: ReactNode;
  title: string;
  href?: string | null;
  sub?: ReactNode;
  actions: ReactNode;
  flatBottom?: boolean;
}) {
  return (
    <div className={cx(CARD, "flex min-h-[66px] items-center gap-3 overflow-x-hidden", flatBottom && "rounded-b-none")}>
      <span className="icon-container icon-md shrink-0 text-2xl text-gray-500" aria-hidden>
        {icon}
      </span>
      <div className="flex-grow truncate">
        <p className="group space-x-2">
          {href ? (
            <a href={href} target="_blank" rel="noreferrer" className="flex min-w-0 flex-grow items-center space-x-3 focus:outline-none focus-visible:ring-2 focus-visible:ring-pink-700">
              <span className="truncate group-hover:underline" title={title}>
                {title}
              </span>
              <span aria-hidden className="hidden text-gray-500 group-hover:inline">
                ↗
              </span>
            </a>
          ) : (
            <span className="truncate" title={title}>
              {title}
            </span>
          )}
        </p>
        {sub}
      </div>
      <div className="flex items-center space-x-2">{actions}</div>
    </div>
  );
}
export function PencilButton({ onClick, label = t("수정") }: { onClick: () => void; label?: string }) {
  return (
    <Button variant="ghost" title={label} aria-label={label} onClick={onClick} className="w-[34px] px-0">
      <span className="text-gray-500">
        <PenLine />
      </span>
    </Button>
  );
}

export function DisconnectButton({ onClick, disabledReason, children = t("연결 해제") }: { onClick?: () => void; disabledReason?: string; children?: ReactNode }) {
  return (
    <Button size="sm" variant="outline" onClick={onClick} disabled={!!disabledReason} title={disabledReason} className="dark:text-white">
      <span className="inline-block min-w-0">{children}</span>
    </Button>
  );
}

export function SourceEdit({
  value,
  placeholder,
  label,
  hint,
  onCancel,
  onSave,
}: {
  value: string;
  placeholder: string;
  label: string;
  hint?: ReactNode;
  onCancel: () => void;
  onSave: (v: string) => void;
}) {
  const [v, setV] = useState(value);
  return (
    <form
      className={cx(CARD, "space-y-4 py-4")}
      onSubmit={(e) => {
        e.preventDefault();
        if (v.trim()) onSave(v.trim());
      }}
    >
      <Input autoFocus mono value={v} onChange={(e) => setV(e.target.value)} placeholder={placeholder} aria-label={label} className="!text-base" />
      {hint}
      <div className="flex items-center justify-end space-x-2">
        <Button variant="outline" onClick={onCancel}>
          {t("취소")}
        </Button>
        <Button type="submit" variant="primary" disabled={!v.trim() || v.trim() === value}>
          {t("업데이트")}
        </Button>
      </div>
    </form>
  );
}

const NO_DISCONNECT = t("aven에서는 서비스의 소스 종류를 바꿀 수 없습니다. 다른 소스를 쓰려면 서비스를 새로 만드세요.");

/** Source Image card with the auto-update strip. `onConfigure` toggles the auto update settings below. */
export function ImageSourceCard({
  svc,
  env,
  image,
  onImage,
  onConfigure,
  configureOpen,
  hint,
}: {
  svc: CanvasService;
  env: Environment;
  image: string;
  onImage: (v: string) => void;
  onConfigure?: () => void;
  configureOpen?: boolean;
  hint?: ReactNode;
}) {
  const qc = useQueryClient();
  const canEdit = useCaps(svc.project_id).canEdit;
  const updates = useImageUpdates(svc.id, svc.source_type === "image" ? env.id : "");
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const list = svc.source_type === "image" ? (updates.data ?? []) : [];
  const actionable = list.find((u) => u.status === "available" || u.status === "failed");
  const deploying = list.some((u) => u.status === "deploying");

  const run = async (key: string, fn: () => Promise<unknown>) => {
    setBusy(key);
    setError(null);
    try {
      const res = await fn();
      if (Array.isArray(res)) qc.setQueryData(qk.imageUpdates(svc.id, env.id), res);
      await qc.invalidateQueries({ queryKey: qk.imageUpdates(svc.id, env.id) });
      await qc.invalidateQueries({ queryKey: qk.canvas(env.id) });
      await qc.invalidateQueries({ queryKey: qk.deployments(svc.id, env.id) });
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  };
  if (editing)
    return (
      <SourceEdit
        value={image}
        placeholder="nginx:alpine"
        label={t("소스 이미지")}
        hint={hint}
        onCancel={() => setEditing(false)}
        onSave={(v) => {
          onImage(v);
          setEditing(false);
        }}
      />
    );
  return (
    <div className="grid">
      <SourceRow
        icon={<Box />}
        title={image || t("이미지 없음")}
        href={image ? imageHref(image) : null}
        flatBottom={!!actionable}
        sub={
          onConfigure ? (
            <button type="button" onClick={onConfigure} aria-expanded={configureOpen} className="group flex cursor-pointer items-center gap-1 text-xs text-gray-500">
              <span className="group-hover:underline">{t("자동 업데이트 설정")}</span>
            </button>
          ) : null
        }
        actions={
          <>
            <PencilButton onClick={() => setEditing(true)} label={t("이미지 수정")} />
            <DisconnectButton disabledReason={NO_DISCONNECT} />
          </>
        }
      />
      {actionable && (
        <div className="flex items-center justify-between rounded-b-md border border-t-0 border-pink-100 bg-pink-50 px-4 py-1.5">
          <div className="flex min-w-0 items-center space-x-3">
            <span className="icon-container icon-md shrink-0 text-2xl text-pink-500" aria-hidden>
              <ArrowUpCircle />
            </span>
            <h3 className="truncate text-sm font-medium text-pink-800" title={actionable.error ? t("이전 시도 실패: {error}", { error: actionable.error }) : undefined}>
              {actionable.status === "failed" ? t("업데이트 실패") : updateKind(actionable)}
            </h3>
          </div>
          <div className="flex items-center">
            <Button
              size="sm"
              variant="accent"
              className="min-w-max rounded-r-none border-r-0"
              disabled={!canEdit || !!busy || deploying}
              loading={busy === "apply"}
              onClick={() => void run("apply", () => api.applyImageUpdate(svc.id, env.id, actionable.id))}
            >
              <span className="inline-block min-w-0">
                {actionable.status === "failed"
                  ? t("다시 시도")
                  : actionable.current_image === actionable.target_image
                    ? t("새 이미지로 다시 배포")
                    : t("{tag} 버전으로 업그레이드", { tag: tagOf(actionable.target_image) })}
              </span>
            </Button>
            <Menu
              label={t("이미지 업데이트")}
              trigger={(p) => (
                <Button {...p} size="sm" variant="accent" aria-label={t("이미지 업데이트 메뉴")} className="w-[26px] min-w-0 rounded-l-none px-2 focus:ring-0 focus-visible:ring-0">
                  <ChevronDown className="!size-[14px] text-pink-500" />
                </Button>
              )}
            >
              {(close) => (
                <>
                  <MenuItem
                    disabled={!canEdit || !!busy}
                    onSelect={() => {
                      close();
                      void run("skip", () => api.skipImageUpdate(svc.id, env.id, actionable.id));
                    }}
                  >
                    {t("이 버전 건너뛰기")}
                  </MenuItem>
                  {onConfigure && (
                    <MenuItem
                      onSelect={() => {
                        close();
                        if (!configureOpen) onConfigure();
                      }}
                    >
                      {t("자동 업데이트 설정")}
                    </MenuItem>
                  )}
                </>
              )}
            </Menu>
          </div>
        </div>
      )}
      {error ? <ErrorBox className="mt-2" error={error} /> : null}
    </div>
  );
}
/** Source Repo card (GitHub). */
export function RepoSourceCard({ url, onUrl }: { url: string; onUrl: (v: string) => void }) {
  const [editing, setEditing] = useState(false);
  if (editing)
    return (
      <SourceEdit
        value={url}
        placeholder="https://github.com/user/repo"
        label={t("소스 저장소")}
        onCancel={() => setEditing(false)}
        onSave={(v) => {
          onUrl(v);
          setEditing(false);
        }}
      />
    );
  const gh = /github\.com[/:]/.test(url);
  const href = gh ? `https://github.com/${repoName(url)}` : /^https?:\/\//.test(url) ? url : null;
  return (
    <SourceRow
      icon={<BrandIcon name="github" className="size-5" />}
      title={url ? (gh ? repoName(url) : url) : t("저장소 없음")}
      href={href}
      actions={
        <>
          <PencilButton onClick={() => setEditing(true)} label={t("저장소 수정")} />
          <DisconnectButton disabledReason={NO_DISCONNECT} />
        </>
      }
    />
  );
}

export function BranchCard({
  branch,
  autoDeploy,
  onBranch,
  onAutoDeploy,
}: {
  branch: string;
  autoDeploy: boolean;
  onBranch: (v: string) => void;
  onAutoDeploy: (v: boolean) => void;
}) {
  const [editing, setEditing] = useState(false);
  if (editing)
    return (
      <SourceEdit
        value={branch}
        placeholder="main"
        label={t("브랜치")}
        onCancel={() => setEditing(false)}
        onSave={(v) => {
          onBranch(v);
          setEditing(false);
        }}
      />
    );
  return (
    <SourceRow
      icon={<GitBranch />}
      title={branch || "main"}
      sub={<p className="text-xs text-gray-500">{autoDeploy ? t("푸시하면 자동으로 배포합니다") : t("자동 배포가 꺼져 있습니다")}</p>}
      actions={
        <>
          <PencilButton onClick={() => setEditing(true)} label={t("브랜치 수정")} />
          <DisconnectButton onClick={() => onAutoDeploy(!autoDeploy)}>{autoDeploy ? t("연결 해제") : t("연결")}</DisconnectButton>
        </>
      }
    />
  );
}
