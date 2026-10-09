"use client";
/* eslint-disable @next/next/no-img-element -- User image attachments include local data URLs. */

import * as React from "react";
import { usePathname } from "next/navigation";
import { ArrowUp, Check, ChevronDown, ChevronRight, ChevronUp, CircleAlert, CirclePause, Copy, LoaderCircle, LockKeyhole, Plus, RefreshCw, RotateCcw, Square, SquarePen, UserPlus, UsersRound, Settings, Paperclip, X, MoreHorizontal, Pencil, Trash2, Crown } from "lucide-react";
import { RightPanel, RightPanelListHeader, RightPanelTabs, RightPanelToggle, RIGHT_PANEL_CARD_BG } from "@/components/right-panel";
import { BotAvatar } from "@/components/bot-avatar";
import { CHAT_RELEASE_BOTTOM_STICK_EVENT, FileTreeView, ToolActivity, WorkspacePanel, conversationDayLabel, isSystemFile, scopeRootPrefix, type ProducedFile, type ToolCallEntry } from "@/components/chat-screen";
import { ChatMarkdown } from "@/components/chat-markdown";
import { TeamMembersDialog } from "@/components/team-members-dialog";
import { TeamSettingsDialog } from "@/components/team-settings-dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { useLocale } from "@/components/locale-provider";
import { teamProgressLabel } from "@/lib/team-progress";
import { usePageHeader } from "@/components/sidebar";
import {
  getAgents,
  getChatHistory,
  getConfig,
  getTeamRun,
  getTeamTopics,
  startTeamRun,
  stopTeamRun,
  listAgentFiles,
  type WorkspaceFile,
  type TeamDelivery,
  renameTeamTopic,
  deleteTeamTopic,
  type TeamRun,
  type TeamTopic,
  type TeamMessage,
  type AgentDetail,
  type ChatHistoryMessage,
  type TeamEntry,
} from "@/lib/api";
import { chatHref, rememberChatTarget, useChatRoute } from "@/lib/chat-route";

// A group topic id, shaped like a private chat's (s-<ms>-<rand>) with a g-
// prefix. The server ties the topic to its group through the member
// sessions, so the id doesn't need to carry the team id.
function newTeamTopicId() {
  return `g-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
}

function parseTeamRoute(pathname: string) {
  const route = pathname.match(/^\/teams\/([^/]+)\/chat\/([^/]+)/);
  if (route) {
    return {
      teamId: decodeURIComponent(route[1]),
      sessionId: decodeURIComponent(route[2]),
    };
  }
  // Keep previously shared URLs usable while all new navigation uses the
  // team-first route. Legacy groups had one deterministic conversation.
  const legacy = pathname.match(/^\/agents\/[^/]+\/team\/([^/]+)/);
  const teamId = legacy ? decodeURIComponent(legacy[1]) : "";
  return { teamId, sessionId: teamId ? `team-${teamId}` : "" };
}

function memberSessionId(sessionId: string, agentId: string) {
  return `${sessionId}-agent-${agentId}`;
}

function normalizeTeamContent(value: string, agentReply = false) {
  if (!agentReply) return value.trim();
  // Also cover legacy history and an unfinished streaming delimiter. User
  // messages and fenced code are literal content, not the group protocol.
  let fence = "";
  let inlineCode = "";
  const lines = value.split("\n");
  return lines.map((line, index) => {
    const marker = line.match(/^ {0,3}(`{3,}|~{3,})/)?.[1];
    if (marker) {
      if (!fence) fence = marker;
      else if (marker[0] === fence[0] && marker.length >= fence.length) fence = "";
      return line;
    }
    if (fence) return line;
    let text = line.replace(/`+|<\|split\|>/g, (token) => {
      if (token.startsWith("`")) {
        if (!inlineCode) inlineCode = token;
        else if (inlineCode === token) inlineCode = "";
        return token;
      }
      return inlineCode ? token : "\n\n";
    });
    if (!inlineCode && index === lines.length - 1) {
      const separator = "<|split|>";
      for (let length = separator.length - 1; length > 0; length--) {
        if (text.endsWith(separator.slice(0, length))) {
          text = text.slice(0, -length);
          break;
        }
      }
    }
    return text;
  }).join("\n").trim();
}

function teamToolLine(event: Record<string, string>) {
  return JSON.stringify(event);
}

// parseTeamTools turns a member's tool message (one JSON line per
// tool_call / tool_result event) into the entries ToolActivity renders.
// Calls left without a result once the run is over show as stopped.
function parseTeamTools(message: TeamMessage, settled: boolean): ToolCallEntry[] {
  const calls = new Map<string, ToolCallEntry>();
  for (const line of message.content.split("\n")) {
    if (!line.trim()) continue;
    try {
      const event = JSON.parse(line) as { id?: string; name?: string; arguments?: string; result?: string };
      const id = event.id || message.id;
      const call = calls.get(id) || { id, name: event.name || "tool", arguments: "" };
      if (event.arguments != null) call.arguments = event.arguments;
      if (event.result != null) call.result = event.result;
      if (event.name) call.name = event.name;
      calls.set(id, call);
    } catch {
      calls.set(message.id, { id: message.id, name: "tool", arguments: "", result: message.content });
    }
  }
  return [...calls.values()].map((call) => call.result == null && settled ? { ...call, result: "(stopped)" } : call);
}

type TeamBlock =
  | { kind: "user"; message: TeamMessage }
  | { kind: "status"; message: TeamMessage }
  | { kind: "agent"; id: string; agentId?: string; items: Array<
    | { kind: "text"; id: string; text: string; timestamp: number }
    | { kind: "tools"; id: string; calls: ToolCallEntry[]; timestamp: number }
    | { kind: "private"; id: string; deliveries: TeamDelivery[]; timestamp: number }> };

// buildTeamBlocks groups the flat group transcript into rendered turns:
// a member's consecutive replies and tool calls share one block, and
// back-to-back tool messages collapse into one activity row.
function buildTeamBlocks(messages: TeamMessage[], settled: boolean): TeamBlock[] {
  const blocks: TeamBlock[] = [];
  for (const message of messages) {
    if (message.role === "user" || message.role === "status") {
      blocks.push(message.role === "user" ? { kind: "user", message } : { kind: "status", message });
      continue;
    }
    const isTool = message.role === "tool";
    const text = isTool ? "" : normalizeTeamContent(message.content, true);
    const deliveries = isTool ? [] : message.deliveries || [];
    if (!isTool && !text && deliveries.length === 0) continue;
    let block = blocks[blocks.length - 1];
    if (block?.kind !== "agent" || block.agentId !== message.agentId) {
      block = { kind: "agent", id: `block-${message.id}`, agentId: message.agentId, items: [] };
      blocks.push(block);
    }
    const last = block.items[block.items.length - 1];
    if (isTool && last?.kind === "tools") last.calls.push(...parseTeamTools(message, settled));
    else if (isTool) block.items.push({ kind: "tools", id: message.id, calls: parseTeamTools(message, settled), timestamp: message.timestamp });
    else {
      if (text) block.items.push({ kind: "text", id: message.id, text, timestamp: message.timestamp });
      // Group receipts show who got a private message, never its body.
      if (deliveries.length) block.items.push({ kind: "private", id: `${message.id}-private`, deliveries, timestamp: message.timestamp });
    }
  }
  return blocks;
}

