"use client";

import { useEffect, useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import {
  MessagesSquare,
  PencilIcon,
  Trash2,
  ChevronLeft,
  ChevronRight,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
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
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";
import {
  getChatSessions,
  renameChatSession,
  deleteChatSession,
} from "@/lib/api";
import { ChannelIcon, channelLabel } from "@/components/channel-icon";

type Session = {
  id: string;
  channel?: string;
  accountId?: string;
  chatId?: string;
  title?: string;
  preview: string;
  thumbnailUrl?: string;
  createdAt?: number;
  updatedAt?: number;
};

const PAGE_SIZE = 20;

export default function AgentChatsPage() {
  const t = useTranslations("agentChats");
  const tc = useTranslations("common");
  const router = useRouter();
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);

  const [sessions, setSessions] = useState<Session[]>([]);
  const [error, setError] = useState("");
  const [page, setPage] = useState(1);
  const [editTarget, setEditTarget] = useState<Session | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Session | null>(null);

  async function refresh() {
    if (!agentId) return;
    setError("");
    try {
      const list = await getChatSessions(agentId);
      setSessions(list);
    } catch (e) {
      setError(e instanceof Error ? e.message : t("errorLoadChats"));
    }
  }
  useEffect(() => {
    refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agentId]);

  // Server returns sessions in some order — sort by updatedAt desc here so
  // the page is deterministic regardless of backend behavior.
  const sorted = useMemo(
    () =>
      [...sessions].sort(
        (a, b) => (b.updatedAt ?? 0) - (a.updatedAt ?? 0),
      ),
    [sessions],
  );

  const totalPages = Math.max(1, Math.ceil(sorted.length / PAGE_SIZE));
  // Clamp the page when sessions shrink below the previous count (e.g.
  // after deleting the last row on the current page).
  const safePage = Math.min(page, totalPages);
  const pageStart = (safePage - 1) * PAGE_SIZE;
  const pageRows = sorted.slice(pageStart, pageStart + PAGE_SIZE);

  function broadcastChange() {
    if (typeof window !== "undefined") {
      window.dispatchEvent(
        new CustomEvent("fastclaw:sessions-changed", {
          detail: { agentId },
        }),
      );
    }
  }

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <div className="flex items-center gap-2">
            <MessagesSquare className="size-5 text-muted-foreground" />
            <h2 className="text-2xl font-semibold tracking-tight">{t("title")}</h2>
          </div>
          <p className="text-sm text-muted-foreground mt-1">
            {t("subtitle", { name: agentName || t("thisAgent") })}
          </p>
        </div>
      </div>

      {error && (
        <Card className="border-destructive/40 bg-destructive/5">
          <CardContent className="pt-6">
            <p className="text-sm text-destructive">{error}</p>
          </CardContent>
        </Card>
      )}

      {sorted.length === 0 ? (
        <div className="rounded-lg border border-border bg-card">
          <div className="flex flex-col items-center justify-center py-16">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-primary/10 mb-4">
              <MessagesSquare className="h-7 w-7 text-primary" />
            </div>
            <p className="text-sm text-muted-foreground mb-1">
              {t("empty")}
            </p>
            <p className="text-xs text-muted-foreground/60 mb-4">
              {t("emptyHint")}
            </p>
            <Button
              variant="outline"
              size="sm"
              onClick={() => router.push(`/agents/${agentId}/chat/`)}
            >
              {t("startChat")}
            </Button>
          </div>
        </div>
      ) : (
        <>
          <div className="rounded-lg border border-border bg-card overflow-hidden">
            <Table className="table-fixed w-full">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("colTitle")}</TableHead>
                  <TableHead className="hidden md:table-cell w-[120px]">{t("colChannel")}</TableHead>
                  <TableHead className="hidden sm:table-cell w-[160px]">{t("colCreated")}</TableHead>
                  <TableHead className="w-[100px] text-right">{t("colActions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {pageRows.map((s) => (
                  <TableRow
                    key={s.id}
                    className="cursor-pointer"
                    onClick={() =>
                      router.push(
                        `/agents/${agentId}/chat/?session=${encodeURIComponent(
                          s.id,
                        )}`,
                      )
                    }
                  >
                    <TableCell className="font-medium">
                      <div className="flex items-center gap-2 min-w-0">
                        {s.thumbnailUrl ? (
                          // eslint-disable-next-line @next/next/no-img-element
                          <img
                            src={s.thumbnailUrl}
                            alt=""
                            className="h-6 w-6 shrink-0 rounded object-cover"
                          />
                        ) : (
                          <ChannelIcon channel={s.channel} className="size-4 shrink-0 text-muted-foreground" />
                        )}
                        <span
                          className="truncate"
                          title={s.title || s.preview || s.id}
                        >
                          {s.title || s.preview || s.id}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell className="hidden md:table-cell text-xs text-muted-foreground whitespace-nowrap">
                      <div className="flex items-center gap-1.5">
                        <ChannelIcon channel={s.channel} className="size-3.5 text-muted-foreground" />
                        <span>{channelLabel(s.channel)}</span>
                      </div>
                    </TableCell>
                    <TableCell className="hidden sm:table-cell text-xs text-muted-foreground whitespace-nowrap">
                      {formatTime(s.createdAt)}
                    </TableCell>
                    <TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
                      <div className="flex justify-end gap-1">
                        <Button
                          size="icon"
                          variant="ghost"
                          onClick={() => setEditTarget(s)}
                          title={t("editTitleTooltip")}
                        >
                          <PencilIcon className="size-4" />
                        </Button>
                        <Button
                          size="icon"
                          variant="ghost"
                          className="text-destructive hover:text-destructive"
                          onClick={() => setDeleteTarget(s)}
                          title={tc("delete")}
                        >
                          <Trash2 className="size-4" />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>

          {totalPages > 1 && (
            <div className="flex items-center justify-between text-sm">
              <span className="text-muted-foreground">
                {t("rangeLabel", {
                  start: pageStart + 1,
                  end: Math.min(pageStart + PAGE_SIZE, sorted.length),
                  total: sorted.length,
                })}
              </span>
              <div className="flex items-center gap-1">
                <Button
                  variant="outline"
                  size="icon"
                  onClick={() => setPage((p) => Math.max(1, p - 1))}
                  disabled={safePage <= 1}
                >
                  <ChevronLeft className="size-4" />
                </Button>
                <span className="px-3 text-muted-foreground">
                  {t("pageLabel", { page: safePage, total: totalPages })}
                </span>
                <Button
                  variant="outline"
                  size="icon"
                  onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
                  disabled={safePage >= totalPages}
                >
                  <ChevronRight className="size-4" />
                </Button>
              </div>
            </div>
          )}
        </>
      )}

      <EditTitleDialog
        target={editTarget}
        agentId={agentId}
        onClose={() => setEditTarget(null)}
        onSaved={() => {
          refresh();
          broadcastChange();
        }}
      />

      <AlertDialog
        open={!!deleteTarget}
        onOpenChange={(v) => !v && setDeleteTarget(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("deleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t.rich("deleteConfirm", {
                name:
                  deleteTarget?.title || deleteTarget?.preview || deleteTarget?.id || "",
                strong: (chunks) => <strong>{chunks}</strong>,
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={async () => {
                if (!deleteTarget || !agentId) return;
                await deleteChatSession(agentId, deleteTarget.id);
                setDeleteTarget(null);
                refresh();
                broadcastChange();
              }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {tc("delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function formatTime(ms?: number): string {
  if (!ms) return "—";
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

function EditTitleDialog({
  target,
  agentId,
  onClose,
  onSaved,
}: {
  target: Session | null;
  agentId: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const t = useTranslations("agentChats");
  const tc = useTranslations("common");
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setDraft(target?.title ?? target?.preview ?? "");
  }, [target]);

  if (!target) return null;

  const save = async () => {
    const next = draft.trim();
    if (!next || next === target.title) {
      onClose();
      return;
    }
    setSaving(true);
    try {
      await renameChatSession(agentId, target.id, next);
      onSaved();
    } finally {
      setSaving(false);
      onClose();
    }
  };

  return (
    <Dialog open={!!target} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("editDialogTitle")}</DialogTitle>
          <DialogDescription>
            {t("editDialogDesc")}
          </DialogDescription>
        </DialogHeader>
        <Input
          autoFocus
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            // Skip Enter while a CJK IME composition is active — otherwise
            // selecting a candidate would submit the dialog prematurely.
            if (e.nativeEvent.isComposing || e.keyCode === 229) return;
            if (e.key === "Enter") {
              e.preventDefault();
              save();
            }
          }}
          placeholder={t("titlePlaceholder")}
        />
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            {tc("cancel")}
          </Button>
          <Button onClick={save} disabled={saving || !draft.trim()}>
            {saving ? tc("saving") : tc("save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
