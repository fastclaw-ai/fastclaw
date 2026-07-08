"use client";

import { useEffect, useMemo, useState } from "react";
import { useTranslations } from "next-intl";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Wrench,
  Save,
  Check,
  Loader2,
  ChevronUp,
  ChevronDown,
  X,
  Plus,
} from "lucide-react";
import {
  getTools,
  saveTools,
  type ToolsConfig,
  type ToolCategoryCatalog,
  type ToolProviderCatalog,
  type ToolProviderSettings,
  type ToolCategorySettings,
} from "@/lib/api";
import RuntimeSettingsPage from "@/app/settings/runtime/page";

// Sentinel value used as the active rail entry when Runtime is selected.
// Real tool categories never start with "__" so this can never collide.
const RUNTIME_ACTIVE = "__runtime__";

export default function ToolsPage() {
  const t = useTranslations("tools");
  const tc = useTranslations("common");
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
      .catch((e) => setError(e instanceof Error ? e.message : t("errorLoadFailed")))
      .finally(() => setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

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
        setError(resp.error || t("errorSaveFailed"));
      } else {
        setSaved(true);
        setTimeout(() => setSaved(false), 2000);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : t("errorSaveFailed"));
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
        <h2 className="text-lg font-semibold tracking-tight mb-3 md:mb-4">{t("title")}</h2>
        <CategoryRail
          categories={cfg?.categories || []}
          active={active}
          onSelect={setActive}
        />
      </aside>
      <div className="flex-1 min-w-0">
        {error && active !== RUNTIME_ACTIVE && (
          <div className="mb-4 rounded-md border border-destructive/40 bg-destructive/10 px-4 py-2 text-sm text-destructive">
            {error}
          </div>
        )}

        {active === RUNTIME_ACTIVE ? (
          // Runtime is a deployment-wide knob (sandbox backend, etc.), not
          // a per-category provider; it lives in the same rail as the tool
          // categories purely as a convenient admin entry point. The
          // component manages its own save / loading state.
          <RuntimeSettingsPage />
        ) : !cfg || cfg.categories.length === 0 ? (
          <div className="rounded-lg border border-border bg-card p-8 text-center">
            <Wrench className="h-8 w-8 text-muted-foreground/40 mx-auto mb-3" />
            <p className="text-sm text-muted-foreground">
              {t("emptyCategories")}
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
                  <><Check className="h-4 w-4 mr-2" /> {t("saved")}</>
                ) : saving ? (
                  <><Loader2 className="h-4 w-4 mr-2 animate-spin" /> {tc("saving")}</>
                ) : (
                  <><Save className="h-4 w-4 mr-2" /> {tc("save")}</>
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
  const t = useTranslations("tools");
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
          {c.label}
        </button>
      ))}
      {/* Runtime sits at the bottom of the rail (or rightmost on mobile);
          super_admin-only — the underlying RuntimeSettingsPage redirects
          anyone else away. The hairline divider visually separates it
          from the per-category tool entries. */}
      <div className="hidden md:block my-1 border-t border-border/60" />
      <button
        type="button"
        onClick={() => onSelect(RUNTIME_ACTIVE)}
        className={itemClass(active === RUNTIME_ACTIVE)}
      >
        {t("runtime")}
      </button>
    </nav>
  );
}

