"use client";

import { useEffect, useState } from "react";
import { Check, Copy, ExternalLink } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { getStatus, type StatusResponse } from "@/lib/api";
import { copyText } from "@/lib/clipboard";

const UPGRADE_CMD = "fastclaw upgrade";

const RELEASES_URL = "https://github.com/fastclaw-ai/fastclaw/releases";

export default function AboutSettingsPage() {
  const [status, setStatus] = useState<StatusResponse | null>(null);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    getStatus()
      .then(setStatus)
      .catch(() => setStatus(null));
  }, []);

  const version = status?.version || "unknown";

  const copyCmd = async () => {
    try {
      if (await copyText(UPGRADE_CMD)) {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      }
    } catch {
      /* clipboard may be unavailable on insecure origins — ignore */
    }
  };

  return (
    <div className="space-y-6">
      <div>
        <h3 className="text-xl font-semibold tracking-tight">About</h3>
        <p className="text-sm text-muted-foreground mt-1">
          Gateway version and release info.
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
            <p className="text-sm font-medium">Upgrade</p>
            <p className="text-xs text-muted-foreground mt-0.5">
              Check GitHub for the latest release and upgrade instructions.
            </p>
          </div>
          <Button
            variant="outline"
            size="sm"
            onClick={() => window.open(RELEASES_URL, "_blank", "noopener,noreferrer")}
          >
            <ExternalLink className="h-4 w-4 mr-2" />
            Releases
          </Button>
        </div>
        <div className="space-y-1.5">
          <p className="text-xs text-muted-foreground">
            Or upgrade in place from the shell:
          </p>
          <div className="flex items-center justify-between gap-2 rounded-md bg-muted/40 px-3 py-2">
            <code className="font-mono text-sm">{UPGRADE_CMD}</code>
            <Button
              size="icon"
              variant="ghost"
              className="h-7 w-7"
              onClick={copyCmd}
              aria-label="Copy command"
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
