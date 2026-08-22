"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { QRCodeSVG } from "qrcode.react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
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
  Radio,
  Plus,
  Trash2,
  Send,
  CheckCircle2,
  ExternalLink,
  Loader2,
  QrCode,
  Eye,
  EyeOff,
} from "lucide-react";
import {
  listAgentChannels,
  connectAgentTelegram,
  connectAgentDiscord,
  connectAgentSlack,
  connectAgentLINE,
  connectAgentFeishu,
  connectAgentWeCom,
  startAgentWeChatLogin,
  pollAgentWeChatLoginStatus,
  disconnectAgentChannel,
  type AgentChannel,
} from "@/lib/api";
import { ChannelIcon as SharedChannelIcon } from "@/components/channel-icon";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";

// Channels page: per-agent IM bot bindings. One card per channel type
// in the catalog — connected types show bot info + Disconnect, others
// show a Connect button. The backend supports multiple bots per type;
// the UI intentionally surfaces only the first binding for now to keep
// the mental model simple (one bot per channel per agent). When we add
// multi-bot management later, this card can expand to a list.

const CATALOG: { type: string; label: string; description: string; available: boolean }[] = [
  {
    type: "telegram",
    label: "Telegram",
    description: "Connect a Telegram bot to relay messages to this agent.",
    available: true,
  },
  {
    type: "discord",
    label: "Discord",
    description: "Connect a Discord bot — works in DMs and servers it's invited to.",
    available: true,
  },
  {
    type: "slack",
    label: "Slack",
    description: "Connect a Slack app via Socket Mode (bot token + app token).",
    available: true,
  },
  {
    type: "line",
    label: "LINE",
    description: "Connect a LINE Messaging API channel via webhook (channel access token + channel secret).",
    available: true,
  },
  {
    type: "wechat",
    label: "WeChat",
    description: "Scan a QR code with the WeChat phone app to relay messages to this agent.",
    available: true,
  },
  {
    type: "wecom",
    label: "WeCom",
    description: "Connect a WeCom Intelligent Bot over an authenticated WebSocket.",
    available: true,
  },
  {
    type: "feishu",
    label: "Feishu",
    description: "Connect a Feishu custom-app bot via webhook (App ID + App Secret).",
    available: true,
  },
];

