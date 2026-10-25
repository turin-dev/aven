"use client";

import { usePathname, useRouter } from "next/navigation";
import type { ReactNode } from "react";
import { ShieldAlert } from "lucide-react";
import type { Me } from "@/lib/api";
import { Button } from "./ui";
import { t } from "@/lib/i18n";

/**
 * 작업 공간이 2단계 인증을 요구하는데 아직 설정하지 않은 세션이면, 계정 보안 화면 말고는 설정 안내를 보여 준다
 * (서버도 설정용 엔드포인트 외에는 403 mfa_setup_required로 막는다).
 */
export function MfaSetupGate({ me, children }: { me: Me | undefined; children: ReactNode }) {
  const pathname = usePathname() || "/";
  const router = useRouter();
  if (!me?.mfa_setup_required || pathname.startsWith("/account")) return <>{children}</>;
  return (
    <div className="dot-grid flex h-full flex-col items-center justify-center gap-4 px-6 text-center">
      <ShieldAlert className="size-10 text-pink-500" aria-hidden />
      <div className="text-base font-semibold">{t("2단계 인증을 설정하세요")}</div>
      <p className="max-w-md text-sm text-gray-600">
        {t("이 작업 공간은 모든 멤버에게 2단계 인증을 요구합니다. 인증 앱 또는 패스키를 설정하면 바로 프로젝트에 접근할 수 있습니다.")}
      </p>
      <Button variant="primary" onClick={() => router.push("/account/?section=security")}>
        {t("2단계 인증 설정")}
      </Button>
    </div>
  );
}
