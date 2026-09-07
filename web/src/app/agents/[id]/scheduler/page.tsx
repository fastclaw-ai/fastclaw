"use client";

import { useCallback, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
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
  Clock,
  Trash2,
  Repeat,
  Calendar,
  Hourglass,
  MessageSquare,
} from "lucide-react";
import {
  listAgentCronJobs,
  deleteAgentCronJob,
  toggleAgentCronJob,
  type AgentCronJob,
} from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";
import { useLocale, type Locale } from "@/components/locale-provider";

// Scheduler page: lists every cron job the agent has on file. The
// `create_cron_job` tool the agent itself uses writes here, so anything
// you said in chat ("每分钟讲笑话", "5 分钟后提醒我睡觉") shows up as a
// row. Disable to pause without losing the job; delete to remove it.

function fmtSchedule(job: AgentCronJob, locale: Locale): string {
  switch (job.type) {
    case "interval":
      return locale === "zh-CN" ? `每 ${job.schedule}` : `every ${job.schedule}`;
    case "once":
      return locale === "zh-CN" ? `于 ${job.schedule}` : `at ${job.schedule}`;
    case "cron":
    default:
      return job.schedule;
  }
}

function fmtRelative(iso: string | undefined, locale: Locale): string {
  if (!iso) return "—";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return iso;
  const diff = t - Date.now();
  const abs = Math.abs(diff);
  const mins = Math.round(abs / 60_000);
  if (mins < 1) return locale === "zh-CN" ? (diff > 0 ? "不到 1 分钟后" : "刚刚") : (diff > 0 ? "in <1m" : "just now");
  if (mins < 60) return locale === "zh-CN" ? (diff > 0 ? `${mins} 分钟后` : `${mins} 分钟前`) : (diff > 0 ? `in ${mins}m` : `${mins}m ago`);
  const hours = Math.round(mins / 60);
  if (hours < 48) return locale === "zh-CN" ? (diff > 0 ? `${hours} 小时后` : `${hours} 小时前`) : (diff > 0 ? `in ${hours}h` : `${hours}h ago`);
  const days = Math.round(hours / 24);
  return locale === "zh-CN" ? (diff > 0 ? `${days} 天后` : `${days} 天前`) : (diff > 0 ? `in ${days}d` : `${days}d ago`);
}

function typeIcon(type: string) {
  switch (type) {
    case "interval":
      return <Repeat className="h-3.5 w-3.5" />;
    case "once":
      return <Hourglass className="h-3.5 w-3.5" />;
    case "cron":
    default:
      return <Calendar className="h-3.5 w-3.5" />;
  }
}

