"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
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
import { Save, Check, Clock, Container } from "lucide-react";
import { getConfig, updateConfig, getMe, type ConfigResponse } from "@/lib/api";
import { useLocale } from "@/components/locale-provider";

export default function RuntimeSettingsPage() {
  const { tr } = useLocale();
  const router = useRouter();
  const [config, setConfig] = useState<ConfigResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [saveError, setSaveError] = useState("");

  const [sandboxEnabled, setSandboxEnabled] = useState(false);
  const [sandboxEnforce, setSandboxEnforce] = useState(false);
  const [sandboxBackend, setSandboxBackend] = useState("docker");
  const [sandboxDockerImage, setSandboxDockerImage] = useState("");
  const [sandboxE2BTemplate, setSandboxE2BTemplate] = useState("base");
  const [sandboxE2BKey, setSandboxE2BKey] = useState("");
  const [sandboxBoxliteImage, setSandboxBoxliteImage] = useState("");
  const [sandboxBoxliteKey, setSandboxBoxliteKey] = useState("");
  const [sandboxBoxliteURL, setSandboxBoxliteURL] = useState("");
  const [defaultTimezone, setDefaultTimezone] = useState("");

  useEffect(() => {
    // Belt-and-suspenders gate: the layout already hides the nav item,
    // but a direct URL hit needs to bounce too.
    getMe().then((m) => {
      if (m?.user?.role !== "super_admin") {
        router.replace("/settings/general");
        return;
      }
      setLoading(true);
      // Explicit system scope: this page edits deployment-wide settings,
      // and the default (merged) view lets a user-level row shadow them.
      getConfig("system")
        .then((cfg) => {
          setConfig(cfg);
          setSandboxEnabled(cfg.sandbox?.enabled || false);
          setSandboxEnforce(cfg.sandbox?.enforce || false);
          const backend = cfg.sandbox?.backend || "docker";
          setSandboxBackend(backend);
          // Each backend has its own persisted field. For configs
          // predating the split there's only the legacy `image` slot,
          // so we migrate it into the backend it belonged to (the saved
          // `backend`) and leave the other two empty.
          const savedImage = cfg.sandbox?.image || "";
          setSandboxDockerImage(
            cfg.sandbox?.dockerImage ?? (backend === "docker" ? savedImage : ""),
          );
          setSandboxE2BTemplate(
            cfg.sandbox?.e2bTemplate ?? (backend === "e2b" ? savedImage || "base" : "base"),
          );
          setSandboxBoxliteImage(
            cfg.sandbox?.boxliteSnapshot ?? (backend === "boxlite" ? savedImage : ""),
          );
          setSandboxE2BKey(cfg.sandbox?.e2bKey || "");
          setSandboxBoxliteKey(cfg.sandbox?.boxliteKey || "");
          setSandboxBoxliteURL(cfg.sandbox?.boxliteUrl || "");
          setDefaultTimezone(cfg.prefs?.timezone || "");
        })
        .catch(() => {})
        .finally(() => setLoading(false));
    });
  }, [router]);

  const handleSave = async () => {
    setSaving(true);
    setSaved(false);
    setSaveError("");
    // Persist every backend's field so switching the dropdown after a
    // save still surfaces the value the user typed for that backend.
    // Also mirror the active backend's value into the legacy `image`
    // slot so consumers that haven't migrated still resolve correctly.
    const activeImage =
      sandboxBackend === "e2b"
        ? sandboxE2BTemplate
        : sandboxBackend === "boxlite"
          ? sandboxBoxliteImage
          : sandboxDockerImage;
    try {
      const result = await updateConfig({
        prefs: {
          timezone: defaultTimezone.trim() || undefined,
        },
        sandbox: {
          enabled: sandboxEnabled,
          enforce: sandboxEnabled && sandboxEnforce,
          backend: sandboxBackend,
          // Send "" (not undefined) for every optional field: the server
          // PATCHes, so undefined means "keep", and a cleared field never
          // cleared — e.g. an E2B template id left in the legacy slot got
          // pulled as the BoxLite image, and a stale BoxLite image pinned
          // a cached :latest instead of the current default. API keys stay
          // undefined-when-empty so a blank field doesn't wipe a stored key.
          image: activeImage,
          dockerImage: sandboxDockerImage,
          e2bTemplate: sandboxE2BTemplate,
          boxliteSnapshot: sandboxBoxliteImage,
          e2bKey: sandboxE2BKey || undefined,
          boxliteKey: sandboxBoxliteKey || undefined,
          boxliteUrl: sandboxBoxliteURL,
        },
      }, "system");
      if (result?.ok === false) {
        setSaveError(result.error || tr("Save failed", "保存失败"));
        return;
      }
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : tr("Save failed", "保存失败"));
      return;
    } finally {
      setSaving(false);
    }
    setSaved(true);
    setTimeout(() => setSaved(false), 2000);
  };

  if (loading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-10 w-48" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }
  if (!config) return null;

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-xl font-semibold tracking-tight">{tr("Runtime", "运行环境")}</h3>
          <p className="text-sm text-muted-foreground mt-1">
            {tr("Gateway and sandbox configuration.", "配置网关和沙箱运行环境。")}
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
              {tr("Saved", "已保存")}
            </>
          ) : (
            <>
              <Save className="h-4 w-4 mr-2" />
              {saving ? tr("Saving…", "正在保存…") : tr("Save", "保存")}
            </>
          )}
        </Button>
      </div>
      {saveError && (
        <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {saveError}
        </div>
      )}

      <div className="rounded-lg border border-border bg-card">
        <div className="p-5">
          <div className="flex items-start gap-3">
            <Clock className="mt-0.5 h-4 w-4 text-sky-500" />
            <div className="grid flex-1 gap-4 sm:grid-cols-[1fr_260px] sm:items-start">
              <div>
                <h3 className="font-medium">{tr("Default timezone", "默认时区")}</h3>
                <p className="mt-1 text-sm text-muted-foreground">
                  {tr(
                    "Used before falling back to the deployment timezone. Current deployment fallback: {{timezone}}.",
                    "优先使用此系统设置；未设置时回退到部署环境时区。当前部署环境时区：{{timezone}}。",
                    { timezone: config.meta?.serverTimezone || tr("Local", "本地时区") },
                  )}
                </p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="default-timezone">{tr("IANA timezone", "IANA 时区")}</Label>
                <Input
                  id="default-timezone"
                  value={defaultTimezone}
                  onChange={(e) => setDefaultTimezone(e.target.value)}
                  placeholder="Asia/Shanghai"
                  className="font-mono text-sm"
                />
              </div>
            </div>
          </div>
        </div>
        <Separator />
        <div className="p-5">
          <div className="flex items-center justify-between">
            <div>
              <div className="flex items-center gap-2 mb-1">
                <Container className="h-4 w-4 text-purple-500" />
                <h3 className="font-medium">{tr("Sandbox", "沙盒")}</h3>
              </div>
              <p className="text-sm text-muted-foreground">
                {tr("Execute code in isolated sandbox environments", "在隔离的沙箱环境中执行代码")}
              </p>
            </div>
            <Switch checked={sandboxEnabled} onCheckedChange={setSandboxEnabled} />
          </div>
        </div>
        {sandboxEnabled && (
          <div className="px-5 pb-5 space-y-4">
            <Separator />
            <div className="flex items-center justify-between gap-4">
              <div>
                <Label>{tr("Always run in the sandbox", "强制在沙盒执行")}</Label>
                <p className="mt-1 text-sm text-muted-foreground">
                  {tr(
                    "On: every command runs inside the sandbox and never on this machine. Off: commands run on this machine by default and the agent opts into the sandbox per call (guest chatters are always sandboxed).",
                    "开启后所有命令都在沙盒中执行，不会在本机运行。关闭时默认在本机执行，由智能体按需选择沙盒（非管理员的访客始终在沙盒中执行）。",
                  )}
                </p>
              </div>
              <Switch checked={sandboxEnforce} onCheckedChange={setSandboxEnforce} />
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label>{tr("Backend", "后端")}</Label>
                <Select value={sandboxBackend} onValueChange={(v) => v && setSandboxBackend(v)}>
                  <SelectTrigger>
                    <SelectValue>
                      {(v: unknown) =>
                        ({ docker: "Docker", e2b: tr("E2B (cloud)", "E2B（云端）"), boxlite: tr("BoxLite (cloud)", "BoxLite（云端）") } as Record<string, string>)[
                          v as string
                        ] ?? (v as string) ?? ""
                      }
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="docker">Docker</SelectItem>
                    <SelectItem value="e2b">{tr("E2B (cloud)", "E2B（云端）")}</SelectItem>
                    <SelectItem value="boxlite">{tr("BoxLite (cloud)", "BoxLite（云端）")}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              {sandboxBackend === "e2b" ? (
                <>
                  <div className="space-y-2">
                    <Label>{tr("E2B API key", "E2B API 密钥")}</Label>
                    <Input
                      type="password"
                      value={sandboxE2BKey}
                      onChange={(e) => setSandboxE2BKey(e.target.value)}
                      placeholder="e2b_..."
                      className="font-mono text-sm"
                    />
                  </div>
                  <div className="space-y-2">
                    <Label>{tr("E2B template", "E2B 模板")}</Label>
                    <Input
                      value={sandboxE2BTemplate}
                      onChange={(e) => setSandboxE2BTemplate(e.target.value)}
                      placeholder="base"
                      className="font-mono text-sm"
                    />
                  </div>
                </>
              ) : sandboxBackend === "boxlite" ? (
                <>
                  <div className="space-y-2">
                    <Label>{tr("BoxLite API key", "BoxLite API 密钥")}</Label>
                    <Input
                      type="password"
                      value={sandboxBoxliteKey}
                      onChange={(e) => setSandboxBoxliteKey(e.target.value)}
                      placeholder="blk_live_..."
                      className="font-mono text-sm"
                    />
                  </div>
                  <div className="space-y-2">
                    <Label>{tr("Image (optional)", "镜像（选填）")}</Label>
                    <Input
                      value={sandboxBoxliteImage}
                      onChange={(e) => setSandboxBoxliteImage(e.target.value)}
                      placeholder="thinkany/fastclaw-sandbox:20261112"
                      className="font-mono text-sm"
                    />
                    <p className="text-xs text-muted-foreground">
                      {tr(
                        "Any public registry image; short names resolve to Docker Hub. Leave empty to use the default.",
                        "任意公共仓库镜像，短名称默认指向 Docker Hub。留空则使用默认镜像。",
                      )}
                    </p>
                  </div>
                  <div className="space-y-2 sm:col-span-2">
                    <Label>{tr("API URL (optional)", "API 地址（选填）")}</Label>
                    <Input
                      value={sandboxBoxliteURL}
                      onChange={(e) => setSandboxBoxliteURL(e.target.value)}
                      placeholder="https://api.boxlite.ai/v1"
                      className="font-mono text-sm"
                    />
                  </div>
                </>
              ) : (
                <div className="space-y-2">
                  <Label>{tr("Docker image", "Docker 镜像")}</Label>
                  <Input
                    value={sandboxDockerImage}
                    onChange={(e) => setSandboxDockerImage(e.target.value)}
                    placeholder="thinkany/fastclaw-sandbox:20261112"
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
