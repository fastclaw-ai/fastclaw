"use client";

import { useEffect, useState, useRef, useCallback } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { getChatHistory, getChatSessions, sendChatStream, getAuthToken, type ChatHistoryMessage, type ChatStreamEvent } from "@/lib/api";
import { Bot, Send, Copy, Check, Plus, MessageSquare, Wrench, ChevronDown, ChevronRight, Download, X, File, FileText, Image as ImageIcon, FileCode, Film, Music } from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

interface ProducedFile {
  path: string; // path relative to workspace
  size?: number;
}

interface ChatMessage {
  id: string;
  role: "user" | "agent" | "tool-group";
  content: string;
  timestamp: number;
  toolCalls?: { id: string; name: string; arguments: string; result?: string }[];
  files?: ProducedFile[];
}

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
  preview: string;
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
      msgs.push({ id: `h-${i}`, role: "user", content: h.content || "", timestamp: 0 });
      i++;
    } else if (h.role === "assistant" && h.toolCalls && h.toolCalls.length > 0) {
      // Group: assistant tool_calls + following tool results + final assistant content
      const calls = h.toolCalls.map((tc) => ({ ...tc, result: undefined as string | undefined }));
      i++;
      // Collect tool results
      while (i < history.length && history[i].role === "tool") {
        const toolMsg = history[i];
        const call = calls.find((c) => c.id === toolMsg.toolCallId);
        if (call) call.result = toolMsg.content;
        i++;
      }
      // Show as tool-group
      msgs.push({
        id: `h-tool-${i}`,
        role: "tool-group",
        content: h.content || "",
        timestamp: 0,
        toolCalls: calls,
      });
      // If next is assistant with content (final answer), add it
      if (i < history.length && history[i].role === "assistant" && history[i].content) {
        msgs.push({ id: `h-${i}`, role: "agent", content: history[i].content || "", timestamp: 0 });
        i++;
      }
    } else if (h.role === "assistant") {
      msgs.push({ id: `h-${i}`, role: "agent", content: h.content || "", timestamp: 0 });
      i++;
    } else {
      i++; // skip unexpected
    }
  }
  return msgs;
}

