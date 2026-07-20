"use client";

import * as React from "react";
import { useTranslations } from "next-intl";
import { Bot, Check, Copy, Loader2, Save } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Skeleton } from "@/components/ui/skeleton";
import { apiFetch, getAgent, updateAgent, type AgentDetail } from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";

// AgentProfilePanel is the "Profile" tab inside the Settings dialog —
// the same fields the admin Edit Agent dialog at /agents/page.tsx
// exposes (avatar, name, description, public toggle), gated to the
// agent's owner. Viewers (super_admin browsing or public-link users)
// see read-only fields. The panel reads agentId from the URL via
// useAgentIdFromURL so the dialog component doesn't have to thread
// it through.

export default function AgentProfilePanel() {
  const t = useTranslations("agentProfile");
  const tc = useTranslations("common");
  const agentId = useAgentIdFromURL();
  const [agent, setAgent] = React.useState<AgentDetail | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);
  const [saved, setSaved] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  // Form state — independent from `agent` so users can revert with a
  // refresh and so the Save button can compare-then-write.
  const [name, setName] = React.useState("");
  const [description, setDescription] = React.useState("");
  const [isPublic, setIsPublic] = React.useState(false);
  const [avatar, setAvatar] = React.useState<File | null>(null);
  const [avatarPreview, setAvatarPreview] = React.useState<string | null>(null);
  const [avatarBust, setAvatarBust] = React.useState<number>(0);
  const [linkCopied, setLinkCopied] = React.useState(false);
  const fileInputRef = React.useRef<HTMLInputElement>(null);

  const refresh = React.useCallback(() => {
    if (!agentId) return;
    setLoading(true);
    getAgent(agentId)
      .then((a) => {
        if (!a) {
          setAgent(null);
          return;
        }
        setAgent(a);
        setName(a.name || "");
        setDescription(a.description || "");
        setIsPublic(!!a.isPublic);
      })
      .catch(() => {})
      .finally(() => setLoading(false));
  }, [agentId]);

  React.useEffect(() => {
    refresh();
  }, [refresh]);

  // Revoke blob URLs we own when the file changes or the panel
  // unmounts — without this the page leaks one URL per attachment
  // swap, mostly harmless but eslint flags it on long sessions.
  React.useEffect(() => {
    return () => {
      if (avatarPreview) URL.revokeObjectURL(avatarPreview);
    };
  }, [avatarPreview]);

  const isOwner = agent?.role === "owner";
  const dirty =
    !!agent &&
    (name.trim() !== (agent.name || "") ||
      description.trim() !== (agent.description || "") ||
      isPublic !== !!agent.isPublic ||
      avatar !== null);

  const onPickAvatar = (e: React.ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0] ?? null;
    setAvatar(f);
    if (avatarPreview) URL.revokeObjectURL(avatarPreview);
    setAvatarPreview(f ? URL.createObjectURL(f) : null);
  };

  const uploadAvatar = async (file: File) => {
    const fd = new FormData();
    fd.append("file", file, "avatar.png");
    await apiFetch(`/api/agents/${agentId}/files`, { method: "POST", body: fd });
    setAvatarBust(Date.now());
  };

  const onSave = async () => {
    if (!agentId || !agent || !isOwner) return;
    if (!name.trim()) {
      setError(t("errorNameRequired"));
      return;
    }
    setSaving(true);
    setError(null);
    try {
      const resp = await updateAgent(agentId, {
        name: name.trim(),
        description: description.trim(),
        isPublic,
      });
      if (resp && (resp.ok === false || resp.error)) {
        setError(resp.error || t("errorUpdateFailed"));
        return;
      }
      if (avatar) {
        try {
          await uploadAvatar(avatar);
        } catch {
          // Non-fatal: text fields saved, only the avatar upload
          // failed. Keep the saved-pulse so the user knows the
          // primary write went through; the next Save can retry the
          // image.
        }
        setAvatar(null);
        if (avatarPreview) URL.revokeObjectURL(avatarPreview);
        setAvatarPreview(null);
      }
      setSaved(true);
      setTimeout(() => setSaved(false), 1500);
      refresh();
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div className="p-6 max-w-3xl space-y-4">
        <Skeleton className="h-8 w-32" />
        <Skeleton className="h-20 w-full" />
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  }

  if (!agent) {
    return (
      <div className="p-6 max-w-3xl">
        <p className="text-sm text-muted-foreground">{t("notFound")}</p>
      </div>
    );
  }

  // Avatar src: the editable preview wins, then a fresh URL bust on
  // upload (so the cached image refreshes), then the canonical avatar
  // route.
  const avatarSrc =
    avatarPreview ||
    `/api/agents/${agent.id}/files/avatar.png${avatarBust ? `?v=${avatarBust}` : ""}`;

  return (
    <div className="p-6 max-w-3xl space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">{t("title")}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {isOwner ? t("subtitleOwner") : t("subtitleViewer")}
          </p>
        </div>
        {isOwner && (
          <Button
            onClick={onSave}
            disabled={saving || !dirty || !name.trim()}
            variant={saved ? "outline" : "default"}
            className={saved ? "border-emerald-500/30 text-emerald-600" : ""}
          >
            {saved ? (
              <><Check className="h-4 w-4 mr-2" /> {t("saved")}</>
            ) : saving ? (
              <><Loader2 className="h-4 w-4 mr-2 animate-spin" /> {tc("saving")}</>
            ) : (
              <><Save className="h-4 w-4 mr-2" /> {tc("save")}</>
            )}
          </Button>
        )}
      </div>

      {error && (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
          {error}
        </div>
      )}

      <div className="rounded-lg border border-border bg-card p-5 space-y-5">
        {/* Avatar + name on the same row, mirrors the admin Edit dialog. */}
        <div className="flex items-start gap-4">
          <button
            type="button"
            onClick={() => isOwner && fileInputRef.current?.click()}
            disabled={!isOwner}
            className="group relative flex size-20 shrink-0 items-center justify-center overflow-hidden rounded-xl border border-dashed bg-muted/40 transition hover:bg-muted disabled:cursor-not-allowed"
            aria-label={t("uploadAvatar")}
          >
            <AgentAvatarImg src={avatarSrc} />
            <input
              ref={fileInputRef}
              type="file"
              accept="image/*"
              className="hidden"
              onChange={onPickAvatar}
              disabled={!isOwner}
            />
          </button>
          <div className="flex-1 space-y-2">
            <Label htmlFor="agent-profile-name">{t("nameLabel")}</Label>
            <Input
              id="agent-profile-name"
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                setError(null);
              }}
              placeholder={t("namePlaceholder")}
              disabled={!isOwner}
            />
            <p className="text-xs text-muted-foreground">
              {t.rich("idLine", {
                id: agent.id,
                code: (chunks) => (
                  <code className="rounded bg-muted px-1 py-0.5 font-mono text-[11px]">
                    {chunks}
                  </code>
                ),
              })}
            </p>
          </div>
        </div>

        <div className="space-y-2">
          <Label htmlFor="agent-profile-desc">{t("descriptionLabel")}</Label>
          <Textarea
            id="agent-profile-desc"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder={t("descriptionPlaceholder")}
            rows={3}
            disabled={!isOwner}
          />
        </div>
      </div>

      <div className="space-y-3 rounded-lg border border-border bg-card p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="space-y-1">
            <Label htmlFor="agent-profile-public" className="text-sm font-medium">
              {t("publicAccessLabel")}
            </Label>
            <p className="text-xs text-muted-foreground">
              {isPublic ? t("publicAccessOnHint") : t("publicAccessOffHint")}
            </p>
          </div>
          <Switch
            id="agent-profile-public"
            checked={isPublic}
            onCheckedChange={(v) => {
              setIsPublic(!!v);
              setLinkCopied(false);
            }}
            disabled={!isOwner}
          />
        </div>
        {isPublic && agent && (
          <div className="flex gap-2">
            <Input
              readOnly
              value={
                typeof window !== "undefined"
                  ? `${window.location.origin}/agents/${agent.id}/chat/`
                  : `/agents/${agent.id}/chat/`
              }
              onFocus={(e) => e.currentTarget.select()}
              className="font-mono text-xs"
            />
            <Button
              type="button"
              variant="outline"
              onClick={async () => {
                if (!agent) return;
                const url = `${window.location.origin}/agents/${agent.id}/chat/`;
                try {
                  await navigator.clipboard.writeText(url);
                  setLinkCopied(true);
                  setTimeout(() => setLinkCopied(false), 2000);
                } catch {
                  // clipboard blocked — user can still select the input
                }
              }}
            >
              {linkCopied ? (
                <>
                  <Check className="h-4 w-4 mr-1.5" />
                  {tc("copied")}
                </>
              ) : (
                <>
                  <Copy className="h-4 w-4 mr-1.5" />
                  {tc("copy")}
                </>
              )}
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}

// AgentAvatarImg renders the avatar with a Bot fallback so an agent
// without an uploaded avatar.png doesn't show a broken-image icon.
// Mirrors the team-switcher's AgentAvatar but takes a plain src so we
// can swap in the local blob URL during edit.
function AgentAvatarImg({ src }: { src: string }) {
  const [failed, setFailed] = React.useState(false);
  React.useEffect(() => {
    setFailed(false);
  }, [src]);
  if (failed) {
    return (
      <div className="flex h-full w-full items-center justify-center bg-primary/10 dark:bg-primary/15">
        <Bot className="h-9 w-9 text-primary" />
      </div>
    );
  }
  return (
    // eslint-disable-next-line @next/next/no-img-element
    <img
      src={src}
      alt=""
      className="h-full w-full object-cover"
      onError={() => setFailed(true)}
    />
  );
}
