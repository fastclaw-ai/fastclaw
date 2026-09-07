"use client";

import * as React from "react";
import { useRouter, usePathname } from "next/navigation";
import {
  SidebarGroup,
  SidebarGroupAction,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuAction,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  useSidebar,
} from "@/components/ui/sidebar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  ChevronRightIcon,
  FolderIcon,
  FolderOpenIcon,
  MoreHorizontalIcon,
  PencilIcon,
  PlusIcon,
  Trash2Icon,
} from "lucide-react";
import {
  createProject,
  deleteProject,
  moveChatSessionToProject,
  updateProject,
  type ProjectEntry,
} from "@/lib/api";
import {
  CHAT_DRAG_MIME,
  hasChatPayload,
  type SessionItem,
} from "@/components/nav-projects";
import { ChatRowActions } from "@/components/chat-row-actions";
import { useLocale } from "@/components/locale-provider";

// NavProjectsList is the "Projects" section of the agent sidebar. Each
// project expands inline to show its child chats; clicking "+ New chat"
// inside a project mints a session pre-bound to that project so the
// very first turn writes to projects/<pid>/.
//
// `sessions` is the full per-agent session list — we filter to those
// whose projectId matches each project. The flat NavSessions component
// is responsible for filtering OUT project sessions on its side so
// they don't double-render in the "Chats" section below.

export interface ProjectChatItem extends SessionItem {
  projectId?: string;
}

