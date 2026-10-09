"use client";

import * as React from "react";
import { useRouter, usePathname } from "next/navigation";
import { Bot, Check, ChevronDown, ImagePlus, LoaderCircle, Plus, Search, UsersRound } from "lucide-react";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  useSidebarOptional,
} from "@/components/ui/sidebar";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { BotAvatar } from "@/components/bot-avatar";
import { TeamAvatarStack } from "@/components/team-avatar-stack";
import { NavUser } from "@/components/nav-user";
import { SidebarTitle } from "@/components/sidebar-title";
import { ChatSearchDialog } from "@/components/chat-search-dialog";
import { useLocale, type Locale } from "@/components/locale-provider";
import { agentChatHref, chatHref, rememberChatTarget } from "@/lib/chat-route";
import { apiFetch, createAgent, updateConfig, getTeamInbox, type TeamInboxNotice, type MeResponse, type TeamEntry } from "@/lib/api";
import { rememberAgentAccess } from "@/lib/agent-access-cache";
import { useRunningAgentIds } from "@/lib/chat-runs";

export interface ConsumerAgentItem {
  id: string;
  name: string;
  description?: string;
  preview?: string;
  avatarUrl?: string;
  sessionId?: string;
  updatedAt?: number;
  // A turn is in flight in one of its chats (server-reported).
  running?: boolean;
}

export interface ConsumerTeamItem extends TeamEntry {
  id: string;
  name: string;
  // Latest message in the group's most recent session.
  preview?: string;
  updatedAt?: number;
  running?: boolean;
}

const AGENT_PAGE_SIZE = 20;

// When each row was last seen ("agent:<id>" / "team:<id>" → its updatedAt
// at the time). Per-browser by design: it only drives the unread dot.
const SEEN_STORAGE_KEY = "fastclaw:chat-list-seen:v1";

