"use client";

import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Separator } from "@/components/ui/separator";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Database, Webhook, Save, Check, Container } from "lucide-react";
import { getConfig, updateConfig, type ConfigResponse } from "@/lib/api";

export default function SettingsPage() {
  const [config, setConfig] = useState<ConfigResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  const [storageType, setStorageType] = useState("file");
  const [dsn, setDsn] = useState("");
  const [webhookEnabled, setWebhookEnabled] = useState(false);
  const [webhookToken, setWebhookToken] = useState("");
  const [webhookPath, setWebhookPath] = useState("/hooks");
  const [sandboxEnabled, setSandboxEnabled] = useState(false);
  const [sandboxBackend, setSandboxBackend] = useState("docker");
  const [sandboxImage, setSandboxImage] = useState("");
  const [sandboxE2BKey, setSandboxE2BKey] = useState("");

  useEffect(() => {
    setLoading(true);
    getConfig()
      .then((cfg) => {
        setConfig(cfg);
        setStorageType(cfg.storage?.type || "file");
        setDsn(cfg.storage?.dsn || "");
        setWebhookEnabled(cfg.hooks?.enabled || false);
        setWebhookToken(cfg.hooks?.token || "");
        setWebhookPath(cfg.hooks?.path || "/hooks");
        setSandboxEnabled(cfg.sandbox?.enabled || false);
        setSandboxBackend(cfg.sandbox?.backend || "docker");
        setSandboxImage(cfg.sandbox?.image || "");
        setSandboxE2BKey(cfg.sandbox?.e2bKey || "");
      })
      .catch(() => {})
      .finally(() => setLoading(false));
  }, []);

  const handleSave = async () => {
    setSaving(true);
    await updateConfig({
      storage: { type: storageType, dsn },
      hooks: {
        enabled: webhookEnabled,
        token: webhookToken,
        path: webhookPath,
      },
      sandbox: {
        enabled: sandboxEnabled,
        backend: sandboxBackend,
        image: sandboxImage || undefined,
        e2bKey: sandboxE2BKey || undefined,
      },
    });
    setSaving(false);
    setSaved(true);
    setTimeout(() => setSaved(false), 2000);
  };

  if (loading) {
    return (
      <div className="p-6 space-y-6 max-w-3xl mx-auto">
        <Skeleton className="h-10 w-48" />
        <Skeleton className="h-64 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6 max-w-3xl mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">Settings</h2>
          <p className="text-sm text-muted-foreground mt-1">
            Gateway configuration
          </p>
        </div>
        <Button
          onClick={handleSave}
          disabled={saving}
          variant={saved ? "outline" : "default"}
          className={saved ? "border-emerald-500/30 text-emerald-600 dark:text-emerald-400" : ""}
        >
          {saved ? (
            <>
              <Check className="h-4 w-4 mr-2" />
              Saved
            </>
          ) : (
            <>
              <Save className="h-4 w-4 mr-2" />
              {saving ? "Saving..." : "Save Settings"}
            </>
          )}
        </Button>
      </div>

      {/* Storage Config */}
      <div className="rounded-lg border border-border bg-card">
        <div className="p-5 pb-3">
          <div className="flex items-center gap-2 mb-1">
            <Database className="h-4 w-4 text-blue-500" />
            <h3 className="font-medium">Storage</h3>
          </div>
          <p className="text-sm text-muted-foreground">
            Configure data persistence backend
          </p>
        </div>
        <div className="px-5 pb-5 space-y-4">
          <div className="space-y-2">
            <Label>Storage Type</Label>
            <Select value={storageType} onValueChange={(v) => v && setStorageType(v)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="file">File System</SelectItem>
                <SelectItem value="sqlite">SQLite</SelectItem>
                <SelectItem value="postgres">PostgreSQL</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {storageType !== "file" && (
            <div className="space-y-2">
              <Label>Connection String (DSN)</Label>
              <Input
                value={dsn}
                onChange={(e) => setDsn(e.target.value)}
                placeholder={storageType === "sqlite" ? "./data.db" : "postgres://user:pass@host:5432/fastclaw"}
                className="font-mono text-sm"
              />
            </div>
          )}
        </div>
      </div>

      {/* Webhook Config */}
      <div className="rounded-lg border border-border bg-card">
        <div className="p-5">
          <div className="flex items-center justify-between">
            <div>
              <div className="flex items-center gap-2 mb-1">
                <Webhook className="h-4 w-4 text-cyan-500" />
                <h3 className="font-medium">Webhooks</h3>
              </div>
              <p className="text-sm text-muted-foreground">
                HTTP webhook ingress for external integrations
              </p>
            </div>
            <Switch
              checked={webhookEnabled}
              onCheckedChange={setWebhookEnabled}
            />
          </div>
        </div>
        {webhookEnabled && (
          <div className="px-5 pb-5 space-y-4">
            <Separator />
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label>Webhook Path</Label>
                <Input
                  value={webhookPath}
                  onChange={(e) => setWebhookPath(e.target.value)}
                  placeholder="/hooks"
                  className="font-mono text-sm"
                />
              </div>
              <div className="space-y-2">
                <Label>Bearer Token</Label>
                <Input
                  type="password"
                  value={webhookToken}
                  onChange={(e) => setWebhookToken(e.target.value)}
                  placeholder="secret-token"
                  className="font-mono text-sm"
                />
              </div>
            </div>
          </div>
        )}
      </div>

      {/* Sandbox Config */}
      <div className="rounded-lg border border-border bg-card">
        <div className="p-5">
          <div className="flex items-center justify-between">
            <div>
              <div className="flex items-center gap-2 mb-1">
                <Container className="h-4 w-4 text-purple-500" />
                <h3 className="font-medium">Sandbox</h3>
              </div>
              <p className="text-sm text-muted-foreground">
                Execute code in isolated sandbox environments
              </p>
            </div>
            <Switch
              checked={sandboxEnabled}
              onCheckedChange={setSandboxEnabled}
            />
          </div>
        </div>
        {sandboxEnabled && (
          <div className="px-5 pb-5 space-y-4">
            <Separator />
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label>Backend</Label>
                <Select value={sandboxBackend} onValueChange={(v) => v && setSandboxBackend(v)}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="docker">Docker</SelectItem>
                    <SelectItem value="e2b">E2B (cloud)</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              {sandboxBackend === "e2b" ? (
                <div className="space-y-2">
                  <Label>E2B API Key</Label>
                  <Input
                    type="password"
                    value={sandboxE2BKey}
                    onChange={(e) => setSandboxE2BKey(e.target.value)}
                    placeholder="e2b_..."
                    className="font-mono text-sm"
                  />
                </div>
              ) : (
                <div className="space-y-2">
                  <Label>Docker Image</Label>
                  <Input
                    value={sandboxImage}
                    onChange={(e) => setSandboxImage(e.target.value)}
                    placeholder="python:3.12-slim"
                    className="font-mono text-sm"
                  />
                </div>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
