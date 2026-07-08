"use client";

import { useEffect, useState, useRef, useCallback, useMemo } from "react";
import { useTranslations } from "next-intl";
import { useRouter, usePathname, useSearchParams } from "next/navigation";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { fileUrl, getAgent, getAgentKnowledgeFile, getChangedFiles, getChatHistoryWithCursor, getChatSessions, getChatTodo, getMe, getScopePreview, getScopePreviewLogs, listAgentFiles, listProjects, renameChatSession, revealAgentWorkspace, sendChatStream, steerChat, uploadAgentFiles, getSkills, type ChatHistoryMessage, type ChatStreamEvent, type KnowledgeSource, type ScopePreview, type SkillInfo, type TodoItem, type ToolResultMetadata, type WorkspaceFile } from "@/lib/api";
import { Bot, Send, Copy, Check, Pencil, Wrench, ChevronDown, ChevronRight, Download, X, File, FileText, Folder, FolderSearch, Image as ImageIcon, FileCode, Film, Music, Puzzle, SlidersHorizontal, ShieldCheck, Paperclip, Square, FolderOpen, RefreshCw, Eye, Code2, RotateCcw, ListChecks, Terminal, ExternalLink, MoreHorizontal, PanelLeftClose, PanelLeftOpen, BookOpen } from "lucide-react";
import Link from "next/link";
import { ChatMarkdown } from "@/components/chat-markdown";

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
import { channelLabel } from "@/components/channel-icon";

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
// Descriptions come from the chat.* message namespace, so the command
// list is a builder over the active translator (memoized in the
// component) instead of a module constant. Command NAMES stay literal —
// they're the wire tokens dispatched to slash.go.
type ChatTranslator = ReturnType<typeof useTranslations<"chat">>;
const BUILTIN_COMMANDS = (t: ChatTranslator): SlashCommand[] => [
  { name: "new", description: t("cmdNew") },
  { name: "reset", description: t("cmdReset") },
  { name: "retry", description: t("cmdRetry") },
  { name: "undo", description: t("cmdUndo") },
  { name: "compact", description: t("cmdCompact") },
  { name: "status", description: t("cmdStatus") },
  { name: "usage", description: t("cmdUsage") },
  { name: "insights", description: t("cmdInsights") },
  { name: "personality", description: t("cmdPersonality") },
  { name: "model", description: t("cmdModel") },
  { name: "goal", description: t("cmdGoal") },
  { name: "help", description: t("cmdHelp") },
  { name: "version", description: t("cmdVersion") },
];
// Locale-independent name set for the "is this a built-in?" checks —
// keep in sync with BUILTIN_COMMANDS above.
const BUILTIN_COMMAND_NAMES = new Set([
  "new", "reset", "retry", "undo", "compact", "status", "usage",
  "insights", "personality", "model", "goal", "help", "version",
]);
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

interface ChatSession {
  id: string;
  title?: string;
  preview: string;
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

/** Convert raw history messages into UI ChatMessages, grouping tool calls with results.
 *  `stoppedLabel` is the localized "(stopped)" placeholder stamped onto
 *  orphaned tool calls (passed in because this helper runs outside the
 *  translator's React context). */
function buildChatMessages(history: ChatHistoryMessage[], stoppedLabel: string): ChatMessage[] {
  const msgs: ChatMessage[] = [];
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
      msgs.push({ id: `h-${i}`, role: "user", content: h.content || "", timestamp: 0, attachments, sender });
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
          c.result = stoppedLabel;
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
        msgs.push({ id: `h-pre-${i}`, role: "agent", content: h.content, timestamp: 0, metadata: h.metadata });
      }
      msgs.push({
        id: `h-tool-${i}`,
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
        msgs.push({ id: `h-${i}`, role: "agent", content: history[i].content || "", timestamp: 0, metadata: history[i].metadata });
        i++;
      }
    } else if (h.role === "assistant") {
      msgs.push({ id: `h-${i}`, role: "agent", content: h.content || "", timestamp: 0, metadata: h.metadata });
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

// Parse the per-route ids out of the pathname. ChatScreen is mounted
// once at the agent layout level and stays alive across these routes:
//
//   /agents/<aid>/                         — fresh loose chat
//   /agents/<aid>/chat/                    — fresh loose chat
//   /agents/<aid>/chat/<session>           — open existing chat by id
//   /agents/<aid>/project/<pid>            — fresh chat in a project
//
// Reading from `usePathname()` (instead of accepting props from the
// TodoPanel renders the per-session todo.md the agent maintains as a
// live progress checklist above the conversation. "Current step" is
// the first unchecked item — we surface it as a single line with a
// "<n>/<total>" counter, and the full list expands on click. Hidden
// entirely when no items exist (caller's responsibility — keeps this
// dumb-component pure).
function TodoPanel({ items, active }: { items: TodoItem[]; active: boolean }) {
  const t = useTranslations("chat");
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
    // Wrapper keeps the panel aligned with the composer's max-w-2xl
    // column. Only the inner div carries border/background, so the
    // surrounding chat area stays clean — no full-width strip across
    // the page.
    <div className="shrink-0 px-4 pt-2">
      <div className="mx-auto max-w-2xl">
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
              {current ? current.text : t("todoFallback")}
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

export function ChatScreen() {
  const t = useTranslations("chat");
  const tc = useTranslations("common");
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
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
  // Reactive: re-derives from pathname so switching agents (sidebar
  // dropdown, browser back/forward) immediately updates downstream
  // fetches. The previous useState(() => ...) flavor froze the id at
  // mount, so background loads kept hitting the old agent and the
  // panel showed stale history under the new URL.
  const selectedAgent = useAgentIdFromURL();
  const [agentName, setAgentName] = useState<string>("");
  // Resolved metadata for `urlProjectId`, surfaced as the
  // empty-state info card on /agents/<aid>/project/<pid>. Null until
  // the fetch lands; the card hides while loading rather than
  // flashing a placeholder.
  const [projectInfo, setProjectInfo] = useState<{
    id: string;
    name: string;
    description?: string;
    updatedAt?: string;
    createdAt?: string;
  } | null>(null);
  const [sessionId, setSessionId] = useState<string>(
    () => urlSessionId || generateSessionId(),
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
  const [knowledgePreview, setKnowledgePreview] = useState<KnowledgeSource | null>(null);
  // Opening the workspace/preview panel collapses the platform sidebar to
  // free horizontal room (null when there's no provider, e.g. act-as view).
  const sidebar = useSidebarOptional();
  useEffect(() => {
    if (filesSheetOpen) sidebar?.setOpen(false);
    // Intentionally keyed only on filesSheetOpen: collapse once when the
    // panel opens; don't fight the user if they re-expand while it's open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filesSheetOpen]);
  const openKnowledgeCitation = useCallback((source: KnowledgeSource) => {
    setKnowledgePreview(source);
    setFilesSheetOpen(true);
  }, []);
  const [sessionTitle, setSessionTitle] = useState<string>("");
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
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const messagesScrollRef = useRef<HTMLDivElement>(null);
  // Auto-scroll only when the user is already pinned to the bottom.
  // Toggled false the moment they scroll up so streaming agent output
  // can't yank them back down mid-read, then flipped back to true once
  // they return to the bottom (or hit the "scroll to latest" button).
  const stickToBottomRef = useRef(true);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

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
    setMessages([]);
    setSessions([]);
    setSessionTitle("");
    setAgentName("");
    setAttachments([]);
  }, [selectedAgent]);

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
        setAgentName(a?.name || a?.id || selectedAgent);
      })
      .catch(() => {
        if (!aborted) setAgentName(selectedAgent);
      });
    return () => {
      aborted = true;
    };
  }, [selectedAgent]);

