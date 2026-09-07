"use client";

import { useCallback, useEffect, useState } from "react";
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
import { useLocale } from "@/components/locale-provider";

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
  const { tr } = useLocale();
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);
  const modeLabel = (mode: string) =>
    mode === "chatbot"
      ? tr("Chatbot", "聊天机器人")
      : mode === "customize"
        ? tr("Customize", "自定义")
        : "Agent";

  // "" = no override saved; runtime falls back to "agent".
  const [promptMode, setPromptMode] = useState<PromptModeValue>("");
  // Per-agent multi-bubble toggle. Applies to web chat and every IM
  // channel the agent is bound to. False is the default; null on the
  // wire is treated as false here.
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
          <h2 className="text-2xl font-semibold tracking-tight">{tr("Context", "上下文")}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {tr("Control what the LLM sees for", "控制大模型为")} {" "}
            <strong>{agentName || tr("this agent", "此 Agent")}</strong>
            {tr(". Prompt mode selects the framework prompt and built-in tools. Plugin tools remain available in every mode.", " 看到的内容。提示词模式会同时选择框架提示词和内置工具；插件工具在所有模式下都保持可用。")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {saved && (
            <span className="inline-flex items-center gap-1.5 text-xs text-emerald-600 dark:text-emerald-400">
              <Check className="h-3.5 w-3.5" /> {tr("Saved", "已保存")}
            </span>
          )}
        </div>
      </div>

      {/* Prompt Mode */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-center justify-between gap-2 mb-3">
          <div className="flex items-center gap-2">
            <MessageSquare className="h-4 w-4 text-primary" />
            <h3 className="font-medium">{tr("Prompt mode", "提示词模式")}</h3>
            {promptMode === "" || promptMode === "agent" ? (
              <Badge variant="outline" className="text-[10px]">
                {tr("Default", "默认")}
              </Badge>
            ) : (
              <Badge className="bg-primary/10 text-primary hover:bg-primary/10 text-[10px]">
                {modeLabel(promptMode)}
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
            <SelectValue>{modeLabel(promptMode || "agent")}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="agent">Agent</SelectItem>
            <SelectItem value="chatbot">{tr("Chatbot", "聊天机器人")}</SelectItem>
            <SelectItem value="customize">{tr("Customize", "自定义")}</SelectItem>
          </SelectContent>
        </Select>
        <div className="mt-3 text-xs text-muted-foreground space-y-1.5">
          <div>
            <strong>Agent</strong> — {tr("full framework prompt, task delegation, tool-use guidance, workspace updates, scheduling, and all built-in tools. Best for autonomous task agents.", "完整框架提示词，包含任务委派、工具使用规范、工作区更新、定时任务和全部内置工具，适合自主执行任务。")}
          </div>
          <div>
            <strong>{tr("Chatbot", "聊天机器人")}</strong> — {tr("a lightweight framework where persona files directly shape the voice. It keeps image, speech, and memory-file tools, and is suited to companions, role-play, and customer support.", "轻量框架，由人格文件直接塑造表达方式。保留图像、语音和记忆文件工具，适合陪伴、角色扮演和客户支持。")}
          </div>
          <div>
            <strong>{tr("Customize", "自定义")}</strong> — {tr("only the date anchor and bootstrap files, with no built-in tools. Define the system prompt through SOUL.md and IDENTITY.md, then add tools through plugins.", "仅保留日期锚点和引导文件，不提供内置工具。通过 SOUL.md 和 IDENTITY.md 完整定义系统提示词，并通过插件添加工具。")}
          </div>
        </div>
        <div className="mt-4 pt-3 border-t border-border flex items-start gap-2 text-xs text-muted-foreground">
          <Puzzle className="h-3.5 w-3.5 mt-0.5 shrink-0" />
          <span>
            {tr("Plugin and MCP tools are available in every mode. For a minimal plugin example, see", "插件和 MCP 工具在所有模式下都可用。最小插件示例见")} {" "}
            <code className="text-[11px]">
              ~/.fastclaw/plugins/fastclaw-plugin-demo
            </code>{" "}
            .
          </span>
        </div>
      </div>

      {/* Multi-bubble replies — applies to web chat and every IM channel. Lives here
          rather than in the Channels tab because it's a property of how
          the LLM communicates, not of the channel binding. */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-start gap-3 min-w-0">
            <MessagesSquare className="h-4 w-4 text-primary mt-0.5 shrink-0" />
            <div className="min-w-0">
              <h3 className="font-medium">{tr("Multi-bubble replies", "多气泡回复")}</h3>
              <p className="text-sm text-muted-foreground mt-1">
                {tr("Make replies feel more conversational by splitting them into 2–4 short messages. Applies to web chat and all IM channels. Disabled by default, which keeps each reply in one message.", "将回复拆成 2–4 条短消息，让对话更自然。适用于网页聊天和所有即时通讯渠道。默认关闭，关闭时每次回复只发送一条消息。")}
              </p>
            </div>
          </div>
          <Switch
            checked={splitReplies}
            onCheckedChange={handleSplitRepliesChange}
            disabled={splitRepliesSaving}
            aria-label={tr("Multi-bubble replies", "多气泡回复")}
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
              <h3 className="font-medium">{tr("Automatically remember chatters", "自动记住聊天用户")}</h3>
              <p className="text-sm text-muted-foreground mt-1">
                {tr("As a backup, every five user turns the runtime distills recent conversation into USER.md and MEMORY.md. This preserves useful context when the model does not write those files itself. Disabled by default.", "作为备用机制，运行时每经过五轮用户对话，会把近期内容提炼到 USER.md 和 MEMORY.md。即使模型没有主动写入这些文件，也能保存有用上下文。默认关闭。")}
              </p>
            </div>
          </div>
          <Switch
            checked={autoPersist}
            onCheckedChange={handleAutoPersistChange}
            disabled={autoPersistSaving}
            aria-label={tr("Automatically remember chatters", "自动记住聊天用户")}
          />
        </div>
      </div>

      {/* Shared identity across channels */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-start gap-3 min-w-0">
            <Link2 className="h-4 w-4 text-primary mt-0.5 shrink-0" />
            <div className="min-w-0">
              <h3 className="font-medium">{tr("Shared identity across channels", "跨渠道共享身份")}</h3>
              <p className="text-sm text-muted-foreground mt-1">
                {tr("When enabled, all IM channels share the same session and memory with web chat, so conversations can continue across channels. Disabled by default; each channel otherwise has an isolated session and memory.", "启用后，所有即时通讯渠道与网页聊天共享同一会话和记忆，可以跨渠道继续对话。默认关闭；关闭时各渠道的会话和记忆相互隔离。")}
              </p>
            </div>
          </div>
          <Switch
            checked={sharedIdentity}
            onCheckedChange={handleSharedIdentityChange}
            disabled={sharedIdentitySaving}
            aria-label={tr("Shared identity across channels", "跨渠道共享身份")}
          />
        </div>
      </div>
    </div>
  );
}
