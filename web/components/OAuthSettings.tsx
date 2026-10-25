"use client";

import { useEffect, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AppWindow, KeyRound, Plus, ShieldCheck } from "lucide-react";
import { api, type ID, type OAuthApp, type OAuthAuthorizedApp, type OAuthClientSecret } from "@/lib/api";
import { useProjects } from "@/lib/queries";
import { fmtRelative } from "@/lib/format";
import { t } from "@/lib/i18n";
import { Button, Card, CopyField, Dialog, EmptyState, ErrorBox, Field, Input, Notice, RowList, Select, Textarea, useConfirm } from "@/components/ui";

const appsKey = ["oauth-apps"] as const;
const secretsKey = (id: string) => ["oauth-client-secrets", id] as const;
const grantsKey = ["oauth-authorized-apps"] as const;

export const OAUTH_SCOPE_LABELS: Record<string, string> = {
  openid: t("로그인(필수)"),
  email: t("이메일"),
  profile: t("이름"),
  offline_access: t("오프라인 접근(refresh token)"),
  "workspace:viewer": t("인스턴스 Viewer"),
  "workspace:member": t("인스턴스 Member"),
  "workspace:admin": t("인스턴스 Admin"),
  "project:viewer": t("프로젝트 Viewer"),
  "project:member": t("프로젝트 Member"),
};

const splitLines = (v: string) => v.split("\n").map((s) => s.trim()).filter(Boolean);

function ScopeChips({ scopes }: { scopes: string[] }) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {scopes.map((s) => (
        <span key={s} title={OAUTH_SCOPE_LABELS[s]} className="rounded bg-gray-100 px-1.5 py-0.5 font-mono text-xs text-gray-600">
          {s}
        </span>
      ))}
    </div>
  );
}

type Discovery = { issuer: string; authorization_endpoint: string; token_endpoint: string; userinfo_endpoint: string; jwks_uri: string; revocation_endpoint: string };

function Endpoints() {
  const d = useQuery({
    queryKey: ["oauth-discovery"],
    queryFn: async (): Promise<Discovery> => {
      const res = await fetch("/oauth/.well-known/openid-configuration");
      if (!res.ok) throw new Error(t("OpenID 설정을 불러오지 못했습니다"));
      return res.json();
    },
    staleTime: 5 * 60_000,
  });
  const rows: [string, string | undefined][] = [
    ["Issuer", d.data?.issuer],
    ["Discovery", d.data ? `${d.data.issuer}/.well-known/openid-configuration` : undefined],
    ["Authorization", d.data?.authorization_endpoint],
    ["Token", d.data?.token_endpoint],
    ["Userinfo", d.data?.userinfo_endpoint],
  ];
  return (
    <Card title={t("엔드포인트")} description={t("OAuth 2.0 / OpenID Connect 라이브러리에는 Issuer(또는 Discovery URL)만 넣으면 됩니다. 토큰을 받으려면 PKCE(S256)를 쓰세요.")}>
      <ErrorBox error={d.error} className="mb-3" />
      <div className="grid gap-3">
        {rows.map(([label, value]) => (
          <Field key={label} label={label}>
            {value ? <CopyField value={value} /> : <div className="h-11 animate-pulse rounded-md bg-gray-100" />}
          </Field>
        ))}
      </div>
    </Card>
  );
}

