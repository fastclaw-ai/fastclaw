"use client";

// The card a web chat shows while run_local_agent works on the person's
// own computer (console → Devices): what the coding agent is doing, and —
// when it stops to ask — the approval buttons. The server streams
// "local_agent" events (internal/agent/local_agents.go); a page opened
// mid-run re-reads the open request with getLocalAgentApproval.

import * as React from "react";
import { Laptop, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useLocale } from "@/components/locale-provider";
import { decideLocalAgentApproval, type ChatStreamEvent } from "@/lib/api";

export interface LocalAgentRun {
  agent: string;
  machine: string;
  text?: string;
  // What the coding agent asks permission for, while it waits.
  approval?: string;
  // One-line outcome of the last approval (timeout, failure).
  note?: string;
}

type Data = ChatStreamEvent["data"];

// applyLocalAgentEvent folds one "local_agent" event into the run state.
export function applyLocalAgentEvent(prev: LocalAgentRun | null, d: Data): LocalAgentRun | null {
  if (!d?.kind) return prev;
  if (d.kind === "done") return null;
  const base: LocalAgentRun = {
    ...(prev ?? {}),
    agent: d.agent || prev?.agent || "",
    machine: d.machine || prev?.machine || "",
  };
  switch (d.kind) {
    case "progress":
      return { ...base, text: d.text || base.text, note: undefined };
    case "approval":
      return { ...base, approval: d.text || "", note: undefined };
    case "approval_resolved":
      return {
        ...base,
        approval: undefined,
        note: d.decision === "timeout" ? "timeout" : d.decision === "failed" ? `failed:${d.error || ""}` : undefined,
      };
  }
  return prev;
}

export function LocalAgentRunCard({
  run,
  agentId,
  sessionId,
  onDecided,
}: {
  run: LocalAgentRun;
  agentId: string;
  sessionId: string;
  onDecided: () => void;
}) {
  const { tr } = useLocale();
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");

  const decide = async (decision: "allow" | "allow_session" | "deny") => {
    setBusy(true);
    setError("");
    try {
      await decideLocalAgentApproval(agentId, sessionId, decision);
      onDecided();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const who = run.machine
    ? tr("{{agent}} on {{machine}}", "{{machine}} 上的 {{agent}}", { agent: run.agent, machine: run.machine })
    : run.agent;

  return (
    <div className="flex justify-start">
      <div className="w-full max-w-xl min-w-0 rounded-xl border border-border bg-card p-4 text-sm">
        <div className="flex items-center gap-2">
          <Laptop className="size-4 shrink-0 text-muted-foreground" />
          <span className="min-w-0 truncate font-medium">{who}</span>
          {!run.approval && <Loader2 className="ml-auto size-3.5 shrink-0 animate-spin text-muted-foreground" />}
        </div>
        {run.approval ? (
          <>
            <p className="mt-2 text-muted-foreground">{tr("Needs your approval:", "需要你确认：")}</p>
            <p className="mt-1 break-words rounded-md bg-muted px-2.5 py-1.5 font-mono text-xs">{run.approval}</p>
            <div className="mt-3 flex flex-wrap gap-2">
              <Button size="sm" disabled={busy} onClick={() => void decide("allow")}>
                {tr("Allow", "允许")}
              </Button>
              <Button size="sm" variant="outline" disabled={busy} onClick={() => void decide("allow_session")}>
                {tr("Allow for this session", "本会话都允许")}
              </Button>
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => void decide("deny")}>
                {tr("Deny", "拒绝")}
              </Button>
            </div>
          </>
        ) : (
          <p className="mt-1.5 break-words text-muted-foreground">
            {run.text || tr("Working on your computer…", "正在你的电脑上处理…")}
          </p>
        )}
        {run.note === "timeout" && (
          <p className="mt-2 text-xs text-amber-600 dark:text-amber-400">
            {tr("The approval timed out and was denied.", "确认已超时，已自动拒绝。")}
          </p>
        )}
        {run.note?.startsWith("failed:") && (
          <p className="mt-2 text-xs text-destructive">
            {tr("Couldn't send your answer to the computer: {{error}}", "无法把你的确认发送到电脑：{{error}}", { error: run.note.slice(7) })}
          </p>
        )}
        {error && <p className="mt-2 text-xs text-destructive">{error}</p>}
      </div>
    </div>
  );
}
