"use client";

import * as React from "react";
import {
  BrainIcon,
  ClockIcon,
  CoinsIcon,
  IdCardIcon,
  InfoIcon,
  LayersIcon,
  Palette,
  Plug,
  RadioIcon,
  ServerIcon,
  SparklesIcon,
  UserCog,
  Wand2Icon,
} from "lucide-react";

import { Dialog, DialogContent } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

import AgentProfilePanel from "@/components/agent-profile-panel";
import AgentCustomizePage from "@/app/agents/[id]/customize/page";
import AgentModelsPage from "@/app/agents/[id]/models/page";
import AgentContextPage from "@/app/agents/[id]/context/page";
import AgentSkillsPage from "@/app/agents/[id]/skills/page";
import AgentPluginsPage from "@/app/agents/[id]/plugins/page";
import AgentMCPPage from "@/app/agents/[id]/mcp/page";
import AgentChannelsPage from "@/app/agents/[id]/channels/page";
import AgentSchedulerPage from "@/app/agents/[id]/scheduler/page";
import AgentUsagePage from "@/app/agents/[id]/usage/page";
import AccountSettingsPage from "@/app/settings/account/page";
import GeneralSettingsPage from "@/app/settings/general/page";
import UserModelsPage from "@/app/models/page";
import AboutSettingsPage from "@/app/settings/about/page";

export type AgentSettingsTab =
  | "profile"
  | "customize"
  | "models"
  | "context"
  | "skills"
  | "plugins"
  | "mcp"
  | "channels"
  | "scheduler"
  | "usage"
  | "account"
  | "general"
  | "about";

type TabIcon = React.ComponentType<{ className?: string }>;

const AGENT_TABS: Array<{ id: AgentSettingsTab; label: string; icon: TabIcon }> = [
  { id: "profile", label: "Profile", icon: IdCardIcon },
  { id: "customize", label: "Customize", icon: Wand2Icon },
  { id: "models", label: "Models", icon: BrainIcon },
  { id: "context", label: "Context", icon: LayersIcon },
  { id: "skills", label: "Skills", icon: SparklesIcon },
  { id: "plugins", label: "Plugins", icon: Plug },
  { id: "mcp", label: "MCP", icon: ServerIcon },
  { id: "channels", label: "Channels", icon: RadioIcon },
  { id: "scheduler", label: "Scheduler", icon: ClockIcon },
  { id: "usage", label: "Token Usage", icon: CoinsIcon },
];

// Runtime intentionally lives only on the standalone /settings/runtime
// page (super_admin-gated) — it's a deployment-wide knob, not the kind
// of thing the average chatter wants in their per-agent dialog.
const USER_TABS: Array<{ id: AgentSettingsTab; label: string; icon: TabIcon }> = [
  { id: "account", label: "Account", icon: UserCog },
  { id: "general", label: "General", icon: Palette },
  // About surfaces the gateway version + upgrade hint — only useful
  // to operators (super_admin), filtered out below for regular users.
  { id: "about", label: "About", icon: InfoIcon },
];

