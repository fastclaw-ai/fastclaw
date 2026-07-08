"use client";

import { useEffect, useState, useCallback } from "react";
import { useTranslations } from "next-intl";
import {
  listProviders,
  createProvider,
  updateProvider,
  deleteProvider,
  type ProviderRow,
  type ScopeName,
} from "@/lib/api";
import { ScopePicker } from "@/components/scope-picker";

export default function ProvidersPage() {
  const t = useTranslations("providers");
  const tc = useTranslations("common");
  const [scope, setScope] = useState<ScopeName>("system");
  const [scopeId, setScopeId] = useState<string>("");
  const [rows, setRows] = useState<ProviderRow[]>([]);
  const [error, setError] = useState("");
  const [draft, setDraft] = useState({
    name: "",
    apiBase: "",
    apiKey: "",
    apiType: "openai-chat",
    authType: "bearer-token",
  });

  const refresh = useCallback(async () => {
    setError("");
    const r = await listProviders(scope, scopeId);
    if (r.providers) setRows(r.providers);
    if (r.error) setError(r.error);
  }, [scope, scopeId]);

  useEffect(() => {
    if (scope === "system" || scopeId) refresh();
  }, [scope, scopeId, refresh]);

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (!draft.name) return;
    const res = await createProvider({ scope, scopeId, ...draft });
    if (res.error) {
      setError(res.error);
      return;
    }
    setDraft({ name: "", apiBase: "", apiKey: "", apiType: "openai-chat", authType: "bearer-token" });
    refresh();
  }

  async function handleEdit(row: ProviderRow, patch: Partial<ProviderRow>) {
    const res = await updateProvider(row.id, patch);
    if (res.error) setError(res.error);
    refresh();
  }

  async function handleDelete(row: ProviderRow) {
    if (!confirm(t("deleteConfirm", { name: row.name, scope: `${row.scope}/${row.scopeId || t("globalScope")}` }))) return;
    const res = await deleteProvider(row.id);
    if (res.error) setError(res.error);
    refresh();
  }

  return (
    <div className="p-8 text-zinc-100">
      <h1 className="mb-2 text-2xl font-bold">{t("title")}</h1>
      <p className="mb-6 text-sm text-zinc-500">
        {t("description")}
      </p>

      <div className="mb-6">
        <ScopePicker scope={scope} scopeId={scopeId} onChange={(s, id) => { setScope(s); setScopeId(id); }} />
      </div>

      <form onSubmit={handleCreate} className="mb-6 space-y-3 rounded-lg border border-zinc-800 bg-zinc-900 p-4">
        <h2 className="font-semibold">{t("addProvider")}</h2>
        <div className="grid grid-cols-2 gap-3">
          <input value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} placeholder={t("namePlaceholder")} className="rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm" />
          <input value={draft.apiBase} onChange={(e) => setDraft({ ...draft, apiBase: e.target.value })} placeholder={t("apiBasePlaceholder")} className="rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm" />
        </div>
        <input type="password" value={draft.apiKey} onChange={(e) => setDraft({ ...draft, apiKey: e.target.value })} placeholder={t("apiKeyPlaceholder")} className="w-full rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm" />
        <div className="grid grid-cols-2 gap-3">
          <select value={draft.apiType} onChange={(e) => setDraft({ ...draft, apiType: e.target.value })} className="rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm">
            <option value="openai-chat">openai-chat</option>
            <option value="anthropic-messages">anthropic-messages</option>
          </select>
          <select value={draft.authType} onChange={(e) => setDraft({ ...draft, authType: e.target.value })} className="rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm">
            <option value="bearer-token">{t("authBearerToken")}</option>
            <option value="api-key">{t("authApiKeyHeader")}</option>
          </select>
        </div>
        <button type="submit" className="rounded bg-violet-600 px-4 py-2 text-sm">{tc("save")}</button>
      </form>

      {error && <p className="mb-4 text-sm text-red-400">{error}</p>}

      <table className="w-full text-sm">
        <thead className="text-left text-zinc-400">
          <tr>
            <th className="py-2">{t("colName")}</th>
            <th>{t("colApiBase")}</th>
            <th>{t("colKey")}</th>
            <th>{t("colType")}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.id} className="border-t border-zinc-800">
              <td className="py-3 font-medium">{row.name}</td>
              <td>
                <input
                  defaultValue={row.apiBase}
                  onBlur={(e) => e.target.value !== row.apiBase && handleEdit(row, { apiBase: e.target.value })}
                  className="w-full rounded border border-zinc-700 bg-zinc-950 px-2 py-1 text-xs"
                />
              </td>
              <td>
                <input
                  type="password"
                  placeholder={row.apiKey || "****"}
                  onBlur={(e) => e.target.value && handleEdit(row, { apiKey: e.target.value })}
                  className="w-full rounded border border-zinc-700 bg-zinc-950 px-2 py-1 text-xs"
                />
              </td>
              <td className="text-xs text-zinc-500">{row.apiType}</td>
              <td className="text-right">
                <button onClick={() => handleDelete(row)} className="text-xs text-red-400 hover:underline">{t("deleteAction")}</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
