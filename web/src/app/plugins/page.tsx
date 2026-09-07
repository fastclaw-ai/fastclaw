"use client";

import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Puzzle, Download, Settings } from "lucide-react";
import { getPlugins, updatePlugin, type PluginInfo } from "@/lib/api";
import { useLocale } from "@/components/locale-provider";

export default function PluginsPage() {
  const { tr } = useLocale();
  const [plugins, setPlugins] = useState<PluginInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [editPlugin, setEditPlugin] = useState<PluginInfo | null>(null);
  const [configJson, setConfigJson] = useState("");
  const [saving, setSaving] = useState(false);

  const fetchPlugins = () => {
    setLoading(true);
    getPlugins()
      .then(setPlugins)
      .catch(() => setPlugins([]))
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    fetchPlugins();
  }, []);

  const handleToggle = async (plugin: PluginInfo) => {
    await updatePlugin(plugin.id, { enabled: !plugin.enabled });
    fetchPlugins();
  };

  const handleOpenConfig = (plugin: PluginInfo) => {
    setEditPlugin(plugin);
    setConfigJson(JSON.stringify(plugin.config || {}, null, 2));
  };

  const handleSaveConfig = async () => {
    if (!editPlugin) return;
    setSaving(true);
    try {
      const config = JSON.parse(configJson);
      await updatePlugin(editPlugin.id, { config });
    } catch {
      // invalid JSON
    }
    setSaving(false);
    setEditPlugin(null);
    fetchPlugins();
  };

  const statusColor = (status: string) => {
    if (status === "running") return "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/20";
    if (status === "stopped") return "bg-muted text-muted-foreground border-border";
    return "bg-amber-500/10 text-amber-600 dark:text-amber-400 border-amber-500/20";
  };

  const typeColor = (type: string) => {
    const colors: Record<string, string> = {
      channel: "bg-blue-500/10 text-blue-600 dark:text-blue-400 border-blue-500/20",
      tool: "bg-violet-500/10 text-violet-600 dark:text-violet-400 border-violet-500/20",
      provider: "bg-amber-500/10 text-amber-600 dark:text-amber-400 border-amber-500/20",
      hook: "bg-cyan-500/10 text-cyan-600 dark:text-cyan-400 border-cyan-500/20",
    };
    return colors[type] || "bg-muted text-muted-foreground border-border";
  };

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">{tr("Plugins", "插件")}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {tr("Extend FastClaw with custom plugins", "使用自定义插件扩展 FastClaw")}
          </p>
        </div>
        <Button variant="outline">
          <Download className="h-4 w-4 mr-2" />
          {tr("Install Plugin", "安装插件")}
        </Button>
      </div>

      <div className="rounded-lg border border-border bg-card">
        {loading ? (
          <div className="p-6 space-y-3">
            {[1, 2].map((i) => (
              <Skeleton key={i} className="h-14 w-full" />
            ))}
          </div>
        ) : plugins.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-16 text-center">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-primary/10 mb-4">
              <Puzzle className="h-7 w-7 text-primary" />
            </div>
            <p className="text-sm text-muted-foreground">{tr("No plugins installed", "还没有安装插件")}</p>
            <p className="text-xs text-muted-foreground/60 mt-1">
              {tr("Plugins add channels, tools, and providers", "插件可以添加渠道、工具和模型提供商")}
            </p>
          </div>
        ) : (
          <div className="overflow-x-auto -mx-6 px-6">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>{tr("Plugin", "插件")}</TableHead>
                <TableHead>{tr("Type", "类型")}</TableHead>
                <TableHead>{tr("Version", "版本")}</TableHead>
                <TableHead>{tr("Status", "状态")}</TableHead>
                <TableHead>{tr("Enabled", "已启用")}</TableHead>
                <TableHead className="text-right">{tr("Config", "配置")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {plugins.map((plugin) => (
                <TableRow
                  key={plugin.id}
                  className="hover:bg-muted/50 transition-colors"
                >
                  <TableCell>
                    <div className="flex items-center gap-3">
                      <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10">
                        <Puzzle className="h-4 w-4 text-primary" />
                      </div>
                      <span className="font-medium">{plugin.id}</span>
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline" className={typeColor(plugin.type)}>
                      {({ channel: tr("Channel", "渠道"), tool: tr("Tool", "工具"), provider: tr("Provider", "提供商"), hook: "Hook" } as Record<string, string>)[plugin.type] || plugin.type}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <code className="font-mono text-xs text-muted-foreground">
                      {plugin.version || "-"}
                    </code>
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline" className={statusColor(plugin.status)}>
                      {({ running: tr("Running", "运行中"), stopped: tr("Stopped", "已停止") } as Record<string, string>)[plugin.status] || plugin.status}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <Switch
                      checked={plugin.enabled}
                      onCheckedChange={() => handleToggle(plugin)}
                    />
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 text-muted-foreground hover:text-foreground"
                      onClick={() => handleOpenConfig(plugin)}
                    >
                      <Settings className="h-3.5 w-3.5" />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          </div>
        )}
      </div>

      {/* Config Editor Dialog */}
      <Dialog open={!!editPlugin} onOpenChange={() => setEditPlugin(null)}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Puzzle className="h-5 w-5 text-primary" />
              {editPlugin?.id} {tr("Configuration", "配置")}
            </DialogTitle>
            <DialogDescription>
              {tr("Edit plugin configuration as JSON", "以 JSON 格式编辑插件配置")}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label>{tr("Config JSON", "配置 JSON")}</Label>
            <Textarea
              value={configJson}
              onChange={(e) => setConfigJson(e.target.value)}
              rows={12}
              className="font-mono text-sm resize-none"
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setEditPlugin(null)}>
              {tr("Cancel", "取消")}
            </Button>
            <Button onClick={handleSaveConfig} disabled={saving}>
              {saving ? tr("Saving…", "正在保存…") : tr("Save Config", "保存配置")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
