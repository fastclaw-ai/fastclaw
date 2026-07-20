"use client";

import { useEffect, useState, useCallback } from "react";
import { useTranslations } from "next-intl";
import {
  listScopedChannels,
  createScopedChannel,
  updateScopedChannel,
  deleteScopedChannel,
  type ChannelRow,
  type ScopeName,
} from "@/lib/api";
import { ScopePicker } from "@/components/scope-picker";

const CHANNEL_TYPES = ["telegram", "discord", "slack"];

export default function ChannelsConfigPage() {
  const t = useTranslations("channelsAdmin");
  const tc = useTranslations("common");
  const [scope, setScope] = useState<ScopeName>("system");
  const [scopeId, setScopeId] = useState<string>("");
  const [rows, setRows] = useState<ChannelRow[]>([]);
  const [error, setError] = useState("");
  const [draft, setDraft] = useState({
    type: "telegram",
    enabled: true,
    botToken: "",
    appToken: "",
  });

  const refresh = useCallback(async () => {
    setError("");
    const r = await listScopedChannels(scope, scopeId);
    if (r.channels) setRows(r.channels);
    if (r.error) setError(r.error);
  }, [scope, scopeId]);

  useEffect(() => {
    if (scope === "system" || scopeId) refresh();
  }, [scope, scopeId, refresh]);

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (!draft.botToken && draft.type !== "slack") return;
    const res = await createScopedChannel({ scope, scopeId, ...draft });
    if (res.error) {
      setError(res.error);
      return;
    }
    setDraft({ type: "telegram", enabled: true, botToken: "", appToken: "" });
    refresh();
  }

  async function handleToggle(row: ChannelRow, enabled: boolean) {
    const res = await updateScopedChannel(row.id, { enabled });
    if (res.error) setError(res.error);
    refresh();
  }

  async function handleDelete(row: ChannelRow) {
    const scopePath = `${row.scope}/${row.scopeId || t("scopeGlobal")}`;
    if (!confirm(t("deleteConfirm", { type: row.type, scope: scopePath }))) return;
    const res = await deleteScopedChannel(row.id);
    if (res.error) setError(res.error);
    refresh();
  }

  async function handleRotateToken(row: ChannelRow, token: string) {
    if (!token) return;
    const res = await updateScopedChannel(row.id, { botToken: token });
    if (res.error) setError(res.error);
    refresh();
  }

  return (
    <div className="p-8 text-zinc-100">
      <h1 className="mb-2 text-2xl font-bold">{t("title")}</h1>
      <p className="mb-6 text-sm text-zinc-500">
        {t.rich("adminSubtitle", { code: (chunks) => <code>{chunks}</code> })}
      </p>

      <div className="mb-6">
        <ScopePicker scope={scope} scopeId={scopeId} onChange={(s, id) => { setScope(s); setScopeId(id); }} />
      </div>

      <form onSubmit={handleCreate} className="mb-6 space-y-3 rounded-lg border border-zinc-800 bg-zinc-900 p-4">
        <h2 className="font-semibold">{t("addChannel")}</h2>
        <div className="grid grid-cols-2 gap-3">
          <select value={draft.type} onChange={(e) => setDraft({ ...draft, type: e.target.value })} className="rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm">
            {CHANNEL_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
          </select>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={draft.enabled} onChange={(e) => setDraft({ ...draft, enabled: e.target.checked })} />
            {t("enabledCheckbox")}
          </label>
        </div>
        <input type="password" value={draft.botToken} onChange={(e) => setDraft({ ...draft, botToken: e.target.value })} placeholder={t("botTokenPlaceholder")} className="w-full rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm" />
        {draft.type === "slack" && (
          <input type="password" value={draft.appToken} onChange={(e) => setDraft({ ...draft, appToken: e.target.value })} placeholder={t("appTokenPlaceholder")} className="w-full rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm" />
        )}
        <button type="submit" className="rounded bg-violet-600 px-4 py-2 text-sm">{tc("save")}</button>
      </form>

      {error && <p className="mb-4 text-sm text-red-400">{error}</p>}

      <table className="w-full text-sm">
        <thead className="text-left text-zinc-400">
          <tr>
            <th className="py-2">{t("colType")}</th>
            <th>{t("colBotToken")}</th>
            <th>{t("colEnabled")}</th>
            <th>{t("colCredKey")}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.id} className="border-t border-zinc-800">
              <td className="py-3 font-medium">{row.type}</td>
              <td>
                <input
                  type="password"
                  placeholder={row.botToken || "****"}
                  onBlur={(e) => handleRotateToken(row, e.target.value)}
                  className="w-full rounded border border-zinc-700 bg-zinc-950 px-2 py-1 text-xs"
                />
              </td>
              <td>
                <input type="checkbox" checked={row.enabled} onChange={(e) => handleToggle(row, e.target.checked)} />
              </td>
              <td className="font-mono text-xs text-zinc-500">{row.credentialKey}</td>
              <td className="text-right">
                <button onClick={() => handleDelete(row)} className="text-xs text-red-400 hover:underline">{tc("delete")}</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