// Tabbed configuration panel. Hosts both the per-agent pages
// (Customize / Models / Skills / Channels / Scheduler) and the
// per-user pages (Account / General / Runtime[admin-only]) so a
// click on the sidebar Settings button covers everything the user
// could want to change. Each tab mounts the existing page component
// lazily — switching tabs unmounts the previous panel, which is fine
// because the pages are self-contained and re-fetch on mount.
//
// role="viewer" hides the owner-only Agent tabs (Profile, Customize,
// Skills, Scheduler, Usage) and only exposes Models + Channels under
// Agent — viewers can pin their own model for the shared agent and
// bind their own IM accounts, but can't touch the agent's identity /
// skills / scheduling. The Models tab id is shared with owners; the
// render branch below picks the agent-scope page for owners and the
// user-scope page for viewers (same tab slot, different writer).
export function AgentSettingsDialog({
  open,
  onOpenChange,
  defaultTab,
  role = "owner",
  userOnly = false,
  isAdmin = false,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  defaultTab?: AgentSettingsTab;
  role?: "owner" | "viewer";
  // userOnly hides the Agent section entirely. Used by the platform
  // sidebar's Settings button, which has no agent context — it should
  // only expose Account + General.
  userOnly?: boolean;
  // isAdmin gates super_admin-only tabs (currently just About — the
  // gateway version + upgrade hint is operator info, not end-user info).
  isAdmin?: boolean;
}) {
  const agentTabs = userOnly
    ? []
    : role === "viewer"
      ? AGENT_TABS.filter((t) => t.id === "models" || t.id === "channels")
      : AGENT_TABS;
  const userTabs = isAdmin ? USER_TABS : USER_TABS.filter((t) => t.id !== "about");
  // Pick the landing tab: userOnly opens on General (User section);
  // viewers land on Models (the first Agent tab they have); owners on
  // Profile.
  const initialTab: AgentSettingsTab =
    defaultTab ??
    (userOnly ? "general" : role === "viewer" ? "models" : "profile");
  const [tab, setTab] = React.useState<AgentSettingsTab>(initialTab);

  // Reset to the requested tab whenever the dialog re-opens, so a fresh
  // click on the sidebar Settings button always lands on the same place.
  React.useEffect(() => {
    if (open) setTab(initialTab);
  }, [open, initialTab]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className={cn(
          "p-0 gap-0 overflow-hidden",
          "h-[85vh] w-[95vw] max-w-[1100px] sm:max-w-[1100px]",
          "grid grid-cols-[220px_1fr] grid-rows-1",
        )}
      >
        <aside className="flex flex-col gap-1 border-r bg-muted/40 p-3 overflow-y-auto">
          {agentTabs.length > 0 && (
            <>
              <SectionLabel>Agent</SectionLabel>
              {agentTabs.map((t) => (
                <TabButton
                  key={t.id}
                  tab={t}
                  active={tab === t.id}
                  onSelect={setTab}
                />
              ))}
            </>
          )}
          <SectionLabel className={agentTabs.length > 0 ? "mt-3" : undefined}>
            User
          </SectionLabel>
          {userTabs.map((t) => (
            <TabButton
              key={t.id}
              tab={t}
              active={tab === t.id}
              onSelect={setTab}
            />
          ))}
        </aside>
        <div className="overflow-y-auto">
          {tab === "profile" && <AgentProfilePanel />}
          {tab === "customize" && <AgentCustomizePage />}
          {tab === "models" &&
            (role === "viewer" ? <UserModelsPage /> : <AgentModelsPage />)}
          {tab === "context" && <AgentContextPage />}
          {tab === "skills" && <AgentSkillsPage />}
          {tab === "plugins" && <AgentPluginsPage />}
          {tab === "mcp" && <AgentMCPPage />}
          {tab === "channels" && <AgentChannelsPage />}
          {tab === "scheduler" && <AgentSchedulerPage />}
          {tab === "usage" && <AgentUsagePage />}
          {tab === "account" && (
            <div className="p-6 max-w-3xl">
              <AccountSettingsPage />
            </div>
          )}
          {tab === "general" && (
            <div className="p-6 max-w-3xl">
              <GeneralSettingsPage />
            </div>
          )}
          {tab === "about" && (
            <div className="p-6 max-w-3xl">
              <AboutSettingsPage />
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function SectionLabel({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "px-2 pt-1 pb-2 text-xs font-medium uppercase tracking-wide text-muted-foreground",
        className,
      )}
    >
      {children}
    </div>
  );
}

function TabButton({
  tab,
  active,
  onSelect,
}: {
  tab: { id: AgentSettingsTab; label: string; icon: TabIcon };
  active: boolean;
  onSelect: (id: AgentSettingsTab) => void;
}) {
  const Icon = tab.icon;
  return (
    <button
      type="button"
      onClick={() => onSelect(tab.id)}
      className={cn(
        "flex items-center gap-2 rounded-md px-2.5 py-2 text-sm text-left transition-colors",
        active
          ? "bg-accent text-accent-foreground font-medium"
          : "text-foreground/80 hover:bg-accent/50",
      )}
    >
      <Icon className="size-4 shrink-0" />
      <span>{tab.label}</span>
    </button>
  );
}
