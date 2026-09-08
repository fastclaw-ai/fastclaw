"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "@/components/ui/sidebar";
import { Bot, ChevronsUpDownIcon, PlusIcon } from "lucide-react";
import { useLocale } from "@/components/locale-provider";

// AgentAvatar shows the agent's uploaded /api/agents/{id}/files/avatar.png
// when available, falls back to the FastClaw logo for the platform header
// (no agent), and falls back to a Bot icon when an agent has no custom
// avatar yet (the image 404s).
function AgentAvatar({
  agentId,
  avatarUrl,
  size = 32,
}: {
  agentId?: string | null;
  avatarUrl?: string;
  size?: number;
}) {
  const [failed, setFailed] = React.useState(false);
  React.useEffect(() => {
    setFailed(false);
  }, [agentId, avatarUrl]);

  if (!agentId) {
    return (
      <img
        src="/logo.png"
        alt="FastClaw"
        width={size}
        height={size}
        className="rounded-lg"
        style={{ width: size, height: size }}
      />
    );
  }
  if (failed) {
    return (
      <div
        className="flex shrink-0 items-center justify-center rounded-lg bg-primary/10 dark:bg-primary/15 border border-primary/15"
        style={{ width: size, height: size }}
      >
        <Bot className="text-primary" style={{ width: size * 0.55, height: size * 0.55 }} />
      </div>
    );
  }
  return (
    // eslint-disable-next-line @next/next/no-img-element
    <img
      src={avatarUrl || `/api/agents/${agentId}/files/avatar.png`}
      alt=""
      width={size}
      height={size}
      className="shrink-0 rounded-lg object-cover"
      style={{ width: size, height: size }}
      onError={() => setFailed(true)}
    />
  );
}

export interface AgentSwitcherItem {
  id: string;
  name?: string;
  model?: string;
  description?: string;
  avatarUrl?: string;
}

// AgentSwitcher renders the sidebar header.
//
//   activeAgentId set     → show that agent's display name + id, dropdown
//                           lists every agent for quick switching
//   activeAgentId unset   → show "FastClaw" (platform brand). The dropdown
//                           still lists agents so users can jump in from
//                           any non-agent page.
//
// We never auto-promote the first agent into the header — the header on
// admin pages (Agents list, API Keys, Settings, …) stays neutral.
export function AgentSwitcher({
  agents,
  activeAgentId,
  onSelect,
  locked = false,
}: {
  agents: AgentSwitcherItem[];
  activeAgentId?: string | null;
  onSelect?: (id: string) => void;
  // locked hides the dropdown trigger / agent list / "Manage agents"
  // entirely — the header becomes a static label + avatar. Used when
  // the caller isn't the owner of the active agent (public-link
  // visitor / super_admin viewing someone else's agent), so they
  // don't see a switcher full of agents that aren't actually theirs.
  locked?: boolean;
}) {
  const { isMobile } = useSidebar();
  const router = useRouter();
  const { tr } = useLocale();

  const active = activeAgentId
    ? agents.find((a) => a.id === activeAgentId) ?? null
    : null;

  const goto = React.useCallback(
    (id: string) => {
      if (onSelect) onSelect(id);
      else router.push(`/agents/${id}/chat/`);
    },
    [onSelect, router],
  );

  const headerLabel = active ? active.name || active.id : "FastClaw";

  if (locked) {
    return (
      <SidebarMenu>
        <SidebarMenuItem>
          <SidebarMenuButton size="lg" className="cursor-default">
            <AgentAvatar agentId={active?.id} avatarUrl={active?.avatarUrl} size={32} />
            <div className="grid flex-1 text-left text-sm leading-tight">
              <span className="truncate font-medium">{headerLabel}</span>
            </div>
          </SidebarMenuButton>
        </SidebarMenuItem>
      </SidebarMenu>
    );
  }

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <SidebarMenuButton
                size="lg"
                className="data-open:bg-sidebar-accent data-open:text-sidebar-accent-foreground"
              />
            }
          >
            <AgentAvatar agentId={active?.id} avatarUrl={active?.avatarUrl} size={32} />
            <div className="grid flex-1 text-left text-sm leading-tight">
              <span className="truncate font-medium">{headerLabel}</span>
            </div>
            <ChevronsUpDownIcon className="ml-auto" />
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="min-w-56 rounded-lg"
            align="start"
            side={isMobile ? "bottom" : "right"}
            sideOffset={4}
          >
            {agents.length > 0 && (
              <>
                <DropdownMenuGroup>
                  <DropdownMenuLabel className="text-xs text-muted-foreground">
                    {tr("Agents", "Agent")}
                  </DropdownMenuLabel>
                  {agents.map((a) => (
                    <DropdownMenuItem
                      key={a.id}
                      onClick={() => goto(a.id)}
                      className="gap-2 p-2"
                    >
                      <AgentAvatar agentId={a.id} avatarUrl={a.avatarUrl} size={24} />
                      <span className="flex-1 truncate">{a.name || a.id}</span>
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuGroup>
                <DropdownMenuSeparator />
              </>
            )}
            <DropdownMenuGroup>
              <DropdownMenuItem
                className="gap-2 p-2"
                onClick={() => router.push("/agents/?manage=1")}
              >
                <div className="flex size-6 items-center justify-center rounded-md border bg-transparent">
                  <PlusIcon className="size-4" />
                </div>
                <div className="font-medium text-muted-foreground">
                  {tr("Manage agents", "管理 Agent")}
                </div>
              </DropdownMenuItem>
            </DropdownMenuGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
