"use client";

import { useEffect, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import type { Me } from "@/lib/api";
import { BrandIcon } from "./ServiceIcon";
import { Button, Dialog } from "./ui";
import { t } from "@/lib/i18n";

const dismissKey = (uid: string) => `keel.github-onboarding.dismissed.${uid}`;

/**
 * GitHub 연결은 사용자별이라 새 계정은 먼저 자기 GitHub 계정을 연결해야 비공개 저장소를 배포할 수 있다.
 * 처음 들어온 사용자(초대 수락 직후 ?welcome=1, 또는 아직 연결하지 않고 이 안내를 닫은 적 없는 사용자)에게 한 번 권한다.
 * "나중에"는 사용자별로 기억한다(localStorage). 계정 화면에서는 띄우지 않는다(거기서 바로 연결한다).
 */
export function GitHubOnboarding({ me }: { me: Me | undefined }) {
  const pathname = usePathname() || "/";
  const params = useSearchParams();
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const welcome = params.get("welcome") === "1";

  useEffect(() => {
    if (!me || me.auth !== "session" || me.mfa_setup_required || pathname.startsWith("/account")) return;
    if (!me.github_app_configured || me.github_connected) return;
    let dismissed = false;
    try {
      dismissed = window.localStorage.getItem(dismissKey(me.id)) === "1";
    } catch {
      /* ignore */
    }
    if (welcome || !dismissed) setOpen(true);
  }, [me, pathname, welcome]);

  // ?welcome=1은 한 번만 쓴다.
  useEffect(() => {
    if (!welcome) return;
    const next = new URLSearchParams(params.toString());
    next.delete("welcome");
    const q = next.toString();
    router.replace(q ? `?${q}` : pathname, { scroll: false });
  }, [welcome, params, router, pathname]);

  if (!me) return null;
  const later = () => {
    try {
      window.localStorage.setItem(dismissKey(me.id), "1");
    } catch {
      /* ignore */
    }
    setOpen(false);
  };
  return (
    <Dialog
      open={open}
      onClose={later}
      title={t("GitHub 계정을 연결하세요")}
      footer={
        <>
          <Button onClick={later}>{t("나중에")}</Button>
          {/* OAuth 승인은 GitHub 화면으로 이동해야 하므로 API 경로로 바로 간다. 승인 뒤 계정 화면으로 돌아온다. */}
          <a href="/api/github/oauth/start?from=account">
            <Button variant="primary">
              <BrandIcon name="github" className="size-4" /> {t("GitHub 연결")}
            </Button>
          </a>
        </>
      }
    >
      <div className="space-y-3 text-sm text-gray-600">
        <p>{t("aven에서는 GitHub 연결이 사람마다 따로입니다. 내 계정을 연결해야 내 저장소 목록을 보고, 비공개 저장소를 배포하고, 커밋에 배포 상태를 표시할 수 있습니다.")}</p>
        <p>{t("내가 만든 프로젝트는 내 GitHub 연결을 씁니다. 다른 사람의 연결은 쓰지 않습니다.")}</p>
        <p className="text-xs text-gray-500">{t("나중에 계정 › GitHub 연결에서 언제든 연결하거나 해제할 수 있습니다.")}</p>
      </div>
    </Dialog>
  );
}
