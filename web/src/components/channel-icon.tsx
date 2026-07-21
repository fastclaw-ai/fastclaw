// Central brand asset map shared by the Channels page, sidebar, chat lists,
// and connection dialogs. Existing marks are bundled locally; WeCom uses the
// verified official mark persisted to the configured R2 asset domain.
const ASSETS: Record<string, string> = {
  telegram: "/channels/telegram.svg",
  discord: "/channels/discord.svg",
  slack: "/channels/slack.svg",
  line: "/channels/line.png",
  feishu: "/channels/feishu.png",
  wechat: "/channels/wechat.svg",
  wecom: "https://cdn.www-agents.com/brand/channels/wecom-logo-20260721.webp",
};

// ChannelIcon renders the per-channel brand mark next to a chat title.
// Returns null for web / unknown channels — web is the default place a
// chat lives in this UI, so a generic globe glyph next to every web
// session adds noise without information. IM rows still get their
// brand mark to disambiguate.
//
// Images carry their own colors; we don't apply a text-* class. WeChat's
// source artwork is non-square (50×40) — object-contain letterboxes it
// inside the box, so it gets a small scale-up to land at visual parity
// with the square marks beside it.
export function ChannelIcon({
  channel,
  className = "size-4 shrink-0",
}: {
  channel?: string;
  className?: string;
}) {
  const src = channel ? ASSETS[channel] : undefined;
  if (!src) return null;
  const extra = channel === "wechat" ? "scale-150" : "";
  return (
    // eslint-disable-next-line @next/next/no-img-element
    <img
      src={src}
      alt={channel ?? ""}
      className={`${className} object-contain ${extra}`}
    />
  );
}

// channelLabel returns a human-readable name suitable for tooltips.
export function channelLabel(channel?: string): string {
  switch (channel) {
    case "telegram":
      return "Telegram";
    case "wechat":
      return "WeChat";
    case "line":
      return "LINE";
    case "discord":
      return "Discord";
    case "slack":
      return "Slack";
    case "feishu":
      return "Feishu";
    case "wecom":
      return "WeCom";
    case "web":
    case "":
    case undefined:
      return "Web";
    default:
      return channel;
  }
}
