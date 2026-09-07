"use client";

import { useCallback, useEffect, useState } from "react";
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
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Server, Plus, Trash2, Pencil, AlertTriangle } from "lucide-react";
import {
  getAgentConfig,
  getMe,
  updateAgent,
  type MCPServerConfig,
} from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";
import { useLocale } from "@/components/locale-provider";

type MCPEntry = { name: string } & MCPServerConfig;

export default function AgentMCPPage() {
  const { tr } = useLocale();
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);
  const [servers, setServers] = useState<Record<string, MCPServerConfig>>({});
  const [loading, setLoading] = useState(true);
  const [editOpen, setEditOpen] = useState(false);
  const [editEntry, setEditEntry] = useState<MCPEntry | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const [isHosted, setIsHosted] = useState(false);

  const fetchConfig = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    try {
      const [cfg, me] = await Promise.all([
        getAgentConfig(agentId),
        getMe().catch(() => null),
      ]);
      setServers(cfg.mcpServers ?? {});
      if (me?.deployMode === "hosted") setIsHosted(true);
    } finally {
      setLoading(false);
    }
  }, [agentId]);

  useEffect(() => {
    fetchConfig();
  }, [fetchConfig]);

  const saveServers = async (next: Record<string, MCPServerConfig>) => {
    await updateAgent(agentId, { mcpServers: next });
    setServers(next);
  };

  const handleSaveEntry = async (entry: MCPEntry) => {
    const { name, ...cfg } = entry;
    // If editing with a new name, remove old key
    if (editEntry && editEntry.name !== name) {
      const next = { ...servers };
      delete next[editEntry.name];
      next[name] = cfg;
      await saveServers(next);
    } else {
      await saveServers({ ...servers, [name]: cfg });
    }
    setEditOpen(false);
    setEditEntry(null);
  };

  const handleDelete = async (name: string) => {
    const next = { ...servers };
    delete next[name];
    await saveServers(next);
    setDeleteTarget(null);
  };

  if (loading) {
    return (
      <div className="p-6 space-y-6 max-w-5xl mx-auto">
        <Skeleton className="h-10 w-48" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  const entries = Object.entries(servers);
  const hasStdio = entries.some(([, cfg]) => cfg.type === "stdio");

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      {isHosted && hasStdio && (
        <div className="flex items-start gap-2 rounded-lg border border-yellow-500/30 bg-yellow-500/5 p-3 text-sm">
          <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0 text-yellow-500" />
          <div>
            <span className="font-medium">{tr("stdio servers may not work in cloud deployments.", "stdio 服务器可能无法在云部署中工作。")}</span>{" "}
            {tr("stdio MCP servers run as local subprocesses and are not shared across instances. Use the", "stdio MCP 服务器作为本地子进程运行，无法跨实例共享。分布式环境请使用")} {" "}<strong>http</strong>{" "}{tr("type for distributed environments.", "类型。")}
          </div>
        </div>
      )}
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-xl font-semibold">{tr("MCP servers", "MCP 服务器")}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {tr("Connect external tool servers through Model Context Protocol. MCP tools are available to", "通过模型上下文协议连接外部工具服务器。MCP 工具会在每次对话中提供给")} {" "}{agentName || tr("this agent", "此 Agent")}.
          </p>
        </div>
        <Button
          size="sm"
          onClick={() => {
            setEditEntry(null);
            setEditOpen(true);
          }}
        >
          <Plus className="w-4 h-4 mr-1" /> {tr("Add server", "添加服务器")}
        </Button>
      </div>

      {entries.length === 0 ? (
        <div className="rounded-lg border border-dashed p-12 text-center text-muted-foreground">
          <Server className="w-10 h-10 mx-auto mb-3 opacity-40" />
          <p>{tr("No MCP servers configured.", "尚未配置 MCP 服务器。")}</p>
          <p className="text-xs mt-1">
            {tr("Add an MCP server to give this agent external tools.", "添加 MCP 服务器，为此 Agent 扩展外部工具。")}
          </p>
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {entries.map(([name, cfg]) => (
            <div
              key={name}
              className="rounded-lg border bg-card p-4 space-y-2"
            >
              <div className="flex items-start justify-between gap-2">
                <div className="flex items-center gap-2 min-w-0">
                  <Server className="w-4 h-4 shrink-0 text-muted-foreground" />
                  <span className="font-medium truncate">{name}</span>
                </div>
                <Badge variant="secondary" className="shrink-0 text-xs">
                  {cfg.type}
                </Badge>
              </div>
              <div className="text-xs text-muted-foreground truncate">
                {cfg.type === "http"
                  ? cfg.url || tr("(no URL)", "（无 URL）")
                  : [cfg.command, ...(cfg.args ?? [])].join(" ")}
              </div>
              {cfg.env && Object.keys(cfg.env).length > 0 && (
                <div className="text-xs text-muted-foreground">
                  env: {Object.keys(cfg.env).join(", ")}
                </div>
              )}
              {cfg.headers && Object.keys(cfg.headers).length > 0 && (
                <div className="text-xs text-muted-foreground">
                  headers: {Object.keys(cfg.headers).join(", ")}
                </div>
              )}
              <div className="flex gap-1 pt-1">
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7"
                  onClick={() => {
                    setEditEntry({ name, ...cfg });
                    setEditOpen(true);
                  }}
                >
                  <Pencil className="w-3.5 h-3.5" />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7 text-destructive"
                  onClick={() => setDeleteTarget(name)}
                >
                  <Trash2 className="w-3.5 h-3.5" />
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Edit / Add dialog */}
      <MCPEditDialog
        open={editOpen}
        onOpenChange={(o) => {
          if (!o) setEditEntry(null);
          setEditOpen(o);
        }}
        initial={editEntry}
        existingNames={Object.keys(servers)}
        onSave={handleSaveEntry}
      />

      {/* Delete confirmation */}
      <AlertDialog
        open={!!deleteTarget}
        onOpenChange={(o) => !o && setDeleteTarget(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tr("Remove MCP server", "移除 MCP 服务器")}</AlertDialogTitle>
            <AlertDialogDescription>
              {tr("Remove", "从此 Agent 移除")} <strong>{deleteTarget}</strong>?
              {tr(" The server tools will no longer be available.", " 该服务器提供的工具将不再可用。")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tr("Cancel", "取消")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground"
              onClick={() => deleteTarget && handleDelete(deleteTarget)}
            >
              {tr("Remove", "移除")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// ---- Edit / Add dialog ----

function MCPEditDialog({
  open,
  onOpenChange,
  initial,
  existingNames,
  onSave,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  initial: MCPEntry | null;
  existingNames: string[];
  onSave: (entry: MCPEntry) => Promise<void>;
}) {
  const { tr } = useLocale();
  const [name, setName] = useState("");
  const [type, setType] = useState<"http" | "stdio">("stdio");
  const [url, setUrl] = useState("");
  const [command, setCommand] = useState("");
  const [args, setArgs] = useState("");
  const [envText, setEnvText] = useState("");
  const [headersText, setHeadersText] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) return;
    if (initial) {
      setName(initial.name);
      setType(initial.type || "stdio");
      setUrl(initial.url ?? "");
      setCommand(initial.command ?? "");
      setArgs((initial.args ?? []).join(" "));
      setEnvText(kvToText(initial.env));
      setHeadersText(kvToText(initial.headers));
    } else {
      setName("");
      setType("stdio");
      setUrl("");
      setCommand("");
      setArgs("");
      setEnvText("");
      setHeadersText("");
    }
    setError("");
  }, [open, initial]);

  const handleSubmit = async () => {
    const trimName = name.trim();
    if (!trimName) {
      setError(tr("Name is required", "名称不能为空"));
      return;
    }
    if (!initial && existingNames.includes(trimName)) {
      setError(tr("A server with this name already exists", "已存在同名服务器"));
      return;
    }
    if (initial && initial.name !== trimName && existingNames.includes(trimName)) {
      setError(tr("A server with this name already exists", "已存在同名服务器"));
      return;
    }

    const entry: MCPEntry = { name: trimName, type };
    if (type === "http") {
      if (!url.trim()) {
        setError(tr("URL is required for HTTP type", "HTTP 类型必须填写 URL"));
        return;
      }
      entry.url = url.trim();
      const h = textToKV(headersText);
      if (Object.keys(h).length > 0) entry.headers = h;
    } else {
      if (!command.trim()) {
        setError(tr("Command is required for stdio type", "stdio 类型必须填写命令"));
        return;
      }
      entry.command = command.trim();
      const a = args.trim();
      if (a) entry.args = a.split(/\s+/);
      const e = textToKV(envText);
      if (Object.keys(e).length > 0) entry.env = e;
    }

    setSaving(true);
    try {
      await onSave(entry);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : tr("Save failed", "保存失败"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{initial ? tr("Edit MCP server", "编辑 MCP 服务器") : tr("Add MCP server", "添加 MCP 服务器")}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label>{tr("Name", "名称")}</Label>
            <Input
              placeholder={tr("e.g. postgres, filesystem", "例如 postgres、filesystem")}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>

          <div className="space-y-1.5">
            <Label>{tr("Type", "类型")}</Label>
            <Select value={type} onValueChange={(v) => setType(v as "http" | "stdio")}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="stdio">stdio</SelectItem>
                <SelectItem value="http">http</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {type === "http" ? (
            <>
              <div className="space-y-1.5">
                <Label>URL</Label>
                <Input
                  placeholder="https://example.com/mcp"
                  value={url}
                  onChange={(e) => setUrl(e.target.value)}
                />
              </div>
              <div className="space-y-1.5">
                <Label>{tr("Headers", "请求头")} <span className="text-muted-foreground font-normal">{tr("(optional, one KEY=VALUE per line)", "（选填，每行一个 KEY=VALUE）")}</span></Label>
                <textarea
                  className="flex w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 min-h-[60px] resize-y"
                  placeholder={"Authorization=Bearer $TOKEN\nX-Custom=value"}
                  value={headersText}
                  onChange={(e) => setHeadersText(e.target.value)}
                  rows={3}
                />
              </div>
            </>
          ) : (
            <>
              <div className="space-y-1.5">
                <Label>{tr("Command", "命令")}</Label>
                <Input
                  placeholder={tr("e.g. npx, python, node", "例如 npx、python、node")}
                  value={command}
                  onChange={(e) => setCommand(e.target.value)}
                />
              </div>
              <div className="space-y-1.5">
                <Label>{tr("Arguments", "参数")} <span className="text-muted-foreground font-normal">{tr("(space-separated)", "（空格分隔）")}</span></Label>
                <Input
                  placeholder={tr("e.g. -y @anthropic/mcp-server-postgres postgresql://…", "例如 -y @anthropic/mcp-server-postgres postgresql://…")}
                  value={args}
                  onChange={(e) => setArgs(e.target.value)}
                />
              </div>
              <div className="space-y-1.5">
                <Label>{tr("Environment", "环境变量")} <span className="text-muted-foreground font-normal">{tr("(optional, one KEY=VALUE per line)", "（选填，每行一个 KEY=VALUE）")}</span></Label>
                <textarea
                  className="flex w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 min-h-[60px] resize-y"
                  placeholder={"DATABASE_URL=postgresql://...\nAPI_KEY=$SECRET"}
                  value={envText}
                  onChange={(e) => setEnvText(e.target.value)}
                  rows={3}
                />
              </div>
            </>
          )}

          {error && <p className="text-sm text-destructive">{error}</p>}

          <div className="flex justify-end gap-2 pt-2">
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {tr("Cancel", "取消")}
            </Button>
            <Button onClick={handleSubmit} disabled={saving}>
              {saving ? tr("Saving…", "正在保存…") : initial ? tr("Save", "保存") : tr("Add", "添加")}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

// KEY=VALUE text ↔ Record<string, string>
function kvToText(kv?: Record<string, string>): string {
  if (!kv) return "";
  return Object.entries(kv)
    .map(([k, v]) => `${k}=${v}`)
    .join("\n");
}

function textToKV(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const idx = trimmed.indexOf("=");
    if (idx <= 0) continue;
    out[trimmed.slice(0, idx).trim()] = trimmed.slice(idx + 1);
  }
  return out;
}