/** 인스턴스 설정의 "Developer"에 해당: OAuth 앱 등록/관리(인스턴스 admin). */
export function OAuthAppsSection() {
  const apps = useQuery({ queryKey: appsKey, queryFn: api.oauthApps });
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<{ app: OAuthApp; secret: string | null } | null>(null);
  return (
    <>
      <Card
        title={t("OAuth 앱")}
        description={t("\"Login with Aven\": 앱을 등록하면 사용자가 Aven 계정으로 로그인하고, 동의한 범위 안에서 앱이 Aven API를 쓸 수 있습니다. 앱의 권한은 승인한 scope와 사용자의 실제 Aven 역할 중 낮은 쪽을 따릅니다.")}
      />
      {created && (
        <div className="mt-8 max-w-3xl space-y-3 rounded-lg border border-gray-200 bg-secondaryBg p-4">
          <Notice onDismiss={() => setCreated(null)}>
            {created.secret ? t("앱을 만들었습니다. client secret은 지금 한 번만 표시되니 안전한 곳에 저장하세요.") : t("앱을 만들었습니다. 네이티브 앱은 client secret 없이 PKCE로 인증합니다.")}
          </Notice>
          <Field label="Client ID">
            <CopyField value={created.app.client_id} />
          </Field>
          {created.secret && (
            <Field label="Client secret">
              <CopyField value={created.secret} secret />
            </Field>
          )}
        </div>
      )}
      <Card
        className="mt-8 border-t border-gray-200 pt-8"
        title={t("등록한 앱")}
        description={t("인스턴스 admin만 앱을 만들고 관리할 수 있습니다.")}
        action={
          <Button variant="primary" size="sm" onClick={() => setCreating(true)}>
            <Plus className="size-4" />{t("새 OAuth 앱")}
          </Button>
        }
      >
        <ErrorBox error={apps.error} className="mb-4" />
        {apps.isPending ? null : !apps.data?.length ? (
          <EmptyState icon={<AppWindow className="size-5" />} title={t("등록한 OAuth 앱이 없습니다")}>
            {t("앱을 등록하면 client ID와 (웹 앱이면) client secret이 발급됩니다.")}
          </EmptyState>
        ) : (
          <div className="space-y-4">
            {apps.data.map((app) => (
              <OAuthAppRow key={app.id} app={app} />
            ))}
          </div>
        )}
      </Card>
      <Endpoints />
      <CreateOAuthAppDialog
        open={creating}
        onClose={() => setCreating(false)}
        onCreated={(v) => {
          setCreating(false);
          setCreated(v);
        }}
      />
    </>
  );
}

function CreateOAuthAppDialog({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: (v: { app: OAuthApp; secret: string | null }) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [clientType, setClientType] = useState<OAuthApp["client_type"]>("confidential");
  const [redirects, setRedirects] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    if (!open) return;
    setName("");
    setDescription("");
    setClientType("confidential");
    setRedirects("");
    setError(null);
  }, [open]);
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const res = await api.createOAuthApp({ name: name.trim(), description: description.trim(), client_type: clientType, redirect_uris: splitLines(redirects) });
      await qc.invalidateQueries({ queryKey: appsKey });
      onCreated({ app: res.app, secret: res.client_secret });
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={() => !busy && onClose()}
      title={t("새 OAuth 앱")}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" loading={busy} disabled={!name.trim() || !splitLines(redirects).length} onClick={submit}>
            {t("앱 만들기")}
          </Button>
        </>
      }
    >
      <form
        className="grid gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <Field label={t("앱 이름")} required hint={t("동의 화면에서 사용자에게 보이는 이름입니다.")}>
          <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={100} autoFocus />
        </Field>
        <Field label={t("설명")}>
          <Input value={description} onChange={(e) => setDescription(e.target.value)} maxLength={1000} placeholder={t("동의 화면에 표시할 설명")} />
        </Field>
        <Field
          label={t("앱 유형")}
          hint={clientType === "confidential" ? t("서버에서 client secret을 안전하게 보관하는 웹 앱입니다. PKCE를 함께 쓰는 것을 권장합니다.") : t("모바일·데스크톱·CLI·브라우저 SPA처럼 secret을 숨길 수 없는 앱입니다. PKCE가 필수이고 client secret은 없습니다.")}
        >
          <Select value={clientType} onChange={(e) => setClientType(e.target.value as OAuthApp["client_type"])}>
            <option value="confidential">{t("웹 앱 (confidential)")}</option>
            <option value="public">{t("네이티브 앱 (public)")}</option>
          </Select>
        </Field>
        <Field label="Redirect URI" required hint={t("한 줄에 하나씩. 인가 요청의 redirect_uri와 scheme·호스트·포트·경로까지 정확히 같아야 합니다. HTTP는 localhost만 허용합니다.")}>
          <Textarea
            rows={3}
            mono
            className="w-full"
            value={redirects}
            onChange={(e) => setRedirects(e.target.value)}
            placeholder={clientType === "public" ? "http://127.0.0.1:8765/callback\ncom.example.app:/oauth/callback" : "https://app.example.com/auth/callback\nhttp://localhost:3000/auth/callback"}
          />
        </Field>
        <ErrorBox error={error} />
      </form>
    </Dialog>
  );
}

