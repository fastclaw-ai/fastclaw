"use client";

import * as React from "react";
import {
  BrainIcon,
  BookOpenIcon,
  ClockIcon,
  CoinsIcon,
  IdCardIcon,
  InfoIcon,
  KeyRoundIcon,
  LayersIcon,
  MessagesSquareIcon,
  Palette,
  Plug,
  RadioIcon,
  ServerIcon,
  SparklesIcon,
  UserCog,
  UsersIcon,
  Wand2Icon,
  WrenchIcon,
} from "lucide-react";

import { Dialog, DialogContent } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import { useLocale, type MessageKey } from "@/components/locale-provider";

import AgentProfilePanel from "@/components/agent-profile-panel";
import AgentCustomizePage from "@/app/agents/[id]/customize/page";
import AgentModelsPage from "@/app/agents/[id]/models/page";
import AgentContextPage from "@/app/agents/[id]/context/page";
import AgentKnowledgePage from "@/app/agents/[id]/knowledge/page";
import AgentSkillsPage from "@/app/agents/[id]/skills/page";
import AgentPluginsPage from "@/app/agents/[id]/plugins/page";
import AgentChannelsPage from "@/app/agents/[id]/channels/page";
import AgentSchedulerPage from "@/app/agents/[id]/scheduler/page";
import AgentMCPPage from "@/app/agents/[id]/mcp/page";
import AgentUsagePage from "@/app/agents/[id]/usage/page";
import AccountSettingsPage from "@/app/settings/account/page";
import GeneralSettingsPage from "@/app/settings/general/page";
import UserModelsPage from "@/app/models/page";
import ApikeysPage from "@/app/apikeys/page";
import SystemSkillsPage from "@/app/skills/page";
import SystemToolsPage from "@/app/tools/page";
import AboutSettingsPage from "@/app/settings/about/page";
import AdminUsersPage from "@/app/admin/users/page";
import AdminChatsPage from "@/app/admin/chats/page";
import AdminUsagePage from "@/app/admin/usage/page";

export type AgentSettingsTab =
  | "profile"
  | "customize"
  | "models"
  | "context"
  | "knowledge"
  | "skills"
  | "mcp"
  | "plugins"
  | "channels"
  | "scheduler"
  | "usage"
  | "account"
  | "general"
  | "apiKeys"
  | "userModels"
  | "userSkills"
  | "systemModels"
  | "systemSkills"
  | "systemTools"
  | "systemUsers"
  | "systemChats"
  | "systemUsage"
  | "about";

type TabIcon = React.ComponentType<{ className?: string }>;

const AGENT_TABS: Array<{ id: AgentSettingsTab; label: string; icon: TabIcon }> = [
  { id: "profile", label: "Profile", icon: IdCardIcon },
  { id: "customize", label: "Customize", icon: Wand2Icon },
  { id: "models", label: "Models", icon: BrainIcon },
  { id: "context", label: "Context", icon: LayersIcon },
  { id: "knowledge", label: "Knowledge", icon: BookOpenIcon },
  { id: "skills", label: "Skills", icon: SparklesIcon },
  { id: "mcp", label: "MCP", icon: ServerIcon },
  { id: "plugins", label: "Plugins", icon: Plug },
  { id: "channels", label: "Channels", icon: RadioIcon },
  { id: "scheduler", label: "Scheduler", icon: ClockIcon },
  { id: "usage", label: "Token Usage", icon: CoinsIcon },
];

const USER_TABS: Array<{ id: AgentSettingsTab; label: string; icon: TabIcon }> = [
  { id: "account", label: "Account", icon: UserCog },
  { id: "general", label: "General", icon: Palette },
  { id: "apiKeys", label: "API Keys", icon: KeyRoundIcon },
];

// Models + skill credentials are personal overrides for every account,
// including super_admin accounts. Admins additionally get a separate System
// group below; explicit API scope keeps the two layers independent.
const USER_CONFIGURATION_TABS: Array<{ id: AgentSettingsTab; label: string; icon: TabIcon }> = [
  { id: "userModels", label: "Models", icon: BrainIcon },
  { id: "userSkills", label: "Skills", icon: SparklesIcon },
];

const SYSTEM_TABS: Array<{ id: AgentSettingsTab; label: string; icon: TabIcon }> = [
  { id: "systemUsers", label: "Users", icon: UsersIcon },
  { id: "systemChats", label: "Chats", icon: MessagesSquareIcon },
  { id: "systemUsage", label: "Token Usage", icon: CoinsIcon },
  { id: "systemModels", label: "Models", icon: BrainIcon },
  { id: "systemSkills", label: "Skills", icon: SparklesIcon },
  { id: "systemTools", label: "Tools", icon: WrenchIcon },
  { id: "about", label: "About", icon: InfoIcon },
];

