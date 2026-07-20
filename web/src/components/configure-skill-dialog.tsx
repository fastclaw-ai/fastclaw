"use client";

import { useEffect, useState } from "react";
import { useTranslations } from "next-intl";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { X, Plus } from "lucide-react";
import {
  updateSkillEntries,
  type SkillInfo,
  type SkillEnvSpec,
} from "@/lib/api";
import { useAgentName } from "@/hooks/use-agent-name";

// SkillEntryView is what the masked GET /api/config response carries
// for a single skill entry. apiKey + env values come back as "***" so
// the UI shows that something is configured without leaking the secret.
export interface SkillEntryView {
  enabled?: boolean;
  apiKey?: string;
  env?: Record<string, string>;
}

export function looksLikeSecret(name: string): boolean {
  const upper = name.toUpperCase();
  return ["KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL"].some((m) =>
    upper.includes(m),
  );
}

// ConfigureSkillDialog renders one input per declared env var (from the
// skill's SKILL.md frontmatter envSpec) plus an escape hatch for adding
// arbitrary vars the skill author didn't declare. Saving POSTs through
// updateSkillEntries; when `agentId` is supplied the patch lands in the
// per-agent override map (cfg.Skills.AgentEntries[agentId][skillName]),
// otherwise in the global map (cfg.Skills.Entries[skillName]). The
// runtime resolves agent-scoped first and falls back to global.
export function ConfigureSkillDialog({
  skill,
  existing,
  agentId,
  onClose,
  onSaved,
}: {
  skill: SkillInfo | null;
  existing?: SkillEntryView;
  agentId?: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const t = useTranslations("skillConfig");
  const tc = useTranslations("common");
  const [env, setEnv] = useState<Record<string, string>>({});
  const [customRows, setCustomRows] = useState<{ name: string; value: string }[]>([]);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const agentName = useAgentName(agentId || "");

  const declaredSpec: SkillEnvSpec[] = skill?.envSpec || [];
  const declaredNames = new Set(declaredSpec.map((s) => s.name));

  useEffect(() => {
    if (!skill) return;
    const initialEnv: Record<string, string> = {};
    for (const spec of declaredSpec) {
      initialEnv[spec.name] = existing?.env?.[spec.name] || "";
    }
    setEnv(initialEnv);
    const customs: { name: string; value: string }[] = [];
    if (existing?.env) {
      for (const [k, v] of Object.entries(existing.env)) {
        if (!declaredNames.has(k)) customs.push({ name: k, value: v });
      }
    }
    setCustomRows(customs);
    setError(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [skill]);

  if (!skill) return null;

  const updateEnv = (name: string, value: string) => {
    setEnv((prev) => ({ ...prev, [name]: value }));
  };

  const addCustomRow = () => setCustomRows((prev) => [...prev, { name: "", value: "" }]);
  const updateCustomRow = (idx: number, patch: Partial<{ name: string; value: string }>) =>
    setCustomRows((prev) => prev.map((r, i) => (i === idx ? { ...r, ...patch } : r)));
  const removeCustomRow = (idx: number) =>
    setCustomRows((prev) => prev.filter((_, i) => i !== idx));

  const handleSave = async () => {
    setSaving(true);
    setError(null);
    const merged: Record<string, string> = { ...env };
    for (const row of customRows) {
      if (!row.name.trim()) continue;
      merged[row.name.trim()] = row.value;
    }
    try {
      const resp = await updateSkillEntries(
        { [skill.name]: { enabled: true, env: merged } },
        agentId,
      );
      if (resp && resp.ok === false) {
        setError(resp.error || t("errorSaveFailed"));
        setSaving(false);
        return;
      }
      onSaved();
    } catch (e) {
      setError(e instanceof Error ? e.message : t("errorSaveFailed"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={!!skill} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("title", { name: skill.name })}</DialogTitle>
          <DialogDescription>
            {agentId
              ? t.rich("descriptionAgent", {
                  name: agentName,
                  strong: (chunks) => <strong>{chunks}</strong>,
                })
              : t("descriptionGlobal")}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          {declaredSpec.length === 0 && customRows.length === 0 && (
            <p className="text-sm text-muted-foreground/70">
              {t("noEnvDeclared")}
            </p>
          )}

          {declaredSpec.map((spec) => {
            const isSecret = spec.secret ?? looksLikeSecret(spec.name);
            const placeholder =
              isSecret && existing?.env?.[spec.name]?.includes("****")
                ? existing.env[spec.name]
                : isSecret
                ? t("notSetPlaceholder")
                : "";
            return (
              <div key={spec.name} className="space-y-1.5">
                <Label className="font-mono text-xs flex items-center gap-2">
                  {spec.name}
                  {spec.required && (
                    <span className="text-[9px] uppercase tracking-wider text-amber-500">
                      {t("requiredBadge")}
                    </span>
                  )}
                  {!spec.required && (
                    <span className="text-[9px] uppercase tracking-wider text-muted-foreground/60">
                      {t("optionalBadge")}
                    </span>
                  )}
                </Label>
                <Input
                  type={isSecret ? "password" : "text"}
                  value={env[spec.name] || ""}
                  placeholder={placeholder}
                  onChange={(e) => updateEnv(spec.name, e.target.value)}
                  className="font-mono text-xs"
                />
                {spec.description && (
                  <p className="text-[11px] text-muted-foreground/70">
                    {spec.description}
                  </p>
                )}
              </div>
            );
          })}

          {customRows.length > 0 && (
            <div className="space-y-2 pt-2 border-t border-border/60">
              <Label className="text-xs uppercase tracking-wider text-muted-foreground/70">
                {t("customEnvVars")}
              </Label>
              {customRows.map((row, idx) => (
                <div key={idx} className="flex items-center gap-2">
                  <Input
                    placeholder={t("varNamePlaceholder")}
                    value={row.name}
                    onChange={(e) => updateCustomRow(idx, { name: e.target.value })}
                    className="font-mono text-xs flex-1"
                  />
                  <Input
                    type={looksLikeSecret(row.name) ? "password" : "text"}
                    placeholder={t("valuePlaceholder")}
                    value={row.value}
                    onChange={(e) => updateCustomRow(idx, { value: e.target.value })}
                    className="font-mono text-xs flex-1"
                  />
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8"
                    onClick={() => removeCustomRow(idx)}
                  >
                    <X className="h-3.5 w-3.5" />
                  </Button>
                </div>
              ))}
            </div>
          )}

          <Button
            variant="ghost"
            size="sm"
            className="text-xs"
            onClick={addCustomRow}
          >
            <Plus className="h-3 w-3 mr-1.5" />
            {t("addCustomEnvVar")}
          </Button>

          {error && <p className="text-xs text-destructive">{error}</p>}
        </div>

        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={onClose}>
            {tc("cancel")}
          </Button>
          <Button onClick={handleSave} disabled={saving}>
            {saving ? tc("saving") : tc("save")}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
