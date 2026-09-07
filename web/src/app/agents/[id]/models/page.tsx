"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Switch } from "@/components/ui/switch";
import { Brain, Plus, Pencil, Trash2, Check, Cpu, Loader2, Share2 } from "lucide-react";
import {
  getAgent,
  getConfig,
  listProviders,
  createProvider,
  updateProvider,
  deleteProvider,
  testProvider,
  testStoredProvider,
  updateAgent,
  type ModelEntry,
  type ProviderRow,
} from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useLocale } from "@/components/locale-provider";
import { useAgentName } from "@/hooks/use-agent-name";

// Per-agent Models page — same UI/UX as the admin /models page, but
// scoped to a single agent. Reads/writes agent-scoped provider rows
// (`scope=agent&scopeId=<agentId>`) and the agent's own model override.
//
// Precedence at runtime (see internal/gateway/userspace.go):
//   - Agent-scope providers shadow system providers by name.
//   - Agent-scope `agents.defaults.model` overrides system default.
// Empty override here => inherit system default.

// `models` are common model IDs pre-filled into the form when the
// preset is selected. The user can keep, edit, or remove them. Empty
// list means "no sensible default" (custom / openrouter / ollama all
// vary too much to ship a baked-in suggestion).
const PROVIDER_PRESETS: Record<
  string,
  { apiBase: string; apiType: string; authType: string; models: string[] }
> = {
  openai: { apiBase: "https://api.openai.com/v1", apiType: "openai-chat", authType: "bearer-token", models: ["gpt-5.5"] },
  openrouter: { apiBase: "https://openrouter.ai/api/v1", apiType: "openai-chat", authType: "bearer-token", models: [] },
  anthropic: { apiBase: "https://api.anthropic.com", apiType: "anthropic-messages", authType: "api-key", models: ["claude-opus-4-7", "claude-sonnet-4-7", "claude-haiku-4-5"] },
  deepseek: { apiBase: "https://api.deepseek.com", apiType: "openai-chat", authType: "bearer-token", models: ["deepseek-v4-pro", "deepseek-v4-flash"] },
  ollama: { apiBase: "http://localhost:11434/v1", apiType: "openai-chat", authType: "bearer-token", models: [] },
  custom: { apiBase: "", apiType: "openai-chat", authType: "bearer-token", models: [] },
};

const PROVIDER_LABELS: Record<string, string> = {
  openai: "OpenAI",
  openrouter: "OpenRouter",
  anthropic: "Anthropic",
  deepseek: "DeepSeek",
  ollama: "Ollama",
  custom: "Custom",
};

const API_TYPE_LABELS: Record<string, string> = {
  "openai-chat": "OpenAI Chat Completions",
  "anthropic-messages": "Anthropic Messages",
};

const AUTH_TYPE_LABELS: Record<string, string> = {
  "bearer-token": "Bearer Token",
  "api-key": "API Key Header",
};

interface ProviderEntry {
  id: string;          // configs row id — required for PUT/DELETE
  name: string;
  apiBase: string;
  apiKey: string;      // unmasked draft (only set while editing)
  maskedKey: string;   // server-returned masked key for display
  apiType: string;
  authType: string;
  models: ModelEntry[];
  // Inheritance source. Only "agent" rows are editable on this page;
  // "user" and "system" rows are read-only views of the chain that
  // resolves at runtime. Two same-name rows in different scopes can
  // coexist (lower scope shadows higher) — looking up by id avoids
  // the collision the old name-keyed lookups had.
  scope: "agent" | "user" | "system";
}

