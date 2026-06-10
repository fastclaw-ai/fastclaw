"use client";

import * as React from "react";
import { usePathname, useSearchParams } from "next/navigation";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
} from "@/components/ui/sidebar";
import { AgentSwitcher, AgentSwitcherItem } from "@/components/team-switcher";
import { NavMain, NavItem } from "@/components/nav-main";
import { NavSessions, SessionItem } from "@/components/nav-projects";
import { NavProjectsList } from "@/components/nav-projects-list";
import { NavUser } from "@/components/nav-user";
import { AgentSettingsDialog } from "@/components/agent-settings-dialog";
import {
  BotIcon,
  BrainIcon,
  CoinsIcon,
  KeyRoundIcon,
  LayoutDashboardIcon,
  MessagesSquareIcon,
  PlusIcon,
  ServerIcon,
  SettingsIcon,
  SparklesIcon,
  UsersIcon,
  WrenchIcon,
} from "lucide-react";
import {
  getAgent,
  getAgents,
  getChatSessions,
  getMe,
  getStatus,
  listProjects,
  type MeResponse,
  type ProjectEntry,
  type StatusResponse,
} from "@/lib/api";

// Extract agent ID from pathname like /agents/default/chat/. The second
// capture is an explicit allow-list of sub-routes so the bare /agents/
// index keeps the Platform nav instead of flipping to Agent nav.
// Add new agent-scoped routes here when they ship — `project` was
// missed when the project chat route was introduced and that left
// the sidebar showing the platform nav for /agents/<id>/project/...
function extractAgentId(pathname: string): string | null {
  const match = pathname.match(
    /^\/agents\/([^/]+)\/(chat|customize|skills|models|sessions|channels|mcp|chats|scheduler|project)/,
  );
  return match ? match[1] : null;
}

// Sidebar nav is rendered as a series of labeled sections so users can
// scan it by domain instead of one flat list:
//
//   (no label)  Overview                              — landing dashboard
//   Agent       Agents · Models · Skills · Tools      — agent-building surfaces
//   User        Users · Chats · Token Usage · API Keys — admin platform tools
//   (no label)  Settings                              — opens the user dialog
//
// Skills / Tools and the Users/Chats/Token-Usage admin entries are
// admin-only. Non-admin sees the Agent group with just Agents + Models,
// and a slim User group with API Keys. Settings is a click-only item —
// its onClick is attached at render time so it can call into component
// state.
const OVERVIEW_ITEM: NavItem = {
  title: "Overview",
  url: "/overview/",
  icon: LayoutDashboardIcon,
};

const USER_AGENT_GROUP: NavItem[] = [
  { title: "Agents", url: "/agents/", icon: BotIcon },
  { title: "Models", url: "/models/", icon: BrainIcon },
];

const ADMIN_AGENT_GROUP: NavItem[] = [
  { title: "Agents", url: "/agents/", icon: BotIcon },
  { title: "Models", url: "/models/", icon: BrainIcon },
  { title: "Skills", url: "/skills/", icon: SparklesIcon },
  { title: "Tools", url: "/tools/", icon: WrenchIcon },
];

const USER_USER_GROUP: NavItem[] = [
  { title: "API Keys", url: "/apikeys/", icon: KeyRoundIcon },
];

const ADMIN_USER_GROUP: NavItem[] = [
  { title: "Users", url: "/admin/users/", icon: UsersIcon },
  { title: "Chats", url: "/admin/chats/", icon: MessagesSquareIcon },
  { title: "MCP", url: "/admin/mcp/", icon: ServerIcon },
  { title: "Token Usage", url: "/admin/usage/", icon: CoinsIcon },
  { title: "API Keys", url: "/apikeys/", icon: KeyRoundIcon },
];

// "New chat" is active iff we're parked on the bare /chat/ page with
// no session open. A session can be encoded two ways:
//   - `?session=<id>` query param on `/chat/`
//   - path segment: `/chat/<sessionId>/`
// Either form means a specific session is open, so the New chat entry
// must NOT light up. We check the exact pathname (rather than
// startsWith) so the path-segment form falls through.
//
// Configuration tabs (Customize / Models / Skills / Channels /
// Scheduler) live in the footer Settings dialog — for owners only —
// so the sidebar nav itself just exposes "New chat" regardless of role.
const AGENT_NAV = (
  agentId: string,
  pathname: string,
  hasSession: boolean,
): NavItem[] => {
  const base = `/agents/${agentId}/chat`;
  const onNewChatRoute = pathname === base || pathname === `${base}/`;
  return [
    {
      title: "New chat",
      url: `${base}/`,
      icon: PlusIcon,
      active: onNewChatRoute && !hasSession,
    },
  ];
};

