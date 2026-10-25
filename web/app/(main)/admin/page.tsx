"use client";

import Link from "next/link";
import { Suspense, useEffect, type ReactNode } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { AppWindow, Bell, Building2, Crown, DatabaseBackup, Gauge, KeyRound, LayoutDashboard, Rocket, MonitorSmartphone, ScrollText, Server, ShieldCheck, SlidersHorizontal, Sparkles, UserCog, Users } from "lucide-react";
import type { AdminCapID } from "@/lib/api";
import { useMe } from "@/lib/queries";
import { InstanceUsers, AuditLog } from "@/components/MembersSettings";
import { GuardrailsSettings } from "@/components/AccessControlsSettings";
import { WorkspaceTwoFactorSetting } from "@/components/WorkspaceTwoFactorSetting";
import { SAMLSettings } from "@/components/SAMLSettings";
import { OAuthAppsSection } from "@/components/OAuthSettings";
import { NotificationChannels, SmtpStatus } from "@/components/NotificationSettings";
import { NodesView } from "@/components/nodes/NodesView";
import { AgentSettings } from "@/components/AgentSettings";
import { SystemStatus } from "@/components/SystemStatus";
import { ResourceLimitsSettings } from "@/components/ResourceLimitsSettings";
import { AdminOverview, AdminWorkspaces } from "@/components/AdminOverview";
import { SetupChecklist } from "@/components/SetupChecklist";
import { OwnerAccess, OwnerBackups, OwnerControl, OwnerSessions } from "@/components/OwnerSettings";
import { Card, ErrorBox, PageBody, PageShell, SideNavLayout, Spinner } from "@/components/ui";
import { t } from "@/lib/i18n";

// cap: 서버 owner가 서버 관리자에게 막을 수 있는 기능(서버도 같은 기준으로 403). owner: 서버 owner에게만 보인다.
type NavItem = { id: string; label: string; icon: ReactNode; cap?: AdminCapID; owner?: true };
const NAV: NavItem[] = [
  { id: "overview", label: t("전체 현황"), icon: <LayoutDashboard /> },
  { id: "users", label: t("사용자·초대"), icon: <Users />, cap: "users" },
  { id: "workspaces", label: t("작업 공간"), icon: <Building2 /> },
  { id: "resources", label: t("자원 제한"), icon: <SlidersHorizontal />, cap: "resources" },
  { id: "nodes", label: t("서버 노드"), icon: <Server />, cap: "nodes" },
  { id: "security", label: t("보안"), icon: <ShieldCheck />, cap: "security" },
  { id: "sso", label: "SAML SSO", icon: <KeyRound />, cap: "sso" },
  { id: "oauth-apps", label: t("OAuth 앱"), icon: <AppWindow />, cap: "oauth_apps" },
  { id: "notifications", label: t("전체 알림"), icon: <Bell />, cap: "notifications" },
  { id: "audit", label: t("감사 기록"), icon: <ScrollText />, cap: "audit" },
  { id: "agent", label: "Aven Agent", icon: <Sparkles />, cap: "agent" },
  { id: "system", label: t("시스템 상태"), icon: <Gauge /> },
  { id: "setup", label: t("Owner · 초기 설정"), icon: <Rocket />, owner: true },
  { id: "owner", label: t("Owner · 점검·제어"), icon: <Crown />, owner: true },
  { id: "owner-access", label: t("Owner · 권한"), icon: <UserCog />, owner: true },
  { id: "owner-sessions", label: t("Owner · 세션·토큰"), icon: <MonitorSmartphone />, owner: true },
  { id: "owner-backups", label: t("Owner · DB 백업"), icon: <DatabaseBackup />, owner: true },
];

function AdminInner() {
  const me = useMe();
  const params = useSearchParams();
  const router = useRouter();
  const caps = me.data?.admin_caps;
  const owner = me.data?.instance_owner === true;
  const nav = NAV.filter((n) => (n.owner ? owner : !n.cap || !caps || caps.includes(n.cap)));
  const section = nav.find((n) => n.id === params.get("section"))?.id ?? "overview";

  useEffect(() => {
    document.title = t("어드민") + " · aven";
    return () => { document.title = "aven"; };
  }, []);

  // Do not mount management components or fetch their data before authorization.
  if (me.isPending) return <PageShell title={t("어드민")}><PageBody><Spinner /></PageBody></PageShell>;
  if (me.error) return <PageShell title={t("어드민")}><PageBody><ErrorBox error={me.error} /></PageBody></PageShell>;
  if (!me.data?.instance_admin) {
    return (
      <PageShell title={t("어드민")}>
        <PageBody>
          <Card title={t("서버 관리자만 접근할 수 있습니다")}>
            <Link href="/projects/" className="text-sm text-pink-600 hover:underline">{t("프로젝트로 돌아가기")}</Link>
          </Card>
        </PageBody>
      </PageShell>
    );
  }

  return (
    <PageShell title={t("어드민")} close={{ href: "/projects/", label: t("어드민 닫기") }}>
      <SideNavLayout
        label={t("어드민 메뉴")}
        items={nav}
        value={section}
        onChange={(id) => router.replace(id === "overview" ? "/admin/" : `/admin/?section=${id}`, { scroll: false })}
      >
        {section === "overview" ? <AdminOverview />
          : section === "users" ? <InstanceUsers />
          : section === "workspaces" ? <AdminWorkspaces />
          : section === "resources" ? <ResourceLimitsSettings />
          : section === "nodes" ? <NodesView />
          : section === "security" ? (
            <div className="space-y-8"><WorkspaceTwoFactorSetting /><GuardrailsSettings /></div>
          )
          : section === "sso" ? <SAMLSettings />
          : section === "oauth-apps" ? <OAuthAppsSection />
          : section === "notifications" ? (
            <><NotificationChannels pid={null} canEdit title={t("서버 전체 알림")} /><SmtpStatus /></>
          )
          : section === "audit" ? <AuditLog />
          : section === "agent" ? <AgentSettings />
          : section === "setup" ? <SetupChecklist />
          : section === "owner" ? <OwnerControl />
          : section === "owner-access" ? <OwnerAccess />
          : section === "owner-sessions" ? <OwnerSessions />
          : section === "owner-backups" ? <OwnerBackups />
          : <SystemStatus />}
      </SideNavLayout>
    </PageShell>
  );
}

export default function AdminPage() {
  return <Suspense fallback={<Spinner />}><AdminInner /></Suspense>;
}
