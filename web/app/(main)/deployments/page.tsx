"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { api } from "@/lib/api";
import { canvasHref } from "@/lib/nav";
import { errorMessage } from "@/components/ui";
import { RedirectNotice } from "@/components/Redirect";
import { t } from "@/lib/i18n";

// Legacy route: /deployments/?id=D  ->  /?project=P&env=E&svc=S&dep=D
function Go() {
  const id = useSearchParams().get("id") ?? "";
  const router = useRouter();
  const [error, setError] = useState<string | null>(id ? null : t("주소에 배포 ID가 없습니다. 서비스의 배포 탭에서 배포를 선택하세요."));
  useEffect(() => {
    if (!id) return;
    let cancelled = false;
    (async () => {
      try {
        const d = await api.getDeployment(id);
        for (const p of await api.listProjects()) {
          const detail = await api.getProject(p.id);
          const env = detail.environments.find((e) => e.id === d.environment_id);
          if (env) {
            if (!cancelled) router.replace(canvasHref({ svc: d.service_id, dep: d.id }, { project: p.id, env: env.name }));
            return;
          }
        }
        router.replace(canvasHref({ svc: d.service_id, dep: d.id }));
      } catch (e) {
        if (!cancelled) setError(t("배포 {id}를 찾지 못했습니다 ({err}). 삭제된 서비스의 배포일 수 있습니다.", { id, err: errorMessage(e) }));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [id, router]);
  return <RedirectNotice error={error} />;
}

export default function LegacyDeployment() {
  return (
    <Suspense fallback={<RedirectNotice />}>
      <Go />
    </Suspense>
  );
}
