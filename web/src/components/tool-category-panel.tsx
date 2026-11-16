"use client";

// One tool category's settings (providers, keys, fallback chain): System →
// Tools edits the deployment's, Console → Tools a user's own layer.

import { useMemo, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { ChevronUp, ChevronDown, X, Plus } from "lucide-react";
import type {
  ToolCategoryCatalog,
  ToolProviderCatalog,
  ToolProviderSettings,
  ToolCategorySettings,
} from "@/lib/api";
import { useLocale } from "@/components/locale-provider";

/** The category's name in the UI language. */
export function useToolCategoryLabel() {
  const { tr } = useLocale();
  return (name: string, label: string) =>
    ({
      web_search: tr("Web Search", "网页搜索"),
      web_fetch: tr("Web Fetch", "网页读取"),
      image_gen: tr("Image Generation", "图像生成"),
      tts: tr("Text-to-Speech", "文本转语音"),
    } as Record<string, string>)[name] || label;
}

export function CategoryPanel({
  catalog,
  providers,
  setProvider,
  tools,
  setTools,
  saveButton,
  keySet,
  intro,
}: {
  catalog: ToolCategoryCatalog;
  providers: Record<string, ToolProviderSettings>;
  setProvider: (name: string, patch: Partial<ToolProviderSettings>) => void;
  tools: ToolCategorySettings;
  setTools: (patch: Partial<ToolCategorySettings>) => void;
  saveButton?: React.ReactNode;
  /** Providers whose key is saved but not shown (Console → Tools). */
  keySet?: Record<string, boolean>;
  /** Replaces the description under the title. */
  intro?: React.ReactNode;
}) {
  const { tr } = useLocale();
  const categoryLabel = useToolCategoryLabel();
  // Which provider's config to render. Default: the first one that
  // already has a value, else the first provider in the catalog. The
  // selector only swaps the visible config — every provider's state
  // is still held in the parent `providers` map so a single Save
  // persists every key the user has touched.
  const firstConfigured = catalog.providers.find((p) => {
    const s = providers[p.name];
    return (s?.apiKey && s.apiKey.trim()) || keySet?.[p.name] || (s?.endpoint && s.endpoint.trim());
  });
  const [selectedProvider, setSelectedProvider] = useState<string>(
    firstConfigured?.name || catalog.providers[0]?.name || "",
  );
  const selected = catalog.providers.find((p) => p.name === selectedProvider);

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h3 className="text-xl font-semibold tracking-tight">{categoryLabel(catalog.name, catalog.label)}</h3>
          {intro ?? (
            <p className="text-sm text-muted-foreground mt-1">
              {tr("Configure provider API keys and fallback order. Tools without a configured provider are hidden from agents.", "配置服务商 API 密钥和回退顺序。未配置服务商的工具不会向 Agent 显示。")}
            </p>
          )}
        </div>
        {saveButton}
      </div>

      {catalog.providers.length > 0 && (
        <div className="rounded-lg border border-border bg-card">
          <div className="p-5 space-y-4">
            <div className="space-y-2">
              <Label>{tr("Provider", "服务商")}</Label>
              <Select
                value={selectedProvider}
                onValueChange={(v) => v && setSelectedProvider(v)}
              >
                <SelectTrigger className="w-full">
                  <SelectValue placeholder={tr("Choose a provider", "选择服务商")}>
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
                      {p.name === "none" ? tr(p.label, "无（依赖模型原生能力）") : p.name === "direct" ? tr(p.label, "直接访问（内置）") : p.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {selected && selected.name === "none" ? (
              <p className="text-xs text-muted-foreground pt-1">
                {tr("No external backend. To apply this choice, make", "不使用外部后端。要使此选择生效，请将")} {" "}
                <code className="font-mono">none/default</code>{" "}
                {tr("the only entry in the fallback chain below. The", "设为下方回退链中的唯一条目。")} {" "}<code className="font-mono">{catalog.name}</code>{" "}
                {tr("tool will then be hidden from agents, so the model uses its native capability when available.", "工具将不会向 Agent 显示，模型会在可用时使用自身原生能力。")}
              </p>
            ) : selected && (
              <ProviderFields
                provider={selected}
                settings={providers[selected.name] || {}}
                keySet={!!keySet?.[selected.name]}
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
  keySet,
  onChange,
}: {
  provider: ToolProviderCatalog;
  settings: ToolProviderSettings;
  keySet?: boolean;
  onChange: (patch: Partial<ToolProviderSettings>) => void;
}) {
  const { tr } = useLocale();
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
          <Label>{tr("API key", "API 密钥")}</Label>
          <Input
            type="password"
            placeholder={keySet ? tr("Saved. Leave empty to keep it", "已保存，留空则不变") : "sk-…"}
            value={settings.apiKey || ""}
            onChange={(e) => onChange({ apiKey: e.target.value })}
            className="font-mono text-sm"
          />
        </div>
      )}
      {provider.needsUrl && (
        <div className="space-y-2">
          <Label>{tr("Endpoint", "服务地址")}</Label>
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
          <Label>{tr("Default model", "默认模型")}</Label>
          <Input
            value={defaultModel}
            onChange={(e) => setOption("model", e.target.value)}
            placeholder={provider.models[0]}
            className="font-mono text-sm"
          />
          <p className="text-[10px] text-muted-foreground">
            {tr("Used when a chain entry omits the model (for example", "回退链条目省略模型时使用（例如仅填写")} {" "}
            <code className="font-mono">{provider.name}</code>{tr("). Suggested:", "）。建议值：")} {" "}
            {provider.models.map((m, i) => (
              <span key={m}>
                {i > 0 && ", "}
                <code className="font-mono">{m}</code>
              </span>
            ))}
            .
          </p>
        </div>
      )}

      <button
        onClick={() => setShowAdvanced((v) => !v)}
        className="text-[11px] text-muted-foreground hover:text-foreground transition-colors"
      >
        {showAdvanced ? tr("Hide advanced options", "收起高级选项") : tr("Show advanced options", "显示高级选项")}
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
  const { tr } = useLocale();
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
        {tr("Provider-specific options", "服务商专用选项")}
      </p>
      {entries.length === 0 && (
        <p className="text-[11px] text-muted-foreground italic">
          {tr("No custom options. The provider uses its defaults.", "没有自定义选项，服务商将使用默认值。")}
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
          placeholder={tr("key", "键")}
          value={newKey}
          onChange={(e) => setNewKey(e.target.value)}
          className="h-8 text-xs font-mono w-40"
        />
        <Input
          placeholder={tr("value", "值")}
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
  const { tr } = useLocale();
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
          {tr("Fallback chain (top → bottom)", "回退链（从上到下）")}
        </Label>
        <div className="flex items-center gap-2">
          <span className="text-xs text-muted-foreground">{tr("Automatic fallback", "自动回退")}</span>
          <Switch
            checked={autoFallback}
            onCheckedChange={(v) => setTools({ autoFallback: v })}
          />
        </div>
      </div>

      <div className="space-y-1.5">
        {chain.length === 0 ? (
          <p className="text-xs text-muted-foreground italic px-2 py-4">
            {tr("No providers selected. The", "尚未选择服务商。添加至少一个服务商之前，")} {" "}<code className="font-mono">{catalog.name}</code>{" "}
            {tr("tool is unavailable to agents until you add at least one.", "工具将不会向 Agent 提供。")}
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
              <SelectValue placeholder={tr("Add provider to chain…", "将服务商添加到回退链…")} />
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
