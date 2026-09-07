// Brand asset paths copied to /public/channels/. Same set the
// dashboard's Channels page uses, so the sidebar / chats list and the
// connect dialog share one visual identity.
const ASSETS: Record<string, string> = {
  telegram: "/channels/telegram.svg",
  discord: "/channels/discord.svg",
  slack: "/channels/slack.svg",
  line: "/channels/line.png",
  feishu: "/channels/feishu.png",
  wechat: "/channels/wechat.svg",
};

// ChannelIcon renders the per-channel brand mark next to a chat title.
// Web / legacy / unknown channels intentionally return null: a plain web
// conversation needs no decorative prefix. Callers can still replace it
// with an image thumbnail when the web chat actually contains media.
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
    case "web":
    case "":
    case undefined:
      return "Web";
    default:
      return channel;
  }
}