export function AppSidebar(props: React.ComponentProps<typeof Sidebar>) {
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const activeAgentId = extractAgentId(pathname);
  const hasOpenSession = !!searchParams?.get("session");

  const [status, setStatus] = React.useState<StatusResponse | null>(null);
  const [me, setMe] = React.useState<MeResponse | null>(null);
  const [agents, setAgents] = React.useState<AgentSwitcherItem[]>([]);
  // role flag per agent the caller can see — owner vs viewer (read-only
  // shared from another user). Drives whether the AGENT_NAV exposes
  // configuration tabs.
  const [agentRoles, setAgentRoles] = React.useState<Record<string, "owner" | "viewer">>({});
  const [sessions, setSessions] = React.useState<SessionItem[]>([]);
  const [projects, setProjects] = React.useState<ProjectEntry[]>([]);
  // Single dialog state covers both entry points: the agent-scoped
  // footer button (full Agent + User tabs) and the platform-nav
  // Settings entry (User tabs only). `settingsUserOnly` picks the mode.
  const [settingsOpen, setSettingsOpen] = React.useState(false);
  const [settingsUserOnly, setSettingsUserOnly] = React.useState(false);

  // Keep status polling so the online dot / admin flag stay fresh.
  React.useEffect(() => {
    getStatus().then(setStatus).catch(() => {});
    const iv = setInterval(() => {
      getStatus().then(setStatus).catch(() => {});
    }, 15000);
    return () => clearInterval(iv);
  }, []);

  // Fetch current user once so the footer can show their name + role.
  React.useEffect(() => {
    getMe().then(setMe).catch(() => {});
  }, []);

  // Agent list drives the switcher dropdown at the top of the sidebar.
  React.useEffect(() => {
    getAgents()
      .then((list) => {
        setAgents(list.map((a) => ({ id: a.id, name: a.name, model: a.model })));
        const roles: Record<string, "owner" | "viewer"> = {};
        for (const a of list) {
          roles[a.id] = a.role === "viewer" ? "viewer" : "owner";
        }
        setAgentRoles(roles);
      })
      .catch(() => {});
  }, []);

  // When the active agent isn't in the caller's owned list — e.g. a
  // super_admin chatting with another user's agent — fetch its name
  // separately and splice it in so the switcher header shows the real
  // name instead of falling back to "FastClaw". The single-agent
  // endpoint also returns role, so capture it here too.
  React.useEffect(() => {
    if (!activeAgentId) return;
    if (agents.some((a) => a.id === activeAgentId)) return;
    let aborted = false;
    getAgent(activeAgentId)
      .then((a) => {
        if (aborted || !a) return;
        setAgents((prev) =>
          prev.some((x) => x.id === a.id)
            ? prev
            : [...prev, { id: a.id, name: a.name, model: a.model }],
        );
        if (a.role === "viewer" || a.role === "owner") {
          setAgentRoles((prev) => ({ ...prev, [a.id]: a.role as "owner" | "viewer" }));
        }
      })
      .catch(() => {});
    return () => {
      aborted = true;
    };
  }, [activeAgentId, agents]);

  // Sessions + projects only matter while a specific agent is selected.
  // We re-run both whenever the active agent changes *or* the chat page
  // broadcasts a `fastclaw:sessions-changed` event (e.g. after rename /
  // new chat / project create) so the sidebar stays in sync without a
  // page refresh. Projects are bundled with sessions because creating a
  // chat in a project also affects which sessions appear under it.
  React.useEffect(() => {
    if (!activeAgentId) {
      setSessions([]);
      setProjects([]);
      return;
    }
    const refetch = () => {
      getChatSessions(activeAgentId)
        .then((list) =>
          setSessions(
            list.map((s) => ({
              id: s.id,
              title: s.title || s.preview || s.id,
              thumbnailUrl: s.thumbnailUrl,
              channel: s.channel,
              projectId: s.projectId,
            })),
          ),
        )
        .catch(() => {});
      listProjects(activeAgentId)
        .then(setProjects)
        .catch(() => {});
    };
    refetch();
    const onChange = (e: Event) => {
      const detail = (e as CustomEvent<{ agentId?: string }>).detail;
      if (!detail || !detail.agentId || detail.agentId === activeAgentId) {
        refetch();
      }
    };
    window.addEventListener("fastclaw:sessions-changed", onChange);
    return () => {
      window.removeEventListener("fastclaw:sessions-changed", onChange);
    };
  }, [activeAgentId]);

  // broadcastSessionsChanged fires the same custom event NavSessions
  // listens to, so a project mutation refreshes both the projects list
  // AND the sessions list (a new chat-in-project shows up under its
  // project, and the project's session count drives the delete-block).
  const broadcastSessionsChanged = React.useCallback(() => {
    if (typeof window !== "undefined" && activeAgentId) {
      window.dispatchEvent(
        new CustomEvent("fastclaw:sessions-changed", {
          detail: { agentId: activeAgentId },
        }),
      );
    }
  }, [activeAgentId]);

  const isAdmin = status?.isAdmin ?? false;
  // quotaLocked = caller has agent_quota=0 (admin-provisions-only,
  // typical single-agent customer model). The agent switcher header
  // is locked (static label, no "Manage agents" dropdown), but the
  // /agents page itself stays reachable so they can browse what's
  // been provisioned and jump into chat — it just hides the Create
  // button. So we keep the Agents nav entry visible.
  const quotaLocked = me?.user?.agentQuota === 0;

  return (
    <Sidebar collapsible="icon" {...props}>
      <SidebarHeader>
        <AgentSwitcher
          agents={agents}
          activeAgentId={activeAgentId}
          locked={
            quotaLocked ||
            (!!activeAgentId && agentRoles[activeAgentId] === "viewer")
          }
        />
      </SidebarHeader>
      <SidebarContent>
        {activeAgentId ? (
          <NavMain
            label="Agent"
            items={[
              ...AGENT_NAV(activeAgentId, pathname, hasOpenSession),
              // Settings sits directly under New chat on agent routes
              // (moved out of the footer) so the agent's own config is
              // the first thing below the chat entry. Click-only: opens
              // the dialog with the full agent tabs (userOnly=false).
              {
                title: "Settings",
                icon: SettingsIcon,
                onClick: () => {
                  setSettingsUserOnly(false);
                  setSettingsOpen(true);
                },
              },
            ]}
          />
        ) : (
          <>
            <NavMain items={[OVERVIEW_ITEM]} />
            <NavMain
              label="Agent"
              items={isAdmin ? ADMIN_AGENT_GROUP : USER_AGENT_GROUP}
            />
            <NavMain
              label="User"
              items={isAdmin ? ADMIN_USER_GROUP : USER_USER_GROUP}
            />
          </>
        )}
        {/* Projects are per-(user, agent), so viewers on a shared agent
            see/create their OWN projects — the owner's projects stay
            private. The owner-only Settings dialog below is unaffected:
            project CRUD is read-write for whoever opened the agent, but
            agent configuration (skills, channels, models) stays the
            owner's. */}
        {activeAgentId && (
          <NavProjectsList
            agentId={activeAgentId}
            projects={projects}
            sessions={sessions}
            onChanged={broadcastSessionsChanged}
          />
        )}
        <NavSessions agentId={activeAgentId} sessions={sessions} />
      </SidebarContent>
      <SidebarFooter>
        {/* On agent routes Settings now lives under New chat (above), so
            the footer entry only shows on platform routes — there it
            opens in user-only mode (Account / General). */}
        {!activeAgentId && (
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton
                tooltip="Settings"
                onClick={() => {
                  setSettingsUserOnly(true);
                  setSettingsOpen(true);
                }}
              >
                <SettingsIcon />
                <span>Settings</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        )}
        <NavUser
          name={
            me?.user?.displayName ||
            me?.user?.username ||
            (isAdmin ? "Admin" : "User")
          }
          subtitle={me?.user?.role || (isAdmin ? "super_admin" : "user")}
        />
      </SidebarFooter>
      <SidebarRail />
      <AgentSettingsDialog
        open={settingsOpen}
        onOpenChange={setSettingsOpen}
        userOnly={settingsUserOnly}
        role={
          activeAgentId && agentRoles[activeAgentId] === "viewer"
            ? "viewer"
            : "owner"
        }
        isAdmin={isAdmin}
      />
    </Sidebar>
  );
}
