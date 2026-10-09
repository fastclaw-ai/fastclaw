"use client";

// System → Tools → Connectors: where the Connany connector service is.
// Saving checks the URL + key against the service first and applies at
// once — no restart. People then connect their own accounts in
// Console → Connectors.

import * as React from "react";
import { Check, Loader2, Save } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useLocale } from "@/components/locale-provider";
import {
  clearConnectorsConfig,
  ConnectorApiError,
  getConnectorsConfig,
  saveConnectorsConfig,
  type ConnectorsConfig,
} from "@/lib/api";

export function ConnectorsAdminPanel() {
  const { tr } = useLocale();
  const [config, setConfig] = React.useState<ConnectorsConfig | null>(null);
  const [url, setUrl] = React.useState("");
  const [apiKey, setApiKey] = React.useState("");
  const [saving, setSaving] = React.useState(false);
  const [saved, setSaved] = React.useState(false);
  const [error, setError] = React.useState("");

  const errorText = React.useCallback(
    (code: string) =>
      ({
        invalid_key: tr("The service rejected this API key.", "连接服务拒绝了这个 API Key。"),
        invalid_url: tr("Use an https:// address (http:// only for localhost), without a path.", "请填写 https:// 地址（仅 localhost 可用 http://），不要带路径。"),
        unreachable: tr("Couldn't reach the service at this address.", "无法访问该地址上的连接服务。"),
        invalid_request: tr("Fill in both the address and the API key.", "请填写地址和 API Key。"),
      } as Record<string, string>)[code] || tr("The connector service didn't answer correctly.", "连接服务没有正常响应。"),
    [tr],
  );

  const show = React.useCallback((c: ConnectorsConfig) => {
    setConfig(c);
    setUrl(c.url);
    setApiKey("");
  }, []);

  React.useEffect(() => {
    getConnectorsConfig()
      .then(show)
      .catch((e) => setError(errorText(e instanceof ConnectorApiError ? e.code : "")));
  }, [show, errorText]);

  const save = async () => {
    setSaving(true);
    setError("");
    try {
      show(await saveConnectorsConfig(url.trim(), apiKey.trim()));
      setSaved(true);
      setTimeout(() => setSaved(false), 2000);
    } catch (e) {
      setError(errorText(e instanceof ConnectorApiError ? e.code : ""));
    } finally {
      setSaving(false);
    }
  };

  const clear = async () => {
    setError("");
    try {
      show(await clearConnectorsConfig());
    } catch (e) {
      setError(errorText(e instanceof ConnectorApiError ? e.code : ""));
    }
  };

  if (!config) {
    return error ? (
      <p className="text-sm text-destructive">{error}</p>
    ) : (
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin" />
        {tr("Loading…", "加载中…")}
      </div>
    );
  }

  return (
    <div className="max-w-2xl space-y-6">
      <div>
        <h2 className="text-lg font-semibold tracking-tight">{tr("Connectors", "连接器")}</h2>
        <p className="mt-1 text-sm text-muted-foreground">
          {tr(
            "Let people connect their own accounts (Notion, GitHub, Linear, …) so their agents can read them in web chats. Accounts are connected through the Connany connector service; each person connects theirs in Console → Connectors.",
            "让用户连接自己的账号（Notion、GitHub、Linear 等），他们的 Agent 在网页对话中就能读取。账号通过 Connany 连接服务接入，每个用户在「控制台 → 连接器」里连接自己的账号。",
          )}
        </p>
      </div>

      <div className="flex items-center gap-2 text-sm">
        <span className={`size-2 rounded-full ${config.enabled ? "bg-emerald-500" : "bg-muted-foreground/40"}`} />
        {config.enabled
          ? config.source === "env"
            ? tr("Enabled (from environment variables)", "已启用（来自环境变量）")
            : tr("Enabled", "已启用")
          : tr("Not enabled", "未启用")}
      </div>

      <div className="space-y-4 rounded-lg border p-4">
        <div className="space-y-1.5">
          <Label htmlFor="connany-url">{tr("Service address", "服务地址")}</Label>
          <Input
            id="connany-url"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://connany.example.com"
            className="font-mono text-sm"
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="connany-key">API Key</Label>
          <Input
            id="connany-key"
            type="password"
            autoComplete="off"
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            placeholder={
              config.apiKeySet
                ? tr("Set ({{hint}}) — leave empty to keep it", "已设置（{{hint}}）——留空则保持不变", { hint: config.apiKeyHint })
                : "cn_live_…"
            }
            className="font-mono text-sm"
          />
          <p className="text-xs text-muted-foreground">
            {tr(
              "The project key stays on the server: it never reaches browsers, agents or their sandboxes.",
              "项目 Key 只保存在服务端，不会发送给浏览器、Agent 或沙盒。",
            )}
          </p>
        </div>
        {error && <p className="text-sm text-destructive">{error}</p>}
        <div className="flex flex-wrap items-center gap-2">
          <Button onClick={save} disabled={saving || !url.trim() || (!apiKey.trim() && !config.apiKeySet)}>
            {saved ? (
              <><Check className="mr-2 size-4" />{tr("Saved", "已保存")}</>
            ) : saving ? (
              <><Loader2 className="mr-2 size-4 animate-spin" />{tr("Checking…", "正在验证…")}</>
            ) : (
              <><Save className="mr-2 size-4" />{tr("Save", "保存")}</>
            )}
          </Button>
          {config.source === "settings" && (
            <Button variant="ghost" onClick={clear}>
              {config.envConfigured
                ? tr("Clear (use environment variables)", "清除（改用环境变量）")
                : tr("Clear (turn off)", "清除（关闭连接器）")}
            </Button>
          )}
        </div>
      </div>

      <p className="text-xs text-muted-foreground">
        {tr(
          "Saving checks the address and key with the service and takes effect immediately. You can also set FASTCLAW_CONNANY_URL and FASTCLAW_CONNANY_API_KEY; settings saved here take precedence.",
          "保存时会先用这里的地址和 Key 访问连接服务验证，验证通过后立即生效，无需重启。也可以通过环境变量 FASTCLAW_CONNANY_URL 和 FASTCLAW_CONNANY_API_KEY 配置；这里保存的设置优先。",
        )}
      </p>
    </div>
  );
}