  // Resolve project name for the hero title when the URL points at a
  // project's empty new-chat state. Cheap enough to do via listProjects
  // — projects per (user, agent) is small and the sidebar has already
  // warmed the network cache.
  useEffect(() => {
    if (!selectedAgent || !urlProjectId) {
      setProjectInfo(null);
      return;
    }
    let aborted = false;
    listProjects(selectedAgent)
      .then((list) => {
        if (aborted) return;
        const p = list.find((x) => x.id === urlProjectId);
        setProjectInfo(p ?? null);
      })
      .catch(() => {
        if (!aborted) setProjectInfo(null);
      });
    return () => {
      aborted = true;
    };
  }, [selectedAgent, urlProjectId]);

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

  // Localized built-in command list. Memoized so the array identity is
  // stable across renders; recomputes only on locale change.
  const builtinCommands = useMemo(() => BUILTIN_COMMANDS(t), [t]);

  // Merged command + skill list for the slash menu. Commands first so
  // built-ins are easy to find; query matches both name and description.
  // Cap at 8 to keep the popover from outgrowing the composer.
  const filteredItems: SlashItem[] = slashOpen
    ? (() => {
        const q = slashQuery.toLowerCase();
        const match = (name: string, desc: string) =>
          !q || name.toLowerCase().includes(q) || desc.toLowerCase().includes(q);
        const cmds: SlashItem[] = builtinCommands
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
    return BUILTIN_COMMAND_NAMES.has(match[1]) ? match[1] : "";
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
            const msg = data.data?.message || t("errorUnknown");
            // Older gateways persisted cancellation as an error event.
            // Ignore those on replay so Stop produces one clear status
            // instead of "(Stopped)" followed by a stale error bubble.
            if (/\bcontext canceled\b/i.test(msg)) break;
            setMessages((prev) => [
              ...prev,
              { id: `e-${Date.now()}`, role: "agent", content: t("errorWithMessage", { message: msg }), timestamp: Date.now() },
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
              getChatHistoryWithCursor(selectedAgent, sessionId)
                .then(({ history, latestEventSeq }) => {
                  if (latestEventSeq > maxSeqRef.current) maxSeqRef.current = latestEventSeq;
                  subscribeSinceRef.current = latestEventSeq;
                  setMessages(buildChatMessages(history, t("toolStopped")));
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
  }, [selectedAgent, sessionId, loadedSessionId]);

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
    if (urlSessionId) {
      prevHadSessionRef.current = true;
      if (urlSessionId !== sessionId) {
        setSessionId(urlSessionId);
        setMessages([]);
      }
      return;
    }
    if (prevHadSessionRef.current) {
      prevHadSessionRef.current = false;
      setSessionId(generateSessionId());
      setMessages([]);
    }
  }, [urlSessionId, sessionId]);

  // Switching conversations (sidebar chat click, New chat, opening a project)
  // changes the URL ids — close the workspace panel so the previous chat's
  // files don't linger over a different conversation. Keyed on the URL ids,
  // not every render, so the user can still re-open it within the SAME chat.
  useEffect(() => {
    setFilesSheetOpen(false);
  }, [urlSessionId, urlProjectId]);

  // Keep the local sessionTitle in sync with the session list. Unknown
  // sessions (brand-new, not saved yet) fall back to empty so the header
  // can render "New chat".
  useEffect(() => {
    const s = sessions.find((x) => x.id === sessionId);
    setSessionTitle(s?.title || s?.preview || "");
  }, [sessionId, sessions]);

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
  const canUseComposer = !!selectedAgent;
  const canSendComposer =
    canUseComposer && (!isReadOnlyView || inputIsReadOnlySafeSlashCommand);
  const canAttach = !!selectedAgent && !sending && !isReadOnlyView;

  const handleRenameTitle = useCallback(
    async (next: string) => {
      const trimmed = next.trim();
      if (!trimmed || !selectedAgent || trimmed === sessionTitle) return;
      setSessionTitle(trimmed);
      try {
        await renameChatSession(selectedAgent, sessionId, trimmed);
      } finally {
        loadSessions(selectedAgent);
        // Tell the global sidebar to refetch its Chats list so the new
        // title shows up without a full page reload. AppSidebar's own
        // fetch only re-runs when activeAgentId changes, which doesn't
        // happen on rename.
        if (typeof window !== "undefined") {
          window.dispatchEvent(
            new CustomEvent("fastclaw:sessions-changed", {
              detail: { agentId: selectedAgent },
            }),
          );
        }
      }
    },
    [selectedAgent, sessionId, sessionTitle, loadSessions],
  );

  // Render the editable title + the workspace-panel toggle into the
  // global sticky header (the chat container injects whatever JSX it
  // wants here, next to the sidebar toggle). The toggle stays wired
  // even when the panel is open so users can collapse it from the
  // same control they used to expand it.
  const headerSlot = useMemo(
    () => (
      <div className="flex flex-1 items-center justify-between gap-2 min-w-0">
        <ChatHeaderTitle
          title={sessionTitle}
          fallback={t("headerFallbackTitle", { name: agentName || selectedAgent })}
          onSave={handleRenameTitle}
        />
        <button
          type="button"
          onClick={() => setFilesSheetOpen((v) => !v)}
          className={`shrink-0 inline-flex h-8 w-8 items-center justify-center rounded-md transition-colors ${
            filesSheetOpen
              ? "bg-muted text-foreground"
              : "text-muted-foreground hover:bg-muted/50 hover:text-foreground"
          }`}
          title={filesSheetOpen ? t("workspaceHide") : t("workspaceShow")}
          aria-pressed={filesSheetOpen}
        >
          <FolderOpen className="h-4 w-4" />
          <span className="sr-only">{t("workspaceToggle")}</span>
        </button>
      </div>
    ),
    [sessionTitle, agentName, selectedAgent, handleRenameTitle, filesSheetOpen, t],
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
    // Reset dedup state when session changes — events from a previous
    // session must not bias the new session's seq filter, and any
    // transient placeholder is no longer relevant.
    maxSeqRef.current = -1;
    subscribeSinceRef.current = -1;
    transientBubbleIdRef.current = null;
    // Close the SSE gate for this sessionId; reopens once the history
    // fetch lands and subscribeSinceRef has been set to the real cursor.
    setLoadedSessionId(null);
    // Reset the todo panel on session change so the previous chat's
    // checklist doesn't briefly flash before the new one's fetch lands.
    setTodoItems([]);
    // Same for the subagent progress indicator — never carry it across
    // sessions; a fresh load means no in-flight delegate_task to track.
    setSubagentProgress(null);
    // Refresh todo.md alongside the history fetch. We don't gate the
    // rest of the load on it — a 404 (no todo.md yet) is the normal
    // empty-session case.
    getChatTodo(selectedAgent, sessionId)
      .then((todo) => setTodoItems(todo.items))
      .catch(() => setTodoItems([]));
    let aborted = false;
    getChatHistoryWithCursor(selectedAgent, sessionId)
      .then(async ({ history, latestEventSeq }) => {
        if (aborted) return;
        if (!sessionHasActivePost && latestEventSeq > maxSeqRef.current) {
          maxSeqRef.current = latestEventSeq;
        }
        subscribeSinceRef.current = latestEventSeq;
        if (!history || history.length === 0) {
          if (!sessionHasActivePost) {
            setMessages([]);
          }
          setLoadedSessionId(sessionId);
          return;
        }
        const built = buildChatMessages(history, t("toolStopped"));
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
  }, [selectedAgent, sessionId]);

  useEffect(() => {
    if (!stickToBottomRef.current) return;
    messagesEndRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages]);

  // Watch the scroll container so we know whether to keep auto-scrolling
  // as new content arrives. 64px slack absorbs streaming jitter — without
  // it, a single token append can push the bottom past the viewport and
  // flip us off "sticky" for one tick before the next auto-scroll fires.
  useEffect(() => {
    const el = messagesScrollRef.current;
    if (!el) return;
    const onScroll = () => {
      const distance = el.scrollHeight - el.scrollTop - el.clientHeight;
      stickToBottomRef.current = distance <= 64;
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, []);

  useEffect(() => {
    const el = textareaRef.current;
    if (el) {
      el.style.height = "auto";
      el.style.height = Math.min(el.scrollHeight, 200) + "px";
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
          { id: `e-${Date.now()}`, role: "agent", content: t("errorUploadFailed", { message: err instanceof Error ? err.message : t("errorUnknown") }), timestamp: Date.now() },
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
            if (tc && (tc.name === "write_file" || tc.name === "edit_file" || tc.name === "apply_patch")) {
              try {
                const args = JSON.parse(tc.arguments);
                const path: string =
                  (typeof args?.path === "string" ? args.path : "") ||
                  (typeof args?.file_path === "string" ? args.file_path : "");
                if (path && /(^|\/)todo\.md$/i.test(path)) {
                  getChatTodo(selectedAgent, sessionId)
                    .then((todo) => setTodoItems(todo.items))
                    .catch(() => {});
                }
              } catch { /* ignore bad args */ }
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
            const msg = evt.data?.message || t("errorUnknown");
            if (/\bcontext canceled\b/i.test(msg)) break;
            setMessages((prev) => [
              ...prev,
              { id: `e-${Date.now()}`, role: "agent", content: t("errorWithMessage", { message: msg }), timestamp: Date.now() },
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
                    tc.result === undefined ? { ...tc, result: t("toolStopped") } : tc,
                  ),
                }
              : m,
          ),
        );
        setMessages((prev) => [
          ...prev,
          { id: `e-${Date.now()}`, role: "agent", content: t("stoppedMessage"), timestamp: Date.now() },
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
            : t("errorNoResponse");
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
  }, [input, attachments, selectedAgent, sessionId, sending, isReadOnlyView, isReadOnlySafeSlashCommand, loadSessions, pathname, router, urlProjectId, t]);

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
        { id: `e-${Date.now()}`, role: "agent", content: t("errorSteerFailed", { message: err instanceof Error ? err.message : t("errorUnknown") }), timestamp: Date.now() },
      ]);
      return;
    }
    if (!ok) {
      setMessages((prev) => prev.filter((m) => m.id !== optimisticId));
      await handleSend(text, true);
    }
  }, [input, selectedAgent, sending, sessionId, urlProjectId, handleSend, t]);

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
    setSessionId(newId);
    setMessages([]);
    router.replace(`/agents/${selectedAgent}/chat/`);
  };

  const handleSelectSession = (sid: string) => {
    setSessionId(sid);
    // history.replaceState (not router.replace) for the same reason as
    // handleSend: /chat/[session] is only pre-rendered for the `_`
    // placeholder under output:'export', so router-driven navigation to
    // a real sid hard-reloads. See the longer note in handleSend.
    window.history.replaceState(null, "", `/agents/${selectedAgent}/chat/${sid}/`);
  };

  const formatTime = (ts: number) =>
    new Date(ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

  // Empty new-chat state: collapse the messages scroll out of the
  // flex-1 lane and center title + composer vertically, Manus-style.
  // Once any message exists the layout swings back to the standard
  // "scroll above, sticky composer at bottom" shape.
  const isEmpty = messages.length === 0;
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
  const heroTitle = t("heroTitle");

  return (
    <div className="flex h-[calc(100vh-3rem)] flex-row">
      <div
        className={
          "flex flex-1 min-w-0 flex-col" +
          // pb-12 (3rem) matches the header height we already subtracted
          // from the parent's h-[calc(100vh-3rem)]. Without it `justify-
          // center` centers content inside the post-header area, which
          // sits visually ~24px below the true viewport mid-line — the
          // user notices the hero + composer pair drifting low. Adding
          // an equal bottom padding biases the centered group upward by
          // half the header height so the optical centre lines up with
          // the geometric centre of the screen.
          (isEmpty ? " justify-center pb-12" : "")
        }
      >
      {/* Messages */}
        <div
          ref={messagesScrollRef}
          className={
            // scrollbar-gutter:stable always reserves the 6px scrollbar track
            // so message rows keep a fixed content width that lines up with the
            // composer below (which gets a matching right inset) — otherwise the
            // scrollbar shifts message edges out of alignment on a narrow panel.
            "min-h-0 px-4 [scrollbar-gutter:stable] " +
            (isEmpty ? "shrink-0" : "flex-1 overflow-y-auto py-4")
          }
        >
          <div className="mx-auto max-w-2xl space-y-3">
            {isEmpty && (
              <div className="py-8 text-center">
                <h1 className="text-3xl md:text-4xl font-semibold tracking-tight">
                  {heroTitle}
                </h1>
              </div>
            )}

            {(() => {
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
                        onOpen={() => setFilesSheetOpen(true)}
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
                  continue;
                }
                elements.push(renderRegularBubble(msg));
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
                          ? "bg-primary/10 dark:bg-primary/15 text-foreground rounded-br-md"
                          : "bg-muted rounded-bl-md"
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
                                aria-label={t("previewAttachment", { name: att.name })}
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
                          <span className="font-medium">{t("iterationCapTitle")}</span>
                          <span className="opacity-80">
                            {t("iterationCapBody", { count: msg.metadata.iterationCapValue ?? "" })}
                          </span>
                        </div>
                      )}
                      {msg.role === "agent" && msg.metadata?.planMode && (
                        <div className="mt-2 flex items-start gap-1.5 rounded-md border border-amber-500/40 bg-amber-500/10 px-2.5 py-1.5 text-xs text-amber-900 dark:text-amber-200">
                          <ListChecks className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                          <span className="font-medium">{t("planModeTitle")}</span>
                          <span className="opacity-80">
                            {t("planModeBody")}
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
                            {t("planRun")}
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
                            {t("planEdit")}
                          </Button>
                          <span className="text-xs text-muted-foreground">
                            {t("planHint")}
                          </span>
                        </div>
                      )}
                    </div>
                    {msg.files && msg.files.length > 0 && (
                      <FilesPanel files={msg.files} onOpen={() => setFilesSheetOpen(true)} />
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
                            title={tc("copy")}
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
                            title={t("retryTooltip")}
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
                            title={tc("copy")}
                          >
                            {copiedId === msg.id ? (
                              <Check className="h-3 w-3 text-emerald-500" />
                            ) : (
                              <Copy className="h-3 w-3" />
                            )}
                          </button>
                          <button
                            onClick={() => setFilesSheetOpen(true)}
                            className="opacity-0 group-hover:opacity-100 inline-flex items-center gap-1 px-1.5 py-0.5 rounded hover:bg-muted text-[10px] text-muted-foreground/60 hover:text-muted-foreground transition-all"
                            title={t("viewTaskFiles")}
                          >
                            <FolderOpen className="h-3 w-3" />
                            <span>{t("filesLabel")}</span>
                          </button>
                        </>
                      )}
                    </div>
                  </div>
                </div>
              );
              }
            })()}

            {sending && (
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

            <div ref={messagesEndRef} />
          </div>
        </div>

        {/* Live progress panel: agent maintains a per-session `todo.md`
            checklist and we render it here right above the composer so
            the user's eye is on the next step they're about to authorize,
            not buried at the top behind a long scroll history. Auto-
            hides when the file doesn't exist or has no checkbox items. */}
        {!isEmpty && todoItems.length > 0 && (
          <TodoPanel items={todoItems} active={sending} />
        )}

        {/* Input — right inset matches the messages' reserved scrollbar gutter
            (6px) so the composer's edges line up with the message rows above. */}
        <div className="shrink-0 pl-4 pr-[calc(1rem+6px)] pb-6 pt-2">
          <div className="mx-auto max-w-2xl relative">
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
                {t.rich("readOnlyChannelBanner", {
                  channel: channelLabel(currentChannel),
                  strong: (chunks) => (
                    <span className="font-medium text-foreground">{chunks}</span>
                  ),
                  code: (chunks) => (
                    <span className="font-mono text-foreground">{chunks}</span>
                  ),
                })}
              </div>
            )}
            {isActAsView && !isReadOnlyChannel && (
              // Super_admin viewing another user's chat via the admin
              // Chats page (?actAs=<uid>). The middleware gates this as
              // read-only for the whole request, so any send would 403
              // — disable the composer and surface why.
              <div className="mb-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
                {t("actAsBanner")}
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
            <div
              className={
                "border border-border bg-card focus-within:ring-2 focus-within:ring-ring/20 transition-shadow " +
                (isEmpty ? "rounded-2xl px-5 pt-4 pb-3" : "rounded-xl px-4 py-3")
              }
            >
              {attachments.length > 0 && (
                <div className="flex flex-wrap gap-2 mb-2 pb-2 border-b border-border/60">
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
                            aria-label={t("previewAttachment", { name: f.name })}
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
                            aria-label={t("removeAttachment")}
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
                          aria-label={t("removeAttachment")}
                        >
                          <X className="h-3 w-3" />
                        </button>
                      </div>
                    );
                  })}
                </div>
              )}
              {/* Empty-state composer: Manus-style — textarea fills the
                  top, action row sits below it. Once messages exist we
                  swing back to the compact single-row layout so the
                  composer doesn't dominate the chat. */}
              {isEmpty ? (
                <>
                  <textarea
                    ref={textareaRef}
                    value={input}
                    onChange={handleInputChange}
                    onKeyDown={handleKeyDown}
                    onBlur={() => setTimeout(() => setSlashOpen(false), 120)}
                    placeholder={
                      isActAsView
                        ? t("placeholderActAs")
                        : isReadOnlyChannel
                          ? t("placeholderReadOnlyChannel", { channel: channelLabel(currentChannel) })
                          : selectedAgent
                            ? t("placeholderMessage", { name: agentName || selectedAgent })
                            : t("placeholderNoAgent")
                    }
                    disabled={!canUseComposer}
                    rows={3}
                    className="block w-full resize-none bg-transparent text-[15px] placeholder:text-muted-foreground/50 outline-none disabled:opacity-50"
                    style={{ maxHeight: 240, minHeight: 72 }}
                  />
                  <div className="mt-2 flex items-center justify-between">
                    <div className="flex items-center gap-2 min-w-0">
                      <label
                        className={`flex h-9 w-9 shrink-0 items-center justify-center rounded-full border border-border text-muted-foreground transition-colors ${
                          !canAttach
                            ? "opacity-50 cursor-not-allowed"
                            : "hover:bg-muted hover:text-foreground cursor-pointer"
                        }`}
                        aria-label={t("attachFiles")}
                      >
                        <Paperclip className="h-4 w-4" />
                        <input
                          ref={fileInputRef}
                          type="file"
                          multiple
                          className="sr-only"
                          onChange={handleFilePick}
                          disabled={!canAttach}
                        />
                      </label>
                      {urlProjectId && projectInfo && (
                        <div
                          className="flex h-9 min-w-0 items-center gap-1.5 rounded-full border border-border px-3 text-xs text-muted-foreground"
                          title={projectInfo.name}
                        >
                          <FolderOpen className="size-3.5 shrink-0" />
                          <span className="truncate max-w-[20ch]">
                            {projectInfo.name}
                          </span>
                        </div>
                      )}
                    </div>
                    {sending ? (
                      <Button
                        onClick={handleStop}
                        size="icon"
                        className="h-9 w-9 shrink-0 rounded-full"
                        aria-label={t("stopGenerating")}
                      >
                        <Square className="h-3.5 w-3.5 fill-current" />
                      </Button>
                    ) : (
                      <Button
                        onMouseDown={(e) => {
                          e.preventDefault();
                          handleSend();
                        }}
                        disabled={(!input.trim() && attachments.length === 0) || !canSendComposer}
                        size="icon"
                        className="h-9 w-9 shrink-0 rounded-full"
                        aria-label={t("sendMessage")}
                      >
                        <Send className="h-4 w-4" />
                      </Button>
                    )}
                  </div>
                </>
              ) : (
                <div className="flex items-center gap-2">
                  <label
                    className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-lg text-muted-foreground transition-colors ${
                      !canAttach
                        ? "opacity-50 cursor-not-allowed"
                        : "hover:bg-muted hover:text-foreground cursor-pointer"
                    }`}
                    aria-label={t("attachFiles")}
                  >
                    <Paperclip className="h-4 w-4" />
                    <input
                      ref={fileInputRef}
                      type="file"
                      multiple
                      className="sr-only"
                      onChange={handleFilePick}
                      disabled={!canAttach}
                    />
                  </label>
                  <textarea
                    ref={textareaRef}
                    value={input}
                    onChange={handleInputChange}
                    onKeyDown={handleKeyDown}
                    onBlur={() => setTimeout(() => setSlashOpen(false), 120)}
                    placeholder={
                      isActAsView
                        ? t("placeholderActAs")
                        : isReadOnlyChannel
                          ? t("placeholderReadOnlyChannel", { channel: channelLabel(currentChannel) })
                          : selectedAgent
                            ? t("placeholderMessage", { name: agentName || selectedAgent })
                            : t("placeholderNoAgent")
                    }
                    disabled={!canUseComposer}
                    rows={1}
                    className="flex-1 resize-none bg-transparent text-[15px] leading-8 placeholder:text-muted-foreground/50 outline-none disabled:opacity-50"
                    style={{ maxHeight: 200, minHeight: 32 }}
                  />
                  {sending ? (
                    <Button
                      onClick={handleStop}
                      size="icon"
                      className="h-8 w-8 shrink-0 rounded-lg"
                      aria-label={t("stopGenerating")}
                    >
                      <Square className="h-3.5 w-3.5 fill-current" />
                    </Button>
                  ) : (
                    <Button
                      onMouseDown={(e) => {
                        e.preventDefault();
                        handleSend();
                      }}
                      disabled={(!input.trim() && attachments.length === 0) || !canSendComposer}
                      size="icon"
                      className="h-8 w-8 shrink-0 rounded-lg"
                      aria-label={t("sendMessage")}
                    >
                      <Send className="h-4 w-4" />
                    </Button>
                  )}
                </div>
              )}
            </div>
          </div>
        </div>
        {lightboxSrc && (
          <div
            className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-6 cursor-zoom-out"
            onClick={() => setLightboxSrc(null)}
            role="dialog"
            aria-modal="true"
            aria-label={t("imagePreview")}
          >
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img
              src={lightboxSrc}
              alt={t("previewAlt")}
              className="max-h-full max-w-full rounded-lg shadow-2xl"
              onClick={(e) => e.stopPropagation()}
            />
            <button
              type="button"
              onClick={() => setLightboxSrc(null)}
              className="absolute right-4 top-4 flex h-9 w-9 items-center justify-center rounded-full bg-background/80 text-foreground hover:bg-background"
              aria-label={t("closePreview")}
            >
              <X className="h-5 w-5" />
            </button>
          </div>
        )}
      </div>
      {filesSheetOpen && selectedAgent && (sessionId || urlProjectId) && (
        <WorkspacePanel
          agentId={selectedAgent}
          // On a project landing (no urlSessionId), sessionId here is the
          // synthetic id chat-screen mints for the upcoming "New chat" —
          // it doesn't correspond to anything on disk, so we suppress it
          // and let projectId drive the scope. Inside an actual chat,
          // urlSessionId is set and we pass the real sessionId.
          sessionId={urlSessionId ? sessionId : ""}
          projectId={!urlSessionId && urlProjectId ? urlProjectId : undefined}
          knowledgePreview={knowledgePreview}
          onClearKnowledgePreview={() => setKnowledgePreview(null)}
          onClose={() => {
            setFilesSheetOpen(false);
            setKnowledgePreview(null);
          }}
        />
      )}
    </div>
  );
}

interface ChatHeaderTitleProps {
  title: string;
  fallback: string;
  onSave: (next: string) => void | Promise<void>;
}

/** Editable chat title rendered into the global sticky header via
 *  usePageHeader. Click / focus to edit; Enter or blur commits. */
function ChatHeaderTitle({ title, fallback, onSave }: ChatHeaderTitleProps) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(title);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!editing) setDraft(title);
  }, [title, editing]);

  useEffect(() => {
    if (editing) inputRef.current?.select();
  }, [editing]);

  const commit = () => {
    setEditing(false);
    const next = draft.trim();
    if (!next || next === title) return;
    onSave(next);
  };

  if (editing) {
    // field-sizing: content grows the input to match its text; min width
    // keeps it reasonable right after entering edit mode even if the
    // current title is very short.
    return (
      <input
        ref={inputRef}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          // Ignore Enter while the user is mid-composition (CJK IME). Both
          // conditions matter: isComposing is the modern signal, keyCode 229
          // is the legacy flag some browsers (and macOS Pinyin in particular)
          // still emit without isComposing set.
          if (e.nativeEvent.isComposing || e.keyCode === 229) return;
          if (e.key === "Enter") {
            e.preventDefault();
            commit();
          } else if (e.key === "Escape") {
            setDraft(title);
            setEditing(false);
          }
        }}
        onBlur={commit}
        style={{ fieldSizing: "content" } as React.CSSProperties}
        className="h-7 min-w-[8ch] max-w-[40ch] rounded-md bg-transparent px-2 text-sm outline-none ring-1 ring-border focus:ring-primary/40"
      />
    );
  }

  return (
    <button
      onClick={() => {
        setDraft(title);
        setEditing(true);
      }}
      // Cap the title width responsively so a long auto-summary doesn't
      // push the whole header off-screen on small viewports. The
      // arbitrary `min(...)` keeps narrow widths on phones (60vw) while
      // capping at ~32rem on desktop; sm:/md: bumps give intermediate
      // breakpoints a deterministic width too.
      className="group flex min-w-0 max-w-[min(60vw,18rem)] sm:max-w-[24rem] md:max-w-[28rem] lg:max-w-[32rem] items-center gap-1.5 rounded-md px-2 py-1 text-sm text-foreground hover:bg-muted/50"
      title={title || fallback}
    >
      <span className="truncate">{title || fallback}</span>
      <Pencil className="h-3 w-3 shrink-0 text-muted-foreground/50 opacity-0 transition-opacity group-hover:opacity-100" />
    </button>
  );
}

/** Renders a group of tool calls as a collapsible summary. When
 *  `nested`, the outer flex/max-width wrappers are dropped so a parent
 *  container (ToolRoundsBundle) can stack rounds without each one
 *  re-imposing its own bubble alignment. */
function ToolCallGroup({ msg, surfacedSrcs, agentId, sessionId, nested = false, roundIndex, subagentProgress, onKnowledgeCitationClick }: { msg: ChatMessage; surfacedSrcs?: ReadonlySet<string>; agentId: string; sessionId: string; nested?: boolean; roundIndex?: number; subagentProgress?: { iteration?: number; max?: number; phase?: "thinking" | "running" | "final-delivery" | "done"; tools?: string[] } | null; onKnowledgeCitationClick?: (source: KnowledgeSource) => void }) {
  const t = useTranslations("chat");
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
                ? t("toolsExecuted", { count: tools.length })
                : t("toolsRunning", { done: doneCount, total: tools.length })}
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
                        title={t("sandboxTooltip")}
                      >
                        <ShieldCheck className="h-2.5 w-2.5" />
                        {t("sandboxBadge")}
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
                        <p className="text-[10px] font-medium text-muted-foreground uppercase mb-1">{t("toolInput")}</p>
                        <pre className="text-xs font-mono bg-muted/50 rounded p-2 overflow-x-auto whitespace-pre-wrap break-all max-h-40">
                          {(() => {
                            try { return JSON.stringify(JSON.parse(tc.arguments), null, 2); }
                            catch { return tc.arguments; }
                          })()}
                        </pre>
                      </div>
                      {tc.result != null ? (
                        <div>
                          <p className="text-[10px] font-medium text-muted-foreground uppercase mb-1">{t("toolOutput")}</p>
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
                            const counter = it && mx ? t("subagentIteration", { current: it, max: mx }) : t("subagentRunning");
                            let detail = "";
                            if (phase === "thinking") detail = t("subagentThinking");
                            else if (phase === "running" && tools?.length) detail = t("subagentRunningTools", { tools: tools.join(", ") });
                            else if (phase === "final-delivery") detail = t("subagentFinalizing");
                            return detail ? `${counter} · ${detail}` : counter;
                          })()}
                        </div>
                      ) : tc.name === "delegate_task" && tc.result == null && tc.id !== activeDelegateId ? (
                        <p className="text-xs text-muted-foreground/60 italic">{t("subagentQueued")}</p>
                      ) : (
                        <p className="text-xs text-muted-foreground/60 italic">{t("toolExecuting")}</p>
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
  const t = useTranslations("chat");
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
                ? t("bundleDone", { tools: totalTools, rounds: rounds.length })
                : t("bundleRunning", { done: doneCount, total: totalTools, rounds: rounds.length })}
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
  const t = useTranslations("chat");
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
      {text || t("buildStarting")}
    </pre>
  );
}

function FilesPanel({ files, onOpen }: { files: ProducedFile[]; onOpen: () => void }) {
  const t = useTranslations("chat");
  return (
    <div className="mt-2 max-w-[85%]">
      <button
        type="button"
        onClick={onOpen}
        className="group inline-flex items-center gap-2 rounded-lg border border-border bg-card/50 px-3 py-2 hover:bg-card/80 transition-colors"
        title={t("openFilesTooltip")}
      >
        <FolderOpen className="h-4 w-4 text-muted-foreground shrink-0 group-hover:text-foreground transition-colors" />
        <span className="text-sm font-medium text-foreground">{t("openFiles")}</span>
        <span className="rounded-full bg-muted px-1.5 py-0.5 text-[11px] font-medium text-muted-foreground/80 tabular-nums">
          {files.length}
        </span>
      </button>
    </div>
  );
}

const FILES_PANEL_MIN = 280;
const FILES_PANEL_MAX = 1000; // wide enough to view a desktop preview iframe
const FILES_PANEL_DEFAULT = 280;
const FILES_PANEL_KEY = "chat:filesPanelWidth";
// When the user switches to the Preview tab and the panel is still narrow,
// auto-grow to this so the embedded site isn't cramped. Transient (not
// persisted), so the Code tab keeps its own saved width.
const PREVIEW_AUTO_WIDTH = 760;

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
  onClose,
}: {
  agentId: string;
  sessionId: string;
  projectId?: string;
  knowledgePreview?: KnowledgeSource | null;
  onClearKnowledgePreview?: () => void;
  onClose: () => void;
}) {
  const t = useTranslations("chat");
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
  const [width, setWidth] = useState<number>(() => {
    if (typeof window === "undefined") return FILES_PANEL_DEFAULT;
    const stored = Number(window.localStorage.getItem(FILES_PANEL_KEY));
    if (Number.isFinite(stored) && stored >= FILES_PANEL_MIN && stored <= FILES_PANEL_MAX) {
      return stored;
    }
    return FILES_PANEL_DEFAULT;
  });
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
      try {
        window.localStorage.setItem(FILES_PANEL_KEY, String(width));
      } catch { /* ignore quota errors */ }
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
  }, [resizing, width]);

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
        alert(res.error || t("errorRevealFailed"));
      }
    } finally {
      setRevealing(false);
    }
  }, [agentId, sessionId, projectId, t]);

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

  // Open at a comfortable width: the 280px drag-floor is far too cramped to
  // read a file tree + viewer. Grow to PREVIEW_AUTO_WIDTH on mount (panel
  // open) — capped by the 70% container maxWidth, so it never overflows. The
  // user can still drag narrower within the session; reopening re-widens.
  useEffect(() => {
    setWidth((w) => (w < PREVIEW_AUTO_WIDTH ? Math.min(PREVIEW_AUTO_WIDTH, FILES_PANEL_MAX) : w));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Also grow when entering Preview / opening a file (in case the user dragged
  // narrow earlier) so the iframe / viewer column isn't cramped.
  useEffect(() => {
    if (tab === "preview" || previewing || knowledgePreview) {
      setWidth((w) => (w < PREVIEW_AUTO_WIDTH ? Math.min(PREVIEW_AUTO_WIDTH, FILES_PANEL_MAX) : w));
    }
  }, [tab, previewing, knowledgePreview]);

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
        className="relative z-30 hidden md:flex shrink-0 flex-col overflow-hidden border-l border-border bg-background -mt-12 h-screen"
      >
        <div
          onMouseDown={(e) => { e.preventDefault(); setResizing(true); }}
          className={`absolute left-0 top-0 bottom-0 w-2 cursor-col-resize z-10 group ${resizing ? "" : ""}`}
          title={t("dragToResize")}
        >
          <div
            className={`absolute inset-y-0 left-1/2 w-px -translate-x-1/2 transition-colors ${
              resizing ? "bg-primary" : "bg-transparent group-hover:bg-primary/40"
            }`}
          />
        </div>
        <div className="flex h-12 items-center justify-between gap-2 border-b border-border px-4">
          <div className="flex min-w-0 items-center gap-2 text-sm font-medium">
            <FolderOpen className="h-4 w-4 shrink-0" />
            {!compactHeader && <span className="truncate">{knowledgePreview ? t("knowledgeTitle") : t("workspaceTitle")}</span>}
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
                      title={t("moreActions")}
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
                      <span>{t("openInNewTab")}</span>
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
                    <span>{t("downloadZip")}</span>
                  </DropdownMenuItem>
                  {deployMode === "self-hosted" && (
                    <DropdownMenuItem
                      disabled={revealing || (!sessionId && !projectId)}
                      onClick={handleReveal}
                    >
                      <FolderSearch className="h-4 w-4 text-muted-foreground" />
                      <span>{t("openInFinder")}</span>
                    </DropdownMenuItem>
                  )}
                  <DropdownMenuItem disabled={loading} onClick={refresh}>
                    <RefreshCw className="h-4 w-4 text-muted-foreground" />
                    <span>{t("refresh")}</span>
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
                    title={t("openPreviewNewTab", { url: appPreview.previewUrl })}
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
                  title={t("downloadAllZip")}
                >
                  <Download className="h-4 w-4" />
                </a>
                {deployMode === "self-hosted" && (
                  <button
                    onClick={handleReveal}
                    disabled={revealing || (!sessionId && !projectId)}
                    className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground disabled:opacity-50"
                    title={t("openFolderInFinder")}
                  >
                    <FolderSearch className="h-4 w-4" />
                  </button>
                )}
                <button
                  onClick={refresh}
                  disabled={loading}
                  className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground disabled:opacity-50"
                  title={t("refresh")}
                >
                  <RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} />
                </button>
              </>
            )}
            <button
              onClick={onClose}
              className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground"
              title={t("close")}
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
                {t("tabFiles")}
              </button>
              <button
                onClick={() => setTab("preview")}
                className={`flex items-center gap-1 rounded px-2.5 py-1 transition-colors ${
                  tab === "preview"
                    ? "bg-background font-medium text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground"
                }`}
              >
                {t("tabPreview")}
                {(appPreview.status === "starting" || appPreview.status === "scaffolding") && (
                  <RefreshCw className="h-3 w-3 animate-spin" />
                )}
              </button>
            </div>
          ) : (
            <span className="px-1 text-xs font-medium text-muted-foreground">{t("tabFiles")}</span>
          )}
          {tab === "code" && (
            <button
              onClick={() => setTreeCollapsed((c) => !c)}
              className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground"
              title={treeCollapsed ? t("showFileTree") : t("hideFileTree")}
            >
              {treeCollapsed ? <PanelLeftOpen className="h-4 w-4" /> : <PanelLeftClose className="h-4 w-4" />}
            </button>
          )}
        </div>
        {tab === "code" ? (
          <div className="flex min-h-0 flex-1">
            {/* Left: file tree (collapsible). */}
            {!treeCollapsed && (
            <div className="flex w-56 shrink-0 flex-col border-r border-border">
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
                    {t("changedFilter", { count: changed.files.length })}
                  </button>
                  <button
                    onClick={() => setShowAll(true)}
                    className={`rounded px-2 py-0.5 transition-colors ${
                      showAll ? "bg-muted font-medium text-foreground" : "text-muted-foreground hover:text-foreground"
                    }`}
                  >
                    {t("allFilesFilter")}
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
                          ? t("emptyNoChanges")
                          : projectId
                            ? t("emptyProject")
                            : t("emptySession")}
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
              ) : (
                <div className="flex h-full flex-col items-center justify-center gap-2 px-6 text-center text-muted-foreground">
                  <FileText className="h-6 w-6" />
                  <p className="text-sm">{t("selectFileHint")}</p>
                </div>
              )}
            </div>
          </div>
        ) : (
          <div className="flex-1 min-h-0">
            {appPreview.status === "running" && appPreview.previewUrl ? (
              <iframe
                src={appPreview.previewUrl}
                className="h-full w-full border-0 bg-white"
                title={t("appPreviewTitle")}
              />
            ) : appPreview.status === "starting" || appPreview.status === "scaffolding" ? (
              <div className="flex h-full flex-col">
                <div className="flex items-center gap-2 border-b border-border px-4 py-2 text-xs text-muted-foreground">
                  <RefreshCw className="h-4 w-4 shrink-0 animate-spin" />
                  <span>
                    {appPreview.status === "scaffolding"
                      ? t("previewInstalling")
                      : t("previewStartingDev")}
                  </span>
                </div>
                <div className="min-h-0 flex-1">
                  <BuildLogView text={buildLogs} />
                </div>
              </div>
            ) : appPreview.status === "crashed" ? (
              <div className="flex h-full flex-col items-center justify-center gap-2 px-6 text-center">
                <p className="text-sm text-destructive">{t("previewCrashed")}</p>
                <p className="text-xs text-muted-foreground">
                  {t("previewCrashedHint")}
                </p>
              </div>
            ) : (
              <div className="flex h-full flex-col items-center justify-center gap-2 px-6 text-center text-muted-foreground">
                <Eye className="h-6 w-6" />
                <p className="text-sm">{t("previewEmpty")}</p>
                <p className="text-xs">
                  {t("previewEmptyHint")}
                </p>
              </div>
            )}
          </div>
        )}
      </aside>
    </>
  );
}

function formatRelativeTime(ts?: number): string {
  if (!ts) return "—";
  const d = new Date(ts * 1000);
  const now = Date.now();
  const diff = now - d.getTime();
  if (diff < 60_000) return "just now";
  if (diff < 3600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86400_000) return `${Math.floor(diff / 3600_000)}h ago`;
  if (diff < 7 * 86400_000) return `${Math.floor(diff / 86400_000)}d ago`;
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

// FileViewer renders a selected workspace file inline (right column of the
// Files tab): image / pdf / markdown / highlighted text / rendered-or-source
// HTML. onClose, when given, deselects the file.
function KnowledgeFileViewer({ agentId, source, onClose }: { agentId: string; source: KnowledgeSource; onClose?: () => void }) {
  const t = useTranslations("chat");
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
            {source.chunk ? ` · ${t("chunkLabel", { chunk: source.chunk })}` : ""}
            {source.score ? ` · ${t("scoreLabel", { score: source.score })}` : ""}
            {file?.hash ? ` · ${file.hash.slice(0, 8)}` : ""}
          </p>
        </div>
        <div className="flex items-center gap-1 shrink-0">
          {(preview === "markdown" || preview === "html") && (
            <button
              onClick={() => setView(view === "rendered" ? "source" : "rendered")}
              className="rounded-md p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted/50"
              title={view === "rendered" ? t("viewSource") : t("viewRendered")}
            >
              {view === "rendered" ? <Code2 className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
            </button>
          )}
          {onClose && (
            <button
              onClick={onClose}
              className="rounded-md p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted/50"
              title={t("closeKnowledgeFile")}
            >
              <X className="h-4 w-4" />
            </button>
          )}
        </div>
      </div>
      <div className="min-h-0 flex-1">
        {error ? (
          <p className="p-4 text-sm text-destructive">{t("errorLoadFailed", { message: error })}</p>
        ) : !file ? (
          <p className="p-4 text-sm text-muted-foreground">{t("loading")}</p>
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
  const t = useTranslations("chat");
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
                title={view === "rendered" ? t("viewSource") : t("viewRendered")}
              >
                {view === "rendered" ? <Code2 className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
            )}
            <a
              href={src}
              target="_blank"
              rel="noopener noreferrer"
              className="rounded-md p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted/50"
              title={t("openInNewTab")}
            >
              <ExternalLink className="h-4 w-4" />
            </a>
            {onClose && (
              <button
                onClick={onClose}
                className="rounded-md p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted/50"
                title={t("closeFile")}
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
              <p className="p-4 text-sm text-destructive">{t("errorLoadFailed", { message: error })}</p>
            ) : text === null ? (
              <p className="p-4 text-sm text-muted-foreground">{t("loading")}</p>
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
              <p className="text-sm text-muted-foreground">{t("noPreviewForType")}</p>
              <a href={downloadUrl} className="inline-flex items-center gap-2 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90">
                <Download className="h-3.5 w-3.5" /> {t("download")}
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
  const t = useTranslations("chat");
  return (
    <div className="absolute bottom-full left-0 right-0 mb-2 rounded-xl border border-border bg-popover shadow-lg overflow-hidden z-20">
      <div className="max-h-[320px] overflow-y-auto py-1">
        {items.map((it, i) => {
          const isCmd = it.kind === "command";
          const Icon = isCmd ? Terminal : Puzzle;
          const badge = isCmd ? t("badgeCommand") : (it.type || t("badgeSkill"));
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
                    {it.description}
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
        {t("manageSkills")}
      </Link>
    </div>
  );
}
