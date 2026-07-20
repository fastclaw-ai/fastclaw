"use client";

import { useEffect, useState } from "react";
import { useTranslations } from "next-intl";
import { Separator } from "@/components/ui/separator";
import {
  getStatus,
  adminListChats,
  getTools,
  getConfig,
  type StatusResponse,
  type ToolsConfig,
  type ConfigResponse,
} from "@/lib/api";
import {
  Bot,
  Radio,
  Brain,
  Users,
  MessagesSquare,
} from "lucide-react";

export default function OverviewPage() {
  const t = useTranslations("overview");
  const [status, setStatus] = useState<StatusResponse | null>(null);
  const [chats, setChats] = useState<number | null>(null);
  const [tools, setTools] = useState<ToolsConfig | null>(null);
  const [runtime, setRuntime] = useState<ConfigResponse["sandbox"] | null>(null);
  const [loading, setLoading] = useState(true);

  const fetchStatus = () => {
    setLoading(true);
    getStatus()
      .then((s) => {
        setStatus(s);
        if (s.isAdmin) {
          adminListChats()
            .then((rows) => setChats(rows.length))
            .catch(() => setChats(null));
          getTools()
            .then(setTools)
            .catch(() => setTools(null));
          // getConfig is super_admin-only on the backend; admins-but-not-
          // super-admin will 403, in which case we just hide the Runtime
          // row rather than treating it as an error.
          getConfig()
            .then((cfg) => setRuntime(cfg.sandbox ?? null))
            .catch(() => setRuntime(null));
        }
      })
      .catch(() => setStatus(null))
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    fetchStatus();
    const interval = setInterval(fetchStatus, 10000);
    return () => clearInterval(interval);
  }, []);

  if (loading && !status) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="h-8 w-8 animate-spin rounded-full border-2 border-muted border-t-primary" />
      </div>
    );
  }

  // Hide empty / not-yet-connected sections so the dashboard reflects
  // what's actually configured: Channels stat when none connected.
  const channelCount = status?.channels?.length || 0;
  const showChannels = channelCount > 0;
  // Non-admins only need to see their agents — gateway plumbing (provider
  // config, users, chats) is admin-only.
  const isAdmin = status?.isAdmin ?? false;

  // Pretty-print the configured fallback chain for each tool category as
  // "Web Search: Exa, Brave". A category with no configured provider is
  // hidden — the Tools panel only surfaces what's actually wired up.
  const toolSummary: { name: string; label: string; providers: string }[] = [];
  if (tools) {
    for (const cat of tools.categories) {
      const cfg = tools.tools[cat.name];
      const chain = [cfg?.primary, ...(cfg?.fallbacks || [])].filter(Boolean) as string[];
      if (chain.length === 0) continue;
      const labels = chain.map((ref) => {
        const [pname] = ref.split("/");
        return cat.providers.find((p) => p.name === pname)?.label || pname;
      });
      toolSummary.push({ name: cat.name, label: cat.label, providers: labels.join(", ") });
    }
  }

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      {/* Header */}
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">{t("title")}</h2>
        <p className="text-sm text-muted-foreground mt-1">
          {t("subtitle")}
        </p>
      </div>

      {/* Stats Cards — Agents shown to everyone; Users + Chats +
          Channels are gateway-management surfaces, admin-only. */}
      <div
        className={`grid gap-4 grid-cols-2 ${
          isAdmin
            ? showChannels
              ? "md:grid-cols-4"
              : "md:grid-cols-3"
            : "md:grid-cols-2"
        }`}
      >
        {/* Agents */}
        <div className="rounded-lg border border-border bg-card p-5">
          <div className="flex items-center justify-between mb-3">
            <span className="text-sm text-muted-foreground">{t("statAgents")}</span>
            <div className="flex h-8 w-8 items-center justify-center rounded-full bg-violet-500/10">
              <Bot className="h-4 w-4 text-violet-500" />
            </div>
          </div>
          <p className="text-3xl font-semibold tracking-tight">
            {status?.agents?.length || 0}
          </p>
          <p className="text-xs text-muted-foreground mt-1">{t("statAgentsHint")}</p>
        </div>

        {/* Users — admin-only */}
        {isAdmin && (
          <div className="rounded-lg border border-border bg-card p-5">
            <div className="flex items-center justify-between mb-3">
              <span className="text-sm text-muted-foreground">{t("statUsers")}</span>
              <div className="flex h-8 w-8 items-center justify-center rounded-full bg-cyan-500/10">
                <Users className="h-4 w-4 text-cyan-500" />
              </div>
            </div>
            <p className="text-3xl font-semibold tracking-tight">
              {status?.users ?? 0}
            </p>
            <p className="text-xs text-muted-foreground mt-1">{t("statUsersHint")}</p>
          </div>
        )}

        {/* Chats — admin-only */}
        {isAdmin && (
          <div className="rounded-lg border border-border bg-card p-5">
            <div className="flex items-center justify-between mb-3">
              <span className="text-sm text-muted-foreground">{t("statChats")}</span>
              <div className="flex h-8 w-8 items-center justify-center rounded-full bg-amber-500/10">
                <MessagesSquare className="h-4 w-4 text-amber-500" />
              </div>
            </div>
            <p className="text-3xl font-semibold tracking-tight">
              {chats ?? "—"}
            </p>
            <p className="text-xs text-muted-foreground mt-1">{t("statChatsHint")}</p>
          </div>
        )}

        {/* Channels — admin-only */}
        {isAdmin && showChannels && (
          <div className="rounded-lg border border-border bg-card p-5">
            <div className="flex items-center justify-between mb-3">
              <span className="text-sm text-muted-foreground">{t("statChannels")}</span>
              <div className="flex h-8 w-8 items-center justify-center rounded-full bg-blue-500/10">
                <Radio className="h-4 w-4 text-blue-500" />
              </div>
            </div>
            <p className="text-3xl font-semibold tracking-tight">{channelCount}</p>
            <p className="text-xs text-muted-foreground mt-1">{t("statChannelsHint")}</p>
          </div>
        )}

      </div>

      {/* Configuration — admin-only summary of the configured LLM model
          and the wired-up tool providers. Hidden for non-admins. */}
      {isAdmin && (
        <div className="rounded-lg border border-border bg-card">
          <div className="p-5 pb-3">
            <div className="flex items-center gap-2 mb-1">
              <Brain className="h-4 w-4 text-amber-500" />
              <h3 className="font-medium">{t("configTitle")}</h3>
            </div>
            <p className="text-sm text-muted-foreground">
              {t("configSubtitle")}
            </p>
          </div>
          <div className="px-5 pb-5 space-y-3">
            <div className="flex items-center justify-between">
              <span className="text-sm text-muted-foreground">{t("configModel")}</span>
              {status?.provider?.model ? (
                <code className="text-sm font-mono bg-muted px-2 py-0.5 rounded">
                  {status.provider.model}
                </code>
              ) : (
                <span className="text-sm text-muted-foreground">—</span>
              )}
            </div>
            <Separator />
            {toolSummary.length > 0 ? (
              toolSummary.map((row) => (
                <div key={row.name} className="space-y-3">
                  <div className="flex items-center justify-between gap-3">
                    <span className="text-sm text-muted-foreground">{row.label}</span>
                    <span className="text-sm truncate">{row.providers}</span>
                  </div>
                  <Separator />
                </div>
              ))
            ) : (
              <>
                <div className="flex items-center justify-between">
                  <span className="text-sm text-muted-foreground">{t("configTools")}</span>
                  <span className="text-sm text-muted-foreground">{t("configToolsNone")}</span>
                </div>
                <Separator />
              </>
            )}
            <div className="flex items-center justify-between gap-3">
              <span className="text-sm text-muted-foreground">{t("configRuntime")}</span>
              <span className="text-sm truncate">
                {runtime?.enabled
                  ? `${runtime.backend === "e2b" ? "E2B" : "Docker"}${
                      runtime.image ? ` (${runtime.image})` : ""
                    }`
                  : t("runtimeDisabled")}
              </span>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
