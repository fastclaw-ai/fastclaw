"use client";

import { useCallback, useEffect, useState } from "react";
import { useTranslations } from "next-intl";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Plug } from "lucide-react";
import {
  getAgent,
  listHookPlugins,
  updateAgent,
  type HookPlugin,
} from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";

// Per-agent plugin enable tab. Mirrors the Skills page layout (cards
// grid with header). Off by default — plugins listed here come from
// the system install; flipping a toggle attaches the plugin's hooks
// to THIS agent only. See registerHookPluginsForAgent in
// internal/gateway/userspace.go for the opt-in semantics.
export default function AgentPluginsPage() {
  const t = useTranslations("agentPlugins");
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);
  const [hookPlugins, setHookPlugins] = useState<HookPlugin[]>([]);
  const [pluginEnabled, setPluginEnabled] = useState<Record<string, boolean>>({});
  const [pluginSaving, setPluginSaving] = useState<Record<string, boolean>>({});
  const [loading, setLoading] = useState(true);

  const fetchAll = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    try {
      const [agentRec, hooks] = await Promise.all([
        getAgent(agentId).catch(() => null),
        listHookPlugins(),
      ]);
      setPluginEnabled(
        agentRec?.plugins && typeof agentRec.plugins === "object"
          ? (agentRec.plugins as Record<string, boolean>)
          : {}
      );
      setHookPlugins(hooks);
    } finally {
      setLoading(false);
    }
  }, [agentId]);

  useEffect(() => {
    fetchAll();
  }, [fetchAll]);

  // Per-plugin toggle. Patch-semantic so flipping one doesn't clobber
  // overrides for sibling plugins. Optimistic update with rollback.
  const handleToggle = async (pluginID: string, next: boolean) => {
    const prev = pluginEnabled[pluginID] === true;
    setPluginEnabled((m) => ({ ...m, [pluginID]: next }));
    setPluginSaving((m) => ({ ...m, [pluginID]: true }));
    try {
      await updateAgent(agentId, { plugins: { [pluginID]: next } });
    } catch {
      setPluginEnabled((m) => ({ ...m, [pluginID]: prev }));
    } finally {
      setPluginSaving((m) => {
        const copy = { ...m };
        delete copy[pluginID];
        return copy;
      });
    }
  };

  if (loading) {
    return (
      <div className="p-6 space-y-6 max-w-5xl mx-auto">
        <Skeleton className="h-10 w-48" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">{t("title")}</h2>
        <p className="text-sm text-muted-foreground mt-1">
          {t.rich("subtitle", {
            name: agentName,
            strong: (chunks) => <strong>{chunks}</strong>,
            code: (chunks) => <code className="text-[10px]">{chunks}</code>,
          })}
        </p>
      </div>

      {hookPlugins.length === 0 ? (
        <div className="rounded-lg border border-dashed border-border bg-card/30 p-12">
          <div className="flex flex-col items-center justify-center">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-primary/10 mb-4">
              <Plug className="h-7 w-7 text-primary" />
            </div>
            <p className="text-sm text-muted-foreground mb-1">
              {t("emptyTitle")}
            </p>
            <p className="text-xs text-muted-foreground/60 max-w-sm text-center">
              {t.rich("emptyHint", {
                code: (chunks) => <code className="text-[10px]">{chunks}</code>,
              })}
            </p>
          </div>
        </div>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {hookPlugins.map((p) => {
            const enabled = pluginEnabled[p.id] === true;
            const saving = pluginSaving[p.id] === true;
            return (
              <div
                key={p.id}
                className="group rounded-lg border border-border bg-card p-5 transition-colors hover:bg-muted/50"
              >
                <div className="flex items-start justify-between mb-3 gap-3">
                  <div className="flex items-center gap-2.5 min-w-0">
                    <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 shrink-0">
                      <Plug className="h-4 w-4 text-primary" />
                    </div>
                    <div className="min-w-0">
                      <p className="text-sm font-medium truncate">
                        {p.name || p.id}
                      </p>
                      {p.version && (
                        <Badge variant="outline" className="mt-1 text-[10px]">
                          v{p.version}
                        </Badge>
                      )}
                    </div>
                  </div>
                  <Switch
                    checked={enabled}
                    onCheckedChange={(v) => handleToggle(p.id, v)}
                    disabled={saving}
                    aria-label={t("enableAria", { id: p.id })}
                  />
                </div>
                {p.description && (
                  <p className="text-xs text-muted-foreground line-clamp-3">
                    {p.description}
                  </p>
                )}
                <code className="text-[10px] text-muted-foreground/70 mt-3 block truncate">
                  {p.id}
                </code>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
