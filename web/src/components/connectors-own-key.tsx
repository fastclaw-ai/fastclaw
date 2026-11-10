"use client";

// Console → Tools → Connectors: the person's own Connany project key.
// Like models and skills, theirs is used first; without one they use the
// deployment's connector service (Admin → Tools), when there is one.

import * as React from "react";
import { Check, KeyRound, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useLocale } from "@/components/locale-provider";
import {
  clearMyConnectorsConfig,
  ConnectorApiError,
  getMyConnectorsConfig,
  saveMyConnectorsConfig,
  type MyConnectorsConfig,
} from "@/lib/api";

export function OwnConnanyKey({ onChanged }: { onChanged?: () => void }) {
  const { tr } = useLocale();
  const [config, setConfig] = React.useState<MyConnectorsConfig | null>(null);
  const [open, setOpen] = React.useState(false);
  const [url, setUrl] = React.useState("");
  const [apiKey, setApiKey] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");

  const errorText = React.useCallback(
    (code: string) =>
      ({
        invalid_key: tr("Connany rejected this API key.", "Connany 拒绝了这个 API Key。"),
        invalid_url: tr("Use a public https:// address, without a path.", "请填写公网 https:// 地址，不要带路径。"),
        unreachable: tr("Couldn't reach Connany at this address.", "无法访问该地址上的 Connany。"),
        invalid_request: tr("Fill in both the address and the API key.", "请填写地址和 API Key。"),
      } as Record<string, string>)[code] || tr("Connany didn't answer correctly.", "Connany 没有正常响应。"),
    [tr],
  );

  const show = React.useCallback((c: MyConnectorsConfig) => {
    setConfig(c);
    setUrl(c.url);
    setApiKey("");
  }, []);

  React.useEffect(() => {
    getMyConnectorsConfig()
      .then((c) => {
        show(c);
        // No service at all: the form is the only thing to do here.
        if (!c.source) setOpen(true);
      })
      .catch((e) => setError(errorText(e instanceof ConnectorApiError ? e.code : "")));
  }, [show, errorText]);

  const run = async (call: () => Promise<MyConnectorsConfig>) => {
    setBusy(true);
    setError("");
    try {
      show(await call());
      setOpen(false);
      onChanged?.();
    } catch (e) {
      setError(errorText(e instanceof ConnectorApiError ? e.code : ""));
    } finally {
      setBusy(false);
    }
  };

  if (!config) return error ? <p className="mt-4 text-sm text-destructive">{error}</p> : null;

  const own = config.source === "user";
  const status = own
    ? tr("Using your own Connany key ({{hint}}).", "正在使用你自己的 Connany Key（{{hint}}）。", { hint: config.apiKeyHint })
    : config.systemAvailable
      ? tr("Using the connector service this FastClaw provides.", "正在使用本站提供的连接服务。")
      : tr(
          "This FastClaw doesn't provide a connector service. Add your own Connany project key to connect your accounts.",
          "本站没有提供连接服务。填写你自己的 Connany 项目 Key，就可以连接你的账号。",
        );

  return (
    <div className="mt-5 rounded-lg border border-border p-4">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <KeyRound className="size-4 shrink-0 text-muted-foreground" />
        <span className="min-w-0 flex-1 text-muted-foreground">{status}</span>
        {!open && (
          <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
            {own ? tr("Change", "修改") : tr("Use my own key", "使用自己的 Key")}
          </Button>
        )}
        {!open && own && config.systemAvailable && (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => void run(clearMyConnectorsConfig)}>
            {tr("Use this site's service", "改用本站服务")}
          </Button>
        )}
      </div>
      {open && (
        <div className="mt-4 space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="own-connany-url">{tr("Connany address", "Connany 地址")}</Label>
            <Input
              id="own-connany-url"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="https://connany.example.com"
              className="font-mono text-sm"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="own-connany-key">{tr("Project API key", "项目 API Key")}</Label>
            <Input
              id="own-connany-key"
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
                "Only your accounts use it. The key stays on the server: it never reaches browsers, agents or their sandboxes. Accounts connected through another key don't carry over.",
                "只用于你自己的账号。Key 只保存在服务端，不会发送给浏览器、Agent 或沙盒。通过其他 Key 连接的账号不会自动带过来。",
              )}
            </p>
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
          <div className="flex flex-wrap items-center gap-2">
            <Button
              size="sm"
              disabled={busy || !url.trim() || (!apiKey.trim() && !config.apiKeySet)}
              onClick={() => void run(() => saveMyConnectorsConfig(url.trim(), apiKey.trim()))}
            >
              {busy ? (
                <><Loader2 className="mr-2 size-4 animate-spin" />{tr("Checking…", "正在验证…")}</>
              ) : (
                <><Check className="mr-2 size-4" />{tr("Save", "保存")}</>
              )}
            </Button>
            {own && (
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => void run(clearMyConnectorsConfig)}>
                {config.systemAvailable ? tr("Remove (use this site's service)", "移除（改用本站服务）") : tr("Remove", "移除")}
              </Button>
            )}
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => { setOpen(false); setError(""); setUrl(config.url); setApiKey(""); }}>
              {tr("Cancel", "取消")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