function readSeen(): Record<string, number> {
  try {
    const parsed = JSON.parse(localStorage.getItem(SEEN_STORAGE_KEY) || "{}");
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch {
    return {};
  }
}

function writeSeen(seen: Record<string, number>) {
  try {
    localStorage.setItem(SEEN_STORAGE_KEY, JSON.stringify(seen));
  } catch {
    // Private mode / blocked storage: unread dots just won't persist.
  }
}
const INLINE_MARKDOWN_TOKEN = /(\*\*[^*\n]+?\*\*|__[^_\n]+?__|~~[^~\n]+?~~|`[^`\n]+?`|\[[^\]\n]+?\]\([^)]+\)|\*[^*\n]+?\*|_[^_\n]+?_)/g;

// Sidebar summaries are deliberately one line, so a full block Markdown
// renderer would introduce invalid nested controls and list/table layout.
// Render the inline subset descriptions and message previews actually use,
// preserving emphasis while keeping links non-interactive inside the row.
function renderInlineMarkdown(text: string, keyPrefix = "md"): React.ReactNode[] {
  const normalized = text.replace(/\s*\n+\s*/g, " ").trim();
  return normalized
    .split(INLINE_MARKDOWN_TOKEN)
    .filter(Boolean)
    .map((part, index) => {
      const key = `${keyPrefix}-${index}`;
      if ((part.startsWith("**") && part.endsWith("**")) || (part.startsWith("__") && part.endsWith("__"))) {
        return <strong key={key}>{renderInlineMarkdown(part.slice(2, -2), key)}</strong>;
      }
      if (part.startsWith("~~") && part.endsWith("~~")) {
        return <del key={key}>{renderInlineMarkdown(part.slice(2, -2), key)}</del>;
      }
      if (part.startsWith("`") && part.endsWith("`")) {
        return <code key={key} className="rounded bg-black/[0.05] px-0.5 font-mono text-[0.92em] dark:bg-white/[0.08]">{part.slice(1, -1)}</code>;
      }
      const link = /^\[([^\]]+)\]\([^)]+\)$/.exec(part);
      if (link) {
        return <span key={key} className="underline decoration-current/35 underline-offset-2">{renderInlineMarkdown(link[1], key)}</span>;
      }
      if ((part.startsWith("*") && part.endsWith("*")) || (part.startsWith("_") && part.endsWith("_"))) {
        return <em key={key}>{renderInlineMarkdown(part.slice(1, -1), key)}</em>;
      }
      return <React.Fragment key={key}>{part}</React.Fragment>;
    });
}

function relativeSessionTime(updatedAt: number | undefined, locale: Locale) {
  if (!updatedAt) return "";
  const date = new Date(updatedAt);
  const now = new Date();
  const sameDay =
    date.getFullYear() === now.getFullYear() &&
    date.getMonth() === now.getMonth() &&
    date.getDate() === now.getDate();
  if (sameDay) {
    return date.toLocaleTimeString(locale === "zh-CN" ? "zh-CN" : "en-US", { hour: "2-digit", minute: "2-digit" });
  }
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (
    date.getFullYear() === yesterday.getFullYear() &&
    date.getMonth() === yesterday.getMonth() &&
    date.getDate() === yesterday.getDate()
  ) {
    return locale === "zh-CN" ? "昨天" : "Yesterday";
  }
  return date.toLocaleDateString(locale, { month: "short", day: "numeric" });
}

// Chat list rows: a small avatar on the name line, preview full width
// underneath, flush with the avatar.
const SIDEBAR_ROW_CLASS =
  "h-auto rounded-lg px-2.5 py-2 data-active:font-normal hover:bg-black/[0.05] dark:hover:bg-white/[0.07]";

export function ConsumerChatSidebar({
  activeAgentId,
  activeTeamId,
  agents,
  teams = [],
  loading = false,
  me,
}: {
  activeAgentId?: string;
  activeTeamId?: string;
  agents: ConsumerAgentItem[];
  teams?: ConsumerTeamItem[];
  loading?: boolean;
  me: MeResponse | null;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const [inbox, setInbox] = React.useState<TeamInboxNotice[]>([]);
  // Runs started in this tab show as working right away; the server's
  // session status (agent.running) covers the rest after the next poll.
  const runningHere = useRunningAgentIds();

  // Unread: a row whose latest activity is newer than when it was last
  // seen. The open row is seen as it updates; a row observed for the
  // first time counts as seen, so a first visit isn't all dots.
  const [seen, setSeen] = React.useState<Record<string, number> | null>(null);
  React.useEffect(() => setSeen(readSeen()), []);
  React.useEffect(() => {
    if (!seen) return;
    let next: Record<string, number> | null = null;
    const mark = (key: string, at: number | undefined, open: boolean) => {
      if (!at) return;
      const current = (next ?? seen)[key];
      if (current === undefined || (open && at > current)) {
        next = { ...(next ?? seen), [key]: at };
      }
    };
    for (const agent of agents) mark(`agent:${agent.id}`, agent.updatedAt, !activeTeamId && activeAgentId === agent.id);
    for (const team of teams) mark(`team:${team.id}`, team.updatedAt, activeTeamId === team.id);
    if (next) {
      writeSeen(next);
      setSeen(next);
    }
  }, [seen, agents, teams, activeAgentId, activeTeamId]);
  const hasUnread = (key: string, at: number | undefined, open: boolean) =>
    !open && !!at && seen?.[key] !== undefined && at > seen[key];
  React.useEffect(() => {
    const uid = me?.user?.id;
    if (!uid) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async () => {
      try {
        const { messages } = await getTeamInbox();
        if (cancelled) return;
        const unread = (messages || []).filter((notice) => {
          const key = `fastclaw:group-inbox-read:${uid}:${notice.agentId}:${notice.sessionId}`;
          if (pathname === chatHref(notice.sessionId) || pathname === `/agents/${encodeURIComponent(notice.agentId)}/chat/${encodeURIComponent(notice.sessionId)}/`) {
            localStorage.setItem(key, String(Math.max(Number(localStorage.getItem(key) || 0), notice.timestamp)));
            return false;
          }
          return notice.timestamp > Number(localStorage.getItem(key) || 0);
        });
        setInbox(unread);
      } catch { /* Retry on the next poll. */ }
      finally { if (!cancelled) timer = setTimeout(refresh, 3000); }
    };
    void refresh();
    return () => { cancelled = true; clearTimeout(timer); };
  }, [me?.user?.id, pathname]);
  const { locale, t, tr } = useLocale();
  const [searchOpen, setSearchOpen] = React.useState(false);
  const [visibleCount, setVisibleCount] = React.useState(AGENT_PAGE_SIZE);
  const [createOpen, setCreateOpen] = React.useState(false);
  const [createTeamOpen, setCreateTeamOpen] = React.useState(false);

  // ⌘K / Ctrl+K opens the search dialog.
  React.useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setSearchOpen(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const filtered = agents;
  const filteredTeams = teams;
  const entries = React.useMemo(
    () => [
      ...filteredTeams.map((team) => ({ kind: "team" as const, id: `team-${team.id}`, updatedAt: team.updatedAt || 0, team })),
      ...agents.map((agent) => ({ kind: "agent" as const, id: agent.id, updatedAt: agent.updatedAt || 0, agent })),
    ].sort((a, b) => b.updatedAt - a.updatedAt),
    [agents, filteredTeams],
  );
  const visibleEntries = React.useMemo(
    () => entries.slice(0, visibleCount),
    [entries, visibleCount],
  );
  const hasMoreAgents = visibleEntries.length < entries.length;
  const unreadAgentIds = React.useMemo(() => new Set(inbox.map((notice) => notice.agentId)), [inbox]);

  // On phones the list is a sheet over the chat; close it on selection.
  const sidebar = useSidebarOptional();
  const openAgent = (agent: ConsumerAgentItem) => {
    sidebar?.setOpenMobile(false);
    const latestPrivate = inbox.filter((notice) => notice.agentId === agent.id).sort((a, b) => b.timestamp - a.timestamp)[0];
    // No conversation yet: open a new session right away, like a group.
    const target = agentChatHref(agent.id, latestPrivate?.sessionId || agent.sessionId);
    // This row came from the caller's authenticated agent list, so the access
    // gate can safely keep the current shell visible during the route swap.
    rememberAgentAccess(agent.id);
    // Dynamic agent ids are served through the static-export fallback. A
    // router navigation can remount that fallback and briefly replace the
    // persistent Bot list with its loading state. Next patches pushState into
    // its reactive router, so this swaps only the active conversation while
    // the left list stays mounted and visually stable.
    window.history.pushState(null, "", target);
  };

  const openTeam = (team: ConsumerTeamItem) => {
    sidebar?.setOpenMobile(false);
    const sessionId = team.sessionId || `team-${team.id}`;
    rememberAgentAccess(team.agents);
    rememberChatTarget(sessionId, { kind: "team", teamId: team.id });
    window.history.pushState(null, "", chatHref(sessionId));
  };

  // Groups and Agents share one list, newest activity first — a group
  // isn't pinned above private chats.
  const renderTeamRow = (team: ConsumerTeamItem) => {
        const members = team.agents
          .map((id) => agents.find((agent) => agent.id === id))
          .filter((agent): agent is ConsumerAgentItem => !!agent);
        const memberLabel = members.map((member) => member.name).join("、");
        const teamUnread = !team.running && hasUnread(`team:${team.id}`, team.updatedAt, activeTeamId === team.id);
        return (
          <SidebarMenuItem key={`team-${team.id}`}>
            <SidebarMenuButton
              isActive={activeTeamId === team.id}
              onClick={() => openTeam(team)}
              tooltip={team.name}
              className={SIDEBAR_ROW_CLASS + " data-active:bg-[#e9e3ec] dark:data-active:bg-[#3a303d]"}
            >
              <span className="grid min-w-0 flex-1 gap-1">
                <span className="flex min-w-0 items-center gap-2">
                  <TeamAvatarStack members={members} avatarUrl={team.avatarUrl} size={20} />
                  <span className="min-w-0 truncate text-[15px] font-semibold leading-5 text-foreground">
                    {team.name}
                  </span>
                  <span className="shrink-0 rounded-full bg-[#e8e1eb] px-1.5 py-0.5 text-[10px] font-semibold leading-none text-[#756878] dark:bg-white/10 dark:text-[#c9bdcc]">
                    {tr("Group", "群聊")}
                  </span>
                  {team.running ? (
                    <WorkingMark className="ml-auto" />
                  ) : (
                    <span className="ml-auto shrink-0 text-[11px] font-normal text-muted-foreground/80">
                      {relativeSessionTime(team.updatedAt, locale)}
                    </span>
                  )}
                </span>
                <span className="flex min-w-0 items-center gap-2">
                  <span className={`min-w-0 flex-1 truncate text-[13px] font-normal leading-5 ${teamUnread ? "text-foreground/80" : "text-muted-foreground"}`}>
                    {team.preview
                      ? renderInlineMarkdown(team.preview)
                      : memberLabel || tr("No Agents", "暂无 Agent")}
                  </span>
                  {teamUnread && <UnreadDot />}
                </span>
              </span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        );
      
  };

  const renderAgentRow = (agent: ConsumerAgentItem) => {
        const active = !activeTeamId && activeAgentId === agent.id;
        // Previews are the raw last reply, so fold multi-bubble split
        // markers back into spaces.
        const summary = (agent.preview || agent.description || t("sidebar.greeting", { name: agent.name }))
          .replace(/\s*<\|split\|>\s*/g, " ");
        const unreadCount = inbox.filter((notice) => notice.agentId === agent.id).length;
        const running = !!agent.running || runningHere.has(agent.id);
        const unread = !running && hasUnread(`agent:${agent.id}`, agent.updatedAt, active);
        return (
          <SidebarMenuItem key={agent.id}>
            <SidebarMenuButton
              isActive={active}
              onClick={() => openAgent(agent)}
              tooltip={agent.name}
              className={SIDEBAR_ROW_CLASS + " data-active:bg-black/[0.075] dark:data-active:bg-white/[0.11]"}
            >
              <span className="grid min-w-0 flex-1 gap-1">
                <span className="flex min-w-0 items-center gap-2">
                  <BotAvatar
                    agentId={agent.id}
                    avatarUrl={agent.avatarUrl}
                    seed={agent.id}
                    size={20}
                  />
                  <span className="min-w-0 flex-1 truncate text-[15px] font-semibold leading-5 text-foreground">
                    {agent.name || t("sidebar.untitledBot")}
                  </span>
                  {running ? (
                    <WorkingMark />
                  ) : (
                    <span className="shrink-0 text-[11px] font-normal text-muted-foreground/80">
                      {relativeSessionTime(agent.updatedAt, locale)}
                    </span>
                  )}
                </span>
                {/* Unread count sits at the end of the preview line, under
                    the time, like a messaging app's chat list. */}
                <span className="flex min-w-0 items-center gap-2">
                  <span className={`min-w-0 flex-1 truncate text-[13px] font-normal leading-5 ${unread ? "text-foreground/80" : "text-muted-foreground"}`}>
                    {renderInlineMarkdown(summary)}
                  </span>
                  {unreadCount > 0 ? (
                    <span
                      aria-label={tr("{{count}} unread private messages", "{{count}} 条未读私信", { count: unreadCount })}
                      className="inline-flex h-[18px] min-w-[18px] shrink-0 items-center justify-center rounded-full bg-red-500 px-1.5 text-[11px] font-medium tabular-nums leading-none text-white"
                    >
                      {unreadCount > 99 ? "99+" : unreadCount}
                    </span>
                  ) : unread && <UnreadDot />}
                </span>
              </span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        );
      
  };

  return (
    <>
      <Sidebar
        collapsible="offcanvas"
        className="border-r border-black/8 bg-[#f7f7f7] dark:border-white/8 dark:bg-[#171717]"
      >
      {/* Same header box as the Console / Admin sidebars (SidebarHeader's
          p-2 + SidebarTitle); search and create sit right of the title.
          56px title + 8px bottom puts the first row at 64px, level with the
          AppRail's first area button (8 + 40 logo + 12 + 4 gap). */}
      <SidebarHeader className="pt-0 pb-2">
        <SidebarTitle
          title={tr("Chat", "对话")}
          className="group-data-[collapsible=icon]:justify-center"
        >
          <button
            type="button"
            onClick={() => setSearchOpen(true)}
            className="ml-auto inline-flex size-8 shrink-0 items-center justify-center rounded-lg text-muted-foreground transition hover:bg-black/5 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring group-data-[collapsible=icon]:ml-0 group-data-[collapsible=icon]:size-10 dark:hover:bg-white/8"
            aria-label={t("sidebar.searchBots")}
            title={`${tr("Search", "搜索")} (${/Mac|iPhone|iPad/.test(typeof navigator === "undefined" ? "" : navigator.platform) ? "⌘K" : "Ctrl+K"})`}
          >
            <Search className="size-4" />
          </button>
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <button
                  type="button"
                  className="inline-flex size-8 shrink-0 items-center justify-center rounded-lg text-muted-foreground transition hover:bg-black/5 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring group-data-[collapsible=icon]:hidden dark:hover:bg-white/8"
                  aria-label={tr("Create", "新建")}
                  title={tr("Create", "新建")}
                >
                  <Plus className="size-4" />
                </button>
              }
            />
            <DropdownMenuContent align="end" sideOffset={7} className="w-52 rounded-xl p-1.5">
              <DropdownMenuItem onClick={() => setCreateOpen(true)} className="h-10 gap-2 rounded-lg px-2.5">
                <Bot className="size-4" />
                {t("sidebar.createBot")}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setCreateTeamOpen(true)} className="h-10 gap-2 rounded-lg px-2.5">
                <UsersRound className="size-4" />
                {tr("Create group chat", "创建群聊")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </SidebarTitle>
      </SidebarHeader>

      <SidebarContent
        aria-busy={loading}
        className="px-3 pb-3 group-data-[collapsible=icon]:overflow-x-hidden! group-data-[collapsible=icon]:overflow-y-auto! group-data-[collapsible=icon]:px-2"
      >
        <SidebarMenu className="gap-1 group-data-[collapsible=icon]:gap-2">
          {loading && filtered.length === 0 && (
            <>
              {[0, 1, 2].map((item) => (
                <SidebarMenuItem key={`agent-loading-${item}`}>
                  <div className="space-y-2 rounded-lg px-2.5 py-2.5">
                    <div className="flex items-center gap-2">
                      <div className="size-5 shrink-0 animate-pulse rounded-full bg-black/[0.075] motion-reduce:animate-none dark:bg-white/[0.1]" />
                      <div className="h-3 w-2/5 animate-pulse rounded-full bg-black/[0.08] motion-reduce:animate-none dark:bg-white/[0.11]" />
                    </div>
                    <div className="h-2.5 w-3/5 animate-pulse rounded-full bg-black/[0.055] motion-reduce:animate-none dark:bg-white/[0.075]" />
                  </div>
                </SidebarMenuItem>
              ))}
              <span className="sr-only" role="status">
                {tr("Loading Agents…", "正在加载 Agent…")}
              </span>
            </>
          )}
          {visibleEntries.map((entry) => entry.kind === "team" ? renderTeamRow(entry.team) : renderAgentRow(entry.agent))}
          {hasMoreAgents && (
            <SidebarMenuItem>
              <SidebarMenuButton
                onClick={() => setVisibleCount((count) => count + AGENT_PAGE_SIZE)}
                tooltip={t("sidebar.loadMore")}
                className="h-9 justify-center gap-1.5 rounded-lg text-xs font-medium text-muted-foreground hover:bg-black/[0.05] hover:text-foreground group-data-[collapsible=icon]:size-10! group-data-[collapsible=icon]:rounded-xl group-data-[collapsible=icon]:p-0! dark:hover:bg-white/[0.07]"
              >
                <ChevronDown className="size-3.5 shrink-0" />
                <span className="group-data-[collapsible=icon]:hidden">
                  {t("sidebar.loadMore")}
                </span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          )}
        </SidebarMenu>

        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <button
                type="button"
                className="mx-auto mt-2 hidden size-9 shrink-0 items-center justify-center rounded-lg text-muted-foreground transition hover:bg-black/5 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring group-data-[collapsible=icon]:flex dark:hover:bg-white/8"
                aria-label={tr("Create", "新建")}
                title={tr("Create", "新建")}
              >
                <Plus className="size-4" />
              </button>
            }
          />
          <DropdownMenuContent side="right" align="start" sideOffset={8} className="w-48 rounded-xl p-1.5">
            <DropdownMenuItem onClick={() => setCreateOpen(true)} className="h-9 gap-2 rounded-lg px-2.5">
              <Bot className="size-4" />
              {t("sidebar.createBot")}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => setCreateTeamOpen(true)} className="h-9 gap-2 rounded-lg px-2.5">
              <UsersRound className="size-4" />
              {tr("Create group chat", "创建群聊")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        {!loading && filtered.length === 0 && filteredTeams.length === 0 && (
          <div className="mx-2 mt-4 rounded-lg border border-dashed border-black/10 px-4 py-8 text-center group-data-[collapsible=icon]:hidden dark:border-white/10">
            <p className="text-sm font-medium">
              {t("sidebar.noBots")}
            </p>
            <button
              type="button"
              onClick={() => router.push("/console/agents/")}
              className="mt-2 text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
            >
              {t("sidebar.manageBots")}
            </button>
          </div>
        )}
      </SidebarContent>

      {/* The account lives in the AppRail on desktop; the rail is hidden
          on mobile, so the sheet keeps it here. */}
      <SidebarFooter className="px-2 pb-3 pt-2 group-data-[collapsible=icon]:px-4 md:hidden">
        <NavUser
          name={me?.user?.displayName || me?.user?.username || tr("User", "用户")}
          subtitle={me?.user?.role || tr("user", "用户")}
        />
      </SidebarFooter>
      {/* Resize-only handle: nothing to drag once the list is collapsed,
          and offcanvas would leave it peeking out past the app rail. */}
      <SidebarRail toggleOnClick={false} className="group-data-[collapsible=offcanvas]:hidden!" />
      </Sidebar>
      <ChatSearchDialog
        open={searchOpen}
        onOpenChange={setSearchOpen}
        agents={agents}
        teams={teams}
        unreadAgentIds={unreadAgentIds}
        onPickAgent={openAgent}
        onPickTeam={openTeam}
      />
      <CreateBotDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={(agentId) => {
          router.push(agentChatHref(agentId));
        }}
      />
      <CreateTeamDialog
        open={createTeamOpen}
        onOpenChange={setCreateTeamOpen}
        agents={agents}
        onCreated={(team) => {
          window.dispatchEvent(new CustomEvent("fastclaw:teams-changed"));
          openTeam(team);
        }}
      />
    </>
  );
}

function CreateTeamDialog({
  open,
  onOpenChange,
  agents,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  agents: ConsumerAgentItem[];
  onCreated: (team: ConsumerTeamItem) => void;
}) {
  const { tr } = useLocale();
  const [name, setName] = React.useState("");
  const [query, setQuery] = React.useState("");
  const [selected, setSelected] = React.useState<string[]>([]);
  const [saving, setSaving] = React.useState(false);
  const [error, setError] = React.useState("");

  const reset = () => {
    setName("");
    setQuery("");
    setSelected([]);
    setSaving(false);
    setError("");
  };
  const close = () => {
    onOpenChange(false);
    reset();
  };
  const visibleAgents = agents.filter((agent) =>
    `${agent.name} ${agent.description || ""}`.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()),
  );

  const createTeam = async () => {
    const teamName = name.trim();
    if (!teamName || selected.length < 2 || saving) return;
    setSaving(true);
    setError("");
    const id = `tm-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
    const sessionId = `tc-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
    const entry: TeamEntry = {
      name: teamName,
      agents: selected,
      defaultAgent: selected[0],
      sessionId,
      groupBehavior: "coordinated",
      createdAt: Date.now(),
    };
    try {
      const response = await updateConfig({ teams: { [id]: entry } }, "user");
      if (!response?.ok) {
        setError(response?.error || tr("Failed to create group chat", "创建群聊失败"));
        return;
      }
      close();
      onCreated({ id, ...entry, name: teamName });
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : tr("Failed to create group chat", "创建群聊失败"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (nextOpen) onOpenChange(true);
        else close();
      }}
    >
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{tr("Create group chat", "创建群聊")}</DialogTitle>
          <DialogDescription>
            {tr(
              "Choose at least two Agents. Mention one by name, or use @all when everyone should answer.",
              "选择至少两个 Agent。对话中可以 @某个 Agent，或用 @all 让所有成员回复。",
            )}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-1">
          <div className="space-y-2">
            <Label htmlFor="team-name">{tr("Group name", "群聊名称")}</Label>
            <Input
              id="team-name"
              value={name}
              onChange={(event) => {
                setName(event.target.value);
                setError("");
              }}
              placeholder={tr("Launch crew", "项目讨论组")}
              autoFocus
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="team-agent-search">{tr("Members", "群成员")}</Label>
            <div className="relative">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                id="team-agent-search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder={tr("Search Agents", "搜索 Agent")}
                className="pl-9"
              />
            </div>
            <div className="max-h-64 space-y-1 overflow-y-auto rounded-xl border p-1.5">
              {visibleAgents.map((agent) => {
                const checked = selected.includes(agent.id);
                return (
                  <button
                    key={agent.id}
                    type="button"
                    onClick={() => {
                      setSelected((current) => checked
                        ? current.filter((id) => id !== agent.id)
                        : [...current, agent.id]);
                      setError("");
                    }}
                    className={`flex w-full items-center gap-3 rounded-lg px-2.5 py-2 text-left transition ${
                      checked ? "bg-[#eee9f0] dark:bg-[#342d36]" : "hover:bg-muted/70"
                    }`}
                  >
                    <BotAvatar agentId={agent.id} avatarUrl={agent.avatarUrl} seed={agent.id} size={34} />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm font-semibold">{agent.name}</span>
                      {agent.description && (
                        <span className="block truncate text-xs text-muted-foreground">{renderInlineMarkdown(agent.description, `team-${agent.id}`)}</span>
                      )}
                    </span>
                    <span className={`flex size-5 items-center justify-center rounded-full border transition ${
                      checked
                        ? "border-[#79677f] bg-[#79677f] text-white"
                        : "border-border text-transparent"
                    }`}>
                      <Check className="size-3" />
                    </span>
                  </button>
                );
              })}
              {visibleAgents.length === 0 && (
                <p className="px-3 py-8 text-center text-sm text-muted-foreground">
                  {tr("No matching Agents", "没有匹配的 Agent")}
                </p>
              )}
            </div>
            <p className="text-xs text-muted-foreground">
              {tr("{{count}} selected", "已选择 {{count}} 个", { count: selected.length })}
            </p>
          </div>
          {agents.length < 2 && (
            <p className="text-sm text-amber-700 dark:text-amber-300">
              {tr("Create at least two Agents before starting a group chat.", "至少创建两个 Agent 后才能发起群聊。")}
            </p>
          )}
          {error && <p className="text-sm text-destructive">{error}</p>}
        </div>

        <DialogFooter>
          <Button type="button" variant="outline" onClick={close}>
            {tr("Cancel", "取消")}
          </Button>
          <Button
            type="button"
            onClick={() => void createTeam()}
            disabled={!name.trim() || selected.length < 2 || saving}
          >
            {saving ? tr("Creating…", "正在创建…") : tr("Create group", "创建群聊")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function CreateBotDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: (agentId: string) => void;
}) {
  const { t } = useLocale();
  const [name, setName] = React.useState("");
  const [description, setDescription] = React.useState("");
  const [avatar, setAvatar] = React.useState<File | null>(null);
  const [avatarPreview, setAvatarPreview] = React.useState<string | null>(null);
  const [error, setError] = React.useState("");
  const [saving, setSaving] = React.useState(false);
  const avatarInput = React.useRef<HTMLInputElement>(null);

  const reset = () => {
    setName("");
    setDescription("");
    setAvatar(null);
    if (avatarPreview) URL.revokeObjectURL(avatarPreview);
    setAvatarPreview(null);
    setError("");
    setSaving(false);
  };

  const close = () => {
    onOpenChange(false);
    reset();
  };

  const handleCreate = async () => {
    const trimmedName = name.trim();
    if (!trimmedName || saving) return;
    setSaving(true);
    setError("");

    try {
      const response = await createAgent({
        name: trimmedName,
        description: description.trim() || undefined,
      });
      if (!response || response.ok === false || response.error) {
        setError(response?.error || t("createBot.failed"));
        return;
      }

      const agentId = response.agent?.id as string | undefined;
      if (!agentId) {
        setError(t("createBot.missingId"));
        return;
      }

      if (avatar) {
        const form = new FormData();
        form.append("file", avatar, "avatar.png");
        await apiFetch(`/api/agents/${encodeURIComponent(agentId)}/files`, {
          method: "POST",
          body: form,
        }).catch(() => null);
      }

      close();
      onCreated(agentId);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("createBot.failed"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (nextOpen) onOpenChange(true);
        else close();
      }}
    >
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("createBot.title")}</DialogTitle>
          <DialogDescription>
            {t("createBot.description")}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div className="flex items-start gap-4">
            <button
              type="button"
              onClick={() => avatarInput.current?.click()}
              className="group relative flex size-20 shrink-0 items-center justify-center overflow-hidden rounded-2xl border border-dashed bg-muted/40 transition hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring"
              aria-label={t("createBot.uploadAvatar")}
            >
              {avatarPreview ? (
                // eslint-disable-next-line @next/next/no-img-element
                <img src={avatarPreview} alt={t("createBot.avatarAlt")} className="size-full object-cover" />
              ) : (
                <ImagePlus className="size-6 text-muted-foreground" />
              )}
              <input
                ref={avatarInput}
                type="file"
                accept="image/*"
                className="hidden"
                onChange={(event) => {
                  const file = event.target.files?.[0] || null;
                  setAvatar(file);
                  if (avatarPreview) URL.revokeObjectURL(avatarPreview);
                  setAvatarPreview(file ? URL.createObjectURL(file) : null);
                }}
              />
            </button>
            <div className="min-w-0 flex-1 space-y-2">
              <Label htmlFor="new-bot-name">{t("createBot.name")}</Label>
              <Input
                id="new-bot-name"
                value={name}
                onChange={(event) => {
                  setName(event.target.value);
                  setError("");
                }}
                placeholder={t("createBot.namePlaceholder")}
                autoFocus
                onKeyDown={(event) => {
                  if (event.key === "Enter" && !event.nativeEvent.isComposing) {
                    event.preventDefault();
                    void handleCreate();
                  }
                }}
              />
            </div>
          </div>

          <div className="space-y-2">
            <Label htmlFor="new-bot-description">{t("createBot.descriptionLabel")}</Label>
            <Textarea
              id="new-bot-description"
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              placeholder={t("createBot.descriptionPlaceholder")}
              rows={3}
            />
          </div>

          {error && <p className="text-sm text-destructive">{error}</p>}
        </div>

        <DialogFooter>
          <Button type="button" variant="outline" onClick={close}>
            {t("common.cancel")}
          </Button>
          <Button type="button" onClick={() => void handleCreate()} disabled={!name.trim() || saving}>
            {saving ? t("createBot.creating") : t("createBot.title")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// Replaces a chat row's time while one of its chats is mid-turn.
function WorkingMark({ className = "" }: { className?: string }) {
  const { tr } = useLocale();
  return (
    <span role="status" aria-label={tr("Working…", "处理中…")} className={`inline-flex shrink-0 ${className}`}>
      <LoaderCircle className="size-3.5 animate-spin text-muted-foreground motion-reduce:animate-none" />
    </span>
  );
}

// New activity since the row was last opened.
function UnreadDot() {
  const { tr } = useLocale();
  return (
    <span
      role="status"
      aria-label={tr("New messages", "有新消息")}
      className="size-2 shrink-0 rounded-full bg-red-500"
    />
  );
}
