"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslations } from "next-intl";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Sparkles,
  Trash2,
  Download,
  Search,
  Loader2,
  Check,
  ExternalLink,
  Settings,
  Upload,
  Files,
  Info,
} from "lucide-react";
import {
  getAgentSkills,
  deleteAgentSkill,
  installSkill,
  uploadSkill,
  searchSkills,
  getConfig,
  type SkillInfo,
  type SkillSearchResult,
} from "@/lib/api";
import { ConfigureSkillDialog, type SkillEntryView } from "@/components/configure-skill-dialog";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";

export default function AgentSkillsPage() {
  const t = useTranslations("agentSkills");
  const tc = useTranslations("common");
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);
  const [skills, setSkills] = useState<SkillInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const [installOpen, setInstallOpen] = useState(false);
  const [configureTarget, setConfigureTarget] = useState<SkillInfo | null>(null);
  // Skill env entries are GLOBAL (keyed by skill name), so the same
  // /api/config blob feeds both the global /skills page and this
  // agent-scoped one. Lets the user configure FAL_KEY etc. from
  // whichever entry point they're already on.
  const [skillEntries, setSkillEntries] = useState<Record<string, SkillEntryView>>({});
  // File input ref + upload state for the local-zip "Upload" button.
  // The server unzips to <agent>/skills/<name>/ and hot-reloads the
  // agent so the new skill shows up without a refresh.
  const uploadInputRef = useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState<string | null>(null);
  const [uploadOpen, setUploadOpen] = useState(false);
  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [dragOver, setDragOver] = useState(false);

  const fetchSkills = useCallback(() => {
    setLoading(true);
    Promise.all([
      getAgentSkills(agentId).catch(() => [] as SkillInfo[]),
      getConfig().catch(() => null),
    ])
      .then(([list, cfg]) => {
        setSkills(list || []);
        // Per-agent override map first (this page edits there); merge
        // global defaults underneath so the "configured" badge still
        // lights up when only the global value is set.
        const skillsCfg = cfg?.skills as
          | {
              entries?: Record<string, SkillEntryView>;
              agentEntries?: Record<string, Record<string, SkillEntryView>>;
            }
          | undefined;
        const globalEntries = skillsCfg?.entries || {};
        const agentMap = skillsCfg?.agentEntries?.[agentId] || {};
        const merged: Record<string, SkillEntryView> = { ...globalEntries };
        for (const [name, entry] of Object.entries(agentMap)) {
          merged[name] = entry;
        }
        setSkillEntries(merged);
      })
      .finally(() => setLoading(false));
  }, [agentId]);

  useEffect(() => {
    fetchSkills();
  }, [fetchSkills]);

  const handleDelete = async () => {
    if (!deleteTarget) return;
    await deleteAgentSkill(agentId, deleteTarget);
    setDeleteTarget(null);
    fetchSkills();
  };

  const handleUploadConfirm = async () => {
    if (!uploadFile || !agentId) return;
    setUploading(true);
    setUploadError(null);
    try {
      const resp = await uploadSkill(uploadFile, agentId);
      if (!resp.ok) {
        // Backend rejects zips that don't contain SKILL.md at the
        // skill root — surface the message inside the dialog so the
        // user can fix the zip and retry without re-opening it.
        setUploadError(resp.error || t("errorUploadFailed"));
        return;
      }
      // Success — close, reset, refresh the grid.
      setUploadOpen(false);
      setUploadFile(null);
      fetchSkills();
    } catch (e) {
      setUploadError(e instanceof Error ? e.message : t("errorUploadFailed"));
    } finally {
      setUploading(false);
      if (uploadInputRef.current) uploadInputRef.current.value = "";
    }
  };

  // Drop dialog state when the dialog closes (cancel or success), so the
  // next open is fresh — no stale file or error from a previous attempt.
  const handleUploadOpenChange = (open: boolean) => {
    setUploadOpen(open);
    if (!open) {
      setUploadFile(null);
      setUploadError(null);
      setDragOver(false);
      if (uploadInputRef.current) uploadInputRef.current.value = "";
    }
  };

  // Filter dropped/selected files to a single .zip — the dropzone accepts
  // multi-drop in the browser, but a skill bundle is one archive so we
  // take the first .zip and reject the rest with an inline message.
  const acceptDroppedFiles = (files: FileList | null) => {
    if (!files || files.length === 0) return;
    if (files.length > 1) {
      setUploadError(t("errorOnlyOneZip"));
      return;
    }
    const f = files[0];
    if (!/\.zip$/i.test(f.name)) {
      setUploadError(t("errorNotZip"));
      return;
    }
    setUploadFile(f);
    setUploadError(null);
  };

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">{t("title")}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {t.rich("subtitle", {
              name: agentName,
              strong: (chunks) => <strong>{chunks}</strong>,
            })}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={() => setUploadOpen(true)}>
            <Upload className="h-4 w-4 mr-2" />
            {t("uploadSkills")}
          </Button>
          <Button variant="outline" onClick={() => setInstallOpen(true)}>
            <Download className="h-4 w-4 mr-2" />
            {t("installSkill")}
          </Button>
        </div>
      </div>

      {loading ? (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {[1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-40" />
          ))}
        </div>
      ) : skills.length === 0 ? (
        <div className="rounded-lg border border-border bg-card">
          <div className="flex flex-col items-center justify-center py-16">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-primary/10 mb-4">
              <Sparkles className="h-7 w-7 text-primary" />
            </div>
            <p className="text-sm text-muted-foreground mb-1">
              {t("emptyTitle")}
            </p>
            <p className="text-xs text-muted-foreground/60 mb-4 max-w-sm text-center">
              {t("emptyHint")}
            </p>
            <Button variant="outline" size="sm" onClick={() => setInstallOpen(true)}>
              <Download className="h-4 w-4 mr-2" />
              {t("installSkill")}
            </Button>
          </div>
        </div>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {skills.map((skill) => (
            <div
              key={skill.name}
              className="group rounded-lg border border-border bg-card p-5 transition-colors hover:bg-muted/50"
            >
              <div className="flex items-start justify-between mb-3">
                <div className="flex items-center gap-2.5">
                  <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10">
                    <Sparkles className="h-4 w-4 text-primary" />
                  </div>
                  <div>
                    <p className="text-sm font-medium">{skill.name}</p>
                    <Badge variant="outline" className="mt-1 text-[10px]">
                      {skill.type || t("typeBadgeFallback")}
                    </Badge>
                  </div>
                </div>
                <div className="flex items-center gap-0.5 opacity-0 group-hover:opacity-100 transition-opacity">
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 text-muted-foreground hover:text-foreground"
                    onClick={() => setConfigureTarget(skill)}
                    title={t("configureTooltip")}
                  >
                    <Settings className="h-3.5 w-3.5" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 text-muted-foreground hover:text-destructive"
                    onClick={() => setDeleteTarget(skill.name)}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                </div>
              </div>
              <p className="text-sm text-muted-foreground line-clamp-2">
                {skill.description || t("noDescription")}
              </p>
              {(skillEntries[skill.name]?.apiKey ||
                Object.keys(skillEntries[skill.name]?.env || {}).length > 0) && (
                <div className="mt-2 inline-flex items-center gap-1 text-[10px] text-emerald-500">
                  <Check className="h-3 w-3" />
                  {t("configuredBadge")}
                </div>
              )}
            </div>
          ))}
        </div>
      )}

      <Dialog open={uploadOpen} onOpenChange={handleUploadOpenChange}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("uploadDialogTitle")}</DialogTitle>
          </DialogHeader>

          {/* Hidden input — both the drop zone click and the "click to upload"
              text trigger it. accept= filters the OS picker to .zip; we still
              re-validate in JS for drops since accept doesn't apply there. */}
          <input
            ref={uploadInputRef}
            type="file"
            accept=".zip,application/zip,application/x-zip-compressed"
            className="hidden"
            onChange={(e) => acceptDroppedFiles(e.target.files)}
          />

          {/* Drop zone. Keeps a constant footprint (32rem-ish content,
              ~12rem tall) so the dialog doesn't jump when a file is
              picked — empty state shows the icon + prompt, populated
              state shows the chosen filename inline. */}
          <button
            type="button"
            onClick={() => uploadInputRef.current?.click()}
            onDragOver={(e) => {
              e.preventDefault();
              setDragOver(true);
            }}
            onDragLeave={() => setDragOver(false)}
            onDrop={(e) => {
              e.preventDefault();
              setDragOver(false);
              acceptDroppedFiles(e.dataTransfer.files);
            }}
            className={`flex h-48 w-full flex-col items-center justify-center gap-3 rounded-xl border-2 border-dashed bg-muted/20 px-6 py-8 text-center transition-colors hover:bg-muted/40 ${
              dragOver ? "border-primary bg-primary/5" : "border-border"
            }`}
          >
            <Files
              className={`h-10 w-10 ${
                uploadFile ? "text-primary" : "text-muted-foreground/60"
              }`}
              strokeWidth={1.4}
            />
            {uploadFile ? (
              <div className="space-y-1">
                <p className="text-sm font-medium break-all">{uploadFile.name}</p>
                <p className="text-xs text-muted-foreground">
                  {t("fileSelectedHint", { size: (uploadFile.size / 1024).toFixed(1) })}
                </p>
              </div>
            ) : (
              <p className="text-sm text-muted-foreground">
                {t("dropzoneHint")}
              </p>
            )}
          </button>

          <div className="space-y-2">
            <p className="text-sm font-medium">{t("fileRequirements")}</p>
            <ul className="space-y-1.5 text-sm text-muted-foreground">
              <li className="flex gap-2">
                <span className="text-muted-foreground/60">•</span>
                <span>
                  {t.rich("requirementZip", {
                    code: (chunks) => <code className="text-foreground">{chunks}</code>,
                  })}
                </span>
              </li>
              <li className="flex gap-2">
                <span className="text-muted-foreground/60">•</span>
                <span>
                  {t.rich("requirementYaml", {
                    code: (chunks) => <code className="text-foreground">{chunks}</code>,
                  })}
                </span>
              </li>
            </ul>
          </div>

          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Info className="h-3.5 w-3.5 shrink-0" />
            <a
              href="https://docs.claude.com/en/docs/claude-code/skills"
              target="_blank"
              rel="noreferrer"
              className="underline hover:text-foreground"
            >
              {t("readMore")}
            </a>
          </div>

          {uploadError && (
            <p className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive break-words">
              {uploadError}
            </p>
          )}

          <div className="flex justify-end gap-2 pt-2">
            <Button
              variant="outline"
              onClick={() => handleUploadOpenChange(false)}
              disabled={uploading}
            >
              {tc("cancel")}
            </Button>
            <Button
              onClick={handleUploadConfirm}
              disabled={!uploadFile || uploading}
            >
              {uploading ? (
                <>
                  <Loader2 className="h-4 w-4 mr-2 animate-spin" />
                  {t("uploading")}
                </>
              ) : (
                t("upload")
              )}
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      <AlertDialog open={!!deleteTarget} onOpenChange={() => setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("removeTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t.rich("removeConfirm", {
                skill: deleteTarget ?? "",
                agent: agentName,
                strong: (chunks) => <strong>{chunks}</strong>,
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {tc("remove")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <InstallSkillDialog
        agentId={agentId}
        agentName={agentName}
        open={installOpen}
        onOpenChange={setInstallOpen}
        onInstalled={() => {
          setInstallOpen(false);
          fetchSkills();
        }}
        installedNames={new Set(skills.map((s) => s.name))}
      />

      <ConfigureSkillDialog
        skill={configureTarget}
        agentId={agentId}
        existing={configureTarget ? skillEntries[configureTarget.name] : undefined}
        onClose={() => setConfigureTarget(null)}
        onSaved={() => {
          setConfigureTarget(null);
          fetchSkills();
        }}
      />
    </div>
  );
}

function InstallSkillDialog({
  agentId,
  agentName,
  open,
  onOpenChange,
  onInstalled,
  installedNames,
}: {
  agentId: string;
  agentName: string;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onInstalled: () => void;
  installedNames: Set<string>;
}) {
  const t = useTranslations("agentSkills");
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SkillSearchResult[]>([]);
  const [searching, setSearching] = useState(false);
  const [installingId, setInstallingId] = useState<string | null>(null);
  const [installError, setInstallError] = useState<string | null>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    if (!open) {
      setQuery("");
      setResults([]);
      setInstallError(null);
    }
  }, [open]);

  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    if (!open) return;
    if (!query.trim()) {
      setResults([]);
      setSearching(false);
      return;
    }
    setSearching(true);
    debounceRef.current = setTimeout(() => {
      searchSkills(query)
        .then((r) => setResults(r))
        .catch(() => setResults([]))
        .finally(() => setSearching(false));
    }, 300);
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
  }, [query, open]);

  const visible = useMemo(() => results.slice(0, 20), [results]);

  const handleInstall = async (r: SkillSearchResult) => {
    setInstallError(null);
    setInstallingId(r.id);
    try {
      // agent: agentId → backend installs into ~/.fastclaw/agents/<id>/skills
      const resp = await installSkill({
        source: "skillssh",
        name: r.skillId,
        agent: agentId,
      });
      if (!resp.ok) {
        setInstallError(resp.error || t("errorInstallFailed"));
        return;
      }
      onInstalled();
    } catch (e) {
      setInstallError(e instanceof Error ? e.message : t("errorInstallFailed"));
    } finally {
      setInstallingId(null);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("installDialogTitle", { name: agentName })}</DialogTitle>
          <DialogDescription>
            {t.rich("installDialogDescription", {
              id: agentId,
              code: (chunks) => (
                <code className="font-mono text-xs">{chunks}</code>
              ),
            })}
          </DialogDescription>
        </DialogHeader>

        <div className="relative">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground/70" />
          <Input
            autoFocus
            placeholder={t("searchPlaceholder")}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="pl-9"
          />
        </div>

        <div className="min-h-[240px] max-h-[420px] overflow-y-auto -mx-1 px-1">
          {!query.trim() ? (
            <div className="flex flex-col items-center justify-center py-12 text-center">
              <Sparkles className="h-8 w-8 text-muted-foreground/40 mb-3" />
              <p className="text-sm text-muted-foreground">
                {t("searchPrompt")}
              </p>
            </div>
          ) : searching ? (
            <div className="space-y-2 py-2">
              {[1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-14" />
              ))}
            </div>
          ) : visible.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-10 text-center">
              <p className="text-sm text-muted-foreground mb-1">
                {t.rich("noResults", {
                  query,
                  strong: (chunks) => (
                    <strong className="text-foreground">{chunks}</strong>
                  ),
                })}
              </p>
            </div>
          ) : (
            <>
              <p className="text-[10px] uppercase tracking-wider text-muted-foreground/70 mb-1.5 px-1">
                {t("resultsFrom")}
              </p>
              <div className="space-y-1.5 py-1">
                {visible.map((r) => {
                  const already = installedNames.has(r.skillId);
                  const busy = installingId === r.id;
                  const detailUrl = `https://skills.sh/${r.id}`;
                  return (
                    <div
                      key={r.id}
                      className="flex items-center gap-3 rounded-md border border-border bg-card p-3 hover:bg-muted/40 transition-colors"
                    >
                      <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 shrink-0">
                        <Sparkles className="h-4 w-4 text-primary" />
                      </div>
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2">
                          <p className="text-sm font-medium truncate">{r.skillId}</p>
                          <span className="text-[10px] text-muted-foreground">
                            {t("installsCount", { count: r.installs })}
                          </span>
                        </div>
                        <a
                          href={detailUrl}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground font-mono truncate"
                          title={t("viewOnSkillsSh", { id: r.id })}
                        >
                          {r.source}
                          <ExternalLink className="h-3 w-3 shrink-0" />
                        </a>
                      </div>
                      <Button
                        size="sm"
                        variant={already ? "outline" : "default"}
                        disabled={already || busy}
                        onClick={() => handleInstall(r)}
                      >
                        {already ? (
                          <>
                            <Check className="h-3.5 w-3.5 mr-1.5" /> {t("installed")}
                          </>
                        ) : busy ? (
                          <>
                            <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" /> {t("installing")}
                          </>
                        ) : (
                          t("install")
                        )}
                      </Button>
                    </div>
                  );
                })}
              </div>
            </>
          )}
        </div>

        {installError && (
          <p className="text-xs text-destructive break-all">{installError}</p>
        )}
      </DialogContent>
    </Dialog>
  );
}
