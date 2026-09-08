"use client";

import { useEffect, useState, useRef, useCallback, useMemo, useLayoutEffect } from "react";
import { useRouter, usePathname, useSearchParams } from "next/navigation";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { createProject, deleteChatSession, fileUrl, getAgent, getAgentKnowledgeFile, getChangedFiles, getChatHistoryWithCursor, getChatSessions, getChatTodo, getMe, getScopePreview, getScopePreviewLogs, getSessionHistory, listAgentFiles, listProjects, renameChatSession, restoreSessionHistory, revealAgentWorkspace, sendChatStream, steerChat, updateAgent, updateProject, uploadAgentFiles, getSkills, type AgentDetail, type ChatHistoryMessage, type ChatStreamEvent, type KnowledgeSource, type ProjectEntry, type ScopePreview, type SkillInfo, type TodoItem, type ToolResultMetadata, type WorkspaceFile, type WorkspaceHistoryEntry } from "@/lib/api";
import { ArrowLeft, ArrowUp, BookOpen, Brain, Check, ChevronDown, ChevronRight, ChevronUp, ChevronsRight, Clock, Code2, Copy, Download, Eye, ExternalLink, File, FileCode, FileText, Film, Folder, FolderOpen, FolderPlus, FolderSearch, Globe2, Image as ImageIcon, Link2, ListChecks, LockKeyhole, MoreHorizontal, Music, PanelLeftClose, PanelLeftOpen, PanelRight, Paperclip, Pencil, Plus, Puzzle, Radio, RefreshCw, RotateCcw, Settings, Share2, ShieldCheck, SlidersHorizontal, Sparkles, Square, SquarePen, Terminal, Trash2, Wrench, X } from "lucide-react";
import Link from "next/link";
import { ChatMarkdown } from "@/components/chat-markdown";
import type { AgentSettingsTab } from "@/components/agent-settings-dialog";

// Split a string on `![alt](data:image/...;base64,...)` markdown.
//
// Real-world content from models is messier than the grammar: base64
// payloads get wrapped with newlines/spaces, the closing `)` is
// sometimes cut off by truncation, and `]` and `(` may sit on separate
// lines. So we look for the header with a regex but consume the URL
// body with a hand-rolled scan that tolerates whitespace inside base64
// and a missing trailing `)`. Returns [...{type:"text"|"image", ...}].
function splitDataImages(s: string): Array<{ type: "text"; text: string } | { type: "image"; alt: string; src: string }> {
  const out: Array<{ type: "text"; text: string } | { type: "image"; alt: string; src: string }> = [];
  const headerRe = /!\[([^\]]*?)\]\s*\(\s*<?\s*(data:image\/[a-z0-9.+-]+;base64,)/gi;
  let lastIdx = 0;
  let m: RegExpExecArray | null;
  while ((m = headerRe.exec(s)) !== null) {
    const alt = m[1];
    const dataPrefix = m[2];
    // Consume base64 body: A–Z a–z 0–9 + / = and any whitespace.
    let cursor = m.index + m[0].length;
    while (cursor < s.length && /[A-Za-z0-9+/=\s]/.test(s[cursor])) cursor++;
    const rawBody = s.slice(m.index + m[0].length, cursor);
    const body = rawBody.replace(/\s+/g, "");
    if (!body) continue;
    const src = dataPrefix + body;
    // Step past optional `>` and `)` closers (or accept truncation).
    let after = cursor;
    while (after < s.length && /[\s>]/.test(s[after])) after++;
    if (s[after] === ")") after++;

    if (m.index > lastIdx) out.push({ type: "text", text: s.slice(lastIdx, m.index) });
    out.push({ type: "image", alt, src });
    lastIdx = after;
    headerRe.lastIndex = after;
  }
  if (lastIdx < s.length) out.push({ type: "text", text: s.slice(lastIdx) });
  return out;
}

// Models sometimes emit markdown images with a line break between `]` and
// `(`, or with very long base64 destinations that some commonmark parsers
// give up on. Rather than fight the markdown parser, extract
// `![alt](data:image/...)` (tolerating whitespace/newlines between `]`
// and `(`) and render those as native <img>, letting ChatMarkdown
// handle everything else. Returns null if no data-URL images are present
// so the caller can fall through to a plain ChatMarkdown render.
// When `suppressAllInlineImages` is true, every data-URL image inside
// the content is dropped (used when the bubble has tool-output images
// already attached at the top — the model often re-embeds an image in
// its reply, and because LLMs can't reproduce base64 verbatim the
// re-embedded bytes are either a duplicate we already showed or a
// hallucination that renders as a different picture). `surfacedSrcs`
// provides a narrower exact-src match for cases where nothing is
// attached to this bubble but a prior bubble showed the same bytes.
function renderContentWithDataImages(
  content: string,
  surfacedSrcs?: ReadonlySet<string>,
  suppressAllInlineImages?: boolean,
  agentId?: string,
  sessionId?: string,
  knowledgeSources?: KnowledgeSource[],
  onKnowledgeCitationClick?: (source: KnowledgeSource) => void,
): React.ReactNode | null {
  const parts = splitDataImages(content);
  if (!parts.some((p) => p.type === "image")) return null;
  return (
    <>
      {parts.map((p, i) => {
        if (p.type === "image") {
          if (suppressAllInlineImages || surfacedSrcs?.has(p.src)) return null;
          return (
            // eslint-disable-next-line @next/next/no-img-element
            <img key={i} src={p.src} alt={p.alt} className="rounded-lg max-w-full h-auto my-2" />
          );
        }
        return (
          <ChatMarkdown
            key={i}
            text={p.text}
            agentId={agentId}
            sessionId={sessionId}
            knowledgeSources={knowledgeSources}
            onKnowledgeCitationClick={onKnowledgeCitationClick}
          />
        );
      })}
    </>
  );
}

import { usePageHeader } from "@/components/sidebar";
import { useSidebarOptional } from "@/components/ui/sidebar";
import { ChannelIcon, channelLabel } from "@/components/channel-icon";
import { BotAvatar } from "@/components/bot-avatar";
import { useLocale, type Locale, type MessageKey } from "@/components/locale-provider";

interface ProducedFile {
  path: string; // path relative to workspace
  size?: number;
}

interface UserAttachment {
  name: string;
  isImage: boolean;
  // Local blob URL for instant in-bubble preview without a server round-trip.
  // Only set on the live-send turn; reloaded history won't carry it.
  previewUrl?: string;
}

// Built-in slash commands surfaced in the composer's `/` menu alongside
// skills. Mirror of the dispatch table in internal/agent/slash.go — keep
// in sync when commands are added/removed/renamed there.
type SlashCommand = { name: string; description: string };
const BUILTIN_COMMANDS: SlashCommand[] = [
  { name: "new", description: "Clear session history" },
  { name: "reset", description: "Clear session history" },
  { name: "retry", description: "Re-run last message" },
  { name: "undo", description: "Undo last turn" },
  { name: "compact", description: "Compress context window" },
  { name: "status", description: "Agent status & memory info" },
  { name: "usage", description: "Billing usage and session stats" },
  { name: "insights", description: "Activity insights (last N days)" },
  { name: "personality", description: "List or switch personality" },
  { name: "model", description: "Show or switch LLM model" },
  { name: "goal", description: "Persistent multi-turn objective" },
  { name: "help", description: "Show command help" },
  { name: "version", description: "Show version" },
];
const READ_ONLY_SLASH_COMMANDS = new Set([
  "help",
  "status",
  "usage",
  "insights",
  "version",
]);
type SlashItem =
  | ({ kind: "command" } & SlashCommand)
  | ({ kind: "skill" } & SkillInfo);

interface ChatMessage {
  id: string;
  role: "user" | "agent" | "tool-group";
  content: string;
  timestamp: number;
  toolCalls?: { id: string; name: string; arguments: string; result?: string; metadata?: ToolResultMetadata }[];
  files?: ProducedFile[];
  attachments?: UserAttachment[];
  // Optimistically-rendered steer bubble awaiting the server's persisted
  // "steer" echo. Used only to dedup against that echo (cleared on
  // match) — not rendered differently.
  pendingSteer?: boolean;
  // Assistant-side metadata (e.g. iteration-cap badge). Stamped from
  // either the live content event's `metadata` payload or the
  // ChatHistoryMessage.metadata on a refresh.
  metadata?: ToolResultMetadata;
  // IM-bridge sender identity, surfaced from session_messages metadata
  // (set by the agent loop for Discord/Telegram/... routed turns).
  // Present means: render an avatar + nickname header instead of an
  // anonymous "you" bubble — the message came from a third party
  // talking to the bot, not from the agent owner themselves.
  sender?: {
    name: string;
    avatarUrl?: string;
    id?: string;
    channel?: string;
  };
}

// Wire token the agent emits to request a multi-bubble reply — must
// match channels.SplitMessageMarker in internal/channels/base.go. On
// IM channels the dispatcher (manager.dispatchOutbound) splits the
// outbound text on this marker into separate platform messages; the
// web UI renders one bubble per split chunk so the experience matches.
const SPLIT_MARKER = "<|split|>";
const CHAT_HISTORY_PAGE_SIZE = 20;

// splitOnMarker breaks `s` on SPLIT_MARKER, trims each chunk, and
// drops the empty ones. Used at render time so a streamed assistant
// reply containing the marker becomes multiple bubbles without any
// upstream content-event rewriting.
function splitOnMarker(s: string): string[] {
  if (!s.includes(SPLIT_MARKER)) return [s];
  const parts = s.split(SPLIT_MARKER).map((p) => p.trim()).filter((p) => p.length > 0);
  return parts.length > 0 ? parts : [s];
}

// Single-segment identity filenames that route to the agent's home dir
// (not the workspace) — exclude from the "Your files" panel.
const SYSTEM_FILES = new Set([
  "SOUL.md", "IDENTITY.md", "USER.md", "BOOTSTRAP.md",
  "MEMORY.md", "KNOWLEDGE.md", "HEARTBEAT.md", "AGENTS.md", "TOOLS.md", "agent.json",
]);

function isSystemFile(path: string): boolean {
  return !path.includes("/") && SYSTEM_FILES.has(path);
}

function parseWrittenSize(result: string): number | undefined {
  const m = result.match(/^Written (\d+) bytes/);
  return m ? parseInt(m[1], 10) : undefined;
}

function isTodoPath(path: unknown): boolean {
  return typeof path === "string" && /(^|[\\/])todo\.md$/i.test(path.trim());
}

// File tools carry their target in different places. write_file/edit_file
// expose a top-level path, while apply_patch embeds every path in its Codex
// patch envelope. Keep this decoding in one place so a successful patch of
// todo.md triggers the same live-panel refresh as an edit_file call.
function toolMutationTouchesTodo(toolName: string, rawArguments: string): boolean {
  if (toolName !== "write_file" && toolName !== "edit_file" && toolName !== "apply_patch") {
    return false;
  }
  try {
    const args = JSON.parse(rawArguments);
    if (toolName !== "apply_patch") {
      return isTodoPath(args?.path) || isTodoPath(args?.file_path);
    }

    const patch = typeof args?.input === "string" ? args.input : "";
    return patch.split("\n").some((line: string) => {
      const match = /^\*\*\* (?:Add|Update|Delete) File:\s*(.+?)\s*$/.exec(line)
        || /^\*\*\* Move to:\s*(.+?)\s*$/.exec(line);
      return match ? isTodoPath(match[1]) : false;
    });
  } catch {
    return false;
  }
}

// Anchor the current todo.md to the conversation turn that last changed it.
// A turn can contain several tool rounds and assistant bubbles, so the card
// belongs after the final non-user message before the next user turn begins.
// This keeps completed checklists in their original place instead of letting
// them drift down to the composer when a later conversation starts.
function findTodoAnchorMessageId(messages: ChatMessage[]): string | null {
  let mutationIndex = -1;
  for (let i = 0; i < messages.length; i++) {
    const message = messages[i];
    if (
      message.role === "tool-group"
      && message.toolCalls?.some((call) =>
        toolMutationTouchesTodo(call.name, call.arguments),
      )
    ) {
      mutationIndex = i;
    }
  }

  if (mutationIndex >= 0) {
    let anchorId = messages[mutationIndex].id;
    for (let i = mutationIndex + 1; i < messages.length; i++) {
      if (messages[i].role === "user") break;
      anchorId = messages[i].id;
    }
    return anchorId;
  }

  // Older histories may not retain tool-call details. In that case, keep
  // the checklist with the latest assistant-side message rather than hiding
  // it or placing it outside the timeline.
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].role !== "user") return messages[i].id;
  }
  return null;
}

interface ChatSession {
  id: string;
  projectId?: string;
  title?: string;
  preview: string;
  createdAt?: number;
  updatedAt?: number;
  thumbnailUrl?: string;
  // channel/accountId/chatId travel with the listing so the chat
  // page can decide whether composing into this session is allowed
  // (only `web` is — IM channels have no reverse-send path).
  channel?: string;
  accountId?: string;
  chatId?: string;
}

function generateSessionId() {
  return `s-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
}

const PASTED_IMAGE_EXTENSIONS: Record<string, string> = {
  "image/avif": "avif",
  "image/gif": "gif",
  "image/heic": "heic",
  "image/heif": "heif",
  "image/jpeg": "jpg",
  "image/png": "png",
  "image/svg+xml": "svg",
  "image/webp": "webp",
};

// Clipboard image files are commonly all named `image.png`. Give every
// paste a unique, useful name so uploading a second screenshot doesn't
// overwrite the first one in the session workspace.
function namePastedImage(file: File, pasteId: number, index: number): File {
  const extension = PASTED_IMAGE_EXTENSIONS[file.type.toLowerCase()] || "png";
  return new globalThis.File([file], `pasted-image-${pasteId}-${index + 1}.${extension}`, {
    type: file.type || `image/${extension}`,
    lastModified: Date.now(),
  });
}

/** Convert raw history messages into UI ChatMessages, grouping tool calls with results. */
function buildChatMessages(history: ChatHistoryMessage[], historyOffset = 0): ChatMessage[] {
  const msgs: ChatMessage[] = [];
  const historyId = (index: number) => historyOffset + index;
  let i = 0;
  while (i < history.length) {
    const h = history[i];
    if (h.role === "user") {
      // Surface image attachments on history-loaded user bubbles. The
      // server emits `imageUrls` on user turns whose ContentParts had
      // image_url blocks; map them into UserAttachment entries so the
      // existing bubble renderer (live-send path) handles them with no
      // additional branching.
      const attachments: UserAttachment[] | undefined =
        h.imageUrls && h.imageUrls.length > 0
          ? h.imageUrls.map((url, idx) => ({
              name: `image-${idx + 1}`,
              isImage: true,
              previewUrl: url,
            }))
          : undefined;
      const sender = h.senderName
        ? {
            name: h.senderName,
            avatarUrl: h.senderAvatarUrl,
            id: h.senderId,
            channel: h.senderChannel,
          }
        : undefined;
      msgs.push({ id: `h-${historyId(i)}`, role: "user", content: h.content || "", timestamp: 0, attachments, sender });
      i++;
    } else if (h.role === "assistant" && h.toolCalls && h.toolCalls.length > 0) {
      // Group: assistant tool_calls + following tool results + final assistant content
      const calls = h.toolCalls.map((tc) => ({
        ...tc,
        result: undefined as string | undefined,
        metadata: undefined as ToolResultMetadata | undefined,
      }));
      i++;
      // Collect tool results
      while (i < history.length && history[i].role === "tool") {
        const toolMsg = history[i];
        const call = calls.find((c) => c.id === toolMsg.toolCallId);
        if (call) {
          call.result = toolMsg.content;
          if (toolMsg.metadata) call.metadata = toolMsg.metadata;
        }
        i++;
      }
      // Defensive: any tool_use that still has no result by the time
      // we leave the tool-result run got orphaned (client aborted, server
      // crashed mid-turn, persistence path raced). Mark them stopped so
      // the UI shows a terminal state instead of spinning forever.
      // Newer turns will have these padded server-side via
      // padOrphanToolResults; this catches sessions that pre-date the fix.
      for (const c of calls) {
        if (c.result === undefined) {
          c.result = "(stopped)";
        }
      }
      // If this assistant turn produced text alongside its tool calls
      // (common with "final answer + closing tool" patterns like text +
      // update_goal), surface that text as its own agent bubble BEFORE
      // the tool-group instead of folding it into the tool-group's
      // content. Folded, the body reads as preamble to a collapsed tool
      // block; split, the model's actual answer stands as a first-class
      // reply.
      if (h.content) {
        msgs.push({ id: `h-pre-${historyId(i)}`, role: "agent", content: h.content, timestamp: 0, metadata: h.metadata });
      }
      msgs.push({
        id: `h-tool-${historyId(i)}`,
        role: "tool-group",
        content: "",
        timestamp: 0,
        toolCalls: calls,
      });
      // If next is assistant with ONLY content and no tool calls (final
      // answer), add it. Must skip when the next assistant also has tool
      // calls — in a multi-turn conversation that's the *start of the next
      // tool-group*, not a final answer, and consuming it here would drop
      // its tool calls on the floor and leave subsequent tool-result
      // messages orphaned.
      if (
        i < history.length &&
        history[i].role === "assistant" &&
        history[i].content &&
        !(history[i].toolCalls && history[i].toolCalls!.length > 0)
      ) {
        msgs.push({ id: `h-${historyId(i)}`, role: "agent", content: history[i].content || "", timestamp: 0, metadata: history[i].metadata });
        i++;
      }
    } else if (h.role === "assistant") {
      msgs.push({ id: `h-${historyId(i)}`, role: "agent", content: h.content || "", timestamp: 0, metadata: h.metadata });
      i++;
    } else {
      i++; // skip unexpected
    }
  }
  return msgs;
}

// isPendingPlanContent recognises the closing line we instructed the
// model to emit on plan-first turns ("Reply `go` to execute, or tell
// me what to change." or its Chinese rendering). Used to decide
// whether to show the inline approve / cancel buttons under an
// assistant bubble. Loose match — model wording drifts but the
// signal is always "go" near "execute / 执行" in the last few lines.
function isPendingPlanContent(content: string): boolean {
  if (!content) return false;
  // Tail-only check so a long plan with the word "go" in step 3 doesn't
  // false-positive — the model only ever closes with the cue line, never
  // opens with it.
  const lines = content.split("\n");
  const tail = lines.slice(Math.max(0, lines.length - 4)).join(" ");
  // English: "Reply `go` to execute" / "Reply 'go' to run"
  if (/reply[^.]*?\bgo\b[^.]*?(execute|run)/i.test(tail)) return true;
  // Chinese: "请回复 go 开始执行" / "回复 go 执行"
  if (/回复[^。]*?go[^。]*?(执行|开始)/.test(tail)) return true;
  // Generic safety net: closing imperative with `go` + execute keyword
  // very close together. Tighter than just "go" appearing anywhere.
  if (/[`'"]go[`'"][^\n]{0,40}(execute|执行)/i.test(tail)) return true;
  return false;
}

// TodoPanel renders the per-session todo.md the agent maintains as a
// live progress checklist inside the conversation. "Current step" is
// the first unchecked item — we surface it as a single line with a
// "<n>/<total>" counter, and the full list expands on click. Hidden
// entirely when no items exist (caller's responsibility — keeps this
// dumb-component pure).
function TodoPanel({ items, active }: { items: TodoItem[]; active: boolean }) {
  const { tr } = useLocale();
  const [open, setOpen] = useState(true);
  const total = items.length;
  const doneCount = items.filter((i) => i.done).length;
  const allDone = doneCount === total;
  // First unchecked item is the live "in progress" step. When the
  // checklist is fully checked, fall through to the last item so the
  // collapsed header still says something concrete.
  const currentIdx = allDone ? total - 1 : items.findIndex((i) => !i.done);
  const current = currentIdx >= 0 ? items[currentIdx] : null;
  return (
    // Keep the checklist visually attached to an assistant turn while
    // retaining the compact reading width used by the previous panel.
    <div className="flex justify-start">
      <div className="w-full max-w-2xl">
        <div className="rounded-lg border border-border bg-muted/40 px-3 py-2 shadow-sm">
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            className="flex w-full items-center gap-2 text-left text-sm"
            aria-expanded={open}
          >
            {allDone ? (
              <Check className="size-4 shrink-0 text-emerald-600" />
            ) : active ? (
              <div className="size-4 shrink-0 rounded-full border-2 border-amber-500 border-t-transparent animate-spin" />
            ) : (
              // Paused: agent isn't streaming. Show a static amber ring
              // so the "where we are" cue is visible without implying
              // ongoing work.
              <div className="size-4 shrink-0 rounded-full border-2 border-amber-500/70" />
            )}
            <span className="font-medium tabular-nums text-muted-foreground">
              {doneCount}/{total}
            </span>
            <span className="truncate flex-1">
              {current ? current.text : tr("Plan checklist", "计划清单")}
            </span>
            {open ? (
              <ChevronDown className="size-4 shrink-0 text-muted-foreground" />
            ) : (
              <ChevronRight className="size-4 shrink-0 text-muted-foreground" />
            )}
          </button>
          {open && (
            <ol className="mt-2 space-y-1 border-t border-border/60 pt-2 text-sm">
              {items.map((it, i) => {
                const isCurrent = i === currentIdx && !it.done;
                return (
                  <li
                    key={i}
                    className={
                      "flex items-start gap-2 rounded px-1.5 py-0.5 " +
                      (isCurrent ? "bg-amber-500/10" : "")
                    }
                  >
                    {it.done ? (
                      <Check className="mt-0.5 size-3.5 shrink-0 text-emerald-600" />
                    ) : isCurrent ? (
                      active ? (
                        <div className="mt-0.5 size-3.5 shrink-0 rounded-full border-2 border-amber-500 border-t-transparent animate-spin" />
                      ) : (
                        <div className="mt-0.5 size-3.5 shrink-0 rounded-full border-2 border-amber-500/70" />
                      )
                    ) : (
                      <div className="mt-1 size-2.5 shrink-0 rounded-full border border-muted-foreground/40" />
                    )}
                    <span
                      className={
                        (it.done ? "line-through text-muted-foreground/70 " : "") +
                        (isCurrent ? "font-medium" : "")
                      }
                    >
                      {it.text}
                    </span>
                  </li>
                );
              })}
            </ol>
          )}
        </div>
      </div>
    </div>
  );
}

// Parse the per-route ids out of the pathname. ChatScreen is mounted
// once at the agent layout level and stays alive across these routes:
//
//   /agents/<aid>/                         — fresh loose chat
//   /agents/<aid>/chat/                    — fresh loose chat
//   /agents/<aid>/chat/<session>           — open existing chat by id
//   /agents/<aid>/project/<pid>            — fresh chat in a project
//
// Reading from `usePathname()` (instead of accepting props from the
// page tree) is what lets the component instance survive sidebar
// navigations — sessionId / projectId become reactive values that
// update in place rather than gating a remount.
function parseAgentRoute(pathname: string): {
  sessionId: string;
  projectId: string;
} {
  const sessMatch = pathname.match(/^\/agents\/[^/]+\/chat\/([^/]+)/);
  if (sessMatch) {
    const sid = sessMatch[1];
    // "_" is the build-time placeholder Next emits under output:'export'
    // for the dynamic [session] segment. Treat it as "no session".
    return { sessionId: sid === "_" ? "" : sid, projectId: "" };
  }
  const projMatch = pathname.match(/^\/agents\/[^/]+\/project\/([^/]+)/);
  if (projMatch) {
    const pid = projMatch[1];
    return { sessionId: "", projectId: pid === "_" ? "" : pid };
  }
  return { sessionId: "", projectId: "" };
}

function conversationDayLabel(timestamp: number, locale: Locale) {
  if (!timestamp) return locale === "zh-CN" ? "今天" : "Today";
  const date = new Date(timestamp);
  const now = new Date();
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const startOfDate = new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
  const dayDelta = Math.round((startOfToday - startOfDate) / 86_400_000);
  if (dayDelta === 0) return locale === "zh-CN" ? "今天" : "Today";
  if (dayDelta === 1) return locale === "zh-CN" ? "昨天" : "Yesterday";
  return date.toLocaleDateString(locale, { month: "short", day: "numeric", year: "numeric" });
}

