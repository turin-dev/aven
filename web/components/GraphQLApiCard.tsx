"use client";

import { useEffect, useState } from "react";
import { ExternalLink } from "lucide-react";
import { Card, CopyField, LABEL_CLS } from "@/components/ui";
import { t } from "@/lib/i18n";

/** Public GraphQL API(/graphql/v2) 안내: 엔드포인트 주소, playground 링크, CLI/curl 예시. */
export function GraphQLApiCard() {
  // 정적 내보내기라 빌드 때는 window가 없다. 주소는 마운트 뒤에 채운다.
  const [origin, setOrigin] = useState("");
  useEffect(() => setOrigin(window.location.origin), []);
  const endpoint = `${origin}/graphql/v2`;
  return (
    <Card
      className="mt-8"
      title="GraphQL API"
      description={t("Railway Public API와 같은 모양의 GraphQL 엔드포인트입니다. 위의 API 토큰을 Authorization: Bearer 헤더로 보내면 REST API와 같은 권한으로 프로젝트, 서비스, 배포, 변수, 환경, 도메인, 볼륨을 다룰 수 있습니다.")}
      action={
        <a
          href="/graphql/v2"
          target="_blank"
          rel="noreferrer"
          className="inline-flex h-[34px] items-center gap-2 rounded-md border border-gray-200 px-3 text-sm hover:bg-gray-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-gray-600"
        >
          {t("Playground 열기")} <ExternalLink className="size-4" aria-hidden />
        </a>
      }
    >
      <div className="space-y-3">
        <div>
          <div className={LABEL_CLS}>{t("엔드포인트")}</div>
          <CopyField label={t("엔드포인트")} value={endpoint} />
        </div>
        <div>
          <div className={LABEL_CLS}>CLI</div>
          <CopyField label={t("CLI 명령")} value={`aven api '{ projects { edges { node { id name } } } }'`} />
        </div>
        <div>
          <div className={LABEL_CLS}>curl</div>
          <CopyField
            label={t("curl 명령")}
            value={`curl -s ${endpoint} -H "Authorization: Bearer $AVEN_TOKEN" -H 'Content-Type: application/json' -d '{"query":"{ me { email } }"}'`}
          />
        </div>
      </div>
    </Card>
  );
}
