"use client";

import { useEffect, useState } from "react";
import { Check, Copy, ExternalLink } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { getStatus, type StatusResponse } from "@/lib/api";
import { useLocale } from "@/components/locale-provider";

const UPGRADE_CMD = "fastclaw upgrade";

const RELEASES_URL = "https://github.com/fastclaw-ai/fastclaw/releases";

export default function AboutSettingsPage() {
  const { tr } = useLocale();
  const [status, setStatus] = useState<StatusResponse | null>(null);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    getStatus()
      .then(setStatus)
      .catch(() => setStatus(null));
  }, []);

  const version = status?.version || tr("unknown", "未知");

  const copyCmd = async () => {
    try {
      await navigator.clipboard.writeText(UPGRADE_CMD);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard may be unavailable on insecure origins — ignore */
    }
  };

  return (
    <div className="space-y-6">
      <div>
        <h3 className="text-xl font-semibold tracking-tight">{tr("About", "关于")}</h3>
        <p className="text-sm text-muted-foreground mt-1">
          {tr("Gateway version and release information.", "查看网关版本和发布信息。")}
        </p>
      </div>

      <div className="rounded-lg border border-border bg-card p-5 space-y-3">
        <div className="flex items-center justify-between">
          <span className="text-sm text-muted-foreground">FastClaw</span>
          <code className="text-sm font-mono bg-muted px-2 py-0.5 rounded">
            {version}
          </code>
        </div>
        <Separator />
        <div className="flex items-center justify-between gap-3">
          <div>
            <p className="text-sm font-medium">{tr("Upgrade", "升级")}</p>
            <p className="text-xs text-muted-foreground mt-0.5">
              {tr(
                "Check GitHub for the latest release and upgrade instructions.",
                "前往 GitHub 查看最新版本和升级说明。",
              )}
            </p>
          </div>
          <Button
            variant="outline"
            size="sm"
            onClick={() => window.open(RELEASES_URL, "_blank", "noopener,noreferrer")}
          >
            <ExternalLink className="h-4 w-4 mr-2" />
            {tr("Releases", "发布版本")}
          </Button>
        </div>
        <div className="space-y-1.5">
          <p className="text-xs text-muted-foreground">
            {tr("Or upgrade in place from the shell:", "也可以在终端中直接升级：")}
          </p>
          <div className="flex items-center justify-between gap-2 rounded-md bg-muted/40 px-3 py-2">
            <code className="font-mono text-sm">{UPGRADE_CMD}</code>
            <Button
              size="icon"
              variant="ghost"
              className="h-7 w-7"
              onClick={copyCmd}
              aria-label={tr("Copy command", "复制命令")}
            >
              {copied ? (
                <Check className="h-3.5 w-3.5" />
              ) : (
                <Copy className="h-3.5 w-3.5" />
              )}
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}
