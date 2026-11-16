"use client";

import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Wrench, Save, Check, Loader2 } from "lucide-react";
import {
  getTools,
  saveTools,
  type ToolsConfig,
  type ToolCategoryCatalog,
  type ToolProviderSettings,
  type ToolCategorySettings,
} from "@/lib/api";
import { CategoryPanel, useToolCategoryLabel } from "@/components/tool-category-panel";
import RuntimeSettingsPage from "@/app/settings/runtime/page";
import { ConnectorsAdminPanel } from "@/components/connectors-admin";
import { useLocale } from "@/components/locale-provider";

// Sentinel value used as the active rail entry when Runtime is selected.
// Real tool categories never start with "__" so this can never collide.
const RUNTIME_ACTIVE = "__runtime__";
const CONNECTORS_ACTIVE = "__connectors__";

export default function ToolsPage() {
  const { tr } = useLocale();
  const [cfg, setCfg] = useState<ToolsConfig | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Mutable copies of the two config maps. The catalog itself is immutable.
  const [providers, setProviders] = useState<Record<string, ToolProviderSettings>>({});
  const [tools, setTools] = useState<Record<string, ToolCategorySettings>>({});
  const [active, setActive] = useState<string>("");

  useEffect(() => {
    getTools()
      .then((data) => {
        setCfg(data);
        setProviders(data.toolProviders || {});
        setTools(data.tools || {});
        if (data.categories.length > 0) setActive(data.categories[0].name);
      })
      .catch((e) => setError(e instanceof Error ? e.message : tr("Load failed", "加载失败")))
      .finally(() => setLoading(false));
  }, [tr]);

  const updateProvider = (name: string, patch: Partial<ToolProviderSettings>) => {
    setProviders((prev) => ({ ...prev, [name]: { ...(prev[name] || {}), ...patch } }));
  };

  const updateCategory = (cat: string, patch: Partial<ToolCategorySettings>) => {
    setTools((prev) => ({ ...prev, [cat]: { ...(prev[cat] || {}), ...patch } }));
  };

  const handleSave = async () => {
    setSaving(true);
    setError(null);
    try {
      // Drop empty provider entries so the config stays tidy.
      const cleaned: Record<string, ToolProviderSettings> = {};
      for (const [name, p] of Object.entries(providers)) {
        const hasKey = p.apiKey && p.apiKey.trim();
        const hasURL = p.endpoint && p.endpoint.trim();
        const hasOpts = p.options && Object.keys(p.options).length > 0;
        if (hasKey || hasURL || hasOpts) cleaned[name] = p;
      }
      const resp = await saveTools({ toolProviders: cleaned, tools });
      if (!resp.ok) {
        setError(resp.error || tr("Save failed", "保存失败"));
      } else {
        setSaved(true);
        setTimeout(() => setSaved(false), 2000);
      }
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
          <Skeleton className="h-96" />
        </div>
      </div>
    );
  }

  const activeCat = cfg?.categories.find((c) => c.name === active);

  return (
    <div className="flex flex-col md:flex-row md:gap-8 p-4 md:p-6 max-w-6xl mx-auto md:min-h-[calc(100vh-3.5rem)]">
      <aside className="md:w-48 md:shrink-0 mb-4 md:mb-0">
        <h2 className="text-lg font-semibold tracking-tight mb-3 md:mb-4">{tr("Tools", "工具")}</h2>
        <CategoryRail
          categories={cfg?.categories || []}
          active={active}
          onSelect={setActive}
        />
      </aside>
      <div className="flex-1 min-w-0">
        {error && active !== RUNTIME_ACTIVE && active !== CONNECTORS_ACTIVE && (
          <div className="mb-4 rounded-md border border-destructive/40 bg-destructive/10 px-4 py-2 text-sm text-destructive">
            {error}
          </div>
        )}

        {active === CONNECTORS_ACTIVE ? (
          // Deployment-wide like Runtime: where the connector service is.
          // The panel manages its own save / loading state.
          <ConnectorsAdminPanel />
        ) : active === RUNTIME_ACTIVE ? (
          // Runtime is a deployment-wide knob (sandbox backend, etc.), not
          // a per-category provider; it lives in the same rail as the tool
          // categories purely as a convenient admin entry point. The
          // component manages its own save / loading state.
          <RuntimeSettingsPage />
        ) : !cfg || cfg.categories.length === 0 ? (
          <div className="rounded-lg border border-border bg-card p-8 text-center">
            <Wrench className="h-8 w-8 text-muted-foreground/40 mx-auto mb-3" />
            <p className="text-sm text-muted-foreground">
              {tr("No tool categories are available in this build.", "当前版本中没有可用的工具分类。")}
            </p>
          </div>
        ) : activeCat ? (
          <CategoryPanel
            key={activeCat.name}
            catalog={activeCat}
            providers={providers}
            setProvider={updateProvider}
            tools={tools[activeCat.name] || {}}
            setTools={(patch) => updateCategory(activeCat.name, patch)}
            saveButton={
              <Button onClick={handleSave} disabled={saving} variant={saved ? "outline" : "default"}>
                {saved ? (
                  <><Check className="h-4 w-4 mr-2" /> {tr("Saved", "已保存")}</>
                ) : saving ? (
                  <><Loader2 className="h-4 w-4 mr-2 animate-spin" /> {tr("Saving…", "正在保存…")}</>
                ) : (
                  <><Save className="h-4 w-4 mr-2" /> {tr("Save", "保存")}</>
                )}
              </Button>
            }
          />
        ) : null}
      </div>
    </div>
  );
}

function CategoryRail({
  categories,
  active,
  onSelect,
}: {
  categories: ToolCategoryCatalog[];
  active: string;
  onSelect: (name: string) => void;
}) {
  const { tr } = useLocale();
  const categoryLabel = useToolCategoryLabel();
  const itemClass = (isActive: boolean) =>
    "shrink-0 md:shrink-0 whitespace-nowrap rounded-md px-3 py-2 text-sm text-left transition " +
    (isActive
      ? "bg-accent text-accent-foreground"
      : "text-muted-foreground hover:bg-muted hover:text-foreground");

  return (
    <nav className="flex flex-row md:flex-col gap-1 overflow-x-auto md:overflow-visible -mx-1 px-1 md:mx-0 md:px-0">
      {categories.map((c) => (
        <button
          key={c.name}
          type="button"
          onClick={() => onSelect(c.name)}
          className={itemClass(c.name === active)}
        >
          {categoryLabel(c.name, c.label)}
        </button>
      ))}
      {/* Runtime sits at the bottom of the rail (or rightmost on mobile);
          super_admin-only — the underlying RuntimeSettingsPage redirects
          anyone else away. The hairline divider visually separates it
          from the per-category tool entries. */}
      <div className="hidden md:block my-1 border-t border-border/60" />
      <button
        type="button"
        onClick={() => onSelect(CONNECTORS_ACTIVE)}
        className={itemClass(active === CONNECTORS_ACTIVE)}
      >
        {tr("Connectors", "连接器")}
      </button>
      <button
        type="button"
        onClick={() => onSelect(RUNTIME_ACTIVE)}
        className={itemClass(active === RUNTIME_ACTIVE)}
      >
        {tr("Runtime", "运行环境")}
      </button>
    </nav>
  );
}
