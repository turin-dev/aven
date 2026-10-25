"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { t } from "@/lib/i18n";
import { qk, useMe } from "@/lib/queries";
import { ErrorBox, Toggle } from "@/components/ui";

export function WorkspaceTwoFactorSetting() {
  const qc = useQueryClient();
  const me = useMe().data;
  const security = useQuery({ queryKey: qk.workspaceSecurity, queryFn: api.workspaceSecurity });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const required = !!security.data?.require_2fa;
  const selfHasMfa = !!(me?.mfa_enabled ?? me?.totp_enabled);
  const blocked = !required && !selfHasMfa;

  const set = async (v: boolean) => {
    setBusy(true);
    setError(null);
    try {
      await api.setWorkspaceSecurity(v);
      await Promise.all([qc.invalidateQueries({ queryKey: qk.workspaceSecurity }), qc.invalidateQueries({ queryKey: qk.me }), qc.invalidateQueries({ queryKey: qk.audit })]);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="max-w-3xl rounded-lg border border-gray-200 p-4">
      <div className="flex items-start gap-4">
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium">{t("2FA 요구")}</div>
          <p className="mt-1 text-sm text-gray-600">
            {t("켜면 모든 멤버가 인증 앱 또는 패스키를 설정해야 대시보드와 CLI로 인스턴스에 접근할 수 있습니다. 아직 설정하지 않은 멤버는 다음 접속 때 설정 화면으로 안내됩니다. API 토큰을 쓰는 자동화는 영향을 받지 않습니다.")}
          </p>
          {blocked && <p className="mt-2 text-xs text-gray-500">{t("먼저 내 계정의 보안 설정에서 2단계 인증을 켜야 이 옵션을 켤 수 있습니다.")}</p>}
        </div>
        <Toggle label={t("2FA 요구")}checked={required} disabled={busy || security.isPending || blocked} onChange={set} />
      </div>
      <ErrorBox error={security.error ?? error} className="mt-3" />
    </div>
  );
}