function getAgentIdFromURL(): string {
  if (typeof window === "undefined") return "default";
  const match = window.location.pathname.match(/\/agents\/([^/]+)\//);
  return match ? match[1] : "default";
}

export default function AgentChatPage() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const [selectedAgent] = useState(() => getAgentIdFromURL());
  const [sessionId, setSessionId] = useState<string>(() => searchParams.get("session") || generateSessionId());
  const [sessions, setSessions] = useState<ChatSession[]>([]);
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState("");
  const [sending, setSending] = useState(false);
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

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

  // Load history when session changes
  useEffect(() => {
    if (!selectedAgent || !sessionId) return;
    getChatHistory(selectedAgent, sessionId)
      .then((history) => {
        if (!history || history.length === 0) {
          setMessages([]);
          return;
        }
        setMessages(buildChatMessages(history));
      })
      .catch(() => setMessages([]));
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

  const handleSend = useCallback(async () => {
    const text = input.trim();
    if (!text || !selectedAgent || sending) return;

    // Pin the sessionId into the URL on the first send so a refresh keeps
    // the user in the same conversation. Use history.replaceState (not
    // router.replace) to avoid Next.js remounting the page and interrupting
    // the stream we're about to start.
    if (typeof window !== "undefined" && !window.location.search.includes("session=")) {
      window.history.replaceState(null, "", `/agents/${selectedAgent}/chat/?session=${sessionId}`);
    }

    setInput("");
    setMessages((prev) => [
      ...prev,
      { id: `u-${Date.now()}`, role: "user", content: text, timestamp: Date.now() },
    ]);
    setSending(true);

    let curGroupId = "";
    let curCalls: { id: string; name: string; arguments: string; result?: string }[] = [];
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
      await sendChatStream(selectedAgent, sessionId, text, (evt: ChatStreamEvent) => {
        switch (evt.type) {
          case "content": {
            const content = evt.data?.content || "";
            if (content === "__NEW_SESSION__") {
              handleNewChat();
              loadSessions(selectedAgent);
              return;
            }
            if (curCalls.length > 0) {
              // Content after tool calls = new round. Finalize current group, start fresh.
              startNewGroup();
            }
            // Store as thinking content (may become part of next tool-group, or stay as final answer)
            curContent = content;
            setMessages((prev) => [
              ...prev,
              { id: `a-${Date.now()}`, role: "agent", content, timestamp: Date.now() },
            ]);
            break;
          }
          case "tool_call": {
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
            if (tc) tc.result = resultText;
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
        }
      });
      // Attach files produced this turn to the final message so the UI can
      // render a "Your files" panel below it.
      if (turnFiles.length > 0) {
        setMessages((prev) => {
          if (prev.length === 0) return prev;
          const updated = [...prev];
          const last = updated[updated.length - 1];
          updated[updated.length - 1] = { ...last, files: turnFiles };
          return updated;
        });
      }
      loadSessions(selectedAgent);
    } catch {
      setMessages((prev) => [
        ...prev,
        { id: `e-${Date.now()}`, role: "agent", content: "Failed to get a response. Is the gateway running?", timestamp: Date.now() },
      ]);
    } finally {
      setSending(false);
      textareaRef.current?.focus();
    }
  }, [input, selectedAgent, sessionId, sending, loadSessions]);

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    // Don't submit while an IME composition is active — Enter in that state
    // is the user confirming the IME candidate (e.g. pinyin → 好), not
    // sending the message. keyCode 229 also signals "composing" on some
    // browsers where isComposing isn't set.
    if (e.nativeEvent.isComposing || e.keyCode === 229) return;
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  };

  const handleCopy = (msg: ChatMessage) => {
    navigator.clipboard.writeText(msg.content);
    setCopiedId(msg.id);
    setTimeout(() => setCopiedId(null), 1500);
  };

  const handleNewChat = () => {
    const newId = generateSessionId();
    setSessionId(newId);
    setMessages([]);
    router.replace(`/agents/${selectedAgent}/chat/`);
  };

  const handleSelectSession = (sid: string) => {
    setSessionId(sid);
    router.replace(`/agents/${selectedAgent}/chat/?session=${sid}`);
  };

  const formatTime = (ts: number) =>
    new Date(ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

  return (
    <div className="flex h-[calc(100vh-3rem)] md:h-screen">
      {/* Sidebar: sessions only */}
      <div className="hidden w-56 flex-col border-r border-border bg-card/30 lg:flex">
        <div className="flex h-14 items-center justify-between border-b border-border px-4">
          <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
            Chat
          </p>
          <button onClick={handleNewChat} title="New chat" className="rounded-md p-1 text-muted-foreground hover:text-foreground hover:bg-muted/50">
            <Plus className="h-4 w-4" />
          </button>
        </div>
        <div className="flex-1 overflow-auto p-2 space-y-1">
          {sessions.map((s) => (
            <button
              key={s.id}
              onClick={() => handleSelectSession(s.id)}
              className={`flex w-full items-center gap-2 rounded-lg px-3 py-2 text-sm transition-colors ${
                sessionId === s.id
                  ? "bg-primary/10 text-primary"
                  : "text-muted-foreground hover:bg-muted/50 hover:text-foreground"
              }`}
            >
              <MessageSquare className="h-3.5 w-3.5 shrink-0" />
              <span className="truncate text-xs">{s.preview}</span>
            </button>
          ))}
          {sessions.length === 0 && (
            <p className="text-xs text-muted-foreground text-center py-4">No sessions yet</p>
          )}
        </div>
      </div>

      {/* Chat area */}
      <div className="flex flex-1 flex-col">
        {/* Chat header */}
        <div className="flex h-14 items-center border-b border-border px-4 shrink-0">
          <div className="flex items-center gap-2.5">
            <div className="flex h-7 w-7 items-center justify-center rounded-full bg-primary/10">
              <Bot className="h-4 w-4 text-primary" />
            </div>
            <span className="text-sm font-semibold">
              {selectedAgent}
            </span>
          </div>
        </div>

        {/* Messages */}
        <div className="flex-1 overflow-y-auto min-h-0 px-4 py-4">
          <div className="mx-auto max-w-2xl space-y-3">
            {messages.length === 0 && (
              <div className="flex flex-col items-center justify-center py-24 text-center">
                <div className="flex h-16 w-16 items-center justify-center rounded-full bg-muted mb-4">
                  <Bot className="h-8 w-8 text-muted-foreground" />
                </div>
                <p className="text-lg font-medium mb-1">
                  Chat with {selectedAgent || "your agent"}
                </p>
                <p className="text-sm text-muted-foreground">
                  Send a message to start a conversation
                </p>
              </div>
            )}

            {messages.map((msg) =>
              msg.role === "tool-group" ? (
                <div key={msg.id}>
                  <ToolCallGroup msg={msg} />
                  {msg.files && msg.files.length > 0 && (
                    <FilesPanel agentId={selectedAgent} files={msg.files} />
                  )}
                </div>
              ) : (
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
                      className={`rounded-2xl px-4 py-2.5 ${
                        msg.role === "user"
                          ? "bg-primary text-primary-foreground rounded-br-md"
                          : "bg-muted rounded-bl-md"
                      }`}
                    >
                      <div className="text-[15px] leading-relaxed prose prose-sm dark:prose-invert max-w-none prose-p:my-1 prose-pre:my-2 prose-ul:my-1 prose-ol:my-1">
                        <ReactMarkdown remarkPlugins={[remarkGfm]}>
                          {msg.content}
                        </ReactMarkdown>
                      </div>
                    </div>
                    {msg.files && msg.files.length > 0 && (
                      <FilesPanel agentId={selectedAgent} files={msg.files} />
                    )}
                    <div
                      className={`flex items-center gap-1.5 mt-1 ${
                        msg.role === "user" ? "justify-end" : "justify-start"
                      }`}
                    >
                      {msg.timestamp > 0 && (
                        <span className="text-[10px] text-muted-foreground/60">
                          {formatTime(msg.timestamp)}
                        </span>
                      )}
                      {msg.role === "agent" && (
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
                      )}
                    </div>
                  </div>
                </div>
              )
            )}

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

        {/* Input */}
        <div className="shrink-0 px-4 pb-6 pt-2">
          <div className="mx-auto max-w-2xl">
            <div className="flex items-end gap-2 rounded-xl border border-border bg-card px-4 py-3 focus-within:ring-2 focus-within:ring-ring/20 transition-shadow">
              <textarea
                ref={textareaRef}
                value={input}
                onChange={(e) => setInput(e.target.value)}
                onKeyDown={handleKeyDown}
                placeholder={
                  selectedAgent
                    ? `Message ${selectedAgent}...`
                    : "Select an agent first"
                }
                disabled={!selectedAgent || sending}
                rows={1}
                className="flex-1 resize-none bg-transparent text-[15px] placeholder:text-muted-foreground/50 outline-none disabled:opacity-50"
                style={{ maxHeight: 200, minHeight: 24 }}
              />
              <Button
                onClick={handleSend}
                disabled={!input.trim() || !selectedAgent || sending}
                size="icon"
                className="h-8 w-8 shrink-0 rounded-lg"
              >
                <Send className="h-4 w-4" />
              </Button>
            </div>
            <p className="text-center text-[11px] text-muted-foreground/50 mt-2">
              Enter to send, Shift+Enter for new line
            </p>
          </div>
        </div>
      </div>
    </div>
  );
}

/** Renders a group of tool calls as a collapsible summary. */
function ToolCallGroup({ msg }: { msg: ChatMessage }) {
  const [groupOpen, setGroupOpen] = useState(false);
  const [expandedTool, setExpandedTool] = useState<Record<string, boolean>>({});

  const tools = msg.toolCalls || [];
  const doneCount = tools.filter((tc) => tc.result != null).length;
  const allDone = doneCount === tools.length;

  const toggleTool = (id: string) =>
    setExpandedTool((prev) => ({ ...prev, [id]: !prev[id] }));

  return (
    <div className="flex justify-start">
      <div className="max-w-[85%] space-y-2">
        {/* Content before tools */}
        {msg.content && (
          <div className="bg-muted rounded-2xl rounded-bl-md px-4 py-2.5">
            <div className="text-[15px] leading-relaxed prose prose-sm dark:prose-invert max-w-none prose-p:my-1">
              <ReactMarkdown remarkPlugins={[remarkGfm]}>
                {msg.content}
              </ReactMarkdown>
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
              <div className="h-3.5 w-3.5 shrink-0 rounded-full border-2 border-amber-500 border-t-transparent animate-spin" />
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
                    <span className="text-muted-foreground/50 font-mono truncate flex-1 text-left text-[11px]">
                      {(() => {
                        try {
                          const args = JSON.parse(tc.arguments);
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
      </div>
    </div>
  );
}

/** File extension → icon + preview kind. */
function fileKind(path: string): { icon: typeof File; preview: "image" | "pdf" | "markdown" | "text" | "none" } {
  const ext = path.toLowerCase().split(".").pop() || "";
  if (["png", "jpg", "jpeg", "gif", "svg", "webp", "bmp", "ico"].includes(ext)) return { icon: ImageIcon, preview: "image" };
  if (ext === "pdf") return { icon: FileText, preview: "pdf" };
  if (ext === "md" || ext === "markdown") return { icon: FileText, preview: "markdown" };
  if (["mp4", "webm", "mov", "mkv"].includes(ext)) return { icon: Film, preview: "none" };
  if (["mp3", "wav", "ogg", "flac", "m4a"].includes(ext)) return { icon: Music, preview: "none" };
  if (["js", "ts", "tsx", "jsx", "py", "go", "rs", "c", "cpp", "h", "java", "rb", "sh", "json", "yaml", "yml", "toml", "xml", "html", "css"].includes(ext))
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

function FilePreview({ agentId, file, onClose }: { agentId: string; file: ProducedFile; onClose: () => void }) {
  const { preview } = fileKind(file.path);
  const src = fileUrl(agentId, file.path, false);
  const downloadUrl = fileUrl(agentId, file.path, true);
  const basename = file.path.split("/").pop() || file.path;
  const [text, setText] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (preview !== "markdown" && preview !== "text") return;
    let cancelled = false;
    fetch(src)
      .then((r) => { if (!r.ok) throw new Error(`HTTP ${r.status}`); return r.text(); })
      .then((t) => { if (!cancelled) setText(t); })
      .catch((e) => { if (!cancelled) setError(String(e)); });
    return () => { cancelled = true; };
  }, [src, preview]);

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
            <a
              href={downloadUrl}
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
                <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>
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
          {preview === "none" && (
            <div className="flex flex-col items-center justify-center h-full gap-3 text-center">
              <File className="h-12 w-12 text-muted-foreground/50" />
              <p className="text-sm text-muted-foreground">Preview not available for this file type.</p>
              <a href={downloadUrl} className="inline-flex items-center gap-2 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90">
                <Download className="h-3.5 w-3.5" /> Download
              </a>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
