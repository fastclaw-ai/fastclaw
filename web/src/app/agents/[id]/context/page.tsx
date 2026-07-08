"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslations } from "next-intl";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Brain, Check, Link2, MessageSquare, MessagesSquare, Puzzle } from "lucide-react";
import { getAgent, updateAgent } from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";

// Per-agent Context page — one knob (mode), one extension point (plugins).
//
// "Context" rather than "Tools" because the page is really about how
// the LLM's context window gets assembled: which framework sections
// participate in the system prompt AND which built-in tools come
// along. Prompt Mode picks both in one go. There's no per-agent
// allowlist anymore — what each mode includes is documented inline
// next to the dropdown; for the live tool list at runtime, look at
// the agent's chat session (tool calls in the transcript) or the
// /api/agents/{id}/tools/registered endpoint.

type PromptModeValue = "" | "agent" | "chatbot" | "customize";

export default function AgentContextPage() {
  const t = useTranslations("context");
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);

  const modeLabel: Record<string, string> = useMemo(
    () => ({
      agent: t("modeAgent"),
      chatbot: t("modeChatbot"),
      customize: t("modeCustomize"),
    }),
    [t],
  );

  // "" = no override saved; runtime falls back to "agent".
  const [promptMode, setPromptMode] = useState<PromptModeValue>("");
  // Per-agent multi-bubble toggle. Applies to every IM channel the
  // agent is bound to. False is the default; null on the wire is
  // treated as false here.
  const [splitReplies, setSplitReplies] = useState(false);
  const [splitRepliesSaving, setSplitRepliesSaving] = useState(false);
  // Per-agent auto-persist toggle. Off by default; null on the wire is
  // treated as false here. When on, every N turns the runtime fires an
  // LLM-driven distill pass that appends to USER.md / MEMORY.md.
  const [autoPersist, setAutoPersist] = useState(false);
  const [autoPersistSaving, setAutoPersistSaving] = useState(false);
  const [sharedIdentity, setSharedIdentity] = useState(false);
  const [sharedIdentitySaving, setSharedIdentitySaving] = useState(false);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  const fetchAll = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    try {
      const agentRec = await getAgent(agentId).catch(() => null);
      const pm = agentRec?.promptMode || "";
      if (pm === "agent" || pm === "chatbot" || pm === "customize") {
        setPromptMode(pm);
      } else {
        setPromptMode("");
      }
      setSplitReplies(agentRec?.splitReplies === true);
      setAutoPersist(agentRec?.autoPersist === true);
      setSharedIdentity(agentRec?.sharedIdentity === true);
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

  const handlePromptModeChange = async (next: PromptModeValue) => {
    const prev = promptMode;
    setPromptMode(next);
    setSaving(true);
    try {
      await updateAgent(agentId, { promptMode: next });
      flashSaved();
    } catch {
      setPromptMode(prev);
    } finally {
      setSaving(false);
    }
  };

  // Optimistic toggle for splitReplies. No "inherit" state anymore —
  // system-level fallback was removed; false is the absolute default
  // when nothing is saved.
  const handleSplitRepliesChange = async (next: boolean) => {
    const prev = splitReplies;
    setSplitReplies(next);
    setSplitRepliesSaving(true);
    try {
      await updateAgent(agentId, { splitReplies: next });
      flashSaved();
    } catch {
      setSplitReplies(prev);
    } finally {
      setSplitRepliesSaving(false);
    }
  };

  // Optimistic toggle for autoPersist. Same shape as splitReplies; on
  // failure roll back. The runtime falls back to system default (off
  // in practice today, since the dead-code NewAgentWithFullCfg path
  // never gets called) when no per-agent override is saved.
  const handleAutoPersistChange = async (next: boolean) => {
    const prev = autoPersist;
    setAutoPersist(next);
    setAutoPersistSaving(true);
    try {
      await updateAgent(agentId, { autoPersist: next });
      flashSaved();
    } catch {
      setAutoPersist(prev);
    } finally {
      setAutoPersistSaving(false);
    }
  };

  const handleSharedIdentityChange = async (next: boolean) => {
    const prev = sharedIdentity;
    setSharedIdentity(next);
    setSharedIdentitySaving(true);
    try {
      await updateAgent(agentId, { sharedIdentity: next });
      flashSaved();
    } catch {
      setSharedIdentity(prev);
    } finally {
      setSharedIdentitySaving(false);
    }
  };

  if (loading) {
    return (
      <div className="p-6 space-y-6 max-w-5xl mx-auto">
        <Skeleton className="h-10 w-48" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">{t("title")}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {t.rich("subtitle", {
              name: agentName || t("thisAgent"),
              strong: (chunks) => <strong>{chunks}</strong>,
            })}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {saved && (
            <span className="inline-flex items-center gap-1.5 text-xs text-emerald-600 dark:text-emerald-400">
              <Check className="h-3.5 w-3.5" /> {t("saved")}
            </span>
          )}
        </div>
      </div>

      {/* Prompt Mode */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-center justify-between gap-2 mb-3">
          <div className="flex items-center gap-2">
            <MessageSquare className="h-4 w-4 text-primary" />
            <h3 className="font-medium">{t("promptModeTitle")}</h3>
            {promptMode === "" || promptMode === "agent" ? (
              <Badge variant="outline" className="text-[10px]">
                {t("badgeDefault")}
              </Badge>
            ) : (
              <Badge className="bg-primary/10 text-primary hover:bg-primary/10 text-[10px]">
                {modeLabel[promptMode]}
              </Badge>
            )}
          </div>
        </div>
        <Select
          value={promptMode || "agent"}
          onValueChange={(v: string | null) => {
            if (v === "agent" || v === "chatbot" || v === "customize") {
              handlePromptModeChange(v);
            }
          }}
          disabled={saving}
        >
          <SelectTrigger className="text-sm max-w-[240px]">
            {/* Explicit children override SelectValue's auto-extraction
                from the active SelectItem — shadcn sometimes falls back
                to rendering the raw `value` string. */}
            <SelectValue>{modeLabel[promptMode || "agent"]}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="agent">{t("modeAgent")}</SelectItem>
            <SelectItem value="chatbot">{t("modeChatbot")}</SelectItem>
            <SelectItem value="customize">{t("modeCustomize")}</SelectItem>
          </SelectContent>
        </Select>
        <div className="mt-3 text-xs text-muted-foreground space-y-1.5">
          <div>
            {t.rich("modeAgentDesc", {
              strong: (chunks) => <strong>{chunks}</strong>,
            })}
          </div>
          <div>
            {t.rich("modeChatbotDesc", {
              strong: (chunks) => <strong>{chunks}</strong>,
              code: (chunks) => <code className="text-[10px]">{chunks}</code>,
            })}
          </div>
          <div>
            {t.rich("modeCustomizeDesc", {
              strong: (chunks) => <strong>{chunks}</strong>,
            })}
          </div>
        </div>
        <div className="mt-4 pt-3 border-t border-border flex items-start gap-2 text-xs text-muted-foreground">
          <Puzzle className="h-3.5 w-3.5 mt-0.5 shrink-0" />
          <span>
            {t.rich("pluginNote", {
              code: (chunks) => <code className="text-[11px]">{chunks}</code>,
            })}
          </span>
        </div>
      </div>

      {/* Multi-bubble replies — applies to every IM channel. Lives here
          rather than in the Channels tab because it's a property of how
          the LLM communicates, not of the channel binding. */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-start gap-3 min-w-0">
            <MessagesSquare className="h-4 w-4 text-primary mt-0.5 shrink-0" />
            <div className="min-w-0">
              <h3 className="font-medium">{t("multiBubbleTitle")}</h3>
              <p className="text-sm text-muted-foreground mt-1">
                {t("multiBubbleDesc")}
              </p>
            </div>
          </div>
          <Switch
            checked={splitReplies}
            onCheckedChange={handleSplitRepliesChange}
            disabled={splitRepliesSaving}
            aria-label={t("multiBubbleTitle")}
          />
        </div>
      </div>

      {/* Auto-remember chatter — lives here because it's about how the
          agent retains context across turns / sessions, parallel to how
          Multi-bubble is about how it emits replies. */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-start gap-3 min-w-0">
            <Brain className="h-4 w-4 text-primary mt-0.5 shrink-0" />
            <div className="min-w-0">
              <h3 className="font-medium">{t("autoRememberTitle")}</h3>
              <p className="text-sm text-muted-foreground mt-1">
                {t.rich("autoRememberDesc", {
                  code: (chunks) => <code className="text-[10px]">{chunks}</code>,
                })}
              </p>
            </div>
          </div>
          <Switch
            checked={autoPersist}
            onCheckedChange={handleAutoPersistChange}
            disabled={autoPersistSaving}
            aria-label={t("autoRememberTitle")}
          />
        </div>
      </div>

      {/* Shared identity across channels */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-start gap-3 min-w-0">
            <Link2 className="h-4 w-4 text-primary mt-0.5 shrink-0" />
            <div className="min-w-0">
              <h3 className="font-medium">{t("sharedIdentityTitle")}</h3>
              <p className="text-sm text-muted-foreground mt-1">
                {t("sharedIdentityDesc")}
              </p>
            </div>
          </div>
          <Switch
            checked={sharedIdentity}
            onCheckedChange={handleSharedIdentityChange}
            disabled={sharedIdentitySaving}
            aria-label={t("sharedIdentityTitle")}
          />
        </div>
      </div>
    </div>
  );
}