export function NavProjectsList({
  agentId,
  projects,
  sessions,
  onChanged,
}: {
  agentId: string | null;
  projects: ProjectEntry[];
  sessions: ProjectChatItem[];
  // Caller refetches projects + sessions when this fires (rename, delete,
  // create chat). Same pattern as the `fastclaw:sessions-changed` event
  // NavSessions broadcasts; we keep it as a callback prop because the
  // Project state lives one level up in AppSidebar.
  onChanged: () => void;
}) {
  const { tr } = useLocale();
  const router = useRouter();
  const pathname = usePathname();
  const [createOpen, setCreateOpen] = React.useState(false);
  const [editTarget, setEditTarget] = React.useState<ProjectEntry | null>(null);
  const [deleteTarget, setDeleteTarget] = React.useState<ProjectEntry | null>(
    null,
  );
  // Open project IDs: clicking a project toggles its expand state. We
  // keep this as a Set so multiple projects can be expanded at once.
  const [expanded, setExpanded] = React.useState<Set<string>>(new Set());
  // Whole-section collapse: clicking the "Projects" header hides the
  // list. Separate from per-project `expanded` above. AppSidebar stays
  // mounted across navigation so this in-memory state persists.
  const [sectionCollapsed, setSectionCollapsed] = React.useState(false);

  // useMemo must run before the early-return below or hook order
  // changes between renders when the active agent comes / goes.
  const sessionsByProject = React.useMemo(() => {
    const m = new Map<string, ProjectChatItem[]>();
    for (const s of sessions) {
      if (!s.projectId) continue;
      const arr = m.get(s.projectId) ?? [];
      arr.push(s);
      m.set(s.projectId, arr);
    }
    return m;
  }, [sessions]);

  // Two distinct "active project" signals — they look similar but
  // gate different things and resolving them together hid a subtle
  // bug where clicking a project from one of its sessions toggled
  // instead of navigating:
  //
  //   urlProjectId  — only set when the URL path is *exactly* the
  //                   project's empty new-chat state
  //                   `/agents/<aid>/project/<pid>/` (no session).
  //                   Drives the click-to-toggle branch below: a
  //                   click is "toggle" only when the user is
  //                   already sitting on the project's landing URL.
  //                   Anywhere else (including inside a session that
  //                   belongs to this project) clicks navigate.
  //
  //   activeProjectId — broader: also matches when the URL is on a
  //                     session whose project_id is this project.
  //                     Used purely for visual / expansion cues —
  //                     the row gets the `isActive` highlight and
  //                     auto-expands so the user always sees their
  //                     project's chats while reading any of them.
  const projectPathMatch = pathname.match(/\/agents\/[^/]+\/project\/([^/]+)\/?$/);
  const urlProjectId = projectPathMatch ? projectPathMatch[1] : null;
  const sessionPathMatch = pathname.match(/\/agents\/[^/]+\/chat\/([^/]+)\/?$/);
  const urlSessionId = sessionPathMatch ? sessionPathMatch[1] : null;
  const activeProjectId = React.useMemo(() => {
    if (urlProjectId) return urlProjectId;
    if (urlSessionId) {
      const sess = sessions.find((s) => s.id === urlSessionId);
      if (sess?.projectId) return sess.projectId;
    }
    return null;
  }, [urlProjectId, urlSessionId, sessions]);

  // Auto-expand the active project so the user always sees their
  // current project's chats when they land on a project chat URL —
  // without this, a fresh page load on `?project=<pid>` would show
  // the project collapsed and require an extra click. Only adds; we
  // never auto-collapse so a deliberate user collapse persists.
  React.useEffect(() => {
    if (!activeProjectId) return;
    setExpanded((prev) => {
      if (prev.has(activeProjectId)) return prev;
      const next = new Set(prev);
      next.add(activeProjectId);
      return next;
    });
  }, [activeProjectId]);

  // Track the URL we last asked the router to navigate to but haven't
  // yet observed in `pathname`. Rapid double/triple clicks on the same
  // project header used to stack one `router.push` per click; under
  // static-export each push fires its own RSC fetch, and stacking
  // enough of them drained the browser's 6-conn-per-origin pool so
  // subsequent clicks (and the in-flight SSE) went `pending` forever.
  // Hooks must run before the early-return below to keep call order
  // stable across renders.
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
  const projectBase = `/agents/${agentId}/project/`;
  // Path form: /chat/<sid>/. Read it from pathname so the highlight
  // stays in sync with the URL (the legacy `?session=` code path
  // depended on a non-reactive `window.location.search` read).
  const activeSessionKey = urlSessionId;

  const toggleExpand = (id: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  };

  // onProjectClick: clicking a project header always navigates to
  // the project's empty new-chat URL (/project/<pid>/) — even from
  // a session that belongs to the same project, so the click is a
  // reliable "give me a fresh chat in this project" affordance.
  // The toggle behavior only kicks in once the user is sitting on
  // that exact URL, so a second click on the project from its own
  // landing page collapses / re-opens the row.
  const onProjectClick = (projectId: string) => {
    if (urlProjectId === projectId) {
      toggleExpand(projectId);
      return;
    }
    navigateOnce(`${projectBase}${encodeURIComponent(projectId)}/`);
  };

  const startNewChat = (projectId: string) => {
    // Used by the "..." dropdown's "New chat in project" item — same
    // navigation as the header click but skips the "currently in
    // project = toggle" branch so it always opens a fresh chat.
    navigateOnce(`${projectBase}${encodeURIComponent(projectId)}/`);
  };

  return (
    <>
      <SidebarGroup className="group-data-[collapsible=icon]:hidden">
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
          {tr("Projects", "项目")}
        </SidebarGroupLabel>
        <SidebarGroupAction
          aria-label={tr("New project", "新建项目")}
          onClick={() => setCreateOpen(true)}
          render={
            <button>
              <PlusIcon className="size-4" />
            </button>
          }
        />
        {!sectionCollapsed && (
        <SidebarMenu>
          {projects.length === 0 && (
            <SidebarMenuItem>
              <div className="px-2 py-1.5 text-xs text-muted-foreground">
                {tr("No projects yet", "还没有项目")}
              </div>
            </SidebarMenuItem>
          )}
          {projects.map((p) => {
            const isOpen = expanded.has(p.id);
            const projectSessions = sessionsByProject.get(p.id) ?? [];
            return (
              <ProjectRow
                key={p.id}
                project={p}
                open={isOpen}
                isActive={activeProjectId === p.id}
                onClick={() => onProjectClick(p.id)}
                onEdit={() => setEditTarget(p)}
                onDelete={() => setDeleteTarget(p)}
                onNewChat={() => startNewChat(p.id)}
                sessions={projectSessions}
                activeSessionKey={activeSessionKey}
                onOpenSession={(sid) =>
                  navigateOnce(`${chatBase}${encodeURIComponent(sid)}/`)
                }
                isOnChatRoute={pathname.startsWith(chatBase)}
                allSessions={sessions}
                agentId={agentId}
                onMoved={() => {
                  // Auto-expand the destination project after a drop so
                  // the user immediately sees their chat land in its
                  // new home, then trigger a refetch.
                  setExpanded((prev) => {
                    if (prev.has(p.id)) return prev;
                    const next = new Set(prev);
                    next.add(p.id);
                    return next;
                  });
                  onChanged();
                }}
              />
            );
          })}
        </SidebarMenu>
        )}
      </SidebarGroup>

      <CreateProjectDialog
        open={createOpen}
        agentId={agentId}
        onOpenChange={(v) => setCreateOpen(v)}
        onCreated={onChanged}
      />
      <EditProjectDialog
        target={editTarget}
        agentId={agentId}
        onClose={() => setEditTarget(null)}
        onSaved={onChanged}
      />
      <DeleteProjectDialog
        target={deleteTarget}
        agentId={agentId}
        onClose={() => setDeleteTarget(null)}
        onDeleted={onChanged}
      />
    </>
  );
}