function CategoryPanel({
  catalog,
  providers,
  setProvider,
  tools,
  setTools,
  saveButton,
}: {
  catalog: ToolCategoryCatalog;
  providers: Record<string, ToolProviderSettings>;
  setProvider: (name: string, patch: Partial<ToolProviderSettings>) => void;
  tools: ToolCategorySettings;
  setTools: (patch: Partial<ToolCategorySettings>) => void;
  saveButton?: React.ReactNode;
}) {
  const t = useTranslations("tools");
  // Which provider's config to render. Default: the first one that
  // already has a value, else the first provider in the catalog. The
  // selector only swaps the visible config — every provider's state
  // is still held in the parent `providers` map so a single Save
  // persists every key the user has touched.
  const firstConfigured = catalog.providers.find((p) => {
    const s = providers[p.name];
    return (s?.apiKey && s.apiKey.trim()) || (s?.endpoint && s.endpoint.trim());
  });
  const [selectedProvider, setSelectedProvider] = useState<string>(
    firstConfigured?.name || catalog.providers[0]?.name || "",
  );
  const selected = catalog.providers.find((p) => p.name === selectedProvider);

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h3 className="text-xl font-semibold tracking-tight">{catalog.label}</h3>
          <p className="text-sm text-muted-foreground mt-1">
            {t("categoryHint")}
          </p>
        </div>
        {saveButton}
      </div>

      {catalog.providers.length > 0 && (
        <div className="rounded-lg border border-border bg-card">
          <div className="p-5 space-y-4">
            <div className="space-y-2">
              <Label>{t("providerLabel")}</Label>
              <Select
                value={selectedProvider}
                onValueChange={(v) => v && setSelectedProvider(v)}
              >
                <SelectTrigger className="w-full">
                  <SelectValue placeholder={t("providerPlaceholder")}>
                    {(v: unknown) =>
                      catalog.providers.find((p) => p.name === v)?.label ??
                      (v as string) ??
                      ""
                    }
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {catalog.providers.map((p) => (
                    <SelectItem key={p.name} value={p.name}>
                      {p.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {selected && selected.name === "none" ? (
              <p className="text-xs text-muted-foreground pt-1">
                {t.rich("noneProviderHint", {
                  name: catalog.name,
                  code: (chunks) => <code className="font-mono">{chunks}</code>,
                })}
              </p>
            ) : selected && (
              <ProviderFields
                provider={selected}
                settings={providers[selected.name] || {}}
                onChange={(patch) => setProvider(selected.name, patch)}
              />
            )}
          </div>
        </div>
      )}

      {/* Fallback chain editor */}
      <ChainEditor catalog={catalog} providers={providers} tools={tools} setTools={setTools} />
    </div>
  );
}

function ProviderFields({
  provider,
  settings,
  onChange,
}: {
  provider: ToolProviderCatalog;
  settings: ToolProviderSettings;
  onChange: (patch: Partial<ToolProviderSettings>) => void;
}) {
  const t = useTranslations("tools");
  const [showAdvanced, setShowAdvanced] = useState(false);
  const defaultModel = settings.options?.model || "";

  const setOption = (k: string, v: string) => {
    const opts = { ...(settings.options || {}) };
    if (v === "") delete opts[k];
    else opts[k] = v;
    onChange({ options: opts });
  };

  return (
    <div className="space-y-3 pt-1">
      {provider.needsKey && (
        <div className="space-y-2">
          <Label>{t("apiKeyLabel")}</Label>
          <Input
            type="password"
            placeholder="sk-…"
            value={settings.apiKey || ""}
            onChange={(e) => onChange({ apiKey: e.target.value })}
            className="font-mono text-sm"
          />
        </div>
      )}
      {provider.needsUrl && (
        <div className="space-y-2">
          <Label>{t("endpointLabel")}</Label>
          <Input
            type="url"
            placeholder="https://searxng.example.com"
            value={settings.endpoint || ""}
            onChange={(e) => onChange({ endpoint: e.target.value })}
            className="font-mono text-sm"
          />
        </div>
      )}
      {provider.models.length > 1 && (
        <div className="space-y-2">
          <Label>{t("defaultModelLabel")}</Label>
          <Input
            value={defaultModel}
            onChange={(e) => setOption("model", e.target.value)}
            placeholder={provider.models[0]}
            className="font-mono text-sm"
          />
          <p className="text-[10px] text-muted-foreground">
            {t.rich("defaultModelHint", {
              name: provider.name,
              code: (chunks) => <code className="font-mono">{chunks}</code>,
            })}{" "}
            {t("suggestedModels")}{" "}
            {provider.models.map((m, i) => (
              <span key={m}>
                {i > 0 && ", "}
                <code className="font-mono">{m}</code>
              </span>
            ))}
          </p>
        </div>
      )}

      <button
        onClick={() => setShowAdvanced((v) => !v)}
        className="text-[11px] text-muted-foreground hover:text-foreground transition-colors"
      >
        {showAdvanced ? t("hideAdvanced") : t("showAdvanced")}
      </button>

      {showAdvanced && (
        <AdvancedOptionsEditor
          options={settings.options || {}}
          onChange={(next) => onChange({ options: next })}
        />
      )}
    </div>
  );
}

function AdvancedOptionsEditor({
  options,
  onChange,
}: {
  options: Record<string, string>;
  onChange: (next: Record<string, string>) => void;
}) {
  const t = useTranslations("tools");
  const [newKey, setNewKey] = useState("");
  const [newVal, setNewVal] = useState("");

  const addPair = () => {
    if (!newKey.trim()) return;
    onChange({ ...options, [newKey.trim()]: newVal });
    setNewKey("");
    setNewVal("");
  };

  const removeKey = (k: string) => {
    const next = { ...options };
    delete next[k];
    onChange(next);
  };

  const entries = Object.entries(options).filter(([k]) => k !== "model");

  return (
    <div className="rounded-md border border-border/70 bg-muted/20 p-3 space-y-2">
      <p className="text-[10px] uppercase tracking-wider text-muted-foreground">
        {t("providerOptions")}
      </p>
      {entries.length === 0 && (
        <p className="text-[11px] text-muted-foreground italic">
          {t("noCustomOptions")}
        </p>
      )}
      {entries.map(([k, v]) => (
        <div key={k} className="flex items-center gap-2">
          <Input
            readOnly
            value={k}
            className="h-8 text-xs font-mono w-40"
          />
          <Input
            value={v}
            onChange={(e) => onChange({ ...options, [k]: e.target.value })}
            className="h-8 text-xs font-mono flex-1"
          />
          <Button
            size="icon"
            variant="ghost"
            className="h-7 w-7 text-muted-foreground hover:text-destructive"
            onClick={() => removeKey(k)}
          >
            <X className="h-3.5 w-3.5" />
          </Button>
        </div>
      ))}
      <div className="flex items-center gap-2 pt-1">
        <Input
          placeholder={t("optionKeyPlaceholder")}
          value={newKey}
          onChange={(e) => setNewKey(e.target.value)}
          className="h-8 text-xs font-mono w-40"
        />
        <Input
          placeholder={t("optionValuePlaceholder")}
          value={newVal}
          onChange={(e) => setNewVal(e.target.value)}
          className="h-8 text-xs font-mono flex-1"
        />
        <Button size="icon" variant="ghost" className="h-7 w-7" onClick={addPair}>
          <Plus className="h-3.5 w-3.5" />
        </Button>
      </div>
    </div>
  );
}

function ChainEditor({
  catalog,
  providers,
  tools,
  setTools,
}: {
  catalog: ToolCategoryCatalog;
  providers: Record<string, ToolProviderSettings>;
  tools: ToolCategorySettings;
  setTools: (patch: Partial<ToolCategorySettings>) => void;
}) {
  const t = useTranslations("tools");
  // Each provider contributes at most one chain option, using whichever
  // model the admin actually configured in the Default model input.
  // Providers with a single catalog model (e.g. the None sentinel, or
  // built-ins that don't expose a model knob) are auto-configured with
  // that one option so they remain pickable without the admin having
  // to type anything. Providers with multiple models and no typed
  // default are skipped — keeping the dropdown short and avoiding chain
  // entries that reference models the admin hasn't actively chosen.
  const refOptions = useMemo(() => {
    const opts: { value: string; label: string }[] = [];
    for (const p of catalog.providers) {
      const typed = providers[p.name]?.options?.model?.trim();
      let model = "";
      if (typed) {
        model = typed;
      } else if (p.models.length === 1) {
        model = p.models[0];
      } else {
        continue;
      }
      opts.push({ value: `${p.name}/${model}`, label: `${p.label} — ${model}` });
    }
    return opts;
  }, [catalog, providers]);

  const autoFallback = tools.autoFallback !== false;
  const chain = [tools.primary, ...(tools.fallbacks || [])].filter(Boolean) as string[];

  const setChain = (next: string[]) => {
    setTools({ primary: next[0] || "", fallbacks: next.slice(1) });
  };

  const addToChain = (ref: string) => {
    if (!ref || chain.includes(ref)) return;
    setChain([...chain, ref]);
  };

  const removeFromChain = (i: number) => {
    const next = [...chain];
    next.splice(i, 1);
    setChain(next);
  };

  const move = (i: number, delta: number) => {
    const j = i + delta;
    if (j < 0 || j >= chain.length) return;
    const next = [...chain];
    [next[i], next[j]] = [next[j], next[i]];
    setChain(next);
  };

  const unusedOptions = refOptions.filter((o) => !chain.includes(o.value));

  return (
    <div className="rounded-lg border border-border bg-card p-5">
      <div className="flex items-center justify-between mb-3">
        <Label className="text-xs uppercase tracking-wider text-muted-foreground">
          {t("fallbackChainLabel")}
        </Label>
        <div className="flex items-center gap-2">
          <span className="text-xs text-muted-foreground">{t("autoFallback")}</span>
          <Switch
            checked={autoFallback}
            onCheckedChange={(v) => setTools({ autoFallback: v })}
          />
        </div>
      </div>

      <div className="space-y-1.5">
        {chain.length === 0 ? (
          <p className="text-xs text-muted-foreground italic px-2 py-4">
            {t.rich("emptyChain", {
              name: catalog.name,
              code: (chunks) => <code className="font-mono">{chunks}</code>,
            })}
          </p>
        ) : (
          chain.map((ref, i) => {
            const label = refOptions.find((o) => o.value === ref)?.label || ref;
            return (
              <div
                key={ref}
                className="flex items-center gap-2 rounded-md border border-border bg-muted/30 px-3 py-2"
              >
                <span className="text-[11px] font-mono text-muted-foreground w-6">
                  {i === 0 ? "1°" : `${i + 1}`}
                </span>
                <span className="text-sm flex-1">{label}</span>
                <span className="text-[11px] font-mono text-muted-foreground">{ref}</span>
                <div className="flex gap-0.5">
                  <Button size="icon" variant="ghost" className="h-6 w-6" onClick={() => move(i, -1)} disabled={i === 0}>
                    <ChevronUp className="h-3.5 w-3.5" />
                  </Button>
                  <Button size="icon" variant="ghost" className="h-6 w-6" onClick={() => move(i, 1)} disabled={i === chain.length - 1}>
                    <ChevronDown className="h-3.5 w-3.5" />
                  </Button>
                  <Button size="icon" variant="ghost" className="h-6 w-6 text-muted-foreground hover:text-destructive" onClick={() => removeFromChain(i)}>
                    <X className="h-3.5 w-3.5" />
                  </Button>
                </div>
              </div>
            );
          })
        )}
      </div>

      {unusedOptions.length > 0 && (
        <div className="mt-3 flex items-center gap-2">
          <Plus className="h-3.5 w-3.5 text-muted-foreground" />
          <Select onValueChange={(v) => v && addToChain(v)} value="">
            <SelectTrigger className="w-64 h-8 text-xs">
              <SelectValue placeholder={t("addProviderPlaceholder")} />
            </SelectTrigger>
            <SelectContent>
              {unusedOptions.map((o) => (
                <SelectItem key={o.value} value={o.value}>
                  {o.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      )}
    </div>
  );
}