function OAuthAppRow({ app }: { app: OAuthApp }) {
  const qc = useQueryClient();
  const confidential = app.client_type === "confidential";
  const secrets = useQuery({ queryKey: secretsKey(app.id), queryFn: () => api.oauthClientSecrets(app.id), enabled: confidential });
  const { confirm, node } = useConfirm();
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(app.name);
  const [description, setDescription] = useState(app.description);
  const [redirects, setRedirects] = useState(app.redirect_uris.join("\n"));
  const [newSecret, setNewSecret] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    setName(app.name);
    setDescription(app.description);
    setRedirects(app.redirect_uris.join("\n"));
  }, [app]);

  const run = async (f: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await f();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  const active = (secrets.data ?? []).filter((s) => !s.revoked_at);

  return (
    <article className="rounded-lg border border-gray-200 bg-secondaryBg p-4">
      {node}
      <div className="mb-4 flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1">
          <h4 className="truncate text-sm font-semibold">{app.name}</h4>
          <p className="text-xs tabular-nums text-gray-500">
            {confidential ? t("웹 앱") : t("네이티브 앱")} · {t("생성 {time}", { time: fmtRelative(app.created_at) })}
          </p>
        </div>
        <Button size="sm" variant="outline" onClick={() => setEditing((v) => !v)}>
          {editing ? t("편집 닫기") : t("편집")}
        </Button>
      </div>
      <div className="space-y-4">
        <Field label="Client ID">
          <CopyField value={app.client_id} />
        </Field>
        {!editing ? (
          <>
            {app.description && <p className="text-sm text-gray-600">{app.description}</p>}
            <div>
              <div className="mb-1 text-xs font-semibold text-gray-600">Redirect URI</div>
              <ul className="space-y-1 font-mono text-xs text-gray-600">
                {app.redirect_uris.map((uri) => (
                  <li key={uri} className="break-all">
                    {uri}
                  </li>
                ))}
              </ul>
            </div>
          </>
        ) : (
          <form
            className="space-y-3 border-t border-gray-200 pt-3"
            onSubmit={(e) => {
              e.preventDefault();
              run(async () => {
                await api.updateOAuthApp(app.id, { name: name.trim(), description: description.trim(), redirect_uris: splitLines(redirects) });
                setEditing(false);
                await qc.invalidateQueries({ queryKey: appsKey });
              });
            }}
          >
            <Field label={t("앱 이름")}>
              <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={100} required />
            </Field>
            <Field label={t("설명")}>
              <Input value={description} onChange={(e) => setDescription(e.target.value)} maxLength={1000} />
            </Field>
            <Field label="Redirect URI" hint={t("한 줄에 하나씩, 최대 10개.")}>
              <Textarea rows={3} mono className="w-full" value={redirects} onChange={(e) => setRedirects(e.target.value)} required />
            </Field>
            <Button type="submit" variant="primary" size="sm" loading={busy}>
              {t("변경 저장")}
            </Button>
          </form>
        )}
        {confidential && (
          <div className="border-t border-gray-200 pt-4">
            <div className="mb-2 flex items-center gap-2 text-sm font-medium">
              <KeyRound className="size-4" />
              Client secret
              <span className="text-xs font-normal text-gray-500">{t("만료되지 않습니다. 교체할 때는 새 secret을 배포한 뒤 이전 것을 폐기하세요.")}</span>
            </div>
            {newSecret && (
              <div className="mb-3 space-y-2">
                <Notice onDismiss={() => setNewSecret(null)}>{t("새 client secret은 지금 한 번만 표시됩니다.")}</Notice>
                <CopyField value={newSecret} secret />
              </div>
            )}
            <ErrorBox error={secrets.error} className="mb-2" />
            {active.length > 0 && (
              <RowList className="mb-3">
                {active.map((secret: OAuthClientSecret) => (
                  <li key={secret.id} className="flex items-center gap-3 px-3 py-2 text-sm">
                    <span className="min-w-0 flex-1 truncate font-mono text-xs text-gray-600">{secret.id}</span>
                    <span className="text-xs tabular-nums text-gray-500">{t("생성 {time}", { time: fmtRelative(secret.created_at) })}</span>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() =>
                        confirm({
                          title: t("Client secret 폐기"),
                          message: t("이 secret으로 하는 토큰 요청은 즉시 실패합니다. 다른 secret을 먼저 배포했는지 확인하세요."),
                          confirmLabel: t("폐기"),
                          danger: true,
                          run: async () => {
                            await api.revokeOAuthClientSecret(app.id, secret.id);
                            await qc.invalidateQueries({ queryKey: secretsKey(app.id) });
                          },
                        })
                      }
                    >
                      {t("폐기")}
                    </Button>
                  </li>
                ))}
              </RowList>
            )}
            {!secrets.isPending && active.length === 0 && <p className="mb-3 text-xs text-danger">{t("활성 client secret이 없어 토큰을 발급할 수 없습니다.")}</p>}
            <Button
              size="sm"
              variant="outline"
              loading={busy}
              onClick={() =>
                run(async () => {
                  const res = await api.createOAuthClientSecret(app.id);
                  setNewSecret(res.secret);
                  await qc.invalidateQueries({ queryKey: secretsKey(app.id) });
                })
              }
            >
              {t("새 secret 만들기")}
            </Button>
          </div>
        )}
        <ErrorBox error={error} />
        <div className="flex flex-wrap items-center gap-3 border-t border-gray-200 pt-4">
          <p className="min-w-0 flex-1 text-xs text-gray-500">{t("앱을 삭제하면 이 앱으로 발급한 모든 토큰이 즉시 무효가 되고 사용자의 승인된 앱 목록에서도 사라집니다.")}</p>
          <Button
            size="sm"
            variant="danger"
            onClick={() =>
              confirm({
                title: t("OAuth 앱 삭제"),
                message: t("{name}을(를) 삭제합니다. 되돌릴 수 없고, 발급된 모든 access token과 refresh token이 즉시 폐기됩니다.", { name: app.name }),
                confirmLabel: t("앱 삭제"),
                danger: true,
                run: async () => {
                  await api.deleteOAuthApp(app.id);
                  await qc.invalidateQueries({ queryKey: appsKey });
                },
              })
            }
          >
            {t("앱 삭제")}
          </Button>
        </div>
      </div>
    </article>
  );
}