function mergeTeamHistory(
  histories: Array<{ agentId: string; history: ChatHistoryMessage[] }>,
  members: AgentDetail[],
): TeamMessage[] {
  const memberNames = new Set(members.map((member) => (member.name || member.id).toLocaleLowerCase()));
  const merged: Array<TeamMessage & { fallbackOrder: number }> = [];
  let fallbackOrder = 0;

  for (const { agentId, history } of histories) {
    const toolResults = new Map(history.flatMap((item) => item.role === "tool" && item.toolCallId ? [[item.toolCallId, item.content || ""] as const] : []));
    for (const [index, item] of history.entries()) {
      const content = normalizeTeamContent(item.content || "", item.role === "assistant");
      // Tool calls replay in the same one-line-per-event form the live run
      // streams, so both render through the same tool activity row.
      const toolCalls = item.role === "assistant" ? item.toolCalls || [] : [];
      const pushTools = () => {
        if (!toolCalls.length) return;
        // Untimed tool turns sort just before the reply that follows them.
        const timestamp = item.timestamp || history.slice(index + 1).find((later) => later.timestamp)?.timestamp || 0;
        for (const call of toolCalls) {
          const result = toolResults.get(call.id);
          merged.push({
            id: `history-${agentId}-${fallbackOrder}`,
            role: "tool",
            content: [teamToolLine({ id: call.id, name: call.name, arguments: call.arguments }),
              teamToolLine({ id: call.id, name: call.name, result: result ?? "(stopped)" })].join("\n"),
            timestamp,
            agentId,
            groupTurnId: item.groupTurnId,
            fallbackOrder: fallbackOrder++,
          });
        }
      };
      if (!content) { pushTools(); continue; }
      // Bot-to-bot context is stored as a user-role message in the target
      // Agent session. The originating Agent already has its own visible
      // assistant bubble, so don't render the injected copy again.
      if (item.role === "user" && item.senderName) {
        const sender = item.senderName.toLocaleLowerCase();
        if (memberNames.has(sender) || sender === "user") continue;
      }
      if (item.role !== "user" && item.role !== "assistant") continue;
      merged.push({
        id: `history-${agentId}-${fallbackOrder}`,
        role: item.role === "user" ? "user" : "agent",
        content,
        timestamp: item.timestamp || 0,
        agentId: item.role === "assistant" ? agentId : undefined,
        groupTurnId: item.groupTurnId,
        fallbackOrder: fallbackOrder++,
      });
      pushTools();
    }
  }

  merged.sort((a, b) => {
    if (a.timestamp && b.timestamp && a.timestamp !== b.timestamp) return a.timestamp - b.timestamp;
    if (a.timestamp !== b.timestamp) return a.timestamp ? -1 : 1;
    return a.fallbackOrder - b.fallbackOrder;
  });

  const seenUsers = new Set<string>();
  return merged
    .filter((message) => {
      if (message.role !== "user") return true;
      const timeBucket = message.timestamp ? Math.floor(message.timestamp / 5000) : message.fallbackOrder;
      const key = message.groupTurnId || `${timeBucket}:${message.content}`;
      if (seenUsers.has(key)) return false;
      seenUsers.add(key);
      return true;
    })
    .map((message) => ({
      id: message.id,
      role: message.role,
      content: message.content,
      timestamp: message.timestamp,
      agentId: message.agentId,
      groupTurnId: message.groupTurnId,
    }));
}

export function TeamChatScreen() {
  const pathname = usePathname() || "";
  const chatRoute = useChatRoute();
  // /chat/<sessionId> doesn't name the group; AppShell resolved it.
  const routeTeamId = chatRoute.status === "ready" && chatRoute.target.kind === "team" ? chatRoute.target.teamId : "";
  const { teamId, sessionId } = React.useMemo(
    () => (routeTeamId ? { teamId: routeTeamId, sessionId: chatRoute.sessionId } : parseTeamRoute(pathname)),
    [routeTeamId, chatRoute.sessionId, pathname],
  );
  // Closed by default like the private chat's panel. Keep the user's panel
  // choice while the keyed topic view changes.
  const [panelOpen, setPanelOpen] = React.useState(false);
  if (!teamId || !sessionId) return null;
  // The server owns execution; a keyed view owns only this topic's UI.
  return <TeamConversation key={`${teamId}/${sessionId}`} teamId={teamId} sessionId={sessionId}
    panelOpen={panelOpen} onPanelChange={setPanelOpen} />;
}

