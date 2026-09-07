"use client";

import { useEffect, useState } from "react";
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
import { Brain, Plus, Pencil, Trash2, Check, Cpu, Loader2 } from "lucide-react";
import {
  getAgent,
  getConfig,
  updateConfig,
  getMe,
  testProvider,
  testStoredProvider,
  listProviders,
  createProvider,
  updateProvider,
  deleteProvider,
  type ModelEntry,
  type ProviderRow,
} from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useLocale } from "@/components/locale-provider";

// Keep these maps in sync with onboard's ProviderStep so the two flows
// look and behave identically — same preset set, same labels, same
// SelectValue render-children pattern.
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
  // scope tells the row apart from the inherited (system) ones a regular
  // user is allowed to see but not mutate. "system" and "agent" rows
  // render with an Inherited badge and disabled edit/delete; "user" rows
  // are the caller's own and fully editable. "agent" only shows up when
  // the page is mounted inside an agent context (chatter viewing a
  // shared agent whose owner enabled shareModelConfig).
  scope: "system" | "user" | "agent";
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

export default function ModelsPage({ scope = "auto" }: { scope?: "auto" | "user" | "system" }) {
  const { tr } = useLocale();
  // Agent context is auto-detected from the URL. The standalone /models
  // page lives outside any /agents/<id>/ path, so the hook returns
  // "default" and we render the plain user-scope view. When this same
  // component is mounted inside the agent settings dialog (chatter
  // viewing a shared agent), the URL is /agents/<id>/... and we pick up
  // the id here — the inheritance chain then includes the agent-scope
  // model + providers (when the owner enabled shareModelConfig).
  const urlAgentId = useAgentIdFromURL();
  const inAgentContext = urlAgentId !== "default" && urlAgentId !== "";
  const [agentName, setAgentName] = useState("");
  const [agentScopeModel, setAgentScopeModel] = useState("");
  const [agentShares, setAgentShares] = useState(false);

  const [providers, setProviders] = useState<ProviderEntry[]>([]);
  const [model, setModel] = useState("");
  // System-only resolution from /api/config?meta. For super_admin this
  // equals `model` always (they ARE system); for regular users it's the
  // value they'd inherit if their user-scope override were cleared. Used
  // only for the Inheriting/Override badge + caption.
  const [systemDefault, setSystemDefault] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  // The settings dialog supplies an explicit scope so a super_admin can
  // manage both a personal override and the system default. Standalone uses
  // "auto" for backwards compatibility: admin → system, everyone else → user.
  const [me, setMe] = useState<{ id: string; role: string } | null>(null);
  const isSuperAdmin = me?.role === "super_admin";
  const isSystemScope = scope === "system" || (scope === "auto" && isSuperAdmin);
  const writeScope: "system" | "user" = isSystemScope ? "system" : "user";
  const writeScopeId = isSystemScope ? "" : (me?.id || "");

  // Dialog state
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingName, setEditingName] = useState<string | null>(null);
  // editingId disambiguates rows when admin's "shared" provider and a
  // user's "private" override happen to share the same name. The find()
  // helpers always match by id; editingName remains for display only.
  const [editingId, setEditingId] = useState<string | null>(null);
  const [formPreset, setFormPreset] = useState("openrouter");
  const [formName, setFormName] = useState("");
  const [formApiBase, setFormApiBase] = useState("");
  const [formApiKey, setFormApiKey] = useState("");
  const [formApiType, setFormApi] = useState("openai-chat");
  const [formAuthType, setFormAuthType] = useState("api-key");
  const [formModels, setFormModels] = useState<ModelEntry[]>([]);
  // Per-model test results keyed by model index in formModels. We test
  // every configured model so the user sees which model IDs the provider
  // actually exposes — a single "ping the base URL" check would mask
  // typos in any individual model id.
  type ModelTestResult = { status: "idle" | "testing" | "success" | "error"; error?: string };
  const [modelTests, setModelTests] = useState<Record<number, ModelTestResult>>({});
  const [batchTesting, setBatchTesting] = useState(false);

  // Add/Update is gated on every non-empty model having a green test
  // result. Empty model rows are ignored (they get filtered out at
  // save), an explicit "no models configured" provider is allowed
  // through (rare but legal — e.g. seeding before the catalog is known).
  const cleanModelRows = formModels
    .map((m, idx) => ({ idx, id: m.id.trim() }))
    .filter((t) => t.id);
  const allModelsPassed =
    cleanModelRows.length === 0 ||
    cleanModelRows.every((t) => modelTests[t.idx]?.status === "success");

  // Collect all provider/model options for the default-model dropdown.
  // Dedupe on `provider/modelId` and walk inner→outer scope so the more
  // specific row wins the label when two rows share a name (e.g. an
  // agent-scope "openai" override shadowing the system "openai").
  // providers[] is already pre-sorted agent → user → system by
  // fetchConfig, so the natural traversal order is correct.
  const allModelOptions: { value: string; label: string }[] = (() => {
    const seen = new Set<string>();
    const out: { value: string; label: string }[] = [];
    for (const p of providers) {
      for (const m of p.models) {
        const value = `${p.name}/${m.id}`;
        if (seen.has(value)) continue;
        seen.add(value);
        out.push({ value, label: `${p.name}/${m.name || m.id}` });
      }
    }
    return out;
  })();

  // Providers are stored in the configs table and only round-trip through
  // the dedicated /api/providers endpoints — POST /api/config silently
  // ignores the providers map. agents.defaults.model is read off the
  // merged /api/config response (so it picks up system+user overlay) but
  // written back through the same endpoint. Keep the two writes split so
  // an empty default-model field doesn't blow away provider rows, and a
  // provider mutation doesn't accidentally clear the default model.
  const fetchConfig = async (
    asAdmin: boolean,
    userId: string,
  ) => {
    setLoading(true);
    try {
      const systemScope = scope === "system" || (scope === "auto" && asAdmin);
      const configScope: "system" | "user" = systemScope ? "system" : "user";
      // Admin: system is the source of truth.
      // Regular user: system (inherited) + own user-scope rows.
      // Agent context (chatter on a shared agent): also pull the agent
      //   record + agent-scope providers. The latter is gated on
      //   shareModelConfig=true server-side; a 403 just means sharing is
      //   off and we render the plain user-scope view.
      const [cfg, sysRes, userRes, agentRec, agentRes] = await Promise.all([
        getConfig(configScope).catch(() => null),
        listProviders("system", "").catch(() => null),
        systemScope ? Promise.resolve(null) : listProviders("user", userId).catch(() => null),
        inAgentContext ? getAgent(urlAgentId).catch(() => null) : Promise.resolve(null),
        inAgentContext ? listProviders("agent", urlAgentId).catch(() => null) : Promise.resolve(null),
      ]);
      const sysRows: ProviderRow[] = (sysRes && Array.isArray(sysRes.providers))
        ? (sysRes.providers as ProviderRow[])
        : [];
      const userRows: ProviderRow[] = (userRes && Array.isArray(userRes.providers))
        ? (userRes.providers as ProviderRow[])
        : [];
      const agentRows: ProviderRow[] = (agentRes && Array.isArray(agentRes.providers))
        ? (agentRes.providers as ProviderRow[])
        : [];
      const toEntry = (r: ProviderRow, sc: "system" | "user" | "agent"): ProviderEntry => ({
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
      // Order: agent (most specific) → user → system. Read-only "agent"
      // rows only appear for non-owners viewing a shared agent (the
      // owner sees the dedicated AgentModelsPage that owns agent-scope
      // editing); we still skip them for admins because admin's
      // standalone /models page is system-scope only.
      const entries: ProviderEntry[] = systemScope
        ? sysRows.map((r) => toEntry(r, "system"))
        : [
            ...agentRows.map((r) => toEntry(r, "agent")),
            ...userRows.map((r) => toEntry(r, "user")),
            ...sysRows.map((r) => toEntry(r, "system")),
          ];
      setProviders(entries);
      setModel(cfg?.agents?.defaults?.model || "");
      setSystemDefault(cfg?.meta?.systemDefaultModel || "");
      const ag = (agentRec as { agent?: { name?: string; model?: string; shareModelConfig?: boolean } } | null)?.agent;
      setAgentName(ag?.name || "");
      setAgentScopeModel(ag?.model || "");
      setAgentShares(!!ag?.shareModelConfig);
    } finally {
      setLoading(false);
    }
  };

  // Resolve identity first, then fetch — admin gets system only, regular
  // user gets the union (system inherited + own user-scope rows).
  useEffect(() => {
    getMe().then((m) => {
      if (!m?.user) return;
      const meRec = { id: m.user.id, role: m.user.role };
      setMe(meRec);
      fetchConfig(meRec.role === "super_admin", meRec.id);
    });
  }, [scope]);

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
    // Saved providers may have models persisted before we shipped the
    // full ModelEntry schema (no cost block, no input array). Layer
    // emptyModel() defaults under the saved data so the dialog always
    // has well-formed objects to edit.
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
    // Pre-mark every model in an editing session as "success" so the
    // user isn't forced to re-test every existing model just to change
    // a display name. Editing the model id, hitting Test, or removing /
    // re-adding rows will reset their status as expected.
    setModelTests(
      provider.models
        ? Object.fromEntries(
            provider.models.map((_m, idx) => [idx, { status: "success" as const }]),
          )
        : {},
    );
    setDialogOpen(true);
  };

  // Match onboard's handleProviderChange: pre-fill api base + api type from
  // the preset, keep provider name editable (auto-set to preset key, but
  // user can rename — e.g. "openai" → "production"), reset test status.
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

  // Test every configured model in turn. We hit one /chat/completions
  // (or /v1/messages) per model id so a typo in any single model surfaces
  // distinctly — a single "ping" with model="" can return 200 and still
  // leave a broken model id silently undetected.
  //
  // When editing an existing row, the user typically hasn't re-typed the
  // API key (the field is empty + masked-only displays), so we route the
  // test through the saved provider row server-side. If they DID type a
  // fresh key, we honor it and use the inline-test path so the new key
  // gets exercised before they save.
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
      // Editing the model id invalidates the previous test result for
      // that row — clear the badge so a stale "connected" doesn't
      // mislead after a typo.
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
    // Reindex modelTests: rows after the removed index shift down by 1.
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

  const flashSaved = () => {
    setSaved(true);
    setTimeout(() => setSaved(false), 2000);
  };

  const handleSaveProvider = async () => {
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
          // Empty key on edit means "keep existing"; the backend treats
          // empty/masked-sentinel values as a no-op.
          apiKey: formApiKey || undefined,
          apiType: formApiType,
          authType: formAuthType,
          models: cleanedModels,
        });
      } else {
        await createProvider({
          scope: writeScope,
          scopeId: writeScopeId,
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
    await fetchConfig(isSuperAdmin, me?.id || "");
  };

  const handleDeleteProvider = async (row: ProviderEntry) => {
    setSaving(true);
    try {
      await deleteProvider(row.id);
      flashSaved();
    } finally {
      setSaving(false);
    }
    await fetchConfig(isSuperAdmin, me?.id || "");
  };

  // Save button at the top persists the default-model setting. An empty
  // value is a legitimate intent ("clear the default") — the backend's
  // `omitempty` on AgentDefaults.Model drops the key from the saved row
  // without disturbing sibling fields, so it's safe to send through.
  const handleSaveAll = async () => {
    setSaving(true);
    try {
      await updateConfig(
        { agents: { defaults: { model: model.trim() } } },
        isSystemScope ? "system" : "user",
      );
      flashSaved();
      await fetchConfig(isSuperAdmin, me?.id || "");
    } finally {
      setSaving(false);
    }
  };

  const handleDefaultModelChange = async (value: string) => {
    setModel(value);
    if (!value.trim()) return;
    setSaving(true);
    try {
      await updateConfig(
        { agents: { defaults: { model: value.trim() } } },
        isSystemScope ? "system" : "user",
      );
      flashSaved();
      // Refresh so Inheriting/Override badge reflects the new state.
      await fetchConfig(isSuperAdmin, me?.id || "");
    } finally {
      setSaving(false);
    }
  };

  // Clear the user-scope agents.defaults.model override so the agent
  // runtime falls back to the system default. Writing an empty string
  // just stores "" at user scope which still wins the merge — instead
  // we send a null/undefined which the backend treats as "delete row".
  const handleClearOverride = async () => {
    setSaving(true);
    try {
      await updateConfig(
        { agents: { defaults: { model: "" } } },
        isSystemScope ? "system" : "user",
      );
      flashSaved();
      await fetchConfig(isSuperAdmin, me?.id || "");
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div className="p-6 space-y-6 max-w-5xl mx-auto">
        <Skeleton className="h-10 w-48" />
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {[1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-48" />
          ))}
        </div>
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">{tr("Models", "模型")}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {inAgentContext ? (
              <>
                {tr("Your model and providers for", "你为")} {" "}
                <strong>{agentName || tr("this agent", "此 Agent")}</strong>
                {tr(". Your override takes priority over the agent default.", " 配置的模型和服务商。你的配置优先于 Agent 默认值。")}
              </>
            ) : (
              <>{tr("Manage LLM providers and the default model", "管理大模型服务商和默认模型")}</>
            )}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={openAddDialog}>
            <Plus className="h-4 w-4 mr-2" />
            {tr("Add provider", "添加服务商")}
          </Button>
          <Button
            onClick={handleSaveAll}
            disabled={saving}
            variant={saved ? "outline" : "default"}
            className={saved ? "border-emerald-500/30 text-emerald-600 dark:text-emerald-400" : ""}
          >
            {saved ? (
              <>
                <Check className="h-4 w-4 mr-2" />
                {tr("Saved", "已保存")}
              </>
            ) : (
              saving ? tr("Saving…", "正在保存…") : tr("Save", "保存")
            )}
          </Button>
        </div>
      </div>

      {/* Default Model — for non-admin we surface inheritance state the
          same way the agent Models page does, so users can see what
          they'd get for free vs what they've overridden. Super_admin is
          the system source of truth, so the badges are noise for them.
          In agent context the inheritance chain becomes
          chatter-user → agent-scope → system, so we show the agent's
          model in the placeholder and caption when sharing is on. */}
      {(() => {
        const inheriting = !isSystemScope && !model.trim();
        const overridden = !isSystemScope && !inheriting;
        // What the runtime will actually use when the chatter has no
        // override. EnsureAgent picks agent-scope first (only when the
        // owner enabled sharing); otherwise it falls through to system.
        const effectiveFallback = inAgentContext && agentShares && agentScopeModel
          ? agentScopeModel
          : systemDefault;
        const fallbackSource = inAgentContext && agentShares && agentScopeModel
          ? "agent"
          : "system";
        return (
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-center justify-between gap-2 mb-3">
          <div className="flex items-center gap-2">
            <Cpu className="h-4 w-4 text-primary" />
            <h3 className="font-medium">
              {inAgentContext ? tr("Active model", "当前模型") : tr("Default model", "默认模型")}
            </h3>
            {!isSystemScope && (inheriting ? (
              <Badge variant="outline" className="text-[10px]">{tr("Inheriting", "继承中")}</Badge>
            ) : (
              <Badge className="bg-primary/10 text-primary hover:bg-primary/10 text-[10px]">{tr("Override", "已覆盖")}</Badge>
            ))}
          </div>
          {overridden && (
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
          <Select value={inheriting ? "" : model} onValueChange={(v: string | null) => v && handleDefaultModelChange(v)}>
            <SelectTrigger className="font-mono text-sm max-w-md">
              <SelectValue placeholder={inheriting ? tr("Inherit ({{model}})", "继承（{{model}}）", { model: effectiveFallback || tr("no default", "无默认值") }) : tr("Select a model", "选择模型")} />
            </SelectTrigger>
            {/* Default `w-(--anchor-width)` locks the popup to the
                trigger's max-w-md. Long ids like
                openrouter/xiaomi/mimo-v2-flash get clipped. Let it grow
                to fit content instead, while still staying at least as
                wide as the trigger. */}
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
            value={inheriting ? "" : model}
            onChange={(e) => setModel(e.target.value)}
            placeholder={inheriting && effectiveFallback ? tr("Inherit ({{model}})", "继承（{{model}}）", { model: effectiveFallback }) : tr("e.g. openai/gpt-4o", "例如 openai/gpt-4o")}
            className="font-mono text-sm max-w-md"
          />
        )}
        <p className="text-xs text-muted-foreground mt-2">
          {isSystemScope ? (
            <>{tr("Used by agents unless overridden in agent configuration.", "供 Agent 默认使用，除非在 Agent 配置中覆盖。")}</>
          ) : inheriting ? (
            <>
              {fallbackSource === "agent" ? (
                <>
                  <strong>{agentName || tr("This agent", "此 Agent")}</strong>
                  {tr(" uses ", "使用 ")}<code className="text-[11px]">{effectiveFallback}</code>
                  {tr(" inherited from the agent. Pick a model above to override it just for you.", "（继承自 Agent）。选择上方模型可仅为自己覆盖该配置。")}
                </>
              ) : (
                <>
                  {tr("Using system default", "正在使用系统默认值")}
                  {effectiveFallback ? (
                    <>: <code className="text-[11px]">{effectiveFallback}</code></>
                  ) : (
                    <> {tr("(none configured)", "（尚未配置）")}</>
                  )}
                  {tr(". Pick a model above to override it", "。选择上方模型可覆盖该配置")}
                  {inAgentContext ? <>{tr(" for this agent.", "，仅用于此 Agent。")}</> : <>{tr(" for your agents only.", "，仅用于你的 Agent。")}</>}
                </>
              )}
            </>
          ) : (
            <>
              {tr("Override applies to", "覆盖配置适用于")}{" "}{inAgentContext ? <strong>{tr("you", "你")}</strong> : <>{tr("your agents", "你的 Agent")}</>}.
              {tr(" Format: ", " 格式：")}<code className="text-[11px]">provider/modelId</code>.
            </>
          )}
        </p>
      </div>
        );
      })()}

      {/* Providers Grid */}
      {providers.length === 0 ? (
        <div className="rounded-lg border border-border bg-card">
          <div className="flex flex-col items-center justify-center py-16">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-amber-500/10 mb-4">
              <Brain className="h-7 w-7 text-amber-500" />
            </div>
            <p className="text-sm text-muted-foreground mb-1">{tr("No providers configured", "尚未配置服务商")}</p>
            <p className="text-xs text-muted-foreground/60 mb-4">
              {tr("Add an LLM provider to get started", "添加大模型服务商以开始使用")}
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
                // A row is editable if it's at the caller's own scope.
                // For super_admin that's "system"; for everyone else
                // it's "user". "agent" rows surfaced for a chatter on
                // a shared agent are always read-only (owner owns them).
                const editable = isSystemScope
                  ? provider.scope === "system"
                  : provider.scope === "user";
                const sourceLabel =
                  provider.scope === "agent"
                    ? tr("Inherited from agent", "继承自 Agent")
                    : editable
                      ? tr("Mine", "我的配置")
                      : tr("Inherited", "已继承");
                const sourceTitle =
                  provider.scope === "agent"
                    ? tr("Configured by the agent owner and shared with chatters.", "由 Agent 所有者配置并共享给聊天用户。")
                    : editable
                      ? ""
                      : tr("Configured by an admin and shared with all users.", "由管理员配置并共享给所有用户。");
                return (
                <TableRow key={`${provider.scope}:${provider.id}`}>
                  <TableCell className="font-medium">{provider.name}</TableCell>
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
                      <Badge variant="outline" className="text-muted-foreground" title={sourceTitle}>
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
              {tr("Configure the LLM provider connection and models", "配置大模型服务商连接和模型")}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            {/* Provider + Provider Name (mirrors onboard's 2-col grid). */}
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

            {/* API Base URL */}
            <div className="space-y-1.5">
              <Label>{tr("API base URL", "API 基础地址")}</Label>
              <Input
                value={formApiBase}
                onChange={(e) => setFormApiBase(e.target.value)}
                placeholder="https://api.openai.com/v1"
                className="font-mono text-sm"
              />
            </div>

            {/* API Key — on edit we never receive the unmasked key from
                the server, so show the masked key as the placeholder so
                the operator can see one is configured. Leave the value
                empty so they can type a replacement; Test connection
                falls back to the stored key when the field is blank. */}
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

            {/* API Type & Auth Type */}
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

            {/* Models Section */}
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

              {/* Test connection runs against every configured model so
                  a typo in any single model id is surfaced per-row, not
                  hidden behind a single green pass/fail. Always visible
                  — Add/Update is gated on every model passing. */}
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
