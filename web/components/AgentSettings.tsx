"use client";

import { useEffect, useState } from "react";
import { CheckCircle2, Unplug } from "lucide-react";
import { agentApi, type AgentSettings as AgentSettingsType } from "@/lib/agentApi";
import { Button, Card, ErrorBox, Field, Input, Notice, cx, useConfirm } from "@/components/ui";
import { t } from "@/lib/i18n";

/** OpenAI-compatible providers; clicking one fills the endpoint (and a typical model). */
const PRESETS = [
  { name: "OpenAI", endpoint: "https://api.openai.com/v1", model: "gpt-4.1-mini" },
  { name: "OpenRouter", endpoint: "https://openrouter.ai/api/v1", model: "anthropic/claude-sonnet-4" },
  { name: t("Ollama (로컬)"), endpoint: "http://localhost:11434/v1", model: "qwen2.5:14b" },
];

/**
 * Workspace admin settings for Keel Agent (bring-your-own-model setup, modeled on Railway's dashboard): an OpenAI-compatible
 * Chat Completions endpoint, model and API key. The key is stored encrypted and never shown again.
 */
export function AgentSettings() {
  const [settings, setSettings] = useState<AgentSettingsType | null>(null);
  const [endpoint, setEndpoint] = useState("");
  const [model, setModel] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [test, setTest] = useState<{ ok: boolean; reply?: string; error?: string } | null>(null);
  const { confirm, node: confirmDialog } = useConfirm();

  const apply = (value: AgentSettingsType) => {
    setSettings(value);
    setEndpoint(value.endpoint);
    setModel(value.model);
  };

  useEffect(() => {
    let active = true;
    agentApi
      .settings()
      .then((value) => active && apply(value))
      .catch((reason) => active && setError(reason))
      .finally(() => active && setLoading(false));
    return () => {
      active = false;
    };
  }, []);

  const run = async (fn: () => Promise<void>) => {
    setSaving(true);
    setError(null);
    setNotice(null);
    setTest(null);
    try {
      await fn();
    } catch (reason) {
      setError(reason);
    } finally {
      setSaving(false);
    }
  };

  const save = () =>
    run(async () => {
      apply(await agentApi.updateSettings({ endpoint: endpoint.trim(), model: model.trim(), ...(apiKey.trim() ? { api_key: apiKey.trim() } : {}) }));
      setApiKey("");
      setNotice(t("모델 설정을 저장했습니다."));
    });

  const clearKey = () =>
    run(async () => {
      apply(await agentApi.updateSettings({ endpoint: endpoint.trim(), model: model.trim(), clear_api_key: true }));
      setNotice(t("저장된 API key를 지웠습니다."));
    });

  const disconnect = () =>
    confirm({
      title: t("Aven Agent 연결 해제"),
      message: t("모델 endpoint와 API key를 지웁니다. 다시 연결할 때까지 아무도 Aven Agent를 쓸 수 없습니다. 대화 기록은 남습니다."),
      confirmLabel: t("연결 해제"),
      danger: true,
      run: () =>
        run(async () => {
          apply(await agentApi.updateSettings({ endpoint: "", model: "" }));
          setNotice(t("Aven Agent 연결을 해제했습니다."));
        }),
    });

  const runTest = async () => {
    setTesting(true);
    setTest(null);
    setError(null);
    try {
      setTest(await agentApi.testSettings());
    } catch (reason) {
      setError(reason);
    } finally {
      setTesting(false);
    }
  };

  if (loading) return <p className="text-sm text-gray-500">{t("설정을 불러오는 중…")}</p>;
  const dirty = endpoint.trim() !== (settings?.endpoint ?? "") || model.trim() !== (settings?.model ?? "") || apiKey.trim() !== "";
  return (
    <>
      {confirmDialog}
      <Card
        title={t("모델 제공자")}
        description={
          settings?.configured ? (
            <span className="inline-flex items-center gap-1 text-success">
              <CheckCircle2 className="size-3.5" /> {t("연결됨 · {model}", { model: settings.model })}
              {settings.key_hint ? ` · ${t("키 …{hint}", { hint: settings.key_hint })}` : ""}
            </span>
          ) : (
            t("연결되지 않음. 아래에서 OpenAI 호환 endpoint를 연결하세요.")
          )
        }
        action={
          <div className="flex flex-wrap justify-end gap-1.5">
            {PRESETS.map((p) => (
              <Button
                key={p.name}
                size="sm"
                variant="outline"
                aria-pressed={endpoint === p.endpoint}
                className={cx(endpoint === p.endpoint && "!border-pink-400")}
                onClick={() => {
                  setEndpoint(p.endpoint);
                  if (!model.trim() || PRESETS.some((x) => x.model === model)) setModel(p.model);
                }}
              >
                {p.name}
              </Button>
            ))}
          </div>
        }
      >
        <div className="space-y-5">
          <Field label="API endpoint" hint={t("OpenAI 호환 Chat Completions API의 기본 주소입니다. /chat/completions는 자동으로 붙습니다.")}>
            <Input mono value={endpoint} onChange={(e) => setEndpoint(e.target.value)} placeholder="https://api.openai.com/v1" autoComplete="url" />
          </Field>
          <Field label={t("모델 ID")} hint={t("제공자가 쓰는 정확한 모델 이름입니다. 도구 호출(function calling)을 지원하는 모델이어야 합니다.")}>
            <Input mono value={model} onChange={(e) => setModel(e.target.value)} placeholder="gpt-4.1-mini" />
          </Field>
          <Field
            label="API key"
            hint={settings?.key_set ? t("키가 저장되어 있습니다(…{hint}). 바꾸려면 새 키를 입력하세요.", { hint: settings.key_hint ?? "****" }) : t("키가 필요 없는 로컬 모델이면 비워 두세요.")}
          >
            <Input
              type="password"
              mono
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder={settings?.key_set ? t("저장된 키는 다시 표시되지 않습니다") : "sk-…"}
              autoComplete="new-password"
            />
          </Field>
          <p className="text-xs leading-5 text-gray-500">
            {t("키는 Aven 마스터 키로 암호화해 저장합니다. 대화 내용과 Aven Agent가 읽은 로그·지표·서비스 정보는 이 endpoint로 전송되며, 요금은 모델 제공자가 청구합니다.")}
          </p>
          {error ? <ErrorBox error={error} /> : null}
          {notice && <Notice onDismiss={() => setNotice(null)}>{notice}</Notice>}
          {test &&
            (test.ok ? (
              <Notice onDismiss={() => setTest(null)}>{t("연결 성공 · 모델 응답: {reply}", { reply: test.reply || t("(빈 응답)") })}</Notice>
            ) : (
              <ErrorBox error={t("연결 실패: {error}", { error: test.error ?? t("알 수 없는 오류") })} />
            ))}
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="primary" onClick={save} loading={saving} disabled={!endpoint.trim() || !model.trim() || !dirty}>
              {t("설정 저장")}
            </Button>
            <Button variant="outline" onClick={runTest} loading={testing} disabled={!settings?.configured || dirty}>
              {t("연결 테스트")}
            </Button>
            {settings?.key_set && (
              <Button variant="ghost" onClick={clearKey} disabled={saving}>
                {t("저장된 키 지우기")}
              </Button>
            )}
            {settings?.configured && (
              <Button variant="ghost" className="ml-auto !text-danger" onClick={disconnect} disabled={saving}>
                <Unplug /> {t("연결 해제")}
              </Button>
            )}
          </div>
        </div>
      </Card>
    </>
  );
}
