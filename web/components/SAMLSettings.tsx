"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, type SAMLSettings as SAMLSettingsData, type WorkspaceRole } from "@/lib/api";
import { fmtTime } from "@/lib/format";
import { Button, Card, CopyField, ErrorBox, Field, Input, Select, Spinner, Toggle } from "@/components/ui";
import { t } from "@/lib/i18n";

const CERT_WARN_DAYS = 30;

export function SAMLSettings() {
  const [data, setData] = useState<SAMLSettingsData | null>(null);
  const [metadataURL, setMetadataURL] = useState("");
  const [metadataXML, setMetadataXML] = useState("");
  const [clearMetadata, setClearMetadata] = useState(false);
  const [domainDraft, setDomainDraft] = useState("");
  const [domains, setDomains] = useState<string[]>([]);
  const [defaultRole, setDefaultRole] = useState<WorkspaceRole>("member");
  const [enforceSSO, setEnforceSSO] = useState(false);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  const applyConfig = (next: SAMLSettingsData) => {
    setData(next);
    setMetadataURL(next.config.metadata_url || "");
    setDomains(next.config.domains.map((item) => item.domain));
    setDefaultRole(next.config.default_role || "member");
    setEnforceSSO(next.config.enforce_sso);
    setClearMetadata(false);
    setMetadataXML("");
    if (fileRef.current) fileRef.current.value = "";
  };

  useEffect(() => {
    api.samlSettings().then(applyConfig).catch(setError).finally(() => setLoading(false));
  }, []);

  const save = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      await api.saveSAMLSettings({
        ...(clearMetadata ? { clear_metadata: true } : metadataXML ? { metadata_xml: metadataXML } : metadataURL.trim() ? { metadata_url: metadataURL.trim() } : {}),
        domains,
        default_role: defaultRole,
        enforce_sso: enforceSSO,
      });
      // IdP 요약(인증서 만료 등)도 다시 받아온다.
      applyConfig(await api.samlSettings());
      setSaved(true);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const addDomain = () => {
    const domain = domainDraft.trim().toLowerCase().replace(/^@/, "").replace(/\.$/, "");
    if (!domain || domains.includes(domain)) return;
    setDomains([...domains, domain]);
    setDomainDraft("");
  };

  const verifyDomain = async (domain: string) => {
    setBusy(true);
    setError(null);
    try {
      const result = await api.verifySAMLDomain(domain);
      setData((cur) => (cur ? { ...cur, config: { ...cur.config, domains: cur.config.domains.map((item) => (item.domain === domain ? result.domain : item)) } } : cur));
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  if (loading) return <Spinner />;
  if (!data) return <ErrorBox error={error ?? t("SAML 설정을 불러오지 못했습니다.")} className="max-w-3xl" />;

  const { config, idp } = data;
  const certDays = idp?.cert_not_after ? Math.floor((new Date(idp.cert_not_after).getTime() - Date.now()) / 86_400_000) : null;
  const hasVerified = config.domains.some((d) => d.verified);
  const testLogin = `${data.login_url}?next=${encodeURIComponent("/admin/?section=sso")}`;

  return (
    <form onSubmit={save}>
      <Card title="SAML Single Sign-On" description={t("조직의 Identity Provider(Okta, Entra ID, Google Workspace, Keycloak 등)로 인스턴스에 로그인합니다. IdP에 아래 서비스 제공자(SP) 정보를 등록하세요.")}>
        <div className="space-y-4">
          <Field label={t("SP 메타데이터 URL (Entity ID)")}>
            <CopyField value={data.sp_metadata_url} label={t("SP 메타데이터 URL")} />
          </Field>
          <Field label="ACS URL (Reply URL)">
            <CopyField value={data.acs_url} label="ACS URL" />
          </Field>
          <p className="text-xs text-gray-500">{t("NameID는 이메일 주소 형식으로 보내거나 email 속성을 포함하세요. IdP 대시보드에서 바로 여는 IdP-initiated 로그인도 지원합니다.")}</p>
        </div>
      </Card>

      <Card title="Identity Provider" description={t("IdP 메타데이터 URL을 입력하거나 메타데이터 XML 파일을 업로드하세요. URL은 저장할 때마다 다시 가져옵니다.")}>
        <div className="space-y-4">
          <Field label={t("메타데이터 URL")}>
            <Input
              value={metadataURL}
              onChange={(event) => {
                setMetadataURL(event.target.value);
                setMetadataXML("");
                setClearMetadata(false);
              }}
              placeholder="https://idp.example.com/app/metadata"
            />
          </Field>
          <Field label={t("또는 메타데이터 XML 파일")}>
            <input
              ref={fileRef}
              type="file"
              accept=".xml,text/xml,application/xml,application/samlmetadata+xml"
              className="block text-sm text-gray-600 file:mr-3 file:rounded-md file:border file:border-gray-200 file:bg-transparent file:px-3 file:py-1.5 file:text-sm file:text-foreground"
              onChange={async (event) => {
                const file = event.target.files?.[0];
                if (!file) return;
                setMetadataXML(await file.text());
                setMetadataURL("");
                setClearMetadata(false);
              }}
            />
          </Field>
          {metadataXML && <p className="text-xs text-gray-500">{t("저장하면 업로드한 메타데이터 XML을 적용합니다.")}</p>}
          {config.configured && idp && (
            <div className="space-y-1 rounded-lg border border-gray-200 px-4 py-3 text-sm">
              <div className="flex items-center gap-3">
                <span className="min-w-0 flex-1 font-medium">{t("연결됨 · {source}", { source: config.metadata_url ? t("URL 메타데이터") : t("업로드한 메타데이터") })}</span>
                <Button
                  size="sm"
                  onClick={() => {
                    setClearMetadata(!clearMetadata);
                    setMetadataXML("");
                    setMetadataURL(clearMetadata ? config.metadata_url : "");
                  }}
                >
                  {clearMetadata ? t("연결 해제 취소") : t("연결 해제")}
                </Button>
              </div>
              <p className="break-all text-xs text-gray-600">IdP Entity ID: {idp.entity_id}</p>
              {idp.sso_url && <p className="break-all text-xs text-gray-600">SSO URL: {idp.sso_url}</p>}
              {idp.cert_not_after && (
                <p className={certDays !== null && certDays < CERT_WARN_DAYS ? "text-xs tabular-nums text-danger" : "text-xs tabular-nums text-gray-600"}>
                  {t("서명 인증서 만료: {time}", { time: fmtTime(idp.cert_not_after) })}
                  {certDays !== null && certDays < 0 ? t(" (만료됨: IdP에서 인증서를 갱신하고 메타데이터를 다시 저장하세요)") : certDays !== null && certDays < CERT_WARN_DAYS ? t(" ({days}일 남음: 갱신이 필요합니다)", { days: certDays }) : ""}
                </p>
              )}
            </div>
          )}
          {clearMetadata && <p className="text-sm text-danger">{t("저장하면 IdP 연결이 해제되고 SSO 로그인이 중지됩니다.")}</p>}
        </div>
      </Card>

      <Card title={t("검증된 이메일 도메인")} description={t("이 도메인의 이메일을 가진 사용자만 SSO로 로그인할 수 있습니다. 도메인을 추가하고 저장한 뒤 DNS TXT 레코드로 소유권을 확인하세요.")}>
        <div className="space-y-3">
          <div className="flex gap-2">
            <Input
              value={domainDraft}
              onChange={(event) => setDomainDraft(event.target.value)}
              placeholder="example.com"
              aria-label={t("이메일 도메인")}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  addDomain();
                }
              }}
            />
            <Button onClick={addDomain} disabled={!domainDraft.trim()}>
              {t("도메인 추가")}
            </Button>
          </div>
          {domains.length === 0 && <p className="text-sm text-gray-500">{t("등록된 도메인이 없습니다.")}</p>}
          {domains.map((domain) => {
            const item = config.domains.find((candidate) => candidate.domain === domain);
            return (
              <div key={domain} className="space-y-2 rounded-lg border border-gray-200 px-4 py-3">
                <div className="flex items-center gap-2">
                  <span className="min-w-0 flex-1 truncate text-sm font-medium">{domain}</span>
                  <span className={item?.verified ? "text-xs text-success" : "text-xs text-gray-500"}>{item ? (item.verified ? t("검증됨") : t("검증 필요")) : t("저장 필요")}</span>
                  {item && !item.verified && (
                    <Button size="sm" disabled={busy} onClick={() => verifyDomain(domain)}>
                      {t("DNS 확인")}
                    </Button>
                  )}
                  <Button size="sm" onClick={() => setDomains(domains.filter((value) => value !== domain))} aria-label={t("{domain} 삭제", { domain })}>
                    {t("삭제")}
                  </Button>
                </div>
                {item && !item.verified && (
                  <div className="grid gap-2 sm:grid-cols-2">
                    <Field label={t("TXT 레코드 이름")}>
                      <CopyField value={`_keel-sso-verification.${domain}`} label={t("DNS 레코드 이름")} />
                    </Field>
                    <Field label={t("TXT 레코드 값")}>
                      <CopyField value={item.token} label={t("DNS 레코드 값")} />
                    </Field>
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </Card>

      <Card title={t("자동 등록 (JIT)")} description={t("검증된 도메인의 새 사용자는 첫 SSO 로그인 때 이 역할로 인스턴스에 추가됩니다. 같은 이메일의 기존 사용자는 그 계정으로 로그인합니다.")}>
        <Field label={t("기본 인스턴스 역할")}>
          <Select value={defaultRole} onChange={(event) => setDefaultRole(event.target.value as WorkspaceRole)}>
            <option value="admin">Admin</option>
            <option value="member">Member</option>
            <option value="viewer">Viewer</option>
          </Select>
        </Field>
      </Card>

      <Card
        title={t("SAML SSO 적용")}
        description={t("켜면 검증된 도메인의 사용자는 SAML로만 로그인할 수 있고, SAML로 로그인하지 않은 기존 세션은 즉시 다시 인증해야 합니다. admin@local 비상 계정과 API 토큰은 계속 쓸 수 있습니다.")}
        action={<Toggle checked={enforceSSO} onChange={setEnforceSSO} label={t("멤버는 SAML SSO로 로그인해야 합니다")} disabled={!config.configured || !hasVerified} />}
      >
        <p className="text-sm text-gray-600">
          {t("켜려면 검증된 도메인 계정으로 SAML 로그인한 관리자여야 합니다.")}{" "}
          {config.configured && hasVerified && (
            <a href={testLogin} className="text-pink-600 underline underline-offset-2">
              {t("SSO로 다시 로그인")}
            </a>
          )}
        </p>
      </Card>

      <div className="mt-8 flex max-w-3xl flex-wrap items-center gap-3">
        <Button type="submit" variant="primary" loading={busy}>
          {t("저장")}
        </Button>
        {saved && !error && <span className="text-sm text-gray-500">{t("저장했습니다.")}</span>}
      </div>
      {error ? <ErrorBox error={error} className="mt-4 max-w-3xl" /> : null}
    </form>
  );
}
