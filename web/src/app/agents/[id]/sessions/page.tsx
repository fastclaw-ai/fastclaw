"use client";

import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";
import { useLocale } from "@/components/locale-provider";

export default function AgentSessionsPage() {
  const { tr } = useLocale();
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);
  return (
    <div className="p-6 max-w-3xl mx-auto">
      <h2 className="text-2xl font-semibold tracking-tight">{tr("Sessions", "会话")}</h2>
      <p className="text-sm text-muted-foreground mt-1">Agent：{agentName}</p>
      <div className="mt-6 rounded-lg border border-border bg-card p-8 text-center text-muted-foreground">
        {tr("Coming soon", "即将推出")}
      </div>
    </div>
  );
}