function emptyModel(): ModelEntry {
  return {
    id: "",
    name: "",
    reasoning: false,
    input: ["text"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 200000,
    maxTokens: 8192,
  };
}

// presetModelRows produces ready-to-edit ModelEntry rows for the IDs
// declared on a preset, so the dialog opens with common models already
// filled in instead of an empty list.
function presetModelRows(preset: string): ModelEntry[] {
  const ids = PROVIDER_PRESETS[preset]?.models || [];
  return ids.map((id) => ({ ...emptyModel(), id, name: id }));
}

export default function AgentModelsPage() {
  const { tr } = useLocale();
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);

  const [providers, setProviders] = useState<ProviderEntry[]>([]);
  const [model, setModel] = useState("");
  const [systemDefault, setSystemDefault] = useState("");
  const [systemProviders, setSystemProviders] = useState<string[]>([]);
  // Default true so the toggle reflects the on-state during the brief
  // window before fetchAll resolves. Backend treats absent key as on
  // (agentShareModelConfig in handlers_agents.go) — keep these aligned.
  const [shareModelConfig, setShareModelConfig] = useState(true);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  // Dialog state — mirrors the admin page exactly.
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingName, setEditingName] = useState<string | null>(null);
  // editingId: with the merged view, two rows can share `name` across
  // scopes (e.g. agent's "openai" override + system's "openai"). Lookups
  // for edit / test must use id.
  const [editingId, setEditingId] = useState<string | null>(null);
  const [formPreset, setFormPreset] = useState("openrouter");
  const [formName, setFormName] = useState("");
  const [formApiBase, setFormApiBase] = useState("");
  const [formApiKey, setFormApiKey] = useState("");
  const [formApiType, setFormApi] = useState("openai-chat");
  const [formAuthType, setFormAuthType] = useState("api-key");
  const [formModels, setFormModels] = useState<ModelEntry[]>([]);
  type ModelTestResult = { status: "idle" | "testing" | "success" | "error"; error?: string };
  const [modelTests, setModelTests] = useState<Record<number, ModelTestResult>>({});
  const [batchTesting, setBatchTesting] = useState(false);

  const cleanModelRows = formModels
    .map((m, idx) => ({ idx, id: m.id.trim() }))
    .filter((t) => t.id);
  const allModelsPassed =
    cleanModelRows.length === 0 ||
    cleanModelRows.every((t) => modelTests[t.idx]?.status === "success");

  // Dropdown lists models from every scope the agent will resolve at
  // runtime — agent overrides shadow user, user overrides system. We
  // dedupe on `provider/modelId` so a same-name override doesn't show
  // twice; the lower-scope row wins (agent > user > system) because it's
  // what would actually be chosen.
  const allModelOptions: { value: string; label: string }[] = useMemo(() => {
    const seen = new Set<string>();
    const order: ProviderEntry["scope"][] = ["agent", "user", "system"];
    const out: { value: string; label: string }[] = [];
    for (const sc of order) {
      for (const p of providers) {
        if (p.scope !== sc) continue;
        for (const m of p.models) {
          const value = `${p.name}/${m.id}`;
          if (seen.has(value)) continue;
          seen.add(value);
          out.push({ value, label: `${p.name}/${m.name || m.id}` });
        }
      }
    }
    return out;
  }, [providers]);

  const fetchAll = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    try {
      // We need the agent's owner to fetch user-scope inherited rows.
      // Pull agent record + all three provider scopes in parallel. The
      // user-scope call gets bound to the owner only after agentRec
      // resolves; doing the user list lazily here keeps things flat
      // without an awkward two-stage fetch.
      const [agentRec, agentScopeRes, sysScopeRes, cfg] = await Promise.all([
        getAgent(agentId).catch(() => null),
        listProviders("agent", agentId).catch(() => null),
        listProviders("system", "").catch(() => null),
        // /api/config may 403 for non-admins; if it does, we just lose
        // the "inheriting system default: X" hint, which is fine.
        getConfig().catch(() => null),
      ]);
      const ownerId = agentRec?.userId || "";
      // user-scope inheritance only applies if we know the owner;
      // anonymous fall-through means no user layer (rare — agents
      // without an owner shouldn't exist post-onboarding).
      const userScopeRes = ownerId
        ? await listProviders("user", ownerId).catch(() => null)
        : null;

      const toRows = (res: { providers?: ProviderRow[] } | null): ProviderRow[] =>
        res && Array.isArray(res.providers) ? (res.providers as ProviderRow[]) : [];
      const toEntry = (r: ProviderRow, sc: ProviderEntry["scope"]): ProviderEntry => ({
        id: r.id,
        name: r.name,
        apiBase: r.apiBase || "",
        apiKey: "",
        maskedKey: r.apiKey || "",
        apiType: r.apiType || "openai-chat",
        authType: r.authType || "bearer-token",
        models: r.models || [],
        scope: sc,
      });
      const merged: ProviderEntry[] = [
        ...toRows(agentScopeRes).map((r) => toEntry(r, "agent")),
        ...toRows(userScopeRes).map((r) => toEntry(r, "user")),
        ...toRows(sysScopeRes).map((r) => toEntry(r, "system")),
      ];
      setProviders(merged);
      setSystemDefault(cfg?.agents?.defaults?.model || "");
      setSystemProviders(toRows(sysScopeRes).map((r) => r.name));
      // The agent's own model override is already resolved server-side
      // by handleGetAgent → agentScopeModel (configs row at scope=agent,
      // name=agents.defaults). Reading from agentRec keeps this page in
      // sync with the rest of the app — `cfg.agents.list` is a stale TS
      // type from before per-agent overrides moved out of the merged
      // config; the Go side never populates it.
      setModel(agentRec?.model || "");
      // Backend always emits a definitive boolean (see agentShareModelConfig);
      // the ?? guards against a stale shape if the page is hit before
      // the binary upgrade lands.
      setShareModelConfig(agentRec?.shareModelConfig ?? true);
    } finally {
      setLoading(false);
    }
  }, [agentId]);

  useEffect(() => {
    fetchAll();
  }, [fetchAll]);

  const flashSaved = () => {
    setSaved(true);
    setTimeout(() => setSaved(false), 2000);
  };

  const openAddDialog = () => {
    setEditingName(null);
    setEditingId(null);
    setFormPreset("openai");
    setFormName("openai");
    setFormApiBase(PROVIDER_PRESETS["openai"].apiBase);
    setFormApi(PROVIDER_PRESETS["openai"].apiType);
    setFormAuthType(PROVIDER_PRESETS["openai"].authType);
    setFormApiKey("");
    setFormModels(presetModelRows("openai"));
    setModelTests({});
    setDialogOpen(true);
  };

  const openEditDialog = (provider: ProviderEntry) => {
    setEditingName(provider.name);
    setEditingId(provider.id);
    const preset = Object.keys(PROVIDER_PRESETS).includes(provider.name) ? provider.name : "custom";
    setFormPreset(preset);
    setFormName(provider.name);
    setFormApiBase(provider.apiBase);
    setFormApi(provider.apiType);
    setFormAuthType(provider.authType || "bearer-token");
    setFormApiKey("");
    setFormModels(
      (provider.models || []).map((m) => {
        const base = emptyModel();
        return {
          ...base,
          ...m,
          cost: { ...base.cost, ...(m.cost || {}) },
          input: m.input && m.input.length > 0 ? [...m.input] : base.input,
        };
      }),
    );
    setModelTests(
      provider.models
        ? Object.fromEntries(
            provider.models.map((_m, idx) => [idx, { status: "success" as const }]),
          )
        : {},
    );
    setDialogOpen(true);
  };

  // Preset switching is treated as "give me a clean slate for this
  // provider" — same way it overwrites apiBase/apiType, it also
  // refreshes the models list with the preset's known model IDs. Edit
  // mode (openEditDialog) loads stored models directly and never goes
  // through this path, so user-saved configurations are never clobbered.
  const handlePresetChange = (preset: string) => {
    setFormPreset(preset);
    const cfg = PROVIDER_PRESETS[preset];
    if (cfg) {
      setFormApiBase(cfg.apiBase);
      setFormApi(cfg.apiType);
      setFormAuthType(cfg.authType);
    }
    setFormName(preset === "custom" ? "" : preset);
    setFormModels(presetModelRows(preset));
    setModelTests({});
  };

  const handleTestConnection = async () => {
    const targets = formModels
      .map((m, idx) => ({ idx, id: m.id.trim() }))
      .filter((t) => t.id);
    if (targets.length === 0) return;
    const editingRow = editingId
      ? providers.find((p) => p.id === editingId)
      : undefined;
    const useStoredKey = !!editingRow && !formApiKey.trim();
    setBatchTesting(true);
    setModelTests((prev) => {
      const next = { ...prev };
      for (const t of targets) next[t.idx] = { status: "testing" };
      return next;
    });
    await Promise.all(
      targets.map(async ({ idx, id }) => {
        try {
          const result = useStoredKey && editingRow
            ? await testStoredProvider(editingRow.id, id, {
                apiBase: formApiBase,
                apiType: formApiType,
                authType: formAuthType,
              })
            : await testProvider({
                apiBase: formApiBase,
                apiKey: formApiKey,
                model: id,
                apiType: formApiType,
                authType: formAuthType,
              });
          setModelTests((prev) => ({
            ...prev,
            [idx]: result.ok
              ? { status: "success" }
              : { status: "error", error: result.error || tr("Connection failed", "连接失败") },
          }));
        } catch {
          setModelTests((prev) => ({
            ...prev,
            [idx]: { status: "error", error: tr("Connection failed", "连接失败") },
          }));
        }
      }),
    );
    setBatchTesting(false);
  };

  const handleAddModel = () => {
    setFormModels((prev) => [...prev, emptyModel()]);
  };

  const handleUpdateModel = (index: number, field: string, value: unknown) => {
    setFormModels((prev) => {
      const updated = [...prev];
      const m = { ...updated[index], cost: { ...updated[index].cost }, input: [...updated[index].input] };
      if (field === "id") m.id = value as string;
      else if (field === "name") m.name = value as string;
      else if (field === "reasoning") m.reasoning = value as boolean;
      else if (field === "contextWindow") m.contextWindow = Number(value) || 0;
      else if (field === "maxTokens") m.maxTokens = Number(value) || 0;
      updated[index] = m;
      return updated;
    });
    if (field === "id") {
      setModelTests((prev) => {
        if (prev[index] === undefined) return prev;
        const { [index]: _drop, ...rest } = prev;
        void _drop;
        return rest;
      });
    }
  };

  const handleRemoveModel = (index: number) => {
    setFormModels((prev) => prev.filter((_, i) => i !== index));
    setModelTests((prev) => {
      const next: Record<number, ModelTestResult> = {};
      for (const [k, v] of Object.entries(prev)) {
        const i = Number(k);
        if (i === index) continue;
        next[i > index ? i - 1 : i] = v;
      }
      return next;
    });
  };

  const handleSaveProvider = async () => {
    if (!agentId) return;
    const name = formName.toLowerCase().trim().replace(/\s+/g, "-");
    if (!name) return;
    const cleanedModels = formModels.filter((m) => m.id.trim());
    const editingRow = editingId
      ? providers.find((p) => p.id === editingId)
      : undefined;

    setSaving(true);
    try {
      if (editingRow) {
        await updateProvider(editingRow.id, {
          apiBase: formApiBase,
          apiKey: formApiKey || undefined,
          apiType: formApiType,
          authType: formAuthType,
          models: cleanedModels,
        });
      } else {
        await createProvider({
          scope: "agent",
          scopeId: agentId,
          name,
          apiBase: formApiBase,
          apiKey: formApiKey,
          apiType: formApiType,
          authType: formAuthType,
          models: cleanedModels,
        });
      }
      flashSaved();
    } finally {
      setSaving(false);
    }
    setDialogOpen(false);
    await fetchAll();
  };

  const handleDeleteProvider = async (row: ProviderEntry) => {
    setSaving(true);
    try {
      await deleteProvider(row.id);
      // If the active model came from this provider, the override is
      // now dangling — clear it so the agent falls back through the
      // chain at runtime.
      if (model.startsWith(`${row.name}/`)) {
        await updateAgent(agentId, { model: "" });
      }
      flashSaved();
    } finally {
      setSaving(false);
    }
    await fetchAll();
  };

  const handleModelChange = async (value: string) => {
    setModel(value);
    setSaving(true);
    try {
      // Empty string means "clear override → inherit system default".
      await updateAgent(agentId, { model: value });
      flashSaved();
    } finally {
      setSaving(false);
    }
  };

  const handleClearOverride = async () => {
    setModel("");
    setSaving(true);
    try {
      await updateAgent(agentId, { model: "" });
      flashSaved();
    } finally {
      setSaving(false);
    }
  };

  // Optimistic — flip the UI immediately, then persist. On failure we
  // revert. invalidateAgent on the server side drops every UserSpace
  // that lazy-attached this agent so chatters see the new gate on
  // their next message, no process restart required.
  const handleShareToggle = async (next: boolean) => {
    const prev = shareModelConfig;
    setShareModelConfig(next);
    setSaving(true);
    try {
      await updateAgent(agentId, { shareModelConfig: next });
      flashSaved();
    } catch {
      setShareModelConfig(prev);
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div className="p-6 space-y-6 max-w-5xl mx-auto">
        <Skeleton className="h-10 w-48" />
        <Skeleton className="h-32 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  const inheriting = !model.trim();

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">{tr("Models", "模型")}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {tr("LLM providers and active model for", "大模型服务商和当前模型，适用于")} {" "}
            <strong>{agentName || tr("this agent", "此 Agent")}</strong>
            {tr(". Agent settings override the system default.", "。Agent 配置优先于系统默认值。")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {saved && (
            <span className="inline-flex items-center gap-1.5 text-xs text-emerald-600 dark:text-emerald-400 mr-2">
              <Check className="h-3.5 w-3.5" /> {tr("Saved", "已保存")}
            </span>
          )}
          <Button variant="outline" onClick={openAddDialog} disabled={saving}>
            <Plus className="h-4 w-4 mr-2" />
            {tr("Add provider", "添加服务商")}
          </Button>
        </div>
      </div>

      {/* Share with chatters */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-start gap-3 min-w-0">
            <Share2 className="h-4 w-4 text-primary mt-0.5 shrink-0" />
            <div className="min-w-0">
              <h3 className="font-medium">{tr("Share model configuration with chatters", "向聊天用户共享模型配置")}</h3>
              <p className="text-sm text-muted-foreground mt-1">
                {shareModelConfig ? (
                  <>
                    {tr("Chatters using", "使用")} <strong>{agentName || tr("this agent", "此 Agent")}</strong>
                    {tr(" inherit your model and provider credentials. Their messages use your tokens.", " 的聊天用户会继承你的模型和服务商凭据，其消息将消耗你的 Token。")}
                  </>
                ) : (
                  <>
                    {tr("Only you use this configuration. Chatters use their own model and providers under ", "此配置仅供你使用。聊天用户需在")}
                    <em>{tr("User → Models", "用户 → 模型")}</em>
                    {tr(", otherwise the agent falls back to the system default.", "中配置自己的模型和服务商，否则 Agent 将回退到系统默认值。")}
                  </>
                )}
              </p>
            </div>
          </div>
          <Switch
            checked={shareModelConfig}
            onCheckedChange={handleShareToggle}
            disabled={saving}
            aria-label={tr("Share model configuration with chatters", "向聊天用户共享模型配置")}
          />
        </div>
      </div>

      {/* Active Model */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-center justify-between gap-2 mb-3">
          <div className="flex items-center gap-2">
            <Cpu className="h-4 w-4 text-primary" />
            <h3 className="font-medium">{tr("Active model", "当前模型")}</h3>
            {inheriting ? (
              <Badge variant="outline" className="text-[10px]">
                {tr("Inheriting", "继承中")}
              </Badge>
            ) : (
              <Badge className="bg-primary/10 text-primary hover:bg-primary/10 text-[10px]">
                {tr("Override", "已覆盖")}
              </Badge>
            )}
          </div>
          {!inheriting && (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 text-xs"
              onClick={handleClearOverride}
              disabled={saving}
            >
              {tr("Clear override", "清除覆盖")}
            </Button>
          )}
        </div>
        {allModelOptions.length > 0 ? (
          <Select
            value={model}
            onValueChange={(v: string | null) => v && handleModelChange(v)}
            disabled={saving}
          >
            <SelectTrigger className="font-mono text-sm max-w-md">
              <SelectValue placeholder={inheriting ? tr("Inherit ({{model}})", "继承（{{model}}）", { model: systemDefault || tr("no system default", "无系统默认值") }) : tr("Select a model", "选择模型")} />
            </SelectTrigger>
            <SelectContent className="!w-auto !min-w-[var(--anchor-width)] !overflow-x-visible">
              {allModelOptions.map((opt) => (
                <SelectItem key={opt.value} value={opt.value}>
                  <span className="font-mono text-sm whitespace-nowrap">{opt.value}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : (
          <Input
            value={model}
            onChange={(e) => setModel(e.target.value)}
            onBlur={() => handleModelChange(model)}
            placeholder={systemDefault ? tr("Inherit ({{model}})", "继承（{{model}}）", { model: systemDefault }) : tr("Add a provider with models below", "请在下方添加包含模型的服务商")}
            className="font-mono text-sm max-w-md"
          />
        )}
        <p className="text-xs text-muted-foreground mt-2">
          {inheriting ? (
            <>
              {tr("Using system default", "正在使用系统默认值")}
              {systemDefault ? (
                <>
                  : <code className="text-[11px]">{systemDefault}</code>
                </>
              ) : (
                <> {tr("(none configured)", "（尚未配置）")}</>
              )}
              {tr(". Pick a model above to override it only for", "。选择上方模型可仅为")} {" "}
              <strong>{agentName || tr("this agent", "此 Agent")}</strong>{tr(".", "覆盖该配置。")}
            </>
          ) : (
            <>
              {tr("Override applies only to", "覆盖配置仅适用于")} <strong>{agentName || tr("this agent", "此 Agent")}</strong>.
              {tr(" Format: ", " 格式：")}<code className="text-[11px]">provider/modelId</code>.
              {systemDefault && (
                <>
                  {" "}
                  {tr("Clearing falls back to", "清除后将回退到")} {" "}
                  <code className="text-[11px]">{systemDefault}</code>.
                </>
              )}
            </>
          )}
        </p>
      </div>

      {/* Providers Table */}
      {providers.length === 0 ? (
        <div className="rounded-lg border border-border bg-card">
          <div className="flex flex-col items-center justify-center py-16">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-amber-500/10 mb-4">
              <Brain className="h-7 w-7 text-amber-500" />
            </div>
            <p className="text-sm text-muted-foreground mb-1">{tr("No providers available", "没有可用的服务商")}</p>
            <p className="text-xs text-muted-foreground/60 mb-4 max-w-md text-center">
              {tr("No agent, user, or system providers are configured. Add one here for this agent, or configure shared providers from the top-level Models page.", "尚未配置 Agent、用户或系统级服务商。可在此为当前 Agent 添加凭据，或在顶层模型页面中配置共享服务商。")}
            </p>
            <Button variant="outline" size="sm" onClick={openAddDialog}>
              <Plus className="h-4 w-4 mr-2" />
              {tr("Add provider", "添加服务商")}
            </Button>
          </div>
        </div>
      ) : (
        <div className="rounded-lg border border-border bg-card overflow-hidden">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{tr("Name", "名称")}</TableHead>
                <TableHead>{tr("API base", "API 地址")}</TableHead>
                <TableHead>{tr("API key", "API 密钥")}</TableHead>
                <TableHead>{tr("Models", "模型")}</TableHead>
                <TableHead>{tr("Source", "来源")}</TableHead>
                <TableHead className="text-right">{tr("Actions", "操作")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {providers.map((provider) => {
                const editable = provider.scope === "agent";
                const sourceLabel =
                  provider.scope === "agent"
                    ? tr("Mine (agent)", "我的配置（Agent）")
                    : provider.scope === "user"
                    ? tr("Inherited from owner", "继承自所有者")
                    : tr("Inherited from admin", "继承自管理员");
                return (
                <TableRow key={`${provider.scope}:${provider.id}`}>
                  <TableCell className="font-medium">
                    <div className="flex items-center gap-2">
                      {provider.name}
                      {editable && systemProviders.includes(provider.name) && (
                        <Badge variant="outline" className="text-[10px]">
                          {tr("shadows system", "覆盖系统配置")}
                        </Badge>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>
                    <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
                      {provider.apiBase || "—"}
                    </code>
                  </TableCell>
                  <TableCell>
                    <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
                      {provider.maskedKey || "—"}
                    </code>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {provider.models.length}
                  </TableCell>
                  <TableCell>
                    {editable ? (
                      <Badge
                        variant="outline"
                        className="bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/20"
                      >
                        {sourceLabel}
                      </Badge>
                    ) : (
                      <Badge variant="outline" className="text-muted-foreground" title={tr("Read-only — owned by the owner or admin", "只读 — 由所有者或管理员维护")}>
                        {sourceLabel}
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button
                        size="icon"
                        variant="ghost"
                        onClick={() => openEditDialog(provider)}
                        title={editable ? tr("Edit", "编辑") : tr("Read-only — inherited row", "只读 — 继承的配置")}
                        disabled={!editable}
                      >
                        <Pencil className="size-4" />
                      </Button>
                      <Button
                        size="icon"
                        variant="ghost"
                        className="text-destructive hover:text-destructive"
                        onClick={() => handleDeleteProvider(provider)}
                        title={editable ? tr("Remove", "移除") : tr("Read-only — inherited row", "只读 — 继承的配置")}
                        disabled={!editable}
                      >
                        <Trash2 className="size-4" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              );})}
            </TableBody>
          </Table>
        </div>
      )}

      {/* Add/Edit Provider Dialog */}
      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="sm:max-w-2xl max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>
              {editingName ? tr("Edit provider", "编辑服务商") : tr("Add provider", "添加服务商")}
            </DialogTitle>
            <DialogDescription>
              {tr("Configure an LLM provider for", "为")} {" "}
              <strong>{agentName || tr("this agent", "此 Agent")}</strong>
              {tr(". Use the same name as a system provider to override it.", " 配置大模型服务商。使用与系统服务商相同的名称即可覆盖它。")}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label>{tr("Provider", "服务商")}</Label>
                <Select
                  value={formPreset}
                  onValueChange={(v: string | null) => v && handlePresetChange(v)}
                  disabled={!!editingName}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue>
                      {(v: unknown) => PROVIDER_LABELS[v as string] ?? (v as string) ?? ""}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {Object.keys(PROVIDER_PRESETS).map((p) => (
                      <SelectItem key={p} value={p}>
                        {p === "custom" ? tr("Custom", "自定义") : PROVIDER_LABELS[p] ?? p}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5">
                <Label>{tr("Provider name", "服务商名称")}</Label>
                <Input
                  value={formName}
                  onChange={(e) => setFormName(e.target.value)}
                  placeholder="openai"
                  className="font-mono text-sm"
                  disabled={!!editingName}
                />
              </div>
            </div>

            <div className="space-y-1.5">
              <Label>{tr("API base URL", "API 基础地址")}</Label>
              <Input
                value={formApiBase}
                onChange={(e) => setFormApiBase(e.target.value)}
                placeholder="https://api.openai.com/v1"
                className="font-mono text-sm"
              />
            </div>

            <div className="space-y-1.5">
              <Label>{tr("API key", "API 密钥")}</Label>
              <Input
                type={editingName && !formApiKey ? "text" : "password"}
                value={formApiKey}
                onChange={(e) => setFormApiKey(e.target.value)}
                placeholder={
                  editingName
                    ? (() => {
                        const row = providers.find((p) => p.id === editingId);
                        return row?.maskedKey || "sk-…";
                      })()
                    : "sk-…"
                }
                className="font-mono text-sm placeholder:text-muted-foreground/70"
              />
              {editingName && (
                <p className="text-[11px] text-muted-foreground/60">
                  {tr("Leave empty to keep the existing key. Connection tests use the saved key.", "留空可保留现有密钥；连接测试会使用已保存的密钥。")}
                </p>
              )}
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label>{tr("API type", "API 类型")}</Label>
                <Select value={formApiType} onValueChange={(v: string | null) => v && setFormApi(v)}>
                  <SelectTrigger className="w-full">
                    <SelectValue>
                      {(v: unknown) => API_TYPE_LABELS[v as string] ?? (v as string) ?? ""}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="openai-chat">OpenAI Chat Completions</SelectItem>
                    <SelectItem value="anthropic-messages">Anthropic Messages</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5">
                <Label>{tr("Auth type", "认证类型")}</Label>
                <Select value={formAuthType} onValueChange={(v: string | null) => v && setFormAuthType(v)}>
                  <SelectTrigger className="w-full">
                    <SelectValue>
                      {(v: unknown) => AUTH_TYPE_LABELS[v as string] ?? (v as string) ?? ""}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="bearer-token">Bearer Token</SelectItem>
                    <SelectItem value="api-key">{tr("API Key Header", "API Key 请求头")}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>

            <div className="space-y-3 pt-2 border-t border-border">
              <div className="flex items-center justify-between">
                <Label className="text-base">{tr("Models", "模型")}</Label>
                <Button variant="outline" size="sm" onClick={handleAddModel}>
                  <Plus className="h-3 w-3 mr-1.5" />
                  {tr("Add model", "添加模型")}
                </Button>
              </div>

              {formModels.length === 0 && (
                <p className="text-sm text-muted-foreground/60 text-center py-4">
                  {tr("No models configured. Add models to use with this provider.", "尚未配置模型。请添加要通过此服务商使用的模型。")}
                </p>
              )}

              {formModels.map((m, idx) => {
                const t = modelTests[idx];
                return (
                <div key={idx} className="rounded-lg border border-border bg-muted/30 p-4 space-y-3">
                  <div className="flex items-center justify-between gap-2">
                    <div className="flex items-center gap-2 min-w-0">
                      <span className="text-sm font-medium text-muted-foreground">
                        {tr("Model {{number}}", "模型 {{number}}", { number: idx + 1 })}
                      </span>
                      {t?.status === "testing" && (
                        <Badge variant="outline" className="text-[10px]">
                          <Loader2 className="mr-1 size-3 animate-spin" /> {tr("testing", "测试中")}
                        </Badge>
                      )}
                      {t?.status === "success" && (
                        <Badge className="bg-emerald-500/15 text-emerald-700 hover:bg-emerald-500/15 text-[10px]">
                          <Check className="mr-1 size-3" /> {tr("connected", "已连接")}
                        </Badge>
                      )}
                      {t?.status === "error" && (
                        <Badge variant="outline" className="border-destructive/40 text-destructive text-[10px]" title={t.error}>
                          {tr("failed", "失败")}
                        </Badge>
                      )}
                    </div>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 text-xs text-destructive hover:text-destructive"
                      onClick={() => handleRemoveModel(idx)}
                    >
                      <Trash2 className="h-3 w-3 mr-1" />
                      {tr("Remove", "移除")}
                    </Button>
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div className="space-y-1">
                      <Label className="text-xs">{tr("Model ID", "模型 ID")}</Label>
                      <Input
                        value={m.id}
                        onChange={(e) => handleUpdateModel(idx, "id", e.target.value)}
                        placeholder={tr("e.g. gpt-4o", "例如 gpt-4o")}
                        className="font-mono text-xs h-8"
                      />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs">{tr("Display name", "显示名称")}</Label>
                      <Input
                        value={m.name}
                        onChange={(e) => handleUpdateModel(idx, "name", e.target.value)}
                        placeholder={tr("e.g. GPT-4o", "例如 GPT-4o")}
                        className="text-xs h-8"
                      />
                    </div>
                  </div>
                </div>
                );
              })}

              <div className="flex flex-col gap-2 pt-2">
                <div className="flex items-center gap-3">
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={handleTestConnection}
                    disabled={
                      batchTesting ||
                      !formApiBase ||
                      cleanModelRows.length === 0
                    }
                  >
                    {batchTesting ? (
                      <>
                        <Loader2 className="mr-1 size-4 animate-spin" /> {tr("Testing", "正在测试")}
                      </>
                    ) : (
                      tr("Test connection", "测试连接")
                    )}
                  </Button>
                  <span className="text-xs text-muted-foreground">
                    {cleanModelRows.length === 0
                      ? tr("Add at least one model with an ID, then test.", "请至少添加一个带 ID 的模型，然后测试。")
                      : tr("Tests every model above; results appear beside each row.", "测试上方所有模型，结果会显示在每一行旁边。")}
                  </span>
                </div>
                {Object.values(modelTests).some((t) => t.status === "error") && (
                  <ul className="space-y-0.5">
                    {formModels.map((m, idx) => {
                      const t = modelTests[idx];
                      if (!t || t.status !== "error" || !m.id.trim()) return null;
                      return (
                        <li key={idx} className="text-xs text-destructive break-all">
                          <code className="font-mono">{m.id}</code>: {t.error}
                        </li>
                      );
                    })}
                  </ul>
                )}
              </div>
            </div>
          </div>
          <DialogFooter className="flex flex-col gap-2 sm:flex-row sm:items-center">
            {!allModelsPassed && (
              <span className="text-xs text-muted-foreground sm:mr-auto">
                {tr("Test every model first — Add or Update unlocks after all tests pass.", "请先测试所有模型；全部通过后才能添加或更新。")}
              </span>
            )}
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              {tr("Cancel", "取消")}
            </Button>
            <Button
              onClick={handleSaveProvider}
              disabled={!formName.trim() || saving || !allModelsPassed}
            >
              {editingName ? tr("Update", "更新") : tr("Add", "添加")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
