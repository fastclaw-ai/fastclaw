"use client";

import { useEffect, useState, useRef, useCallback, useMemo } from "react";
import { useRouter, usePathname, useSearchParams } from "next/navigation";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { getAgent, getChatHistoryWithCursor, getChatSessions, getChatTodo, getMe, listAgentFiles, listProjects, renameChatSession, revealAgentWorkspace, sendChatStream, uploadAgentFiles, getAuthToken, getSkills, type ChatHistoryMessage, type ChatStreamEvent, type SkillInfo, type TodoItem, type ToolResultMetadata, type WorkspaceFile } from "@/lib/api";
import { Bot, Send, Copy, Check, Pencil, Wrench, ChevronDown, ChevronRight, Download, X, File, FileText, FolderSearch, Image as ImageIcon, FileCode, Film, Music, Puzzle, SlidersHorizontal, ShieldCheck, Paperclip, Square, FolderOpen, RefreshCw, Eye, Code2, RotateCcw, ListChecks } from "lucide-react";
import Link from "next/link";
import ReactMarkdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";
import { ExternalAnchor } from "@/components/markdown-link";

// react-markdown's default urlTransform strips any protocol not in the
// safe-list (http, https, mailto, ircs, xmpp) — including `data:`. We want
// inline base64 images to render, so fall through to the default for
// everything else.
function urlTransform(url: string, key: string): string {
  if (key === "src" && url.startsWith("data:image/")) return url;
  return defaultUrlTransform(url);
}

// makeUrlTransform builds a urlTransform that also remaps sandbox
// `/workspace/<name>` paths to the authenticated file API URL for the
// active agent. Skills that produce a file return a sandbox path like
// /workspace/img_xxx.png; the LLM puts that in `![](/workspace/...)`
// for images and `[label](workspace/file.zip)` (commonmark eats the
// leading slash on relative refs) for plain links.
//
// The docker bind-mount is session-scoped (host:
// ~/.fastclaw/workspaces/<agent>/sessions/<sid>/ ↔ container:/workspace),
// so the workspace.Store sees the file at sessions/<sid>/<name>. We
// must prepend that prefix or the file API resolves against the agent
// root and 404s.
//
// Without the href branch a click on `[file](workspace/x.zip)` navigates
// the browser top-level to `/workspace/x.zip`, which no API route
// handles. The SPA catch-all serves index.html, Next.js finds no
// matching route, falls through to the root page, and the root page's
// auth check redirects the (logged-in) user to /overview/. End result:
// click "download" → land on /overview/. Rewriting hrefs the same way
// as srcs sends the click to the proper authenticated file endpoint
// with ?download=1 so it triggers a real download instead.
function makeUrlTransform(agentId: string, sessionId: string) {
  const rewriteWorkspace = (url: string, asDownload: boolean): string | null => {
    // Tolerate both `/workspace/...` (LLM writes the absolute sandbox
    // path) and `workspace/...` (commonmark / agents that drop the
    // leading slash on relative refs).
    let rel: string | null = null;
    if (url.startsWith("/workspace/")) rel = url.slice("/workspace/".length);
    else if (url.startsWith("workspace/")) rel = url.slice("workspace/".length);
    if (rel === null) return null;
    const scoped = sessionId ? `sessions/${sessionId}/${rel}` : rel;
    return fileUrl(agentId, scoped, asDownload);
  };
  return (url: string, key: string): string => {
    if (key === "src") {
      if (url.startsWith("data:image/")) return url;
      const rewritten = rewriteWorkspace(url, false);
      if (rewritten !== null) return rewritten;
    } else if (key === "href") {
      const rewritten = rewriteWorkspace(url, true);
      if (rewritten !== null) return rewritten;
    }
    return defaultUrlTransform(url);
  };
}

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
// and `(`) and render those as native <img>, letting ReactMarkdown
// handle everything else. Returns null if no data-URL images are present
// so the caller can fall through to a plain ReactMarkdown render.
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
          <ReactMarkdown key={i} remarkPlugins={[remarkGfm]} components={{ a: ExternalAnchor }}>
            {p.text}
          </ReactMarkdown>
        );
      })}
    </>
  );
}

import { usePageHeader } from "@/components/sidebar";
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

interface ChatMessage {
  id: string;
  role: "user" | "agent" | "tool-group";
  content: string;
  timestamp: number;
  toolCalls?: { id: string; name: string; arguments: string; result?: string; metadata?: ToolResultMetadata }[];
  files?: ProducedFile[];
  attachments?: UserAttachment[];
  // Assistant-side metadata (e.g. iteration-cap badge). Stamped from
  // either the live content event's `metadata` payload or the
  // ChatHistoryMessage.metadata on a refresh.
  metadata?: ToolResultMetadata;
}

