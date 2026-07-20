"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
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
import { useSidebar } from "@/components/ui/sidebar";
import { MoreHorizontalIcon, PencilIcon, Trash2Icon } from "lucide-react";
import { deleteChatSession, renameChatSession } from "@/lib/api";

// ChatRowActions is the shared "..." dropdown attached to every chat
// row in the sidebar — both the flat "Chats" list and the chats nested
// under a project. It owns its own Edit / Delete dialog state so the
// caller only needs to render the trigger and the dialogs unmount the
// instant they close.
//
// Variant controls absolute positioning + hover gating:
//   "menu-item"     — paired with SidebarMenuButton (top-1.5 right-1)
//   "menu-sub-item" — paired with SidebarMenuSubButton (smaller; uses
//                     the group/menu-sub-item hover scope so the trigger
//                     only fades in when the sub-row is hovered).

export interface ChatRowSession {
  id: string;
  title: string;
}

export function ChatRowActions({
  agentId,
  session,
  onChanged,
  variant = "menu-item",
}: {
  agentId: string;
  session: ChatRowSession;
  onChanged: () => void;
  variant?: "menu-item" | "menu-sub-item";
}) {
  const router = useRouter();
  const { isMobile } = useSidebar();
  const t = useTranslations("sessions");
  const tc = useTranslations("common");
  const [editOpen, setEditOpen] = React.useState(false);
  const [deleteOpen, setDeleteOpen] = React.useState(false);

  const onConfirmDelete = async () => {
    setDeleteOpen(false);
    try {
      await deleteChatSession(agentId, session.id);
    } finally {
      // If the deleted session is currently open, bounce back to the
      // fresh chat URL so the page doesn't hang on a stale id.
      if (
        typeof window !== "undefined" &&
        window.location.pathname.replace(/\/$/, "").endsWith("/chat/" + session.id)
      ) {
        router.replace(`/agents/${encodeURIComponent(agentId)}/chat/`);
      }
      onChanged();
    }
  };

  // Trigger styling: SidebarMenuAction (used by the flat chats list)
  // hooks into group-hover/menu-item; project sub-rows use a different
  // group selector (group/menu-sub-item) and a smaller chip so it fits
  // the h-7 sub-button. The two trigger flavors live here so callers
  // don't have to know either layout's details.
  //
  // The sub-item variant uses right:-20px so the chip pokes out past
  // the SidebarMenuSub's mx-3.5 (14px margin) PLUS px-2.5 (10px
  // padding) inset and ends up flush with the parent project row's
  // `...` action (which sits at right:4px). 14 + 10 - 4 = 20 →
  // right:-20px. Without this escape the sub chip sits ~20px to the
  // left of the parent's chip.
  const triggerClass =
    variant === "menu-sub-item"
      ? "absolute top-1 right-[-20px] flex h-5 w-5 items-center justify-center rounded-md text-sidebar-foreground outline-hidden transition-opacity hover:bg-sidebar-accent hover:text-sidebar-accent-foreground aria-expanded:opacity-100 md:opacity-0 group-hover/menu-sub-item:opacity-100 group-focus-within/menu-sub-item:opacity-100 [&>svg]:size-4"
      : "absolute top-1.5 right-1 flex aspect-square w-5 items-center justify-center rounded-md p-0 text-sidebar-foreground outline-hidden transition-transform hover:bg-sidebar-accent hover:text-sidebar-accent-foreground aria-expanded:opacity-100 md:opacity-0 group-hover/menu-item:opacity-100 group-focus-within/menu-item:opacity-100 [&>svg]:size-4";

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <button type="button" className={triggerClass}>
              <MoreHorizontalIcon />
              <span className="sr-only">{t("actionsLabel")}</span>
            </button>
          }
        />
        <DropdownMenuContent
          className="w-40 rounded-lg"
          side={isMobile ? "bottom" : "right"}
          align={isMobile ? "end" : "start"}
        >
          <DropdownMenuItem onClick={() => setEditOpen(true)}>
            <PencilIcon className="text-muted-foreground" />
            <span>{tc("edit")}</span>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            onClick={() => setDeleteOpen(true)}
            className="text-destructive focus:text-destructive"
          >
            <Trash2Icon className="text-destructive" />
            <span>{tc("delete")}</span>
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <EditTitleDialog
        open={editOpen}
        onOpenChange={setEditOpen}
        agentId={agentId}
        session={session}
        onSaved={onChanged}
      />

      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("deleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t.rich("deleteDescription", {
                title: session.title || session.id,
                strong: (chunks) => <strong>{chunks}</strong>,
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={onConfirmDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {tc("delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function EditTitleDialog({
  open,
  onOpenChange,
  agentId,
  session,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  agentId: string;
  session: ChatRowSession;
  onSaved: () => void;
}) {
  const t = useTranslations("sessions");
  const tc = useTranslations("common");
  const [draft, setDraft] = React.useState("");
  const [saving, setSaving] = React.useState(false);

  // Re-prime the draft each time the dialog opens. Without this, a
  // user who edits, cancels, and re-opens would see their stale draft.
  React.useEffect(() => {
    if (open) setDraft(session.title ?? "");
  }, [open, session.title]);

  const save = async () => {
    const next = draft.trim();
    if (!next || next === session.title) {
      onOpenChange(false);
      return;
    }
    setSaving(true);
    try {
      await renameChatSession(agentId, session.id, next);
      onSaved();
    } finally {
      setSaving(false);
      onOpenChange(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("editTitle")}</DialogTitle>
          <DialogDescription>
            {t("editDescription")}
          </DialogDescription>
        </DialogHeader>
        <Input
          autoFocus
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            // Ignore Enter while a CJK IME composition is active —
            // otherwise selecting a candidate would submit the dialog
            // prematurely.
            if (e.nativeEvent.isComposing || e.keyCode === 229) return;
            if (e.key === "Enter") {
              e.preventDefault();
              save();
            }
          }}
          placeholder={t("titlePlaceholder")}
        />
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={saving}
          >
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
