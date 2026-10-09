"use client";

import * as React from "react";
import { usePathname } from "next/navigation";
import { SidebarLayout } from "@/components/sidebar";
import AgentAccessGate from "@/components/agent-access-gate";
import { ChatScreen } from "@/components/chat-screen";
import { TeamChatScreen } from "@/components/team-chat-screen";
import { LegacyTeamChatRedirect } from "@/components/legacy-team-chat-redirect";
import {
  ChatRouteContext,
  agentChatHref,
  chatHref,
  chatSessionFromPath,
  knownChatTarget,
  rememberChatTarget,
  resolveChatTarget,
  type ChatRouteState,
} from "@/lib/chat-route";
import { useLocale } from "@/components/locale-provider";

// Paths that render on their own (no sidebar chrome). /signup is in
// here because hitting it directly while signed out (e.g. from an admin
// invite link) was leaking the authenticated app chrome — Overview /
// Agents / Models in the sidebar — to a not-yet-registered visitor.
const BARE_PATHS = ["/", "/onboard", "/signup"];

function wantsSidebar(pathname: string) {
  if (BARE_PATHS.includes(pathname)) return false;
  if (pathname.startsWith("/onboard/")) return false;
  if (pathname.startsWith("/signup/")) return false;
  // The tab a connector authorization runs in (open → platform → done).
  if (pathname.startsWith("/connectors/")) return false;
  return true;
}

// AppShell mounts SidebarLayout once for every authenticated page and keeps
// that instance alive across client-side navigations. Previously each route
// segment had its own layout.tsx that re-wrapped SidebarLayout, so Next
// unmounted and remounted the sidebar on every top-level nav — triggering a
// fresh status / agents / sessions fetch and a visible flash. One shell at
// the root means the sidebar (and its effects) persists across navigations.
export function AppShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const chatRoute = useChatRouteState(pathname);
  if (!wantsSidebar(pathname)) {
    return <>{children}</>;
  }
  // Native history navigation updates the pathname without replacing Next's
  // static-export route tree. Choose the conversation here so crossing from a
  // group to an Agent cannot leave the previous route's screen mounted.
  let content = children;
  if (chatRoute.status === "ready") {
    // Same element tree as the /agents/… and /teams/… branches below, so
    // the first send's URL change (/agents/<a>/chat/ → /chat/<sid>/) keeps
    // the conversation mounted and its reply stream alive.
    content = chatRoute.target.kind === "team"
      ? <TeamChatScreen />
      : <AgentAccessGate><ChatScreen /></AgentAccessGate>;
  } else if (chatRoute.status !== "none") {
    content = <ChatSessionPending missing={chatRoute.status === "missing"} />;
  } else if (legacyChatHref(pathname)) {
    content = <LegacyChatRedirect />;
  } else if (/^\/agents\/[^/]+\/team\/[^/]+\/?$/.test(pathname)) {
    content = <LegacyTeamChatRedirect />;
  } else if (/^\/agents\/[^/]+(?:\/(?:chats|project(?:\/[^/]+)?))?\/?$/.test(pathname)) {
    content = <AgentAccessGate><ChatScreen /></AgentAccessGate>;
  }
  // The sidebar sits outside the page, so the /chat/<sessionId> resolution
  // is provided around both: it marks the open agent or group too.
  return (
    <ChatRouteContext.Provider value={chatRoute}>
      <SidebarLayout>{content}</SidebarLayout>
    </ChatRouteContext.Provider>
  );
}

// useChatRouteState resolves a /chat/<sessionId> URL to its agent or group.
// Results are keyed by session id, so a stale result never shows under a
// different session.
function useChatRouteState(pathname: string): ChatRouteState {
  const sessionId = chatSessionFromPath(pathname);
  const [resolved, setResolved] = React.useState<{
    sessionId: string;
    target: Awaited<ReturnType<typeof resolveChatTarget>>;
  } | null>(null);
  React.useEffect(() => {
    if (!sessionId) return;
    let cancelled = false;
    resolveChatTarget(sessionId)
      .catch(() => null)
      .then((target) => {
        if (!cancelled) setResolved({ sessionId, target });
      });
    return () => {
      cancelled = true;
    };
  }, [sessionId]);
  if (!sessionId) return { sessionId: "", status: "none" };
  // Targets this tab already knows (it just created the session, or
  // resolved it before) apply on the first render — no loading frame.
  const known = knownChatTarget(sessionId);
  if (known) return { sessionId, status: "ready", target: known };
  if (resolved?.sessionId !== sessionId) return { sessionId, status: "loading" };
  return resolved.target
    ? { sessionId, status: "ready", target: resolved.target }
    : { sessionId, status: "missing" };
}

// legacyChatHref maps the old per-Agent / per-group chat URLs onto
// /chat/<sessionId>/: an Agent with no session in the URL opens a new chat,
// the same way a new group session does. Returns "" for other paths.
function legacyChatHref(pathname: string): string {
  const decode = (value: string) => {
    try {
      return decodeURIComponent(value);
    } catch {
      return value;
    }
  };
  const agentChat = pathname.match(/^\/agents\/([^/]+)(?:\/chat(?:\/([^/]+))?)?\/?$/);
  if (agentChat && agentChat[1] !== "_") {
    const agentId = decode(agentChat[1]);
    const sessionId = agentChat[2] && agentChat[2] !== "_" ? decode(agentChat[2]) : "";
    return sessionId ? agentChatHref(agentId, sessionId) : "new";
  }
  const teamChat = pathname.match(/^\/teams\/([^/]+)\/chat\/([^/]+)\/?$/);
  if (teamChat && teamChat[2] !== "_") {
    const sessionId = decode(teamChat[2]);
    rememberChatTarget(sessionId, { kind: "team", teamId: decode(teamChat[1]) });
    return chatHref(sessionId);
  }
  return "";
}

// LegacyChatRedirect swaps an old chat URL for its /chat/<sessionId>/ form
// in place (history.replaceState keeps the shell mounted; Next follows it),
// carrying the query along — ?actAs= read-only views included.
function LegacyChatRedirect() {
  const pathname = usePathname();
  React.useEffect(() => {
    let href = legacyChatHref(pathname);
    if (!href) return;
    if (href === "new") {
      const agentId = decodeURIComponent(pathname.split("/")[2] || "");
      href = agentChatHref(agentId);
    }
    window.history.replaceState(null, "", href + window.location.search + window.location.hash);
  }, [pathname]);
  return (
    <div className="flex h-[calc(100vh-3.5rem)] items-center justify-center">
      <div className="h-6 w-6 animate-spin rounded-full border-2 border-muted border-t-primary" />
    </div>
  );
}

function ChatSessionPending({ missing }: { missing: boolean }) {
  const { tr } = useLocale();
  if (missing) {
    return (
      <div className="flex h-[calc(100vh-3.5rem)] flex-col items-center justify-center gap-2 px-6 text-center">
        <p className="text-sm font-medium">{tr("Session not found", "找不到这个会话")}</p>
        <p className="text-sm text-muted-foreground">
          {tr("It may have been deleted, or it belongs to another account.", "它可能已被删除，或属于其他账号。")}
        </p>
      </div>
    );
  }
  return (
    <div className="flex h-[calc(100vh-3.5rem)] items-center justify-center">
      <div className="h-6 w-6 animate-spin rounded-full border-2 border-muted border-t-primary" />
    </div>
  );
}
