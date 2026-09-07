"use client";

import { usePathname } from "next/navigation";
import AgentAccessGate from "@/components/agent-access-gate";
import { ChatScreen } from "@/components/chat-screen";

// AgentLayoutClient owns the single ChatScreen instance for everything
// under /agents/<id>/{chat,project,chats}. Previously each chat route
// segment (chat/, chat/[session], project/[pid]) rendered its own
// <ChatScreen/>, so navigating between sidebar links unmounted and
// remounted the whole chat surface — losing scroll, blanking messages,
// tearing down the SSE, and stacking up subscribe-replay floods that
// starved the browser connection pool. Mounting ChatScreen here keeps
// one instance alive across sidebar nav; ChatScreen reads sessionId /
// projectId out of usePathname() and reacts to URL changes in place.
//
// The page.tsx files under chat/, chat/[session]/, project/[pid]/
// still exist (Next needs them for routing + static export) but each
// returns null — all the visible UI lives in ChatScreen.
//
// Sibling routes that aren't part of the chat surface (customize/,
// models/, channels/, …) render their own page.tsx — for those the
// gate below switches to rendering only `{children}` so ChatScreen
// doesn't sit underneath them.
function isChatRoute(pathname: string, agentId: string): boolean {
  if (!agentId) return false;
  const base = `/agents/${agentId}`;
  if (pathname === base || pathname === `${base}/`) return true;
  // Keep legacy `/chats` URLs inside the persistent chat shell. The current
  // UI expands recent conversations in place instead of navigating there.
  const tail = pathname.slice(base.length);
  return (
    tail === "/chat" ||
    tail.startsWith("/chat/") ||
    tail === "/chats" ||
    tail === "/chats/" ||
    tail === "/project" ||
    tail.startsWith("/project/")
  );
}

export default function AgentLayoutClient({
  children,
}: {
  children: React.ReactNode;
}) {
  const pathname = usePathname() || "";
  // Pull agent id off the URL the same way AgentAccessGate does — the
  // server-side `params` baked at build time always reads "default".
  const m = pathname.match(/^\/agents\/([^/]+)/);
  const agentId = m ? m[1] : "";
  const onChat = isChatRoute(pathname, agentId);
  return (
    <AgentAccessGate>
      {onChat ? (
        <>
          <ChatScreen />
          {/* Page slot still rendered (returns null) so Next's router
              can mount/unmount it on navigation — that's what triggers
              the pathname update ChatScreen reacts to. */}
          {children}
        </>
      ) : (
        children
      )}
    </AgentAccessGate>
  );
}