const TAB_LABEL_KEYS: Record<AgentSettingsTab, MessageKey> = {
  profile: "settings.tab.profile",
  customize: "settings.tab.customize",
  models: "settings.tab.models",
  context: "settings.tab.context",
  knowledge: "settings.tab.knowledge",
  skills: "settings.tab.skills",
  mcp: "settings.tab.mcp",
  plugins: "settings.tab.plugins",
  channels: "settings.tab.channels",
  scheduler: "settings.tab.scheduler",
  usage: "settings.tab.usage",
  account: "settings.tab.account",
  general: "settings.tab.general",
  apiKeys: "settings.tab.apiKeys",
  userModels: "settings.tab.models",
  userSkills: "settings.tab.skills",
  systemModels: "settings.tab.models",
  systemSkills: "settings.tab.skills",
  systemTools: "settings.tab.tools",
  systemUsers: "settings.tab.users",
  systemChats: "settings.tab.chats",
  systemUsage: "settings.tab.usage",
  about: "settings.tab.about",
};

// Tabbed configuration panel with two mutually-exclusive modes:
// Agent settings (the default) and User settings (`userOnly`). Keeping
// them separate prevents account preferences from appearing inside a
// Bot-specific settings surface. Each tab mounts its existing page
// component lazily.
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
  // userOnly hides the Agent section entirely. Used by the account-menu
  // Settings entry to expose personal configuration and, for admins, a
  // separately labeled System section.
  userOnly?: boolean;
  // isAdmin adds deployment-wide configuration under a distinct System
  // section; API authorization remains the source of truth.
  isAdmin?: boolean;
}) {
  const { t } = useLocale();
  const agentTabs = userOnly
    ? []
    : role === "viewer"
      ? AGENT_TABS.filter((t) => t.id === "models" || t.id === "channels")
      : AGENT_TABS;
  const userTabs = userOnly
    ? [...USER_TABS, ...USER_CONFIGURATION_TABS]
    : [];
  const systemTabs = userOnly && isAdmin ? SYSTEM_TABS : [];
  const visibleTabs = [...agentTabs, ...userTabs, ...systemTabs];
  // Pick the landing tab: userOnly opens on General (User section);
  // viewers land on Models (the first Agent tab they have); owners on
  // Profile. Ignore a requested default that isn't visible in the
  // current mode so switching between Agent and User settings can never
  // leave the content pane blank.
  const initialTab: AgentSettingsTab =
    defaultTab && visibleTabs.some((candidate) => candidate.id === defaultTab)
      ? defaultTab
      : userOnly
        ? "general"
        : role === "viewer"
          ? "models"
          : "profile";
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
              <SectionLabel>{t("common.agent")}</SectionLabel>
              {agentTabs.map((agentTab) => (
                <TabButton
                  key={agentTab.id}
                  tab={{ ...agentTab, label: t(TAB_LABEL_KEYS[agentTab.id]) }}
                  active={tab === agentTab.id}
                  onSelect={setTab}
                />
              ))}
            </>
          )}
          {userTabs.length > 0 && (
            <>
              <SectionLabel>{t("common.user")}</SectionLabel>
              {userTabs.map((userTab) => (
                <TabButton
                  key={userTab.id}
                  tab={{ ...userTab, label: t(TAB_LABEL_KEYS[userTab.id]) }}
                  active={tab === userTab.id}
                  onSelect={setTab}
                />
              ))}
            </>
          )}
          {systemTabs.length > 0 && (
            <>
              <SectionLabel className="mt-3">{t("common.system")}</SectionLabel>
              {systemTabs.map((systemTab) => (
                <TabButton
                  key={systemTab.id}
                  tab={{ ...systemTab, label: t(TAB_LABEL_KEYS[systemTab.id]) }}
                  active={tab === systemTab.id}
                  onSelect={setTab}
                />
              ))}
            </>
          )}
        </aside>
        <div className="min-w-0 overflow-auto">
          {tab === "profile" && <AgentProfilePanel />}
          {tab === "customize" && <AgentCustomizePage />}
          {tab === "models" &&
            (role === "viewer" ? <UserModelsPage /> : <AgentModelsPage />)}
          {tab === "context" && <AgentContextPage />}
          {tab === "knowledge" && <AgentKnowledgePage />}
          {tab === "skills" && <AgentSkillsPage />}
          {tab === "mcp" && <AgentMCPPage />}
          {tab === "plugins" && <AgentPluginsPage />}
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
          {tab === "apiKeys" && <ApikeysPage />}
          {tab === "userModels" && <UserModelsPage scope="user" />}
          {tab === "userSkills" && <SystemSkillsPage scope="user" />}
          {tab === "systemModels" && <UserModelsPage scope="system" />}
          {tab === "systemSkills" && <SystemSkillsPage scope="system" />}
          {tab === "systemTools" && <SystemToolsPage />}
          {tab === "systemUsers" && <AdminUsersPage />}
          {tab === "systemChats" && <AdminChatsPage />}
          {tab === "systemUsage" && <AdminUsagePage />}
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