// Tailwind class string applied to every chat-bubble markdown wrapper
// (assistant + user). The unmodified `prose prose-sm` defaults render
// markdown like a long-form article — H1/H2 are huge, headings have
// big top/bottom margins, and tables / code blocks introduce extra
// vertical padding. In a conversational bubble those defaults make a
// reply with `## Section` look out of proportion next to surrounding
// plain text. This override clamps headings to slightly-larger-than-
// body sizes, tightens table cell padding, and shrinks the gap above
// each block element so the bubble reads as one coherent message.
const CHAT_PROSE_CLASS =
  "text-[15px] leading-relaxed prose prose-sm max-w-none dark:prose-invert " +
  "prose-p:my-1 prose-pre:my-2 prose-ul:my-1 prose-ol:my-1 " +
  "prose-headings:font-semibold prose-headings:mt-3 prose-headings:mb-1 " +
  "prose-h1:text-[16px] prose-h2:text-[15.5px] prose-h3:text-[15px] " +
  "prose-h4:text-[15px] prose-h5:text-[15px] prose-h6:text-[15px] " +
  "prose-table:my-2 prose-table:text-[14px] " +
  "prose-th:py-1 prose-th:px-2 prose-td:py-1 prose-td:px-2 " +
  "prose-hr:my-3";