export function ChatScreen() {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const { locale, t, tr } = useLocale();
  const isChatsPage = /^\/agents\/[^/]+\/chats\/?$/.test(pathname || "");
  // When `?actAs=<uid>` is in the URL, this chat is being opened by a
  // super_admin viewing another user's session (read-only by middleware).
  // Forces the composer into a disabled state and surfaces a banner so
  // the admin can't try to type and get a silent 403.
  const actAsUserId = searchParams?.get("actAs") || "";
  const isActAsView = !!actAsUserId;
  const { sessionId: urlSessionId, projectId: urlProjectId } = useMemo(
    () => parseAgentRoute(pathname || ""),
    [pathname],
  );
  // A Chats route can be reconstructed by the static-export router even
  // though it visually behaves like a right-panel view. Carry the active
  // session in the query so the central conversation can be restored after
  // that reconstruction instead of falling back to a fresh welcome screen.
  const chatsSessionId = isChatsPage ? searchParams?.get("session") || "" : "";
  const routeSessionId = urlSessionId || chatsSessionId;
  // Reactive: re-derives from pathname so switching agents (sidebar
  // dropdown, browser back/forward) immediately updates downstream
  // fetches. The previous useState(() => ...) flavor froze the id at
  // mount, so background loads kept hitting the old agent and the
  // panel showed stale history under the new URL.
  const selectedAgent = useAgentIdFromURL();
  const [agentName, setAgentName] = useState<string>("");
  const [agentDetail, setAgentDetail] = useState<AgentDetail | null>(null);
  const [projects, setProjects] = useState<ProjectEntry[]>([]);
  const [sessionId, setSessionId] = useState<string>(
    () => routeSessionId || generateSessionId(),
  );
  const [sessions, setSessions] = useState<ChatSession[]>([]);
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState("");
  const [sending, setSending] = useState(false);
  // todo.md state for the current session — agent maintains the file,
  // we re-fetch on every write_file/edit_file event that touches
  // todo.md plus once at mount. Empty `items` hides the panel.
  const [todoItems, setTodoItems] = useState<TodoItem[]>([]);
  // Last subagent_progress event from the active delegate_task run.
  // Cleared when the subagent reports phase="done" or when sending
  // turns off, so it never lingers across turns. Only one subagent
  // runs at a time (delegate_task is registered serial) so we don't
  // need to key this by tool_call_id.
  const [subagentProgress, setSubagentProgress] = useState<null | {
    iteration?: number;
    max?: number;
    phase?: "thinking" | "running" | "final-delivery" | "done";
    tools?: string[];
  }>(null);
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [filesSheetOpen, setFilesSheetOpen] = useState(false);
  const [botPanelOpen, setBotPanelOpen] = useState(false);
  const [workspaceReturnsToBotPanel, setWorkspaceReturnsToBotPanel] = useState(false);
  const [knowledgePreview, setKnowledgePreview] = useState<KnowledgeSource | null>(null);
  // The compact workspace is only a file navigator, so it can coexist with
  // the platform sidebar. Collapse that sidebar only while a file/app preview
  // needs the extra canvas, then restore the state the user had before.
  const sidebar = useSidebarOptional();
  const sidebarOpenRef = useRef(sidebar?.open ?? false);
  const sidebarSetOpenRef = useRef(sidebar?.setOpen);
  const workspacePreviewActiveRef = useRef(false);
  const sidebarWasOpenBeforePreviewRef = useRef(false);
  useEffect(() => {
    sidebarOpenRef.current = sidebar?.open ?? false;
    sidebarSetOpenRef.current = sidebar?.setOpen;
  }, [sidebar?.open, sidebar?.setOpen]);
  const handleWorkspacePreviewChange = useCallback((active: boolean) => {
    if (workspacePreviewActiveRef.current === active) return;
    workspacePreviewActiveRef.current = active;
    if (active) {
      sidebarWasOpenBeforePreviewRef.current = sidebarOpenRef.current;
      if (sidebarOpenRef.current) sidebarSetOpenRef.current?.(false);
      return;
    }
    if (sidebarWasOpenBeforePreviewRef.current) {
      sidebarSetOpenRef.current?.(true);
    }
    sidebarWasOpenBeforePreviewRef.current = false;
  }, []);
  const openWorkspace = useCallback(() => {
    setWorkspaceReturnsToBotPanel(false);
    setBotPanelOpen(false);
    setFilesSheetOpen(true);
  }, []);
  const openWorkspaceFromBotPanel = useCallback(() => {
    setWorkspaceReturnsToBotPanel(true);
    setBotPanelOpen(false);
    setFilesSheetOpen(true);
  }, []);
  const openKnowledgeCitation = useCallback((source: KnowledgeSource) => {
    setKnowledgePreview(source);
    openWorkspace();
  }, [openWorkspace]);
  const [attachments, setAttachments] = useState<File[]>([]);
  // Lightbox for clicking either an attachment thumbnail (compose box)
  // or an inline image in a sent message bubble. `null` = closed.
  const [lightboxSrc, setLightboxSrc] = useState<string | null>(null);
  // Object URLs for image attachments in the compose box. Keyed by file
  // index so we can revoke on remove without re-computing for every
  // chip on every keystroke. Re-derived whenever `attachments` changes.
  const attachmentPreviews = useMemo(
    () =>
      attachments.map((f) =>
        f.type.startsWith("image/") ? URL.createObjectURL(f) : null,
      ),
    [attachments],
  );
  useEffect(() => {
    return () => {
      for (const url of attachmentPreviews) if (url) URL.revokeObjectURL(url);
    };
  }, [attachmentPreviews]);
  const messagesScrollRef = useRef<HTMLDivElement>(null);
  const messagesContentRef = useRef<HTMLDivElement>(null);
  // Auto-scroll only when the user is already pinned to the bottom.
  // Toggled false the moment they scroll up so streaming agent output
  // can't yank them back down mid-read, then flipped back to true once
  // they return to the bottom (or hit the "scroll to latest" button).
  const stickToBottomRef = useRef(true);
  const historyStartRef = useRef(0);
  const historyLoadingOlderRef = useRef(false);
  const activeHistoryScopeRef = useRef({ agentId: selectedAgent, sessionId });
  const pendingInitialScrollRef = useRef("");
  const pendingPrependScrollRef = useRef<{ height: number; top: number } | null>(null);
  const [hasOlderHistory, setHasOlderHistory] = useState(false);
  const [messageScrollState, setMessageScrollState] = useState({
    overflow: false,
    canScrollUp: false,
    canScrollDown: false,
  });
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const pasteIdRef = useRef(0);
  // Todo reads are asynchronous and can finish after the user has moved to
  // another conversation. Keep both the active scope and a monotonically
  // increasing request token so neither an old session response nor an
  // earlier request within the same session can overwrite current state.
  const activeTodoScopeRef = useRef({ agentId: selectedAgent, sessionId });
  const todoRequestTokenRef = useRef(0);
  const resetTodoScope = useCallback((agentId: string, nextSessionId: string) => {
    activeTodoScopeRef.current = { agentId, sessionId: nextSessionId };
    todoRequestTokenRef.current += 1;
    setTodoItems([]);
  }, []);
  const refreshTodoForScope = useCallback((agentId: string, targetSessionId: string) => {
    const active = activeTodoScopeRef.current;
    if (active.agentId !== agentId || active.sessionId !== targetSessionId) return;

    const requestToken = ++todoRequestTokenRef.current;
    getChatTodo(agentId, targetSessionId)
      .then((todo) => {
        const current = activeTodoScopeRef.current;
        if (
          todoRequestTokenRef.current === requestToken
          && current.agentId === agentId
          && current.sessionId === targetSessionId
        ) {
          setTodoItems(todo.items);
        }
      })
      .catch(() => {
        const current = activeTodoScopeRef.current;
        if (
          todoRequestTokenRef.current === requestToken
          && current.agentId === agentId
          && current.sessionId === targetSessionId
        ) {
          setTodoItems([]);
        }
      });
  }, []);

  // Dedupe events arriving on both the active POST stream and the
  // parallel /api/chat/subscribe SSE — both subscribe to the same
  // chat-events hub on the server. Tracks the highest seq we've
  // already rendered for the current session.
  const maxSeqRef = useRef<number>(-1);
  // Resume cursor handed to /api/chat/subscribe so a freshly reloaded
  // page replays only deltas it didn't see. Captured from the chat
  // history endpoint, refreshed when sessionId changes.
  const subscribeSinceRef = useRef<number>(-1);
  // Transient assistant bubble created from subscribe-replayed content
  // events (i.e. when the user reloaded mid-turn and we're catching up
  // before the agent's "done" lands). Cleared on the next history
  // reload, which replaces the placeholder with the canonical message
  // pulled from session_messages.
  const transientBubbleIdRef = useRef<string | null>(null);
  // streamingMsgIdRef holds the id of the assistant bubble currently
  // accreting content_delta chunks via the active POST sendChatStream.
  // Shared with the parallel /api/chat/subscribe SSE handler so that
  // handler can detect "POST stream owns this turn" and skip its own
  // bubble-creation path for the trailing `content` event — both
  // handlers see the same content event from the hub, and if subscribe
  // wins the race the dedup-by-seq guard won't fire on the POST side,
  // producing a duplicate bubble. The ref is reset to null at startNewGroup
  // and when a tool_call rolls the bubble into a tool-group.
  const streamingMsgIdRef = useRef<string | null>(null);
  // First-send navigation (`/chat/` -> `/chat/<sid>/`) triggers the
  // history-loading effect while the POST stream is still in flight.
  // Keep that fetch from clearing optimistic bubbles or advancing the
  // seq cursor past events the POST handler is about to render.
  const inFlightSendSessionRef = useRef<string | null>(null);
  // AbortController for the in-flight chat stream so the Stop button can
  // cancel both the upload and the SSE connection. Reset on every new turn.
  const abortRef = useRef<AbortController | null>(null);

  // Gates the EventSource effect: holds the sessionId whose history has
  // been fetched and whose `subscribeSinceRef` is now accurate. Without
  // this gate, the SSE effect (declared earlier and therefore run first)
  // would open `/api/chat/subscribe?since=-1` before the history fetch
  // resolved — server-side that means "replay every session_event" and
  // for a long deep-research chat the replay floods the client AND ties
  // up the EventSource HTTP connection slot. Stack a few of those across
  // rapid project↔chat navigations and the browser's 6-conn-per-origin
  // limit pins everything `pending`, the page goes white, and clicks
  // stop registering. Storing the loaded sessionId (not just a boolean)
  // keeps the gate honest if sessionId changes again before history
  // catches up.
  const [loadedSessionId, setLoadedSessionId] = useState<string | null>(null);
  const isConversationLoading = Boolean(
    selectedAgent
      && routeSessionId
      && (routeSessionId !== sessionId || loadedSessionId !== routeSessionId),
  );

  // Slash-command menu state. The menu opens when the textarea holds a
  // token beginning with `/` at the caret; selecting a skill swaps that
  // token for `/<skill-name> `, leaving the cursor after the space so the
  // user can keep typing their prompt.
  const [skills, setSkills] = useState<SkillInfo[]>([]);
  const [slashOpen, setSlashOpen] = useState(false);
  const [slashQuery, setSlashQuery] = useState("");
  const [slashIndex, setSlashIndex] = useState(0);

  useEffect(() => {
    getSkills().then(setSkills).catch(() => setSkills([]));
  }, []);

  // Reset chat-pane state when the active agent changes. Without
  // this, switching agents from the sidebar would briefly render the
  // previous agent's history / sessions / title under the new URL
  // until the load effects below replaced them. Clearing eagerly is
  // cheaper than threading a "loading" flag through every render.
  // We deliberately keep `input` so a half-typed message survives an
  // accidental agent switch.
  const lastAgentRef = useRef(selectedAgent);
  useEffect(() => {
    if (lastAgentRef.current === selectedAgent) return;
    lastAgentRef.current = selectedAgent;
    resetTodoScope(selectedAgent, sessionId);
    setMessages([]);
    setSessions([]);
    setProjects([]);
    setAgentName("");
    setAgentDetail(null);
    setAttachments([]);
  }, [selectedAgent, sessionId, resetTodoScope]);

  // Resolve the agent's display name once. The chat title and any
  // future header bits should show "Chat with My Helper", not the
  // opaque agt_xxx id. Uses /api/agents/{id} (owner or super_admin) so
  // an admin viewing another user's agent still gets the real name —
  // /api/agents (list) is owner-scoped and would miss it.
  useEffect(() => {
    if (!selectedAgent) return;
    let aborted = false;
    getAgent(selectedAgent)
      .then((a) => {
        if (aborted) return;
        setAgentDetail(a);
        setAgentName(a?.name || a?.id || selectedAgent);
      })
      .catch(() => {
        if (!aborted) {
          setAgentDetail(null);
          setAgentName(selectedAgent);
        }
      });
    return () => {
      aborted = true;
    };
  }, [selectedAgent]);

  const loadProjects = useCallback((agentId: string) => {
    listProjects(agentId)
      .then((list) => setProjects(list || []))
      .catch(() => setProjects([]));
  }, []);

  useEffect(() => {
    if (!selectedAgent) return;
    loadProjects(selectedAgent);
  }, [selectedAgent, loadProjects]);

  const projectInfo = useMemo(
    () => (urlProjectId ? projects.find((project) => project.id === urlProjectId) ?? null : null),
    [projects, urlProjectId],
  );

  // Detect whether the caret is inside a /token and, if so, what's been
  // typed after the slash. Cheap enough to run every keystroke.
  const slashContext = (value: string, caret: number): { start: number; query: string } | null => {
    const before = value.slice(0, caret);
    // Require the slash to be at start-of-message or preceded by whitespace
    // so paths / URLs with slashes don't trigger the menu.
    const match = /(^|\s)\/([\w-]*)$/.exec(before);
    if (!match) return null;
    return { start: caret - match[2].length - 1, query: match[2] };
  };

  // Merged command + skill list for the slash menu. Commands first so
  // built-ins are easy to find; query matches both name and description.
  // Cap at 8 to keep the popover from outgrowing the composer.
  const filteredItems: SlashItem[] = slashOpen
    ? (() => {
        const q = slashQuery.toLowerCase();
        const match = (name: string, desc: string) =>
          !q || name.toLowerCase().includes(q) || desc.toLowerCase().includes(q);
        const cmds: SlashItem[] = BUILTIN_COMMANDS
          .filter((c) => match(c.name, c.description))
          .map((c) => ({ kind: "command", ...c }));
        const sks: SlashItem[] = skills
          .filter((s) => match(s.name, s.description || ""))
          .map((s) => ({ kind: "skill", ...s }));
        return [...cmds, ...sks].slice(0, 8);
      })()
    : [];

  const selectItem = useCallback(
    (item: SlashItem) => {
      const el = textareaRef.current;
      if (!el) return;
      const caret = el.selectionStart ?? input.length;
      const ctx = slashContext(input, caret);
      if (!ctx) return;
      const before = input.slice(0, ctx.start);
      const after = input.slice(caret);
      const insert = `/${item.name} `;
      const next = before + insert + after;
      setInput(next);
      setSlashOpen(false);
      setSlashQuery("");
      setSlashIndex(0);
      requestAnimationFrame(() => {
        const pos = before.length + insert.length;
        el.focus();
        el.setSelectionRange(pos, pos);
      });
    },
    [input],
  );

  const getBuiltInSlashCommandName = useCallback((value: string) => {
    const trimmed = value.trim();
    const match = /^\/([\w-]+)(?:\s|$)/.exec(trimmed);
    if (!match) return "";
    return BUILTIN_COMMANDS.some((c) => c.name === match[1]) ? match[1] : "";
  }, []);

  const isReadOnlySafeSlashCommand = useCallback(
    (value: string) => READ_ONLY_SLASH_COMMANDS.has(getBuiltInSlashCommandName(value)),
    [getBuiltInSlashCommandName],
  );

  const isExactBuiltInSlashCommand = useCallback(
    (value: string) => {
      const trimmed = value.trim();
      const match = /^\/([\w-]+)$/.exec(trimmed);
      return !!match && getBuiltInSlashCommandName(trimmed) === match[1];
    },
    [getBuiltInSlashCommandName],
  );

  // Load sessions when agent changes
  const loadSessions = useCallback((agentId: string) => {
    getChatSessions(agentId)
      .then((list) => setSessions(list || []))
      .catch(() => setSessions([]));
  }, []);

  useEffect(() => {
    if (!selectedAgent) return;
    loadSessions(selectedAgent);
  }, [selectedAgent, loadSessions]);

  // Live + replay subscription. Two job:
  //
  //   1. Cron-fired (and other async) plain text messages routed
  //      through the server's WebChannel — these arrive with shape
  //      { text } and get appended as a new agent bubble.
  //
  //   2. Resume-on-reload of a turn that was in flight when the user
  //      refreshed. The server replays chat_events with seq > since
  //      and then keeps the connection live for new events. These
  //      arrive with ChatStreamEvent shape ({ seq, type, data }).
  //
  // Dedupe across this connection AND the parallel POST sendChatStream
  // (both subscribe to the same hub server-side) by skipping events
  // whose seq is <= maxSeqRef.
  useEffect(() => {
    if (!selectedAgent || !sessionId) return;
    // Wait for the history fetch to land for THIS sessionId before
    // opening the SSE — see the loadedSessionId comment for the
    // browser-connection-pool failure mode this prevents.
    if (loadedSessionId !== sessionId) return;
    const since = subscribeSinceRef.current;
    const url = `/api/chat/subscribe?agentId=${encodeURIComponent(selectedAgent)}&sessionId=${encodeURIComponent(sessionId)}&since=${since}`;
    const es = new EventSource(url, { withCredentials: true });
    es.onmessage = (ev) => {
      let data: {
        seq?: number;
        type?: string;
        text?: string;
        data?: {
          content?: string;
          message?: string;
          metadata?: ToolResultMetadata;
          // subagent_progress fields
          iteration?: number;
          max?: number;
          phase?: "thinking" | "running" | "final-delivery" | "done";
          tools?: string[];
        };
      };
      try {
        data = JSON.parse(ev.data);
      } catch {
        return;
      }
      // Shape A: ChatStreamEvent (in-flight turn deltas).
      if (typeof data.type === "string") {
        const seq = typeof data.seq === "number" ? data.seq : -1;
        if (seq >= 0 && seq <= maxSeqRef.current) return; // already rendered via POST stream
        // While the foreground POST /api/chat/stream is active for this
        // session, let that callback own rendering. Otherwise this
        // parallel subscribe connection can win the race, create a
        // transient slash bubble, then reload history on `done`; slash
        // replies are event-only, so that reload clears the visible
        // answer and makes the send look like it did nothing.
        if (inFlightSendSessionRef.current === sessionId) return;
        // CAREFUL: do NOT bump maxSeqRef before the switch. This handler
        // intentionally drops tool_call / tool_result during catch-up
        // (the post-`done` history reload renders them properly) — but
        // a pre-switch bump would mark those seqs as "rendered" and the
        // parallel POST sendChatStream callback would dedup-skip the
        // very same events when it tries to actually render them. Bump
        // only inside cases that really took ownership of this seq.
        const claim = () => {
          if (seq >= 0) maxSeqRef.current = seq;
        };
        switch (data.type) {
          case "content": {
            const content = data.data?.content || "";
            const meta = data.data?.metadata;
            if (!content && !meta) break;
            // The active POST sendChatStream is rendering this turn
            // via content_delta into streamingMsgIdRef. Both
            // subscriptions sit on the same hub, so the `content`
            // event reaches both handlers; if subscribe wins the
            // race it would create a duplicate transient bubble
            // before the POST callback can dedup. Bail when the
            // POST handler is mid-stream — it will seal the bubble
            // itself when it processes the event.
            //
            // Metadata-only events (no content) still go through so
            // a forced-final-delivery retro-stamp lands.
            if (streamingMsgIdRef.current && content) {
              claim();
              break;
            }
            claim();
            setMessages((prev) => {
              if (transientBubbleIdRef.current) {
                const idx = prev.findIndex((m) => m.id === transientBubbleIdRef.current);
                if (idx >= 0) {
                  const updated = [...prev];
                  updated[idx] = {
                    ...updated[idx],
                    content: (updated[idx].content || "") + content,
                    metadata: meta ? { ...updated[idx].metadata, ...meta } : updated[idx].metadata,
                  };
                  return updated;
                }
              }
              // Metadata-only retro-stamp event with no transient
              // bubble: attach to the most recent agent bubble so the
              // badge sticks across an active subscribe session.
              if (!content && meta) {
                for (let i = prev.length - 1; i >= 0; i--) {
                  if (prev[i].role === "agent") {
                    const updated = [...prev];
                    updated[i] = { ...updated[i], metadata: { ...updated[i].metadata, ...meta } };
                    return updated;
                  }
                }
                return prev;
              }
              const id = `resume-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
              transientBubbleIdRef.current = id;
              return [...prev, { id, role: "agent", content, timestamp: Date.now(), metadata: meta }];
            });
            break;
          }
          case "error": {
            claim();
            const msg = data.data?.message || tr("Unknown error", "未知错误");
            // Older gateways persisted cancellation as an error event.
            // Ignore those on replay so Stop produces one clear status
            // instead of "(Stopped)" followed by a stale error bubble.
            if (/\bcontext canceled\b/i.test(msg)) break;
            setMessages((prev) => [
              ...prev,
              { id: `e-${Date.now()}`, role: "agent", content: `Error: ${msg}`, timestamp: Date.now() },
            ]);
            break;
          }
          case "subagent_progress": {
            claim();
            if (data.data?.phase === "done") {
              setSubagentProgress(null);
            } else {
              setSubagentProgress({
                iteration: data.data?.iteration,
                max: data.data?.max,
                phase: data.data?.phase,
                tools: data.data?.tools,
              });
            }
            break;
          }
          case "steer": {
            claim();
            applySteerEvent(data.data?.content || "");
            break;
          }
          case "done": {
            claim();
            // Defensive clear — content events should already have
            // sealed the streaming bubble, but a turn that errors out
            // before the trailing `content` event lands would leave
            // the ref dangling and cause the next turn's first
            // content_delta to write into the stale id.
            streamingMsgIdRef.current = null;
            // Only reload history when we actually built a transient
            // bubble from subscribe-replayed content events (i.e. the
            // user reloaded mid-turn and we need to swap the
            // placeholder for the canonical message saved in
            // session_messages). When the active POST stream rendered
            // the turn directly, transient bubble is null — a reload
            // here would clobber any rendered error bubbles too,
            // because LLM-error turns never write an assistant
            // message to session_messages.
            if (transientBubbleIdRef.current) {
              transientBubbleIdRef.current = null;
              getChatHistoryWithCursor(selectedAgent, sessionId, { limit: CHAT_HISTORY_PAGE_SIZE })
                .then(({ history, latestEventSeq, historyStart, hasMoreHistory }) => {
                  if (latestEventSeq > maxSeqRef.current) maxSeqRef.current = latestEventSeq;
                  subscribeSinceRef.current = latestEventSeq;
                  historyStartRef.current = historyStart;
                  setHasOlderHistory(hasMoreHistory);
                  setMessages(buildChatMessages(history, historyStart));
                })
                .catch(() => {});
            }
            // Tell the sidebar to refresh — the new turn may have
            // produced an updated session title.
            if (typeof window !== "undefined") {
              window.dispatchEvent(
                new CustomEvent("fastclaw:sessions-changed", {
                  detail: { agentId: selectedAgent },
                }),
              );
            }
            break;
          }
          // tool_call / tool_result during catch-up are skipped here —
          // the next history reload (on `done`) will render them
          // properly via buildChatMessages.
        }
        return;
      }
      // Shape B: legacy WebChannel { text } — cron-fired async messages.
      const text = data.text || "";
      if (!text) return;
      setMessages((prev) => [
        ...prev,
        {
          id: `async-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
          role: "agent",
          content: text,
          timestamp: Date.now(),
        },
      ]);
    };
    es.onerror = () => {
      // EventSource auto-reconnects on transient errors; only close on
      // unmount. A persistent 404 (session removed, agent gone) will
      // keep flapping but is harmless.
    };
    return () => {
      es.close();
    };
  }, [selectedAgent, sessionId, loadedSessionId, tr]);

  // Reactively swap sessionId when the URL changes underneath us.
  // Three URL transitions matter, all handled by the same branch logic:
  //   - /chat/A → /chat/B               : adopt the new session id
  //   - /chat/A → /chat/ or /project/P  : mint a fresh id, clear messages
  //   - /chat/  → /chat/A               : adopt the session id
  // `prevHadSessionRef` keeps the initial mount from re-minting on top
  // of the id useState() already picked, AND keeps two consecutive
  // no-session URLs (e.g. /chat/ → /project/P/) from churning the id.
  // Messages are cleared on any id change so the in-place swap doesn't
  // briefly show the previous chat's content while the new history
  // fetch is in flight.
  const prevHadSessionRef = useRef(false);
  useEffect(() => {
    if (routeSessionId) {
      prevHadSessionRef.current = true;
      if (routeSessionId !== sessionId) {
        resetTodoScope(selectedAgent, routeSessionId);
        setSessionId(routeSessionId);
        setMessages([]);
      }
      return;
    }
    // `/chats` with no selected session only replaces the right-side panel.
    // Do not mint a new central conversation while that route is active.
    if (isChatsPage) return;
    if (prevHadSessionRef.current) {
      prevHadSessionRef.current = false;
      const nextSessionId = generateSessionId();
      resetTodoScope(selectedAgent, nextSessionId);
      setSessionId(nextSessionId);
      setMessages([]);
    }
  }, [isChatsPage, routeSessionId, sessionId, selectedAgent, resetTodoScope]);

  // Switching conversations (sidebar chat click, New chat, opening a project)
  // changes the URL ids — close the workspace panel so the previous chat's
  // files don't linger over a different conversation. Keyed on the URL ids,
  // not every render, so the user can still re-open it within the SAME chat.
  useEffect(() => {
    setFilesSheetOpen(false);
  }, [routeSessionId, urlProjectId]);

  // Channel of the currently-open session, derived from the sessions
  // list. Brand-new web chats don't have a row yet — the fallback to
  // "web" keeps composing enabled for them. IM sessions get a banner +
  // disabled input because composing here would write to the agent's
  // session but never reach the upstream messenger.
  const currentChannel = useMemo<string>(() => {
    const s = sessions.find((x) => x.id === sessionId);
    return s?.channel || "web";
  }, [sessions, sessionId]);
  // Read-only channel / actAs views keep the textarea editable so the
  // user can type local query slashes such as /usage, while the send
  // gate below blocks normal messages and mutating slash commands.
  const isReadOnlyChannel = currentChannel !== "web";
  const isReadOnlyView = isReadOnlyChannel || isActAsView;
  const inputIsReadOnlySafeSlashCommand = isReadOnlySafeSlashCommand(input);
  const canUseComposer = !!selectedAgent && !isConversationLoading;
  const canSendComposer =
    canUseComposer && (!isReadOnlyView || inputIsReadOnlySafeSlashCommand);
  const canAttach = !!selectedAgent && !sending && !isReadOnlyView;

  const openBotSettings = useCallback(() => {
    if (typeof window === "undefined" || !selectedAgent) return;
    window.dispatchEvent(
      new CustomEvent("fastclaw:open-agent-settings", {
        detail: { agentId: selectedAgent },
      }),
    );
  }, [selectedAgent]);

  const openBotSettingsTab = useCallback((tab: AgentSettingsTab, userOnly = false) => {
    if (typeof window === "undefined" || !selectedAgent) return;
    window.dispatchEvent(
      new CustomEvent("fastclaw:open-agent-settings", {
        detail: { agentId: selectedAgent, tab, userOnly },
      }),
    );
  }, [selectedAgent]);

  const handleAgentPublicChange = useCallback((isPublic: boolean) => {
    setAgentDetail((current) => current ? { ...current, isPublic } : current);
  }, []);

  // Keep legacy/direct /chats URLs closable even though the in-panel recent
  // list now expands in place and no longer navigates there.
  const fallbackChatHref = useMemo(() => {
    if (!selectedAgent) return "/agents";
    const activeSessionExists = sessions.some((topic) => topic.id === sessionId);
    return activeSessionExists
      ? `/agents/${encodeURIComponent(selectedAgent)}/chat/${encodeURIComponent(sessionId)}/`
      : `/agents/${encodeURIComponent(selectedAgent)}/chat/`;
  }, [selectedAgent, sessionId, sessions]);
  const leaveChatsPage = useCallback((keepAgentPanelOpen: boolean) => {
    if (typeof window === "undefined") return;
    setBotPanelOpen(keepAgentPanelOpen);
    window.history.replaceState(null, "", fallbackChatHref);
  }, [fallbackChatHref]);
  const navigationPanelOpen = botPanelOpen || isChatsPage;

  // Conversation header follows the compact Bot pattern: identity opens
  // settings, while the monitor action opens the live workspace.
  const headerSlot = useMemo(
    () => (
      <div className="flex h-full min-w-0 flex-1 items-center gap-3 px-4 md:px-5">
        <button
          type="button"
          onClick={openBotSettings}
          className="group flex min-w-0 max-w-[min(60vw,32rem)] items-center gap-3 rounded-xl px-1.5 py-1 transition-colors hover:bg-muted/60 focus-visible:ring-2 focus-visible:ring-ring"
          title={tr("Open settings for {{agent}}", "打开 {{agent}} 的设置", { agent: agentName || selectedAgent })}
          aria-label={tr("Open settings for {{agent}}", "打开 {{agent}} 的设置", { agent: agentName || selectedAgent })}
        >
          <BotAvatar
            agentId={selectedAgent}
            avatarUrl={agentDetail?.avatarUrl}
            size={28}
          />
          <span className="truncate text-sm font-semibold text-foreground">
            {agentName || selectedAgent}
          </span>
        </button>
        <ShareAgentMenu
          agentId={selectedAgent}
          agentName={agentName || selectedAgent}
          isPublic={agentDetail?.isPublic === true}
          canChangeVisibility={agentDetail?.role === "owner" && !isActAsView}
          onPublicChange={handleAgentPublicChange}
        />
        <button
          type="button"
          onClick={() => {
            setFilesSheetOpen(false);
            setKnowledgePreview(null);
            setWorkspaceReturnsToBotPanel(false);
            if (isChatsPage) {
              leaveChatsPage(false);
              return;
            }
            setBotPanelOpen((value) => !value);
          }}
          className={`inline-flex size-9 shrink-0 items-center justify-center rounded-xl transition-colors ${
            navigationPanelOpen
              ? "bg-muted text-foreground"
              : "text-muted-foreground hover:bg-muted/70 hover:text-foreground"
          }`}
          title={navigationPanelOpen ? tr("Close navigation panel", "关闭导航侧栏") : tr("Open projects and recent chats", "打开项目和最近话题")}
          aria-label={navigationPanelOpen ? tr("Close navigation panel", "关闭导航侧栏") : tr("Open projects and recent chats", "打开项目和最近话题")}
          aria-pressed={navigationPanelOpen}
        >
          <PanelRight className="size-[18px]" />
        </button>
      </div>
    ),
    [
      agentName,
      selectedAgent,
      agentDetail?.avatarUrl,
      agentDetail?.isPublic,
      agentDetail?.role,
      isActAsView,
      openBotSettings,
      handleAgentPublicChange,
      isChatsPage,
      leaveChatsPage,
      navigationPanelOpen,
      botPanelOpen,
      tr,
    ],
  );
  usePageHeader(headerSlot, [headerSlot]);

  // Load history when session changes. After rebuilding messages from
  // server history we also re-attach this session's workspace files to
  // the trailing assistant bubble so the "Your files" panel survives a
  // refresh — server history doesn't carry per-turn file diffs, so we
  // approximate by listing everything under sessions/<sid>/ once and
  // hanging it off the last agent message.
  useEffect(() => {
    if (!selectedAgent || !sessionId) return;
    const sessionHasActivePost = inFlightSendSessionRef.current === sessionId;
    activeHistoryScopeRef.current = { agentId: selectedAgent, sessionId };
    historyStartRef.current = 0;
    historyLoadingOlderRef.current = false;
    pendingPrependScrollRef.current = null;
    pendingInitialScrollRef.current = sessionId;
    stickToBottomRef.current = true;
    setHasOlderHistory(false);
    // Reset dedup state when session changes — events from a previous
    // session must not bias the new session's seq filter, and any
    // transient placeholder is no longer relevant.
    maxSeqRef.current = -1;
    subscribeSinceRef.current = -1;
    transientBubbleIdRef.current = null;
    // Close the SSE gate for this sessionId; reopens once the history
    // fetch lands and subscribeSinceRef has been set to the real cursor.
    setLoadedSessionId(null);
    // Reset the todo panel on session change and invalidate every request
    // started for the previous conversation before loading this scope.
    resetTodoScope(selectedAgent, sessionId);
    // Same for the subagent progress indicator — never carry it across
    // sessions; a fresh load means no in-flight delegate_task to track.
    setSubagentProgress(null);
    // Refresh todo.md alongside the history fetch. We don't gate the
    // rest of the load on it — a 404 (no todo.md yet) is the normal
    // empty-session case.
    refreshTodoForScope(selectedAgent, sessionId);
    let aborted = false;
    getChatHistoryWithCursor(selectedAgent, sessionId, { limit: CHAT_HISTORY_PAGE_SIZE })
      .then(async ({ history, latestEventSeq, historyStart, hasMoreHistory }) => {
        if (aborted) return;
        if (!sessionHasActivePost && latestEventSeq > maxSeqRef.current) {
          maxSeqRef.current = latestEventSeq;
        }
        subscribeSinceRef.current = latestEventSeq;
        historyStartRef.current = historyStart;
        setHasOlderHistory(hasMoreHistory);
        if (!history || history.length === 0) {
          if (!sessionHasActivePost) {
            setMessages([]);
          }
          setLoadedSessionId(sessionId);
          return;
        }
        const built = buildChatMessages(history, historyStart);
        try {
          // listAgentFiles(agentId, sessionId) lets the backend pick
          // the right prefix — projects/<pid>/ for project chats,
          // sessions/<chat>/ for loose ones — so we don't have to
          // hard-code `sessions/<sid>/` here. The hard-coded prefix
          // missed every file in a project chat.
          const sessionFiles: ProducedFile[] = (
            await listAgentFiles(selectedAgent, sessionId)
          )
            .filter((f) => !isSystemFile(f.path))
            .map((f) => ({ path: f.path, size: f.size }));
          if (sessionFiles.length > 0) {
            for (let i = built.length - 1; i >= 0; i--) {
              if (built[i].role === "agent" || built[i].role === "tool-group") {
                built[i] = { ...built[i], files: sessionFiles };
                break;
              }
            }
          }
        } catch { /* listing failed — fall back to no panel */ }
        if (aborted) return;
        setMessages(built);
        setLoadedSessionId(sessionId);
      })
      .catch(() => {
        if (aborted) return;
        historyStartRef.current = 0;
        setHasOlderHistory(false);
        if (!sessionHasActivePost) {
          setMessages([]);
        }
        // History fetch failed — open the SSE anyway so live events
        // still flow, but use seq=0 instead of -1 so we don't trigger a
        // full server-side replay as a side effect.
        subscribeSinceRef.current = 0;
        setLoadedSessionId(sessionId);
      });
    return () => {
      aborted = true;
    };
  }, [selectedAgent, sessionId, refreshTodoForScope, resetTodoScope]);

  const updateMessageScrollState = useCallback(() => {
    const el = messagesScrollRef.current;
    if (!el) return;
    const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
    const next = {
      overflow: el.scrollHeight > el.clientHeight + 1 || hasOlderHistory,
      canScrollUp: el.scrollTop > 8 || hasOlderHistory,
      canScrollDown: distanceFromBottom > 8,
    };
    setMessageScrollState((current) =>
      current.overflow === next.overflow
      && current.canScrollUp === next.canScrollUp
      && current.canScrollDown === next.canScrollDown
        ? current
        : next,
    );
  }, [hasOlderHistory]);

  const loadOlderHistory = useCallback(async () => {
    if (
      !selectedAgent
      || !sessionId
      || !hasOlderHistory
      || historyStartRef.current <= 0
      || historyLoadingOlderRef.current
    ) {
      return;
    }
    historyLoadingOlderRef.current = true;
    const requestScope = { agentId: selectedAgent, sessionId };
    try {
      const result = await getChatHistoryWithCursor(selectedAgent, sessionId, {
        limit: CHAT_HISTORY_PAGE_SIZE,
        before: historyStartRef.current,
      });
      const activeScope = activeHistoryScopeRef.current;
      if (
        activeScope.agentId !== requestScope.agentId
        || activeScope.sessionId !== requestScope.sessionId
      ) {
        return;
      }
      const olderMessages = buildChatMessages(result.history, result.historyStart);
      historyStartRef.current = result.historyStart;
      setHasOlderHistory(result.hasMoreHistory);
      if (olderMessages.length === 0) return;

      const el = messagesScrollRef.current;
      if (el) {
        pendingPrependScrollRef.current = {
          height: el.scrollHeight,
          top: el.scrollTop,
        };
      }
      stickToBottomRef.current = false;
      setMessages((current) => {
        const existingIds = new Set(current.map((message) => message.id));
        return [
          ...olderMessages.filter((message) => !existingIds.has(message.id)),
          ...current,
        ];
      });
    } finally {
      historyLoadingOlderRef.current = false;
    }
  }, [hasOlderHistory, selectedAgent, sessionId]);

  // Perform all automatic positioning before paint. Entering a long
  // conversation therefore opens on the newest message immediately instead
  // of visibly animating the scrollbar from the top. Prepending an older page
  // preserves the message that was under the reader's eyes.
  useLayoutEffect(() => {
    const el = messagesScrollRef.current;
    if (!el) return;

    const restore = pendingPrependScrollRef.current;
    if (restore) {
      pendingPrependScrollRef.current = null;
      el.scrollTop = restore.top + (el.scrollHeight - restore.height);
    } else if (
      pendingInitialScrollRef.current === sessionId
      && loadedSessionId === sessionId
    ) {
      pendingInitialScrollRef.current = "";
      stickToBottomRef.current = true;
      el.scrollTop = el.scrollHeight;
    } else if (stickToBottomRef.current) {
      el.scrollTop = el.scrollHeight;
    }
    updateMessageScrollState();
  }, [loadedSessionId, messages, sessionId, updateMessageScrollState]);

  // Keep the controls accurate while the user scrolls and as Markdown,
  // images, or tool panels change the content height after rendering.
  useEffect(() => {
    const el = messagesScrollRef.current;
    const content = messagesContentRef.current;
    if (!el) return;
    const onScroll = () => {
      const distance = el.scrollHeight - el.scrollTop - el.clientHeight;
      stickToBottomRef.current = distance <= 64;
      updateMessageScrollState();
      if (el.scrollTop <= 24 && hasOlderHistory) {
        void loadOlderHistory();
      }
    };
    const resizeObserver = new ResizeObserver(() => {
      if (stickToBottomRef.current && !pendingPrependScrollRef.current) {
        el.scrollTop = el.scrollHeight;
      }
      updateMessageScrollState();
    });
    el.addEventListener("scroll", onScroll, { passive: true });
    if (content) resizeObserver.observe(content);
    updateMessageScrollState();
    return () => {
      el.removeEventListener("scroll", onScroll);
      resizeObserver.disconnect();
    };
  }, [hasOlderHistory, loadOlderHistory, updateMessageScrollState]);

  const scrollMessagesByPage = useCallback((direction: -1 | 1) => {
    const el = messagesScrollRef.current;
    if (!el) return;
    if (direction < 0 && el.scrollTop <= 8 && hasOlderHistory) {
      void loadOlderHistory();
      return;
    }
    el.scrollBy({
      top: direction * Math.max(240, el.clientHeight * 0.72),
      behavior: "smooth",
    });
  }, [hasOlderHistory, loadOlderHistory]);

  useEffect(() => {
    const el = textareaRef.current;
    if (el) {
      el.style.height = "auto";
      el.style.height = Math.min(el.scrollHeight, 180) + "px";
    }
  }, [input]);

  // applySteerEvent renders a server "steer" echo as a user bubble,
  // reconciling against the optimistic pendingSteer bubble (if any) so
  // the message isn't duplicated. seq-dedup is already applied upstream
  // by both event consumers.
  const applySteerEvent = useCallback((content: string) => {
    if (!content) return;
    setMessages((prev) => {
      const idx = prev.findIndex(
        (m) => m.role === "user" && m.pendingSteer && m.content === content,
      );
      if (idx >= 0) {
        const updated = [...prev];
        updated[idx] = { ...updated[idx], pendingSteer: undefined };
        return updated;
      }
      return [
        ...prev,
        { id: `s-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`, role: "user", content, timestamp: Date.now() },
      ];
    });
  }, []);

  const handleSend = useCallback(async (overrideText?: string, force?: boolean) => {
    // overrideText lets caller post a message that didn't come from
    // the composer (e.g. the plan-approval button clicking "go"). When
    // present it bypasses the input field entirely — composer state
    // stays as the user left it. force bypasses the in-flight guard;
    // used by the steer 409 fallback (server confirmed no active turn).
    const composerText = (overrideText ?? input).trim();
    const text = composerText;
    const slashAllowedInReadOnlyView = isReadOnlySafeSlashCommand(text);
    // Allow sending with attachments only (no text), but require at least one.
    if (
      (!text && attachments.length === 0) ||
      !selectedAgent ||
      (sending && !force) ||
      (isReadOnlyView && !slashAllowedInReadOnlyView)
    ) {
      return;
    }
    setSlashOpen(false);

    // `/project/<pid>` is the lazy-create marker the sidebar dropped
    // us at. Captured here so it can ride the first chat request body;
    // once the session row exists, project_id is on the row and the URL
    // drops back to the bare chat form.
    const projectIdHint = urlProjectId;

    // Pin the sessionId into the URL on the first send so a refresh
    // keeps the user in the same conversation. We use the native
    // History API instead of `router.replace` because output:'export'
    // only pre-renders the `_` placeholder for /chat/[session]; a
    // router.replace to the real sid triggers an RSC fetch that the
    // SPA fallback can't satisfy, and Next falls back to a hard
    // window.location navigation — which kills the in-flight stream
    // we're about to start. Next 16's app-router patches
    // history.replaceState to dispatch ACTION_RESTORE, so usePathname /
    // useSearchParams (and the sidebar's navigateOnce dedupe that
    // derives from them) still see the new URL.
    const target = `/agents/${selectedAgent}/chat/${sessionId}/`;
    inFlightSendSessionRef.current = sessionId;
    if (pathname !== target) {
      window.history.replaceState(null, "", target);
    }

    // Upload attachments first so the agent can read them by name on its
    // first turn. Files land at sessions/<sid>/<basename> in the workspace
    // store, which is the same dir the docker sandbox bind-mounts as
    // /workspace. We also (a) build user-bubble preview metadata so images
    // render inline in the user's bubble without waiting on the server
    // round-trip, and (b) read images as data URLs so vision-capable
    // models receive them as image_url content parts.
    const filesToUpload = attachments;
    setAttachments([]);

    let userBubbleAttachments: UserAttachment[] = [];
    let imageDataUrls: string[] = [];

    if (filesToUpload.length > 0) {
      userBubbleAttachments = filesToUpload.map((f) => ({
        name: f.name,
        isImage: f.type.startsWith("image/"),
        previewUrl: f.type.startsWith("image/") ? URL.createObjectURL(f) : undefined,
      }));

      try {
        await uploadAgentFiles(selectedAgent, sessionId, filesToUpload);
      } catch (err) {
        setMessages((prev) => [
          ...prev,
          { id: `e-${Date.now()}`, role: "agent", content: `File upload failed: ${err instanceof Error ? err.message : "unknown error"}`, timestamp: Date.now() },
        ]);
        return;
      }

      // Read each image as a base64 data URL. We do this AFTER upload —
      // upload only needs the File object; data URL conversion is for the
      // provider call. Done in parallel for snappy UX on multi-attach.
      imageDataUrls = (
        await Promise.all(
          filesToUpload.map(async (f) => {
            if (!f.type.startsWith("image/")) return null;
            return await new Promise<string | null>((resolve) => {
              const reader = new FileReader();
              reader.onload = () => resolve(typeof reader.result === "string" ? reader.result : null);
              reader.onerror = () => resolve(null);
              reader.readAsDataURL(f);
            });
          }),
        )
      ).filter((s): s is string => !!s);
    }
    // Build the prompt actually sent to the model. Images travel as
    // `imageUrls` for vision, but the model also needs the on-disk path
    // for skills like image-tool that take `input: "/workspace/<file>"`.
    // We prepend `[Attached: /workspace/<name>]` lines for that — the
    // server's StripAttachedPrefix removes them on history read so user
    // bubbles, page titles, and sidebar previews stay clean.
    const attachedPaths = filesToUpload.map((f) => `/workspace/${f.name}`);
    const breadcrumb = attachedPaths
      .map((p) => `[Attached: ${p}]`)
      .join("\n");
    const fullText = breadcrumb
      ? (text ? `${breadcrumb}\n${text}` : breadcrumb)
      : text;

    // Only clear the composer when the send came from it. Override
    // sends (plan-approval button etc.) leave whatever the user was
    // typing alone.
    if (overrideText === undefined) {
      setInput("");
    }
    // Sending always means "I want to see what happens next" — re-pin
    // to bottom even if the user had scrolled up to read earlier in the
    // conversation.
    stickToBottomRef.current = true;
    setMessages((prev) => [
      ...prev,
      {
        id: `u-${Date.now()}`,
        role: "user",
        content: text, // bubble shows text only; attachments rendered separately above
        timestamp: Date.now(),
        attachments: userBubbleAttachments.length > 0 ? userBubbleAttachments : undefined,
      },
    ]);
    setSending(true);
    abortRef.current = new AbortController();

    // Snapshot the workspace before the turn so we can diff at `done` and
    // attach newly-created / modified files (PDFs, images, …) to the
    // final reply. Fire-and-forget; if the snapshot fails we just won't
    // surface files this turn. `path → size|modTime` key.
    const preTurnFilesPromise = listAgentFiles(selectedAgent)
      .then((items) => {
        const m = new Map<string, string>();
        for (const f of items) m.set(f.path, `${f.size}|${f.modTime}`);
        return m;
      })
      .catch(() => new Map<string, string>());

    let curGroupId = "";
    let curCalls: { id: string; name: string; arguments: string; result?: string; metadata?: ToolResultMetadata }[] = [];
    let curContent = "";
    // streamingMsgIdRef tracks the in-flight assistant bubble for
    // content_delta accretion. Stored on a ref (declared above) so
    // the parallel /api/chat/subscribe SSE handler can observe it
    // and skip duplicating the bubble when the trailing `content`
    // event races through it ahead of the POST callback. Reset to
    // null at startNewGroup, after the `content` seal, and on
    // tool_call / done.
    const turnFiles: ProducedFile[] = [];
    const seenPaths = new Set<string>();

    const startNewGroup = () => {
      curGroupId = `tg-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`;
      curCalls = [];
      curContent = "";
      streamingMsgIdRef.current = null;
    };
    startNewGroup();

    try {
      await sendChatStream(selectedAgent, sessionId, fullText, (evt: ChatStreamEvent) => {
        // Dedup against /api/chat/subscribe SSE, which subscribes to
        // the same chat-events hub server-side. Whichever path arrives
        // first renders; the other skips. seq < 0 means persistence
        // failed for this event — fall through and accept the
        // possibility of a double-render rather than dropping the
        // event entirely.
        if (typeof evt.seq === "number" && evt.seq >= 0) {
          if (evt.seq <= maxSeqRef.current) return;
          maxSeqRef.current = evt.seq;
        }
        switch (evt.type) {
          case "content_delta": {
            // Incremental token chunk from the provider. Append to the
            // in-flight assistant bubble — create one on the first
            // delta of a round (and after a tool-group split). The
            // final `content` event still arrives with the full text
            // when the turn completes so refresh / replay paths stay
            // intact even though deltas aren't persisted.
            const delta = evt.data?.delta || "";
            if (!delta) break;
            if (curCalls.length > 0 && !streamingMsgIdRef.current) {
              // Content after tool calls = new round; reset state so
              // the new bubble is its own message, not appended onto
              // the previous tool-group's thinking text.
              startNewGroup();
            }
            curContent += delta;
            if (!streamingMsgIdRef.current) {
              const id = `a-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`;
              streamingMsgIdRef.current = id;
              setMessages((prev) => [
                ...prev,
                { id, role: "agent", content: delta, timestamp: Date.now() },
              ]);
            } else {
              const id = streamingMsgIdRef.current;
              setMessages((prev) => {
                const idx = prev.findIndex((m) => m.id === id);
                if (idx < 0) return prev;
                const updated = [...prev];
                updated[idx] = { ...updated[idx], content: (updated[idx].content || "") + delta };
                return updated;
              });
            }
            break;
          }
          case "content": {
            const content = evt.data?.content || "";
            const meta = evt.data?.metadata;
            if (content === "__NEW_SESSION__") {
              handleNewChat();
              loadSessions(selectedAgent);
              return;
            }
            if (!content && !meta) {
              break;
            }
            // If the bubble was already streamed in via content_delta,
            // the final `content` carries the same text — just seal
            // the in-flight ID, optionally attach metadata, and skip
            // creating a duplicate bubble.
            if (streamingMsgIdRef.current) {
              const id = streamingMsgIdRef.current;
              streamingMsgIdRef.current = null;
              if (meta) {
                setMessages((prev) => {
                  const idx = prev.findIndex((m) => m.id === id);
                  if (idx < 0) return prev;
                  const updated = [...prev];
                  updated[idx] = { ...updated[idx], metadata: { ...updated[idx].metadata, ...meta } };
                  return updated;
                });
              }
              curContent = content;
              break;
            }
            // Metadata-only event with empty content: the backend uses
            // this to retro-stamp the previous bubble (e.g. for the
            // streaming forced-final-delivery path where chunks flow
            // through a separate channel and the metadata follows after).
            // Apply to the last agent message instead of creating an
            // empty new bubble.
            if (!content && meta) {
              setMessages((prev) => {
                for (let i = prev.length - 1; i >= 0; i--) {
                  if (prev[i].role === "agent") {
                    const updated = [...prev];
                    updated[i] = { ...updated[i], metadata: { ...updated[i].metadata, ...meta } };
                    return updated;
                  }
                }
                return prev;
              });
              break;
            }
            if (curCalls.length > 0) {
              // Content after tool calls = new round. Finalize current group, start fresh.
              startNewGroup();
            }
            // Store as thinking content (may become part of next tool-group, or stay as final answer)
            curContent = content;
            setMessages((prev) => [
              ...prev,
              { id: `a-${Date.now()}`, role: "agent", content, timestamp: Date.now(), metadata: meta },
            ]);
            break;
          }
          case "tool_call": {
            // The in-flight streamed bubble (if any) is about to be
            // converted into a tool-group by the existing "replace
            // last agent message" logic below. Clear the streaming
            // ID so a subsequent content_delta on the next round
            // spawns a fresh bubble instead of writing into the
            // now-defunct ID.
            streamingMsgIdRef.current = null;
            // New round starts if every tool in the current group has
            // already resolved. Without this, two assistant turns that
            // happen back-to-back with no intervening content event get
            // merged into one visual group live — inconsistent with the
            // refresh path (buildChatMessages) which correctly splits
            // per assistant message.
            if (curCalls.length > 0 && curCalls.every((c) => c.result !== undefined)) {
              startNewGroup();
            }
            curCalls.push({
              id: evt.data?.id || "",
              name: evt.data?.name || "",
              arguments: evt.data?.arguments || "{}",
            });
            const groupId = curGroupId;
            const calls = [...curCalls];
            setMessages((prev) => {
              // Update existing tool-group for this round (additional
              // tool_call within the same assistant turn).
              const idx = prev.findIndex((m) => m.id === groupId);
              if (idx >= 0) {
                const updated = [...prev];
                updated[idx] = { ...updated[idx], toolCalls: calls };
                return updated;
              }
              // Leave any streamed agent bubble in place — don't fold
              // its text into the tool-group. Mirrors the split applied
              // in buildChatMessages on history reload, so live and
              // reloaded views stay consistent.
              return [
                ...prev,
                { id: groupId, role: "tool-group" as const, content: "", timestamp: Date.now(), toolCalls: calls },
              ];
            });
            break;
          }
          case "tool_result": {
            const tc = curCalls.find((c) => c.id === (evt.data?.id || ""));
            const resultText = evt.data?.result || "";
            if (tc) {
              tc.result = resultText;
              if (evt.data?.metadata) tc.metadata = evt.data.metadata;
            }
            // Track successful write_file calls that landed in the workspace
            // (i.e. a relative path that isn't a system identity file).
            if (tc && tc.name === "write_file" && /^Written \d+ bytes/.test(resultText)) {
              try {
                const args = JSON.parse(tc.arguments);
                const p: string = typeof args?.path === "string" ? args.path : "";
                if (p && !p.startsWith("/") && !isSystemFile(p) && !seenPaths.has(p)) {
                  seenPaths.add(p);
                  turnFiles.push({ path: p, size: parseWrittenSize(resultText) });
                }
              } catch { /* ignore bad args */ }
            }
            // Refresh the todo panel whenever a file-mutation tool just
            // touched todo.md. We inspect arguments rather than poll on
            // every tool_result so the network cost stays proportional
            // to actual updates (a long run with 50 web_search calls
            // doesn't trigger 50 refetches).
            if (tc && toolMutationTouchesTodo(tc.name, tc.arguments)) {
              refreshTodoForScope(selectedAgent, sessionId);
            }
            const groupId = curGroupId;
            const calls = [...curCalls];
            setMessages((prev) => {
              const idx = prev.findIndex((m) => m.id === groupId);
              if (idx < 0) return prev;
              const updated = [...prev];
              updated[idx] = { ...updated[idx], toolCalls: calls };
              return updated;
            });
            break;
          }
          case "subagent_progress": {
            // Subagent emitted a heartbeat. Stored as a single
            // "current run state" since delegate_task is registered
            // serial — only one subagent in flight at any time.
            // phase="done" clears the indicator.
            if (evt.data?.phase === "done") {
              setSubagentProgress(null);
            } else {
              setSubagentProgress({
                iteration: evt.data?.iteration,
                max: evt.data?.max,
                phase: evt.data?.phase,
                tools: evt.data?.tools,
              });
            }
            break;
          }
          case "steer": {
            // A message the user injected mid-turn was folded into the
            // running turn server-side. Render it as a user bubble
            // (reconciled against the optimistic pendingSteer bubble).
            applySteerEvent(evt.data?.content || "");
            break;
          }
          case "error": {
            // Surface backend errors as a chat bubble. Without this the
            // turn just hangs — the model failed (provider 4xx/5xx,
            // serialization mismatch, etc.) and the only signal was a
            // gateway log line the user can't see.
            const msg = evt.data?.message || tr("Unknown error", "未知错误");
            if (/\bcontext canceled\b/i.test(msg)) break;
            setMessages((prev) => [
              ...prev,
              { id: `e-${Date.now()}`, role: "agent", content: tr("Error: {{error}}", "错误：{{error}}", { error: msg }), timestamp: Date.now() },
            ]);
            break;
          }
        }
      }, abortRef.current.signal, imageDataUrls, projectIdHint);
      // Diff the workspace against the pre-turn snapshot so files
      // produced by *exec* (e.g. a Python script that saves PDFs) get
      // surfaced too — `turnFiles` only catches write_file tool calls
      // with relative, non-identity paths, which misses most real-
      // world flows. Union both sources by path.
      const postTurnFiles = await listAgentFiles(selectedAgent).catch(() => []);
      const preSnap = await preTurnFilesPromise;
      const diffFiles: ProducedFile[] = [];
      for (const f of postTurnFiles) {
        if (isSystemFile(f.path)) continue;
        const key = `${f.size}|${f.modTime}`;
        if (preSnap.get(f.path) === key) continue; // unchanged
        if (seenPaths.has(f.path)) continue;
        diffFiles.push({ path: f.path, size: f.size });
      }
      const allFiles = [...turnFiles, ...diffFiles];
      // Diagnostic: when sandbox-exec produces a file but the Files
      // panel doesn't show, we need to know whether the API returned
      // the file at all and where the diff dropped it. Cheap to keep.
      if (typeof console !== "undefined") {
        console.log("[chat] post-turn files diff", {
          agent: selectedAgent,
          sessionId,
          preSnapSize: preSnap.size,
          postTurnCount: postTurnFiles.length,
          postTurnPaths: postTurnFiles.map((f) => f.path),
          turnFiles,
          diffFiles,
          attached: allFiles.length,
        });
      }
      if (allFiles.length > 0) {
        setMessages((prev) => {
          if (prev.length === 0) return prev;
          const updated = [...prev];
          const last = updated[updated.length - 1];
          updated[updated.length - 1] = { ...last, files: allFiles };
          if (typeof console !== "undefined") {
            console.log("[chat] attached files to last message", {
              lastId: last.id,
              lastRole: last.role,
              files: allFiles,
            });
          }
          return updated;
        });
      }
      loadSessions(selectedAgent);
      // First-turn of a brand-new session just got persisted — tell the
      // global sidebar to refetch its Chats list so the new title shows
      // up without a full page reload.
      if (typeof window !== "undefined") {
        window.dispatchEvent(
          new CustomEvent("fastclaw:sessions-changed", {
            detail: { agentId: selectedAgent },
          }),
        );
      }
    } catch (err) {
      // AbortError from the user clicking Stop is expected — surface a
      // brief "Stopped" line so they see the cancellation took effect,
      // not a generic failure message.
      const isAbort = err instanceof DOMException && err.name === "AbortError";
      // Surface the underlying error in DevTools so future "Failed to
      // get a response" reports come with a concrete cause (network,
      // parse, post-turn fetch, …) rather than the generic message.
      if (typeof console !== "undefined") {
        console.error("[chat] handleSend error", err);
      }
      // Keyboard-stack abort + post-stream tear-down can both throw an
      // AbortError after a successful turn (the SSE reader is
      // released on `done`, then a stray reader.cancel() races with a
      // late server EOF and surfaces as one). Both look identical to
      // user-pressed-Stop here, so we additionally suppress the
      // toast when at least one agent reply already landed for this
      // turn — the user just got their answer; we shouldn't tack on
      // a confusing failure bubble.
      if (isAbort) {
        // Resolve any in-flight tools in the current tool-group so they
        // stop spinning. Server-side padOrphanToolResults will write a
        // matching record on its end; this just keeps the UI consistent
        // until the next history fetch overwrites it.
        setMessages((prev) =>
          prev.map((m) =>
            m.role === "tool-group" && m.toolCalls
              ? {
                  ...m,
                  toolCalls: m.toolCalls.map((tc) =>
                    tc.result === undefined ? { ...tc, result: tr("(stopped)", "（已停止）") } : tc,
                  ),
                }
              : m,
          ),
        );
        setMessages((prev) => [
          ...prev,
          { id: `e-${Date.now()}`, role: "agent", content: tr("(Stopped)", "（已停止）"), timestamp: Date.now() },
        ]);
      } else {
        setMessages((prev) => {
          const lastUser = [...prev].reverse().findIndex((m) => m.role === "user");
          if (lastUser >= 0) {
            const replyAfter = prev
              .slice(prev.length - lastUser)
              .some((m) => m.role === "agent" || m.role === "tool-group");
            if (replyAfter) return prev; // turn already produced output
          }
          const errMsg = err instanceof Error && err.message
            ? err.message
            : tr("Failed to get a response. Is the gateway running?", "获取回复失败，请确认网关是否正在运行。")
          return [
            ...prev,
            {
              id: `e-${Date.now()}`,
              role: "agent",
              content: errMsg,
              timestamp: Date.now(),
            },
          ];
        });
      }
    } finally {
      if (inFlightSendSessionRef.current === sessionId) {
        inFlightSendSessionRef.current = null;
      }
      abortRef.current = null;
      setSending(false);
      // Belt-and-suspenders: the subagent's done event clears this on
      // the happy path, but if a network blip drops that event we don't
      // want a stale "iteration 5/20" sitting under a finished turn.
      setSubagentProgress(null);
      textareaRef.current?.focus();
    }
  }, [input, attachments, selectedAgent, sessionId, sending, isReadOnlyView, isReadOnlySafeSlashCommand, loadSessions, pathname, refreshTodoForScope, router, tr, urlProjectId]);

  const handleStop = useCallback(() => {
    abortRef.current?.abort();
  }, []);

  // handleSteer fires while a turn is streaming: it buffers the message
  // into the running turn (the agent folds it in between tool rounds and
  // streams a "steer" echo on the existing SSE). On 409 (no active turn
  // — the turn just ended) it falls back to a normal send so nothing is
  // lost.
  const handleSteer = useCallback(async () => {
    const text = input.trim();
    // Only ever called from handleKeyDown's `if (sending)` branch;
    // within one render React state is snapshot-consistent, so `sending`
    // is necessarily true here.
    if (!text || !selectedAgent || !sending) return;
    setInput("");
    const optimisticId = `s-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`;
    setMessages((prev) => [
      ...prev,
      { id: optimisticId, role: "user", content: text, timestamp: Date.now(), pendingSteer: true },
    ]);
    let ok = false;
    try {
      ok = await steerChat(selectedAgent, sessionId, text, urlProjectId);
    } catch (err) {
      setMessages((prev) => [
        ...prev.filter((m) => m.id !== optimisticId),
        { id: `e-${Date.now()}`, role: "agent", content: `Steer failed: ${err instanceof Error ? err.message : "unknown error"}`, timestamp: Date.now() },
      ]);
      return;
    }
    if (!ok) {
      setMessages((prev) => prev.filter((m) => m.id !== optimisticId));
      await handleSend(text, true);
    }
  }, [input, selectedAgent, sending, sessionId, urlProjectId, handleSend]);

  const handleFilePick = useCallback((e: React.ChangeEvent<HTMLInputElement>) => {
    const picked = e.target.files;
    if (!picked || picked.length === 0) return;
    // Snapshot the FileList into a stable File[] BEFORE we reset the
    // input. FileList is tied to the input element — setting value=""
    // empties it. Under StrictMode React invokes the setState updater
    // twice for purity checks; if the closure references the live
    // FileList, the second invocation sees an empty list and the state
    // ends up empty even though the user picked a file.
    const newFiles = Array.from(picked);
    e.target.value = "";
    setAttachments((prev) => [...prev, ...newFiles]);
  }, []);

  const handlePaste = useCallback((e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    if (!canAttach) return;

    // `items` is the most reliable source for screenshots, while `files`
    // covers browsers that expose clipboard files without DataTransferItems.
    const itemImages = Array.from(e.clipboardData.items)
      .filter((item) => item.kind === "file" && item.type.startsWith("image/"))
      .map((item) => item.getAsFile())
      .filter((file): file is File => file !== null);
    const clipboardImages = itemImages.length > 0
      ? itemImages
      : Array.from(e.clipboardData.files).filter((file) => file.type.startsWith("image/"));

    if (clipboardImages.length === 0) return;

    // The image is represented by the preview chips, so don't also paste
    // the browser's text/html fallback (often a huge data URL) into input.
    e.preventDefault();
    pasteIdRef.current += 1;
    const pasteId = Date.now() * 1000 + pasteIdRef.current;
    const pastedFiles = clipboardImages.map((file, index) =>
      namePastedImage(file, pasteId, index),
    );
    setAttachments((prev) => [...prev, ...pastedFiles]);
    setSlashOpen(false);
  }, [canAttach]);

  const removeAttachment = useCallback((idx: number) => {
    setAttachments((prev) => prev.filter((_, i) => i !== idx));
  }, []);

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    // Don't submit while an IME composition is active — Enter in that state
    // is the user confirming the IME candidate (e.g. pinyin → 好), not
    // sending the message. keyCode 229 also signals "composing" on some
    // browsers where isComposing isn't set.
    if (e.nativeEvent.isComposing || e.keyCode === 229) return;

    // Slash menu keyboard handling takes precedence when open: arrows move
    // the selection, Enter confirms, Escape closes without sending.
    if (slashOpen && filteredItems.length > 0 && !isExactBuiltInSlashCommand(input)) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setSlashIndex((i) => (i + 1) % filteredItems.length);
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        setSlashIndex((i) => (i - 1 + filteredItems.length) % filteredItems.length);
        return;
      }
      if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault();
        selectItem(filteredItems[slashIndex]);
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        setSlashOpen(false);
        return;
      }
    }

    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      // While a turn is streaming, Enter steers the running turn instead
      // of being blocked; otherwise it's a normal send.
      if (sending) {
        handleSteer();
      } else {
        handleSend();
      }
    }
  };

  // onChange wrapper: update input + slash menu visibility in one pass.
  const handleInputChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const next = e.target.value;
    setInput(next);
    const caret = e.target.selectionStart ?? next.length;
    const ctx = slashContext(next, caret);
    if (ctx) {
      setSlashOpen(true);
      setSlashQuery(ctx.query);
      setSlashIndex(0);
    } else {
      setSlashOpen(false);
    }
  };

  const handleCopy = (msg: ChatMessage) => {
    navigator.clipboard.writeText(msg.content);
    setCopiedId(msg.id);
    setTimeout(() => setCopiedId(null), 1500);
  };

  // handleRetry refills the composer with this message's content so the
  // user can verify/edit before resending. Deliberately not auto-sending —
  // a one-click resend that quietly discards the existing agent reply is
  // too easy to fire by accident.
  const handleRetry = (msg: ChatMessage) => {
    setInput(msg.content);
    setTimeout(() => {
      const el = textareaRef.current;
      if (el) {
        el.focus();
        const end = el.value.length;
        el.setSelectionRange(end, end);
      }
    }, 0);
  };

  const handleNewChat = () => {
    const newId = generateSessionId();
    resetTodoScope(selectedAgent, newId);
    setSessionId(newId);
    setMessages([]);
    router.replace(`/agents/${selectedAgent}/chat/`);
  };

  const handleSelectSession = (sid: string) => {
    resetTodoScope(selectedAgent, sid);
    setSessionId(sid);
    // history.replaceState (not router.replace) for the same reason as
    // handleSend: /chat/[session] is only pre-rendered for the `_`
    // placeholder under output:'export', so router-driven navigation to
    // a real sid hard-reloads. See the longer note in handleSend.
    window.history.replaceState(null, "", `/agents/${selectedAgent}/chat/${sid}/`);
  };

  const formatTime = (ts: number) =>
    new Date(ts).toLocaleTimeString(locale === "zh-CN" ? "zh-CN" : "en-US", { hour: "2-digit", minute: "2-digit" });

  // A newly created Bot opens directly in conversation mode with a local
  // welcome bubble. Existing session URLs keep their normal history-loading
  // behavior, while project landing pages retain the centered hero composer.
  const isEmpty = messages.length === 0;
  const showBotWelcome = !isConversationLoading && isEmpty && !routeSessionId && !urlProjectId;
  const showEmptyHero = !isConversationLoading && isEmpty && !showBotWelcome;
  const todoAnchorMessageId = todoItems.length > 0
    ? findTodoAnchorMessageId(messages)
    : null;
  const todoIsActive = sending && todoAnchorMessageId !== null && (() => {
    const anchorIndex = messages.findIndex((message) => message.id === todoAnchorMessageId);
    return anchorIndex >= 0
      && !messages.slice(anchorIndex + 1).some((message) => message.role === "user");
  })();
  const botWelcome = agentDetail?.description?.trim()
    ? tr(
        "Hey, I am {{name}}. {{description}} What should we start with?",
        "Hey，我是{{name}}。{{description}} 想先从哪件事开始？",
        { name: agentName || tr("your new Agent", "新来的 Agent"), description: agentDetail.description.trim() },
      )
    : tr(
        "Hey, I am {{name}}. Did you create me to focus on one job, or to be a general assistant whenever you need one?",
        "Hey，我是{{name}}。你把我创建出来，是想让我专门做好一件事，还是做一个随时待命的通用助手？",
        { name: agentName || tr("your new Agent", "新来的 Agent") },
      );
  // Compute the id of the latest agent bubble that's a pending plan
  // (numbered plan + "Reply `go` to execute" footer), only when no
  // user message has followed it. This is the single bubble that gets
  // the inline approve/cancel buttons; older plans further up the
  // history never get buttons re-rendered on them.
  const pendingPlanId: string | null = (() => {
    if (sending) return null;
    for (let i = messages.length - 1; i >= 0; i--) {
      const m = messages[i];
      if (m.role === "user") return null; // user already replied → plan is no longer pending
      if (m.role === "agent" && isPendingPlanContent(m.content)) return m.id;
    }
    return null;
  })();
  // Hero title is the same on both the bare agent home and a project
  // landing page — Manus-style "what can I do" prompt. Project pages
  // render a small info card UNDER the hero (folder + name + meta)
  // instead of taking over the headline, so users always know which
  // agent they're chatting with first.
  const heroTitle = tr("What can I do for you?", "我能为你做什么？");

  return (
    <div className="flex h-[calc(100vh-3.5rem)] flex-row bg-background">
      <div
        className={
          "relative flex flex-1 min-w-0 flex-col xl:min-w-[520px]" +
          // pb-14 (3.5rem) matches the header height we already subtracted
          // from the parent's h-[calc(100vh-3.5rem)]. Without it `justify-
          // center` centers content inside the post-header area, which
          // sits visually ~24px below the true viewport mid-line — the
          // user notices the hero + composer pair drifting low. Adding
          // an equal bottom padding biases the centered group upward by
          // half the header height so the optical centre lines up with
          // the geometric centre of the screen.
          (showEmptyHero ? " justify-center pb-14" : "")
        }
      >
      {/* Messages */}
        <div
          ref={messagesScrollRef}
          className={
            // scrollbar-gutter:stable always reserves the 4px scrollbar track
            // so message rows keep a fixed content width that lines up with the
            // composer below (which gets a matching right inset) — otherwise the
            // scrollbar shifts message edges out of alignment on a narrow panel.
            "min-h-0 px-4 [scrollbar-gutter:stable] " +
            (showEmptyHero ? "shrink-0" : "flex-1 overflow-y-auto py-4")
          }
        >
          <div
            ref={messagesContentRef}
            className={`mx-auto w-full max-w-5xl ${
              isConversationLoading ? "flex min-h-full items-center justify-center" : "space-y-3"
            }`}
          >
            {isConversationLoading && (
              <div
                role="status"
                aria-live="polite"
                className="inline-flex items-center gap-2.5 rounded-full border border-black/[0.07] bg-card px-4 py-2.5 text-sm text-muted-foreground shadow-[0_5px_20px_rgba(0,0,0,0.04)] dark:border-white/[0.09]"
              >
                <RefreshCw
                  className="size-4 animate-spin motion-reduce:animate-none"
                  aria-hidden="true"
                />
                <span>{tr("Loading conversation…", "正在加载对话…")}</span>
              </div>
            )}
            {showEmptyHero && (
              <div className="py-8 text-center">
                <h1 className="text-3xl md:text-4xl font-semibold tracking-tight">
                  {heroTitle}
                </h1>
              </div>
            )}

            {!isConversationLoading && !showEmptyHero && (
              <div className="pb-5 pt-1 text-center text-xs font-medium text-muted-foreground/75">
                {conversationDayLabel(messages[0]?.timestamp || Date.now(), locale)}
              </div>
            )}

            {showBotWelcome && (
              <div className="flex justify-start">
                <div className="max-w-full rounded-2xl rounded-bl-md bg-[#f1f1f1] px-4 py-2.5 text-[#202020] dark:bg-white/[0.09] dark:text-foreground">
                  <p className="whitespace-pre-wrap text-[15px] leading-6">{botWelcome}</p>
                </div>
              </div>
            )}

            {!isConversationLoading && (() => {
              // Tool-group artefacts (e.g. an image rendered to base64 by
              // a Python script inside the sandbox) are attached to the
              // *next* agent reply bubble — so they appear as part of the
              // assistant's answer, not inside the tool panel. If no agent
              // reply follows (tool still running or chain didn't finish),
              // we skip surfacing the image; it will show up with the next
              // reply. `surfacedSrcs` tracks every image src we've
              // surfaced so bubbles can suppress duplicate inline copies.
              const attachedImages = new Map<string, Array<{ alt: string; src: string }>>();
              const surfacedSrcs = new Set<string>();
              let pending: Array<{ alt: string; src: string }> = [];
              for (const m of messages) {
                if (m.role === "tool-group" && m.toolCalls) {
                  for (const tc of m.toolCalls) {
                    if (!tc.result) continue;
                    for (const p of splitDataImages(tc.result)) {
                      if (p.type === "image") {
                        pending.push({ alt: p.alt, src: p.src });
                      }
                    }
                  }
                  continue;
                }
                if (m.role === "agent" && pending.length > 0) {
                  attachedImages.set(m.id, pending);
                  for (const img of pending) surfacedSrcs.add(img.src);
                  pending = [];
                }
              }
              // Walk messages once so we can bundle consecutive
              // tool-group rounds into a single collapsible. Without
              // this, a long ReAct turn with seven sequential rounds
              // produces seven independently-collapsible boxes that
              // dominate the chat — the bundle hides them behind one
              // header until the user actually wants to dive in.
              const elements: React.ReactNode[] = [];
              for (let i = 0; i < messages.length; i++) {
                const msg = messages[i];
                if (msg.role === "tool-group") {
                  const start = i;
                  while (
                    i + 1 < messages.length &&
                    messages[i + 1].role === "tool-group"
                  ) {
                    i++;
                  }
                  const rounds = messages.slice(start, i + 1);
                  // Keep the surfacing of any per-round produced files
                  // out here so each round's panel still renders below
                  // the bundle in chronological order.
                  const filePanels = rounds
                    .filter((r) => r.files && r.files.length > 0)
                    .map((r) => (
                      <FilesPanel
                        key={`files-${r.id}`}
                        files={r.files!}
                        onOpen={openWorkspace}
                      />
                    ));
                  if (rounds.length === 1) {
                    elements.push(
                      <div key={rounds[0].id}>
                        <ToolCallGroup
                          msg={rounds[0]}
                          surfacedSrcs={surfacedSrcs}
                          agentId={selectedAgent}
                          sessionId={sessionId}
                          subagentProgress={subagentProgress}
                          onKnowledgeCitationClick={openKnowledgeCitation}
                        />
                        {filePanels}
                      </div>,
                    );
                  } else {
                    elements.push(
                      <div key={`bundle-${rounds[0].id}`}>
                        <ToolRoundsBundle
                          rounds={rounds}
                          surfacedSrcs={surfacedSrcs}
                          agentId={selectedAgent}
                          sessionId={sessionId}
                          subagentProgress={subagentProgress}
                          onKnowledgeCitationClick={openKnowledgeCitation}
                        />
                        {filePanels}
                      </div>,
                    );
                  }
                  if (rounds.some((round) => round.id === todoAnchorMessageId)) {
                    elements.push(
                      <TodoPanel key="todo-panel" items={todoItems} active={todoIsActive} />,
                    );
                  }
                  continue;
                }
                // Agent bubbles may carry the `<|split|>` marker the
                // LLM emits for multi-bubble output (mirrors IM channel
                // behavior). Expand into one bubble per chunk so the
                // marker never surfaces as literal text. Attach files /
                // metadata only to the last chunk to match the IM
                // dispatcher's "attach to last chunk" rule.
                if (msg.role === "agent" && msg.content.includes(SPLIT_MARKER)) {
                  const parts = splitOnMarker(msg.content);
                  parts.forEach((part, idx) => {
                    const isLast = idx === parts.length - 1;
                    elements.push(
                      renderRegularBubble({
                        ...msg,
                        id: `${msg.id}-s${idx}`,
                        content: part,
                        files: isLast ? msg.files : undefined,
                        metadata: isLast ? msg.metadata : undefined,
                      }),
                    );
                  });
                  if (msg.id === todoAnchorMessageId) {
                    elements.push(
                      <TodoPanel key="todo-panel" items={todoItems} active={todoIsActive} />,
                    );
                  }
                  continue;
                }
                elements.push(renderRegularBubble(msg));
                if (msg.id === todoAnchorMessageId) {
                  elements.push(
                    <TodoPanel key="todo-panel" items={todoItems} active={todoIsActive} />,
                  );
                }
              }
              return elements;

              function renderRegularBubble(msg: ChatMessage) {
                return (
                <div
                  key={msg.id}
                  className={`flex ${msg.role === "user" ? "justify-end" : "justify-start"}`}
                >
                  <div
                    className={`group relative ${
                      // Assistant content (tables, long markdown) uses the full
                      // lane — capped only by the lane's max-w-2xl — so it stops
                      // wrapping early and leaving a big empty gutter on narrow
                      // panels. User bubbles stay hugged to the right.
                      msg.role === "user" ? "max-w-[80%] order-1" : "max-w-full"
                    }`}
                  >
                    {msg.role === "user" && msg.sender && (
                      <div className="mb-1 flex items-center justify-end gap-2 text-xs text-muted-foreground">
                        <span className="font-medium text-foreground/80">{msg.sender.name}</span>
                        {msg.sender.avatarUrl ? (
                          // eslint-disable-next-line @next/next/no-img-element
                          <img
                            src={msg.sender.avatarUrl}
                            alt={msg.sender.name}
                            className="h-5 w-5 rounded-full object-cover ring-1 ring-border"
                          />
                        ) : (
                          <span className="flex h-5 w-5 items-center justify-center rounded-full bg-primary/20 text-[10px] font-semibold uppercase text-foreground">
                            {msg.sender.name.slice(0, 1)}
                          </span>
                        )}
                      </div>
                    )}
                    <div
                      className={`rounded-2xl px-4 py-2.5 break-words ${
                        msg.role === "user"
                          ? "user-chat-bubble bg-[#111111] text-white rounded-br-md dark:bg-white dark:text-black"
                          : "bg-[#f1f1f1] text-[#202020] rounded-bl-md dark:bg-white/[0.09] dark:text-foreground"
                      }`}
                    >
                      {(() => {
                        const attached = attachedImages.get(msg.id);
                        return attached && attached.length > 0 ? (
                          <div className="space-y-2 mb-2">
                            {attached.map((img, i) => (
                              // eslint-disable-next-line @next/next/no-img-element
                              <img
                                key={i}
                                src={img.src}
                                alt={img.alt}
                                className="rounded-lg max-w-full h-auto"
                              />
                            ))}
                          </div>
                        ) : null;
                      })()}
                      {msg.role === "user" && msg.attachments && msg.attachments.length > 0 && (
                        <div className="flex flex-wrap gap-2 mb-2 justify-end">
                          {msg.attachments.map((att, i) =>
                            att.isImage && att.previewUrl ? (
                              <button
                                key={i}
                                type="button"
                                onClick={() => setLightboxSrc(att.previewUrl!)}
                                className="block cursor-zoom-in"
                                aria-label={tr("Preview {{name}}", "预览 {{name}}", { name: att.name })}
                              >
                                {/* eslint-disable-next-line @next/next/no-img-element */}
                                <img
                                  src={att.previewUrl}
                                  alt={att.name}
                                  className="rounded-lg max-h-48 max-w-[12rem] w-auto h-auto object-cover"
                                />
                              </button>
                            ) : (
                              <div
                                key={i}
                                className="flex items-center gap-2 rounded-md bg-sidebar-foreground/10 px-2 py-1.5 text-xs"
                              >
                                <Paperclip className="h-3 w-3 opacity-70" />
                                <span className="truncate">{att.name}</span>
                              </div>
                            ),
                          )}
                        </div>
                      )}
                      {msg.content && (
                        renderContentWithDataImages(
                          msg.content,
                          surfacedSrcs,
                          (attachedImages.get(msg.id)?.length ?? 0) > 0,
                          selectedAgent,
                          sessionId,
                          msg.metadata?.knowledgeSources,
                          openKnowledgeCitation,
                        ) ?? (
                          <ChatMarkdown
                            text={msg.content}
                            agentId={selectedAgent}
                            sessionId={sessionId}
                            knowledgeSources={msg.metadata?.knowledgeSources}
                            onKnowledgeCitationClick={openKnowledgeCitation}
                          />
                        )
                      )}
                      {msg.role === "agent" && msg.metadata?.iterationCapReached && (
                        <div className="mt-2 flex items-start gap-1.5 rounded-md border border-amber-500/40 bg-amber-500/10 px-2.5 py-1.5 text-xs text-amber-900 dark:text-amber-200">
                          <span className="font-medium">{tr("Iteration limit reached", "已达到迭代上限")}</span>
                          <span className="opacity-80">
                            {tr("The agent reached its {{count}} tool-call budget before finishing. The answer above was synthesized from partial results, so some fields may be marked unknown or partial. Continue the conversation to go further.", "Agent 在完成任务前已用完 {{count}} 次工具调用预算。上方回答基于部分结果整理，某些字段可能标记为未知或不完整；可继续对话以进一步处理。", { count: msg.metadata.iterationCapValue ?? "" })}
                          </span>
                        </div>
                      )}
                      {msg.role === "agent" && msg.metadata?.planMode && (
                        <div className="mt-2 flex items-start gap-1.5 rounded-md border border-amber-500/40 bg-amber-500/10 px-2.5 py-1.5 text-xs text-amber-900 dark:text-amber-200">
                          <ListChecks className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                          <span className="font-medium">{tr("Plan only — review before executing.", "仅生成计划——请确认后再执行。")}</span>
                          <span className="opacity-80">
                            {tr('Tools were disabled for this turn. Reply with "go" or your edits to run it.', "本轮未启用工具。回复“go”或提出修改后即可执行。")}
                          </span>
                        </div>
                      )}
                      {msg.role === "agent" && msg.id === pendingPlanId && (
                        <div className="mt-3 flex flex-wrap items-center gap-2">
                          <Button
                            size="sm"
                            onClick={() => handleSend("go")}
                            disabled={sending}
                            className="h-8 gap-1.5"
                          >
                            <Check className="h-3.5 w-3.5" />
                            {tr("Run plan", "执行计划")}
                          </Button>
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() => {
                              // No payload sent — user is rejecting the
                              // plan. Focus the composer with a hint so
                              // they type what to change; the buttons
                              // re-render once the next assistant reply
                              // matches the pending-plan signal again.
                              setInput("");
                              textareaRef.current?.focus();
                            }}
                            disabled={sending}
                            className="h-8 gap-1.5"
                          >
                            <X className="h-3.5 w-3.5" />
                            {tr("Edit", "修改")}
                          </Button>
                          <span className="text-xs text-muted-foreground">
                            {tr("Run the plan to authorize the agent to complete it end to end, or choose Edit to revise it below.", "选择“执行计划”可授权 Agent 完整执行；选择“修改”可在下方调整计划。")}
                          </span>
                        </div>
                      )}
                    </div>
                    {msg.files && msg.files.length > 0 && (
                      <FilesPanel files={msg.files} onOpen={openWorkspace} />
                    )}
                    <div
                      className={`flex items-center gap-1.5 mt-1 ${
                        msg.role === "user" ? "justify-end" : "justify-start"
                      }`}
                    >
                      {msg.role === "user" ? (
                        <>
                          {msg.timestamp > 0 && (
                            <span className="opacity-0 group-hover:opacity-100 text-[10px] text-muted-foreground/60 transition-all">
                              {formatTime(msg.timestamp)}
                            </span>
                          )}
                          <button
                            onClick={() => handleCopy(msg)}
                            className="opacity-0 group-hover:opacity-100 p-0.5 rounded hover:bg-muted text-muted-foreground/60 hover:text-muted-foreground transition-all"
                            title={tr("Copy", "复制")}
                          >
                            {copiedId === msg.id ? (
                              <Check className="h-3 w-3 text-emerald-500" />
                            ) : (
                              <Copy className="h-3 w-3" />
                            )}
                          </button>
                          <button
                            onClick={() => handleRetry(msg)}
                            className="opacity-0 group-hover:opacity-100 p-0.5 rounded hover:bg-muted text-muted-foreground/60 hover:text-muted-foreground transition-all"
                            title={tr("Resend (refills the composer)", "重新发送（填回输入框）")}
                          >
                            <RotateCcw className="h-3 w-3" />
                          </button>
                        </>
                      ) : (
                        <>
                          {msg.timestamp > 0 && (
                            <span className="text-[10px] text-muted-foreground/60">
                              {formatTime(msg.timestamp)}
                            </span>
                          )}
                          <button
                            onClick={() => handleCopy(msg)}
                            className="opacity-0 group-hover:opacity-100 p-0.5 rounded hover:bg-muted text-muted-foreground/60 hover:text-muted-foreground transition-all"
                            title={tr("Copy", "复制")}
                          >
                            {copiedId === msg.id ? (
                              <Check className="h-3 w-3 text-emerald-500" />
                            ) : (
                              <Copy className="h-3 w-3" />
                            )}
                          </button>
                          <button
                            onClick={openWorkspace}
                            className="opacity-0 group-hover:opacity-100 inline-flex items-center gap-1 px-1.5 py-0.5 rounded hover:bg-muted text-[10px] text-muted-foreground/60 hover:text-muted-foreground transition-all"
                            title={tr("View task files", "查看任务文件")}
                          >
                            <FolderOpen className="h-3 w-3" />
                            <span>{tr("Files", "文件")}</span>
                          </button>
                        </>
                      )}
                    </div>
                  </div>
                </div>
              );
              }
            })()}

            {!isConversationLoading && sending && (
              <div className="flex justify-start">
                <div className="bg-muted rounded-2xl rounded-bl-md px-4 py-3">
                  <div className="flex items-center gap-1">
                    <span className="typing-dot inline-block h-2 w-2 rounded-full bg-muted-foreground/60" style={{ animationDelay: "0ms" }} />
                    <span className="typing-dot inline-block h-2 w-2 rounded-full bg-muted-foreground/60" style={{ animationDelay: "200ms" }} />
                    <span className="typing-dot inline-block h-2 w-2 rounded-full bg-muted-foreground/60" style={{ animationDelay: "400ms" }} />
                  </div>
                </div>
              </div>
            )}

          </div>
        </div>

        {!isConversationLoading && !showEmptyHero && messageScrollState.overflow && (
          <div className="pointer-events-none absolute right-3 top-1/2 z-20 flex -translate-y-1/2 flex-col gap-1.5">
            <button
              type="button"
              onClick={() => scrollMessagesByPage(-1)}
              disabled={!messageScrollState.canScrollUp}
              className="pointer-events-auto flex size-9 items-center justify-center rounded-xl border border-black/[0.08] bg-background/95 text-muted-foreground shadow-[0_6px_20px_rgba(0,0,0,0.12)] backdrop-blur transition hover:bg-background hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-35 dark:border-white/[0.1]"
              aria-label={tr("Scroll up", "向上滚动")}
              title={tr("Scroll up", "向上滚动")}
            >
              <ChevronUp className="size-4" />
            </button>
            <button
              type="button"
              onClick={() => scrollMessagesByPage(1)}
              disabled={!messageScrollState.canScrollDown}
              className="pointer-events-auto flex size-9 items-center justify-center rounded-xl border border-black/[0.08] bg-background/95 text-muted-foreground shadow-[0_6px_20px_rgba(0,0,0,0.12)] backdrop-blur transition hover:bg-background hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-35 dark:border-white/[0.1]"
              aria-label={tr("Scroll down", "向下滚动")}
              title={tr("Scroll down", "向下滚动")}
            >
              <ChevronDown className="size-4" />
            </button>
          </div>
        )}

        {/* Full-width conversation composer, matching the compact Bot layout. */}
        <div
          aria-hidden={isConversationLoading}
          className={isConversationLoading ? "hidden" : "shrink-0 px-3 pb-5 pt-2 sm:px-5"}
        >
          <div className="relative mx-auto w-full">
            {isReadOnlyChannel && (
              // The web compose path can't deliver into upstream IM
              // platforms (no reverse channel adapter, no outbound
              // routing), so writing here would silently corrupt the
              // session: the agent would process the turn, the IM
              // user would never see it, and on refresh the original
              // session's history wins because the orphan write
              // landed under a triple lookup that didn't match.
              // Block the input outright and tell the user where to
              // reply.
              <div className="mb-2 rounded-lg border border-border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
                {tr("This conversation is hosted on", "此对话来自")} {" "}
                <span className="font-medium text-foreground">
                  {channelLabel(currentChannel)}
                </span>
                {tr(". Reply there. Slash commands such as", "。请在该渠道中回复。这里仍可运行类似")} {" "}
                <span className="font-mono text-foreground">/usage</span>{" "}
                {tr("can run here, but normal messages typed here will not reach the user on the other side.", "的斜杠命令，但在此输入的普通消息不会发送给渠道另一端的用户。")}
              </div>
            )}
            {isActAsView && !isReadOnlyChannel && (
              // Super_admin viewing another user's chat via the admin
              // Chats page (?actAs=<uid>). The middleware gates this as
              // read-only for the whole request, so any send would 403
              // — disable the composer and surface why.
              <div className="mb-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
                {tr("Read-only — you are viewing another user's chat. Sending messages here is disabled.", "只读模式——你正在查看其他用户的对话，无法在此发送消息。")}
              </div>
            )}
            {slashOpen && filteredItems.length > 0 && (
              <SlashMenu
                items={filteredItems}
                activeIndex={slashIndex}
                onHover={setSlashIndex}
                onSelect={selectItem}
              />
            )}
            <div className="rounded-[22px] border border-black/10 bg-card p-1.5 shadow-[0_8px_28px_rgba(0,0,0,0.055)] transition-shadow focus-within:border-black/15 focus-within:ring-2 focus-within:ring-black/5 dark:border-white/10 dark:focus-within:border-white/16 dark:focus-within:ring-white/5">
              {attachments.length > 0 && (
                <div className="mx-1 mb-2 flex flex-wrap gap-2 border-b border-border/60 px-1 pb-2 pt-1">
                  {attachments.map((f, i) => {
                    const preview = attachmentPreviews[i];
                    if (preview) {
                      return (
                        <div
                          key={`${f.name}-${i}`}
                          className="group relative h-14 w-14 overflow-hidden rounded-md border border-border bg-muted"
                        >
                          <button
                            type="button"
                            onClick={() => setLightboxSrc(preview)}
                            className="block h-full w-full cursor-zoom-in"
                            aria-label={tr("Preview {{name}}", "预览 {{name}}", { name: f.name })}
                          >
                            {/* eslint-disable-next-line @next/next/no-img-element */}
                            <img
                              src={preview}
                              alt={f.name}
                              className="h-full w-full object-cover"
                            />
                          </button>
                          <button
                            type="button"
                            onClick={() => removeAttachment(i)}
                            className="absolute right-0.5 top-0.5 flex h-4 w-4 items-center justify-center rounded-full bg-background/80 text-muted-foreground opacity-0 transition group-hover:opacity-100 hover:text-foreground"
                            aria-label={tr("Remove attachment", "移除附件")}
                          >
                            <X className="h-3 w-3" />
                          </button>
                        </div>
                      );
                    }
                    return (
                      <div
                        key={`${f.name}-${i}`}
                        className="flex items-center gap-1.5 rounded-md bg-muted/60 pl-2 pr-1 py-1 text-xs"
                      >
                        <Paperclip className="h-3 w-3 text-muted-foreground" />
                        <span className="max-w-[160px] truncate">{f.name}</span>
                        <button
                          type="button"
                          onClick={() => removeAttachment(i)}
                          className="p-0.5 rounded hover:bg-muted-foreground/15 text-muted-foreground hover:text-foreground"
                          aria-label={tr("Remove attachment", "移除附件")}
                        >
                          <X className="h-3 w-3" />
                        </button>
                      </div>
                    );
                  })}
                </div>
              )}
              <div className="flex items-end gap-2">
                <DropdownMenu>
                  <DropdownMenuTrigger
                    disabled={!canUseComposer}
                    render={
                      <button
                        type="button"
                        className="flex size-8 shrink-0 items-center justify-center rounded-full border border-border/80 bg-muted/35 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:cursor-not-allowed disabled:opacity-45"
                        aria-label={t("composer.moreOptions")}
                        title={t("composer.moreOptions")}
                      >
                        <Plus className="size-[17px]" />
                      </button>
                    }
                  />
                  <DropdownMenuContent
                    align="start"
                    side="top"
                    sideOffset={10}
                    className="w-56 rounded-2xl p-1.5 shadow-xl"
                  >
                    <DropdownMenuItem
                      disabled={!canAttach}
                      onClick={() => fileInputRef.current?.click()}
                      className="gap-2.5 rounded-xl px-3 py-2.5"
                    >
                      <Paperclip className="size-4 text-muted-foreground" />
                      <span>{t("composer.addAttachment")}</span>
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
                <input
                  ref={fileInputRef}
                  type="file"
                  multiple
                  className="sr-only"
                  onChange={handleFilePick}
                  disabled={!canAttach}
                />
                {urlProjectId && projectInfo && (
                  <div
                    className="flex h-8 min-w-0 shrink-0 items-center gap-1.5 rounded-full border border-border px-2.5 text-xs text-muted-foreground"
                    title={projectInfo.name}
                  >
                    <FolderOpen className="size-3.5 shrink-0" />
                    <span className="max-w-[14ch] truncate">{projectInfo.name}</span>
                  </div>
                )}
                <textarea
                  ref={textareaRef}
                  value={input}
                  onChange={handleInputChange}
                  onPaste={handlePaste}
                  onKeyDown={handleKeyDown}
                  onBlur={() => setTimeout(() => setSlashOpen(false), 120)}
                  placeholder={
                    isActAsView
                      ? t("composer.readOnly")
                      : isReadOnlyChannel
                        ? t("composer.slashOnly", { channel: channelLabel(currentChannel) })
                        : selectedAgent
                          ? t("composer.message", { name: agentName || selectedAgent })
                          : t("composer.selectAgent")
                  }
                  disabled={!canUseComposer}
                  rows={1}
                  className="block min-w-0 flex-1 resize-none bg-transparent px-1 py-1 text-[15px] leading-6 placeholder:text-muted-foreground/45 outline-none [scrollbar-width:none] [&::-webkit-scrollbar]:hidden disabled:opacity-50"
                  style={{ maxHeight: 180, minHeight: 32 }}
                />
                {sending ? (
                  <Button
                    onClick={handleStop}
                    size="icon"
                    className="size-8 shrink-0 rounded-full bg-[#111] text-white hover:bg-black disabled:bg-[#111] dark:bg-white dark:text-black dark:hover:bg-white/90"
                    aria-label={t("composer.stop")}
                  >
                    <Square className="size-3 fill-current" />
                  </Button>
                ) : input.trim() || attachments.length > 0 ? (
                  <Button
                    onMouseDown={(event) => {
                      event.preventDefault();
                      handleSend();
                    }}
                    disabled={!canSendComposer}
                    size="icon"
                    className="size-8 shrink-0 rounded-full bg-[#111] text-white hover:bg-black disabled:bg-[#111] disabled:text-white/70 dark:bg-white dark:text-black dark:hover:bg-white/90"
                    aria-label={t("composer.send")}
                  >
                    <ArrowUp className="size-[17px] stroke-[2.25]" />
                  </Button>
                ) : null}
              </div>
            </div>
          </div>
        </div>
        {lightboxSrc && (
          <div
            className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-6 cursor-zoom-out"
            onClick={() => setLightboxSrc(null)}
            role="dialog"
            aria-modal="true"
            aria-label={tr("Image preview", "图片预览")}
          >
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img
              src={lightboxSrc}
              alt={tr("Preview", "预览")}
              className="max-h-full max-w-full rounded-lg shadow-2xl"
              onClick={(e) => e.stopPropagation()}
            />
            <button
              type="button"
              onClick={() => setLightboxSrc(null)}
              className="absolute right-4 top-4 flex h-9 w-9 items-center justify-center rounded-full bg-background/80 text-foreground hover:bg-background"
              aria-label={tr("Close preview", "关闭预览")}
            >
              <X className="h-5 w-5" />
            </button>
          </div>
        )}
      </div>
      {navigationPanelOpen && selectedAgent && (
        <BotControlPanel
          agentId={selectedAgent}
          projects={projects}
          activeProjectId={urlProjectId || null}
          expandedProjectId={
            urlProjectId
              || sessions.find((topic) => topic.id === sessionId)?.projectId
              || null
          }
          topics={sessions}
          activeTopicId={sessionId}
          agentRole={agentDetail?.role}
          onOpenSettings={openBotSettings}
          onOpenSettingsTab={openBotSettingsTab}
          onOpenWorkspace={openWorkspaceFromBotPanel}
          workspaceAvailable={Boolean(routeSessionId || urlProjectId)}
          onSelectProject={(projectId) => {
            router.push(`/agents/${selectedAgent}/project/${encodeURIComponent(projectId)}/`);
          }}
          onProjectsChanged={() => {
            loadProjects(selectedAgent);
            window.dispatchEvent(
              new CustomEvent("fastclaw:sessions-changed", {
                detail: { agentId: selectedAgent },
              }),
            );
          }}
          onSelectTopic={handleSelectSession}
          onNewTopic={handleNewChat}
          onTopicsChanged={() => {
            loadSessions(selectedAgent);
            window.dispatchEvent(
              new CustomEvent("fastclaw:sessions-changed", {
                detail: { agentId: selectedAgent },
              }),
            );
          }}
          onClose={() => {
            if (isChatsPage) leaveChatsPage(false);
            else setBotPanelOpen(false);
          }}
        />
      )}
      {filesSheetOpen && selectedAgent && (sessionId || urlProjectId) && (
        <WorkspacePanel
          agentId={selectedAgent}
          // On a project landing (no routeSessionId), sessionId here is the
          // synthetic id chat-screen mints for the upcoming "New chat" —
          // it doesn't correspond to anything on disk, so we suppress it
          // and let projectId drive the scope. Inside an actual chat,
          // routeSessionId is set and we pass the real sessionId.
          sessionId={routeSessionId ? sessionId : ""}
          projectId={!routeSessionId && urlProjectId ? urlProjectId : undefined}
          knowledgePreview={knowledgePreview}
          onClearKnowledgePreview={() => setKnowledgePreview(null)}
          onPreviewStateChange={handleWorkspacePreviewChange}
          onBack={
            workspaceReturnsToBotPanel
              ? () => {
                  setFilesSheetOpen(false);
                  setKnowledgePreview(null);
                  setWorkspaceReturnsToBotPanel(false);
                  setBotPanelOpen(true);
                }
              : undefined
          }
          onClose={() => {
            setFilesSheetOpen(false);
            setKnowledgePreview(null);
            setWorkspaceReturnsToBotPanel(false);
          }}
        />
      )}
    </div>
  );
}

