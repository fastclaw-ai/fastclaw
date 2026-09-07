"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { ChevronsLeft, ChevronsRight, ImagePlus, Plus, Search } from "lucide-react";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  useSidebar,
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
import { BotAvatar } from "@/components/bot-avatar";
import { NavUser } from "@/components/nav-user";
import { useLocale, type Locale } from "@/components/locale-provider";
import { apiFetch, createAgent, type MeResponse } from "@/lib/api";

export interface ConsumerAgentItem {
  id: string;
  name: string;
  preview?: string;
  avatarUrl?: string;
  sessionId?: string;
  updatedAt?: number;
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

export function ConsumerChatSidebar({
  activeAgentId,
  agents,
  loading = false,
  me,
}: {
  activeAgentId: string;
  agents: ConsumerAgentItem[];
  loading?: boolean;
  me: MeResponse | null;
}) {
  const router = useRouter();
  const { state: sidebarState, toggleSidebar } = useSidebar();
  const { locale, t, tr } = useLocale();
  const [query, setQuery] = React.useState("");
  const [createOpen, setCreateOpen] = React.useState(false);

  const filtered = React.useMemo(() => {
    const q = query.trim().toLocaleLowerCase();
    if (!q) return agents;
    return agents.filter((agent) =>
      `${agent.name} ${agent.preview || ""}`.toLocaleLowerCase().includes(q),
    );
  }, [query, agents]);

  const openAgent = (agent: ConsumerAgentItem) => {
    const base = `/agents/${encodeURIComponent(agent.id)}/chat/`;
    router.push(agent.sessionId ? `${base}${encodeURIComponent(agent.sessionId)}/` : base);
  };

  return (
    <>
      <Sidebar
        collapsible="icon"
        className="border-r border-black/8 bg-[#f7f7f7] dark:border-white/8 dark:bg-[#171717]"
      >
      <SidebarHeader className="gap-2 px-2 pb-5 pt-0 group-data-[collapsible=icon]:px-2 group-data-[collapsible=icon]:pb-2">
        <div className="-mx-2 flex h-14 items-center gap-2 px-2 group-data-[collapsible=icon]:justify-center">
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img
            src="/logo.png"
            alt=""
            width={36}
            height={36}
            draggable={false}
            className="size-9 shrink-0 select-none object-contain group-data-[collapsible=icon]:hidden"
          />
          <span className="truncate text-[18px] font-bold tracking-[-0.025em] text-foreground group-data-[collapsible=icon]:hidden">
            FastClaw
          </span>
          <button
            type="button"
            onClick={toggleSidebar}
            className="group/sidebar-toggle relative ml-auto inline-flex size-8 shrink-0 items-center justify-center rounded-lg text-muted-foreground transition hover:bg-black/5 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring group-data-[collapsible=icon]:ml-0 group-data-[collapsible=icon]:size-10 dark:hover:bg-white/8"
            aria-label={sidebarState === "collapsed" ? t("sidebar.expandContacts") : t("sidebar.collapseContacts")}
            title={sidebarState === "collapsed" ? t("sidebar.expandContacts") : t("sidebar.collapseContacts")}
          >
            {sidebarState === "collapsed" ? (
              <>
                {/* eslint-disable-next-line @next/next/no-img-element */}
                <img
                  src="/logo.png"
                  alt=""
                  width={36}
                  height={36}
                  draggable={false}
                  className="size-9 select-none object-contain transition-opacity group-hover/sidebar-toggle:opacity-0 group-focus-visible/sidebar-toggle:opacity-0"
                />
                <ChevronsRight className="absolute size-4 opacity-0 transition-opacity group-hover/sidebar-toggle:opacity-100 group-focus-visible/sidebar-toggle:opacity-100" />
              </>
            ) : (
              <ChevronsLeft className="size-4" />
            )}
          </button>
        </div>
        <div className="flex items-center gap-1.5 group-data-[collapsible=icon]:hidden">
          <div className="relative min-w-0 flex-1">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-[15px] -translate-y-1/2 text-muted-foreground" />
            <input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t("sidebar.searchBots")}
              aria-label={t("sidebar.searchBots")}
              className="h-8 w-full rounded-md border border-black/7 bg-black/[0.035] pl-8 pr-2.5 text-sm outline-none transition focus:border-black/15 focus:bg-white focus:ring-2 focus:ring-black/5 dark:border-white/8 dark:bg-white/[0.055] dark:focus:border-white/15 dark:focus:bg-white/[0.08]"
            />
          </div>
          <button
            type="button"
            onClick={() => setCreateOpen(true)}
            className="inline-flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition hover:bg-black/5 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring dark:hover:bg-white/8"
            aria-label={t("sidebar.createBot")}
            title={t("sidebar.createBot")}
          >
            <Plus className="size-4" />
          </button>
        </div>
      </SidebarHeader>

      <SidebarContent
        aria-busy={loading}
        className="px-1.5 pb-3 group-data-[collapsible=icon]:overflow-x-hidden! group-data-[collapsible=icon]:overflow-y-auto! group-data-[collapsible=icon]:px-2"
      >
        <SidebarMenu className="gap-1 group-data-[collapsible=icon]:gap-2">
          {loading && filtered.length === 0 && (
            <>
              {[0, 1, 2].map((item) => (
                <SidebarMenuItem key={`agent-loading-${item}`}>
                  <div className="flex h-[58px] items-center gap-3 rounded-lg px-2.5 py-2 group-data-[collapsible=icon]:size-12! group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:rounded-xl group-data-[collapsible=icon]:p-[7px]!">
                    <div className="size-[34px] shrink-0 animate-pulse rounded-full bg-black/[0.075] motion-reduce:animate-none dark:bg-white/[0.1]" />
                    <div className="min-w-0 flex-1 space-y-2 group-data-[collapsible=icon]:hidden">
                      <div className="h-3 w-2/5 animate-pulse rounded-full bg-black/[0.08] motion-reduce:animate-none dark:bg-white/[0.11]" />
                      <div className="h-2.5 w-4/5 animate-pulse rounded-full bg-black/[0.055] motion-reduce:animate-none dark:bg-white/[0.075]" />
                    </div>
                  </div>
                </SidebarMenuItem>
              ))}
              <span className="sr-only" role="status">
                {tr("Loading Agents…", "正在加载 Agent…")}
              </span>
            </>
          )}
          {filtered.map((agent) => {
            const active = activeAgentId === agent.id;
            return (
              <SidebarMenuItem key={agent.id}>
                <SidebarMenuButton
                  isActive={active}
                  onClick={() => openAgent(agent)}
                  tooltip={agent.name}
                  className="h-[58px] gap-3 rounded-lg px-2.5 py-2 data-active:bg-black/[0.075] data-active:font-normal hover:bg-black/[0.05] group-data-[collapsible=icon]:size-12! group-data-[collapsible=icon]:rounded-xl group-data-[collapsible=icon]:p-[7px]! dark:data-active:bg-white/[0.11] dark:hover:bg-white/[0.07]"
                >
                  <BotAvatar
                    agentId={agent.id}
                    avatarUrl={agent.avatarUrl}
                    seed={agent.id}
                    size={34}
                  />
                  <span className="grid min-w-0 flex-1 gap-0.5 group-data-[collapsible=icon]:hidden">
                    <span className="flex min-w-0 items-baseline justify-between gap-2">
                      <span className="truncate text-[15px] font-semibold leading-5 text-foreground">
                        {agent.name || t("sidebar.untitledBot")}
                      </span>
                      <span className="shrink-0 text-[11px] font-normal text-muted-foreground/80">
                        {relativeSessionTime(agent.updatedAt, locale)}
                      </span>
                    </span>
                    <span className="truncate text-[13px] font-normal leading-5 text-muted-foreground">
                      {agent.preview || t("sidebar.greeting", { name: agent.name })}
                    </span>
                  </span>
                </SidebarMenuButton>
              </SidebarMenuItem>
            );
          })}
        </SidebarMenu>

        <button
          type="button"
          onClick={() => setCreateOpen(true)}
          className="mx-auto mt-2 hidden size-9 shrink-0 items-center justify-center rounded-lg text-muted-foreground transition hover:bg-black/5 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring group-data-[collapsible=icon]:flex dark:hover:bg-white/8"
          aria-label={t("sidebar.createBot")}
          title={t("sidebar.createBot")}
        >
          <Plus className="size-4" />
        </button>

        {!loading && filtered.length === 0 && (
          <div className="mx-2 mt-4 rounded-lg border border-dashed border-black/10 px-4 py-8 text-center group-data-[collapsible=icon]:hidden dark:border-white/10">
            <p className="text-sm font-medium">
              {query ? t("sidebar.noMatches") : t("sidebar.noBots")}
            </p>
            <button
              type="button"
              onClick={() => router.push("/agents/?manage=1")}
              className="mt-2 text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
            >
              {t("sidebar.manageBots")}
            </button>
          </div>
        )}
      </SidebarContent>

      <SidebarFooter className="px-2 pb-3 pt-2 group-data-[collapsible=icon]:px-4">
        <NavUser
          name={me?.user?.displayName || me?.user?.username || tr("User", "用户")}
          subtitle={me?.user?.role || tr("user", "用户")}
        />
      </SidebarFooter>
      <SidebarRail toggleOnClick={false} />
      </Sidebar>
      <CreateBotDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={(agentId) => {
          router.push(`/agents/${encodeURIComponent(agentId)}/chat/`);
        }}
      />
    </>
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
