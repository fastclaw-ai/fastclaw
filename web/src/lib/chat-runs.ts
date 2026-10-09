"use client";

import { useMemo, useSyncExternalStore } from "react";
import { apiFetch } from "./api";

export type ChatRunStatus = "running" | "completed" | "stopped" | "failed";
interface ChatRun { controller: AbortController; status: ChatRunStatus }
// Transport ownership outlives the selected page. Keys include BOTH identities.
const runs = new Map<string, ChatRun>();
const listeners = new Set<() => void>();
const key = (agentId: string, sessionId: string) => JSON.stringify([agentId, sessionId]);
const notify = () => listeners.forEach((listener) => listener());
const subscribe = (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; };
export function beginChatRun(agentId: string, sessionId: string) {
  const id = key(agentId, sessionId);
  if (runs.get(id)?.status === "running") return null;
  const controller = new AbortController();
  runs.set(id, { controller, status: "running" });
  notify();
  return controller;
}
export function finishChatRun(agentId: string, sessionId: string, controller: AbortController, status: ChatRunStatus) {
  const id = key(agentId, sessionId);
  if (runs.get(id)?.controller !== controller) return;
  runs.set(id, { controller, status });
  notify();
}
export function stopChatRun(agentId: string, sessionId: string) {
  runs.get(key(agentId, sessionId))?.controller.abort();
  void apiFetch(`/api/chat/stop?${new URLSearchParams({ agentId, sessionId })}`, { method: "POST" }).catch(() => {});
}
export function useChatRunStatus(agentId: string, sessionId: string) {
  return useSyncExternalStore(subscribe, () => runs.get(key(agentId, sessionId))?.status, () => undefined);
}
// Agents with a run in flight from this tab, as a stable string snapshot
// (useSyncExternalStore compares snapshots by value) — the chat list
// shows them as working before the server's session status catches up.
const runningAgentsSnapshot = () =>
  [...new Set([...runs].filter(([, run]) => run.status === "running").map(([id]) => (JSON.parse(id) as [string, string])[0]))]
    .sort()
    .join("\n");
export function useRunningAgentIds(): ReadonlySet<string> {
  const snapshot = useSyncExternalStore(subscribe, runningAgentsSnapshot, () => "");
  return useMemo(() => new Set(snapshot ? snapshot.split("\n") : []), [snapshot]);
}