const BOT_PANEL_MIN_WIDTH = 300;
const BOT_PANEL_DEFAULT_WIDTH = 400;
const BOT_PANEL_MAX_WIDTH = 520;
const CHAT_PANE_MIN_WIDTH = 520;
const BOT_PANEL_WIDTH_KEY = "fastclaw:bot-panel-width";

function ShareAgentMenu({
  agentId,
  agentName,
  isPublic,
  canChangeVisibility,
  onPublicChange,
}: {
  agentId: string;
  agentName: string;
  isPublic: boolean;
  canChangeVisibility: boolean;
  onPublicChange: (isPublic: boolean) => void;
}) {
  const { tr } = useLocale();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState("");
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const handlePointerDown = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    document.addEventListener("pointerdown", handlePointerDown);
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("pointerdown", handlePointerDown);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [open]);

  const copyLink = async () => {
    const url = `${window.location.origin}/agents/${encodeURIComponent(agentId)}/chat/`;
    let copiedToClipboard = false;
    if (navigator.clipboard?.writeText) {
      try {
        await navigator.clipboard.writeText(url);
        copiedToClipboard = true;
      } catch {
        // Clipboard API is commonly blocked on HTTP/self-signed installs.
        // Fall through to the selection-based copy path below.
      }
    }
    if (!copiedToClipboard) {
      const input = document.createElement("textarea");
      input.value = url;
      input.setAttribute("readonly", "");
      input.style.position = "fixed";
      input.style.left = "-9999px";
      document.body.appendChild(input);
      input.select();
      copiedToClipboard = document.execCommand("copy");
      input.remove();
    }
    if (!copiedToClipboard) {
      throw new Error(tr("Could not copy the link", "无法复制链接，请检查浏览器权限"));
    }
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1800);
  };

  const changeVisibility = async (nextPublic: boolean, copyAfter = false) => {
    if (busy) return;
    if (nextPublic !== isPublic && !canChangeVisibility) return;
    setBusy(true);
    setError("");
    setCopied(false);
    try {
      if (nextPublic !== isPublic) {
        const response = await updateAgent(agentId, { isPublic: nextPublic });
        if (response?.ok === false || response?.error) {
          throw new Error(response?.error || tr("Could not update sharing", "无法更新分享设置"));
        }
        onPublicChange(nextPublic);
      }
      if (copyAfter) await copyLink();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : tr("Could not update sharing", "无法更新分享设置"));
    } finally {
      setBusy(false);
    }
  };

  const triggerLabel = tr("Share {{agent}}", "分享 {{agent}}", { agent: agentName });

  return (
    <div ref={rootRef} className="relative ml-auto shrink-0">
      <button
        type="button"
        onClick={() => {
          setOpen((current) => !current);
          setError("");
          setCopied(false);
        }}
        className={`inline-flex size-9 items-center justify-center rounded-lg transition-colors focus-visible:ring-2 focus-visible:ring-ring ${
          open
            ? "bg-muted text-foreground"
            : "text-muted-foreground hover:bg-muted/70 hover:text-foreground"
        }`}
        title={triggerLabel}
        aria-label={triggerLabel}
        aria-haspopup="dialog"
        aria-expanded={open}
      >
        <Share2 className="size-[17px]" />
      </button>

      {open && (
        <div
          role="dialog"
          aria-label={triggerLabel}
          className="absolute right-0 top-full z-50 mt-1.5 w-[min(320px,calc(100vw-24px))] rounded-xl border border-border bg-popover p-1.5 text-popover-foreground shadow-lg ring-1 ring-foreground/5"
        >
          <button
            type="button"
            disabled={busy || (!canChangeVisibility && isPublic)}
            onClick={() => void changeVisibility(false)}
            className="flex w-full items-center gap-3 rounded-lg px-2 py-2 text-left transition-colors hover:bg-muted focus-visible:bg-muted focus-visible:outline-none disabled:cursor-default disabled:opacity-60"
          >
            <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted text-foreground">
              <LockKeyhole className="size-[18px] stroke-[1.8]" />
            </span>
            <span className="min-w-0 flex-1">
              <span className="block text-sm font-medium">{tr("Only me", "仅自己")}</span>
              <span className="block truncate text-xs text-muted-foreground">{tr("Only you can view", "只有你自己可以查看")}</span>
            </span>
            {!isPublic && <Check className="size-4 shrink-0" />}
          </button>

          <button
            type="button"
            disabled={busy || !canChangeVisibility}
            onClick={() => void changeVisibility(true)}
            className="flex w-full items-center gap-3 rounded-lg px-2 py-2 text-left transition-colors hover:bg-muted focus-visible:bg-muted focus-visible:outline-none disabled:cursor-default disabled:opacity-60"
          >
            <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted text-foreground">
              <Globe2 className="size-[18px] stroke-[1.8]" />
            </span>
            <span className="min-w-0 flex-1">
              <span className="block text-sm font-medium">{tr("Public access", "公开访问")}</span>
              <span className="block truncate text-xs text-muted-foreground">{tr("Anyone with the link can view", "任何拿到链接的人都可以查看")}</span>
            </span>
            {busy && !isPublic ? (
              <RefreshCw className="size-4 shrink-0 animate-spin" />
            ) : isPublic ? (
              <Check className="size-4 shrink-0" />
            ) : null}
          </button>

          {error && <p role="alert" className="px-2 pb-1 pt-1 text-xs text-destructive">{error}</p>}

          {isPublic && (
            <Button
              type="button"
              disabled={busy}
              onClick={() => void changeVisibility(true, true)}
              className="mt-1 w-full"
              aria-live="polite"
            >
              {busy ? (
                <RefreshCw className="size-4 animate-spin" />
              ) : copied ? (
                <Check className="size-4" />
              ) : (
                <Link2 className="size-4" />
              )}
              {busy
                ? tr("Copying…", "正在复制…")
                : copied
                  ? tr("Link copied", "链接已复制")
                  : tr("Copy link", "复制链接")}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

const BOT_QUICK_ACTIONS: Array<{
  id: string;
  labelKey: MessageKey;
  icon: typeof Brain;
  tab?: AgentSettingsTab;
  userOnly?: boolean;
  ownerOnly?: boolean;
  action?: "workspace" | "settings";
}> = [
  { id: "models", tab: "models", labelKey: "settings.tab.models", icon: Brain },
  { id: "skills", tab: "skills", labelKey: "settings.tab.skills", icon: Sparkles, ownerOnly: true },
  { id: "channels", tab: "channels", labelKey: "settings.tab.channels", icon: Radio },
  { id: "scheduler", tab: "scheduler", labelKey: "settings.tab.scheduler", icon: Clock, ownerOnly: true },
  { id: "workspace", labelKey: "workspace.title", icon: FolderOpen, action: "workspace" },
  { id: "settings", labelKey: "sidebar.moreSettings", icon: Settings, action: "settings" },
];

function BotControlPanel({
  agentId,
  projects,
  activeProjectId,
  expandedProjectId,
  topics,
  activeTopicId,
  agentRole,
  onOpenSettings,
  onOpenSettingsTab,
  onOpenWorkspace,
  workspaceAvailable,
  onSelectProject,
  onProjectsChanged,
  onSelectTopic,
  onNewTopic,
  onTopicsChanged,
  onClose,
}: {
  agentId: string;
  projects: ProjectEntry[];
  activeProjectId: string | null;
  expandedProjectId: string | null;
  topics: ChatSession[];
  activeTopicId: string;
  agentRole?: "owner" | "viewer";
  onOpenSettings: () => void;
  onOpenSettingsTab: (tab: AgentSettingsTab, userOnly?: boolean) => void;
  onOpenWorkspace: () => void;
  workspaceAvailable: boolean;
  onSelectProject: (projectId: string) => void;
  onProjectsChanged: () => void;
  onSelectTopic: (sessionId: string) => void;
  onNewTopic: () => void;
  onTopicsChanged: () => void;
  onClose: () => void;
}) {
  const { t, tr } = useLocale();
  const [showAllProjects, setShowAllProjects] = useState(false);
  const [visibleTopicCount, setVisibleTopicCount] = useState(10);
  const [expandedProjects, setExpandedProjects] = useState<Set<string>>(
    () => new Set(expandedProjectId ? [expandedProjectId] : []),
  );
  const [createProjectOpen, setCreateProjectOpen] = useState(false);
  const [editProject, setEditProject] = useState<ProjectEntry | null>(null);
  const [editTopic, setEditTopic] = useState<ChatSession | null>(null);
  const [deleteTopic, setDeleteTopic] = useState<ChatSession | null>(null);
  const [panelWidth, setPanelWidth] = useState(() => {
    if (typeof window === "undefined") return BOT_PANEL_DEFAULT_WIDTH;
    const stored = Number(window.localStorage.getItem(BOT_PANEL_WIDTH_KEY));
    return Number.isFinite(stored) && stored >= BOT_PANEL_MIN_WIDTH && stored <= BOT_PANEL_MAX_WIDTH
      ? stored
      : BOT_PANEL_DEFAULT_WIDTH;
  });
  const [resizing, setResizing] = useState(false);
  const panelRef = useRef<HTMLElement>(null);
  const resizeStartRef = useRef({ x: 0, width: BOT_PANEL_DEFAULT_WIDTH });
  const latestPanelWidthRef = useRef(panelWidth);

  const clampPanelWidth = useCallback((candidate: number) => {
    const containerWidth = panelRef.current?.parentElement?.getBoundingClientRect().width
      || window.innerWidth;
    const responsiveMax = Math.max(
      BOT_PANEL_MIN_WIDTH,
      containerWidth - CHAT_PANE_MIN_WIDTH,
    );
    return Math.round(
      Math.max(
        BOT_PANEL_MIN_WIDTH,
        Math.min(BOT_PANEL_MAX_WIDTH, responsiveMax, candidate),
      ),
    );
  }, []);

  const commitPanelWidth = useCallback((width: number) => {
    const next = clampPanelWidth(width);
    latestPanelWidthRef.current = next;
    setPanelWidth(next);
    try {
      window.localStorage.setItem(BOT_PANEL_WIDTH_KEY, String(next));
    } catch {
      // Ignore private-mode and quota failures.
    }
  }, [clampPanelWidth]);

  useEffect(() => {
    if (!resizing) return;
    const handleMove = (event: PointerEvent) => {
      const next = clampPanelWidth(
        resizeStartRef.current.width + resizeStartRef.current.x - event.clientX,
      );
      latestPanelWidthRef.current = next;
      setPanelWidth(next);
    };
    const handleUp = () => {
      setResizing(false);
      try {
        window.localStorage.setItem(
          BOT_PANEL_WIDTH_KEY,
          String(latestPanelWidthRef.current),
        );
      } catch {
        // Ignore private-mode and quota failures.
      }
    };
    document.body.style.cursor = "col-resize";
    document.body.style.userSelect = "none";
    window.addEventListener("pointermove", handleMove);
    window.addEventListener("pointerup", handleUp, { once: true });
    return () => {
      window.removeEventListener("pointermove", handleMove);
      window.removeEventListener("pointerup", handleUp);
      document.body.style.cursor = "";
      document.body.style.userSelect = "";
    };
  }, [clampPanelWidth, resizing]);

  useEffect(() => {
    const handleShortcut = (event: KeyboardEvent) => {
      if (
        (event.metaKey || event.ctrlKey)
        && event.key.toLowerCase() === "n"
        && !createProjectOpen
        && !editProject
        && !editTopic
        && !deleteTopic
      ) {
        event.preventDefault();
        onNewTopic();
      }
    };
    window.addEventListener("keydown", handleShortcut);
    return () => window.removeEventListener("keydown", handleShortcut);
  }, [createProjectOpen, deleteTopic, editProject, editTopic, onNewTopic]);

  useEffect(() => {
    if (!expandedProjectId) return;
    setExpandedProjects((current) => {
      if (current.has(expandedProjectId)) return current;
      const next = new Set(current);
      next.add(expandedProjectId);
      return next;
    });
  }, [expandedProjectId]);

  const visibleProjects = showAllProjects ? projects : projects.slice(0, 5);
  const sortedTopics = useMemo(
    () => [...topics].sort((a, b) => (b.updatedAt || b.createdAt || 0) - (a.updatedAt || a.createdAt || 0)),
    [topics],
  );
  const looseTopics = sortedTopics.filter((topic) => !topic.projectId);
  const visibleRecentTopics = looseTopics.slice(0, visibleTopicCount);
  const hiddenTopicCount = Math.max(0, looseTopics.length - visibleRecentTopics.length);
  const topicsByProject = useMemo(() => {
    const grouped = new Map<string, ChatSession[]>();
    for (const topic of sortedTopics) {
      if (!topic.projectId) continue;
      const projectTopics = grouped.get(topic.projectId) || [];
      projectTopics.push(topic);
      grouped.set(topic.projectId, projectTopics);
    }
    return grouped;
  }, [sortedTopics]);

  const handleProjectClick = (projectId: string, active: boolean) => {
    setExpandedProjects((current) => {
      const next = new Set(current);
      if (next.has(projectId)) next.delete(projectId);
      else next.add(projectId);
      return next;
    });
    if (!active) onSelectProject(projectId);
  };

  return (
    <>
      <button
        type="button"
        className="fixed inset-0 z-40 bg-black/20 xl:hidden"
        onClick={onClose}
        aria-label={tr("Close Agent panel", "关闭 Agent 侧栏")}
      />
      <aside
        ref={panelRef}
        data-resizable-right-panel="true"
        style={{ "--bot-panel-width": `${panelWidth}px` } as React.CSSProperties}
        className="fixed inset-y-0 right-0 z-50 flex h-dvh w-[min(94vw,420px)] shrink-0 flex-col border-l border-border bg-background shadow-2xl xl:relative xl:z-30 xl:-mt-14 xl:h-screen xl:w-[var(--bot-panel-width)] xl:min-w-[300px] xl:max-w-[calc(100%_-_520px)] xl:shadow-none"
      >
        <button
          type="button"
          className={`absolute inset-y-0 left-0 z-40 hidden w-3 -translate-x-1/2 cursor-col-resize touch-none outline-none after:absolute after:inset-y-0 after:left-1/2 after:w-px after:transition-colors hover:after:bg-foreground/25 focus-visible:after:bg-ring xl:block ${
            resizing ? "after:bg-ring" : "after:bg-transparent"
          }`}
          onPointerDown={(event) => {
            event.preventDefault();
            resizeStartRef.current = {
              x: event.clientX,
              width: panelRef.current?.getBoundingClientRect().width || panelWidth,
            };
            latestPanelWidthRef.current = resizeStartRef.current.width;
            setResizing(true);
          }}
          onDoubleClick={() => commitPanelWidth(BOT_PANEL_DEFAULT_WIDTH)}
          onKeyDown={(event) => {
            if (event.key === "ArrowLeft") {
              event.preventDefault();
              commitPanelWidth(panelWidth + 16);
            } else if (event.key === "ArrowRight") {
              event.preventDefault();
              commitPanelWidth(panelWidth - 16);
            } else if (event.key === "Home") {
              event.preventDefault();
              commitPanelWidth(BOT_PANEL_DEFAULT_WIDTH);
            }
          }}
          aria-label={tr("Resize right panel", "调整右侧栏宽度")}
          aria-orientation="vertical"
          aria-valuemin={BOT_PANEL_MIN_WIDTH}
          aria-valuemax={BOT_PANEL_MAX_WIDTH}
          aria-valuenow={panelWidth}
          role="separator"
          title={tr("Drag to resize · Double-click to reset", "拖动调整宽度 · 双击恢复默认宽度")}
        />
        <div className="flex h-14 shrink-0 items-center justify-between gap-3 px-5">
          <h2 className="min-w-0 truncate text-sm font-semibold text-foreground">
            {t("common.agent")}
          </h2>
          <button
            type="button"
            onClick={onClose}
            className="flex size-8 items-center justify-center rounded-lg text-muted-foreground transition hover:bg-black/5 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring dark:hover:bg-white/8"
            aria-label={tr("Collapse Agent panel", "收起 Agent 侧栏")}
            title={tr("Collapse", "收起")}
          >
            <ChevronsRight className="size-[18px]" />
          </button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-8 pt-1">
          <section aria-label={t("common.agent")} className="mb-5">
            <div className="grid grid-cols-3 gap-1 rounded-2xl border border-border/70 bg-muted/25 p-1">
              {BOT_QUICK_ACTIONS.map((action) => {
                const Icon = action.icon;
                const label = t(action.labelKey);
                const disabled =
                  (action.action === "workspace" && !workspaceAvailable)
                  || (action.ownerOnly && agentRole !== "owner");
                const title = action.action === "workspace" && !workspaceAvailable
                  ? t("workspace.unavailable")
                  : label;
                return (
                  <button
                    key={action.id}
                    type="button"
                    onClick={() => {
                      if (action.action === "workspace") onOpenWorkspace();
                      else if (action.action === "settings") onOpenSettings();
                      else if (action.tab) onOpenSettingsTab(action.tab, action.userOnly);
                    }}
                    disabled={disabled}
                    className="group flex min-h-[68px] min-w-0 flex-col items-center justify-center gap-1.5 rounded-xl px-1.5 py-2 text-muted-foreground transition-[background-color,color,box-shadow,transform] hover:-translate-y-px hover:bg-background hover:text-foreground hover:shadow-sm focus-visible:bg-background focus-visible:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 focus-visible:ring-offset-background active:translate-y-0 disabled:cursor-not-allowed disabled:opacity-35 disabled:hover:translate-y-0 disabled:hover:bg-transparent disabled:hover:text-muted-foreground disabled:hover:shadow-none dark:hover:bg-white/[0.06] dark:hover:shadow-none dark:disabled:hover:bg-transparent"
                    aria-label={label}
                    title={title}
                  >
                    <Icon className="size-[18px] stroke-[1.7] transition-transform group-hover:scale-105" />
                    <span className="max-w-full truncate text-[12px] font-medium leading-4">
                      {label}
                    </span>
                  </button>
                );
              })}
            </div>
          </section>

          <section>
            <div className="mb-1 flex h-7 items-center justify-between px-1">
              <h3 className="text-sm font-semibold text-muted-foreground">{tr("Projects", "项目")}</h3>
              <Tooltip>
                <TooltipTrigger
                  render={
                    <button
                      type="button"
                      onClick={() => setCreateProjectOpen(true)}
                      className="flex size-7 items-center justify-center rounded-md text-muted-foreground transition hover:bg-black/5 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring dark:hover:bg-white/8"
                      aria-label={tr("New project", "新建项目")}
                    >
                      <FolderPlus className="size-4" />
                    </button>
                  }
                />
                <TooltipContent side="left">{tr("New project", "新建项目")}</TooltipContent>
              </Tooltip>
            </div>
            {projects.length === 0 ? (
              <p className="px-2 py-1.5 text-sm leading-5 text-muted-foreground/75">{tr("No projects yet", "还没有项目")}</p>
            ) : (
              <div>
                {visibleProjects.map((project) => {
                  const active = project.id === activeProjectId;
                  const open = expandedProjects.has(project.id);
                  const projectTopics = topicsByProject.get(project.id) || [];
                  return (
                    <div key={project.id}>
                      <div
                        className={`group relative -ml-2 w-[calc(100%+0.5rem)] rounded-xl transition-colors ${
                          active
                            ? "bg-black/[0.07] dark:bg-white/[0.11]"
                            : "hover:bg-black/[0.04] dark:hover:bg-white/[0.07]"
                        }`}
                      >
                        <button
                          type="button"
                          onClick={() => handleProjectClick(project.id, active)}
                          className="flex h-10 w-full min-w-0 items-center gap-2.5 rounded-xl pl-[1.125rem] pr-[4.5rem] text-left focus-visible:ring-2 focus-visible:ring-ring"
                          aria-current={active ? "page" : undefined}
                          aria-expanded={open}
                          title={project.name}
                        >
                          {open ? (
                            <FolderOpen className="size-[17px] shrink-0 text-foreground/85" />
                          ) : (
                            <Folder className="size-[17px] shrink-0 text-foreground/85" />
                          )}
                          <span className="min-w-0 flex-1 truncate text-[15px] font-medium">
                            {project.name}
                          </span>
                        </button>
                        <div className={`absolute right-2 top-1/2 flex -translate-y-1/2 items-center gap-0.5 transition-opacity ${
                          active ? "opacity-100" : "opacity-0 group-hover:opacity-100 group-focus-within:opacity-100"
                        }`}>
                          <DropdownMenu>
                            <DropdownMenuTrigger
                              render={
                                <button
                                  type="button"
                                  onClick={(event) => event.stopPropagation()}
                                  className="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-background/70 hover:text-foreground"
                                  aria-label={tr("More actions for {{name}}", "{{name}} 的更多操作", { name: project.name })}
                                >
                                  <MoreHorizontal className="size-4" />
                                </button>
                              }
                            />
                            <DropdownMenuContent align="end" className="w-40 rounded-xl">
                              <DropdownMenuItem onClick={() => onSelectProject(project.id)}>
                                <Plus className="size-4 text-muted-foreground" />
                                {tr("New chat in project", "新建项目话题")}
                              </DropdownMenuItem>
                              <DropdownMenuItem onClick={() => setEditProject(project)}>
                                <Pencil className="size-4 text-muted-foreground" />
                                {tr("Edit project", "编辑项目")}
                              </DropdownMenuItem>
                            </DropdownMenuContent>
                          </DropdownMenu>
                          <button
                            type="button"
                            onClick={(event) => {
                              event.stopPropagation();
                              setEditProject(project);
                            }}
                            className="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-background/70 hover:text-foreground"
                            aria-label={tr("Edit {{name}}", "编辑 {{name}}", { name: project.name })}
                            title={tr("Edit project", "编辑项目")}
                          >
                            <Pencil className="size-4" />
                          </button>
                        </div>
                      </div>
                      {open && projectTopics.length > 0 && (
                        <div className="-ml-2 mt-1 w-[calc(100%+0.5rem)] space-y-1 pb-1">
                          {projectTopics.map((topic) => (
                            <BotTopicNavigationRow
                              key={topic.id}
                              topic={topic}
                              active={topic.id === activeTopicId}
                              nested
                              onSelect={() => onSelectTopic(topic.id)}
                              onRename={() => setEditTopic(topic)}
                              onDelete={() => setDeleteTopic(topic)}
                            />
                          ))}
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
            )}
            {projects.length > 5 && (
              <button
                type="button"
                onClick={() => setShowAllProjects((value) => !value)}
                className="mt-1 rounded-lg px-1 py-1 text-sm font-medium text-muted-foreground transition hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
              >
                {showAllProjects ? tr("Show less", "收起") : tr("Show all", "展开显示")}
              </button>
            )}
          </section>

          <section className="mt-5">
            <div className="mb-1 flex h-7 items-center justify-between px-1">
              <h3 className="text-sm font-semibold text-muted-foreground">{tr("Recent", "最近")}</h3>
              <Tooltip>
                <TooltipTrigger
                  render={
                    <button
                      type="button"
                      onClick={onNewTopic}
                      className="flex size-7 items-center justify-center rounded-md text-muted-foreground transition hover:bg-black/5 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring dark:hover:bg-white/8"
                      aria-label={tr("New chat", "新建对话")}
                    >
                      <SquarePen className="size-4" />
                    </button>
                  }
                />
                <TooltipContent side="left">
                  <span>{tr("New chat", "新建对话")}</span>
                  <kbd data-slot="kbd" className="bg-background/15 px-1.5 py-0.5 text-[10px]">⌘N</kbd>
                </TooltipContent>
              </Tooltip>
            </div>
            {visibleRecentTopics.length === 0 ? (
              <p className="px-2 py-1.5 text-sm leading-5 text-muted-foreground/75">{tr("Recent chats appear here after you send a message.", "发送消息后，最近话题会显示在这里。")}</p>
            ) : (
              <div>
                {visibleRecentTopics.map((topic) => {
                  const active = topic.id === activeTopicId;
                  const topicTitle = topic.title?.trim() || topic.preview?.trim() || tr("Untitled chat", "未命名话题");
                  return (
                    <div
                      key={topic.id}
                      className={`group relative -ml-2 w-[calc(100%+0.5rem)] rounded-md transition-colors ${
                        active
                          ? "bg-black/[0.07] font-medium text-foreground dark:bg-white/[0.11]"
                          : "hover:bg-black/[0.04] dark:hover:bg-white/[0.07]"
                      }`}
                    >
                      <button
                        type="button"
                        onClick={() => onSelectTopic(topic.id)}
                        className="flex w-full min-w-0 items-center gap-2.5 rounded-md py-1.5 pl-3 pr-9 text-left text-[15px] leading-5 focus-visible:ring-2 focus-visible:ring-ring"
                        aria-current={active ? "page" : undefined}
                      >
                        <ChatSessionLeadingVisual topic={topic} />
                        <span className="min-w-0 flex-1 truncate">{topicTitle}</span>
                      </button>
                      <DropdownMenu>
                        <DropdownMenuTrigger
                          render={
                            <button
                              type="button"
                              onClick={(event) => event.stopPropagation()}
                              className={`absolute right-1 top-1/2 flex size-6 -translate-y-1/2 items-center justify-center rounded-md text-muted-foreground transition-opacity hover:bg-background/60 hover:text-foreground aria-expanded:opacity-100 ${
                                active ? "opacity-100" : "opacity-0 group-hover:opacity-100 group-focus-within:opacity-100"
                              }`}
                              aria-label={tr("More actions for {{name}}", "{{name}} 的更多操作", { name: topicTitle })}
                            >
                              <MoreHorizontal className="size-4" />
                            </button>
                          }
                        />
                        <DropdownMenuContent align="end" className="w-36 rounded-xl">
                          <DropdownMenuItem onClick={() => setEditTopic(topic)}>
                            <Pencil className="size-4 text-muted-foreground" />
                            {tr("Rename", "重命名")}
                          </DropdownMenuItem>
                          <DropdownMenuItem
                            onClick={() => setDeleteTopic(topic)}
                            className="text-destructive focus:text-destructive"
                          >
                            <Trash2 className="size-4 text-destructive" />
                            {tr("Delete", "删除")}
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </div>
                  );
                })}
                {hiddenTopicCount > 0 && (
                  <button
                    type="button"
                    onClick={() => {
                      setVisibleTopicCount((current) =>
                        Math.min(current + 10, looseTopics.length),
                      );
                    }}
                    className="mt-1 flex h-9 w-full items-center gap-2.5 rounded-lg px-3 text-left text-sm font-medium text-muted-foreground transition hover:bg-black/[0.04] hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring dark:hover:bg-white/[0.07]"
                  >
                    <ChevronDown className="size-4 shrink-0" />
                    <span>{tr("Load more", "加载更多")}</span>
                    <span className="ml-auto text-xs tabular-nums text-muted-foreground/70">
                      {hiddenTopicCount}
                    </span>
                  </button>
                )}
              </div>
            )}
          </section>
        </div>
      </aside>
      {createProjectOpen && (
        <BotProjectCreateDialog
          agentId={agentId}
          onClose={() => setCreateProjectOpen(false)}
          onCreated={(project) => {
            onProjectsChanged();
            setCreateProjectOpen(false);
            onSelectProject(project.id);
          }}
        />
      )}
      {editProject && (
        <BotProjectEditDialog
          key={editProject.id}
          agentId={agentId}
          project={editProject}
          onClose={() => setEditProject(null)}
          onSaved={onProjectsChanged}
        />
      )}
      {editTopic && (
        <BotTopicEditDialog
          key={editTopic.id}
          agentId={agentId}
          topic={editTopic}
          onClose={() => setEditTopic(null)}
          onSaved={onTopicsChanged}
        />
      )}
      {deleteTopic && (
        <BotTopicDeleteDialog
          key={deleteTopic.id}
          agentId={agentId}
          topic={deleteTopic}
          onClose={() => setDeleteTopic(null)}
          onDeleted={() => {
            const deletedActiveTopic = deleteTopic.id === activeTopicId;
            setDeleteTopic(null);
            onTopicsChanged();
            if (deletedActiveTopic) onNewTopic();
          }}
        />
      )}
    </>
  );
}

function ChatSessionLeadingVisual({ topic }: { topic: ChatSession }) {
  const isWeb = !topic.channel || topic.channel === "web";
  if (isWeb && topic.thumbnailUrl) {
    return (
      // eslint-disable-next-line @next/next/no-img-element
      <img
        src={topic.thumbnailUrl}
        alt=""
        className="size-5 shrink-0 rounded-md object-cover"
      />
    );
  }
  if (isWeb) return null;
  return (
    <span className="flex size-5 shrink-0 items-center justify-center text-muted-foreground">
      <ChannelIcon channel={topic.channel} className="size-4 shrink-0" />
    </span>
  );
}

function BotTopicNavigationRow({
  topic,
  active,
  nested = false,
  onSelect,
  onRename,
  onDelete,
}: {
  topic: ChatSession;
  active: boolean;
  nested?: boolean;
  onSelect: () => void;
  onRename: () => void;
  onDelete: () => void;
}) {
  const { tr } = useLocale();
  const topicTitle = topic.title?.trim() || topic.preview?.trim() || tr("Untitled chat", "未命名话题");
  return (
    <div
      className={`group relative rounded-md transition-colors ${
        active
          ? "bg-black/[0.07] font-medium text-foreground dark:bg-white/[0.11]"
          : "hover:bg-black/[0.04] dark:hover:bg-white/[0.07]"
      }`}
    >
      <button
        type="button"
        onClick={onSelect}
        className={`flex w-full min-w-0 items-center gap-2.5 rounded-md py-1.5 pr-8 text-left leading-5 focus-visible:ring-2 focus-visible:ring-ring ${
          nested ? "pl-[2.75rem] text-[15px]" : "pl-2 text-[15px]"
        }`}
        aria-current={active ? "page" : undefined}
      >
        <ChatSessionLeadingVisual topic={topic} />
        <span className="min-w-0 flex-1 truncate">{topicTitle}</span>
      </button>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <button
              type="button"
              onClick={(event) => event.stopPropagation()}
              className={`absolute right-1 top-1/2 flex size-6 -translate-y-1/2 items-center justify-center rounded-md text-muted-foreground transition-opacity hover:bg-background/60 hover:text-foreground aria-expanded:opacity-100 ${
                active ? "opacity-100" : "opacity-0 group-hover:opacity-100 group-focus-within:opacity-100"
              }`}
              aria-label={tr("More actions for {{name}}", "{{name}} 的更多操作", { name: topicTitle })}
            >
              <MoreHorizontal className="size-4" />
            </button>
          }
        />
        <DropdownMenuContent align="end" className="w-36 rounded-xl">
          <DropdownMenuItem onClick={onRename}>
            <Pencil className="size-4 text-muted-foreground" />
            {tr("Rename", "重命名")}
          </DropdownMenuItem>
          <DropdownMenuItem
            onClick={onDelete}
            className="text-destructive focus:text-destructive"
          >
            <Trash2 className="size-4 text-destructive" />
            {tr("Delete", "删除")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

function BotProjectCreateDialog({
  agentId,
  onClose,
  onCreated,
}: {
  agentId: string;
  onClose: () => void;
  onCreated: (project: ProjectEntry) => void;
}) {
  const { tr } = useLocale();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const save = async () => {
    const nextName = name.trim();
    if (!nextName || saving) return;
    setSaving(true);
    setError("");
    try {
      const result = await createProject(agentId, {
        name: nextName,
        description: description.trim(),
      });
      if ("error" in result) {
        setError(result.error || tr("Creation failed. Try again.", "创建失败，请重试。"));
        return;
      }
      onCreated(result);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : tr("Creation failed. Try again.", "创建失败，请重试。"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{tr("New project", "新建项目")}</DialogTitle>
          <DialogDescription>{tr("Organize related chats and files in one project.", "将相关对话和文件整理到同一个项目中。")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div>
            <label className="mb-1.5 block text-xs font-medium" htmlFor="new-bot-project-name">{tr("Project name", "项目名称")}</label>
            <Input
              id="new-bot-project-name"
              autoFocus
              value={name}
              onChange={(event) => setName(event.target.value)}
              onKeyDown={(event) => {
                if (event.nativeEvent.isComposing || event.keyCode === 229) return;
                if (event.key === "Enter") {
                  event.preventDefault();
                  void save();
                }
              }}
            />
          </div>
          <div>
            <label className="mb-1.5 block text-xs font-medium" htmlFor="new-bot-project-description">{tr("Project description", "项目说明")}</label>
            <Textarea
              id="new-bot-project-description"
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              rows={3}
            />
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={saving}>{tr("Cancel", "取消")}</Button>
          <Button onClick={() => void save()} disabled={saving || !name.trim()}>
            {saving ? tr("Creating…", "创建中…") : tr("Create", "创建")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function BotTopicEditDialog({
  agentId,
  topic,
  onClose,
  onSaved,
}: {
  agentId: string;
  topic: ChatSession;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { tr } = useLocale();
  const currentTitle = topic.title?.trim() || topic.preview?.trim() || tr("Untitled chat", "未命名话题");
  const [title, setTitle] = useState(currentTitle);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const save = async () => {
    const nextTitle = title.trim();
    if (!nextTitle || saving) return;
    if (nextTitle === currentTitle) {
      onClose();
      return;
    }
    setSaving(true);
    setError("");
    try {
      const result = await renameChatSession(agentId, topic.id, nextTitle);
      if (result?.error) {
        setError(result.error);
        return;
      }
      onSaved();
      onClose();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : tr("Save failed. Try again.", "保存失败，请重试。"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{tr("Rename chat", "重命名对话")}</DialogTitle>
          <DialogDescription>{tr("Use a title that makes this chat easier to find.", "使用更容易查找的话题名称。")}</DialogDescription>
        </DialogHeader>
        <Input
          autoFocus
          value={title}
          onChange={(event) => setTitle(event.target.value)}
          onKeyDown={(event) => {
            if (event.nativeEvent.isComposing || event.keyCode === 229) return;
            if (event.key === "Enter") {
              event.preventDefault();
              void save();
            }
          }}
        />
        {error && <p className="text-sm text-destructive">{error}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={saving}>{tr("Cancel", "取消")}</Button>
          <Button onClick={() => void save()} disabled={saving || !title.trim()}>
            {saving ? tr("Saving…", "保存中…") : tr("Save", "保存")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function BotTopicDeleteDialog({
  agentId,
  topic,
  onClose,
  onDeleted,
}: {
  agentId: string;
  topic: ChatSession;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { tr } = useLocale();
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState("");
  const topicTitle = topic.title?.trim() || topic.preview?.trim() || tr("Untitled chat", "未命名话题");

  const remove = async () => {
    if (deleting) return;
    setDeleting(true);
    setError("");
    try {
      const result = await deleteChatSession(agentId, topic.id);
      if (result?.error) {
        setError(result.error);
        return;
      }
      onDeleted();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : tr("Deletion failed. Try again.", "删除失败，请重试。"));
    } finally {
      setDeleting(false);
    }
  };

  return (
    <AlertDialog open onOpenChange={(open) => !open && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{tr("Delete chat", "删除对话")}</AlertDialogTitle>
          <AlertDialogDescription>
            {tr("Delete “{{title}}”? The complete chat history will be permanently deleted.", "确定删除“{{title}}”吗？完整对话记录将被永久删除。", { title: topicTitle })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && <p className="text-sm text-destructive">{error}</p>}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={deleting}>{tr("Cancel", "取消")}</AlertDialogCancel>
          <AlertDialogAction
            disabled={deleting}
            onClick={(event) => {
              event.preventDefault();
              void remove();
            }}
            className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
          >
            {deleting ? tr("Deleting…", "删除中…") : tr("Delete", "删除")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function BotProjectEditDialog({
  agentId,
  project,
  onClose,
  onSaved,
}: {
  agentId: string;
  project: ProjectEntry;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { tr } = useLocale();
  const [name, setName] = useState(project.name);
  const [description, setDescription] = useState(project.description || "");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const save = async () => {
    const nextName = name.trim();
    if (!nextName || saving) return;
    setSaving(true);
    setError("");
    try {
      const result = await updateProject(agentId, project.id, {
        name: nextName,
        description: description.trim(),
      });
      if ("error" in result && result.error) {
        setError(result.error);
        return;
      }
      onSaved();
      onClose();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : tr("Save failed. Try again.", "保存失败，请重试。"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{tr("Edit project", "编辑项目")}</DialogTitle>
          <DialogDescription>{tr("Change the project name and description without moving its files.", "修改项目名称和说明，不会移动项目中的文件。")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div>
            <label className="mb-1.5 block text-xs font-medium" htmlFor="bot-project-name">{tr("Project name", "项目名称")}</label>
            <Input
              id="bot-project-name"
              autoFocus
              value={name}
              onChange={(event) => setName(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") void save();
              }}
            />
          </div>
          <div>
            <label className="mb-1.5 block text-xs font-medium" htmlFor="bot-project-description">{tr("Project description", "项目说明")}</label>
            <Textarea
              id="bot-project-description"
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              rows={3}
            />
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={saving}>{tr("Cancel", "取消")}</Button>
          <Button onClick={() => void save()} disabled={saving || !name.trim()}>
            {saving ? tr("Saving…", "保存中…") : tr("Save", "保存")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Renders a group of tool calls as a collapsible summary. When
 *  `nested`, the outer flex/max-width wrappers are dropped so a parent
 *  container (ToolRoundsBundle) can stack rounds without each one
 *  re-imposing its own bubble alignment. */
function ToolCallGroup({ msg, surfacedSrcs, agentId, sessionId, nested = false, roundIndex, subagentProgress, onKnowledgeCitationClick }: { msg: ChatMessage; surfacedSrcs?: ReadonlySet<string>; agentId: string; sessionId: string; nested?: boolean; roundIndex?: number; subagentProgress?: { iteration?: number; max?: number; phase?: "thinking" | "running" | "final-delivery" | "done"; tools?: string[] } | null; onKnowledgeCitationClick?: (source: KnowledgeSource) => void }) {
  const { tr } = useLocale();
  const [groupOpen, setGroupOpen] = useState(false);
  const [expandedTool, setExpandedTool] = useState<Record<string, boolean>>({});

  const tools = msg.toolCalls || [];
  const doneCount = tools.filter((tc) => tc.result != null).length;
  const allDone = doneCount === tools.length;

  // delegate_task is registered serial, so only the FIRST not-yet-
  // returned delegate_task in this round corresponds to the active
  // subagentProgress event stream. Older ones already finished;
  // later ones are queued on the mutex and have no progress yet.
  const activeDelegateId = (() => {
    for (const tc of tools) {
      if (tc.name === "delegate_task" && tc.result == null) {
        return tc.id;
      }
    }
    return null;
  })();

  const toggleTool = (id: string) =>
    setExpandedTool((prev) => ({ ...prev, [id]: !prev[id] }));

  const inner = (
    <>
      {/* Content before tools */}
      {msg.content && (
        <div className="bg-muted rounded-2xl rounded-bl-md px-4 py-2.5">
          {renderContentWithDataImages(msg.content, surfacedSrcs, false, agentId, sessionId, msg.metadata?.knowledgeSources, onKnowledgeCitationClick) ?? (
            <ChatMarkdown
              text={msg.content}
              agentId={agentId}
              sessionId={sessionId}
              knowledgeSources={msg.metadata?.knowledgeSources}
              onKnowledgeCitationClick={onKnowledgeCitationClick}
            />
          )}
        </div>
      )}
      {/* Collapsed tool group summary */}
      <div className="rounded-lg border border-border bg-card/50 overflow-hidden">
          <button
            onClick={() => setGroupOpen(!groupOpen)}
            className="flex w-full items-center gap-2 px-3 py-2 text-xs hover:bg-muted/50 transition-colors"
          >
            {!allDone ? (
              <div className="h-5 w-5 shrink-0 rounded-full border-2 border-amber-500 border-t-transparent animate-spin" />
            ) : roundIndex !== undefined ? (
              // When this group is a round inside a bundle, the leading
              // glyph carries the round number — gives the bundle's
              // expanded view a built-in step indicator without an
              // extra "ROUND N" label row above each card.
              <span className="h-5 w-5 shrink-0 inline-flex items-center justify-center rounded-full bg-amber-500/10 text-[11px] font-semibold text-amber-600 dark:text-amber-400">
                {roundIndex}
              </span>
            ) : (
              <Wrench className="h-3.5 w-3.5 text-amber-500 shrink-0" />
            )}
            <span className="font-medium text-foreground">
              {allDone
                ? tr("Executed {{count}} tool(s)", "已执行 {{count}} 个工具", { count: tools.length })
                : tr("Running tools ({{done}}/{{total}})…", "正在运行工具（{{done}}/{{total}}）…", { done: doneCount, total: tools.length })}
            </span>
            <span className="text-muted-foreground/60 text-[11px] flex-1 text-left truncate">
              {tools.map((tc) => tc.name).join(", ")}
            </span>
            {groupOpen ? (
              <ChevronDown className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
            )}
          </button>

          {groupOpen && (
            <div className="border-t border-border">
              {tools.map((tc) => (
                <div key={tc.id} className="border-b border-border last:border-b-0">
                  <button
                    onClick={() => toggleTool(tc.id)}
                    className="flex w-full items-center gap-2 px-3 py-1.5 text-xs hover:bg-muted/30 transition-colors"
                  >
                    {tc.result === undefined ? (
                      <div className="h-3 w-3 shrink-0 rounded-full border-2 border-amber-500/60 border-t-transparent animate-spin" />
                    ) : (
                      <Check className="h-3 w-3 text-emerald-500 shrink-0" />
                    )}
                    <span className="font-medium text-foreground">{tc.name}</span>
                    {tc.metadata?.sandbox && (
                      <span
                        className="flex items-center gap-0.5 rounded bg-emerald-500/10 px-1 py-0.5 text-[10px] font-medium text-emerald-600 dark:text-emerald-400"
                        title={tr("Executed inside a sandboxed container", "在沙盒容器中执行")}
                      >
                        <ShieldCheck className="h-2.5 w-2.5" />
                        {tr("sandbox", "沙盒")}
                      </span>
                    )}
                    <span className="text-muted-foreground/50 font-mono truncate flex-1 text-left text-[11px]">
                      {(() => {
                        try {
                          const args = JSON.parse(tc.arguments);
                          // delegate_task's `task` arg always opens with
                          // the same boilerplate ("You are a B2B lead
                          // researcher…"); the differentiating part is a
                          // markdown heading further down ("## Target:
                          // <industry>"). Surface that line instead of
                          // the head so a fan-out of N delegates doesn't
                          // look like N copies of the same call.
                          if (tc.name === "delegate_task" && typeof args.task === "string") {
                            const m = args.task.match(/^#+\s*Target:\s*(.+)$/m) ||
                                      args.task.match(/^#+\s+(.+)$/m);
                            if (m) return m[1].trim();
                            return args.task.replace(/\s+/g, " ").slice(0, 120);
                          }
                          return Object.values(args).join(", ");
                        } catch {
                          return tc.arguments;
                        }
                      })()}
                    </span>
                    {expandedTool[tc.id] ? (
                      <ChevronDown className="h-3 w-3 text-muted-foreground/50 shrink-0" />
                    ) : (
                      <ChevronRight className="h-3 w-3 text-muted-foreground/50 shrink-0" />
                    )}
                  </button>
                  {expandedTool[tc.id] && (
                    <div className="px-3 py-2 space-y-2 bg-muted/20">
                      <div>
                        <p className="text-[10px] font-medium text-muted-foreground uppercase mb-1">{tr("Input", "输入")}</p>
                        <pre className="text-xs font-mono bg-muted/50 rounded p-2 overflow-x-auto whitespace-pre-wrap break-all max-h-40">
                          {(() => {
                            try { return JSON.stringify(JSON.parse(tc.arguments), null, 2); }
                            catch { return tc.arguments; }
                          })()}
                        </pre>
                      </div>
                      {tc.result != null ? (
                        <div>
                          <p className="text-[10px] font-medium text-muted-foreground uppercase mb-1">{tr("Output", "输出")}</p>
                          <pre className="text-xs font-mono bg-muted/50 rounded p-2 overflow-x-auto whitespace-pre-wrap break-all max-h-60">
                            {tc.result.length > 2000 ? tc.result.slice(0, 2000) + "..." : tc.result}
                          </pre>
                        </div>
                      ) : tc.name === "delegate_task" && tc.id === activeDelegateId && subagentProgress ? (
                        <div className="text-xs text-muted-foreground/80 italic">
                          {(() => {
                            const it = subagentProgress.iteration;
                            const mx = subagentProgress.max;
                            const phase = subagentProgress.phase;
                            const tools = subagentProgress.tools;
                            const counter = it && mx
                              ? tr("Iteration {{current}}/{{max}}", "迭代 {{current}}/{{max}}", { current: it, max: mx })
                              : tr("Sub-agent running", "子 Agent 正在运行");
                            let detail = "";
                            if (phase === "thinking") detail = tr("thinking", "思考中");
                            else if (phase === "running" && tools?.length) detail = tr("running {{tools}}", "正在运行 {{tools}}", { tools: tools.join(", ") });
                            else if (phase === "final-delivery") detail = tr("synthesizing final answer", "正在整理最终回答");
                            return detail ? `${counter} · ${detail}` : counter;
                          })()}
                        </div>
                      ) : tc.name === "delegate_task" && tc.result == null && tc.id !== activeDelegateId ? (
                        <p className="text-xs text-muted-foreground/60 italic">{tr("Queued (waiting for the previous sub-agent)…", "已排队（等待前一个子 Agent）…")}</p>
                      ) : (
                        <p className="text-xs text-muted-foreground/60 italic">{tr("Executing…", "正在执行…")}</p>
                      )}
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      </>
    );
  if (nested) {
    return <div className="space-y-2">{inner}</div>;
  }
  return (
    <div className="flex justify-start">
      <div className="max-w-full space-y-2">{inner}</div>
    </div>
  );
}

/** ToolRoundsBundle wraps consecutive tool-group rounds (the agent
 *  ran tools, got results, then ran more tools, …) in a single
 *  collapsible header so a long ReAct turn doesn't take over the chat
 *  with seven independent "Executed N tools" boxes. The aggregate
 *  badge shows total rounds + total tools; expanding reveals each
 *  round as a regular ToolCallGroup, which itself stays collapsible
 *  per the existing per-round UX. Single-round bundles aren't built
 *  here — those still render as a flat ToolCallGroup so the extra
 *  layer doesn't show up unless it earns its keep. */
function ToolRoundsBundle({
  rounds,
  surfacedSrcs,
  agentId,
  sessionId,
  subagentProgress,
  onKnowledgeCitationClick,
}: {
  rounds: ChatMessage[];
  surfacedSrcs?: ReadonlySet<string>;
  agentId: string;
  sessionId: string;
  subagentProgress?: { iteration?: number; max?: number; phase?: "thinking" | "running" | "final-delivery" | "done"; tools?: string[] } | null;
  onKnowledgeCitationClick?: (source: KnowledgeSource) => void;
}) {
  const { tr } = useLocale();
  const [open, setOpen] = useState(false);
  const allTools = rounds.flatMap((r) => r.toolCalls || []);
  const totalTools = allTools.length;
  const doneCount = allTools.filter((tc) => tc.result != null).length;
  const allDone = doneCount === totalTools;
  return (
    <div className="flex justify-start">
      <div className="max-w-full w-full">
        <div className="rounded-lg border border-border bg-card/50 overflow-hidden">
          <button
            onClick={() => setOpen(!open)}
            className="flex w-full items-center gap-2 px-3 py-2 text-xs hover:bg-muted/50 transition-colors"
          >
            {!allDone ? (
              <div className="h-3.5 w-3.5 shrink-0 rounded-full border-2 border-amber-500 border-t-transparent animate-spin" />
            ) : (
              <Wrench className="h-3.5 w-3.5 text-amber-500 shrink-0" />
            )}
            <span className="font-medium text-foreground">
              {allDone
                ? tr("Used {{tools}} tool(s) across {{rounds}} round(s)", "共 {{rounds}} 轮，使用了 {{tools}} 个工具", { tools: totalTools, rounds: rounds.length })
                : tr("Running tools… ({{done}}/{{total}} across {{rounds}} rounds)", "正在运行工具…（{{rounds}} 轮中已完成 {{done}}/{{total}}）", { done: doneCount, total: totalTools, rounds: rounds.length })}
            </span>
            <span className="ml-auto" />
            {open ? (
              <ChevronDown className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
            )}
          </button>
          {open && (
            <div className="border-t border-border p-2 space-y-1.5 bg-background/30">
              {rounds.map((round, idx) => (
                <ToolCallGroup
                  key={round.id || idx}
                  msg={round}
                  surfacedSrcs={surfacedSrcs}
                  agentId={agentId}
                  sessionId={sessionId}
                  nested
                  roundIndex={idx + 1}
                  subagentProgress={subagentProgress}
                  onKnowledgeCitationClick={onKnowledgeCitationClick}
                />
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

/** File extension → icon + preview kind. */
function fileKind(path: string): { icon: typeof File; preview: "image" | "pdf" | "markdown" | "html" | "text" | "none" } {
  const ext = path.toLowerCase().split(".").pop() || "";
  if (["png", "jpg", "jpeg", "gif", "svg", "webp", "bmp", "ico"].includes(ext)) return { icon: ImageIcon, preview: "image" };
  if (ext === "pdf") return { icon: FileText, preview: "pdf" };
  if (ext === "md" || ext === "markdown") return { icon: FileText, preview: "markdown" };
  if (ext === "html" || ext === "htm") return { icon: FileCode, preview: "html" };
  if (["mp4", "webm", "mov", "mkv", "avi", "m4v"].includes(ext)) return { icon: Film, preview: "none" };
  if (["mp3", "wav", "ogg", "flac", "m4a", "aac"].includes(ext)) return { icon: Music, preview: "none" };
  // Genuinely binary formats → download only. Everything else is treated as
  // plain text below (so .env / Dockerfile / .sql / SKILL / extension-less /
  // unknown-but-textual files render their content instead of a download
  // prompt). `.text()` on a binary that slips through just shows garbage —
  // an acceptable trade for never hiding a readable file.
  if (
    [
      "zip", "tar", "gz", "tgz", "bz2", "7z", "rar", "xz", "zst",
      "woff", "woff2", "ttf", "otf", "eot",
      "exe", "dll", "so", "dylib", "bin", "dat", "wasm", "class", "node",
      "doc", "docx", "xls", "xlsx", "ppt", "pptx",
      "db", "sqlite", "sqlite3", "mdb",
    ].includes(ext)
  ) {
    return { icon: File, preview: "none" };
  }
  // Known code extensions get the code icon; all other textual files fall
  // through to a plain-text view with a generic file icon.
  if (
    ["js", "ts", "tsx", "jsx", "mjs", "cjs", "py", "go", "rs", "c", "cpp", "h", "java",
     "rb", "sh", "bash", "zsh", "json", "jsonc", "yaml", "yml", "toml", "xml", "css",
     "scss", "sql", "dockerfile"].includes(ext)
  ) {
    return { icon: FileCode, preview: "text" };
  }
  return { icon: FileText, preview: "text" };
}

function formatBytes(n?: number): string {
  if (n === undefined) return "";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

// zipUrl carries NO bearer token — same cookie-auth rationale as fileUrl
// (see lib/api): a token in the URL leaks a full API credential via Referer,
// history, and proxy logs.
function zipUrl(agentId: string, sessionId: string, projectId?: string): string {
  const params = new URLSearchParams();
  // projectId wins when both are present — same precedence as the
  // backend's fileScopeForRequest, which treats projectId-without-
  // session as "whole project zip".
  if (projectId) params.set("projectId", projectId);
  else if (sessionId) params.set("sessionId", sessionId);
  const qs = params.toString();
  return `/api/agents/${agentId}/files.zip${qs ? "?" + qs : ""}`;
}

// FilesPanel no longer inlines the produced-file list into the message
// bubble — a long workspace (skills/, .DS_Store, lockfiles, …) buried the
// reply. Instead it surfaces a single "Open files" affordance that opens
// the WorkspacePanel side sheet, which already handles the tree, preview,
// and download. onOpen is wired to setFilesSheetOpen(true) at the call site.
// BuildLogView renders the live scaffold/dev log as a scrolling terminal,
// auto-pinned to the bottom so the latest pnpm-install lines stay visible.
function BuildLogView({ text }: { text: string }) {
  const { tr } = useLocale();
  const ref = useRef<HTMLPreElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [text]);
  return (
    <pre
      ref={ref}
      className="h-full w-full overflow-auto whitespace-pre-wrap break-words bg-zinc-950 px-4 py-3 text-left font-mono text-[11px] leading-relaxed text-zinc-300"
    >
      {text || tr("Starting build…", "正在开始构建…")}
    </pre>
  );
}

function FilesPanel({ files, onOpen }: { files: ProducedFile[]; onOpen: () => void }) {
  const { tr } = useLocale();
  return (
    <div className="mt-2 max-w-[85%]">
      <button
        type="button"
        onClick={onOpen}
        className="group inline-flex items-center gap-2 rounded-lg border border-border bg-card/50 px-3 py-2 text-xs hover:bg-card/80 transition-colors"
        title={tr("Open workspace files", "打开工作区文件")}
      >
        <FolderOpen className="h-3.5 w-3.5 text-muted-foreground shrink-0 group-hover:text-foreground transition-colors" />
        <span className="font-medium text-foreground">{tr("Open files", "打开文件")}</span>
        <span className="rounded-full bg-muted px-1.5 py-0.5 text-[11px] font-medium text-muted-foreground/80 tabular-nums">
          {files.length}
        </span>
      </button>
    </div>
  );
}

const FILES_PANEL_MIN = 280;
const FILES_PANEL_MAX = 1000; // wide enough to view a desktop preview iframe
const FILES_PANEL_DEFAULT = BOT_PANEL_DEFAULT_WIDTH;
const FILES_PANEL_KEY = "chat:filesPanelWidth";
// When the user switches to the Preview tab and the panel is still narrow,
// auto-grow to this so the embedded site isn't cramped. Transient (not
// persisted), so the Code tab keeps its own saved width.
const PREVIEW_AUTO_WIDTH = 760;

function compactWorkspaceWidth(): number {
  if (typeof window === "undefined") return FILES_PANEL_DEFAULT;
  const botWidth = Number(window.localStorage.getItem(BOT_PANEL_WIDTH_KEY));
  return Number.isFinite(botWidth)
    && botWidth >= BOT_PANEL_MIN_WIDTH
    && botWidth <= BOT_PANEL_MAX_WIDTH
    ? botWidth
    : FILES_PANEL_DEFAULT;
}

function expandedWorkspaceWidth(): number {
  if (typeof window === "undefined") return PREVIEW_AUTO_WIDTH;
  const stored = Number(window.localStorage.getItem(FILES_PANEL_KEY));
  return Number.isFinite(stored)
    && stored >= PREVIEW_AUTO_WIDTH
    && stored <= FILES_PANEL_MAX
    ? stored
    : PREVIEW_AUTO_WIDTH;
}

// WorkspacePanel renders the files in the active scope:
//   - chat scope (sessionId set): files produced in this conversation.
//     Project chats also see root-level project files so shared notes
//     are visible alongside the chat's own outputs.
//   - project scope (projectId set, no session): every file under the
//     project — root-level + every chat's subtree. Used on the
//     /agents/<aid>/project/<pid> landing where no specific chat is
//     selected, so the user can still see what's accumulated in the
//     project.
// The agent's shared files (SKILL.md / main.py / templates) are
// excluded by the backend's scope filter so they can't leak into
// either view and confuse "what did this conversation produce".
// --- Workspace directory tree ---

type FileTreeNode = {
  name: string;
  path: string; // full workspace-relative path (files) or the folder path (dirs)
  isDir: boolean;
  size?: number;
  children: FileTreeNode[];
};

// buildFileTree turns the flat file list into a nested tree. stripPrefix (e.g.
// "sessions/<sid>/") is removed for the tree STRUCTURE so the session/project
// folder is the implicit root — but file leaves keep their FULL path, which the
// preview/download URLs need. Folders are synthesized from the remaining
// segments; their `path` is the relative path (a stable, unique toggle key).
function buildFileTree(files: WorkspaceFile[], stripPrefix: string): FileTreeNode[] {
  const root: FileTreeNode = { name: "", path: "", isDir: true, children: [] };
  for (const f of files) {
    const rel = stripPrefix && f.path.startsWith(stripPrefix)
      ? f.path.slice(stripPrefix.length)
      : f.path;
    const parts = rel.split("/").filter(Boolean);
    if (parts.length === 0) continue;
    let node = root;
    for (let i = 0; i < parts.length; i++) {
      const isLeaf = i === parts.length - 1;
      const name = parts[i];
      let child = node.children.find((c) => c.name === name && c.isDir === !isLeaf);
      if (!child) {
        child = isLeaf
          ? { name, path: f.path, isDir: false, size: f.size, children: [] }
          : { name, path: parts.slice(0, i + 1).join("/"), isDir: true, children: [] };
        node.children.push(child);
      }
      node = child;
    }
  }
  sortFileTree(root.children);
  return root.children;
}

function sortFileTree(nodes: FileTreeNode[]) {
  nodes.sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1; // folders before files
    return a.name.localeCompare(b.name);
  });
  for (const n of nodes) if (n.isDir) sortFileTree(n.children);
}

function FileTreeView({
  files,
  rootPrefix,
  selectedPath,
  onSelect,
  defaultExpandDepth = 1,
}: {
  files: WorkspaceFile[];
  rootPrefix: string;
  selectedPath?: string;
  onSelect: (f: ProducedFile) => void;
  // Folders shallower than this are open on first load (1 = open the root
  // folders only) so the user sees the top entries without a deep dump.
  defaultExpandDepth?: number;
}) {
  const tree = useMemo(() => buildFileTree(files, rootPrefix), [files, rootPrefix]);
  // Expansion state keys on stable relative paths, so it survives refreshes.
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  // Auto-expand the first `defaultExpandDepth` folder levels once, when the
  // tree first arrives (files load async). User toggles persist after that.
  const initedRef = useRef(false);
  useEffect(() => {
    if (initedRef.current || tree.length === 0) return;
    initedRef.current = true;
    const next = new Set<string>();
    const walk = (nodes: FileTreeNode[], depth: number) => {
      for (const n of nodes) {
        if (n.isDir && depth < defaultExpandDepth) {
          next.add(n.path);
          walk(n.children, depth + 1);
        }
      }
    };
    walk(tree, 0);
    setExpanded(next);
  }, [tree, defaultExpandDepth]);
  const toggle = useCallback((path: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  }, []);
  return (
    <div className="text-sm">
      {tree.map((n) => (
        <FileTreeRow
          key={n.path}
          node={n}
          depth={0}
          expanded={expanded}
          toggle={toggle}
          selectedPath={selectedPath}
          onSelect={onSelect}
        />
      ))}
    </div>
  );
}

function FileTreeRow({
  node,
  depth,
  expanded,
  toggle,
  selectedPath,
  onSelect,
}: {
  node: FileTreeNode;
  depth: number;
  expanded: Set<string>;
  toggle: (p: string) => void;
  selectedPath?: string;
  onSelect: (f: ProducedFile) => void;
}) {
  const pad = { paddingLeft: 8 + depth * 14 };
  if (node.isDir) {
    const open = expanded.has(node.path);
    return (
      <>
        <button
          onClick={() => toggle(node.path)}
          style={pad}
          className="flex w-full items-center gap-1.5 py-1 pr-2 rounded-md text-left hover:bg-muted/40"
        >
          {open ? (
            <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          )}
          <Folder className="h-4 w-4 shrink-0 text-muted-foreground" />
          <span className="truncate text-foreground">{node.name}</span>
        </button>
        {open &&
          node.children.map((c) => (
            <FileTreeRow
              key={c.path}
              node={c}
              depth={depth + 1}
              expanded={expanded}
              toggle={toggle}
              selectedPath={selectedPath}
              onSelect={onSelect}
            />
          ))}
      </>
    );
  }
  const { icon: Icon } = fileKind(node.path);
  const active = selectedPath === node.path;
  return (
    <button
      onClick={() => onSelect({ path: node.path, size: node.size })}
      style={pad}
      className={`flex w-full items-center gap-1.5 py-1 pr-2 rounded-md text-left ${active ? "bg-muted" : "hover:bg-muted/40"}`}
      title={node.path}
    >
      <span className="w-3.5 shrink-0" />
      <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
      <span className="truncate text-foreground">{node.name}</span>
    </button>
  );
}

// langForPath maps a file extension to a Shiki language id so the code
// preview can syntax-highlight via the markdown code-fence renderer.
function langForPath(path: string): string {
  const ext = path.toLowerCase().split(".").pop() || "";
  const map: Record<string, string> = {
    ts: "ts", tsx: "tsx", js: "js", jsx: "jsx", mjs: "js", cjs: "js",
    json: "json", css: "css", scss: "scss", html: "html", htm: "html",
    py: "python", go: "go", rs: "rust", rb: "ruby", java: "java",
    c: "c", cpp: "cpp", h: "c", sh: "bash", bash: "bash", zsh: "bash",
    yaml: "yaml", yml: "yaml", toml: "toml", xml: "xml", sql: "sql",
    md: "markdown", markdown: "markdown",
  };
  return map[ext] || "text";
}

function WorkspacePanel({
  agentId,
  sessionId,
  projectId,
  knowledgePreview,
  onClearKnowledgePreview,
  onPreviewStateChange,
  onBack,
  onClose,
}: {
  agentId: string;
  sessionId: string;
  projectId?: string;
  knowledgePreview?: KnowledgeSource | null;
  onClearKnowledgePreview?: () => void;
  onPreviewStateChange?: (active: boolean) => void;
  onBack?: () => void;
  onClose: () => void;
}) {
  const { locale, t, tr } = useLocale();
  const [files, setFiles] = useState<WorkspaceFile[]>([]);
  const [loading, setLoading] = useState(false);
  const [previewing, setPreviewing] = useState<ProducedFile | null>(null);
  // Live dev-server preview for this chat scope (from start_app_preview).
  const [appPreview, setAppPreview] = useState<ScopePreview>({ status: "none" });
  // Live build/dev log tail, shown in the preview pane while the app is
  // scaffolding so "Building…" isn't an opaque spinner.
  const [buildLogs, setBuildLogs] = useState("");
  // Code (file tree) vs Preview (embedded iframe of the running dev server).
  const [tab, setTab] = useState<"code" | "preview">("code");
  // Collapse the left file tree to give the viewer the full width.
  const [treeCollapsed, setTreeCollapsed] = useState(false);
  // Files the agent changed vs the template baseline (so the tree can show
  // just this task's output), and whether to show all files instead.
  const [changed, setChanged] = useState<{ files: WorkspaceFile[]; available: boolean }>({ files: [], available: false });
  const [showAll, setShowAll] = useState(false);
  // Workspace version history dropdown (per-session git snapshots).
  const [historyOpen, setHistoryOpen] = useState(false);
  const [history, setHistory] = useState<WorkspaceHistoryEntry[]>([]);
  const [restoring, setRestoring] = useState(false);
  // Self-hosted-only "open in Finder" affordance. We learn the deploy
  // mode from /api/me on mount; it doesn't change at runtime, so one
  // fetch per panel instance is enough. Hosted deployments leave this
  // null and the button never renders.
  const [deployMode, setDeployMode] = useState<"self-hosted" | "hosted" | null>(null);
  const [revealing, setRevealing] = useState(false);
  useEffect(() => {
    let cancelled = false;
    getMe()
      .then((m) => {
        if (cancelled) return;
        if (m.deployMode === "self-hosted" || m.deployMode === "hosted") {
          setDeployMode(m.deployMode);
        }
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);
  const [width, setWidth] = useState<number>(compactWorkspaceWidth);
  const [resizing, setResizing] = useState(false);

  // Measure the panel's ACTUAL rendered width (not the `width` state, which
  // the CSS maxWidth cap can shrink below on small viewports) so the header
  // can collapse its toolbar before it overflows and pushes a page scroll.
  const asideRef = useRef<HTMLElement>(null);
  const [panelW, setPanelW] = useState<number>(FILES_PANEL_DEFAULT);
  useEffect(() => {
    const el = asideRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((entries) => {
      for (const e of entries) setPanelW(e.contentRect.width);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  // Below this the secondary action icons fold into a "⋯" menu and the
  // "Files" label drops, so the header always fits the narrow panel.
  const compactHeader = panelW < 480;
  const viewerExpanded = tab === "preview" || Boolean(previewing || knowledgePreview);

  useEffect(() => {
    if (!resizing) return;
    const handleMove = (e: MouseEvent) => {
      const next = Math.min(
        FILES_PANEL_MAX,
        Math.max(FILES_PANEL_MIN, window.innerWidth - e.clientX),
      );
      setWidth(next);
    };
    const handleUp = () => {
      setResizing(false);
      if (viewerExpanded) {
        try {
          window.localStorage.setItem(FILES_PANEL_KEY, String(width));
        } catch { /* ignore quota errors */ }
      }
    };
    window.addEventListener("mousemove", handleMove);
    window.addEventListener("mouseup", handleUp);
    document.body.style.cursor = "col-resize";
    document.body.style.userSelect = "none";
    return () => {
      window.removeEventListener("mousemove", handleMove);
      window.removeEventListener("mouseup", handleUp);
      document.body.style.cursor = "";
      document.body.style.userSelect = "";
    };
  }, [resizing, viewerExpanded, width]);

  const handleReveal = useCallback(async () => {
    if (!agentId || (!sessionId && !projectId)) return;
    setRevealing(true);
    try {
      const res = await revealAgentWorkspace(agentId, sessionId || undefined, projectId);
      if (!res.ok) {
        // Best-effort UX — surface the error inline rather than a
        // toast lib we don't have. The message comes from the
        // backend (e.g. "S3-backed store, no host path").
        // eslint-disable-next-line no-alert
        alert(res.error || tr("Could not open workspace folder", "无法打开工作区文件夹"));
      }
    } finally {
      setRevealing(false);
    }
  }, [agentId, sessionId, projectId, tr]);

  const refresh = useCallback(async () => {
    // Project scope (no session) is handled via projectId; chat scope
    // requires sessionId. With neither, there's nothing to fetch.
    if (!agentId || (!sessionId && !projectId)) return;
    setLoading(true);
    try {
      // When projectId is set we skip sessionId — backend scope filter
      // expects exactly one of them to drive the prefix match. Mixing
      // them would fall into the chat-scope branch and miss other
      // chats' files.
      const list = projectId
        ? await listAgentFiles(agentId, undefined, projectId)
        : await listAgentFiles(agentId, sessionId);
      const cleaned = list
        .filter((f) => !isSystemFile(f.path))
        .sort((a, b) => (b.modTime || 0) - (a.modTime || 0));
      setFiles(cleaned);
      // Best-effort: is there a live app preview for this scope?
      getScopePreview(agentId, projectId ? undefined : sessionId, projectId)
        .then(setAppPreview)
        .catch(() => setAppPreview({ status: "none" }));
      // Best-effort: which files did the agent change vs the template?
      getChangedFiles(agentId, projectId ? undefined : sessionId, projectId)
        .then(setChanged)
        .catch(() => setChanged({ files: [], available: false }));
    } finally {
      setLoading(false);
    }
  }, [agentId, sessionId, projectId]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  // Workspace version history: load commits when the dropdown opens;
  // restore checks out the whole session workspace to that commit and
  // refreshes the file tree/viewer.
  const toggleHistory = useCallback(async () => {
    if (historyOpen) {
      setHistoryOpen(false);
      return;
    }
    if (!sessionId) return;
    setHistoryOpen(true);
    try {
      setHistory(await getSessionHistory(agentId, sessionId));
    } catch {
      setHistory([]);
    }
  }, [historyOpen, agentId, sessionId]);

  const handleRestore = useCallback(
    async (commit: string) => {
      if (!sessionId || restoring) return;
      // eslint-disable-next-line no-alert
      if (!window.confirm(tr("Restore this chat's workspace to this version? Current uncommitted file changes will be overwritten.", "将此会话的工作区回滚到该版本？当前未提交的文件修改会被覆盖。"))) return;
      setRestoring(true);
      try {
        await restoreSessionHistory(agentId, sessionId, commit);
        setHistoryOpen(false);
        setPreviewing(null);
        await refresh();
      } catch (error) {
        // eslint-disable-next-line no-alert
        alert(tr("Restore failed: {{error}}", "回滚失败：{{error}}", { error: String(error) }));
      } finally {
        setRestoring(false);
      }
    },
    [agentId, sessionId, restoring, refresh, tr],
  );

  // Switching conversations swaps the file tree to the new scope — clear the
  // selected file too, so the viewer never shows a file from the previous
  // conversation (the tree refetches but `previewing` would otherwise linger).
  useEffect(() => {
    setPreviewing(null);
  }, [agentId, sessionId, projectId]);

  // While the Preview tab is open, poll the runtime so a "building" preview
  // flips to the live iframe on its own (and reflects sleep/crash). Cheap
  // local call; stops when the tab closes.
  useEffect(() => {
    if (tab !== "preview") return;
    let active = true;
    const sid = projectId ? undefined : sessionId;
    const poll = async () => {
      const p = await getScopePreview(agentId, sid, projectId).catch(() => null);
      if (!active || !p) return;
      setAppPreview(p);
      // While building, tail the live install/dev output for the log pane.
      if (p.status === "scaffolding" || p.status === "starting") {
        const logs = await getScopePreviewLogs(agentId, sid, projectId).catch(() => "");
        if (active) setBuildLogs(logs);
      }
    };
    poll();
    const t = setInterval(poll, 4000);
    return () => {
      active = false;
      clearInterval(t);
    };
  }, [tab, agentId, sessionId, projectId]);

  // Default to the same width as the Bot panel and show only the file tree.
  // Selecting a file (or the live Preview tab) promotes the workspace into
  // its wide inspection mode; closing the viewer returns it to navigation.
  useEffect(() => {
    setWidth(viewerExpanded ? expandedWorkspaceWidth() : compactWorkspaceWidth());
    if (!viewerExpanded) setTreeCollapsed(false);
    onPreviewStateChange?.(viewerExpanded);
    return () => {
      if (viewerExpanded) onPreviewStateChange?.(false);
    };
  }, [onPreviewStateChange, viewerExpanded]);

  // The Preview tab only exists for coding projects with a live dev server.
  // When there's no app preview, hide the tab and snap back to Files.
  const hasPreview = appPreview.status !== "none";
  useEffect(() => {
    if (!hasPreview && tab === "preview") setTab("code");
  }, [hasPreview, tab]);

  return (
    <>
      <aside
        ref={asideRef}
        // width is the dragged/auto px width, but cap it to 70% of THIS panel's
        // own flex container (the chat-area minus the platform sidebar) — NOT
        // the viewport. A container-relative cap auto-shrinks the panel when the
        // sidebar expands (the container narrows, 70% narrows with it), so the
        // chat always keeps ≥30% and the page never scrolls horizontally. min()
        // still bounds it to FILES_PANEL_MAX on very wide screens. overflow-
        // hidden is the belt-and-suspenders against inner content overflow.
        style={{ width, maxWidth: `min(${FILES_PANEL_MAX}px, 70%)` }}
        className={`relative z-30 hidden md:flex shrink-0 flex-col overflow-hidden border-l border-border bg-background -mt-14 h-screen ${
          resizing ? "" : "transition-[width] duration-200 ease-out motion-reduce:transition-none"
        }`}
      >
        <div
          onMouseDown={(e) => { e.preventDefault(); setResizing(true); }}
          className={`absolute left-0 top-0 bottom-0 w-2 cursor-col-resize z-10 group ${resizing ? "" : ""}`}
          title={tr("Drag to resize", "拖动调整大小")}
        >
          <div
            className={`absolute inset-y-0 left-1/2 w-px -translate-x-1/2 transition-colors ${
              resizing ? "bg-primary" : "bg-transparent group-hover:bg-primary/40"
            }`}
          />
        </div>
        <div className="flex h-12 items-center justify-between gap-2 border-b border-border px-4">
          <div className="flex min-w-0 items-center gap-2 text-sm font-medium">
            {onBack ? (
              <button
                type="button"
                onClick={onBack}
                className="-ml-1 flex size-7 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
                aria-label={t("workspace.back")}
                title={t("workspace.back")}
              >
                <ArrowLeft className="size-4" />
              </button>
            ) : (
              <FolderOpen className="h-4 w-4 shrink-0" />
            )}
            {!compactHeader && (
              <span className="truncate">
                {knowledgePreview ? tr("Knowledge", "知识库") : t("workspace.title")}
              </span>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-1">
            {/* Secondary actions: inline on a wide panel, folded into a "⋯"
                menu when the panel is narrow so the toolbar never overflows
                and pushes a horizontal page scroll. */}
            {compactHeader ? (
              <DropdownMenu>
                <DropdownMenuTrigger
                  render={
                    <button
                      className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground"
                      title={tr("More actions", "更多操作")}
                    >
                      <MoreHorizontal className="h-4 w-4" />
                    </button>
                  }
                />
                <DropdownMenuContent align="end" className="w-44 rounded-lg">
                  {appPreview.status === "running" && appPreview.previewUrl && (
                    <DropdownMenuItem
                      onClick={() =>
                        window.open(appPreview.previewUrl!, "_blank", "noopener,noreferrer")
                      }
                    >
                      <ExternalLink className="h-4 w-4 text-muted-foreground" />
                      <span>{tr("Open in new tab", "在新标签页中打开")}</span>
                    </DropdownMenuItem>
                  )}
                  <DropdownMenuItem
                    disabled={files.length === 0}
                    onClick={() => {
                      if (files.length === 0) return;
                      const a = document.createElement("a");
                      a.href = zipUrl(agentId, sessionId, projectId);
                      a.rel = "noopener";
                      a.click();
                    }}
                  >
                    <Download className="h-4 w-4 text-muted-foreground" />
                    <span>{tr("Download zip", "下载 ZIP")}</span>
                  </DropdownMenuItem>
                  {deployMode === "self-hosted" && (
                    <DropdownMenuItem
                      disabled={revealing || (!sessionId && !projectId)}
                      onClick={handleReveal}
                    >
                      <FolderSearch className="h-4 w-4 text-muted-foreground" />
                      <span>{tr("Open in Finder", "在访达中打开")}</span>
                    </DropdownMenuItem>
                  )}
                  <DropdownMenuItem disabled={loading} onClick={refresh}>
                    <RefreshCw className="h-4 w-4 text-muted-foreground" />
                    <span>{tr("Refresh", "刷新")}</span>
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            ) : (
              <>
                {appPreview.status === "running" && appPreview.previewUrl && (
                  <a
                    href={appPreview.previewUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground"
                    title={tr("Open preview in a new tab: {{url}}", "在新标签页中打开预览：{{url}}", { url: appPreview.previewUrl })}
                  >
                    <ExternalLink className="h-4 w-4" />
                  </a>
                )}
                <a
                  href={files.length > 0 ? zipUrl(agentId, sessionId, projectId) : undefined}
                  aria-disabled={files.length === 0}
                  className={`rounded-md p-1.5 transition-colors ${
                    files.length === 0
                      ? "pointer-events-none text-muted-foreground/40"
                      : "text-muted-foreground hover:bg-muted/50 hover:text-foreground"
                  }`}
                  title={tr("Download all as zip", "下载全部文件为 ZIP")}
                >
                  <Download className="h-4 w-4" />
                </a>
                {deployMode === "self-hosted" && (
                  <button
                    onClick={handleReveal}
                    disabled={revealing || (!sessionId && !projectId)}
                    className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground disabled:opacity-50"
                    title={tr("Open folder in Finder", "在访达中打开文件夹")}
                  >
                    <FolderSearch className="h-4 w-4" />
                  </button>
                )}
                <button
                  onClick={refresh}
                  disabled={loading}
                  className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground disabled:opacity-50"
                  title={tr("Refresh", "刷新")}
                >
                  <RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} />
                </button>
                {sessionId && !projectId && (
                  <span className="relative">
                    <button
                      onClick={toggleHistory}
                      disabled={restoring}
                      className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground disabled:opacity-50"
                      title={tr("Version history (restore workspace)", "版本历史（回滚工作区）")}
                    >
                      <RotateCcw className={`h-4 w-4 ${restoring ? "animate-spin" : ""}`} />
                    </button>
                    {historyOpen && (
                      <div className="absolute right-0 top-8 z-20 w-72 rounded-md border border-border bg-popover p-1 shadow-lg">
                        <div className="px-2 py-1 text-xs font-medium text-muted-foreground">{tr("Version history", "版本历史")}</div>
                        {history.length === 0 ? (
                          <div className="px-2 py-3 text-center text-xs text-muted-foreground">{tr("No history snapshots", "暂无历史快照")}</div>
                        ) : (
                          history.slice(0, 20).map((entry) => (
                            <div
                              key={entry.hash}
                              className="flex items-center justify-between gap-2 rounded px-2 py-1 hover:bg-muted/50"
                            >
                              <div className="min-w-0 flex-1">
                                <div className="truncate text-xs">{entry.message}</div>
                                <div className="text-[10px] text-muted-foreground">
                                  {entry.hash.slice(0, 7)} · {new Date(entry.time * 1000).toLocaleString(locale === "zh-CN" ? "zh-CN" : "en-US")}
                                </div>
                              </div>
                              <button
                                onClick={() => handleRestore(entry.hash)}
                                disabled={restoring}
                                className="shrink-0 rounded bg-muted px-2 py-0.5 text-xs hover:bg-accent disabled:opacity-50"
                              >
                                {tr("Restore", "回滚")}
                              </button>
                            </div>
                          ))
                        )}
                      </div>
                    )}
                  </span>
                )}
              </>
            )}
            <button
              onClick={onClose}
              className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground"
              title={tr("Close", "关闭")}
            >
              <X className="h-4 w-4" />
            </button>
          </div>
        </div>
        {/* Row 2: Files (tree) / Preview (dev server) toggle + tree collapse.
            The Preview tab is shown only for coding projects with a live dev
            server; a plain file session (a PDF, some docs) just shows Files. */}
        <div className="flex h-10 items-center justify-between gap-2 border-b border-border px-3">
          {hasPreview ? (
            <div className="flex items-center rounded-md bg-muted p-0.5 text-xs">
              <button
                onClick={() => setTab("code")}
                className={`rounded px-2.5 py-1 transition-colors ${
                  tab === "code"
                    ? "bg-background font-medium text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground"
                }`}
              >
                {tr("Files", "文件")}
              </button>
              <button
                onClick={() => setTab("preview")}
                className={`flex items-center gap-1 rounded px-2.5 py-1 transition-colors ${
                  tab === "preview"
                    ? "bg-background font-medium text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground"
                }`}
              >
                {tr("Preview", "预览")}
                {(appPreview.status === "starting" || appPreview.status === "scaffolding") && (
                  <RefreshCw className="h-3 w-3 animate-spin" />
                )}
              </button>
            </div>
          ) : (
            <span className="px-1 text-xs font-medium text-muted-foreground">{tr("Files", "文件")}</span>
          )}
          {tab === "code" && viewerExpanded && (
            <button
              onClick={() => setTreeCollapsed((c) => !c)}
              className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground"
              title={treeCollapsed ? tr("Show file tree", "显示文件树") : tr("Hide file tree", "隐藏文件树")}
            >
              {treeCollapsed ? <PanelLeftOpen className="h-4 w-4" /> : <PanelLeftClose className="h-4 w-4" />}
            </button>
          )}
        </div>
        {tab === "code" ? (
          <div className="flex min-h-0 flex-1">
            {/* Left: file tree (collapsible). */}
            {!(viewerExpanded && treeCollapsed) && (
            <div className={`flex flex-col ${viewerExpanded ? "w-56 shrink-0 border-r border-border" : "w-full"}`}>
              {/* When there's a template baseline, default to showing only the
                  files THIS task changed; let the user flip to the full tree. */}
              {changed.available && (
                <div className="flex items-center gap-1 border-b border-border px-3 py-1.5 text-xs">
                  <button
                    onClick={() => setShowAll(false)}
                    className={`rounded px-2 py-0.5 transition-colors ${
                      !showAll ? "bg-muted font-medium text-foreground" : "text-muted-foreground hover:text-foreground"
                    }`}
                  >
                    {tr("Changed", "已更改")}{changed.files.length ? ` (${changed.files.length})` : ""}
                  </button>
                  <button
                    onClick={() => setShowAll(true)}
                    className={`rounded px-2 py-0.5 transition-colors ${
                      showAll ? "bg-muted font-medium text-foreground" : "text-muted-foreground hover:text-foreground"
                    }`}
                  >
                    {tr("All files", "全部文件")}
                  </button>
                </div>
              )}
              <div className="flex-1 overflow-y-auto p-2">
                {(() => {
                  const showChanged = changed.available && !showAll;
                  const list = showChanged ? changed.files : files;
                  if (!loading && list.length === 0) {
                    return (
                      <p className="px-3 py-8 text-center text-sm text-muted-foreground">
                        {showChanged
                          ? tr("No changes yet — the agent has not edited any files.", "还没有更改——Agent 尚未编辑任何文件。")
                          : projectId
                            ? tr("No files in this project yet.", "此项目中还没有文件。")
                            : tr("No files in this session yet.", "此会话中还没有文件。")}
                      </p>
                    );
                  }
                  return (
                    <FileTreeView
                      files={list}
                      rootPrefix={projectId ? `projects/${projectId}/` : `sessions/${sessionId}/`}
                      selectedPath={previewing?.path}
                      onSelect={(f) => {
                        onClearKnowledgePreview?.();
                        setPreviewing(f);
                      }}
                    />
                  );
                })()}
              </div>
            </div>
            )}
            {/* Right: viewer for the selected file — overflow-hidden so wide
                content (a PDF, long code lines) never scrolls the panel. */}
            {viewerExpanded && (
            <div className="min-w-0 flex-1 overflow-hidden">
              {knowledgePreview ? (
                <KnowledgeFileViewer
                  key={`${knowledgePreview.id}-${knowledgePreview.path}`}
                  agentId={agentId}
                  source={knowledgePreview}
                  onClose={onClearKnowledgePreview}
                />
              ) : previewing ? (
                <FileViewer
                  // Remount on file change so text/error/view state resets and
                  // the new file's content is fetched (not the stale previous).
                  key={previewing.path}
                  agentId={agentId}
                  file={previewing}
                  onClose={() => setPreviewing(null)}
                />
              ) : null}
            </div>
            )}
          </div>
        ) : (
          <div className="flex-1 min-h-0">
            {appPreview.status === "running" && appPreview.previewUrl ? (
              <iframe
                src={appPreview.previewUrl}
                className="h-full w-full border-0 bg-white"
                title={tr("App preview", "应用预览")}
              />
            ) : appPreview.status === "starting" || appPreview.status === "scaffolding" ? (
              <div className="flex h-full flex-col">
                <div className="flex items-center gap-2 border-b border-border px-4 py-2 text-xs text-muted-foreground">
                  <RefreshCw className="h-4 w-4 shrink-0 animate-spin" />
                  <span>
                    {appPreview.status === "scaffolding"
                      ? tr("Installing dependencies — this can take a few minutes…", "正在安装依赖——可能需要几分钟…")
                      : tr("Starting the dev server…", "正在启动开发服务器…")}
                  </span>
                </div>
                <div className="min-h-0 flex-1">
                  <BuildLogView text={buildLogs} />
                </div>
              </div>
            ) : appPreview.status === "crashed" ? (
              <div className="flex h-full flex-col items-center justify-center gap-2 px-6 text-center">
                <p className="text-sm text-destructive">{tr("Preview failed to start.", "预览启动失败。")}</p>
                <p className="text-xs text-muted-foreground">
                  {tr("Ask the agent to check the development server logs (app_preview_logs).", "请让 Agent 检查开发服务器日志（app_preview_logs）。")}
                </p>
              </div>
            ) : (
              <div className="flex h-full flex-col items-center justify-center gap-2 px-6 text-center text-muted-foreground">
                <Eye className="h-6 w-6" />
                <p className="text-sm">{tr("No preview yet.", "还没有预览。")}</p>
                <p className="text-xs">
                  {tr("Ask the agent to build an app, and it will appear here.", "让 Agent 构建应用后，预览会显示在这里。")}
                </p>
              </div>
            )}
          </div>
        )}
      </aside>
    </>
  );
}

// FileViewer renders a selected workspace file inline (right column of the
// Files tab): image / pdf / markdown / highlighted text / rendered-or-source
// HTML. onClose, when given, deselects the file.
function KnowledgeFileViewer({ agentId, source, onClose }: { agentId: string; source: KnowledgeSource; onClose?: () => void }) {
  const { tr } = useLocale();
  const storedName = source.path.startsWith("knowledge/") ? source.path.slice("knowledge/".length) : source.path;
  const [file, setFile] = useState<{ name: string; content: string; size: number; hash?: string } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [view, setView] = useState<"rendered" | "source">("source");

  useEffect(() => {
    let cancelled = false;
    getAgentKnowledgeFile(agentId, storedName)
      .then((data) => {
        if (!cancelled) setFile({ name: data.name || source.file, content: data.content || "", size: data.size || 0, hash: data.hash });
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      });
    return () => {
      cancelled = true;
    };
  }, [agentId, source.file, storedName]);

  const displayName = file?.name || source.file || storedName;
  const preview = fileKind(displayName).preview;
  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-between border-b border-border px-4 py-2 shrink-0">
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <BookOpen className="h-4 w-4 text-muted-foreground shrink-0" />
            <span className="truncate text-sm font-medium">{displayName}</span>
          </div>
          <p className="mt-0.5 truncate text-[11px] text-muted-foreground">
            {source.id}
            {source.chunk ? ` · ${tr("chunk", "分块")} ${source.chunk}` : ""}
            {source.score ? ` · ${tr("score", "相关度")} ${source.score}` : ""}
            {file?.hash ? ` · ${file.hash.slice(0, 8)}` : ""}
          </p>
        </div>
        <div className="flex items-center gap-1 shrink-0">
          {(preview === "markdown" || preview === "html") && (
            <button
              onClick={() => setView(view === "rendered" ? "source" : "rendered")}
              className="rounded-md p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted/50"
              title={view === "rendered" ? tr("View source", "查看源码") : tr("View rendered", "查看渲染结果")}
            >
              {view === "rendered" ? <Code2 className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
            </button>
          )}
          {onClose && (
            <button
              onClick={onClose}
              className="rounded-md p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted/50"
              title={tr("Close knowledge file", "关闭知识库文件")}
            >
              <X className="h-4 w-4" />
            </button>
          )}
        </div>
      </div>
      <div className="min-h-0 flex-1">
        {error ? (
          <p className="p-4 text-sm text-destructive">{tr("Failed to load: {{error}}", "加载失败：{{error}}", { error })}</p>
        ) : !file ? (
          <p className="p-4 text-sm text-muted-foreground">{tr("Loading…", "正在加载…")}</p>
        ) : preview !== "text" && view === "rendered" ? (
          <div className="h-full overflow-auto p-4">
            <ChatMarkdown text={file.content} />
          </div>
        ) : file.content.includes("```") ? (
          <pre className="h-full overflow-auto whitespace-pre-wrap break-all p-3 font-mono text-xs">{file.content}</pre>
        ) : (
          <div className="h-full overflow-auto">
            <ChatMarkdown bareCode text={"```" + langForPath(displayName) + "\n" + file.content + "\n```"} />
          </div>
        )}
      </div>
    </div>
  );
}

function FileViewer({ agentId, file, onClose }: { agentId: string; file: ProducedFile; onClose?: () => void }) {
  const { tr } = useLocale();
  const { preview } = fileKind(file.path);
  const src = fileUrl(agentId, file.path, false);
  const downloadUrl = fileUrl(agentId, file.path, true);
  const basename = file.path.split("/").pop() || file.path;
  const [text, setText] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Default to SOURCE for markdown/html — clicking a file shows its code; the
  // toggle flips to rendered when wanted.
  const [view, setView] = useState<"rendered" | "source">("source");

  useEffect(() => {
    // Fetch the raw text for anything we show as source: markdown, code/text,
    // and html (html starts in source view too).
    if (preview !== "markdown" && preview !== "text" && preview !== "html") return;
    if (text !== null) return;
    let cancelled = false;
    fetch(src)
      .then((r) => { if (!r.ok) throw new Error(`HTTP ${r.status}`); return r.text(); })
      .then((t) => { if (!cancelled) setText(t); })
      .catch((e) => { if (!cancelled) setError(String(e)); });
    return () => { cancelled = true; };
  }, [src, preview, text]);

  return (
    <div className="flex h-full flex-col">
        <div className="flex items-center justify-between border-b border-border px-4 py-2 shrink-0">
          <div className="flex items-center gap-2 min-w-0">
            <FileText className="h-4 w-4 text-muted-foreground shrink-0" />
            <span className="font-medium text-sm truncate">{basename}</span>
            {file.size !== undefined && (
              <span className="text-[11px] text-muted-foreground shrink-0">{formatBytes(file.size)}</span>
            )}
          </div>
          <div className="flex items-center gap-1 shrink-0">
            {(preview === "html" || preview === "markdown") && (
              <button
                onClick={() => setView(view === "rendered" ? "source" : "rendered")}
                className="rounded-md p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted/50"
                title={view === "rendered" ? tr("View source", "查看源码") : tr("View rendered", "查看渲染结果")}
              >
                {view === "rendered" ? <Code2 className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
            )}
            <a
              href={src}
              target="_blank"
              rel="noopener noreferrer"
              className="rounded-md p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted/50"
              title={tr("Open in new tab", "在新标签页中打开")}
            >
              <ExternalLink className="h-4 w-4" />
            </a>
            {onClose && (
              <button
                onClick={onClose}
                className="rounded-md p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted/50"
                title={tr("Close file", "关闭文件")}
              >
                <X className="h-4 w-4" />
              </button>
            )}
          </div>
        </div>
        <div className="min-h-0 flex-1">
          {preview === "image" && (
            <div className="flex h-full items-center justify-center overflow-auto p-4">
              <img src={src} alt={basename} className="max-h-full max-w-full object-contain" />
            </div>
          )}
          {preview === "pdf" && (
            <iframe src={src} className="h-full w-full border-0" title={basename} />
          )}
          {(preview === "markdown" || preview === "text" || preview === "html") && (
            // Rendered view (markdown / html) only when toggled; otherwise the
            // default is full-bleed highlighted SOURCE.
            preview !== "text" && view === "rendered" ? (
              preview === "html" ? (
                // sandbox="allow-scripts": CSS/animations work; scripts can't
                // reach parent cookies/storage — safe for untrusted output.
                <iframe
                  src={src}
                  sandbox="allow-scripts"
                  className="h-full w-full border-0 bg-white"
                  title={basename}
                />
              ) : (
                <div className="h-full overflow-auto p-4">
                  <ChatMarkdown text={text ?? ""} />
                </div>
              )
            ) : error ? (
              <p className="p-4 text-sm text-destructive">{tr("Failed to load: {{error}}", "加载失败：{{error}}", { error })}</p>
            ) : text === null ? (
              <p className="p-4 text-sm text-muted-foreground">{tr("Loading…", "正在加载…")}</p>
            ) : text.includes("```") ? (
              // Content with its own fences would break the fenced wrapper —
              // fall back to a plain (unhighlighted) full-bleed block.
              <pre className="h-full overflow-auto whitespace-pre-wrap break-all p-3 font-mono text-xs">{text}</pre>
            ) : (
              // Shiki-highlighted source, no card / copy pill / padding —
              // fills the pane edge-to-edge.
              <div className="h-full overflow-auto">
                <ChatMarkdown bareCode text={"```" + langForPath(file.path) + "\n" + text + "\n```"} />
              </div>
            )
          )}
          {preview === "none" && (
            <div className="flex h-full flex-col items-center justify-center gap-3 p-4 text-center">
              <File className="h-12 w-12 text-muted-foreground/50" />
              <p className="text-sm text-muted-foreground">{tr("Preview is not available for this file type.", "此文件类型不支持预览。")}</p>
              <a href={downloadUrl} className="inline-flex items-center gap-2 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90">
                <Download className="h-3.5 w-3.5" /> {tr("Download", "下载")}
              </a>
            </div>
          )}
        </div>
    </div>
  );
}

function SlashMenu({
  items,
  activeIndex,
  onHover,
  onSelect,
}: {
  items: SlashItem[];
  activeIndex: number;
  onHover: (i: number) => void;
  onSelect: (s: SlashItem) => void;
}) {
  const { tr } = useLocale();
  const commandDescriptions: Record<string, string> = {
    new: tr("Clear session history", "清空会话历史"),
    reset: tr("Clear session history", "清空会话历史"),
    retry: tr("Re-run last message", "重新运行上一条消息"),
    undo: tr("Undo last turn", "撤销上一轮对话"),
    compact: tr("Compress context window", "压缩上下文窗口"),
    status: tr("Agent status & memory info", "Agent 状态与记忆信息"),
    usage: tr("Billing usage and session stats", "计费用量与会话统计"),
    insights: tr("Activity insights (last N days)", "最近 N 天的活动分析"),
    personality: tr("List or switch personality", "查看或切换个性") ,
    model: tr("Show or switch LLM model", "查看或切换模型"),
    goal: tr("Persistent multi-turn objective", "持续多轮目标"),
    help: tr("Show command help", "显示命令帮助"),
    version: tr("Show version", "显示版本"),
  };
  return (
    <div className="absolute bottom-full left-0 right-0 mb-2 rounded-xl border border-border bg-popover shadow-lg overflow-hidden z-20">
      <div className="max-h-[320px] overflow-y-auto py-1">
        {items.map((it, i) => {
          const isCmd = it.kind === "command";
          const Icon = isCmd ? Terminal : Puzzle;
          const badge = isCmd ? tr("command", "命令") : (it.type || tr("skill", "技能"));
          const label = isCmd ? `/${it.name}` : it.name;
          return (
            <button
              key={`${it.kind}-${it.name}`}
              // onMouseDown fires before the textarea's onBlur so the click
              // isn't swallowed by the blur-driven menu close.
              onMouseDown={(e) => {
                e.preventDefault();
                onSelect(it);
              }}
              onMouseEnter={() => onHover(i)}
              className={`w-full flex items-start gap-3 px-3 py-2 text-left transition-colors ${
                i === activeIndex ? "bg-muted/60" : "hover:bg-muted/40"
              }`}
            >
              <Icon className="h-4 w-4 mt-0.5 shrink-0 text-muted-foreground" />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <p className="text-sm font-medium truncate">{label}</p>
                  <span className="text-[10px] uppercase tracking-wider text-muted-foreground/70">
                    {badge}
                  </span>
                </div>
                {it.description && (
                  <p className="text-xs text-muted-foreground line-clamp-1">
                    {isCmd ? commandDescriptions[it.name] || it.description : it.description}
                  </p>
                )}
              </div>
            </button>
          );
        })}
      </div>
      <Link
        href="/skills/"
        className="flex items-center gap-2 border-t border-border px-3 py-2 text-xs text-muted-foreground hover:text-foreground hover:bg-muted/30 transition-colors"
      >
        <SlidersHorizontal className="h-3.5 w-3.5" />
        {tr("Manage Skills", "管理技能")}
      </Link>
    </div>
  );
}