/** 계정 설정 → 승인된 앱: 내가 승인한 앱과 권한, 공유한 프로젝트 변경, 접근 폐기. */
export function AuthorizedAppsSection() {
  const apps = useQuery({ queryKey: grantsKey, queryFn: api.oauthAuthorizedApps });
  return (
    <Card
      title={t("승인된 앱")}
      description={t("Aven 계정으로 로그인을 허용한 앱입니다. 접근을 폐기하면 그 앱의 모든 토큰이 즉시 무효가 됩니다. 다시 연결하면 새로 동의를 받습니다.")}
    >
      <ErrorBox error={apps.error} className="mb-4" />
      {apps.isPending ? null : !apps.data?.length ? (
        <EmptyState icon={<ShieldCheck className="size-5" />} title={t("승인된 앱이 없습니다")} />
      ) : (
        <div className="space-y-4">
          {apps.data.map((app) => (
            <AuthorizedAppRow key={app.grant_id} app={app} />
          ))}
        </div>
      )}
    </Card>
  );
}

function AuthorizedAppRow({ app }: { app: OAuthAuthorizedApp }) {
  const qc = useQueryClient();
  const projects = useProjects();
  const { confirm, node } = useConfirm();
  const [editing, setEditing] = useState(false);
  const hasProjectScope = app.scopes.some((s) => s.startsWith("project:"));
  const names = new Map((projects.data ?? []).map((p) => [p.id, p.name]));
  const level = (prefix: string) => app.scopes.filter((s) => s.startsWith(prefix)).map((s) => OAUTH_SCOPE_LABELS[s] ?? s);
  return (
    <article className="rounded-lg border border-gray-200 bg-secondaryBg p-4">
      {node}
      <div className="mb-3 flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1">
          <h3 className="truncate text-sm font-semibold">{app.name}</h3>
          <p className="text-xs tabular-nums text-gray-500">
            {t("승인 {time}", { time: fmtRelative(app.created_at) })}
            {app.last_used_at ? t(" · 마지막 사용 {time}", { time: fmtRelative(app.last_used_at) }) : t(" · 아직 사용하지 않음")}
          </p>
        </div>
        <Button
          size="sm"
          variant="danger"
          onClick={() =>
            confirm({
              title: t("앱 접근 폐기"),
              message: t("{name}의 모든 토큰이 즉시 무효가 되어 더 이상 내 계정으로 Aven API를 쓸 수 없습니다.", { name: app.name }),
              confirmLabel: t("접근 폐기"),
              danger: true,
              run: async () => {
                await api.revokeOAuthGrant(app.grant_id);
                await qc.invalidateQueries({ queryKey: grantsKey });
              },
            })
          }
        >
          {t("접근 폐기")}
        </Button>
      </div>
      {app.description && <p className="mb-3 text-sm text-gray-600">{app.description}</p>}
      <ScopeChips scopes={app.scopes} />
      <dl className="mt-3 grid gap-2 text-sm">
        {level("workspace:").length > 0 && (
          <Row label={t("인스턴스")}>
            {level("workspace:").join(", ")} {t("— 내 역할 이하로 모든 프로젝트에 적용")}
          </Row>
        )}
        {hasProjectScope && (
          <Row label={t("공유한 프로젝트")}>
            <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
              {app.project_ids.map((id) => (
                <span key={id} className="rounded bg-gray-100 px-1.5 py-0.5 text-xs">
                  {names.get(id) ?? t("접근할 수 없는 프로젝트")}
                </span>
              ))}
              <button type="button" className="cursor-pointer text-xs font-medium text-pink-600 hover:underline" onClick={() => setEditing(true)}>
                {t("변경")}
              </button>
            </span>
          </Row>
        )}
      </dl>
      {hasProjectScope && <ShareProjectsDialog open={editing} onClose={() => setEditing(false)} app={app} />}
    </article>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-wrap gap-x-3">
      <dt className="w-28 shrink-0 text-gray-500">{label}</dt>
      <dd className="min-w-0 flex-1">{children}</dd>
    </div>
  );
}

