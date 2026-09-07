"use client";

import * as React from "react";
import { usePathname, useRouter } from "next/navigation";
import {
  SidebarGroup,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from "@/components/ui/sidebar";
import { ChevronRightIcon, MoreHorizontal } from "lucide-react";
import { moveChatSessionToProject } from "@/lib/api";
import { ChannelIcon, channelLabel } from "@/components/channel-icon";
import { ChatRowActions } from "@/components/chat-row-actions";
import { useLocale } from "@/components/locale-provider";

// MIME type carried in dataTransfer for chat-session drags. Custom
// type so we don't react to unrelated drops (text dragged in from
// outside the app, files from the desktop, etc.).
export const CHAT_DRAG_MIME = "application/x-fastclaw-chat";

// Cap the sidebar list so a chatty agent doesn't push every other nav
// item off-screen. The full list lives at /agents/<id>/chats with
// pagination — the trailing "More…" row links there.
const MAX_SIDEBAR_SESSIONS = 10;

export interface SessionItem {
  id: string;
  title: string;
  preview?: string;
  updatedAt?: number;
  // Set when the session's first user turn carried an image attachment.
  // Renders as a small thumbnail before the title so multimodal chats
  // show "image + text" instead of just the text label.
  thumbnailUrl?: string;
  // channel drives the per-channel icon prefix (telegram / wechat /
  // line / web …). Empty falls back to the web glyph.
  channel?: string;
  // projectId, when set, marks this chat as belonging to a project —
  // NavSessions filters these out so they only appear nested under
  // their project (NavProjectsList renders them).
  projectId?: string;
}

export function NavSessions({
  agentId,
  sessions,
}: {
  agentId: string | null;
  sessions: SessionItem[];
}) {
  const { tr } = useLocale();
  const pathname = usePathname();
  const router = useRouter();
  // Drop-zone state for "drag a project chat back out into Chats".
  // The whole group acts as a target — we only highlight when the
  // drag carries a CHAT_DRAG_MIME payload AND the source chat is
  // currently inside a project (dropping a loose chat onto its own
  // group is a no-op, so suppressing the highlight makes that
  // self-evident). Hook must run before the early-return below to
  // keep call order stable across renders.
  const [chatsDropActive, setChatsDropActive] = React.useState(false);
  // Whole-section collapse: clicking the "Chats" header hides the list.
  const [sectionCollapsed, setSectionCollapsed] = React.useState(false);

  // Dedupe rapid double-clicks on the same chat row — see the matching
  // block in nav-projects-list.tsx for the connection-pool starvation
  // it prevents. Hooks must come before the early-return.
  const inFlightTargetRef = React.useRef<string | null>(null);
  React.useEffect(() => {
    inFlightTargetRef.current = null;
  }, [pathname]);
  const navigateOnce = React.useCallback(
    (target: string) => {
      const here =
        pathname === target || pathname === target.replace(/\/$/, "");
      if (here) return;
      if (inFlightTargetRef.current === target) return;
      inFlightTargetRef.current = target;
      router.push(target);
    },
    [pathname, router],
  );

  if (!agentId) return null;

  const chatBase = `/agents/${agentId}/chat/`;
  const looseSessions = sessions.filter((session) => !session.projectId);

  // Any mutation (rename / delete) broadcasts so AppSidebar re-fetches and
  // the chat page (if open) also re-syncs its local sessions list.
  const broadcastChange = () => {
    if (typeof window !== "undefined") {
      window.dispatchEvent(
        new CustomEvent("fastclaw:sessions-changed", {
          detail: { agentId },
        }),
      );
    }
  };

  const onChatsDragOver = (e: React.DragEvent) => {
    if (!hasChatPayload(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    if (!chatsDropActive) setChatsDropActive(true);
  };
  const onChatsDragLeave = () => setChatsDropActive(false);
  const onChatsDrop = async (e: React.DragEvent) => {
    if (!hasChatPayload(e)) return;
    e.preventDefault();
    setChatsDropActive(false);
    const sid = e.dataTransfer.getData(CHAT_DRAG_MIME);
    if (!sid) return;
    const sess = sessions.find((s) => s.id === sid);
    // Already loose — nothing to do.
    if (!sess || !sess.projectId) return;
    const res = await moveChatSessionToProject(agentId, sid, "");
    if (res?.error) {
      // Surface the failure inline; no toast infra in the sidebar yet
      // so a console error + alert keeps the user from silently losing
      // the action.
      console.error("move chat to loose failed:", res.error);
      window.alert(tr("Failed to move chat: {{error}}", "移动对话失败：{{error}}", { error: res.error }));
      return;
    }
    broadcastChange();
  };

  return (
    <>
      <SidebarGroup
        className="group-data-[collapsible=icon]:hidden"
        onDragOver={onChatsDragOver}
        onDragLeave={onChatsDragLeave}
        onDrop={onChatsDrop}
      >
        <SidebarGroupLabel
          onClick={() => setSectionCollapsed((c) => !c)}
          className="cursor-pointer select-none hover:text-sidebar-foreground"
        >
          <ChevronRightIcon
            className={
              "mr-1 transition-transform " +
              (sectionCollapsed ? "rotate-0" : "rotate-90")
            }
          />
          {tr("Chats", "对话")}
        </SidebarGroupLabel>
        {!sectionCollapsed && (
        <SidebarMenu
          className={
            chatsDropActive
              ? "rounded-md outline outline-2 outline-primary/40"
              : ""
          }
        >
          {/* Skip chats that belong to a project — they render nested
              under their project in NavProjectsList instead, so the flat
              "Chats" section keeps showing only loose chats. */}
          {looseSessions.slice(0, MAX_SIDEBAR_SESSIONS).map((s) => {
            const href = `${chatBase}${encodeURIComponent(s.id)}/`;
            // Path form: /agents/<aid>/chat/<sid>/. Match exactly so a
            // sibling chat doesn't light up just because pathname
            // shares the chat base.
            const active = pathname === href || pathname === href.replace(/\/$/, "");
            return (
              <SessionRow
                key={s.id}
                agentId={agentId}
                session={s}
                active={active}
                onOpen={() => navigateOnce(href)}
                onChanged={broadcastChange}
              />
            );
          })}
          {looseSessions.length > MAX_SIDEBAR_SESSIONS && (
            <SidebarMenuItem>
              <SidebarMenuButton
                onClick={() => navigateOnce(`/agents/${agentId}/chats`)}
                tooltip={tr("See all chats", "查看全部对话")}
                className="text-muted-foreground"
              >
                <MoreHorizontal className="size-4" />
                <span>{tr("More", "更多")}</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          )}
          {looseSessions.length === 0 && (
            <SidebarMenuItem>
              <div className="px-2 py-1.5 text-xs text-muted-foreground">
                {tr("No chats yet", "还没有对话")}
              </div>
            </SidebarMenuItem>
          )}
        </SidebarMenu>
        )}
      </SidebarGroup>
    </>
  );
}

// hasChatPayload: cheap predicate the drop targets use to gate
// preventDefault + highlight. dataTransfer.types is available during
// drag enter/over, so we can avoid lighting up for unrelated drags
// (text/uri-list, Files, etc.) that happen to cross the sidebar.
export function hasChatPayload(e: React.DragEvent): boolean {
  return Array.from(e.dataTransfer.types).includes(CHAT_DRAG_MIME);
}

function SessionRow({
  agentId,
  session,
  active,
  onOpen,
  onChanged,
}: {
  agentId: string;
  session: SessionItem;
  active: boolean;
  onOpen: () => void;
  onChanged: () => void;
}) {
  const onDragStart = (e: React.DragEvent) => {
    e.dataTransfer.setData(CHAT_DRAG_MIME, session.id);
    e.dataTransfer.effectAllowed = "move";
  };
  return (
    <SidebarMenuItem draggable onDragStart={onDragStart}>
      <SidebarMenuButton
        isActive={active}
        tooltip={`${channelLabel(session.channel)} · ${session.title}`}
        onClick={onOpen}
      >
        {(!session.channel || session.channel === "web") && session.thumbnailUrl ? (
          // eslint-disable-next-line @next/next/no-img-element
          <img
            src={session.thumbnailUrl}
            alt=""
            className="h-5 w-5 shrink-0 rounded object-cover"
          />
        ) : (
          <ChannelIcon channel={session.channel} />
        )}
        <span className="truncate">{session.title || session.id}</span>
      </SidebarMenuButton>
      <ChatRowActions
        agentId={agentId}
        session={{ id: session.id, title: session.title }}
        onChanged={onChanged}
      />
    </SidebarMenuItem>
  );
}