// Single-segment identity filenames that route to the agent's home dir
// (not the workspace) — exclude from the "Your files" panel.
const SYSTEM_FILES = new Set([
  "SOUL.md", "IDENTITY.md", "USER.md", "BOOTSTRAP.md",
  "MEMORY.md", "HEARTBEAT.md", "AGENTS.md", "TOOLS.md", "agent.json",
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

/** Convert raw history messages into UI ChatMessages, grouping tool calls with results. */
function buildChatMessages(history: ChatHistoryMessage[]): ChatMessage[] {
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
      msgs.push({ id: `h-${i}`, role: "user", content: h.content || "", timestamp: 0, attachments });
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
      // Show as tool-group
      msgs.push({
        id: `h-tool-${i}`,
        role: "tool-group",
        content: h.content || "",
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
              {current ? current.text : "Plan checklist"}
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

  const filteredSkills = slashOpen
    ? skills
        .filter((s) => {
          const q = slashQuery.toLowerCase();
          if (!q) return true;
          return s.name.toLowerCase().includes(q) || (s.description || "").toLowerCase().includes(q);
        })
        .slice(0, 8)
    : [];

  const selectSkill = useCallback(
    (skill: SkillInfo) => {
      const el = textareaRef.current;
      if (!el) return;
      const caret = el.selectionStart ?? input.length;
      const ctx = slashContext(input, caret);
      if (!ctx) return;
      const before = input.slice(0, ctx.start);
      const after = input.slice(caret);
      const insert = `/${skill.name} `;
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
            const msg = data.data?.message || "Unknown error";
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
          case "done": {
            claim();
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
                  setMessages(buildChatMessages(history));
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
  // isReadOnlyChannel locks the composer for IM-bound sessions (replies
  // must come from the upstream channel). isActAsView locks it when a
  // super_admin opened this URL to inspect another user's chat. Both
  // collapse to the same disabled state on the textarea / send button;
  // the banners differ so the user knows *why*.
  const isReadOnlyChannel = currentChannel !== "web";
  const isReadOnlyView = isReadOnlyChannel || isActAsView;

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
          fallback={`Chat with ${agentName || selectedAgent}`}
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
          title={filesSheetOpen ? "Hide workspace" : "Show workspace"}
          aria-pressed={filesSheetOpen}
        >
          <FolderOpen className="h-4 w-4" />
          <span className="sr-only">Toggle workspace</span>
        </button>
      </div>
    ),
    [sessionTitle, agentName, selectedAgent, handleRenameTitle, filesSheetOpen],
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
        if (latestEventSeq > maxSeqRef.current) maxSeqRef.current = latestEventSeq;
        subscribeSinceRef.current = latestEventSeq;
        if (!history || history.length === 0) {
          setMessages([]);
          setLoadedSessionId(sessionId);
          return;
        }
        const built = buildChatMessages(history);
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
        setMessages([]);
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
    messagesEndRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages]);

  useEffect(() => {
    const el = textareaRef.current;
    if (el) {
      el.style.height = "auto";
      el.style.height = Math.min(el.scrollHeight, 200) + "px";
    }
  }, [input]);

  const handleSend = useCallback(async (overrideText?: string) => {
    // overrideText lets caller post a message that didn't come from
    // the composer (e.g. the plan-approval button clicking "go"). When
    // present it bypasses the input field entirely — composer state
    // stays as the user left it.
    const composerText = (overrideText ?? input).trim();
    const text = composerText;
    // Allow sending with attachments only (no text), but require at least one.
    if ((!text && attachments.length === 0) || !selectedAgent || sending) return;

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
    const turnFiles: ProducedFile[] = [];
    const seenPaths = new Set<string>();

    const startNewGroup = () => {
      curGroupId = `tg-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`;
      curCalls = [];
      curContent = "";
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
          case "content": {
            const content = evt.data?.content || "";
            const meta = evt.data?.metadata;
            if (content === "__NEW_SESSION__") {
              handleNewChat();
              loadSessions(selectedAgent);
              return;
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
            const content = curContent;
            setMessages((prev) => {
              // If last message is the thinking content for this round, replace with tool-group
              const last = prev[prev.length - 1];
              if (content && last?.role === "agent" && last.content === content) {
                return [
                  ...prev.slice(0, -1),
                  { id: groupId, role: "tool-group" as const, content, timestamp: Date.now(), toolCalls: calls },
                ];
              }
              // Update existing tool-group for this round
              const idx = prev.findIndex((m) => m.id === groupId);
              if (idx >= 0) {
                const updated = [...prev];
                updated[idx] = { ...updated[idx], toolCalls: calls };
                return updated;
              }
              // New tool-group
              return [
                ...prev,
                { id: groupId, role: "tool-group" as const, content, timestamp: Date.now(), toolCalls: calls },
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
          case "error": {
            // Surface backend errors as a chat bubble. Without this the
            // turn just hangs — the model failed (provider 4xx/5xx,
            // serialization mismatch, etc.) and the only signal was a
            // gateway log line the user can't see.
            const msg = evt.data?.message || "Unknown error";
            setMessages((prev) => [
              ...prev,
              { id: `e-${Date.now()}`, role: "agent", content: `Error: ${msg}`, timestamp: Date.now() },
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
                    tc.result === undefined ? { ...tc, result: "(stopped)" } : tc,
                  ),
                }
              : m,
          ),
        );
        setMessages((prev) => [
          ...prev,
          { id: `e-${Date.now()}`, role: "agent", content: "(Stopped)", timestamp: Date.now() },
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
            : "Failed to get a response. Is the gateway running?";
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
      abortRef.current = null;
      setSending(false);
      // Belt-and-suspenders: the subagent's done event clears this on
      // the happy path, but if a network blip drops that event we don't
      // want a stale "iteration 5/20" sitting under a finished turn.
      setSubagentProgress(null);
      textareaRef.current?.focus();
    }
  }, [input, attachments, selectedAgent, sessionId, sending, loadSessions, pathname, router, urlProjectId]);

  const handleStop = useCallback(() => {
    abortRef.current?.abort();
  }, []);

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
    if (slashOpen && filteredSkills.length > 0) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setSlashIndex((i) => (i + 1) % filteredSkills.length);
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        setSlashIndex((i) => (i - 1 + filteredSkills.length) % filteredSkills.length);
        return;
      }
      if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault();
        selectSkill(filteredSkills[slashIndex]);
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
      handleSend();
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
  const heroTitle = "What can I do for you?";

  return (
    <div className="flex h-[calc(100vh-3rem)] flex-row">
      <div
        className={
          "flex flex-1 min-w-0 flex-col" +
          (isEmpty ? " justify-center" : "")
        }
      >
      {/* Messages */}
        <div
          className={
            "min-h-0 px-4 " +
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
                        agentId={selectedAgent}
                        files={r.files!}
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
                        />
                        {filePanels}
                      </div>,
                    );
                  }
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
                    className={`group relative max-w-[80%] ${
                      msg.role === "user" ? "order-1" : ""
                    }`}
                  >
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
                                aria-label={`Preview ${att.name}`}
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
                        <div className={CHAT_PROSE_CLASS}>
                          {renderContentWithDataImages(
                            msg.content,
                            surfacedSrcs,
                            (attachedImages.get(msg.id)?.length ?? 0) > 0,
                          ) ?? (
                            <ReactMarkdown remarkPlugins={[remarkGfm]} urlTransform={makeUrlTransform(selectedAgent, sessionId)} components={{ a: ExternalAnchor }}>
                              {msg.content}
                            </ReactMarkdown>
                          )}
                        </div>
                      )}
                      {msg.role === "agent" && msg.metadata?.iterationCapReached && (
                        <div className="mt-2 flex items-start gap-1.5 rounded-md border border-amber-500/40 bg-amber-500/10 px-2.5 py-1.5 text-xs text-amber-900 dark:text-amber-200">
                          <span className="font-medium">Iteration limit reached</span>
                          <span className="opacity-80">
                            Agent hit the {msg.metadata.iterationCapValue ?? ""} tool-call budget before finishing. The answer above was synthesized from partial results — fields may be marked unknown / partial. Continue the conversation to push further.
                          </span>
                        </div>
                      )}
                      {msg.role === "agent" && msg.metadata?.planMode && (
                        <div className="mt-2 flex items-start gap-1.5 rounded-md border border-amber-500/40 bg-amber-500/10 px-2.5 py-1.5 text-xs text-amber-900 dark:text-amber-200">
                          <ListChecks className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                          <span className="font-medium">Plan only — review before executing.</span>
                          <span className="opacity-80">
                            Tools were disabled for this turn. Reply with &quot;go&quot; (or edits) to run it.
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
                            Run plan
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
                            Edit
                          </Button>
                          <span className="text-xs text-muted-foreground">
                            Run plan to authorize the agent end-to-end, or Edit to revise below.
                          </span>
                        </div>
                      )}
                    </div>
                    {msg.files && msg.files.length > 0 && (
                      <FilesPanel agentId={selectedAgent} files={msg.files} />
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
                            title="Copy"
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
                            title="Resend (refills the composer)"
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
                            title="Copy"
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
                            title="View task files"
                          >
                            <FolderOpen className="h-3 w-3" />
                            <span>Files</span>
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

        {/* Input */}
        <div className="shrink-0 px-4 pb-6 pt-2">
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
                This conversation lives on{" "}
                <span className="font-medium text-foreground">
                  {channelLabel(currentChannel)}
                </span>
                . Reply from there — messages typed here won't reach the user on
                the other side.
              </div>
            )}
            {isActAsView && !isReadOnlyChannel && (
              // Super_admin viewing another user's chat via the admin
              // Chats page (?actAs=<uid>). The middleware gates this as
              // read-only for the whole request, so any send would 403
              // — disable the composer and surface why.
              <div className="mb-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
                Read-only — you&apos;re viewing another user&apos;s chat.
                Sending messages here is disabled.
              </div>
            )}
            {slashOpen && filteredSkills.length > 0 && (
              <SlashMenu
                skills={filteredSkills}
                activeIndex={slashIndex}
                onHover={setSlashIndex}
                onSelect={selectSkill}
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
                            aria-label={`Preview ${f.name}`}
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
                            aria-label="Remove attachment"
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
                          aria-label="Remove attachment"
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
                        ? "Read-only — viewing another user's chat"
                        : isReadOnlyChannel
                          ? `Read-only — reply from ${channelLabel(currentChannel)}`
                          : selectedAgent
                            ? `Message ${agentName || selectedAgent}... ("/" to pick a skill)`
                            : "Select an agent first"
                    }
                    disabled={!selectedAgent || sending || isReadOnlyView}
                    rows={3}
                    className="block w-full resize-none bg-transparent text-[15px] placeholder:text-muted-foreground/50 outline-none disabled:opacity-50"
                    style={{ maxHeight: 240, minHeight: 72 }}
                  />
                  <div className="mt-2 flex items-center justify-between">
                    <div className="flex items-center gap-2 min-w-0">
                      <label
                        className={`flex h-9 w-9 shrink-0 items-center justify-center rounded-full border border-border text-muted-foreground transition-colors ${
                          !selectedAgent || sending || isReadOnlyView
                            ? "opacity-50 cursor-not-allowed"
                            : "hover:bg-muted hover:text-foreground cursor-pointer"
                        }`}
                        aria-label="Attach files"
                      >
                        <Paperclip className="h-4 w-4" />
                        <input
                          ref={fileInputRef}
                          type="file"
                          multiple
                          className="sr-only"
                          onChange={handleFilePick}
                          disabled={!selectedAgent || sending || isReadOnlyView}
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
                        aria-label="Stop generating"
                      >
                        <Square className="h-3.5 w-3.5 fill-current" />
                      </Button>
                    ) : (
                      <Button
                        onClick={() => handleSend()}
                        disabled={(!input.trim() && attachments.length === 0) || !selectedAgent || isReadOnlyView}
                        size="icon"
                        className="h-9 w-9 shrink-0 rounded-full"
                        aria-label="Send message"
                      >
                        <Send className="h-4 w-4" />
                      </Button>
                    )}
                  </div>
                </>
              ) : (
                <div className="flex items-end gap-2">
                  <label
                    className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-lg text-muted-foreground transition-colors ${
                      !selectedAgent || sending || isReadOnlyView
                        ? "opacity-50 cursor-not-allowed"
                        : "hover:bg-muted hover:text-foreground cursor-pointer"
                    }`}
                    aria-label="Attach files"
                  >
                    <Paperclip className="h-4 w-4" />
                    <input
                      ref={fileInputRef}
                      type="file"
                      multiple
                      className="sr-only"
                      onChange={handleFilePick}
                      disabled={!selectedAgent || sending || isReadOnlyView}
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
                        ? "Read-only — viewing another user's chat"
                        : isReadOnlyChannel
                          ? `Read-only — reply from ${channelLabel(currentChannel)}`
                          : selectedAgent
                            ? `Message ${agentName || selectedAgent}... ("/" to pick a skill)`
                            : "Select an agent first"
                    }
                    disabled={!selectedAgent || sending || isReadOnlyView}
                    rows={1}
                    className="flex-1 resize-none bg-transparent text-[15px] placeholder:text-muted-foreground/50 outline-none disabled:opacity-50"
                    style={{ maxHeight: 200, minHeight: 24 }}
                  />
                  {sending ? (
                    <Button
                      onClick={handleStop}
                      size="icon"
                      className="h-8 w-8 shrink-0 rounded-lg"
                      aria-label="Stop generating"
                    >
                      <Square className="h-3.5 w-3.5 fill-current" />
                    </Button>
                  ) : (
                    <Button
                      onClick={() => handleSend()}
                      disabled={(!input.trim() && attachments.length === 0) || !selectedAgent || isReadOnlyView}
                      size="icon"
                      className="h-8 w-8 shrink-0 rounded-lg"
                      aria-label="Send message"
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
            aria-label="Image preview"
          >
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img
              src={lightboxSrc}
              alt="Preview"
              className="max-h-full max-w-full rounded-lg shadow-2xl"
              onClick={(e) => e.stopPropagation()}
            />
            <button
              type="button"
              onClick={() => setLightboxSrc(null)}
              className="absolute right-4 top-4 flex h-9 w-9 items-center justify-center rounded-full bg-background/80 text-foreground hover:bg-background"
              aria-label="Close preview"
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
          onClose={() => setFilesSheetOpen(false)}
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
function ToolCallGroup({ msg, surfacedSrcs, agentId, sessionId, nested = false, roundIndex, subagentProgress }: { msg: ChatMessage; surfacedSrcs?: ReadonlySet<string>; agentId: string; sessionId: string; nested?: boolean; roundIndex?: number; subagentProgress?: { iteration?: number; max?: number; phase?: "thinking" | "running" | "final-delivery" | "done"; tools?: string[] } | null }) {
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
          <div className={CHAT_PROSE_CLASS}>
            {renderContentWithDataImages(msg.content, surfacedSrcs) ?? (
              <ReactMarkdown remarkPlugins={[remarkGfm]} urlTransform={makeUrlTransform(agentId, sessionId)} components={{ a: ExternalAnchor }}>
                {msg.content}
              </ReactMarkdown>
            )}
          </div>
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
                ? `Executed ${tools.length} tool${tools.length > 1 ? "s" : ""}`
                : `Running tools (${doneCount}/${tools.length})...`}
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
                        title="Executed inside a sandboxed container"
                      >
                        <ShieldCheck className="h-2.5 w-2.5" />
                        sandbox
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
                        <p className="text-[10px] font-medium text-muted-foreground uppercase mb-1">Input</p>
                        <pre className="text-xs font-mono bg-muted/50 rounded p-2 overflow-x-auto whitespace-pre-wrap break-all max-h-40">
                          {(() => {
                            try { return JSON.stringify(JSON.parse(tc.arguments), null, 2); }
                            catch { return tc.arguments; }
                          })()}
                        </pre>
                      </div>
                      {tc.result != null ? (
                        <div>
                          <p className="text-[10px] font-medium text-muted-foreground uppercase mb-1">Output</p>
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
                            const counter = it && mx ? `Iteration ${it}/${mx}` : "Sub-agent running";
                            let detail = "";
                            if (phase === "thinking") detail = "thinking";
                            else if (phase === "running" && tools?.length) detail = `running ${tools.join(", ")}`;
                            else if (phase === "final-delivery") detail = "synthesizing final answer";
                            return detail ? `${counter} · ${detail}` : counter;
                          })()}
                        </div>
                      ) : tc.name === "delegate_task" && tc.result == null && tc.id !== activeDelegateId ? (
                        <p className="text-xs text-muted-foreground/60 italic">Queued (waiting on prior sub-agent)…</p>
                      ) : (
                        <p className="text-xs text-muted-foreground/60 italic">Executing...</p>
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
      <div className="max-w-[85%] space-y-2">{inner}</div>
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
}: {
  rounds: ChatMessage[];
  surfacedSrcs?: ReadonlySet<string>;
  agentId: string;
  sessionId: string;
  subagentProgress?: { iteration?: number; max?: number; phase?: "thinking" | "running" | "final-delivery" | "done"; tools?: string[] } | null;
}) {
  const [open, setOpen] = useState(false);
  const allTools = rounds.flatMap((r) => r.toolCalls || []);
  const totalTools = allTools.length;
  const doneCount = allTools.filter((tc) => tc.result != null).length;
  const allDone = doneCount === totalTools;
  return (
    <div className="flex justify-start">
      <div className="max-w-[85%] w-full">
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
                ? `Used ${totalTools} tool${totalTools === 1 ? "" : "s"} across ${rounds.length} round${rounds.length === 1 ? "" : "s"}`
                : `Running tools… (${doneCount}/${totalTools} across ${rounds.length} rounds)`}
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
  if (["mp4", "webm", "mov", "mkv"].includes(ext)) return { icon: Film, preview: "none" };
  if (["mp3", "wav", "ogg", "flac", "m4a"].includes(ext)) return { icon: Music, preview: "none" };
  if (["js", "ts", "tsx", "jsx", "py", "go", "rs", "c", "cpp", "h", "java", "rb", "sh", "json", "yaml", "yml", "toml", "xml", "css"].includes(ext))
    return { icon: FileCode, preview: "text" };
  if (["txt", "csv", "log"].includes(ext)) return { icon: FileText, preview: "text" };
  return { icon: File, preview: "none" };
}

function formatBytes(n?: number): string {
  if (n === undefined) return "";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

function fileUrl(agentId: string, path: string, download: boolean): string {
  const token = getAuthToken();
  const encoded = path.split("/").map(encodeURIComponent).join("/");
  const params = new URLSearchParams();
  if (download) params.set("download", "1");
  if (token) params.set("token", token);
  const qs = params.toString();
  return `/api/agents/${agentId}/files/${encoded}${qs ? "?" + qs : ""}`;
}

function zipUrl(agentId: string, sessionId: string, projectId?: string): string {
  const token = getAuthToken();
  const params = new URLSearchParams();
  // projectId wins when both are present — same precedence as the
  // backend's fileScopeForRequest, which treats projectId-without-
  // session as "whole project zip".
  if (projectId) params.set("projectId", projectId);
  else if (sessionId) params.set("sessionId", sessionId);
  if (token) params.set("token", token);
  const qs = params.toString();
  return `/api/agents/${agentId}/files.zip${qs ? "?" + qs : ""}`;
}

function FilesPanel({ agentId, files }: { agentId: string; files: ProducedFile[] }) {
  const [previewing, setPreviewing] = useState<ProducedFile | null>(null);
  return (
    <>
      <div className="mt-2 space-y-1.5 max-w-[85%]">
        <p className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground/70">
          Your files
        </p>
        <div className="flex flex-col gap-1.5">
          {files.map((f) => {
            const { icon: Icon } = fileKind(f.path);
            const basename = f.path.split("/").pop() || f.path;
            const downloadUrl = fileUrl(agentId, f.path, true);
            return (
              <div
                key={f.path}
                className="group flex items-center gap-2.5 rounded-lg border border-border bg-card/50 px-3 py-2 hover:bg-card/80 transition-colors"
              >
                <Icon className="h-4 w-4 text-muted-foreground shrink-0" />
                <button
                  onClick={() => setPreviewing(f)}
                  className="flex-1 min-w-0 text-left"
                  title="Open preview"
                >
                  <div className="text-sm font-medium text-foreground truncate">{basename}</div>
                  {f.size !== undefined && (
                    <div className="text-[11px] text-muted-foreground/70">{formatBytes(f.size)}</div>
                  )}
                </button>
                <a
                  href={downloadUrl}
                  download={basename}
                  className="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors"
                  title="Download"
                >
                  <Download className="h-4 w-4" />
                </a>
              </div>
            );
          })}
        </div>
      </div>
      {previewing && (
        <FilePreview
          agentId={agentId}
          file={previewing}
          onClose={() => setPreviewing(null)}
        />
      )}
    </>
  );
}

const FILES_PANEL_MIN = 280;
const FILES_PANEL_MAX = 640;
const FILES_PANEL_DEFAULT = 280;
const FILES_PANEL_KEY = "chat:filesPanelWidth";

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
function WorkspacePanel({
  agentId,
  sessionId,
  projectId,
  onClose,
}: {
  agentId: string;
  sessionId: string;
  projectId?: string;
  onClose: () => void;
}) {
  const [files, setFiles] = useState<WorkspaceFile[]>([]);
  const [loading, setLoading] = useState(false);
  const [previewing, setPreviewing] = useState<ProducedFile | null>(null);
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
        alert(res.error || "Could not open workspace folder");
      }
    } finally {
      setRevealing(false);
    }
  }, [agentId, sessionId, projectId]);

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
    } finally {
      setLoading(false);
    }
  }, [agentId, sessionId, projectId]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  return (
    <>
      <aside
        style={{ width }}
        className="relative z-30 hidden md:flex shrink-0 flex-col border-l border-border bg-background -mt-12 h-screen"
      >
        <div
          onMouseDown={(e) => { e.preventDefault(); setResizing(true); }}
          className={`absolute -left-1 top-0 bottom-0 w-2 cursor-col-resize z-10 group ${resizing ? "" : ""}`}
          title="Drag to resize"
        >
          <div
            className={`absolute inset-y-0 left-1/2 w-px -translate-x-1/2 transition-colors ${
              resizing ? "bg-primary" : "bg-transparent group-hover:bg-primary/40"
            }`}
          />
        </div>
        <div className="flex items-center justify-between gap-2 px-4 h-12 border-b border-border">
          <div className="flex items-center gap-2 text-sm font-medium">
            <FolderOpen className="h-4 w-4" />
            Workspace
          </div>
          <div className="flex items-center gap-1">
            <a
              href={
                files.length > 0
                  ? zipUrl(agentId, sessionId, projectId)
                  : undefined
              }
              aria-disabled={files.length === 0}
              className={`p-1.5 rounded-md transition-colors ${
                files.length === 0
                  ? "text-muted-foreground/40 pointer-events-none"
                  : "text-muted-foreground hover:text-foreground hover:bg-muted/50"
              }`}
              title="Download all as zip"
            >
              <Download className="h-4 w-4" />
            </a>
            {/* Open the workspace folder in the operator's native file
                browser. Self-hosted only — hosted deployments don't
                expose a meaningful "local folder" so the button is
                hidden entirely (we learned the mode from /api/me at
                mount). */}
            {deployMode === "self-hosted" && (
              <button
                onClick={handleReveal}
                disabled={revealing || (!sessionId && !projectId)}
                className="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors disabled:opacity-50"
                title="Open folder in Finder"
              >
                <FolderSearch className="h-4 w-4" />
              </button>
            )}
            <button
              onClick={refresh}
              disabled={loading}
              className="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors disabled:opacity-50"
              title="Refresh"
            >
              <RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} />
            </button>
            <button
              onClick={onClose}
              className="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors"
              title="Close"
            >
              <X className="h-4 w-4" />
            </button>
          </div>
        </div>
        <div className="flex-1 overflow-y-auto p-2">
          {!loading && files.length === 0 ? (
            <p className="px-3 py-8 text-center text-sm text-muted-foreground">
              {projectId
                ? "No files in this project yet."
                : "No files in this session yet."}
            </p>
          ) : (
            <div className="flex flex-col">
              <div className="grid grid-cols-[1fr_auto_auto] gap-3 px-3 py-2 text-[11px] font-medium uppercase tracking-wider text-muted-foreground/70 border-b">
                <span>Name</span>
                <span>Modified</span>
                <span>Size</span>
              </div>
              {files.map((f) => {
                const { icon: Icon } = fileKind(f.path);
                const basename = f.path.split("/").pop() || f.path;
                const downloadUrl = fileUrl(agentId, f.path, true);
                return (
                  <div
                    key={f.path}
                    className="group grid grid-cols-[1fr_auto_auto] items-center gap-3 px-3 py-2 hover:bg-muted/40 rounded-md transition-colors"
                  >
                    <button
                      onClick={() => setPreviewing({ path: f.path, size: f.size })}
                      className="flex items-center gap-2 min-w-0 text-left"
                      title="Open preview"
                    >
                      <Icon className="h-4 w-4 text-muted-foreground shrink-0" />
                      <span className="text-sm text-foreground truncate">{basename}</span>
                    </button>
                    <span className="text-[11px] text-muted-foreground/70 whitespace-nowrap">
                      {formatRelativeTime(f.modTime)}
                    </span>
                    <a
                      href={downloadUrl}
                      download={basename}
                      className="text-[11px] text-muted-foreground/70 whitespace-nowrap hover:text-foreground"
                      title="Download"
                    >
                      {formatBytes(f.size)}
                    </a>
                  </div>
                );
              })}
            </div>
          )}
        </div>
      </aside>
      {previewing && (
        <FilePreview
          agentId={agentId}
          file={previewing}
          onClose={() => setPreviewing(null)}
        />
      )}
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

function FilePreview({ agentId, file, onClose }: { agentId: string; file: ProducedFile; onClose: () => void }) {
  const { preview } = fileKind(file.path);
  const src = fileUrl(agentId, file.path, false);
  const downloadUrl = fileUrl(agentId, file.path, true);
  const basename = file.path.split("/").pop() || file.path;
  const [text, setText] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [htmlView, setHtmlView] = useState<"rendered" | "source">("rendered");

  useEffect(() => {
    // HTML fetches its text lazily only when the user switches to source view.
    if (preview !== "markdown" && preview !== "text") return;
    let cancelled = false;
    fetch(src)
      .then((r) => { if (!r.ok) throw new Error(`HTTP ${r.status}`); return r.text(); })
      .then((t) => { if (!cancelled) setText(t); })
      .catch((e) => { if (!cancelled) setError(String(e)); });
    return () => { cancelled = true; };
  }, [src, preview]);

  useEffect(() => {
    if (preview !== "html" || htmlView !== "source" || text !== null) return;
    let cancelled = false;
    fetch(src)
      .then((r) => { if (!r.ok) throw new Error(`HTTP ${r.status}`); return r.text(); })
      .then((t) => { if (!cancelled) setText(t); })
      .catch((e) => { if (!cancelled) setError(String(e)); });
    return () => { cancelled = true; };
  }, [src, preview, htmlView, text]);

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-background/80 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="flex h-[85vh] w-full max-w-4xl flex-col rounded-xl border border-border bg-card shadow-2xl" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between border-b border-border px-4 py-3 shrink-0">
          <div className="flex items-center gap-2 min-w-0">
            <FileText className="h-4 w-4 text-muted-foreground shrink-0" />
            <span className="font-medium text-sm truncate">{basename}</span>
            {file.size !== undefined && (
              <span className="text-[11px] text-muted-foreground shrink-0">{formatBytes(file.size)}</span>
            )}
          </div>
          <div className="flex items-center gap-1 shrink-0">
            {preview === "html" && (
              <button
                onClick={() => setHtmlView(htmlView === "rendered" ? "source" : "rendered")}
                className="rounded-md p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted/50"
                title={htmlView === "rendered" ? "View source" : "View rendered"}
              >
                {htmlView === "rendered" ? <Code2 className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
            )}
            <a
              href={downloadUrl}
              download={basename}
              className="rounded-md p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted/50"
              title="Download"
            >
              <Download className="h-4 w-4" />
            </a>
            <button
              onClick={onClose}
              className="rounded-md p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted/50"
              title="Close"
            >
              <X className="h-4 w-4" />
            </button>
          </div>
        </div>
        <div className="flex-1 overflow-auto p-4 min-h-0">
          {preview === "image" && (
            <img src={src} alt={basename} className="max-w-full max-h-full mx-auto object-contain" />
          )}
          {preview === "pdf" && (
            <iframe src={src} className="h-full w-full border-0" title={basename} />
          )}
          {preview === "markdown" && (
            error ? <p className="text-sm text-destructive">Failed to load: {error}</p>
            : text === null ? <p className="text-sm text-muted-foreground">Loading…</p>
            : (
              <div className="prose prose-sm dark:prose-invert max-w-none">
                <ReactMarkdown remarkPlugins={[remarkGfm]} components={{ a: ExternalAnchor }}>{text}</ReactMarkdown>
              </div>
            )
          )}
          {preview === "text" && (
            error ? <p className="text-sm text-destructive">Failed to load: {error}</p>
            : text === null ? <p className="text-sm text-muted-foreground">Loading…</p>
            : (
              <pre className="text-xs font-mono whitespace-pre-wrap break-all bg-muted/30 rounded p-3">{text}</pre>
            )
          )}
          {preview === "html" && (
            htmlView === "rendered" ? (
              // sandbox="allow-scripts" runs the page in a null origin: CSS,
              // animations, charts work, but scripts can't reach parent
              // cookies/storage/API — safe for untrusted agent output.
              <iframe
                src={src}
                sandbox="allow-scripts"
                className="h-full w-full border-0 rounded bg-white"
                title={basename}
              />
            ) : error ? <p className="text-sm text-destructive">Failed to load: {error}</p>
            : text === null ? <p className="text-sm text-muted-foreground">Loading…</p>
            : (
              <pre className="text-xs font-mono whitespace-pre-wrap break-all bg-muted/30 rounded p-3">{text}</pre>
            )
          )}
          {preview === "none" && (
            <div className="flex flex-col items-center justify-center h-full gap-3 text-center">
              <File className="h-12 w-12 text-muted-foreground/50" />
              <p className="text-sm text-muted-foreground">Preview not available for this file type.</p>
              <a href={downloadUrl} download={basename} className="inline-flex items-center gap-2 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90">
                <Download className="h-3.5 w-3.5" /> Download
              </a>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function SlashMenu({
  skills,
  activeIndex,
  onHover,
  onSelect,
}: {
  skills: SkillInfo[];
  activeIndex: number;
  onHover: (i: number) => void;
  onSelect: (s: SkillInfo) => void;
}) {
  return (
    <div className="absolute bottom-full left-0 right-0 mb-2 rounded-xl border border-border bg-popover shadow-lg overflow-hidden z-20">
      <div className="max-h-[320px] overflow-y-auto py-1">
        {skills.map((s, i) => (
          <button
            key={s.name}
            // onMouseDown fires before the textarea's onBlur so the click
            // isn't swallowed by the blur-driven menu close.
            onMouseDown={(e) => {
              e.preventDefault();
              onSelect(s);
            }}
            onMouseEnter={() => onHover(i)}
            className={`w-full flex items-start gap-3 px-3 py-2 text-left transition-colors ${
              i === activeIndex ? "bg-muted/60" : "hover:bg-muted/40"
            }`}
          >
            <Puzzle className="h-4 w-4 mt-0.5 shrink-0 text-muted-foreground" />
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-2">
                <p className="text-sm font-medium truncate">{s.name}</p>
                <span className="text-[10px] uppercase tracking-wider text-muted-foreground/70">
                  {s.type || "skill"}
                </span>
              </div>
              {s.description && (
                <p className="text-xs text-muted-foreground line-clamp-1">
                  {s.description}
                </p>
              )}
            </div>
          </button>
        ))}
      </div>
      <Link
        href="/skills/"
        className="flex items-center gap-2 border-t border-border px-3 py-2 text-xs text-muted-foreground hover:text-foreground hover:bg-muted/30 transition-colors"
      >
        <SlidersHorizontal className="h-3.5 w-3.5" />
        Manage Skills
      </Link>
    </div>
  );
}