function TeamConversation({ teamId, sessionId, panelOpen, onPanelChange }: {
  teamId: string;
  sessionId: string;
  panelOpen: boolean;
  onPanelChange: (open: boolean) => void;
}) {
  const { locale, t, tr } = useLocale();
  const [team, setTeam] = React.useState<TeamEntry | null>(null);
  const [members, setMembers] = React.useState<AgentDetail[]>([]);
  const [addMembersOpen, setAddMembersOpen] = React.useState(false);
  const [settingsOpen, setSettingsOpen] = React.useState(false);
  const [panelTab, setPanelTab] = React.useState<"sessions" | "members" | "files" | "settings">("sessions");
  // A member's workspace opened from the Files tab replaces the panel,
  // as in the private chat; its back button returns to the tab.
  const [workspace, setWorkspace] = React.useState<{ agentId: string; sessionId: string; file: ProducedFile } | null>(null);
  const [topicEdit, setTopicEdit] = React.useState<{ topic: TeamTopic; remove: boolean } | null>(null);
  const [topicTitle, setTopicTitle] = React.useState("");
  const [topicSaving, setTopicSaving] = React.useState(false);
  const [images, setImages] = React.useState<string[]>([]);
  const [attachments, setAttachments] = React.useState<Array<{ url: string; name: string }>>([]);
  const fileRef = React.useRef<HTMLInputElement>(null);
  const [caret, setCaret] = React.useState(0);
  const [mentionIndex, setMentionIndex] = React.useState(0);
  const [mentionDismissed, setMentionDismissed] = React.useState(false);
  const [history, setHistory] = React.useState<TeamMessage[]>([]);
  const [run, setRun] = React.useState<TeamRun | null>(null);
  const runRef = React.useRef<TeamRun | null>(null);
  const adoptRun = React.useCallback((next: TeamRun | null) => {
    const previous = runRef.current;
    if (next?.completeHistory) setHistory([]);
    else if (previous?.turnId && previous.turnId !== next?.turnId) {
      setHistory((current) => [
        ...current.filter((message) => message.groupTurnId !== previous.turnId),
        ...previous.messages,
      ]);
    }
    runRef.current = next;
    setRun(next);
  }, []);
  const [topics, setTopics] = React.useState<TeamTopic[]>([]);
  const [input, setInput] = React.useState("");
  const [loading, setLoading] = React.useState(true);
  const [submitting, setSubmitting] = React.useState(false);
  const [loadError, setLoadError] = React.useState("");
  const [actionError, setActionError] = React.useState("");
  const [connectionError, setConnectionError] = React.useState(false);
  const submitRef = React.useRef(false);
  const generationRef = React.useRef(0);
  const textareaRef = React.useRef<HTMLTextAreaElement>(null);
  const scrollRef = React.useRef<HTMLDivElement>(null);
  const contentRef = React.useRef<HTMLDivElement>(null);
  const stickToBottom = React.useRef(true);
  const [scrollState, setScrollState] = React.useState({ overflow: false, canScrollUp: false, canScrollDown: false });
  const [copiedId, setCopiedId] = React.useState<string | null>(null);
  const [lightboxSrc, setLightboxSrc] = React.useState<string | null>(null);
  const sending = submitting || run?.status === "running" || !!run?.activeAgents.length;
  const messages = React.useMemo(() => [
    ...(run?.completeHistory ? [] : history.filter((message) => !run?.turnId || message.groupTurnId !== run.turnId)),
    ...(run?.messages || []),
  ], [history, run]);

  const replaceDeletedTopic = React.useCallback((available: TeamTopic[]) => {
    const nextId = available.find((topic) => topic.sessionId !== sessionId)?.sessionId
      || newTeamTopicId();
    // Group contacts and old bookmarks may still point at the deleted default
    // topic. Replace that URL so Back cannot lead straight into the tombstone.
    rememberChatTarget(nextId, { kind: "team", teamId });
    window.history.replaceState(null, "", chatHref(nextId));
  }, [teamId, sessionId]);

  React.useEffect(() => {
    let cancelled = false;
    Promise.all([getConfig("user"), getAgents()])
      .then(async ([config, agents]) => {
        if (cancelled) return;
        const nextTeam = config.teams?.[teamId];
        if (!nextTeam) throw new Error(tr("This group chat no longer exists.", "这个群聊不存在或已被删除。"));
        const byId = new Map(agents.map((agent) => [agent.id, agent]));
        const nextMembers = nextTeam.agents.map((id) => byId.get(id)).filter((agent): agent is AgentDetail => !!agent);
        if (!nextMembers.length) throw new Error(tr("No accessible Agents remain in this group.", "这个群聊中已没有可访问的 Agent。"));
        setTeam(nextTeam);
        setMembers(nextMembers);
        const { run: current, deleted } = await getTeamRun(teamId, sessionId);
        if (cancelled) return;
        if (deleted) {
          const available = await getTeamTopics(teamId);
          if (!cancelled) replaceDeletedTopic(available.topics);
          return;
        }
        if (!current?.completeHistory) {
          const histories = await Promise.all(nextMembers.map(async (member) => ({ agentId: member.id,
            history: await getChatHistory(member.id, memberSessionId(sessionId, member.id)),
          })));
          if (cancelled) return;
          setHistory(mergeTeamHistory(histories, nextMembers));
        }
        adoptRun(current);
      })
      .catch((cause) => { if (!cancelled) setLoadError(cause instanceof Error ? cause.message : String(cause)); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [sessionId, teamId, tr, adoptRun, replaceDeletedTopic]);

  React.useEffect(() => {
    if (loading || loadError) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      const generation = generationRef.current;
      try {
        const [next, current] = await Promise.all([getTeamTopics(teamId), getTeamRun(teamId, sessionId)]);
        if (cancelled || submitRef.current || generation !== generationRef.current) return;
        if (current.deleted) {
          replaceDeletedTopic(next.topics);
          return;
        }
        setTopics(next.topics);
        adoptRun(current.run);
        setConnectionError(false);
      } catch { if (!cancelled) setConnectionError(true); }
      finally { if (!cancelled) timer = setTimeout(poll, 800); }
    };
    void poll();
    return () => { cancelled = true; clearTimeout(timer); };
  }, [loading, loadError, sessionId, teamId, adoptRun, replaceDeletedTopic]);

  React.useEffect(() => {
    const area = textareaRef.current;
    if (!area) return;
    area.style.height = "32px";
    area.style.height = `${Math.min(area.scrollHeight, 180)}px`;
  }, [input]);

  const updateScrollState = React.useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const next = {
      overflow: el.scrollHeight > el.clientHeight + 8,
      canScrollUp: el.scrollTop > 8,
      canScrollDown: el.scrollHeight - el.scrollTop - el.clientHeight > 8,
    };
    setScrollState((current) => current.overflow === next.overflow && current.canScrollUp === next.canScrollUp
      && current.canScrollDown === next.canScrollDown ? current : next);
  }, []);

  React.useEffect(() => {
    const viewport = scrollRef.current;
    if (!viewport) return;
    if (stickToBottom.current) viewport.scrollTop = viewport.scrollHeight;
    updateScrollState();
  }, [messages, sending, updateScrollState]);

  // Same scroll behavior as the private chat: stay pinned to the bottom
  // while content grows, except after the user opens a disclosure.
  React.useEffect(() => {
    const el = scrollRef.current;
    const content = contentRef.current;
    if (!el) return;
    const onScroll = () => {
      stickToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight <= 64;
      updateScrollState();
    };
    const releaseBottomStick = () => { stickToBottom.current = false; };
    // Publish the list's scrollbar gutter so the composer pads by the
    // same amount and both columns line up (as in the private chat).
    const syncScrollbarGutter = () => {
      el.parentElement?.style.setProperty("--chat-scrollbar", `${el.offsetWidth - el.clientWidth}px`);
    };
    const resizeObserver = new ResizeObserver(() => {
      if (stickToBottom.current) el.scrollTop = el.scrollHeight;
      syncScrollbarGutter();
      updateScrollState();
    });
    el.addEventListener("scroll", onScroll, { passive: true });
    window.addEventListener(CHAT_RELEASE_BOTTOM_STICK_EVENT, releaseBottomStick);
    if (content) resizeObserver.observe(content);
    resizeObserver.observe(el);
    syncScrollbarGutter();
    return () => {
      el.removeEventListener("scroll", onScroll);
      window.removeEventListener(CHAT_RELEASE_BOTTOM_STICK_EVENT, releaseBottomStick);
      resizeObserver.disconnect();
    };
  }, [updateScrollState]);

  const scrollByPage = (direction: -1 | 1) => {
    const el = scrollRef.current;
    el?.scrollBy({ top: direction * Math.max(240, el.clientHeight * 0.72), behavior: "smooth" });
  };

  const copyText = (id: string, text: string) => {
    void navigator.clipboard?.writeText(text).then(() => {
      setCopiedId(id);
      setTimeout(() => setCopiedId((current) => (current === id ? null : current)), 1500);
    }).catch(() => {});
  };

  const formatTime = (ts: number) =>
    new Date(ts).toLocaleTimeString(locale === "zh-CN" ? "zh-CN" : "en-US", { hour: "2-digit", minute: "2-digit" });
  const teamName = team?.name?.trim() || tr("Group chat", "群聊");
  // Same compact header as the private chat: the name opens settings,
  // the panel toggle sits on the right.
  const header = React.useMemo(
    () => (
      <div className="flex h-full min-w-0 flex-1 items-center gap-3 pl-1 pr-4 md:pr-5">
        <button
          type="button"
          onClick={() => setSettingsOpen(true)}
          disabled={!team}
          className="group flex min-w-0 max-w-[min(60vw,32rem)] items-center gap-3 rounded-xl px-1.5 py-1 transition-colors hover:bg-muted/60 focus-visible:ring-2 focus-visible:ring-ring disabled:hover:bg-transparent"
          title={tr("Open settings for {{name}}", "打开 {{name}} 的设置", { name: teamName })}
          aria-label={tr("Open settings for {{name}}", "打开 {{name}} 的设置", { name: teamName })}
        >
          <span className="truncate text-sm font-semibold text-foreground">{teamName}</span>
        </button>
        <div className="ml-auto shrink-0">
          <RightPanelToggle open={panelOpen}
            label={panelOpen ? tr("Close group panel", "关闭群聊侧栏") : tr("Open sessions and members", "打开会话和群成员")}
            onToggle={() => onPanelChange(!panelOpen)} />
        </div>
      </div>
    ),
    [team, teamName, tr, panelOpen, onPanelChange],
  );
  usePageHeader(header, [header]);

  const send = async () => {
    const text = input.trim();
    if ((!text && !images.length && !attachments.length) || sending || submitRef.current || !team) return;
    submitRef.current = true;
    generationRef.current++;
    setSubmitting(true);
    setActionError("");
    stickToBottom.current = true;
    try {
      const next = await startTeamRun(teamId, sessionId, text || tr("Please review the attached images", "请查看附件"), images, attachments);
      generationRef.current++;
      adoptRun(next);
      setInput("");
      setImages([]);
      setAttachments([]);
      setMentionDismissed(true);
      window.dispatchEvent(new CustomEvent("fastclaw:sessions-changed"));
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      generationRef.current++;
      submitRef.current = false;
      setSubmitting(false);
    }
  };

  const stopPressRef = React.useRef(false);
  const stop = async () => {
    try { await stopTeamRun(teamId, sessionId); }
    catch (cause) { setActionError(cause instanceof Error ? cause.message : String(cause)); }
  };
  const openTopic = (id: string) => {
    // The topic may not exist server-side until its first message; record
    // its group so /chat/<id> opens it without a lookup.
    rememberChatTarget(id, { kind: "team", teamId });
    window.history.pushState(null, "", chatHref(id));
  };
  const newTopic = () => openTopic(newTeamTopicId());
  const newTopicRef = React.useRef(newTopic);
  newTopicRef.current = newTopic;
  // ⌘N starts a new session while the panel is open, as in the private chat.
  const shortcutBlocked = !!topicEdit || settingsOpen || addMembersOpen;
  React.useEffect(() => {
    if (!panelOpen || shortcutBlocked) return;
    const handleShortcut = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "n") {
        event.preventDefault();
        newTopicRef.current();
      }
    };
    window.addEventListener("keydown", handleShortcut);
    return () => window.removeEventListener("keydown", handleShortcut);
  }, [panelOpen, shortcutBlocked]);
  const updateTopic = async () => {
    if (!topicEdit || topicSaving) return;
    setTopicSaving(true); setActionError("");
    try {
      if (topicEdit.remove) await deleteTeamTopic(teamId, topicEdit.topic.sessionId);
      else await renameTeamTopic(teamId, topicEdit.topic.sessionId, topicTitle.trim());
      generationRef.current++;
      const next = await getTeamTopics(teamId);
      setTopics(next.topics);
      if (topicEdit.remove && topicEdit.topic.sessionId === sessionId) openTopic(next.topics[0]?.sessionId || newTeamTopicId());
      setTopicEdit(null);
    } catch (e) { setActionError(e instanceof Error ? e.message : String(e)); }
    finally { setTopicSaving(false); }
  };
  const addImages = async (files: File[]) => {
    const selected = files;
    if (selected.some((file) => file.size > 10 * 1024 * 1024)) {
      setActionError(tr("Each file must be under 10 MB", "单个附件不能超过 10 MB")); return;
    }
    try {
      const urls = await Promise.all(selected.slice(0, 8).map((file) => new Promise<string>((resolve, reject) => {
        const reader = new FileReader(); reader.onload = () => resolve(String(reader.result)); reader.onerror = reject; reader.readAsDataURL(file);
      })));
      setImages((current) => [...current, ...urls.filter((_, index) => selected[index].type.startsWith("image/"))].slice(0, 8));
      setAttachments((current) => [...current, ...urls.flatMap((url, index) => selected[index].type.startsWith("image/") ? [] : [{ url, name: selected[index].name }])].slice(0, 8));
    } catch { setActionError(tr("Could not read image", "无法读取图片")); }
  };
  const mention = (() => {
    if (mentionDismissed) return null;
    const before = input.slice(0, caret), start = before.lastIndexOf("@");
    if (start < 0 || (start > 0 && !/[\s,，、(（]/u.test(before[start - 1]))) return null;
    const query = before.slice(start + 1);
    if (query.length > 80 || /[\n@,，。!！?？:：;；]/u.test(query)) return null;
    const names = ["all", ...members.map((m) => m.name || m.id)];
    const normalized = query.normalize("NFKC").toLocaleLowerCase();
    if (!names.some((name) => name.normalize("NFKC").toLocaleLowerCase().startsWith(normalized))
      && names.some((name) => normalized.startsWith(name.normalize("NFKC").toLocaleLowerCase() + " "))) return null;
    const options = [{ id: "all", name: tr("Everyone", "所有成员"), mention: "all" }, ...members.map((m) => ({ id: m.id, name: m.name || m.id, mention: m.name || m.id }))]
      .filter((m) => `${m.name} ${m.id}`.normalize("NFKC").toLocaleLowerCase().includes(normalized));
    return { start, options };
  })();
  const insertMention = (name: string) => {
    if (!mention) return;
    const value = `@${name} `, position = mention.start + value.length;
    setInput(input.slice(0, mention.start) + value + input.slice(caret)); setCaret(position); setMentionDismissed(true);
    requestAnimationFrame(() => { textareaRef.current?.focus(); textareaRef.current?.setSelectionRange(position, position); });
  };
  const statusLabel = (topic: TeamTopic) => {
    if (topic.status === "running" || topic.activeAgents?.length) return tr("Running", "进行中");
    if (topic.limited) return tr("Paused · round limit", "已暂停 · 达到轮数上限");
    if (topic.status === "completed") return tr("Completed", "已完成");
    if (topic.status === "stopped") return tr("Stopped", "已停止");
    if (topic.status === "failed") return tr("Failed", "失败");
    return tr("Idle", "空闲");
  };
  const visibleTopics = topics.some((topic) => topic.sessionId === sessionId) ? topics : [
    { sessionId, title: tr("New session", "新会话"), status: "idle" as const, updatedAt: Date.now(), activeAgents: [] }, ...topics,
  ];

  const showWelcome = !loading && !loadError && messages.length === 0 && members.length > 0;
  const lead = members.find((member) => member.id === (team?.defaultAgent || team?.agents[0])) || members[0];
  const blocks = buildTeamBlocks(messages, !sending);
  const memberName = (id?: string) => members.find((m) => m.id === id)?.name || id || tr("Agent", "Agent");
  const actionButton = "rounded p-0.5 text-muted-foreground/60 opacity-0 transition-all hover:bg-muted hover:text-muted-foreground group-hover:opacity-100";

  return (
    <main className="relative flex h-[calc(100vh-3.5rem)] min-w-0 bg-background">
      <div className="relative flex min-h-0 min-w-0 flex-1 flex-col xl:min-w-[520px]">
      <div ref={scrollRef}
        className="min-h-0 flex-1 overflow-y-auto px-4 py-4 [scrollbar-gutter:stable]">
        <div ref={contentRef} className={`mx-auto w-full max-w-4xl ${loading || loadError ? "flex min-h-full items-center justify-center" : "space-y-3"}`}>
          {loading && (
            <div role="status" aria-live="polite"
              className="inline-flex items-center gap-2.5 rounded-full border border-black/[0.07] bg-card px-4 py-2.5 text-sm text-muted-foreground shadow-[0_5px_20px_rgba(0,0,0,0.04)] dark:border-white/[0.09]">
              <RefreshCw className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
              <span>{tr("Loading group chat…", "正在加载群聊…")}</span>
            </div>
          )}

          {!loading && loadError && (
            <div className="max-w-md rounded-2xl border border-dashed px-6 py-10 text-center">
              <UsersRound className="mx-auto mb-3 size-7 text-muted-foreground" />
              <p className="text-sm text-muted-foreground">{loadError}</p>
            </div>
          )}

          {!loading && !loadError && (
            <div className="pb-5 pt-1 text-center text-xs font-medium text-muted-foreground/75">
              {conversationDayLabel(messages.find((message) => message.timestamp)?.timestamp || Date.now(), locale)}
            </div>
          )}

          {/* A new session opens in conversation mode with a local greeting
              from the lead, like the private chat's new-chat welcome. */}
          {showWelcome && (
            <div className="min-w-0 space-y-2">
              <div className="flex h-5 items-center gap-2 text-[13px] font-medium text-muted-foreground">
                <BotAvatar agentId={lead?.id} avatarUrl={lead?.avatarUrl} seed={lead?.id} size={20} className="rounded-md" />
                <span className="truncate">{memberName(lead?.id)}</span>
              </div>
              <p className="max-w-full whitespace-pre-wrap py-0.5 text-[15px] leading-6 text-[#202020] dark:text-foreground">
                {tr(
                  "Hey, this is {{team}}. I'm {{lead}}, the lead. Mention a member with @, use @all for everyone, or just describe the task and I'll coordinate. What should we start with?",
                  "Hey，这里是「{{team}}」，我是队长{{lead}}。可以 @指定成员，用 @all 让全员参与，或直接说任务，我来协调分工。想先从哪件事开始？",
                  { team: teamName, lead: memberName(lead?.id) },
                )}
              </p>
            </div>
          )}

          {!loading && !loadError && blocks.map((block) => {
            if (block.kind === "status") {
              return (
                // A quiet system notice: small, left-aligned in the turn lane,
                // like the private chat's inline warnings.
                <div key={block.message.id} role="status"
                  className="flex w-fit max-w-full items-start gap-1.5 rounded-lg bg-destructive/[0.06] px-2.5 py-1.5 text-xs leading-5 text-destructive/90 dark:bg-destructive/10">
                  <CircleAlert aria-hidden="true" className="mt-0.5 size-3.5 shrink-0" />
                  <span className="min-w-0 break-words">{block.message.content}</span>
                </div>
              );
            }
            if (block.kind === "user") {
              const message = block.message;
              const text = normalizeTeamContent(message.content);
              return (
                <div key={message.id} className="flex justify-end">
                  <div className="group relative min-w-0 max-w-[80%]">
                    {/* Attachments sit above the bubble, which carries only text. */}
                    {(message.imageUrls?.length || message.attachments?.length) ? (
                      <div className="mb-2 flex flex-wrap justify-end gap-2">
                        {message.imageUrls?.map((url, index) => (
                          <button key={index} type="button" onClick={() => setLightboxSrc(url)}
                            className="block size-32 shrink-0 cursor-zoom-in overflow-hidden rounded-xl border border-black/10 bg-muted/40 transition hover:opacity-90 focus-visible:ring-2 focus-visible:ring-ring md:size-36 dark:border-white/10"
                            aria-label={tr("Preview image", "预览图片")}>
                            <img src={url} alt={tr("Attachment", "图片附件")} className="size-full object-cover" />
                          </button>
                        ))}
                        {message.attachments?.map((file, index) => (
                          <a key={index} href={file.url} download={file.name}
                            className="flex max-w-[16rem] items-center gap-2 rounded-xl border border-black/10 bg-background px-3 py-2 text-sm text-foreground dark:border-white/10">
                            <Paperclip className="size-4 shrink-0 text-muted-foreground" />
                            <span className="truncate">{file.name}</span>
                          </a>
                        ))}
                      </div>
                    ) : null}
                    {text && (
                      <div className="user-chat-bubble break-words rounded-2xl rounded-br-md border border-[#ded5e2] bg-[#eee9f0] px-4 py-2.5 text-[#29252a] dark:border-[#4b404e] dark:bg-[#342d36] dark:text-[#f8f5f9]">
                        <ChatMarkdown text={text} />
                      </div>
                    )}
                    <div className="mt-1 flex items-center justify-end gap-1.5">
                      {message.timestamp > 0 && (
                        <span className="text-[10px] text-muted-foreground/60 opacity-0 transition-all group-hover:opacity-100">{formatTime(message.timestamp)}</span>
                      )}
                      <button type="button" onClick={() => copyText(message.id, text)} className={actionButton} title={tr("Copy", "复制")} aria-label={tr("Copy", "复制")}>
                        {copiedId === message.id ? <Check className="size-3 text-emerald-500" /> : <Copy className="size-3" />}
                      </button>
                      <button type="button" className={actionButton}
                        onClick={() => { setInput(text); requestAnimationFrame(() => textareaRef.current?.focus()); }}
                        title={tr("Resend (refills the composer)", "重新发送（填回输入框）")} aria-label={tr("Resend (refills the composer)", "重新发送（填回输入框）")}>
                        <RotateCcw className="size-3" />
                      </button>
                    </div>
                  </div>
                </div>
              );
            }
            // One member's consecutive replies and tool activity read as a
            // single plain-text turn under that member's name.
            const member = members.find((candidate) => candidate.id === block.agentId);
            const memberSession = member ? memberSessionId(sessionId, member.id) : undefined;
            const replyText = block.items.flatMap((item) => item.kind === "text" ? [item.text] : []).join("\n\n");
            const lastTimestamp = [...block.items].reverse().find((item) => item.timestamp)?.timestamp || 0;
            return (
              <div key={block.id} className="group relative min-w-0 space-y-2">
                <div className="flex h-5 items-center gap-2 text-[13px] font-medium text-muted-foreground">
                  <BotAvatar agentId={member?.id} avatarUrl={member?.avatarUrl} seed={member?.id || block.agentId} size={20} className="rounded-md" />
                  <span className="truncate">{memberName(block.agentId)}</span>
                </div>
                {block.items.map((item) => item.kind === "tools" ? (
                  <ToolActivity key={item.id} items={item.calls.map((tc) => ({ kind: "tool" as const, tc }))} />
                ) : item.kind === "private" ? (
                  <p key={item.id} className="flex items-center gap-2 text-sm leading-6 text-muted-foreground">
                    <LockKeyhole className="size-4 shrink-0 stroke-[1.8]" />
                    <span className="truncate">
                      {tr("Sent private message to {{names}}", "已发送私信给 {{names}}", {
                        names: [...new Set(item.deliveries.map((delivery) =>
                          delivery.recipientId === "human" ? tr("you", "你") : delivery.recipientName || memberName(delivery.recipientId)))].join(", "),
                      })}
                    </span>
                  </p>
                ) : (
                  <div key={item.id} className="break-words py-0.5 text-[#202020] dark:text-foreground">
                    <ChatMarkdown text={item.text} agentId={member?.id} sessionId={memberSession} />
                  </div>
                ))}
                {replyText && (
                  <div className="flex h-5 items-center gap-1.5 opacity-0 transition-opacity focus-within:opacity-100 group-hover:opacity-100">
                    {lastTimestamp > 0 && <span className="text-[10px] text-muted-foreground/60">{formatTime(lastTimestamp)}</span>}
                    <button type="button" onClick={() => copyText(block.id, replyText)} className={actionButton} title={tr("Copy", "复制")} aria-label={tr("Copy", "复制")}>
                      {copiedId === block.id ? <Check className="size-3 text-emerald-500" /> : <Copy className="size-3" />}
                    </button>
                  </div>
                )}
              </div>
            );
          })}

          {!loading && !loadError && sending && (
            <div className="flex items-center gap-2.5">
              <div className="rounded-2xl rounded-bl-md bg-muted px-4 py-3">
                <div className="flex items-center gap-1">
                  {[0, 200, 400].map((delay) => (
                    <span key={delay} className="typing-dot inline-block size-2 rounded-full bg-muted-foreground/60" style={{ animationDelay: `${delay}ms` }} />
                  ))}
                </div>
              </div>
              <span className="truncate text-xs text-muted-foreground">
                {run?.activeAgents.length ? tr("{{names}} is replying…", "{{names}} 正在回复…", { names: run.activeAgents.map((id) => memberName(id)).join(tr(", ", "、")) }) : teamProgressLabel(run, tr)}
              </span>
            </div>
          )}
        </div>
      </div>

      {!loading && !loadError && scrollState.overflow && (
        <div className="pointer-events-none absolute right-3 top-1/2 z-20 flex -translate-y-1/2 flex-col gap-1.5">
          {([[-1, ChevronUp, tr("Scroll up", "向上滚动"), scrollState.canScrollUp], [1, ChevronDown, tr("Scroll down", "向下滚动"), scrollState.canScrollDown]] as const).map(([direction, Icon, label, enabled]) => (
            <button key={direction} type="button" onClick={() => scrollByPage(direction)} disabled={!enabled}
              className="pointer-events-auto flex size-9 items-center justify-center rounded-xl border border-black/[0.08] bg-background/95 text-muted-foreground shadow-[0_6px_20px_rgba(0,0,0,0.12)] backdrop-blur transition hover:bg-background hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-35 dark:border-white/[0.1]"
              aria-label={label} title={label}>
              <Icon className="size-4" />
            </button>
          ))}
        </div>
      )}

      {!loading && !loadError && (
        <div className="shrink-0 pb-5 pl-4 pr-[calc(1rem+var(--chat-scrollbar,0px))] pt-2">
          <div className="relative mx-auto w-full max-w-4xl">
            {(actionError || connectionError) && <p role="status" className="mb-2 px-1 text-sm text-destructive">{actionError || tr("Connection interrupted. Reconnecting; replies continue in the background.", "连接暂时中断，正在重连；回复仍在后台继续。")}</p>}
            {mention && <div id="team-mentions" role="listbox" aria-label={tr("Mention a member", "提及成员")} className="absolute bottom-full left-0 z-20 mb-2 max-h-60 w-72 overflow-auto rounded-2xl border bg-popover p-1.5 shadow-xl">
              {mention.options.length ? mention.options.map((option, index) => <button key={option.id} id={`team-mention-${index}`} role="option" aria-selected={index === mentionIndex}
                className={`flex w-full items-center gap-2.5 rounded-xl px-3 py-2 text-left text-sm ${index === mentionIndex ? "bg-muted" : "hover:bg-muted/50"}`}
                onMouseDown={(e) => e.preventDefault()} onClick={() => insertMention(option.mention)}>
                {option.id === "all" ? <UsersRound className="size-5 text-muted-foreground" /> : <BotAvatar agentId={option.id} size={20} className="rounded-md" />}<span className="truncate">{option.name}</span>
              </button>) : <p className="p-3 text-xs text-muted-foreground">{tr("No matching members", "没有匹配的成员")}</p>}
            </div>}
            <div className="rounded-[22px] border border-black/10 bg-card p-1.5 shadow-[0_8px_28px_rgba(0,0,0,0.055)] transition-shadow focus-within:border-black/15 focus-within:ring-2 focus-within:ring-black/5 dark:border-white/10 dark:focus-within:border-white/16 dark:focus-within:ring-white/5">
              {(images.length > 0 || attachments.length > 0) && (
                <div className="mx-1 mb-2 flex flex-wrap gap-2 border-b border-border/60 px-1 pb-2 pt-1">
                  {images.map((url, index) => (
                    <div key={`image-${index}`} className="group relative size-14 overflow-hidden rounded-md border border-border bg-muted">
                      <button type="button" onClick={() => setLightboxSrc(url)} className="block size-full cursor-zoom-in" aria-label={tr("Preview image", "预览图片")}>
                        <img src={url} alt={tr("Image attachment", "图片附件")} className="size-full object-cover" />
                      </button>
                      <button type="button" onClick={() => setImages((current) => current.filter((_, i) => i !== index))}
                        className="absolute right-0.5 top-0.5 flex size-4 items-center justify-center rounded-full bg-background/80 text-muted-foreground opacity-0 transition hover:text-foreground group-hover:opacity-100"
                        aria-label={tr("Remove attachment", "移除附件")}>
                        <X className="size-3" />
                      </button>
                    </div>
                  ))}
                  {attachments.map((file, index) => (
                    <div key={`file-${index}`} className="flex items-center gap-1.5 rounded-md bg-muted/60 py-1 pl-2 pr-1 text-xs">
                      <Paperclip className="size-3 text-muted-foreground" />
                      <span className="max-w-[160px] truncate">{file.name}</span>
                      <button type="button" onClick={() => setAttachments((current) => current.filter((_, i) => i !== index))}
                        className="rounded p-0.5 text-muted-foreground hover:bg-muted-foreground/15 hover:text-foreground" aria-label={tr("Remove attachment", "移除附件")}>
                        <X className="size-3" />
                      </button>
                    </div>
                  ))}
                </div>
              )}
              <input ref={fileRef} type="file" multiple className="sr-only" onChange={(e) => { void addImages(Array.from(e.target.files || [])); e.target.value = ""; }} />
              <div className="flex items-end gap-2">
                <DropdownMenu>
                  <DropdownMenuTrigger render={
                    <button type="button" aria-label={t("composer.moreOptions")} title={t("composer.moreOptions")}
                      className="flex size-8 shrink-0 items-center justify-center rounded-full border border-border/80 bg-muted/35 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground">
                      <Plus className="size-[17px]" />
                    </button>
                  } />
                  <DropdownMenuContent align="start" side="top" sideOffset={10} className="w-56 rounded-2xl p-1.5 shadow-xl">
                    <DropdownMenuItem onClick={() => fileRef.current?.click()} className="gap-2.5 rounded-xl px-3 py-2.5">
                      <Paperclip className="size-4 text-muted-foreground" />
                      <span>{t("composer.addAttachment")}</span>
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
                <textarea
                  ref={textareaRef}
                  value={input}
                  onChange={(event) => { setInput(event.target.value); setCaret(event.target.selectionStart); setMentionIndex(0); setMentionDismissed(false); }}
                  onSelect={(event) => setCaret(event.currentTarget.selectionStart)}
                  onPaste={(event) => { const files = Array.from(event.clipboardData.items).filter((item) => item.type.startsWith("image/")).flatMap((item) => { const file = item.getAsFile(); return file ? [file] : []; }); if (files.length) { event.preventDefault(); void addImages(files); } }}
                  aria-controls={mention ? "team-mentions" : undefined}
                  aria-activedescendant={mention?.options.length ? `team-mention-${mentionIndex}` : undefined}
                  onKeyDown={(event) => {
                    if (mention && !event.nativeEvent.isComposing) {
                      if (event.key === "Escape") { event.preventDefault(); setMentionDismissed(true); return; }
                      if (mention.options.length && (event.key === "ArrowDown" || event.key === "ArrowUp")) { event.preventDefault(); setMentionIndex((current) => (current + (event.key === "ArrowDown" ? 1 : -1) + mention.options.length) % mention.options.length); return; }
                      if (mention.options.length && (event.key === "Tab" || (event.key === "Enter" && !event.shiftKey))) { event.preventDefault(); insertMention(mention.options[mentionIndex % mention.options.length].mention); return; }
                    }
                    if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
                      event.preventDefault();
                      void send();
                    }
                  }}
                  rows={1}
                  placeholder={tr("Message group · @Agent or @all", "给群聊发消息 · @Agent 或 @all")}
                  className="block min-w-0 flex-1 resize-none bg-transparent px-1 py-1 text-[15px] leading-6 placeholder:text-muted-foreground/45 outline-none [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
                  style={{ maxHeight: 180, minHeight: 32 }}
                />
                {/* Send fires on mousedown and flips `sending` before mouseup;
                    see chat-screen's composer — distinct keys plus a press
                    that must start on Stop keep that mouseup from stopping
                    the run it just started. */}
                {sending ? (
                  <Button key="composer-stop" disabled={submitting}
                    onPointerDown={() => { stopPressRef.current = true; }}
                    onPointerLeave={() => { stopPressRef.current = false; }}
                    onClick={(event) => {
                      const pressed = stopPressRef.current || event.detail === 0;
                      stopPressRef.current = false;
                      if (pressed) void stop();
                    }}
                    size="icon" aria-label={t("composer.stop")}
                    className="size-8 shrink-0 rounded-full bg-[#111] text-white hover:bg-black disabled:bg-[#111] dark:bg-white dark:text-black dark:hover:bg-white/90">
                    <Square className="size-3 fill-current" />
                  </Button>
                ) : (
                  // Always present so the composer reads as sendable; it
                  // stays dimmed until there's something to send.
                  <Button key="composer-send" onMouseDown={(event) => { event.preventDefault(); void send(); }}
                    disabled={!(input.trim() || images.length || attachments.length)} size="icon" aria-label={t("composer.send")}
                    className="size-8 shrink-0 rounded-full bg-[#111] text-white hover:bg-black disabled:bg-black/15 disabled:text-white disabled:opacity-100 dark:bg-white dark:text-black dark:hover:bg-white/90 dark:disabled:bg-white/20 dark:disabled:text-black/60">
                    <ArrowUp className="size-[17px] stroke-[2.25]" />
                  </Button>
                )}
              </div>
            </div>
          </div>
        </div>
      )}
      {lightboxSrc && (
        <div className="fixed inset-0 z-50 flex cursor-zoom-out items-center justify-center bg-black/80 p-6"
          onClick={() => setLightboxSrc(null)} role="dialog" aria-modal="true" aria-label={tr("Image preview", "图片预览")}>
          <img src={lightboxSrc} alt={tr("Preview", "预览")} className="max-h-full max-w-full rounded-lg shadow-2xl" onClick={(e) => e.stopPropagation()} />
          <button type="button" onClick={() => setLightboxSrc(null)} aria-label={tr("Close preview", "关闭预览")}
            className="absolute right-4 top-4 flex size-9 items-center justify-center rounded-full bg-background/80 text-foreground hover:bg-background">
            <X className="size-5" />
          </button>
        </div>
      )}
      </div>
      {workspace && (
        <WorkspacePanel
          key={`${workspace.agentId}/${workspace.sessionId}`}
          agentId={workspace.agentId}
          sessionId={workspace.sessionId}
          initialPreview={workspace.file}
          onBack={() => { setWorkspace(null); setPanelTab("files"); onPanelChange(true); }}
          onClose={() => setWorkspace(null)}
        />
      )}
      {panelOpen && !workspace && (
        <RightPanel title={tr("Group chat", "群聊")} label={tr("group panel", "群聊侧栏")} onClose={() => onPanelChange(false)}>
          <RightPanelTabs label={tr("Group chat", "群聊")} value={panelTab} onChange={setPanelTab} tabs={[
            ["sessions", tr("Sessions", "会话")],
            ["members", tr("Members", "成员")],
            ["files", tr("Files", "文件")],
            ["settings", tr("Settings", "设置")],
          ]} />
          <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-8 pt-3">
            {panelTab === "sessions" && <>
              <RightPanelListHeader
                label={tr("{{count}} sessions", "{{count}} 个会话", { count: visibleTopics.length })}
                actionLabel={tr("New session", "新建会话")} shortcut="⌘N" icon={SquarePen} onAction={newTopic} />
              <nav aria-label={tr("Group sessions", "群聊会话")} className="space-y-0.5">
                {visibleTopics.map((topic) => {
                  const running = topic.status === "running" || !!topic.activeAgents?.length;
                  const active = topic.sessionId === sessionId;
                  const title = topic.title || tr("Untitled session", "未命名会话");
                  const runningNames = topic.activeAgents?.map((id) => members.find((m) => m.id === id)?.name || id).join(" · ");
                  return <div key={topic.sessionId} className={`group relative rounded-xl transition-colors ${RIGHT_PANEL_CARD_BG(active)}`}>
                    <button type="button" onClick={() => openTopic(topic.sessionId)} aria-current={active ? "page" : undefined}
                      title={running && runningNames ? `${title} · ${runningNames}` : title}
                      className="flex h-8 w-full min-w-0 items-center gap-2 rounded-xl px-3 pr-10 text-left focus-visible:ring-2 focus-visible:ring-ring">
                      <span className={`min-w-0 flex-1 truncate text-[13.5px] leading-5 text-foreground ${active ? "font-medium" : ""}`}>{title}</span>
                    </button>
                    {/* Run status sits in the ⋯ button's slot and gives way to it on hover. */}
                    <span className="pointer-events-none absolute right-1.5 top-1/2 flex size-7 -translate-y-1/2 items-center justify-center transition-opacity group-hover:opacity-0 group-focus-within:opacity-0">
                      <TeamTopicStatus topic={topic} label={statusLabel(topic)} />
                    </span>
                    <DropdownMenu>
                      <DropdownMenuTrigger render={<button type="button" onClick={(event) => event.stopPropagation()}
                        aria-label={tr("More actions for {{name}}", "{{name}} 的更多操作", { name: title })}
                        className="absolute right-1.5 top-1/2 flex size-7 -translate-y-1/2 items-center justify-center rounded-lg text-muted-foreground opacity-0 transition-opacity hover:bg-background/70 hover:text-foreground group-hover:opacity-100 group-focus-within:opacity-100 aria-expanded:opacity-100">
                        <MoreHorizontal className="size-4" /></button>} />
                      <DropdownMenuContent align="end" className="w-36 rounded-xl">
                        <DropdownMenuItem disabled={running} onClick={() => { setTopicTitle(topic.title); setTopicEdit({ topic, remove: false }); }}>
                          <Pencil className="size-4 text-muted-foreground" />{tr("Rename", "重命名")}
                        </DropdownMenuItem>
                        <DropdownMenuItem disabled={running} onClick={() => setTopicEdit({ topic, remove: true })} className="text-destructive focus:text-destructive">
                          <Trash2 className="size-4 text-destructive" />{tr("Delete", "删除")}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>;
                })}
              </nav>
            </>}
            {panelTab === "members" && <>
              <RightPanelListHeader
                label={tr("{{count}} members", "{{count}} 位成员", { count: members.length })}
                actionLabel={tr("Manage members", "成员管理")} icon={UserPlus} disabled={!team} onAction={() => setAddMembersOpen(true)} />
              <div className="space-y-0.5">
                {members.map((member) => {
                  const name = member.name || member.id;
                  const lead = (team?.defaultAgent || team?.agents[0]) === member.id;
                  return <button key={member.id} type="button"
                    title={tr("Open settings for {{name}}", "打开 {{name}} 的设置", { name })}
                    className={`flex h-10 w-full min-w-0 items-center gap-2.5 rounded-xl px-3 text-left transition-colors focus-visible:ring-2 focus-visible:ring-ring ${RIGHT_PANEL_CARD_BG(false)}`}
                    // AppSidebar owns the Agent settings dialog; naming the
                    // member opens it for that Agent from the group route.
                    onClick={() => window.dispatchEvent(new CustomEvent("fastclaw:open-agent-settings", { detail: { agentId: member.id } }))}>
                    <BotAvatar agentId={member.id} avatarUrl={member.avatarUrl} seed={member.id} size={24} className="shrink-0 rounded-md" />
                    <span className="min-w-0 flex-1 truncate text-[13.5px] leading-5 text-foreground">{name}</span>
                    {lead && <span className="flex shrink-0 items-center gap-1 text-xs text-muted-foreground"><Crown className="size-3" />{tr("Lead", "队长")}</span>}
                  </button>;
                })}
              </div>
            </>}
            {panelTab === "files" && (
              <TeamFilesTab members={members} sessionId={sessionId} refreshKey={`${run?.turnId || ""}:${sending}`}
                onOpen={(agentId, file) => {
                  setWorkspace({ agentId, sessionId: memberSessionId(sessionId, agentId), file });
                  onPanelChange(false);
                }} />
            )}
            {panelTab === "settings" && (
              <div className="overflow-hidden rounded-xl bg-black/[0.03] dark:bg-white/[0.05]">
                {([
                  [Settings, tr("Group settings", "群聊设置"), () => setSettingsOpen(true)],
                  [UsersRound, tr("Manage members", "成员管理"), () => setAddMembersOpen(true)],
                ] as const).map(([Icon, label, action], index) => (
                  <button key={label} type="button" onClick={action} disabled={!team} title={label}
                    className={`flex h-11 w-full min-w-0 items-center gap-3 px-3.5 text-left text-foreground transition-colors hover:bg-black/[0.04] focus-visible:bg-black/[0.04] focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent dark:hover:bg-white/[0.06] ${
                      index > 0 ? "border-t border-black/[0.05] dark:border-white/[0.06]" : ""}`}>
                    <Icon className="size-4 shrink-0 stroke-[1.8] text-muted-foreground" />
                    <span className="min-w-0 flex-1 truncate text-sm">{label}</span>
                    <ChevronRight className="size-4 shrink-0 text-muted-foreground/70" />
                  </button>
                ))}
              </div>
            )}
          </div>
        </RightPanel>
      )}
      {settingsOpen && team && <TeamSettingsDialog teamId={teamId} team={team} onClose={() => setSettingsOpen(false)}
        onSaved={(next, nextMembers) => { setTeam(next); setMembers(nextMembers); setSettingsOpen(false); window.dispatchEvent(new CustomEvent("fastclaw:teams-changed")); }} />}
      {topicEdit && <Dialog open onOpenChange={(open) => { if (!open && !topicSaving) setTopicEdit(null); }}><DialogContent>
        <DialogHeader><DialogTitle>{topicEdit.remove ? tr("Delete session", "删除会话") : tr("Rename session", "重命名会话")}</DialogTitle>
          <DialogDescription>{topicEdit.remove ? tr("Remove this session from the group?", "确定从群聊中删除这个会话？") : tr("Set a title for this session.", "设置会话名称。")}</DialogDescription></DialogHeader>
        {!topicEdit.remove && <Input aria-label={tr("Session title", "会话名称")} value={topicTitle} onChange={(e) => setTopicTitle(e.target.value)} />}
        {actionError && <p role="alert" className="text-sm text-destructive">{actionError}</p>}
        <DialogFooter><Button variant="outline" disabled={topicSaving} onClick={() => setTopicEdit(null)}>{tr("Cancel", "取消")}</Button>
          <Button disabled={topicSaving || (!topicEdit.remove && !topicTitle.trim())} onClick={() => void updateTopic()}>{tr("Confirm", "确定")}</Button></DialogFooter>
      </DialogContent></Dialog>}
      {addMembersOpen && team && <TeamMembersDialog teamId={teamId} team={team} onClose={() => setAddMembersOpen(false)}
        onSaved={(nextTeam, nextMembers) => {
          setTeam(nextTeam);
          setMembers(nextMembers);
          setAddMembersOpen(false);
          window.dispatchEvent(new CustomEvent("fastclaw:teams-changed"));
        }} />}
    </main>
  );
}

