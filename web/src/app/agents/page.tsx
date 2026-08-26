"use client";

import { useEffect, useRef, useState } from "react";
import { copyText } from "@/lib/clipboard";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Textarea } from "@/components/ui/textarea";
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
import { Skeleton } from "@/components/ui/skeleton";
import { Bot, Plus, Trash2, ImagePlus, Pencil, Copy, Check } from "lucide-react";
import { Switch } from "@/components/ui/switch";
import {
  adminListAgents,
  apiFetch,
  getAgents,
  getMe,
  getStatus,
  createAgent,
  updateAgent,
  deleteAgent,
  type AgentDetail,
} from "@/lib/api";

interface OtherAgent {
  id: string;
  name: string;
  description?: string;
  userId: string;
  ownerUsername?: string;
  ownerEmail?: string;
  ownerDisplayName?: string;
}

// AgentAvatar tries to load /api/agents/{id}/files/avatar.png and falls
// back to the default Bot icon when the agent has no avatar yet (404).
function AgentAvatar({
  agent,
  bust,
  size = 48,
}: {
  agent: AgentDetail;
  bust?: number; // cache-buster ticked after upload
  size?: number;
}) {
  const [failed, setFailed] = useState(false);
  if (!agent.avatarUrl || failed) {
    return (
      <div
        className="flex shrink-0 items-center justify-center rounded-xl bg-primary/10 dark:bg-primary/15 border border-primary/15"
        style={{ width: size, height: size }}
      >
        <Bot className="text-primary" style={{ width: size * 0.5, height: size * 0.5 }} />
      </div>
    );
  }
  const url = bust ? `${agent.avatarUrl}?v=${bust}` : agent.avatarUrl;
  return (
    // eslint-disable-next-line @next/next/no-img-element
    <img
      src={url}
      alt={agent.name || agent.id}
      className="shrink-0 rounded-xl object-cover"
      style={{ width: size, height: size }}
      onError={() => setFailed(true)}
    />
  );
}

