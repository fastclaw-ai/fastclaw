"use client";

// Console → Devices: the person's own computers linked through agl
// (docs/agl-machines.md). Agents they chat with — on the web or in an IM
// 1:1 chat — hand work to the coding agents on these computers (Claude
// Code, Codex, …) with run_local_agent. Linking runs one install command
// on the computer; this page shows it and waits for the computer to dial in.

import * as React from "react";
import { ArrowLeft, Bot, CheckCircle2, Copy, Laptop, Loader2, Plus, RefreshCw, Settings, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
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
import { useLocale } from "@/components/locale-provider";
import {
  createMachinePairing,
  deleteMachine,
  getMachineAgentModels,
  getMachinePairingStatus,
  listMachines,
  refreshMachineAgents,
  testMachineAgent,
  updateMachine,
  type Machine,
  type MachineAccess,
  type MachineAgent,
  type MachineAgentModel,
  type MachinePairing,
  type MachinePatch,
} from "@/lib/api";
import { agentIconIsMonoBlack, agentIconNeedsDarkTile, agentIconUrl } from "@/lib/agent-icons";

const REFRESH_MS = 15_000;
const PAIRING_POLL_MS = 2_000;

type Tr = ReturnType<typeof useLocale>["tr"];

function accessLabel(tr: Tr, access: string) {
  return (
    {
      read_only: tr("Read only", "只读"),
      ask: tr("Ask every time", "每次询问"),
      edit: tr("Edit files, ask for commands", "可改文件，命令需确认"),
      full: tr("Full access", "完全信任"),
    } as Record<string, string>
  )[access] ?? access;
}

function lastSeenText(tr: Tr, m: Machine) {
  if (m.online) return tr("Online", "在线");
  if (!m.lastSeenAt) return tr("Offline", "离线");
  const mins = Math.max(0, Math.round((Date.now() - new Date(m.lastSeenAt).getTime()) / 60_000));
  if (mins < 1) return tr("Offline · just now", "离线 · 刚刚在线");
  if (mins < 60) return tr("Offline · {{n}} min ago", "离线 · {{n}} 分钟前在线", { n: mins });
  const hours = Math.round(mins / 60);
  if (hours < 48) return tr("Offline · {{n}} h ago", "离线 · {{n}} 小时前在线", { n: hours });
  return tr("Offline · {{n}} days ago", "离线 · {{n}} 天前在线", { n: Math.round(hours / 24) });
}

export function DevicesSettingsPage() {
  const { tr } = useLocale();
  const [machines, setMachines] = React.useState<Machine[] | null>(null);
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState("");
  const [linking, setLinking] = React.useState(false);
  const [editing, setEditing] = React.useState<Machine | null>(null);
  const [confirmRemove, setConfirmRemove] = React.useState<Machine | null>(null);

  const load = React.useCallback(async () => {
    try {
      setMachines(await listMachines());
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  // Online state changes on the computer's side: re-read now and then,
  // and when the window comes back.
  React.useEffect(() => {
    void load();
    const timer = setInterval(() => {
      if (!document.hidden) void load();
    }, REFRESH_MS);
    const onFocus = () => void load();
    window.addEventListener("focus", onFocus);
    return () => {
      clearInterval(timer);
      window.removeEventListener("focus", onFocus);
    };
  }, [load]);

  const act = async (key: string, run: () => Promise<unknown>) => {
    setBusy(key);
    try {
      await run();
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy("");
    }
  };

  return (
    <div className="mx-auto max-w-5xl p-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <h2 className="text-2xl font-semibold tracking-tight">{tr("Devices", "设备")}</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            {tr(
              "Link your own computer so your agents can hand work to the coding agents on it (Claude Code, Codex, …) when you chat with them — on the web or in a 1:1 IM chat. Only you can use your devices.",
              "连接你自己的电脑，和 Agent 对话时（网页或私聊），它可以把任务交给电脑上的编程助手（Claude Code、Codex 等）完成。只有你本人能使用你的设备。",
            )}
          </p>
        </div>
        <Button onClick={() => setLinking(true)}>
          <Plus className="mr-1 size-4" />
          {tr("Link a computer", "连接电脑")}
        </Button>
      </div>

      {error && <p className="mt-4 text-sm text-destructive">{error}</p>}

      {machines === null ? (
        !error && (
          <div className="mt-6 flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            {tr("Loading…", "加载中…")}
          </div>
        )
      ) : machines.length === 0 ? (
        <div className="mt-6 flex flex-col items-center rounded-lg border border-dashed border-border px-6 py-12 text-center">
          <Laptop className="size-8 text-muted-foreground" />
          <p className="mt-3 text-sm font-medium">{tr("No computer linked yet", "还没有连接电脑")}</p>
          <p className="mt-1 max-w-md text-sm text-muted-foreground">
            {tr(
              "Run one command on your Mac or Linux computer. It installs agl, which keeps the computer linked and runs the coding agents already installed there.",
              "在你的 Mac 或 Linux 电脑上运行一条命令即可。它会安装 agl，由 agl 保持电脑在线，并调用电脑上已经装好的编程助手。",
            )}
          </p>
          <Button className="mt-4" variant="outline" onClick={() => setLinking(true)}>
            {tr("Link a computer", "连接电脑")}
          </Button>
        </div>
      ) : (
        <div className="mt-6 grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {machines.map((m) => {
            const ready = m.agents.filter((a) => a.available && a.enabled);
            return (
              <div
                key={m.id}
                className="group flex min-w-0 flex-col rounded-lg border border-border bg-card p-5 transition-colors hover:bg-muted/50"
              >
                <div className="flex items-start gap-3">
                  <div className="relative mt-0.5 shrink-0">
                    <Laptop className="size-6 text-muted-foreground" />
                    <span
                      className={`absolute -right-0.5 -bottom-0.5 size-2.5 rounded-full ring-2 ring-card ${
                        m.online ? "bg-emerald-500" : "bg-muted-foreground/40"
                      }`}
                    />
                  </div>
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium">{m.name}</p>
                    <p className="mt-0.5 truncate text-xs text-muted-foreground">{lastSeenText(tr, m)}</p>
                  </div>
                  {busy === m.id ? (
                    <Loader2 className="size-4 shrink-0 animate-spin text-muted-foreground" />
                  ) : (
                    <div className="flex shrink-0 items-center gap-0.5 opacity-100 transition-opacity md:opacity-0 md:group-hover:opacity-100 md:group-focus-within:opacity-100">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7 text-muted-foreground hover:text-foreground"
                        onClick={() => setEditing(m)}
                        title={tr("Settings", "设置")}
                        aria-label={tr("Settings", "设置")}
                      >
                        <Settings className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7 text-muted-foreground hover:text-destructive"
                        onClick={() => setConfirmRemove(m)}
                        title={tr("Remove", "移除")}
                        aria-label={tr("Remove", "移除")}
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                  )}
                </div>
                {ready.length === 0 ? (
                  <p className="mt-3 text-sm text-muted-foreground">{tr("No local agents available yet", "暂无可用的本地 Agent")}</p>
                ) : (
                  <div className="mt-3 flex items-center gap-1.5" title={ready.map((a) => a.name || a.id).join(" · ")}>
                    {ready.slice(0, 6).map((a) => (
                      <AgentAvatar key={a.id} agent={a} size="sm" />
                    ))}
                    {ready.length > 6 && <span className="text-xs text-muted-foreground">+{ready.length - 6}</span>}
                  </div>
                )}
                <div className="mt-2 flex flex-wrap items-center gap-1.5 text-[10px]">
                  {m.allowAgents ? (
                    <span className="rounded border border-border px-1.5 py-0.5 text-muted-foreground">
                      {accessLabel(tr, m.defaultAccess)}
                    </span>
                  ) : (
                    <span className="rounded border border-amber-500/40 px-1.5 py-0.5 text-amber-600 dark:text-amber-400">
                      {tr("Agents off", "已停用")}
                    </span>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      )}

      {editing && (
        <DeviceSettingsDialog
          machine={editing}
          onClose={() => setEditing(null)}
          onSave={async (patch) => {
            const updated = await updateMachine(editing.id, patch);
            await load();
            return updated;
          }}
          onRefreshed={() => void load()}
        />
      )}

      {linking && (
        <LinkDialog
          onClose={() => setLinking(false)}
          onLinked={() => {
            void load();
          }}
        />
      )}

      <AlertDialog open={!!confirmRemove} onOpenChange={(v) => !v && setConfirmRemove(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tr("Remove device", "移除设备")}</AlertDialogTitle>
            <AlertDialogDescription>
              {tr(
                "Remove {{name}}? Agents can no longer use it, and agl on that computer stops connecting. To link it again, run a new install command.",
                "要移除 {{name}} 吗？移除后 Agent 无法再使用它，那台电脑上的 agl 也会停止连接。之后要重新连接，需要再运行一次安装命令。",
                { name: confirmRemove?.name || "" },
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tr("Cancel", "取消")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => {
                const m = confirmRemove;
                setConfirmRemove(null);
                if (m) void act(m.id, () => deleteMachine(m.id));
              }}
            >
              {tr("Remove", "移除")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// DeviceSettingsDialog edits one computer: name, whether agents may use
// it, the permissions runs start with, and the folder they start in.
function DeviceSettingsDialog({
  machine: initial,
  onClose,
  onSave,
  onRefreshed,
}: {
  machine: Machine;
  onClose: () => void;
  onSave: (patch: MachinePatch) => Promise<Machine>;
  onRefreshed: () => void;
}) {
  const { tr } = useLocale();
  // The machine as last saved / re-detected while this dialog is open.
  const [machine, setMachine] = React.useState(initial);
  const [editingAgent, setEditingAgent] = React.useState<string | null>(null);
  const [detecting, setDetecting] = React.useState(false);
  const [name, setName] = React.useState(machine.name);
  const [allowAgents, setAllowAgents] = React.useState(machine.allowAgents);
  const [access, setAccess] = React.useState<MachineAccess>(machine.defaultAccess);
  const [cwd, setCwd] = React.useState(machine.defaultCwd);
  const [saving, setSaving] = React.useState(false);
  const [error, setError] = React.useState("");

  const save = async () => {
    const patch: MachinePatch = {};
    const n = name.trim();
    if (n && n !== machine.name) patch.name = n;
    if (allowAgents !== machine.allowAgents) patch.allowAgents = allowAgents;
    if (access !== machine.defaultAccess) patch.defaultAccess = access;
    if (cwd.trim() !== machine.defaultCwd) patch.defaultCwd = cwd.trim();
    if (Object.keys(patch).length === 0) {
      onClose();
      return;
    }
    setSaving(true);
    setError("");
    try {
      await onSave(patch);
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  const details = [
    machine.hostname && machine.hostname !== machine.name ? machine.hostname : "",
    machine.os ? `${machine.os}${machine.arch ? `/${machine.arch}` : ""}` : "",
    machine.version ? `agl ${machine.version}` : "",
  ].filter(Boolean);

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        {editingAgent && machine.agents.some((a) => a.id === editingAgent) ? (
          <LocalAgentEditor
            machine={machine}
            agent={machine.agents.find((a) => a.id === editingAgent)!}
            onBack={() => setEditingAgent(null)}
            onSave={async (setting) => {
              const updated = await onSave({ agents: { [editingAgent]: setting } });
              setMachine(updated);
              setEditingAgent(null);
            }}
          />
        ) : (
        <>
        <DialogHeader>
          <DialogTitle>{tr("Device settings", "设备设置")}</DialogTitle>
          <DialogDescription>
            {lastSeenText(tr, machine)}
            {details.length > 0 ? ` · ${details.join(" · ")}` : ""}
          </DialogDescription>
        </DialogHeader>

        <div className="min-w-0 space-y-4">
          <div className="space-y-1.5">
            <p className="text-sm font-medium">{tr("Name", "名称")}</p>
            <Input value={name} maxLength={64} onChange={(e) => setName(e.target.value)} />
          </div>

          <div className="flex items-center justify-between gap-3">
            <div className="min-w-0">
              <p className="text-sm font-medium">{tr("Let agents use it", "允许 Agent 使用")}</p>
              <p className="text-xs text-muted-foreground">
                {tr("Agents you chat with can hand tasks to this computer.", "和你对话的 Agent 可以把任务交给这台电脑。")}
              </p>
            </div>
            <Switch checked={allowAgents} onCheckedChange={setAllowAgents} />
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="min-w-0 space-y-1.5">
              <p className="text-sm font-medium">{tr("Permissions", "权限")}</p>
              <Select value={access} onValueChange={(v) => v && setAccess(v as MachineAccess)}>
                <SelectTrigger className="w-full">
                  <SelectValue>{(v: unknown) => accessLabel(tr, String(v ?? ""))}</SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {(["read_only", "ask", "edit", "full"] as const).map((a) => (
                    <SelectItem key={a} value={a}>{accessLabel(tr, a)}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="min-w-0 space-y-1.5">
              <p className="text-sm font-medium">{tr("Default folder", "默认目录")}</p>
              <Input
                value={cwd}
                onChange={(e) => setCwd(e.target.value)}
                placeholder={tr("Home folder (~)", "用户主目录（~）")}
                className="font-mono text-sm"
              />
            </div>
          </div>

          <LocalAgentList
            machine={machine}
            detecting={detecting}
            onDetect={async () => {
              setDetecting(true);
              setError("");
              try {
                setMachine(await refreshMachineAgents(machine.id));
                onRefreshed();
              } catch (e) {
                setError(e instanceof Error ? e.message : String(e));
              } finally {
                setDetecting(false);
              }
            }}
            onEdit={(id) => setEditingAgent(id)}
          />

          {error && <p className="text-sm text-destructive">{error}</p>}
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>{tr("Cancel", "取消")}</Button>
          <Button onClick={() => void save()} disabled={saving}>
            {saving && <Loader2 className="mr-1 size-4 animate-spin" />}
            {tr("Save", "保存")}
          </Button>
        </DialogFooter>
        </>
        )}
      </DialogContent>
    </Dialog>
  );
}

// AgentAvatar is an agl agent's icon, or a generic bot.
function AgentAvatar({ agent, size = "md" }: { agent: Pick<MachineAgent, "id" | "name">; size?: "sm" | "md" | "lg" }) {
  const url = agentIconUrl(agent.id);
  const box = size === "sm" ? "size-6 rounded-md" : size === "lg" ? "size-12 rounded-xl" : "size-9 rounded-lg";
  const img = size === "sm" ? "size-4" : size === "lg" ? "size-7" : "size-5";
  return (
    <span className={`inline-flex shrink-0 items-center justify-center border border-border bg-background ${box}`} title={agent.name || agent.id}>
      {url ? (
        // White marks get their own small dark square, like Kimi's logo.
        // eslint-disable-next-line @next/next/no-img-element
        <img
          src={url}
          alt=""
          className={`${img} object-contain ${agentIconNeedsDarkTile(agent.id) ? "rounded-[22%] bg-[#242528] p-[12%]" : ""} ${
            agentIconIsMonoBlack(agent.id) ? "dark:invert" : ""
          }`}
        />
      ) : (
        <Bot className={`${img} text-muted-foreground`} />
      )}
    </span>
  );
}

// LocalAgentList: the computer's local agents as agl detected them —
// installed ones with path and version (Edit), then the missing ones.
function LocalAgentList({
  machine,
  detecting,
  onDetect,
  onEdit,
}: {
  machine: Machine;
  detecting: boolean;
  onDetect: () => void;
  onEdit: (id: string) => void;
}) {
  const { tr } = useLocale();
  const [showMissing, setShowMissing] = React.useState(false);
  const installed = machine.agents.filter((a) => a.available);
  const missing = machine.agents.filter((a) => !a.available);
  return (
    <div className="min-w-0 space-y-2">
      <div className="flex items-center gap-2">
        <p className="text-sm font-medium">{tr("Local agents", "本地 Agent")}</p>
        <span className="text-xs text-muted-foreground">{installed.length}</span>
        <span className="flex-1" />
        <Button
          size="sm"
          variant="outline"
          className="h-7 px-2 text-xs"
          disabled={detecting || !machine.online}
          title={machine.online ? undefined : tr("The computer is offline", "电脑当前离线")}
          onClick={onDetect}
        >
          <RefreshCw className={`mr-1 size-3.5 ${detecting ? "animate-spin" : ""}`} />
          {detecting ? tr("Detecting…", "检测中…") : tr("Detect", "检测")}
        </Button>
      </div>
      {installed.length === 0 ? (
        <p className="text-xs text-muted-foreground">{tr("No local agents found on this computer.", "这台电脑上没有检测到本地 Agent。")}</p>
      ) : (
        <ul className="divide-y divide-border rounded-lg border border-border">
          {installed.map((a) => (
            <li key={a.id} className={`flex min-w-0 items-center gap-3 px-3 py-2.5 ${a.enabled ? "" : "opacity-60"}`}>
              <AgentAvatar agent={a} />
              <div className="min-w-0 flex-1">
                <p className="flex min-w-0 items-center gap-1.5 text-sm font-medium">
                  <span className="truncate">{a.name || a.id}</span>
                  {!a.enabled && (
                    <span className="shrink-0 rounded border border-border px-1 py-px text-[10px] font-normal text-muted-foreground">
                      {tr("Off", "已停用")}
                    </span>
                  )}
                </p>
                <p className="truncate font-mono text-[11px] text-muted-foreground" title={a.path}>
                  {a.path || a.id}
                </p>
              </div>
              {a.version && (
                <span className="hidden max-w-24 shrink-0 truncate font-mono text-[11px] text-muted-foreground sm:inline" title={a.version}>
                  {shortVersion(a.version)}
                </span>
              )}
              <Button size="sm" variant="outline" className="h-7 shrink-0 px-2.5 text-xs" onClick={() => onEdit(a.id)}>
                {tr("Edit", "编辑")}
              </Button>
            </li>
          ))}
        </ul>
      )}
      {missing.length > 0 && (
        <div>
          <button
            type="button"
            className="text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
            onClick={() => setShowMissing((v) => !v)}
          >
            {showMissing
              ? tr("Hide not installed ({{n}})", "收起未安装（{{n}}）", { n: missing.length })
              : tr("Not installed ({{n}})", "未安装（{{n}}）", { n: missing.length })}
          </button>
          {showMissing && (
            <ul className="mt-2 space-y-2">
              {missing.map((a) => (
                <li key={a.id} className="flex min-w-0 items-start gap-3">
                  <span className="opacity-50">
                    <AgentAvatar agent={a} size="sm" />
                  </span>
                  <div className="min-w-0 flex-1">
                    <p className="text-sm text-muted-foreground">
                      {a.name || a.id}
                      {a.status === "desktop-only" && (
                        <span className="ml-1.5 text-[11px]">{tr("(desktop app only, CLI not installed)", "（仅装了桌面版，未装命令行）")}</span>
                      )}
                    </p>
                    {a.installCommand && <CopyLine text={a.installCommand} />}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}

// shortVersion pulls "1.2.3" out of a --version line ("codex-cli 0.166.0").
function shortVersion(v: string) {
  return v.match(/v?\d+(?:\.\d+)+(?:[-+][0-9A-Za-z.-]+)?/)?.[0] ?? v;
}

function CopyLine({ text }: { text: string }) {
  const { tr } = useLocale();
  const [copied, setCopied] = React.useState(false);
  return (
    <div className="mt-1 flex min-w-0 items-center gap-1 rounded-md bg-muted px-2 py-1">
      <code className="min-w-0 flex-1 truncate font-mono text-[11px] select-all" title={text}>{text}</code>
      <button
        type="button"
        className="shrink-0 text-muted-foreground hover:text-foreground"
        aria-label={tr("Copy", "复制")}
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(text);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          } catch {
            // clipboard blocked: the text stays selectable
          }
        }}
      >
        {copied ? <CheckCircle2 className="size-3.5 text-emerald-500" /> : <Copy className="size-3.5" />}
      </button>
    </div>
  );
}

const DEFAULT_OPTION = "__default__";

// LocalAgentEditor: one local agent — whether agents may use it, the
// model and reasoning effort its runs start with, and a test run.
function LocalAgentEditor({
  machine,
  agent,
  onBack,
  onSave,
}: {
  machine: Machine;
  agent: MachineAgent;
  onBack: () => void;
  onSave: (setting: { enabled: boolean; model: string; effort: string }) => Promise<void>;
}) {
  const { tr } = useLocale();
  const [enabled, setEnabled] = React.useState(agent.enabled);
  const [model, setModel] = React.useState(agent.model);
  const [effort, setEffort] = React.useState(agent.effort);
  const [models, setModels] = React.useState<MachineAgentModel[] | null>(null);
  const [modelsError, setModelsError] = React.useState("");
  const [busy, setBusy] = React.useState<"" | "test" | "save">("");
  const [error, setError] = React.useState("");
  const [result, setResult] = React.useState("");
  const hasModels = agent.features.includes("models");
  const hasEffort = agent.features.includes("effort");

  React.useEffect(() => {
    if (!hasModels || !machine.online) return;
    let cancelled = false;
    getMachineAgentModels(machine.id, agent.id)
      .then((list) => !cancelled && setModels(list))
      .catch((e) => !cancelled && setModelsError(e instanceof Error ? e.message : String(e)));
    return () => {
      cancelled = true;
    };
  }, [hasModels, machine.id, machine.online, agent.id]);

  const selectedModel = models?.find((m) => m.id === model) ?? models?.find((m) => m.default);
  const efforts = selectedModel?.efforts ?? [];
  const modelLabel = (id: string) => {
    if (id === DEFAULT_OPTION || id === "") {
      const def = models?.find((m) => m.default);
      return def ? tr("Default ({{m}})", "默认（{{m}}）", { m: def.name || def.id }) : tr("Default", "默认");
    }
    const m = models?.find((x) => x.id === id);
    return m?.name || id;
  };
  const changed = () => {
    setResult("");
    setError("");
  };

  const test = async () => {
    setBusy("test");
    changed();
    try {
      const r = await testMachineAgent(machine.id, agent.id, { model, effort });
      setResult(
        tr("Works · {{s}}s{{m}}", "连接正常 · {{s}} 秒{{m}}", {
          s: (r.durationMs / 1000).toFixed(1),
          m: r.model ? ` · ${r.model}` : "",
        }),
      );
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy("");
    }
  };

  const save = async () => {
    setBusy("save");
    setError("");
    try {
      await onSave({ enabled, model, effort });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setBusy("");
    }
  };

  return (
    <>
      <DialogHeader>
        <DialogTitle className="flex items-center gap-2">
          <button
            type="button"
            className="-ml-1 inline-flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
            aria-label={tr("Back", "返回")}
            onClick={onBack}
          >
            <ArrowLeft className="size-4" />
          </button>
          {tr("Edit local agent", "编辑本地 Agent")}
        </DialogTitle>
        <DialogDescription>{machine.name}</DialogDescription>
      </DialogHeader>

      <div className="min-w-0 space-y-4">
        <div className="flex min-w-0 items-center gap-3">
          <AgentAvatar agent={agent} size="lg" />
          <div className="min-w-0">
            <p className="truncate font-medium">{agent.name || agent.id}</p>
            {agent.version && <p className="truncate font-mono text-xs text-muted-foreground">{agent.version}</p>}
          </div>
        </div>

        <div className="space-y-1.5">
          <p className="text-sm font-medium">{tr("Executable", "可执行文件")}</p>
          <p className="break-all rounded-md bg-muted px-2.5 py-1.5 font-mono text-xs">{agent.path || agent.id}</p>
          <p className="text-xs text-muted-foreground">
            {tr(
              "Detected on the computer. To use another executable or startup arguments, set them in agl on that computer (~/.agl/config.json), then Detect again.",
              "由电脑上的 agl 自动检测。要换可执行文件或加启动参数，请在那台电脑的 agl 配置（~/.agl/config.json）里设置，然后重新检测。",
            )}
          </p>
        </div>

        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0">
            <p className="text-sm font-medium">{tr("Available to agents", "可用")}</p>
            <p className="text-xs text-muted-foreground">
              {tr("Agents you chat with can hand tasks to it.", "和你对话的 Agent 可以把任务交给它。")}
            </p>
          </div>
          <Switch
            checked={enabled}
            onCheckedChange={(v) => {
              setEnabled(v);
              changed();
            }}
          />
        </div>

        {hasModels && (
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="min-w-0 space-y-1.5">
              <p className="text-sm font-medium">{tr("Model", "模型")}</p>
              {models ? (
                <Select
                  value={model || DEFAULT_OPTION}
                  onValueChange={(v) => {
                    const next = !v || v === DEFAULT_OPTION ? "" : String(v);
                    setModel(next);
                    const m = models.find((x) => x.id === next) ?? models.find((x) => x.default);
                    if (effort && !m?.efforts?.includes(effort)) setEffort("");
                    changed();
                  }}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue>{(v: unknown) => modelLabel(String(v ?? ""))}</SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={DEFAULT_OPTION}>{modelLabel(DEFAULT_OPTION)}</SelectItem>
                    {models.map((m) => (
                      <SelectItem key={m.id} value={m.id}>{m.name || m.id}</SelectItem>
                    ))}
                    {model && !models.some((m) => m.id === model) && <SelectItem value={model}>{model}</SelectItem>}
                  </SelectContent>
                </Select>
              ) : (
                <Input
                  value={model}
                  onChange={(e) => {
                    setModel(e.target.value);
                    changed();
                  }}
                  placeholder={modelsError ? tr("Default", "默认") : tr("Loading…", "加载中…")}
                  className="font-mono text-sm"
                />
              )}
            </div>
            {hasEffort && (
              <div className="min-w-0 space-y-1.5">
                <p className="text-sm font-medium">{tr("Reasoning effort", "推理强度")}</p>
                <Select
                  value={effort || DEFAULT_OPTION}
                  onValueChange={(v) => {
                    setEffort(!v || v === DEFAULT_OPTION ? "" : String(v));
                    changed();
                  }}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue>{(v: unknown) => (!v || v === DEFAULT_OPTION ? tr("Default", "默认") : String(v))}</SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={DEFAULT_OPTION}>{tr("Default", "默认")}</SelectItem>
                    {(efforts.length ? efforts : effort ? [effort] : []).map((e) => (
                      <SelectItem key={e} value={e}>{e}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}
          </div>
        )}

        {error && <p className="text-sm text-destructive">{error}</p>}
        {result && (
          <p className="flex items-center gap-1.5 text-sm text-emerald-600 dark:text-emerald-400">
            <CheckCircle2 className="size-4" />
            {result}
          </p>
        )}
        <p className="text-xs text-muted-foreground">
          {tr(
            "Testing sends a short message using this agent's own account on the computer, which may use a little of its quota.",
            "测试会用电脑上这个 Agent 自己的账号发送一条简短消息，可能消耗少量额度。",
          )}
        </p>
      </div>

      <DialogFooter className="sm:justify-between">
        <Button variant="outline" disabled={!!busy || !machine.online} onClick={() => void test()}>
          {busy === "test" && <Loader2 className="mr-1 size-4 animate-spin" />}
          {busy === "test" ? tr("Testing…", "测试中…") : tr("Test", "测试")}
        </Button>
        <div className="flex gap-2">
          <Button variant="ghost" onClick={onBack} disabled={busy === "save"}>{tr("Cancel", "取消")}</Button>
          <Button onClick={() => void save()} disabled={!!busy}>
            {busy === "save" && <Loader2 className="mr-1 size-4 animate-spin" />}
            {tr("Save", "保存")}
          </Button>
        </div>
      </DialogFooter>
    </>
  );
}

// LinkDialog mints a one-time install command and waits until the
// computer that runs it dials in (or the code expires).
function LinkDialog({ onClose, onLinked }: { onClose: () => void; onLinked: () => void }) {
  const { tr } = useLocale();
  const [pairing, setPairing] = React.useState<MachinePairing | null>(null);
  const [status, setStatus] = React.useState<"creating" | "pending" | "connected" | "expired" | "error">("creating");
  const [error, setError] = React.useState("");
  const [copied, setCopied] = React.useState(false);

  const create = React.useCallback(async () => {
    setStatus("creating");
    setError("");
    try {
      setPairing(await createMachinePairing());
      setStatus("pending");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setStatus("error");
    }
  }, []);

  React.useEffect(() => {
    void create();
  }, [create]);

  React.useEffect(() => {
    if (status !== "pending" || !pairing) return;
    let stopped = false;
    const timer = setInterval(async () => {
      try {
        const r = await getMachinePairingStatus(pairing.code);
        if (stopped) return;
        if (r.status === "connected") {
          setStatus("connected");
          onLinked();
        } else if (r.status === "expired") {
          setStatus("expired");
        }
      } catch {
        // transient; keep polling
      }
    }, PAIRING_POLL_MS);
    return () => {
      stopped = true;
      clearInterval(timer);
    };
  }, [status, pairing, onLinked]);

  const copy = async () => {
    if (!pairing) return;
    try {
      await navigator.clipboard.writeText(pairing.installCommand);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // clipboard blocked: the command stays selectable
    }
  };

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{tr("Link a computer", "连接电脑")}</DialogTitle>
          <DialogDescription>
            {tr(
              "Open a terminal on the computer you want to link (macOS or Linux) and run this command. It works once, for 10 minutes.",
              "在要连接的电脑（macOS 或 Linux）上打开终端，运行下面这条命令。命令只能使用一次，10 分钟内有效。",
            )}
          </DialogDescription>
        </DialogHeader>

        {status === "creating" && (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            {tr("Preparing…", "正在生成…")}
          </div>
        )}
        {status === "error" && <p className="text-sm text-destructive">{error}</p>}

        {pairing && (status === "pending" || status === "expired") && (
          <div className="min-w-0 space-y-3">
            <div className="flex min-w-0 items-start gap-2 rounded-md bg-muted p-3">
              <code className="min-w-0 flex-1 break-all font-mono text-xs select-all">{pairing.installCommand}</code>
              <Button size="sm" variant="ghost" className="h-7 shrink-0 px-2" onClick={() => void copy()} aria-label={tr("Copy", "复制")}>
                {copied ? <CheckCircle2 className="size-4 text-emerald-500" /> : <Copy className="size-4" />}
              </Button>
            </div>
            {status === "pending" ? (
              <p className="flex items-center gap-2 text-sm text-muted-foreground">
                <Loader2 className="size-4 animate-spin" />
                {tr("Waiting for the computer to connect…", "等待电脑连接…")}
              </p>
            ) : (
              <p className="text-sm text-amber-600 dark:text-amber-400">
                {tr("This command has expired.", "这条命令已过期。")}
              </p>
            )}
          </div>
        )}

        {status === "connected" && (
          <p className="flex items-center gap-2 text-sm text-emerald-600 dark:text-emerald-400">
            <CheckCircle2 className="size-4" />
            {tr("Linked. Your agents can now use this computer.", "已连接，你的 Agent 现在可以使用这台电脑了。")}
          </p>
        )}

        <DialogFooter>
          {(status === "expired" || status === "error") && (
            <Button variant="outline" onClick={() => void create()}>
              {tr("New command", "重新生成")}
            </Button>
          )}
          <Button variant={status === "connected" ? "default" : "ghost"} onClick={onClose}>
            {status === "connected" ? tr("Done", "完成") : tr("Close", "关闭")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