function ProjectRow({
  project,
  open,
  isActive,
  onClick,
  onEdit,
  onDelete,
  onNewChat,
  sessions,
  activeSessionKey,
  onOpenSession,
  isOnChatRoute,
  allSessions,
  agentId,
  onMoved,
}: {
  project: ProjectEntry;
  open: boolean;
  // isActive marks the project the user is currently viewing — drives
  // the visual selection state on the header AND the click semantics
  // (active = toggle, inactive = navigate).
  isActive: boolean;
  onClick: () => void;
  onEdit: () => void;
  onDelete: () => void;
  // onNewChat is wired up only via the "..." dropdown now — the
  // expanded sub-list no longer carries a "+ New chat" affordance
  // because clicking the project header itself opens a fresh chat in
  // the project (the `?project=<pid>` URL = empty new chat state).
  onNewChat: () => void;
  sessions: ProjectChatItem[];
  activeSessionKey: string | null;
  onOpenSession: (sessionId: string) => void;
  isOnChatRoute: boolean;
  // allSessions is the full per-agent list — needed during a drop to
  // look up the source chat's current projectId so a self-drop is a
  // cheap no-op instead of a wasted API roundtrip.
  allSessions: ProjectChatItem[];
  agentId: string;
  onMoved: () => void;
}) {
  const { tr } = useLocale();
  const { isMobile } = useSidebar();
  const [dropActive, setDropActive] = React.useState(false);
  const onDragOver = (e: React.DragEvent) => {
    if (!hasChatPayload(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    if (!dropActive) setDropActive(true);
  };
  const onDragLeave = () => setDropActive(false);
  const onDrop = async (e: React.DragEvent) => {
    if (!hasChatPayload(e)) return;
    e.preventDefault();
    // Stop the parent "Chats" group from also handling this drop —
    // without this a drop on a project would also fire the loose-chat
    // detach handler.
    e.stopPropagation();
    setDropActive(false);
    const sid = e.dataTransfer.getData(CHAT_DRAG_MIME);
    if (!sid) return;
    const sess = allSessions.find((s) => s.id === sid);
    if (sess && sess.projectId === project.id) return;
    const res = await moveChatSessionToProject(agentId, sid, project.id);
    if (res?.error) {
      console.error("move chat to project failed:", res.error);
      window.alert(tr("Failed to move chat: {{error}}", "移动对话失败：{{error}}", { error: res.error }));
      return;
    }
    onMoved();
  };
  return (
    <SidebarMenuItem
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
      className={dropActive ? "rounded-md outline outline-2 outline-primary/40" : ""}
    >
      <SidebarMenuButton
        tooltip={project.name}
        isActive={isActive}
        onClick={onClick}
        className="font-medium"
      >
        {/* Default: just the folder icon. Hover: swap to a chevron
            so the user can tell the row is collapsible (and the
            chevron's rotation conveys current open/closed state).
            Both icons share a slot so the row width doesn't jump
            when the hover swaps them. group/menu-item is set on
            SidebarMenuItem in components/ui/sidebar.tsx. */}
        <span className="relative size-4 shrink-0">
          <span className="absolute inset-0 flex items-center justify-center transition-opacity group-hover/menu-item:opacity-0">
            {open ? <FolderOpenIcon /> : <FolderIcon />}
          </span>
          <span className="absolute inset-0 flex items-center justify-center opacity-0 transition-opacity group-hover/menu-item:opacity-100">
            <ChevronRightIcon
              className={
                "transition-transform " + (open ? "rotate-90" : "rotate-0")
              }
            />
          </span>
        </span>
        <span className="truncate">{project.name}</span>
      </SidebarMenuButton>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <SidebarMenuAction showOnHover>
              <MoreHorizontalIcon />
              <span className="sr-only">{tr("Project actions", "项目操作")}</span>
            </SidebarMenuAction>
          }
        />
        <DropdownMenuContent
          className="w-44 rounded-lg"
          side={isMobile ? "bottom" : "right"}
          align={isMobile ? "end" : "start"}
        >
          <DropdownMenuItem onClick={onNewChat}>
            <PlusIcon className="text-muted-foreground" />
            <span>{tr("New chat in project", "在项目中新建对话")}</span>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={onEdit}>
            <PencilIcon className="text-muted-foreground" />
            <span>{tr("Edit", "编辑")}</span>
          </DropdownMenuItem>
          <DropdownMenuItem
            onClick={onDelete}
            className="text-destructive focus:text-destructive"
          >
            <Trash2Icon className="text-destructive" />
            <span>{tr("Delete", "删除")}</span>
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      {open && sessions.length > 0 && (
        <SidebarMenuSub>
          {sessions.map((s) => {
            const active = isOnChatRoute && activeSessionKey === s.id;
            return (
              <SidebarMenuSubItem
                key={s.id}
                draggable
                onDragStart={(e) => {
                  e.dataTransfer.setData(CHAT_DRAG_MIME, s.id);
                  e.dataTransfer.effectAllowed = "move";
                }}
              >
                <SidebarMenuSubButton
                  isActive={active}
                  onClick={(e) => {
                    e.preventDefault();
                    onOpenSession(s.id);
                  }}
                  // Reserve room on the right so the title doesn't slip
                  // under the absolutely-positioned actions chip on
                  // hover. pr-7 ≈ width of the 5-unit chip + gutter.
                  className="pr-7"
                >
                  <span className="truncate">{s.title || s.id}</span>
                </SidebarMenuSubButton>
                <ChatRowActions
                  agentId={agentId}
                  session={{ id: s.id, title: s.title }}
                  onChanged={onMoved}
                  variant="menu-sub-item"
                />
              </SidebarMenuSubItem>
            );
          })}
        </SidebarMenuSub>
      )}
    </SidebarMenuItem>
  );
}

function CreateProjectDialog({
  open,
  agentId,
  onOpenChange,
  onCreated,
}: {
  open: boolean;
  agentId: string;
  onOpenChange: (v: boolean) => void;
  onCreated: () => void;
}) {
  const { tr } = useLocale();
  const [name, setName] = React.useState("");
  const [description, setDescription] = React.useState("");
  const [saving, setSaving] = React.useState(false);

  React.useEffect(() => {
    if (open) {
      setName("");
      setDescription("");
    }
  }, [open]);

  const save = async () => {
    const n = name.trim();
    if (!n) return;
    setSaving(true);
    try {
      const res = await createProject(agentId, {
        name: n,
        description: description.trim() || undefined,
      });
      if ("error" in res && res.error) return;
      onCreated();
      onOpenChange(false);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{tr("New project", "新建项目")}</DialogTitle>
          <DialogDescription>
            {tr("Group chats that share research, files, or context. Every chat in a project sees the same workspace folder.", "将共享研究资料、文件或上下文的对话整理到一起。项目中的每个对话都使用同一个工作区文件夹。")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div>
            <label className="mb-1 block text-xs font-medium">{tr("Name", "名称")}</label>
            <Input
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={tr("e.g. NLP survey", "例如：NLP 调研")}
            />
          </div>
          <div>
            <label className="mb-1 block text-xs font-medium">
              {tr("Description (optional)", "说明（可选）")}
            </label>
            <Textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder={tr("What this project is for…", "这个项目用于什么…")}
              rows={3}
            />
          </div>
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={saving}
          >
            {tr("Cancel", "取消")}
          </Button>
          <Button onClick={save} disabled={saving || !name.trim()}>
            {saving ? tr("Creating…", "正在创建…") : tr("Create", "创建")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function EditProjectDialog({
  target,
  agentId,
  onClose,
  onSaved,
}: {
  target: ProjectEntry | null;
  agentId: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { tr } = useLocale();
  const [name, setName] = React.useState("");
  const [description, setDescription] = React.useState("");
  const [saving, setSaving] = React.useState(false);

  React.useEffect(() => {
    setName(target?.name ?? "");
    setDescription(target?.description ?? "");
  }, [target]);

  if (!target) return null;

  const save = async () => {
    const n = name.trim();
    if (!n) return;
    setSaving(true);
    try {
      const res = await updateProject(agentId, target.id, {
        name: n,
        description: description.trim(),
      });
      if ("error" in res && res.error) return;
      onSaved();
      onClose();
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={!!target} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{tr("Edit project", "编辑项目")}</DialogTitle>
          <DialogDescription>
            {tr("Rename the project or update its description. The workspace folder stays the same and files are not moved.", "修改项目名称或说明。工作区文件夹保持不变，文件不会移动。")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div>
            <label className="mb-1 block text-xs font-medium">{tr("Name", "名称")}</label>
            <Input
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div>
            <label className="mb-1 block text-xs font-medium">
              {tr("Description", "说明")}
            </label>
            <Textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={3}
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            {tr("Cancel", "取消")}
          </Button>
          <Button onClick={save} disabled={saving || !name.trim()}>
            {saving ? tr("Saving…", "正在保存…") : tr("Save", "保存")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DeleteProjectDialog({
  target,
  agentId,
  onClose,
  onDeleted,
}: {
  target: ProjectEntry | null;
  agentId: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { tr } = useLocale();
  const [error, setError] = React.useState<string>("");
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (target) setError("");
  }, [target]);

  const onConfirm = async () => {
    if (!target) return;
    setBusy(true);
    try {
      const res = await deleteProject(agentId, target.id);
      if (res.error) {
        // Server returned 409 with sessionCount when the project still
        // owns chats — surface a hint instead of just "delete failed".
        if (res.sessionCount && res.sessionCount > 0) {
          setError(
            tr(
              "This project still has {{count}} chat(s). Delete or move them first.",
              "此项目中仍有 {{count}} 个对话，请先删除或移动它们。",
              { count: res.sessionCount },
            ),
          );
        } else {
          setError(res.error);
        }
        return;
      }
      onDeleted();
      onClose();
    } finally {
      setBusy(false);
    }
  };

  return (
    <AlertDialog open={!!target} onOpenChange={(v) => !v && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{tr("Delete project", "删除项目")}</AlertDialogTitle>
          <AlertDialogDescription>
            {tr(
              "Delete {{project}}? Chats inside the project must be removed first; they will not be deleted automatically. The workspace folder on disk will remain.",
              "确定删除 {{project}} 吗？必须先移除项目中的对话，系统不会级联删除；磁盘上的工作区文件夹会保留。",
              { project: target?.name || "" },
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <div className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-xs text-destructive">
            {error}
          </div>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{tr("Cancel", "取消")}</AlertDialogCancel>
          <AlertDialogAction
            onClick={onConfirm}
            disabled={busy}
            className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
          >
            {busy ? tr("Deleting…", "正在删除…") : tr("Delete", "删除")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
