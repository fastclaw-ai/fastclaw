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
  evolink: { apiBase: "https://direct.evolink.ai/v1", apiType: "openai-chat", authType: "bearer-token", models: ["gpt-5.2"] },
  openrouter: { apiBase: "https://openrouter.ai/api/v1", apiType: "openai-chat", authType: "bearer-token", models: [] },
  anthropic: { apiBase: "https://api.anthropic.com", apiType: "anthropic-messages", authType: "api-key", models: ["claude-opus-4-7", "claude-sonnet-4-7", "claude-haiku-4-5"] },
  deepseek: { apiBase: "https://api.deepseek.com", apiType: "openai-chat", authType: "bearer-token", models: ["deepseek-v4-pro", "deepseek-v4-flash"] },
  ollama: { apiBase: "http://localhost:11434/v1", apiType: "openai-chat", authType: "bearer-token", models: [] },
  custom: { apiBase: "", apiType: "openai-chat", authType: "bearer-token", models: [] },
};

const PROVIDER_LABELS: Record<string, string> = {
  openai: "OpenAI",
  evolink: "EvoLink",
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
              : { status: "error", error: result.error || "Connection failed" },
          }));
        } catch {
          setModelTests((prev) => ({
            ...prev,
            [idx]: { status: "error", error: "Connection failed" },
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
          <h2 className="text-2xl font-semibold tracking-tight">Models</h2>
          <p className="text-sm text-muted-foreground mt-1">
            LLM providers and active model scoped to{" "}
            <strong>{agentName || "this agent"}</strong>. Agent-scope settings
            override the system default.
          </p>
        </div>
        <div className="flex items-center gap-2">
          {saved && (
            <span className="inline-flex items-center gap-1.5 text-xs text-emerald-600 dark:text-emerald-400 mr-2">
              <Check className="h-3.5 w-3.5" /> Saved
            </span>
          )}
          <Button variant="outline" onClick={openAddDialog} disabled={saving}>
            <Plus className="h-4 w-4 mr-2" />
            Add Provider
          </Button>
        </div>
      </div>

      {/* Share with chatters */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-start gap-3 min-w-0">
            <Share2 className="h-4 w-4 text-primary mt-0.5 shrink-0" />
            <div className="min-w-0">
              <h3 className="font-medium">Share model config with chatters</h3>
              <p className="text-sm text-muted-foreground mt-1">
                {shareModelConfig ? (
                  <>
                    Chatters using <strong>{agentName || "this agent"}</strong>{" "}
                    inherit your model and provider credentials. Your tokens
                    are spent on their messages.
                  </>
                ) : (
                  <>
                    Only you use this configuration. Chatters bring their own
                    model + providers under <em>User → Models</em>, otherwise
                    the agent falls back to the system default.
                  </>
                )}
              </p>
            </div>
          </div>
          <Switch
            checked={shareModelConfig}
            onCheckedChange={handleShareToggle}
            disabled={saving}
            aria-label="Share model config with chatters"
          />
        </div>
      </div>

      {/* Active Model */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-center justify-between gap-2 mb-3">
          <div className="flex items-center gap-2">
            <Cpu className="h-4 w-4 text-primary" />
            <h3 className="font-medium">Active Model</h3>
            {inheriting ? (
              <Badge variant="outline" className="text-[10px]">
                Inheriting
              </Badge>
            ) : (
              <Badge className="bg-primary/10 text-primary hover:bg-primary/10 text-[10px]">
                Override
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
              Clear override
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
              <SelectValue placeholder={inheriting ? `Inherit (${systemDefault || "no system default"})` : "Select a model"} />
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
            placeholder={systemDefault ? `Inherit (${systemDefault})` : "Add a provider with models below"}
            className="font-mono text-sm max-w-md"
          />
        )}
        <p className="text-xs text-muted-foreground mt-2">
          {inheriting ? (
            <>
              Using system default
              {systemDefault ? (
                <>
                  : <code className="text-[11px]">{systemDefault}</code>
                </>
              ) : (
                <> (none configured)</>
              )}
              . Pick a model above to override for{" "}
              <strong>{agentName || "this agent"}</strong> only.
            </>
          ) : (
            <>
              Override applies to <strong>{agentName || "this agent"}</strong>{" "}
              only. Format <code className="text-[11px]">provider/modelId</code>.
              {systemDefault && (
                <>
                  {" "}
                  Clearing falls back to{" "}
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
            <p className="text-sm text-muted-foreground mb-1">
              No providers available
            </p>
            <p className="text-xs text-muted-foreground/60 mb-4 max-w-md text-center">
              No agent / user / system providers are configured. Add one here to
              give this agent credentials, or configure shared ones from the
              top-level Models page.
            </p>
            <Button variant="outline" size="sm" onClick={openAddDialog}>
              <Plus className="h-4 w-4 mr-2" />
              Add Provider
            </Button>
          </div>
        </div>
      ) : (
        <div className="rounded-lg border border-border bg-card overflow-hidden">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>API Base</TableHead>
                <TableHead>API Key</TableHead>
                <TableHead>Models</TableHead>
                <TableHead>Source</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {providers.map((provider) => {
                const editable = provider.scope === "agent";
                const sourceLabel =
                  provider.scope === "agent"
                    ? "Mine (agent)"
                    : provider.scope === "user"
                    ? "Inherited from owner"
                    : "Inherited from admin";
                return (
                <TableRow key={`${provider.scope}:${provider.id}`}>
                  <TableCell className="font-medium">
                    <div className="flex items-center gap-2">
                      {provider.name}
                      {editable && systemProviders.includes(provider.name) && (
                        <Badge variant="outline" className="text-[10px]">
                          shadows system
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
                      <Badge variant="outline" className="text-muted-foreground" title="Read-only — owner / admin owns this row">
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
                        title={editable ? "Edit" : "Read-only — inherited row"}
                        disabled={!editable}
                      >
                        <Pencil className="size-4" />
                      </Button>
                      <Button
                        size="icon"
                        variant="ghost"
                        className="text-destructive hover:text-destructive"
                        onClick={() => handleDeleteProvider(provider)}
                        title={editable ? "Remove" : "Read-only — inherited row"}
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
              {editingName ? "Edit Provider" : "Add Provider"}
            </DialogTitle>
            <DialogDescription>
              Configure an LLM provider scoped to{" "}
              <strong>{agentName || "this agent"}</strong>. Use the same name as
              a system provider to shadow it.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label>Provider</Label>
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
                        {PROVIDER_LABELS[p] ?? p}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5">
                <Label>Provider Name</Label>
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
              <Label>API Base URL</Label>
              <Input
                value={formApiBase}
                onChange={(e) => setFormApiBase(e.target.value)}
                placeholder="https://api.openai.com/v1"
                className="font-mono text-sm"
              />
            </div>

            <div className="space-y-1.5">
              <Label>API Key</Label>
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
                  Leave empty to keep existing key. Test connection uses the saved key.
                </p>
              )}
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label>API Type</Label>
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
                <Label>Auth Type</Label>
                <Select value={formAuthType} onValueChange={(v: string | null) => v && setFormAuthType(v)}>
                  <SelectTrigger className="w-full">
                    <SelectValue>
                      {(v: unknown) => AUTH_TYPE_LABELS[v as string] ?? (v as string) ?? ""}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="bearer-token">Bearer Token</SelectItem>
                    <SelectItem value="api-key">API Key Header</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>

            <div className="space-y-3 pt-2 border-t border-border">
              <div className="flex items-center justify-between">
                <Label className="text-base">Models</Label>
                <Button variant="outline" size="sm" onClick={handleAddModel}>
                  <Plus className="h-3 w-3 mr-1.5" />
                  Add Model
                </Button>
              </div>

              {formModels.length === 0 && (
                <p className="text-sm text-muted-foreground/60 text-center py-4">
                  No models configured. Add models to use with this provider.
                </p>
              )}

              {formModels.map((m, idx) => {
                const t = modelTests[idx];
                return (
                <div key={idx} className="rounded-lg border border-border bg-muted/30 p-4 space-y-3">
                  <div className="flex items-center justify-between gap-2">
                    <div className="flex items-center gap-2 min-w-0">
                      <span className="text-sm font-medium text-muted-foreground">
                        Model {idx + 1}
                      </span>
                      {t?.status === "testing" && (
                        <Badge variant="outline" className="text-[10px]">
                          <Loader2 className="mr-1 size-3 animate-spin" /> testing
                        </Badge>
                      )}
                      {t?.status === "success" && (
                        <Badge className="bg-emerald-500/15 text-emerald-700 hover:bg-emerald-500/15 text-[10px]">
                          <Check className="mr-1 size-3" /> connected
                        </Badge>
                      )}
                      {t?.status === "error" && (
                        <Badge variant="outline" className="border-destructive/40 text-destructive text-[10px]" title={t.error}>
                          failed
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
                      Remove
                    </Button>
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div className="space-y-1">
                      <Label className="text-xs">Model ID</Label>
                      <Input
                        value={m.id}
                        onChange={(e) => handleUpdateModel(idx, "id", e.target.value)}
                        placeholder="e.g. gpt-4o"
                        className="font-mono text-xs h-8"
                      />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs">Display Name</Label>
                      <Input
                        value={m.name}
                        onChange={(e) => handleUpdateModel(idx, "name", e.target.value)}
                        placeholder="e.g. GPT-4o"
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
                        <Loader2 className="mr-1 size-4 animate-spin" /> Testing
                      </>
                    ) : (
                      "Test connection"
                    )}
                  </Button>
                  <span className="text-xs text-muted-foreground">
                    {cleanModelRows.length === 0
                      ? "Add at least one model with an id, then test."
                      : "Pings every model above; results show next to each row."}
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
                Test every model first — Add/Update unlocks once they all pass.
              </span>
            )}
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={handleSaveProvider}
              disabled={!formName.trim() || saving || !allModelsPassed}
            >
              {editingName ? "Update" : "Add"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