export default function AgentsPage() {
  const [agents, setAgents] = useState<AgentDetail[]>([]);
  const [otherAgents, setOtherAgents] = useState<OtherAgent[]>([]);
  const [isAdmin, setIsAdmin] = useState(false);
  const [loading, setLoading] = useState(true);
  const [activeTab, setActiveTab] = useState<"own" | "others">("own");
  // quotaLocked = true when the caller has agent_quota=0 (admin
  // provisions only). They can still browse /agents to see what's
  // been provisioned for them and jump into chat — we just hide the
  // Create button. If nothing has been provisioned yet, the empty
  // state tells them to contact their admin.
  const [quotaLocked, setQuotaLocked] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<AgentDetail | null>(null);
  const [deleteId, setDeleteId] = useState<string | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [saving, setSaving] = useState(false);

  // Bumped after avatar upload so <img> re-fetches the new file.
  const [avatarBust, setAvatarBust] = useState<Record<string, number>>({});

  // Create dialog state
  const [newName, setNewName] = useState("");
  const [newDescription, setNewDescription] = useState("");
  const [newAvatar, setNewAvatar] = useState<File | null>(null);
  const [newAvatarPreview, setNewAvatarPreview] = useState<string | null>(null);
  const [createError, setCreateError] = useState<string | null>(null);
  const createAvatarInput = useRef<HTMLInputElement>(null);

  // Edit dialog state
  const [editName, setEditName] = useState("");
  const [editDescription, setEditDescription] = useState("");
  const [editIsPublic, setEditIsPublic] = useState(false);
  const [editAvatar, setEditAvatar] = useState<File | null>(null);
  const [editAvatarPreview, setEditAvatarPreview] = useState<string | null>(null);
  const [editError, setEditError] = useState<string | null>(null);
  const [editLinkCopied, setEditLinkCopied] = useState(false);
  const editAvatarInput = useRef<HTMLInputElement>(null);

  const resetCreateForm = () => {
    setNewName("");
    setNewDescription("");
    setNewAvatar(null);
    if (newAvatarPreview) URL.revokeObjectURL(newAvatarPreview);
    setNewAvatarPreview(null);
    setCreateError(null);
  };

  const resetEditForm = () => {
    setEditName("");
    setEditDescription("");
    setEditIsPublic(false);
    setEditAvatar(null);
    if (editAvatarPreview) URL.revokeObjectURL(editAvatarPreview);
    setEditAvatarPreview(null);
    setEditError(null);
    setEditLinkCopied(false);
  };

  const openEdit = (agent: AgentDetail) => {
    resetEditForm();
    setEditTarget(agent);
    setEditName(agent.name || "");
    setEditDescription(agent.description || "");
    setEditIsPublic(!!agent.isPublic);
  };

  const fetchAgents = async () => {
    setLoading(true);
    // /api/agents returns the caller's owned agents only. Public agents
    // owned by other users surface as separate links — they don't auto-
    // populate the dashboard list.
    const list = await getAgents().catch(() => [] as AgentDetail[]);
    setAgents(list);
    // Admins also see other users' agents (read-only) below their own.
    // We resolve isAdmin from /api/status and only call adminListAgents
    // when entitled — non-admins would 403 and the UI would flash an error.
    const status = await getStatus().catch(() => null);
    const admin = !!status?.isAdmin;
    setIsAdmin(admin);
    if (admin) {
      const visibleIds = new Set(list.map((a) => a.id));
      const res = await adminListAgents().catch(() => null);
      const all: OtherAgent[] = (res?.agents || []) as OtherAgent[];
      setOtherAgents(all.filter((a) => !visibleIds.has(a.id)));
    } else {
      setOtherAgents([]);
    }
    setLoading(false);
  };

  const ownedAgents = agents;

  useEffect(() => {
    fetchAgents();
  }, []);

  // Resolve quotaLocked from /api/me. agent_quota === 0 means the
  // caller can't self-create — we hide the Create button but still
  // render the list so they can see admin-provisioned agents and
  // jump into chat.
  useEffect(() => {
    let aborted = false;
    getMe()
      .then((me) => {
        if (aborted) return;
        if (me?.user?.agentQuota === 0) setQuotaLocked(true);
      })
      .catch(() => {});
    return () => {
      aborted = true;
    };
  }, []);

  async function uploadAvatar(agentID: string, file: File) {
    const fd = new FormData();
    fd.append("file", file, "avatar.png");
    await apiFetch(`/api/agents/${agentID}/files`, { method: "POST", body: fd });
    setAvatarBust((m) => ({ ...m, [agentID]: Date.now() }));
  }

  const handleCreate = async () => {
    if (!newName.trim()) return;
    setSaving(true);
    setCreateError(null);
    const resp = await createAgent({
      name: newName.trim(),
      description: newDescription.trim() || undefined,
    });
    if (resp && (resp.ok === false || resp.error)) {
      setCreateError(resp.error || "Failed to create agent");
      setSaving(false);
      return;
    }
    const newId: string | undefined = resp?.agent?.id;
    if (newId && newAvatar) {
      try {
        await uploadAvatar(newId, newAvatar);
      } catch {
        // non-fatal — agent is created; avatar can be retried via Edit
      }
    }
    setSaving(false);
    setCreateOpen(false);
    resetCreateForm();
    fetchAgents();
  };

  const handleEdit = async () => {
    if (!editTarget || !editName.trim()) return;
    setSaving(true);
    setEditError(null);
    const resp = await updateAgent(editTarget.id, {
      name: editName.trim(),
      description: editDescription.trim(),
      isPublic: editIsPublic,
    });
    if (resp && (resp.ok === false || resp.error)) {
      setEditError(resp.error || "Failed to update agent");
      setSaving(false);
      return;
    }
    if (editAvatar) {
      try {
        await uploadAvatar(editTarget.id, editAvatar);
      } catch {
        // non-fatal — text fields saved; user can retry avatar upload
      }
    }
    setSaving(false);
    setEditTarget(null);
    resetEditForm();
    fetchAgents();
  };

  const handleDelete = async () => {
    if (!deleteId) return;
    setDeleting(true);
    setDeleteError(null);
    try {
      await deleteAgent(deleteId);
      setDeleteId(null);
      fetchAgents();
    } catch (err) {
      setDeleteError(err instanceof Error ? err.message : "Failed to delete agent");
    } finally {
      setDeleting(false);
    }
  };

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">Agents</h2>
          <p className="text-sm text-muted-foreground mt-1">
            Manage your AI agents and their configurations
          </p>
        </div>
        {!quotaLocked && (
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="h-4 w-4 mr-2" />
            New Agent
          </Button>
        )}
      </div>

      {loading ? (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {[1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-48" />
          ))}
        </div>
      ) : ownedAgents.length === 0 && otherAgents.length === 0 ? (
        <div className="rounded-lg border border-border bg-card">
          <div className="flex flex-col items-center justify-center py-16 text-center">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-primary/10 mb-4">
              <Bot className="h-7 w-7 text-primary" />
            </div>
            <p className="text-sm text-muted-foreground">
              {quotaLocked
                ? "No agent has been provisioned for your account yet — contact your admin."
                : "No agents configured yet"}
            </p>
            {!quotaLocked && (
              <Button
                onClick={() => setCreateOpen(true)}
                variant="outline"
                className="mt-4"
              >
                Create your first agent
              </Button>
            )}
          </div>
        </div>
      ) : (
        <>
        {isAdmin && otherAgents.length > 0 && (
          <div className="flex gap-1 border-b border-border overflow-x-auto">
            <button
              onClick={() => setActiveTab("own")}
              className={`px-3 py-2 text-sm font-medium whitespace-nowrap border-b-2 transition-colors ${
                activeTab === "own"
                  ? "border-primary text-primary"
                  : "border-transparent text-muted-foreground hover:text-foreground"
              }`}
            >
              Your agents
              <span className="ml-1.5 text-xs text-muted-foreground/70">
                {ownedAgents.length}
              </span>
            </button>
            <button
              onClick={() => setActiveTab("others")}
              className={`px-3 py-2 text-sm font-medium whitespace-nowrap border-b-2 transition-colors ${
                activeTab === "others"
                  ? "border-primary text-primary"
                  : "border-transparent text-muted-foreground hover:text-foreground"
              }`}
            >
              Others&apos; agents
              <span className="ml-1.5 text-xs text-muted-foreground/70">
                {otherAgents.length}
              </span>
            </button>
          </div>
        )}
        {(activeTab === "own" || !(isAdmin && otherAgents.length > 0)) && ownedAgents.length > 0 && (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {ownedAgents.map((agent) => (
            <div
              key={agent.id}
              className="group flex h-full flex-col rounded-lg border border-border bg-card p-5 transition-colors hover:bg-muted/50 cursor-pointer"
              onClick={() => (window.location.href = `/agents/${agent.id}/chat/`)}
            >
              <div className="flex items-start justify-between mb-4">
                <AgentAvatar agent={agent} bust={avatarBust[agent.id]} size={48} />
                {agent.isPublic ? (
                  <Badge
                    variant="outline"
                    className="bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/20"
                  >
                    <span className="mr-1.5 inline-block h-1.5 w-1.5 rounded-full bg-emerald-500" />
                    Public
                  </Badge>
                ) : (
                  <Badge
                    variant="outline"
                    className="bg-muted/60 text-muted-foreground"
                  >
                    Private
                  </Badge>
                )}
              </div>
              <p className="text-base font-medium mb-1 truncate">{agent.name || agent.id}</p>
              <p
                className={`font-mono text-xs text-muted-foreground truncate ${
                  agent.description ? "" : "mb-3"
                }`}
              >
                {agent.id}
              </p>
              {agent.description && (
                <p className="mt-2 mb-3 text-sm text-muted-foreground line-clamp-2">
                  {agent.description}
                </p>
              )}
              {/* mt-auto pins the action row to the card bottom so cards
                  with no description don't shrink — keeps the grid row
                  aligned regardless of content length. */}
              {/* quotaLocked users (agent_quota=0) are admin-provisioned —
                  they can browse and chat but can't mutate the agent
                  record, so hide Edit/Remove entirely. */}
              {!quotaLocked && (
                <div className="flex items-center gap-2 mt-auto pt-3 border-t border-border">
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-8 text-xs"
                    onClick={(e) => {
                      e.stopPropagation();
                      openEdit(agent);
                    }}
                  >
                    <Pencil className="h-3 w-3 mr-1.5" />
                    Edit
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-8 text-xs text-destructive hover:text-destructive"
                    onClick={(e) => {
                      e.stopPropagation();
                      setDeleteId(agent.id);
                    }}
                  >
                    <Trash2 className="h-3 w-3 mr-1.5" />
                    Remove
                  </Button>
                </div>
              )}
            </div>
          ))}
        </div>
        )}

        {isAdmin && otherAgents.length > 0 && activeTab === "others" && (
            <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
              {otherAgents.map((agent) => (
                <div
                  key={agent.id}
                  className="group flex h-full flex-col rounded-lg border border-border bg-card p-5 opacity-90 transition-colors hover:bg-muted/50 hover:opacity-100 cursor-pointer"
                  onClick={() =>
                    (window.location.href = `/agents/${agent.id}/chat/`)
                  }
                >
                  <div className="flex items-start justify-between mb-4">
                    <div className="flex shrink-0 items-center justify-center rounded-xl bg-gradient-to-br from-zinc-500 to-zinc-700 size-12">
                      <Bot className="text-white size-6" />
                    </div>
                    <Badge
                      variant="outline"
                      className="max-w-[60%] bg-muted/40 text-muted-foreground"
                    >
                      <span className="truncate">
                        Owner: {agent.ownerDisplayName || agent.ownerUsername || agent.userId}
                      </span>
                    </Badge>
                  </div>
                  <p className="text-base font-medium mb-1 truncate">
                    {agent.name || agent.id}
                  </p>
                  <p
                    className={`font-mono text-xs text-muted-foreground truncate ${
                      agent.description ? "" : "mb-3"
                    }`}
                  >
                    {agent.id}
                  </p>
                  {agent.description && (
                    <p className="mt-2 mb-3 text-sm text-muted-foreground line-clamp-2">
                      {agent.description}
                    </p>
                  )}
                  <div className="mt-auto pt-3 border-t border-border">
                    <p className="text-xs text-muted-foreground">
                      Click to chat — only the owner can edit or remove this agent.
                    </p>
                  </div>
                </div>
              ))}
            </div>
        )}
        </>
      )}

      {/* Create Dialog */}
      <Dialog
        open={createOpen}
        onOpenChange={(v) => {
          setCreateOpen(v);
          if (!v) resetCreateForm();
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Create New Agent</DialogTitle>
            <DialogDescription>
              The system generates a globally unique id (e.g.{" "}
              <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">agt_a1b2c3…</code>);
              everything below is for display.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="flex items-start gap-4">
              <button
                type="button"
                onClick={() => createAvatarInput.current?.click()}
                className="group relative flex size-20 shrink-0 items-center justify-center overflow-hidden rounded-xl border border-dashed bg-muted/40 transition hover:bg-muted"
                aria-label="Upload avatar"
              >
                {newAvatarPreview ? (
                  // eslint-disable-next-line @next/next/no-img-element
                  <img src={newAvatarPreview} alt="avatar" className="size-full object-cover" />
                ) : (
                  <ImagePlus className="size-6 text-muted-foreground" />
                )}
                <input
                  ref={createAvatarInput}
                  type="file"
                  accept="image/*"
                  className="hidden"
                  onChange={(e) => {
                    const f = e.target.files?.[0] ?? null;
                    setNewAvatar(f);
                    if (newAvatarPreview) URL.revokeObjectURL(newAvatarPreview);
                    setNewAvatarPreview(f ? URL.createObjectURL(f) : null);
                  }}
                />
              </button>
              <div className="flex-1 space-y-2">
                <Label htmlFor="agent-name">Name</Label>
                <Input
                  id="agent-name"
                  value={newName}
                  onChange={(e) => {
                    setNewName(e.target.value);
                    setCreateError(null);
                  }}
                  placeholder="My Helper"
                  autoFocus
                />
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="agent-desc">Description (optional)</Label>
              <Textarea
                id="agent-desc"
                value={newDescription}
                onChange={(e) => setNewDescription(e.target.value)}
                placeholder="What's this agent for? Shown in the agent list and on its profile."
                rows={3}
              />
            </div>
            {createError && (
              <p className="text-sm text-destructive">{createError}</p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              Cancel
            </Button>
            <Button onClick={handleCreate} disabled={!newName.trim() || saving}>
              {saving ? "Creating..." : "Create Agent"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Edit Dialog */}
      <Dialog
        open={editTarget !== null}
        onOpenChange={(v) => {
          if (!v) {
            setEditTarget(null);
            resetEditForm();
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Edit Agent</DialogTitle>
            <DialogDescription>
              ID is locked —{" "}
              <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">
                {editTarget?.id}
              </code>
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="flex items-start gap-4">
              <button
                type="button"
                onClick={() => editAvatarInput.current?.click()}
                className="group relative flex size-20 shrink-0 items-center justify-center overflow-hidden rounded-xl border border-dashed bg-muted/40 transition hover:bg-muted"
                aria-label="Upload avatar"
              >
                {editAvatarPreview ? (
                  // eslint-disable-next-line @next/next/no-img-element
                  <img src={editAvatarPreview} alt="avatar" className="size-full object-cover" />
                ) : editTarget ? (
                  <AgentAvatar agent={editTarget} bust={avatarBust[editTarget.id]} size={80} />
                ) : null}
                <input
                  ref={editAvatarInput}
                  type="file"
                  accept="image/*"
                  className="hidden"
                  onChange={(e) => {
                    const f = e.target.files?.[0] ?? null;
                    setEditAvatar(f);
                    if (editAvatarPreview) URL.revokeObjectURL(editAvatarPreview);
                    setEditAvatarPreview(f ? URL.createObjectURL(f) : null);
                  }}
                />
              </button>
              <div className="flex-1 space-y-2">
                <Label htmlFor="agent-edit-name">Name</Label>
                <Input
                  id="agent-edit-name"
                  value={editName}
                  onChange={(e) => {
                    setEditName(e.target.value);
                    setEditError(null);
                  }}
                  placeholder="My Helper"
                />
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="agent-edit-desc">Description</Label>
              <Textarea
                id="agent-edit-desc"
                value={editDescription}
                onChange={(e) => setEditDescription(e.target.value)}
                placeholder="What's this agent for?"
                rows={3}
              />
            </div>

            {/* Public/Private toggle. Off (default) = owner-only.
                On = anyone with the chat URL can chat under their own
                account; sessions/memory partition per chatter. */}
            <div className="space-y-3 rounded-lg border border-border p-4">
              <div className="flex items-start justify-between gap-4">
                <div className="space-y-1">
                  <Label htmlFor="agent-edit-public" className="text-sm font-medium">
                    Public access
                  </Label>
                  <p className="text-xs text-muted-foreground">
                    {editIsPublic
                      ? "Anyone with the link can chat. Their history stays private to them."
                      : "Only you can use this agent."}
                  </p>
                </div>
                <Switch
                  id="agent-edit-public"
                  checked={editIsPublic}
                  onCheckedChange={(v) => {
                    setEditIsPublic(!!v);
                    setEditLinkCopied(false);
                  }}
                />
              </div>
              {editIsPublic && editTarget && (
                <div className="flex gap-2">
                  <Input
                    readOnly
                    value={
                      typeof window !== "undefined"
                        ? `${window.location.origin}/agents/${editTarget.id}/chat/`
                        : `/agents/${editTarget.id}/chat/`
                    }
                    onFocus={(e) => e.currentTarget.select()}
                    className="font-mono text-xs"
                  />
                  <Button
                    type="button"
                    variant="outline"
                    onClick={async () => {
                      if (!editTarget) return;
                      const url = `${window.location.origin}/agents/${editTarget.id}/chat/`;
                      try {
                        if (await copyText(url)) {
                          setEditLinkCopied(true);
                          setTimeout(() => setEditLinkCopied(false), 2000);
                        }
                      } catch {
                        // clipboard blocked — user can still select the input
                      }
                    }}
                  >
                    {editLinkCopied ? (
                      <>
                        <Check className="h-4 w-4 mr-1.5" />
                        Copied
                      </>
                    ) : (
                      <>
                        <Copy className="h-4 w-4 mr-1.5" />
                        Copy
                      </>
                    )}
                  </Button>
                </div>
              )}
            </div>

            {editError && <p className="text-sm text-destructive">{editError}</p>}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setEditTarget(null)}>
              Cancel
            </Button>
            <Button onClick={handleEdit} disabled={!editName.trim() || saving}>
              {saving ? "Saving..." : "Save"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation */}
      <AlertDialog
        open={!!deleteId}
        onOpenChange={(open) => {
          if (!open && !deleting) {
            setDeleteId(null);
            setDeleteError(null);
          }
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete Agent</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to delete <strong>{deleteId}</strong>?
              This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          {deleteError && (
            <p className="text-sm text-destructive">{deleteError}</p>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                handleDelete();
              }}
              disabled={deleting}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {deleting ? "Deleting..." : "Delete"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

    </div>
  );
}