// TeamFilesTab lists what each member produced in this session. Members
// keep separate workspaces, so their trees are listed under their names.
function TeamFilesTab({ members, sessionId, refreshKey, onOpen }: {
  members: AgentDetail[];
  sessionId: string;
  refreshKey: string;
  onOpen: (agentId: string, file: ProducedFile) => void;
}) {
  const { tr } = useLocale();
  const [files, setFiles] = React.useState<Record<string, WorkspaceFile[]> | null>(null);
  React.useEffect(() => {
    let cancelled = false;
    // A finished turn changes refreshKey; keep the current trees while
    // refetching instead of flashing the spinner.
    Promise.all(members.map(async (member) => [member.id,
      (await listAgentFiles(member.id, memberSessionId(sessionId, member.id)).catch(() => [] as WorkspaceFile[]))
        .filter((file) => !isSystemFile(file.path))] as const))
      .then((entries) => { if (!cancelled) setFiles(Object.fromEntries(entries)); });
    return () => { cancelled = true; };
  }, [members, sessionId, refreshKey]);
  if (!files) {
    return <div className="flex justify-center py-6"><LoaderCircle className="size-4 animate-spin text-muted-foreground" /></div>;
  }
  const withFiles = members.filter((member) => files[member.id]?.length);
  if (!withFiles.length) {
    return (
      <p className="px-1 py-6 text-center text-sm leading-5 text-muted-foreground/75">
        {tr("Files members create in this session appear here.", "成员在本会话中生成的文件会显示在这里。")}
      </p>
    );
  }
  return (
    <div className="space-y-4">
      {withFiles.map((member) => {
        const memberFiles = files[member.id];
        return (
          <section key={member.id} aria-label={member.name || member.id}>
            <div className="mb-1 flex h-8 items-center gap-2 px-3 text-[13px] text-muted-foreground">
              <BotAvatar agentId={member.id} avatarUrl={member.avatarUrl} seed={member.id} size={18} className="rounded-md" />
              <span className="min-w-0 flex-1 truncate">{member.name || member.id}</span>
              <span className="shrink-0 tabular-nums text-xs text-muted-foreground/70">{memberFiles.length}</span>
            </div>
            <FileTreeView files={memberFiles} rootPrefix={scopeRootPrefix(memberFiles)} onSelect={(file) => onOpen(member.id, file)} />
          </section>
        );
      })}
    </div>
  );
}

// TeamTopicStatus marks sessions that need attention; idle and completed
// ones stay unmarked, like the private chat's session list.
function TeamTopicStatus({ topic, label }: { topic: TeamTopic; label: string }) {
  const running = topic.status === "running" || !!topic.activeAgents?.length;
  const Icon = running ? LoaderCircle
    : topic.limited || topic.status === "stopped" ? CirclePause
      : topic.status === "failed" ? CircleAlert : null;
  if (!Icon) return null;
  return (
    <span role="img" title={label} aria-label={label} className={`flex size-4 shrink-0 items-center justify-center ${running ? "text-violet-600 dark:text-violet-300" : topic.status === "failed" ? "text-destructive" : "text-muted-foreground"}`}>
      <Icon aria-hidden="true" className={`size-3.5 ${running ? "animate-spin motion-reduce:animate-none" : ""}`} />
    </span>
  );
}

