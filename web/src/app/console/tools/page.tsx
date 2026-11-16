"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { Check, Loader2, Save, Undo2, Wrench } from "lucide-react";
import { OwnConnanyKey } from "@/components/connectors-own-key";
import { useLocale } from "@/components/locale-provider";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { CategoryPanel, useToolCategoryLabel } from "@/components/tool-category-panel";
import {
  getMyTools,
  saveMyTools,
  type MyToolsConfig,
  type ToolCategorySettings,
  type ToolProviderSettings,
} from "@/lib/api";

// Console → Tools: the caller's own tool settings. Like models and skills,
// what a user sets here is used first and the deployment's tools
// (System → Tools) are the fallback, category by category. An app
// account's settings also apply to its end users' chats. Keys are never
// sent back to the browser: a saved one shows as "saved".
const CONNECTORS = "__connectors__";

export default function ConsoleToolsPage() {
  const { tr } = useLocale();
  const categoryLabel = useToolCategoryLabel();
  const [cfg, setCfg] = useState<MyToolsConfig | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [providers, setProviders] = useState<Record<string, ToolProviderSettings>>({});
  const [keySet, setKeySet] = useState<Record<string, boolean>>({});
  const [tools, setTools] = useState<Record<string, ToolCategorySettings>>({});
  const [active, setActive] = useState<string>("");

  const load = () =>
    getMyTools()
      .then((data) => {
        setCfg(data);
        const prov: Record<string, ToolProviderSettings> = {};
        const set: Record<string, boolean> = {};
        for (const [name, p] of Object.entries(data.toolProviders || {})) {
          prov[name] = { endpoint: p.endpoint, options: p.options };
          set[name] = p.apiKeySet;
        }
        setProviders(prov);
        setKeySet(set);
        setTools(data.tools || {});
        setActive((cur) => cur || data.categories[0]?.name || CONNECTORS);
      })
      .catch((e) => setError(e instanceof Error ? e.message : tr("Load failed", "加载失败")))
      .finally(() => setLoading(false));

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleSave = async (nextTools = tools) => {
    setSaving(true);
    setError(null);
    try {
      const resp = await saveMyTools({ toolProviders: providers, tools: nextTools });
      if (!resp.ok) {
        setError(resp.error || tr("Save failed", "保存失败"));
        return;
      }
      setSaved(true);
      setTimeout(() => setSaved(false), 2000);
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : tr("Save failed", "保存失败"));
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div className="flex flex-col md:flex-row md:gap-8 p-4 md:p-6 max-w-6xl mx-auto">
        <aside className="md:w-48 md:shrink-0 mb-4 md:mb-0">
          <Skeleton className="h-6 w-20 mb-4" />
          <Skeleton className="h-9 w-full" />
        </aside>
        <div className="flex-1 min-w-0 space-y-4">
          <Skeleton className="h-7 w-40" />
          <Skeleton className="h-64" />
        </div>
      </div>
    );
  }

  const categories = cfg?.categories || [];
  const activeCat = categories.find((c) => c.name === active);
  const own = activeCat ? tools[activeCat.name] : undefined;
  const inherited = activeCat ? cfg?.inherited?.[activeCat.name] : undefined;

  const itemClass = (isActive: boolean) =>
    "shrink-0 whitespace-nowrap rounded-md px-3 py-2 text-sm text-left transition " +
    (isActive ? "bg-accent text-accent-foreground" : "text-muted-foreground hover:bg-muted hover:text-foreground");

  const saveButton = (
    <Button onClick={() => handleSave()} disabled={saving} variant={saved ? "outline" : "default"}>
      {saved ? (
        <><Check className="h-4 w-4 mr-2" /> {tr("Saved", "已保存")}</>
      ) : saving ? (
        <><Loader2 className="h-4 w-4 mr-2 animate-spin" /> {tr("Saving…", "正在保存…")}</>
      ) : (
        <><Save className="h-4 w-4 mr-2" /> {tr("Save", "保存")}</>
      )}
    </Button>
  );

  return (
    <div className="flex flex-col md:flex-row md:gap-8 p-4 md:p-6 max-w-6xl mx-auto md:min-h-[calc(100vh-3.5rem)]">
      <aside className="md:w-48 md:shrink-0 mb-4 md:mb-0">
        <h2 className="text-lg font-semibold tracking-tight mb-3 md:mb-4">{tr("Tools", "工具")}</h2>
        <nav className="flex flex-row md:flex-col gap-1 overflow-x-auto md:overflow-visible -mx-1 px-1 md:mx-0 md:px-0">
          {categories.map((c) => (
            <button key={c.name} type="button" onClick={() => setActive(c.name)} className={itemClass(active === c.name)}>
              {categoryLabel(c.name, c.label)}
              {tools[c.name] && (
                <span className="ml-1.5 text-[10px] text-muted-foreground">{tr("· own", "· 自定义")}</span>
              )}
            </button>
          ))}
          <div className="hidden md:block my-1 border-t border-border/60" />
          <button type="button" onClick={() => setActive(CONNECTORS)} className={itemClass(active === CONNECTORS)}>
            {tr("Connectors", "连接器")}
          </button>
        </nav>
      </aside>
      <div className="flex-1 min-w-0">
        {error && active !== CONNECTORS && (
          <div className="mb-4 rounded-md border border-destructive/40 bg-destructive/10 px-4 py-2 text-sm text-destructive">
            {error}
          </div>
        )}
        {active === CONNECTORS ? (
          <ConnectorsConfig />
        ) : !activeCat ? (
          <div className="rounded-lg border border-border bg-card p-8 text-center">
            <Wrench className="h-8 w-8 text-muted-foreground/40 mx-auto mb-3" />
            <p className="text-sm text-muted-foreground">
              {tr("No tool categories are available.", "没有可用的工具分类。")}
            </p>
          </div>
        ) : own ? (
          <CategoryPanel
            key={activeCat.name}
            catalog={activeCat}
            providers={providers}
            keySet={keySet}
            setProvider={(name, patch) => setProviders((prev) => ({ ...prev, [name]: { ...(prev[name] || {}), ...patch } }))}
            tools={own}
            setTools={(patch) => setTools((prev) => ({ ...prev, [activeCat.name]: { ...(prev[activeCat.name] || {}), ...patch } }))}
            saveButton={
              <div className="flex shrink-0 gap-2">
                <Button
                  variant="outline"
                  disabled={saving}
                  onClick={() => {
                    const next = { ...tools };
                    delete next[activeCat.name];
                    setTools(next);
                    handleSave(next);
                  }}
                >
                  <Undo2 className="h-4 w-4 mr-2" />
                  {tr("Use the system's", "改用系统配置")}
                </Button>
                {saveButton}
              </div>
            }
            intro={
              <p className="text-sm text-muted-foreground mt-1">
                {tr(
                  "Your own providers for this tool, used instead of the system's. Keys stay on the server and are never shown again. Your app's end users use these too.",
                  "这个工具使用你自己的服务商，优先于系统配置。Key 只保存在服务端，保存后不再显示。你应用的终端用户也会使用这里的配置。",
                )}
              </p>
            }
          />
        ) : (
          <div className="max-w-3xl space-y-4">
            <div>
              <h3 className="text-xl font-semibold tracking-tight">{categoryLabel(activeCat.name, activeCat.label)}</h3>
              <p className="text-sm text-muted-foreground mt-1">
                {inherited
                  ? tr("Using the system's settings.", "正在使用系统配置。")
                  : tr("The system doesn't provide this tool, so agents don't have it.", "系统没有提供这个工具，Agent 目前用不了。")}
              </p>
            </div>
            {inherited && (
              <div className="rounded-lg border border-border bg-card p-5 space-y-1.5">
                <p className="text-xs uppercase tracking-wider text-muted-foreground">
                  {tr("System providers (top → bottom)", "系统服务商（从上到下）")}
                </p>
                {inherited.chain.map((ref, i) => (
                  <div key={ref} className="flex items-center gap-2 rounded-md border border-border bg-muted/30 px-3 py-2">
                    <span className="text-[11px] font-mono text-muted-foreground w-6">{i === 0 ? "1°" : `${i + 1}`}</span>
                    <span className="text-sm font-mono">{ref}</span>
                  </div>
                ))}
              </div>
            )}
            <Button
              onClick={() => setTools((prev) => ({ ...prev, [activeCat.name]: {} }))}
              variant={inherited ? "outline" : "default"}
            >
              {tr("Use my own providers", "使用我自己的服务商")}
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}

function ConnectorsConfig() {
  const { tr } = useLocale();
  return (
    <div className="max-w-3xl">
      <h2 className="text-2xl font-semibold tracking-tight">{tr("Connectors", "连接器")}</h2>
      <p className="mt-1 text-sm text-muted-foreground">
        {tr(
          "Which Connany connector service holds your accounts: your own project key, or the one this site provides. Connect and manage the accounts in ",
          "你的账号接在哪个 Connany 连接服务上：你自己的项目 Key，或本站提供的服务。连接和管理账号请到",
        )}
        <Link href="/console/connectors/" className="text-foreground underline underline-offset-4">
          {tr("Connectors", "「连接器」")}
        </Link>
        {tr(".", "。")}
      </p>
      <OwnConnanyKey />
    </div>
  );
}
