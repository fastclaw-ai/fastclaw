"use client";

import { useTranslations } from "next-intl";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";

export default function AgentSessionsPage() {
  const t = useTranslations("agentSessions");
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);
  return (
    <div className="p-6 max-w-3xl mx-auto">
      <h2 className="text-2xl font-semibold tracking-tight">{t("title")}</h2>
      <p className="text-sm text-muted-foreground mt-1">{t("agentLabel", { name: agentName })}</p>
      <div className="mt-6 rounded-lg border border-border bg-card p-8 text-center text-muted-foreground">
        {t("comingSoon")}
      </div>
    </div>
  );
}