export default function AgentSchedulerPage() {
  const { tr } = useLocale();
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);

  const [jobs, setJobs] = useState<AgentCronJob[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<AgentCronJob | null>(null);
  // Track in-flight toggles by job id so the row reflects optimistic
  // state and the switch doesn't double-fire while the request is open.
  const [toggling, setToggling] = useState<Record<string, boolean>>({});

  const refresh = useCallback(() => {
    if (!agentId) return;
    setLoading(true);
    listAgentCronJobs(agentId)
      .then((list) => {
        setJobs(list);
        setError("");
      })
      .catch((e) => setError(e instanceof Error ? e.message : tr("Failed to load jobs", "加载定时任务失败")))
      .finally(() => setLoading(false));
  }, [agentId, tr]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const handleToggle = async (job: AgentCronJob, enabled: boolean) => {
    if (!agentId || toggling[job.id]) return;
    setToggling((m) => ({ ...m, [job.id]: true }));
    // Optimistic update — flip immediately, server is authoritative on
    // refresh. Reverts on failure.
    setJobs((prev) =>
      prev.map((j) => (j.id === job.id ? { ...j, enabled } : j)),
    );
    const res = await toggleAgentCronJob(agentId, job.id, enabled);
    setToggling((m) => {
      const { [job.id]: _drop, ...rest } = m;
      void _drop;
      return rest;
    });
    if (res.error || !res.ok) {
      setError(res.error || tr("Failed to update job", "更新定时任务失败"));
      // Revert by refetching the canonical state.
      refresh();
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget || !agentId) return;
    const target = deleteTarget;
    setDeleteTarget(null);
    const res = await deleteAgentCronJob(agentId, target.id);
    if (res.error) setError(res.error);
    refresh();
  };

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <div className="flex items-center gap-2">
            <Clock className="size-5 text-muted-foreground" />
            <h2 className="text-2xl font-semibold tracking-tight">{tr("Scheduler", "定时任务")}</h2>
          </div>
          <p className="text-sm text-muted-foreground mt-1">
            {tr("Scheduled tasks for", "管理")} <strong>{agentName || tr("this agent", "此 Agent")}</strong>{tr(".", " 的定时任务。")}
          </p>
        </div>
      </div>

      {error && (
        <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-4">
          <p className="text-sm text-destructive">{error}</p>
        </div>
      )}

      {loading ? (
        <div className="space-y-2">
          <Skeleton className="h-20" />
          <Skeleton className="h-20" />
          <Skeleton className="h-20" />
        </div>
      ) : jobs.length === 0 ? (
        <div className="rounded-lg border border-dashed border-border bg-card/50 p-10 text-center">
          <Clock className="mx-auto size-8 text-muted-foreground/50 mb-3" />
          <p className="text-sm text-muted-foreground">
            {tr("No scheduled tasks yet.", "还没有定时任务。")}
          </p>
        </div>
      ) : (
        <div className="grid gap-3">
          {jobs.map((job) => (
            <JobRow
              key={job.id}
              job={job}
              busy={!!toggling[job.id]}
              onToggle={(enabled) => handleToggle(job, enabled)}
              onDelete={() => setDeleteTarget(job)}
            />
          ))}
        </div>
      )}

      <AlertDialog
        open={!!deleteTarget}
        onOpenChange={(v) => !v && setDeleteTarget(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tr("Delete scheduled task", "删除定时任务")}</AlertDialogTitle>
            <AlertDialogDescription>
              {tr("Remove", "删除")} <strong>{deleteTarget?.name || deleteTarget?.id}</strong>?
              {tr(" This stops future runs and cannot be undone. Existing chat history is preserved.", " 之后将不再执行，且无法撤销。已有聊天记录会被保留。")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tr("Cancel", "取消")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {tr("Delete", "删除")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function JobRow({
  job,
  busy,
  onToggle,
  onDelete,
}: {
  job: AgentCronJob;
  busy: boolean;
  onToggle: (enabled: boolean) => void;
  onDelete: () => void;
}) {
  const { locale, tr } = useLocale();
  return (
    <div className="rounded-lg border border-border bg-card p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="flex-1 min-w-0 space-y-2">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="font-medium truncate">{job.name || job.id}</span>
            <Badge
              variant="outline"
              className="inline-flex items-center gap-1 text-[10px]"
            >
              {typeIcon(job.type)}
              {job.type}
            </Badge>
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px]">
              {fmtSchedule(job, locale)}
            </code>
            {job.channel && (
              <span className="text-[11px] text-muted-foreground">
                {tr("via {{channel}}", "通过 {{channel}}", { channel: job.channel })}
              </span>
            )}
            {job.chatterId && (
              <span
                className="text-[11px] text-muted-foreground"
                title={tr("User who created this task", "创建此任务的用户")}
              >
                {tr("by {{user}}", "由 {{user}} 创建", { user: job.chatterId })}
              </span>
            )}
          </div>
          <div className="flex items-start gap-1.5 text-xs text-muted-foreground">
            <MessageSquare className="size-3.5 mt-0.5 shrink-0" />
            <span className="break-words">{job.message}</span>
          </div>
          <div className="flex gap-4 text-[11px] text-muted-foreground/80">
            <span>
              {tr("Last run:", "上次执行：")} {" "}
              <span className="font-mono">{fmtRelative(job.lastRun, locale)}</span>
            </span>
            <span>
              {tr("Next run:", "下次执行：")} {" "}
              <span className="font-mono">{fmtRelative(job.nextRun, locale)}</span>
            </span>
          </div>
        </div>
        <div className="flex items-center gap-1 shrink-0">
          <Switch
            checked={job.enabled}
            disabled={busy}
            onCheckedChange={(v) => onToggle(v)}
            aria-label={job.enabled ? tr("Disable", "停用") : tr("Enable", "启用")}
          />
          <Button
            size="icon"
            variant="ghost"
            className="text-destructive hover:text-destructive"
            onClick={onDelete}
            title={tr("Delete", "删除")}
          >
            <Trash2 className="size-4" />
          </Button>
        </div>
      </div>
    </div>
  );
}
