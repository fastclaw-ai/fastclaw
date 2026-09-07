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
} from "lucide-react";
import {
  listAgentChannels,
  connectAgentTelegram,
  connectAgentDiscord,
  connectAgentSlack,
  connectAgentLINE,
  connectAgentFeishu,
  startAgentWeChatLogin,
  pollAgentWeChatLoginStatus,
  disconnectAgentChannel,
  type AgentChannel,
} from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";
import { useLocale } from "@/components/locale-provider";

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
    type: "feishu",
    label: "Feishu",
    description: "Connect a Feishu custom-app bot via webhook (App ID + App Secret).",
    available: true,
  },
];

export default function AgentChannelsPage() {
  const { tr } = useLocale();
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
  const [feishuOpen, setFeishuOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<AgentChannel | null>(null);

  const refresh = useCallback(() => {
    if (!agentId) return;
    setLoading(true);
    listAgentChannels(agentId)
      .then((list) => setChannels(list))
      .catch((e) => setError(e instanceof Error ? e.message : tr("Failed to load channels", "加载渠道失败")))
      .finally(() => setLoading(false));
  }, [agentId, tr]);

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
            <h2 className="text-2xl font-semibold tracking-tight">{tr("Channels", "渠道")}</h2>
          </div>
          <p className="text-sm text-muted-foreground mt-1">
            {tr(
              "Connect messaging platforms to {{agent}} so people can chat with it through Telegram, Discord, and more.",
              "为 {{agent}} 连接即时通讯平台，让用户可以通过 Telegram、Discord 等渠道与其聊天。",
              { agent: agentName || tr("this agent", "此 Agent") },
            )}
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

      <ConnectFeishuDialog
        open={feishuOpen}
        onOpenChange={setFeishuOpen}
        agentId={agentId}
        onConnected={refresh}
      />

      <AlertDialog open={!!deleteTarget} onOpenChange={(v) => !v && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tr("Disconnect channel", "断开渠道连接")}</AlertDialogTitle>
            <AlertDialogDescription>
              {tr(
                "Disconnect {{channel}}? Existing chat history is preserved, but new messages will no longer be forwarded to this agent.",
                "要断开 {{channel}} 吗？已有聊天记录会被保留，但新消息将不再转发给此 Agent。",
                { channel: deleteTarget?.botUsername || deleteTarget?.accountId || deleteTarget?.type || "" },
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tr("Cancel", "取消")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {tr("Disconnect", "断开连接")}
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
  const { tr } = useLocale();
  const localizedDescription =
    ({
      telegram: "连接 Telegram 机器人，将消息转发给此 Agent。",
      discord: "连接 Discord 机器人，支持私信和已邀请的服务器。",
      slack: "通过 Socket Mode 连接 Slack 应用。",
      line: "通过 Webhook 连接 LINE Messaging API 渠道。",
      wechat: "使用微信手机客户端扫码，将消息转发给此 Agent。",
      feishu: "通过长连接或 Webhook 连接飞书自建应用机器人。",
    } as Record<string, string>)[type] || description;
  return (
    <div className="rounded-lg border border-border bg-card p-4 flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <ChannelIcon type={type} />
        <span className="font-medium">{label}</span>
      </div>
      <p className="text-xs text-muted-foreground flex-1">{tr(description, localizedDescription)}</p>
      <Button
        size="sm"
        variant={available ? "outline" : "ghost"}
        disabled={!available}
        onClick={onConnect}
        className="w-full"
      >
        <Plus className="h-3.5 w-3.5 mr-1.5" />
        {available ? tr("Connect", "连接") : tr("Coming soon", "即将推出")}
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
  const { tr } = useLocale();
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
          <ChannelIcon type={channel.type} />
          <span className="font-medium truncate">{label}</span>
        </div>
        {channel.enabled && (
          <span className="inline-flex items-center gap-1 text-xs text-emerald-600 dark:text-emerald-400">
            <CheckCircle2 className="h-3 w-3" />
            {tr("Connected", "已连接")}
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
              @{channel.botUsername}
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
        {tr("Disconnect", "断开连接")}
      </Button>
    </div>
  );
}

function ChannelIcon({ type }: { type: string }) {
  // Brand SVG/PNG assets live in /public/channels — copied from the
  // workany-web icon set. We size them at 16x16 to match the lucide
  // icons they replace; the asset's intrinsic colors carry the brand
  // tint so we don't need a `text-*` class. WeChat has no asset yet so
  // it falls through to the lucide MessageSquare in emerald.
  const asset: Record<string, string> = {
    telegram: "/channels/telegram.svg",
    discord: "/channels/discord.svg",
    slack: "/channels/slack.svg",
    line: "/channels/line.png",
    feishu: "/channels/feishu.png",
    wechat: "/channels/wechat.svg",
  };
  if (asset[type]) {
    // WeChat's artwork is non-square (50×40) — object-contain letterboxes
    // it inside the 16×16 box, leaving a visible gap on top/bottom. Scale
    // up just this one so it reads at the same visual weight as the
    // square brand icons next to it.
    const extra = type === "wechat" ? "scale-150" : "";
    return (
      <img
        src={asset[type]}
        alt={type}
        className={`h-4 w-4 object-contain ${extra}`}
      />
    );
  }
  return <Radio className="h-4 w-4 text-muted-foreground" />;
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
  const { tr } = useLocale();
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
      setError(res.error || tr("Failed to connect", "连接失败"));
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
            {tr("Connect Telegram bot", "连接 Telegram 机器人")}
          </DialogTitle>
          <DialogDescription>
            {tr("Open", "打开")} {" "}
            <a
              href="https://t.me/BotFather"
              target="_blank"
              rel="noreferrer"
              className="underline"
            >
              @BotFather
            </a>{" "}
            {tr("in Telegram, run", "，在 Telegram 中运行")} <code>/newbot</code>
            {tr(", then paste the HTTP API token it returns. The token is verified before it is saved.", "，然后粘贴返回的 HTTP API Token。保存前会先验证 Token。")}
          </DialogDescription>
        </DialogHeader>

        {connected ? (
          <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4 space-y-2">
            <div className="flex items-center gap-2">
              <CheckCircle2 className="h-4 w-4 text-emerald-500" />
              <span className="text-sm font-medium">{tr("Connected", "已连接")}</span>
            </div>
            <p className="text-sm">
              {tr("Bot is live as", "机器人账号为")} {" "}
              <a
                href={`https://t.me/${connected.botUsername}`}
                target="_blank"
                rel="noreferrer"
                className="font-mono text-sky-600 dark:text-sky-400 hover:underline inline-flex items-center gap-1"
              >
                @{connected.botUsername}
                <ExternalLink className="h-3 w-3" />
              </a>
              {tr(". Send it a Telegram message to test the integration.", "。在 Telegram 中向它发送消息即可测试连接。")}
            </p>
          </div>
        ) : (
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label htmlFor="bot-token">{tr("Bot token", "机器人 Token")}</Label>
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
            <Button onClick={() => onOpenChange(false)}>{tr("Done", "完成")}</Button>
          ) : (
            <>
              <Button
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={submitting}
              >
                {tr("Cancel", "取消")}
              </Button>
              <Button onClick={submit} disabled={submitting || !token.trim()}>
                {submitting ? tr("Connecting…", "正在连接…") : tr("Connect", "连接")}
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
  const { tr } = useLocale();
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
      setError(res.error || tr("Failed to connect", "连接失败"));
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
            {tr("Connect Discord bot", "连接 Discord 机器人")}
          </DialogTitle>
          <DialogDescription>
            {tr("Open the", "打开")} {" "}
            <a
              href="https://discord.com/developers/applications"
              target="_blank"
              rel="noreferrer"
              className="underline"
            >
              Discord Developer Portal
            </a>
            {tr(", create an application, add a bot, and copy its token. Enable", "，创建应用并添加机器人，然后复制 Bot Token。在 Bot → Privileged Gateway Intents 中启用")} {" "}
            <strong>MESSAGE CONTENT INTENT</strong>{tr(". The token is verified before it is saved.", "。保存前会先验证 Token。")}
          </DialogDescription>
        </DialogHeader>

        {connected ? (
          <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4 space-y-2">
            <div className="flex items-center gap-2">
              <CheckCircle2 className="h-4 w-4 text-emerald-500" />
              <span className="text-sm font-medium">{tr("Connected", "已连接")}</span>
            </div>
            <p className="text-sm">
              {tr("Bot is live as", "机器人账号为")} {" "}
              <span className="font-mono">{connected.botUsername}</span>.
              {tr(" Invite it to a server through OAuth2 → URL Generator → Bot scope, or send it a direct message to test.", "。通过 OAuth2 → URL Generator → Bot scope 将其邀请到服务器，或发送私信进行测试。")}
            </p>
          </div>
        ) : (
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label htmlFor="discord-bot-token">{tr("Bot token", "机器人 Token")}</Label>
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
            <Button onClick={() => onOpenChange(false)}>{tr("Done", "完成")}</Button>
          ) : (
            <>
              <Button
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={submitting}
              >
                {tr("Cancel", "取消")}
              </Button>
              <Button onClick={submit} disabled={submitting || !token.trim()}>
                {submitting ? tr("Connecting…", "正在连接…") : tr("Connect", "连接")}
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
  const { tr } = useLocale();
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
      setError(res.error || tr("Failed to connect", "连接失败"));
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
            {tr("Connect Slack app", "连接 Slack 应用")}
          </DialogTitle>
          <DialogDescription>
            {tr("Create a Slack app at", "在以下地址创建 Slack 应用：")} {" "}
            <a
              href="https://api.slack.com/apps"
              target="_blank"
              rel="noreferrer"
              className="underline"
            >
              api.slack.com/apps
            </a>
            {tr(". Enable Socket Mode, create an app-level token with connections:write, and copy the Bot User OAuth Token under OAuth & Permissions. Under Event Subscriptions, subscribe to message.channels, message.im, and app_mention, then reinstall the app if prompted.", "。启用 Socket Mode，创建包含 connections:write 权限的 App-Level Token，并在 OAuth & Permissions 中复制 Bot User OAuth Token。在 Event Subscriptions 中订阅 message.channels、message.im 和 app_mention；如有提示，请重新安装应用。")}
          </DialogDescription>
        </DialogHeader>

        {connected ? (
          <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4 space-y-2">
            <div className="flex items-center gap-2">
              <CheckCircle2 className="h-4 w-4 text-emerald-500" />
              <span className="text-sm font-medium">{tr("Connected", "已连接")}</span>
            </div>
            <p className="text-sm">
              {tr("Bot is live in workspace", "机器人已连接到工作区")} {" "}
              <strong>{connected.teamName}</strong>
              {tr(". Invite it to a channel with", "。使用")} <code>/invite @bot</code>{" "}
              {tr("and send it a message to test.", "将其邀请到频道，然后发送消息进行测试。")}
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
            <Button onClick={() => onOpenChange(false)}>{tr("Done", "完成")}</Button>
          ) : (
            <>
              <Button
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={submitting}
              >
                {tr("Cancel", "取消")}
              </Button>
              <Button
                onClick={submit}
                disabled={submitting || !botToken.trim() || !appToken.trim()}
              >
                {submitting ? tr("Connecting…", "正在连接…") : tr("Connect", "连接")}
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
  const { tr } = useLocale();
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
      setError(res.error || tr("Failed to connect", "连接失败"));
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
            {tr("Connect LINE channel", "连接 LINE 渠道")}
          </DialogTitle>
          <DialogDescription>
            {tr("Create a Messaging API channel at", "在以下地址创建 Messaging API 渠道：")} {" "}
            <a
              href="https://developers.line.biz"
              target="_blank"
              rel="noreferrer"
              className="underline"
            >
              developers.line.biz
            </a>
            {tr(". Under Messaging API, issue a long-lived Channel access token and copy the Channel secret from the Basic settings tab. After saving the URL we generate, enable Use webhook.", "。在 Messaging API 中签发长期有效的 Channel access token，并从 Basic settings 标签页复制 Channel secret。保存我们生成的 URL 后，请启用 Use webhook。")}
          </DialogDescription>
        </DialogHeader>

        {connected ? (
          <div className="space-y-3 py-2">
            <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4 space-y-2">
              <div className="flex items-center gap-2">
                <CheckCircle2 className="h-4 w-4 text-emerald-500" />
                <span className="text-sm font-medium">{tr("Credentials valid", "凭证有效")}</span>
              </div>
              <p className="text-sm">
                {tr("Bot identified as", "已识别机器人：")} {" "}
                <strong>{connected.botName || tr("(unnamed)", "（未命名）")}</strong>{" "}
                {connected.basicId && (
                  <code className="font-mono text-xs">{connected.basicId}</code>
                )}.
              </p>
            </div>
            <div className="rounded-lg border bg-muted/30 p-4 space-y-2">
              <p className="text-sm font-medium">{tr("One last step", "最后一步")}</p>
              <p className="text-xs text-muted-foreground">
                {tr("Paste this URL into LINE Developers Console → Messaging API → Webhook URL, click Verify, then enable Use webhook.", "将此 URL 粘贴到 LINE Developers Console → Messaging API → Webhook URL，点击 Verify，然后启用 Use webhook。")}
              </p>
              <Input
                readOnly
                value={connected.webhookUrl}
                className="font-mono text-xs"
                onFocus={(e) => e.currentTarget.select()}
              />
              <p className="text-xs text-muted-foreground">
                {tr("Add the bot as a friend by searching its basic ID, or invite it to a group, then send a message to test.", "通过搜索 Basic ID 将机器人添加为好友，或将其邀请进群，然后发送消息进行测试。")}
              </p>
            </div>
          </div>
        ) : (
          <div className="space-y-3 py-2">
            <div className="space-y-1.5">
              <Label htmlFor="line-channel-token">{tr("Channel access token", "Channel access token")}</Label>
              <Input
                id="line-channel-token"
                value={channelToken}
                onChange={(e) => setChannelToken(e.target.value)}
                placeholder={tr("long-lived token", "长期有效 Token")}
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
                placeholder={tr("from Basic settings", "来自 Basic settings")}
                className="font-mono text-sm"
              />
              <p className="text-xs text-muted-foreground">
                {tr("Optional but strongly recommended — fastclaw uses this secret to verify inbound webhook payloads with HMAC-SHA256.", "可选但强烈建议填写——fastclaw 会使用此密钥通过 HMAC-SHA256 验证传入的 Webhook 请求。")}
              </p>
            </div>
            {error && <p className="text-xs text-destructive">{error}</p>}
          </div>
        )}

        <DialogFooter>
          {connected ? (
            <Button onClick={() => onOpenChange(false)}>{tr("Done", "完成")}</Button>
          ) : (
            <>
              <Button
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={submitting}
              >
                {tr("Cancel", "取消")}
              </Button>
              <Button
                onClick={submit}
                disabled={submitting || !channelToken.trim()}
              >
                {submitting ? tr("Validating…", "正在验证…") : tr("Connect", "连接")}
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
  const { tr } = useLocale();
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
      setError(res.error || tr("Failed to fetch QR code", "获取二维码失败"));
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
  }, [agentId, onConnected, stopPolling, tr]);

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
            {tr("Connect WeChat", "连接微信")}
          </DialogTitle>
          <DialogDescription>
            {tr("Scan the QR code with the WeChat mobile app to bind a personal WeChat account as this agent's bot. Incoming direct messages are relayed to the agent, and its replies are sent back as plain text.", "使用微信手机客户端扫描二维码，将个人微信账号绑定为此 Agent 的机器人。收到的私信会转发给 Agent，其回复将以纯文本发回微信。")}
          </DialogDescription>
        </DialogHeader>

        {connected ? (
          <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4 space-y-2">
            <div className="flex items-center gap-2">
              <CheckCircle2 className="h-4 w-4 text-emerald-500" />
              <span className="text-sm font-medium">{tr("Connected", "已连接")}</span>
            </div>
            <p className="text-sm">
              {tr("Bot is live as", "机器人账号为")} <code className="font-mono text-xs">{accountId}</code>.
              {" "}{tr("Send it a WeChat message to test.", "向该账号发送微信消息即可测试连接。")}
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
              {status === "wait" && <>{tr("Waiting for scan…", "等待扫码…")}</>}
              {status === "scaned" && (
                <>
                  <CheckCircle2 className="h-4 w-4 text-emerald-500" />
                  {tr("Scanned — confirm on your phone.", "已扫码——请在手机上确认。")}
                </>
              )}
              {status === "confirmed" && (
                <>
                  <Loader2 className="h-4 w-4 animate-spin" />
                  {tr("Connecting…", "正在连接…")}
                </>
              )}
              {status === "expired" && (
                <span className="text-destructive">{tr("QR code expired.", "二维码已过期。")}</span>
              )}
            </div>

            {error && <p className="text-xs text-destructive">{error}</p>}
          </div>
        )}

        <DialogFooter>
          {connected ? (
            <Button onClick={() => onOpenChange(false)}>{tr("Done", "完成")}</Button>
          ) : (
            <>
              {status === "expired" && (
                <Button onClick={startLogin} disabled={loading}>
                  {loading ? tr("Refreshing…", "正在刷新…") : tr("Refresh QR", "刷新二维码")}
                </Button>
              )}
              <Button variant="outline" onClick={() => onOpenChange(false)}>
                {tr("Cancel", "取消")}
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
  const { tr } = useLocale();
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
      setError(res.error || tr("Failed to connect", "连接失败"));
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
            {tr("Connect Feishu app", "连接飞书应用")}
          </DialogTitle>
          <DialogDescription>
            {tr("Create a custom app at", "在以下地址创建自建应用：")} {" "}
            <a
              href="https://open.feishu.cn"
              target="_blank"
              rel="noreferrer"
              className="underline"
            >
              open.feishu.cn
            </a>
            {tr(". Enable the bot capability, request the", "。启用机器人能力，申请")} {" "}
            <code>im:message</code> + <code>im:message:send_as_bot</code>{" "}
            {tr("scopes, then copy the App ID and App Secret from Credentials & Basic Info. Long-connection mode (recommended) needs nothing else; webhook mode also needs the Verification Token and Encrypt Key from Event Subscriptions.", "权限，然后从「凭证与基础信息」复制 App ID 和 App Secret。长连接模式（推荐）无需其他配置；Webhook 模式还需填写「事件订阅」中的 Verification Token 和 Encrypt Key。")}
          </DialogDescription>
        </DialogHeader>

        {connected ? (
          <div className="space-y-3 py-2">
            <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4 space-y-2">
              <div className="flex items-center gap-2">
                <CheckCircle2 className="h-4 w-4 text-emerald-500" />
                <span className="text-sm font-medium">{tr("Credentials valid", "凭证有效")}</span>
              </div>
              <p className="text-sm">
                {tr("Bot identified as", "已识别机器人：")} {" "}
                <strong>{connected.botName || tr("(unnamed)", "（未命名）")}</strong>.
              </p>
            </div>
            {connected.useLongConn ? (
              <div className="rounded-lg border bg-muted/30 p-4 space-y-2">
                <p className="text-sm font-medium">{tr("Long-connection mode", "长连接模式")}</p>
                <p className="text-xs text-muted-foreground">
                  {tr("fastclaw is now opening a WebSocket to Feishu, so no public URL is needed. In the Feishu Developer Console, choose 事件与回调 → 事件配置 → 订阅方式 → 使用长连接接收事件, then add im.message.receive_v1 under Subscribe to bot events.", "fastclaw 正在与飞书建立 WebSocket，无需配置公网 URL。在飞书开发者后台选择「事件与回调 → 事件配置 → 订阅方式 → 使用长连接接收事件」，然后在「添加事件」中添加 im.message.receive_v1。")}
                </p>
              </div>
            ) : (
              <div className="rounded-lg border bg-muted/30 p-4 space-y-2">
                <p className="text-sm font-medium">{tr("One last step", "最后一步")}</p>
                <p className="text-xs text-muted-foreground">
                  {tr("Paste this URL into Feishu Developer Console → Event Subscriptions → Request URL, then click Save. Feishu will send a verification request here, and this fastclaw instance will respond automatically.", "将此 URL 粘贴到飞书开发者后台的「事件订阅 → 请求地址」，然后点击保存。飞书会向此地址发送验证请求，fastclaw 将自动响应。")}
                </p>
                <Input
                  readOnly
                  value={connected.webhookUrl}
                  className="font-mono text-xs"
                  onFocus={(e) => e.currentTarget.select()}
                />
                <p className="text-xs text-muted-foreground">
                  {tr("Subscribe to", "订阅")} <code>im.message.receive_v1</code> {tr("to receive messages.", "以接收消息。")}
                </p>
              </div>
            )}
          </div>
        ) : (
          <div className="space-y-3 py-2">
            <div className="flex items-start justify-between gap-3 rounded-lg border bg-muted/30 p-3">
              <div className="space-y-0.5">
                <Label htmlFor="feishu-long-conn" className="text-sm">
                  {tr("Long-connection mode", "长连接模式")}
                </Label>
                <p className="text-xs text-muted-foreground">
                  {tr("fastclaw opens a WebSocket to Feishu, so no public URL is required. Turn this off to use the classic webhook flow.", "fastclaw 会通过 WebSocket 连接飞书，无需公网 URL。关闭此选项可改用传统 Webhook 流程。")}
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
                placeholder={tr("from Event Subscriptions", "来自事件订阅")}
                className="font-mono text-sm"
              />
              <p className="text-xs text-muted-foreground">
                {tr("Optional but recommended — fastclaw rejects webhook payloads whose", "可选但建议填写——如果 Webhook 请求中的")} <code>header.token</code> {tr("does not match.", "不匹配，fastclaw 将拒绝该请求。")}
              </p>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="feishu-encrypt-key">Encrypt Key</Label>
              <Input
                id="feishu-encrypt-key"
                value={encryptKey}
                onChange={(e) => setEncryptKey(e.target.value)}
                placeholder={tr("leave empty if encryption is not configured", "未配置加密策略时留空")}
                type="password"
                className="font-mono text-sm"
              />
              <p className="text-xs text-muted-foreground">
                {tr("Only required if an Encrypt Key is configured under 加密策略 in the Feishu console. Leave it empty to accept plaintext webhook bodies.", "仅当飞书后台的「加密策略」配置了 Encrypt Key 时才需要填写；留空表示接收明文 Webhook 请求。")}
              </p>
            </div>
              </>
            )}
            {error && <p className="text-xs text-destructive">{error}</p>}
          </div>
        )}

        <DialogFooter>
          {connected ? (
            <Button onClick={() => onOpenChange(false)}>{tr("Done", "完成")}</Button>
          ) : (
            <>
              <Button
                variant="outline"
                onClick={() => onOpenChange(false)}
                disabled={submitting}
              >
                {tr("Cancel", "取消")}
              </Button>
              <Button
                onClick={submit}
                disabled={submitting || !appId.trim() || !appSecret.trim()}
              >
                {submitting ? tr("Validating…", "正在验证…") : tr("Connect", "连接")}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