export default function AgentChannelsPage() {
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);

  const [channels, setChannels] = useState<AgentChannel[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const [telegramOpen, setTelegramOpen] = useState(false);
  const [discordOpen, setDiscordOpen] = useState(false);
  const [slackOpen, setSlackOpen] = useState(false);
  const [lineOpen, setLineOpen] = useState(false);
  const [wechatOpen, setWechatOpen] = useState(false);
  const [wecomOpen, setWecomOpen] = useState(false);
  const [feishuOpen, setFeishuOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<AgentChannel | null>(null);

  const refresh = useCallback(() => {
    if (!agentId) return;
    setLoading(true);
    listAgentChannels(agentId)
      .then((list) => setChannels(list))
      .catch((e) => setError(e instanceof Error ? e.message : "Failed to load channels"))
      .finally(() => setLoading(false));
  }, [agentId]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  // First binding per channel type — the UI is currently single-bot,
  // even though the backend allows multiple. If multiple exist (legacy
  // data), the rest are still wired up server-side, just hidden here.
  const byType = useMemo(() => {
    const m: Record<string, AgentChannel> = {};
    for (const ch of channels) {
      if (!m[ch.type]) m[ch.type] = ch;
    }
    return m;
  }, [channels]);

  const handleDelete = async () => {
    if (!deleteTarget || !agentId) return;
    const target = deleteTarget;
    setDeleteTarget(null);
    const res = await disconnectAgentChannel(agentId, target.type, target.accountId);
    if (res.error) setError(res.error);
    refresh();
  };

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <div className="flex items-center gap-2">
            <Radio className="size-5 text-muted-foreground" />
            <h2 className="text-2xl font-semibold tracking-tight">Channels</h2>
          </div>
          <p className="text-sm text-muted-foreground mt-1">
            Connect IM platforms to <strong>{agentName || "this agent"}</strong>{" "}
            so people can chat with it on Telegram, Discord, and more.
          </p>
        </div>
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
      ) : (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {CATALOG.map((entry) => {
            const connected = byType[entry.type];
            return connected ? (
              <ConnectedCard
                key={entry.type}
                label={entry.label}
                channel={connected}
                onDelete={() => setDeleteTarget(connected)}
              />
            ) : (
              <CatalogCard
                key={entry.type}
                type={entry.type}
                label={entry.label}
                description={entry.description}
                available={entry.available}
                onConnect={() => {
                  if (entry.type === "telegram") setTelegramOpen(true);
                  else if (entry.type === "discord") setDiscordOpen(true);
                  else if (entry.type === "slack") setSlackOpen(true);
                  else if (entry.type === "line") setLineOpen(true);
                  else if (entry.type === "wechat") setWechatOpen(true);
                  else if (entry.type === "wecom") setWecomOpen(true);
                  else if (entry.type === "feishu") setFeishuOpen(true);
                }}
              />
            );
          })}
        </div>
      )}

      <ConnectTelegramDialog
        open={telegramOpen}
        onOpenChange={setTelegramOpen}
        agentId={agentId}
        onConnected={refresh}
      />

      <ConnectDiscordDialog
        open={discordOpen}
        onOpenChange={setDiscordOpen}
        agentId={agentId}
        onConnected={refresh}
      />

      <ConnectSlackDialog
        open={slackOpen}
        onOpenChange={setSlackOpen}
        agentId={agentId}
        onConnected={refresh}
      />

      <ConnectLINEDialog
        open={lineOpen}
        onOpenChange={setLineOpen}
        agentId={agentId}
        onConnected={refresh}
      />

      <ConnectWeChatDialog
        open={wechatOpen}
        onOpenChange={setWechatOpen}
        agentId={agentId}
        onConnected={refresh}
      />

      <ConnectWeComDialog
        open={wecomOpen}
        onOpenChange={setWecomOpen}
        agentId={agentId}
        onConnected={refresh}
      />

      <ConnectFeishuDialog
        open={feishuOpen}
        onOpenChange={setFeishuOpen}
        agentId={agentId}
        onConnected={refresh}
      />

      <AlertDialog open={!!deleteTarget} onOpenChange={(v) => !v && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Disconnect channel</AlertDialogTitle>
            <AlertDialogDescription>
              Disconnect{" "}
              <strong>
                {deleteTarget?.botUsername || deleteTarget?.accountId || deleteTarget?.type}
              </strong>
              ? Existing chat history is preserved, but the bot will stop
              forwarding new messages to this agent.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Disconnect
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function CatalogCard({
  type,
  label,
  description,
  available,
  onConnect,
}: {
  type: string;
  label: string;
  description: string;
  available: boolean;
  onConnect: () => void;
}) {
  return (
    <div className="rounded-lg border border-border bg-card p-4 flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <SharedChannelIcon channel={type} className="h-4 w-4 shrink-0" />
        <span className="font-medium">{label}</span>
      </div>
      <p className="text-xs text-muted-foreground flex-1">{description}</p>
      <Button
        size="sm"
        variant={available ? "outline" : "ghost"}
        disabled={!available}
        onClick={onConnect}
        className="w-full"
      >
        <Plus className="h-3.5 w-3.5 mr-1.5" />
        {available ? "Connect" : "Coming soon"}
      </Button>
    </div>
  );
}

function ConnectedCard({
  label,
  channel,
  onDelete,
}: {
  label: string;
  channel: AgentChannel;
  onDelete: () => void;
}) {
  // Telegram is the only provider with a public profile URL pattern
  // (t.me/<username>); Discord/Slack don't expose one from a bot
  // username alone, so we render plain text for those.
  const botLink =
    channel.type === "telegram" && channel.botUsername
      ? `https://t.me/${channel.botUsername}`
      : null;

  return (
    <div className="rounded-lg border border-border bg-card p-4 flex flex-col gap-3">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 min-w-0">
          <SharedChannelIcon channel={channel.type} className="h-4 w-4 shrink-0" />
          <span className="font-medium truncate">{label}</span>
        </div>
        {channel.enabled && (
          <span className="inline-flex items-center gap-1 text-xs text-emerald-600 dark:text-emerald-400">
            <CheckCircle2 className="h-3 w-3" />
            Connected
          </span>
        )}
      </div>

      <div className="flex-1 space-y-1.5 min-w-0">
        {channel.botUsername && (
          botLink ? (
            <a
              href={botLink}
              target="_blank"
              rel="noreferrer"
              className="text-xs text-muted-foreground hover:text-foreground inline-flex items-center gap-1 truncate max-w-full"
            >
              @{channel.botUsername}
              <ExternalLink className="h-3 w-3 shrink-0" />
            </a>
          ) : (
            <p className="text-xs text-muted-foreground truncate">
              {channel.type === "wecom" ? channel.botUsername : `@${channel.botUsername}`}
            </p>
          )
        )}
        <code className="text-xs text-muted-foreground/80 font-mono truncate block">
          {channel.botToken}
        </code>
      </div>

      <Button
        size="sm"
        variant="outline"
        onClick={onDelete}
        className="w-full text-destructive hover:text-destructive hover:bg-destructive/5"
      >
        <Trash2 className="h-3.5 w-3.5 mr-1.5" />
        Disconnect
      </Button>
    </div>
  );
}

function ConnectTelegramDialog({
  open,
  onOpenChange,
  agentId,
  onConnected,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  agentId: string;
  onConnected: () => void;
}) {
  const [token, setToken] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [connected, setConnected] = useState<{ botUsername: string } | null>(null);

  useEffect(() => {
    if (!open) {
      setToken("");
      setError("");
      setSubmitting(false);
      setConnected(null);
    }
  }, [open]);

  const submit = async () => {
    if (!token.trim() || !agentId) return;
    setSubmitting(true);
    setError("");
    const res = await connectAgentTelegram(agentId, token.trim());
    setSubmitting(false);
    if (res.error || !res.ok) {
      setError(res.error || "Failed to connect");
      return;
    }
    setConnected({ botUsername: res.botUsername || "" });
    onConnected();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <img src="/channels/telegram.svg" alt="Telegram" className="h-5 w-5 object-contain" />
            Connect Telegram bot
          </DialogTitle>
          <DialogDescription>
            Talk to{" "}
            <a
              href="https://t.me/BotFather"
              target="_blank"
              rel="noreferrer"
              className="underline"
            >
              @BotFather
            </a>{" "}
            on Telegram, run <code>/newbot</code>, and paste the HTTP API token
            it returns. The token is verified via <code>getMe</code> before
            anything is saved.
          </DialogDescription>
        </DialogHeader>

        {connected ? (
          <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4 space-y-2">
            <div className="flex items-center gap-2">
              <CheckCircle2 className="h-4 w-4 text-emerald-500" />
              <span className="text-sm font-medium">Connected</span>
            </div>
            <p className="text-sm">
              Bot is live as{" "}
              <a
                href={`https://t.me/${connected.botUsername}`}
                target="_blank"
                rel="noreferrer"
                className="font-mono text-sky-600 dark:text-sky-400 hover:underline inline-flex items-center gap-1"
              >
                @{connected.botUsername}
                <ExternalLink className="h-3 w-3" />
              </a>
              . Send it a message on Telegram to test the integration.
            </p>
          </div>
        ) : (
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label htmlFor="bot-token">Bot token</Label>
              <Input
                id="bot-token"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder="123456789:ABCdef..."
                className="font-mono text-sm"
                autoFocus
              />
            </div>
            {error && (
              <p className="text-xs text-destructive">{error}</p>
            )}
          </div>
        )}

        <DialogFooter>
          {connected ? (
            <Button onClick={() => onOpenChange(false)}>Done</Button>
          ) : (
            <>
              <Button
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={submitting}
              >
                Cancel
              </Button>
              <Button onClick={submit} disabled={submitting || !token.trim()}>
                {submitting ? "Connecting…" : "Connect"}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ConnectDiscordDialog({
  open,
  onOpenChange,
  agentId,
  onConnected,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  agentId: string;
  onConnected: () => void;
}) {
  const [token, setToken] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [connected, setConnected] = useState<{ botUsername: string } | null>(null);

  useEffect(() => {
    if (!open) {
      setToken("");
      setError("");
      setSubmitting(false);
      setConnected(null);
    }
  }, [open]);

  const submit = async () => {
    if (!token.trim() || !agentId) return;
    setSubmitting(true);
    setError("");
    const res = await connectAgentDiscord(agentId, token.trim());
    setSubmitting(false);
    if (res.error || !res.ok) {
      setError(res.error || "Failed to connect");
      return;
    }
    setConnected({ botUsername: res.botUsername || "" });
    onConnected();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <img src="/channels/discord.svg" alt="Discord" className="h-5 w-5 object-contain" />
            Connect Discord bot
          </DialogTitle>
          <DialogDescription>
            Open the{" "}
            <a
              href="https://discord.com/developers/applications"
              target="_blank"
              rel="noreferrer"
              className="underline"
            >
              Discord Developer Portal
            </a>
            , create an application, add a Bot, and copy the Bot Token. Make
            sure <strong>MESSAGE CONTENT INTENT</strong> is enabled under
            Bot → Privileged Gateway Intents. The token is verified via{" "}
            <code>/users/@me</code> before anything is saved.
          </DialogDescription>
        </DialogHeader>

        {connected ? (
          <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4 space-y-2">
            <div className="flex items-center gap-2">
              <CheckCircle2 className="h-4 w-4 text-emerald-500" />
              <span className="text-sm font-medium">Connected</span>
            </div>
            <p className="text-sm">
              Bot is live as{" "}
              <span className="font-mono">{connected.botUsername}</span>.
              Invite it to a server (OAuth2 → URL Generator → Bot scope) or
              DM it on Discord to test.
            </p>
          </div>
        ) : (
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label htmlFor="discord-bot-token">Bot Token</Label>
              <Input
                id="discord-bot-token"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder="MTEx..."
                className="font-mono text-sm"
                autoFocus
              />
            </div>
            {error && <p className="text-xs text-destructive">{error}</p>}
          </div>
        )}

        <DialogFooter>
          {connected ? (
            <Button onClick={() => onOpenChange(false)}>Done</Button>
          ) : (
            <>
              <Button
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={submitting}
              >
                Cancel
              </Button>
              <Button onClick={submit} disabled={submitting || !token.trim()}>
                {submitting ? "Connecting…" : "Connect"}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ConnectSlackDialog({
  open,
  onOpenChange,
  agentId,
  onConnected,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  agentId: string;
  onConnected: () => void;
}) {
  const [botToken, setBotToken] = useState("");
  const [appToken, setAppToken] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [connected, setConnected] = useState<{ teamName: string } | null>(null);

  useEffect(() => {
    if (!open) {
      setBotToken("");
      setAppToken("");
      setError("");
      setSubmitting(false);
      setConnected(null);
    }
  }, [open]);

  const submit = async () => {
    if (!botToken.trim() || !appToken.trim() || !agentId) return;
    setSubmitting(true);
    setError("");
    const res = await connectAgentSlack(agentId, botToken.trim(), appToken.trim());
    setSubmitting(false);
    if (res.error || !res.ok) {
      setError(res.error || "Failed to connect");
      return;
    }
    setConnected({ teamName: res.teamName || "" });
    onConnected();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <img src="/channels/slack.svg" alt="Slack" className="h-5 w-5 object-contain" />
            Connect Slack app
          </DialogTitle>
          <DialogDescription>
            Create a Slack app at{" "}
            <a
              href="https://api.slack.com/apps"
              target="_blank"
              rel="noreferrer"
              className="underline"
            >
              api.slack.com/apps
            </a>
            . Enable <strong>Socket Mode</strong>, generate an{" "}
            <strong>app-level token</strong> (xapp-…) with{" "}
            <code>connections:write</code>, then under{" "}
            <strong>OAuth & Permissions</strong> copy the{" "}
            <strong>Bot User OAuth Token</strong> (xoxb-…). Then go to{" "}
            <strong>Event Subscriptions → Subscribe to bot events</strong> and
            add <code>message.channels</code>, <code>message.im</code>, and{" "}
            <code>app_mention</code> (Slack will prompt for the matching scopes
            — <code>channels:history</code>, <code>im:history</code>,{" "}
            <code>app_mentions:read</code> — and ask you to reinstall).
          </DialogDescription>
        </DialogHeader>

        {connected ? (
          <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4 space-y-2">
            <div className="flex items-center gap-2">
              <CheckCircle2 className="h-4 w-4 text-emerald-500" />
              <span className="text-sm font-medium">Connected</span>
            </div>
            <p className="text-sm">
              Bot is live in workspace{" "}
              <strong>{connected.teamName}</strong>. Invite it to a channel
              with <code>/invite @bot</code> and message it to test.
            </p>
          </div>
        ) : (
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label htmlFor="slack-bot-token">Bot User OAuth Token</Label>
              <Input
                id="slack-bot-token"
                value={botToken}
                onChange={(e) => setBotToken(e.target.value)}
                placeholder="xoxb-..."
                className="font-mono text-sm"
                autoFocus
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="slack-app-token">App-Level Token</Label>
              <Input
                id="slack-app-token"
                value={appToken}
                onChange={(e) => setAppToken(e.target.value)}
                placeholder="xapp-..."
                className="font-mono text-sm"
              />
            </div>
            {error && <p className="text-xs text-destructive">{error}</p>}
          </div>
        )}

        <DialogFooter>
          {connected ? (
            <Button onClick={() => onOpenChange(false)}>Done</Button>
          ) : (
            <>
              <Button
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={submitting}
              >
                Cancel
              </Button>
              <Button
                onClick={submit}
                disabled={submitting || !botToken.trim() || !appToken.trim()}
              >
                {submitting ? "Connecting…" : "Connect"}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// LINE Messaging API connect dialog. Two-step UX matching Feishu:
//   1. User pastes Channel access token + Channel secret; we hit
//      /v2/bot/info to validate and capture the bot's userId.
//   2. On success, surface the public webhook URL — user pastes it
//      into LINE Developers Console under "Messaging API → Webhook URL"
//      and toggles "Use webhook" on.
function ConnectLINEDialog({
  open,
  onOpenChange,
  agentId,
  onConnected,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  agentId: string;
  onConnected: () => void;
}) {
  const [channelToken, setChannelToken] = useState("");
  const [channelSecret, setChannelSecret] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [connected, setConnected] = useState<{ botName: string; basicId: string; webhookUrl: string } | null>(null);

  useEffect(() => {
    if (!open) {
      setChannelToken("");
      setChannelSecret("");
      setError("");
      setSubmitting(false);
      setConnected(null);
    }
  }, [open]);

  const submit = async () => {
    if (!channelToken.trim() || !agentId) return;
    setSubmitting(true);
    setError("");
    const res = await connectAgentLINE(
      agentId,
      channelToken.trim(),
      channelSecret.trim(),
    );
    setSubmitting(false);
    if (res.error || !res.ok) {
      setError(res.error || "Failed to connect");
      return;
    }
    setConnected({
      botName: res.botName || "",
      basicId: res.basicId || "",
      webhookUrl: res.webhookUrl || "",
    });
    onConnected();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <img src="/channels/line.png" alt="LINE" className="h-5 w-5 object-contain" />
            Connect LINE channel
          </DialogTitle>
          <DialogDescription>
            Create a Messaging API channel at{" "}
            <a
              href="https://developers.line.biz"
              target="_blank"
              rel="noreferrer"
              className="underline"
            >
              developers.line.biz
            </a>
            . Under <strong>Messaging API</strong> issue a long-lived{" "}
            <strong>Channel access token</strong>, and copy the{" "}
            <strong>Channel secret</strong> from the Basic settings tab. Toggle
            on <em>Use webhook</em> after saving the URL we&apos;ll generate.
          </DialogDescription>
        </DialogHeader>

        {connected ? (
          <div className="space-y-3 py-2">
            <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4 space-y-2">
              <div className="flex items-center gap-2">
                <CheckCircle2 className="h-4 w-4 text-emerald-500" />
                <span className="text-sm font-medium">Credentials valid</span>
              </div>
              <p className="text-sm">
                Bot identified as{" "}
                <strong>{connected.botName || "(unnamed)"}</strong>{" "}
                {connected.basicId && (
                  <code className="font-mono text-xs">{connected.basicId}</code>
                )}.
              </p>
            </div>
            <div className="rounded-lg border bg-muted/30 p-4 space-y-2">
              <p className="text-sm font-medium">One last step</p>
              <p className="text-xs text-muted-foreground">
                Paste this into LINE Developers Console →{" "}
                <strong>Messaging API → Webhook URL</strong>, click{" "}
                <em>Verify</em>, then toggle{" "}
                <strong>Use webhook</strong> on.
              </p>
              <Input
                readOnly
                value={connected.webhookUrl}
                className="font-mono text-xs"
                onFocus={(e) => e.currentTarget.select()}
              />
              <p className="text-xs text-muted-foreground">
                Add the bot as a friend (search the basic ID), or invite it to
                a group, then send a message to test.
              </p>
            </div>
          </div>
        ) : (
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label htmlFor="line-channel-token">Channel access token</Label>
              <Input
                id="line-channel-token"
                value={channelToken}
                onChange={(e) => setChannelToken(e.target.value)}
                placeholder="long-lived token"
                type="password"
                className="font-mono text-sm"
                autoFocus
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="line-channel-secret">Channel secret</Label>
              <Input
                id="line-channel-secret"
                value={channelSecret}
                onChange={(e) => setChannelSecret(e.target.value)}
                placeholder="from Basic settings"
                className="font-mono text-sm"
              />
              <p className="text-xs text-muted-foreground">
                Optional but strongly recommended — fastclaw verifies inbound
                webhook payloads via HMAC-SHA256 against this secret.
              </p>
            </div>
            {error && <p className="text-xs text-destructive">{error}</p>}
          </div>
        )}

        <DialogFooter>
          {connected ? (
            <Button onClick={() => onOpenChange(false)}>Done</Button>
          ) : (
            <>
              <Button
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={submitting}
              >
                Cancel
              </Button>
              <Button
                onClick={submit}
                disabled={submitting || !channelToken.trim()}
              >
                {submitting ? "Validating…" : "Connect"}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ConnectWeChatDialog drives the QR-scan login: fetch a session token,
// render its `qrCode` string as a QR image, then poll the server every
// 3s for state. The polling endpoint does ONE upstream round-trip per
// call (no long-poll on our side), so the lifecycle is purely client-
// driven — closing the dialog cleans up via the polling ref.
function ConnectWeChatDialog({
  open,
  onOpenChange,
  agentId,
  onConnected,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  agentId: string;
  onConnected: () => void;
}) {
  type WechatStatus = "wait" | "scaned" | "confirmed" | "expired" | "";
  const [qrPayload, setQrPayload] = useState("");
  const [sessionId, setSessionId] = useState("");
  const [status, setStatus] = useState<WechatStatus>("");
  const [accountId, setAccountId] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const stopPolling = useCallback(() => {
    if (pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
  }, []);

  // Cleanup on unmount and on dialog close.
  useEffect(() => () => stopPolling(), [stopPolling]);
  useEffect(() => {
    if (!open) {
      stopPolling();
      setQrPayload("");
      setSessionId("");
      setStatus("");
      setAccountId("");
      setError("");
      setLoading(false);
    }
  }, [open, stopPolling]);

  const startLogin = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    setError("");
    setStatus("");
    setAccountId("");
    setQrPayload("");
    stopPolling();
    const res = await startAgentWeChatLogin(agentId);
    setLoading(false);
    if (res.error || !res.sessionId || !res.qrCodeImg) {
      setError(res.error || "Failed to fetch QR code");
      return;
    }
    setSessionId(res.sessionId);
    setQrPayload(res.qrCodeImg);
    setStatus("wait");
    pollRef.current = setInterval(async () => {
      const s = await pollAgentWeChatLoginStatus(agentId, res.sessionId!);
      if (s.error) {
        // Don't kill the loop on a single transient error — iLink's
        // status endpoint occasionally hiccups, and the next tick
        // usually recovers. Surface it as a banner only.
        setError(s.error);
        return;
      }
      setError("");
      if (s.status) setStatus(s.status as WechatStatus);
      if (s.connected) {
        stopPolling();
        if (s.accountId) setAccountId(s.accountId);
        onConnected();
      }
      if (s.status === "expired") {
        stopPolling();
      }
    }, 3000);
  }, [agentId, onConnected, stopPolling]);

  // Auto-fetch a QR as soon as the dialog opens (no separate "name"
  // step — fastclaw doesn't surface per-account names, accountID is
  // ilink_bot_id).
  useEffect(() => {
    if (open && !qrPayload && !loading && !error) {
      startLogin();
    }
  }, [open, qrPayload, loading, error, startLogin]);

  const connected = !!accountId;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[420px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <img src="/channels/wechat.svg" alt="WeChat" className="h-5 w-5 object-contain scale-150" />
            Connect WeChat
          </DialogTitle>
          <DialogDescription>
            Scan the QR code with the WeChat phone app to bind a personal
            WeChat account as the bot for this agent. Inbound DMs will be
            relayed to the agent; the agent's replies are sent back as
            plain text.
          </DialogDescription>
        </DialogHeader>

        {connected ? (
          <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4 space-y-2">
            <div className="flex items-center gap-2">
              <CheckCircle2 className="h-4 w-4 text-emerald-500" />
              <span className="text-sm font-medium">Connected</span>
            </div>
            <p className="text-sm">
              Bot is live as <code className="font-mono text-xs">{accountId}</code>.
              Send it a WeChat message to test.
            </p>
          </div>
        ) : (
          <div className="flex flex-col items-center gap-4 py-2">
            {loading ? (
              <div className="flex h-56 w-56 items-center justify-center">
                <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
              </div>
            ) : qrPayload ? (
              <div className="rounded-lg border bg-white p-4">
                <QRCodeSVG value={qrPayload} size={224} level="M" />
              </div>
            ) : (
              <div className="flex h-56 w-56 items-center justify-center text-sm text-muted-foreground">
                <QrCode className="h-8 w-8 opacity-50" />
              </div>
            )}

            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              {status === "wait" && <>Waiting for scan…</>}
              {status === "scaned" && (
                <>
                  <CheckCircle2 className="h-4 w-4 text-emerald-500" />
                  Scanned — confirm on your phone.
                </>
              )}
              {status === "confirmed" && (
                <>
                  <Loader2 className="h-4 w-4 animate-spin" />
                  Connecting…
                </>
              )}
              {status === "expired" && (
                <span className="text-destructive">QR code expired.</span>
              )}
            </div>

            {error && <p className="text-xs text-destructive">{error}</p>}
          </div>
        )}

        <DialogFooter>
          {connected ? (
            <Button onClick={() => onOpenChange(false)}>Done</Button>
          ) : (
            <>
              {status === "expired" && (
                <Button onClick={startLogin} disabled={loading}>
                  {loading ? "Refreshing…" : "Refresh QR"}
                </Button>
              )}
              <Button variant="outline" onClick={() => onOpenChange(false)}>
                Cancel
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ConnectWeComDialog({
  open,
  onOpenChange,
  agentId,
  onConnected,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  agentId: string;
  onConnected: () => void;
}) {
  const [botId, setBotId] = useState("");
  const [secret, setSecret] = useState("");
  const [showSecret, setShowSecret] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [connectedBotId, setConnectedBotId] = useState("");

  useEffect(() => {
    if (!open) {
      setBotId("");
      setSecret("");
      setShowSecret(false);
      setSubmitting(false);
      setError("");
      setConnectedBotId("");
    }
  }, [open]);

  const submit = async () => {
    if (!botId.trim() || !secret.trim() || !agentId) return;
    setSubmitting(true);
    setError("");
    try {
      const res = await connectAgentWeCom(agentId, {
        botId: botId.trim(),
        secret: secret.trim(),
      });
      if (res.error || !res.ok) {
        setError(res.error || "Failed to connect WeCom");
        return;
      }
      setConnectedBotId(res.botId || botId.trim());
      setSecret("");
      onConnected();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Failed to connect WeCom");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <SharedChannelIcon channel="wecom" className="h-5 w-5 shrink-0" />
            Connect WeCom Intelligent Bot
          </DialogTitle>
          <DialogDescription>
            Create an API-mode Intelligent Bot in WeCom, select Long
            Connection, then paste its Bot ID and Secret. No public callback
            URL is required.
          </DialogDescription>
        </DialogHeader>

        {connectedBotId ? (
          <div className="space-y-3 py-2">
            <div className="rounded-lg border bg-muted/30 p-4 space-y-2">
              <div className="flex items-center gap-2">
                <CheckCircle2 className="h-4 w-4 text-primary" />
                <span className="text-sm font-medium">Connected</span>
              </div>
              <p className="text-sm">The WeCom bot is connected to this agent.</p>
              <code className="block text-xs text-muted-foreground">
                {connectedBotId}
              </code>
            </div>
          </div>
        ) : (
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label htmlFor="wecom-bot-id">Bot ID</Label>
              <Input
                id="wecom-bot-id"
                value={botId}
                onChange={(event) => setBotId(event.target.value)}
                placeholder="Paste the Bot ID"
                className="font-mono text-sm"
                autoComplete="off"
                autoFocus
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="wecom-secret">Secret</Label>
              <div className="relative">
                <Input
                  id="wecom-secret"
                  value={secret}
                  onChange={(event) => setSecret(event.target.value)}
                  placeholder="Paste the Secret"
                  type={showSecret ? "text" : "password"}
                  className="pr-10 font-mono text-sm"
                  autoComplete="new-password"
                />
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  className="absolute right-1 top-1/2 h-7 w-7 -translate-y-1/2 text-muted-foreground"
                  onClick={() => setShowSecret((visible) => !visible)}
                  aria-label={showSecret ? "Hide Secret" : "Show Secret"}
                >
                  {showSecret ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">
                The Secret is sent only to this FastClaw instance and is shown
                masked after connection.
              </p>
            </div>
            {error && <p className="text-xs text-destructive">{error}</p>}
          </div>
        )}

        <DialogFooter>
          {connectedBotId ? (
            <Button onClick={() => onOpenChange(false)}>Done</Button>
          ) : (
            <>
              <Button
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={submitting}
              >
                Cancel
              </Button>
              <Button
                onClick={submit}
                disabled={submitting || !botId.trim() || !secret.trim()}
              >
                {submitting ? (
                  <>
                    <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                    Connecting…
                  </>
                ) : (
                  "Connect"
                )}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// Feishu / Feishu connect dialog. Two-step UX:
//   1. User pastes App ID + App Secret + Verification Token, we validate
//      via /tenant_access_token + /bot/v3/info.
//   2. On success, we surface the webhook URL — user must paste it
//      into the Feishu Developer Console under "Event Subscriptions →
//      Request URL" and re-trigger Feishu's URL verification handshake
//      from there before the bot starts receiving messages.
function ConnectFeishuDialog({
  open,
  onOpenChange,
  agentId,
  onConnected,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  agentId: string;
  onConnected: () => void;
}) {
  const [appId, setAppId] = useState("");
  const [appSecret, setAppSecret] = useState("");
  const [verificationToken, setVerificationToken] = useState("");
  const [encryptKey, setEncryptKey] = useState("");
  const [useLongConn, setUseLongConn] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [connected, setConnected] = useState<{
    botName: string;
    webhookUrl: string;
    useLongConn: boolean;
  } | null>(null);

  useEffect(() => {
    if (!open) {
      setAppId("");
      setAppSecret("");
      setVerificationToken("");
      setEncryptKey("");
      setUseLongConn(true);
      setError("");
      setSubmitting(false);
      setConnected(null);
    }
  }, [open]);

  const submit = async () => {
    if (!appId.trim() || !appSecret.trim() || !agentId) return;
    setSubmitting(true);
    setError("");
    const res = await connectAgentFeishu(
      agentId,
      appId.trim(),
      appSecret.trim(),
      verificationToken.trim(),
      encryptKey.trim(),
      useLongConn,
    );
    setSubmitting(false);
    if (res.error || !res.ok) {
      setError(res.error || "Failed to connect");
      return;
    }
    setConnected({
      botName: res.botName || "",
      webhookUrl: res.webhookUrl || "",
      useLongConn: !!res.useLongConn,
    });
    onConnected();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <img src="/channels/feishu.png" alt="Feishu" className="h-5 w-5 object-contain" />
            Connect Feishu app
          </DialogTitle>
          <DialogDescription>
            Create a custom app at{" "}
            <a
              href="https://open.feishu.cn"
              target="_blank"
              rel="noreferrer"
              className="underline"
            >
              open.feishu.cn
            </a>
            . Enable the bot capability, request{" "}
            <code>im:message</code> + <code>im:message:send_as_bot</code>{" "}
            scopes, then copy the App ID + App Secret from{" "}
            <strong>Credentials & Basic Info</strong>. Long-connection mode
            (recommended) needs nothing else; webhook mode also needs the
            Verification Token / Encrypt Key from{" "}
            <strong>Event Subscriptions</strong>.
          </DialogDescription>
        </DialogHeader>

        {connected ? (
          <div className="space-y-3 py-2">
            <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4 space-y-2">
              <div className="flex items-center gap-2">
                <CheckCircle2 className="h-4 w-4 text-emerald-500" />
                <span className="text-sm font-medium">Credentials valid</span>
              </div>
              <p className="text-sm">
                Bot identified as{" "}
                <strong>{connected.botName || "(unnamed)"}</strong>.
              </p>
            </div>
            {connected.useLongConn ? (
              <div className="rounded-lg border bg-muted/30 p-4 space-y-2">
                <p className="text-sm font-medium">Long-connection mode</p>
                <p className="text-xs text-muted-foreground">
                  fastclaw is now opening a WebSocket to Feishu — no public
                  URL setup needed. In the Feishu Developer Console under{" "}
                  <strong>事件与回调 → 事件配置 → 订阅方式</strong>, pick{" "}
                  <strong>使用长连接接收事件</strong>, then under{" "}
                  <strong>Subscribe to bot events</strong> add{" "}
                  <code>im.message.receive_v1</code>.
                </p>
              </div>
            ) : (
              <div className="rounded-lg border bg-muted/30 p-4 space-y-2">
                <p className="text-sm font-medium">One last step</p>
                <p className="text-xs text-muted-foreground">
                  Paste this into Feishu Developer Console →{" "}
                  <strong>Event Subscriptions → Request URL</strong>, then
                  click <em>Save</em>. Feishu will POST a verification
                  challenge here and this fastclaw instance will echo it
                  automatically.
                </p>
                <Input
                  readOnly
                  value={connected.webhookUrl}
                  className="font-mono text-xs"
                  onFocus={(e) => e.currentTarget.select()}
                />
                <p className="text-xs text-muted-foreground">
                  Subscribe to <code>im.message.receive_v1</code> to receive
                  messages.
                </p>
              </div>
            )}
          </div>
        ) : (
          <div className="space-y-3 py-2">
            <div className="flex items-start justify-between gap-3 rounded-lg border bg-muted/30 p-3">
              <div className="space-y-0.5">
                <Label htmlFor="feishu-long-conn" className="text-sm">
                  Long-connection mode
                </Label>
                <p className="text-xs text-muted-foreground">
                  fastclaw opens a WebSocket to Feishu — no public URL
                  required. Turn off to use the classic webhook flow.
                </p>
              </div>
              <Switch
                id="feishu-long-conn"
                checked={useLongConn}
                onCheckedChange={setUseLongConn}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="feishu-app-id">App ID</Label>
              <Input
                id="feishu-app-id"
                value={appId}
                onChange={(e) => setAppId(e.target.value)}
                placeholder="cli_..."
                className="font-mono text-sm"
                autoFocus
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="feishu-app-secret">App Secret</Label>
              <Input
                id="feishu-app-secret"
                value={appSecret}
                onChange={(e) => setAppSecret(e.target.value)}
                placeholder="..."
                type="password"
                className="font-mono text-sm"
              />
            </div>
            {!useLongConn && (
              <>
            <div className="space-y-1.5">
              <Label htmlFor="feishu-verification-token">Verification Token</Label>
              <Input
                id="feishu-verification-token"
                value={verificationToken}
                onChange={(e) => setVerificationToken(e.target.value)}
                placeholder="from Event Subscriptions tab"
                className="font-mono text-sm"
              />
              <p className="text-xs text-muted-foreground">
                Optional but recommended — fastclaw rejects webhook payloads
                whose <code>header.token</code> doesn&apos;t match.
              </p>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="feishu-encrypt-key">Encrypt Key</Label>
              <Input
                id="feishu-encrypt-key"
                value={encryptKey}
                onChange={(e) => setEncryptKey(e.target.value)}
                placeholder="leave empty if 加密策略 is not configured"
                type="password"
                className="font-mono text-sm"
              />
              <p className="text-xs text-muted-foreground">
                Only required if you set an Encrypt Key under{" "}
                <strong>加密策略</strong> in the Feishu console. Empty = expect
                plaintext webhook bodies.
              </p>
            </div>
              </>
            )}
            {error && <p className="text-xs text-destructive">{error}</p>}
          </div>
        )}

        <DialogFooter>
          {connected ? (
            <Button onClick={() => onOpenChange(false)}>Done</Button>
          ) : (
            <>
              <Button
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={submitting}
              >
                Cancel
              </Button>
              <Button
                onClick={submit}
                disabled={submitting || !appId.trim() || !appSecret.trim()}
              >
                {submitting ? "Validating…" : "Connect"}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
