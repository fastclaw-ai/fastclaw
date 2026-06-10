"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { PencilIcon, PlusIcon, ServerIcon, Trash2Icon } from "lucide-react";

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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import type { MCPServer, MCPServerInput, MCPServerType } from "@/lib/api";

type Pair = { key: string; value: string };

type WriteResult = { ok: boolean; error?: string };

export interface MCPManagerProps {
  // Short noun phrase describing who inherits these servers, e.g.
  // "this agent" or "all agents (system-wide)".
  scopeLabel: string;
  // Optional extra note rendered under the intro paragraph.
  scopeNote?: string;
  // Whether the caller may mutate. When false, add/edit/delete are hidden.
  readOnly?: boolean;
  list: () => Promise<MCPServer[]>;
  create: (input: MCPServerInput) => Promise<WriteResult>;
  update: (name: string, input: MCPServerInput) => Promise<WriteResult>;
  remove: (name: string) => Promise<WriteResult>;
}

export function MCPManager({
  scopeLabel,
  scopeNote,
  readOnly = false,
  list,
  create,
  update,
  remove,
}: MCPManagerProps) {
  const [servers, setServers] = useState<MCPServer[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<MCPServer | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<MCPServer | null>(null);

  const refresh = useCallback(() => {
    setLoading(true);
    setError("");
    list()
      .then((servers) => setServers(servers))
      .catch((e) => setError(e instanceof Error ? e.message : "Failed to load MCP servers"))
      .finally(() => setLoading(false));
  }, [list]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const openCreate = () => {
    setEditing(null);
    setDialogOpen(true);
  };

  const openEdit = (server: MCPServer) => {
    setEditing(server);
    setDialogOpen(true);
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    const target = deleteTarget;
    setDeleteTarget(null);
    setError("");
    try {
      const res = await remove(target.name);
      if (res.error) setError(res.error);
      refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to delete MCP server");
    }
  };

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between gap-4">
        <div>
          <div className="flex items-center gap-2">
            <ServerIcon className="size-5 text-muted-foreground" />
            <h2 className="text-2xl font-semibold tracking-tight">MCP Servers</h2>
          </div>
          <p className="text-sm text-muted-foreground mt-1">
            Add MCP servers to expose external tools to <strong>{scopeLabel}</strong>. Tools appear
            as <code className="rounded bg-muted px-1 py-0.5">mcp_&lt;server&gt;_&lt;tool&gt;</code>.
          </p>
          {scopeNote && <p className="text-sm text-muted-foreground mt-1">{scopeNote}</p>}
        </div>
        {!readOnly && (
          <Button onClick={openCreate}>
            <PlusIcon className="mr-2 size-4" />
            Add MCP Server
          </Button>
        )}
      </div>

      <div className="rounded-lg border bg-muted/30 p-4 text-sm text-muted-foreground">
        HTTP MCP is the safest option for hosted services. stdio MCP starts a local process and is
        only available on self-hosted deployments.
      </div>

      {error && (
        <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-4">
          <p className="text-sm text-destructive">{error}</p>
        </div>
      )}

      {loading ? (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          <Skeleton className="h-40" />
          <Skeleton className="h-40" />
          <Skeleton className="h-40" />
        </div>
      ) : servers.length === 0 ? (
        <div className="rounded-lg border border-dashed p-8 text-center">
          <ServerIcon className="mx-auto size-8 text-muted-foreground" />
          <h3 className="mt-3 text-base font-medium">No MCP servers configured</h3>
          <p className="mt-1 text-sm text-muted-foreground">
            Add an MCP server to make external tools available.
          </p>
          {!readOnly && (
            <Button className="mt-4" onClick={openCreate}>
              <PlusIcon className="mr-2 size-4" />
              Add MCP Server
            </Button>
          )}
        </div>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {servers.map((server) => (
            <ServerCard
              key={server.name}
              server={server}
              readOnly={readOnly}
              onEdit={() => openEdit(server)}
              onDelete={() => setDeleteTarget(server)}
            />
          ))}
        </div>
      )}

      <MCPServerDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        server={editing}
        create={create}
        update={update}
        onSaved={refresh}
      />

      <AlertDialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete MCP server?</AlertDialogTitle>
            <AlertDialogDescription>
              This removes <strong>{deleteTarget?.name}</strong>. Existing chats will lose access to
              tools provided by that server after the agent reloads.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={handleDelete}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function ServerCard({
  server,
  readOnly,
  onEdit,
  onDelete,
}: {
  server: MCPServer;
  readOnly: boolean;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const detail =
    server.type === "http" ? server.url : [server.command, ...(server.args || [])].join(" ");

  return (
    <div className="rounded-lg border bg-card p-4 shadow-sm">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h3 className="truncate font-medium">{server.name}</h3>
            <Badge variant="secondary">{server.type}</Badge>
          </div>
          <Badge className="mt-2" variant={server.enabled ? "default" : "outline"}>
            {server.enabled ? "Enabled" : "Disabled"}
          </Badge>
        </div>
        {!readOnly && (
          <div className="flex gap-1">
            <Button variant="ghost" size="icon" onClick={onEdit} aria-label={`Edit ${server.name}`}>
              <PencilIcon className="size-4" />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              onClick={onDelete}
              aria-label={`Delete ${server.name}`}
            >
              <Trash2Icon className="size-4 text-destructive" />
            </Button>
          </div>
        )}
      </div>
      <p className="mt-4 line-clamp-2 break-all text-sm text-muted-foreground">
        {detail || "No connection details"}
      </p>
      {server.updatedAt && (
        <p className="mt-3 text-xs text-muted-foreground">
          Updated {new Date(server.updatedAt).toLocaleString()}
        </p>
      )}
    </div>
  );
}

function MCPServerDialog({
  open,
  onOpenChange,
  server,
  create,
  update,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  server: MCPServer | null;
  create: (input: MCPServerInput) => Promise<WriteResult>;
  update: (name: string, input: MCPServerInput) => Promise<WriteResult>;
  onSaved: () => void;
}) {
  const editing = !!server;
  const [name, setName] = useState("");
  const [type, setType] = useState<MCPServerType>("http");
  const [enabled, setEnabled] = useState(true);
  const [url, setURL] = useState("");
  const [command, setCommand] = useState("");
  const [argsText, setArgsText] = useState("");
  const [headers, setHeaders] = useState<Pair[]>([]);
  const [env, setEnv] = useState<Pair[]>([]);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) return;
    setName(server?.name || "");
    setType(server?.type || "http");
    setEnabled(server?.enabled ?? true);
    setURL(server?.url || "");
    setCommand(server?.command || "");
    setArgsText((server?.args || []).join("\n"));
    setHeaders(recordToPairs(server?.headers));
    setEnv(recordToPairs(server?.env));
    setSaving(false);
    setError("");
  }, [open, server]);

  const canSubmit = useMemo(() => {
    if (!name.trim()) return false;
    if (type === "http") return !!url.trim();
    return !!command.trim();
  }, [command, name, type, url]);

  const submit = async () => {
    if (!canSubmit) return;
    setSaving(true);
    setError("");
    const input: MCPServerInput =
      type === "http"
        ? {
            name: name.trim(),
            type,
            enabled,
            url: url.trim(),
            headers: pairsToRecord(headers),
          }
        : {
            name: name.trim(),
            type,
            enabled,
            command: command.trim(),
            args: argsText
              .split("\n")
              .map((arg) => arg.trim())
              .filter(Boolean),
            env: pairsToRecord(env),
          };
    try {
      const res = server ? await update(server.name, input) : await create(input);
      if (res.error) {
        setError(res.error);
        return;
      }
      onOpenChange(false);
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save MCP server");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{editing ? "Edit MCP server" : "Add MCP server"}</DialogTitle>
          <DialogDescription>
            Configure an MCP server. Secret-looking values are masked when loaded and preserved if
            saved unchanged.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {error && (
            <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
              {error}
            </div>
          )}

          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="mcp-name">Name</Label>
              <Input
                id="mcp-name"
                value={name}
                disabled={editing}
                placeholder="github"
                onChange={(e) => setName(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Letters, numbers, and underscore only.
              </p>
            </div>
            <div className="space-y-2">
              <Label>Type</Label>
              <Select
                value={type}
                onValueChange={(value) => setType(value as MCPServerType)}
                disabled={editing}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="http">HTTP</SelectItem>
                  <SelectItem value="stdio">stdio</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="flex items-center justify-between rounded-lg border p-3">
            <div>
              <Label>Enabled</Label>
              <p className="text-xs text-muted-foreground">
                Disabled servers stay saved but are not loaded.
              </p>
            </div>
            <Switch checked={enabled} onCheckedChange={setEnabled} />
          </div>

          {type === "http" ? (
            <div className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="mcp-url">URL</Label>
                <Input
                  id="mcp-url"
                  value={url}
                  placeholder="https://example.com/mcp"
                  onChange={(e) => setURL(e.target.value)}
                />
              </div>
              <PairEditor
                label="Headers"
                pairs={headers}
                onChange={setHeaders}
                secretPlaceholder="Authorization"
              />
            </div>
          ) : (
            <div className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="mcp-command">Command</Label>
                <Input
                  id="mcp-command"
                  value={command}
                  placeholder="npx"
                  onChange={(e) => setCommand(e.target.value)}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="mcp-args">Args</Label>
                <Textarea
                  id="mcp-args"
                  value={argsText}
                  placeholder={"-y\n@modelcontextprotocol/server-filesystem\n/workspace"}
                  onChange={(e) => setArgsText(e.target.value)}
                />
                <p className="text-xs text-muted-foreground">One argument per line.</p>
              </div>
              <PairEditor
                label="Environment"
                pairs={env}
                onChange={setEnv}
                secretPlaceholder="API_TOKEN"
              />
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={saving}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={!canSubmit || saving}>
            {saving ? "Saving..." : "Save"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function PairEditor({
  label,
  pairs,
  onChange,
  secretPlaceholder,
}: {
  label: string;
  pairs: Pair[];
  onChange: (pairs: Pair[]) => void;
  secretPlaceholder: string;
}) {
  const setPair = (index: number, patch: Partial<Pair>) => {
    onChange(pairs.map((pair, i) => (i === index ? { ...pair, ...patch } : pair)));
  };

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <Label>{label}</Label>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => onChange([...pairs, { key: "", value: "" }])}
        >
          Add
        </Button>
      </div>
      {pairs.length === 0 ? (
        <p className="rounded-md border border-dashed p-3 text-sm text-muted-foreground">
          No {label.toLowerCase()} configured.
        </p>
      ) : (
        <div className="space-y-2">
          {pairs.map((pair, index) => (
            <div key={index} className="grid grid-cols-[1fr_1fr_auto] gap-2">
              <Input
                value={pair.key}
                placeholder={secretPlaceholder}
                onChange={(e) => setPair(index, { key: e.target.value })}
              />
              <Input
                value={pair.value}
                placeholder="value"
                onChange={(e) => setPair(index, { value: e.target.value })}
              />
              <Button
                type="button"
                variant="ghost"
                size="icon"
                onClick={() => onChange(pairs.filter((_, i) => i !== index))}
              >
                <Trash2Icon className="size-4" />
              </Button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function recordToPairs(record?: Record<string, string>): Pair[] {
  return Object.entries(record || {}).map(([key, value]) => ({ key, value }));
}

function pairsToRecord(pairs: Pair[]): Record<string, string> | undefined {
  const out: Record<string, string> = {};
  for (const pair of pairs) {
    const key = pair.key.trim();
    if (!key) continue;
    out[key] = pair.value;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}