function ShareProjectsDialog({ open, onClose, app }: { open: boolean; onClose: () => void; app: OAuthAuthorizedApp }) {
  const qc = useQueryClient();
  const projects = useProjects();
  const [selected, setSelected] = useState<ID[]>(app.project_ids);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    if (open) {
      setSelected(app.project_ids);
      setError(null);
    }
  }, [open, app.project_ids]);
  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.updateOAuthGrant(app.grant_id, { project_ids: selected });
      await qc.invalidateQueries({ queryKey: grantsKey });
      onClose();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={() => !busy && onClose()}
      title={t("{name}에 공유할 프로젝트", { name: app.name })}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("취소")}
          </Button>
          <Button variant="primary" loading={busy} disabled={!selected.length} onClick={save}>
            {t("저장")}
          </Button>
        </>
      }
    >
      <p className="mb-3">{t("선택한 프로젝트만 앱이 볼 수 있습니다. 권한은 내 프로젝트 역할을 넘지 않습니다.")}</p>
      <ErrorBox error={projects.error} className="mb-3" />
      <div className="max-h-[50dvh] space-y-2 overflow-y-auto">
        {(projects.data ?? []).map((p) => (
          <label key={p.id} className="flex cursor-pointer items-center gap-3 rounded-md border border-gray-200 px-3 py-2">
            <input
              type="checkbox"
              className="accent-pink-500"
              checked={selected.includes(p.id)}
              onChange={() => setSelected((cur) => (cur.includes(p.id) ? cur.filter((v) => v !== p.id) : [...cur, p.id]))}
            />
            <span className="min-w-0 flex-1 truncate text-sm text-foreground">{p.name}</span>
          </label>
        ))}
      </div>
      <ErrorBox error={error} className="mt-3" />
    </Dialog>
  );
}
