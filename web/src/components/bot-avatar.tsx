"use client";

import * as React from "react";
import { cn } from "@/lib/utils";

const FALLBACK_COLORS = [
  "#12b8aa",
  "#ff4d64",
  "#8b6cf6",
  "#ff9f2f",
  "#1383a8",
  "#06bd70",
  "#b17b45",
];

function colorFor(seed: string) {
  let hash = 0;
  for (let i = 0; i < seed.length; i++) {
    hash = (hash * 31 + seed.charCodeAt(i)) >>> 0;
  }
  return FALLBACK_COLORS[hash % FALLBACK_COLORS.length];
}

/**
 * Shared Bot avatar for the conversation-first UI. Uploaded avatars win;
 * the fallback is FastClaw's small two-eye "claw" mark, with a stable color
 * derived from the current Bot/session so lists remain easy to scan.
 */
export function BotAvatar({
  agentId,
  avatarUrl,
  seed,
  size = 40,
  className,
}: {
  agentId?: string | null;
  avatarUrl?: string;
  seed?: string;
  size?: number;
  className?: string;
}) {
  const [failed, setFailed] = React.useState(false);
  const resolvedSrc = avatarUrl || (agentId ? `/api/agents/${agentId}/files/avatar.png` : "");

  React.useEffect(() => setFailed(false), [resolvedSrc]);

  if (resolvedSrc && !failed) {
    return (
      // eslint-disable-next-line @next/next/no-img-element
      <img
        src={resolvedSrc}
        alt=""
        width={size}
        height={size}
        onError={() => setFailed(true)}
        className={cn("shrink-0 rounded-[36%] object-cover", className)}
        style={{ width: size, height: size }}
      />
    );
  }

  const color = colorFor(seed || agentId || "fastclaw");
  return (
    <span
      aria-hidden="true"
      className={cn(
        "relative inline-flex shrink-0 items-center justify-center overflow-hidden rounded-[38%_46%_40%_50%]",
        className,
      )}
      style={{ width: size, height: size, backgroundColor: color }}
    >
      <span
        className="absolute bg-white/95"
        style={{
          width: Math.max(3, Math.round(size * 0.095)),
          height: Math.max(6, Math.round(size * 0.22)),
          borderRadius: 999,
          left: "43%",
          top: "35%",
          transform: "rotate(-10deg)",
        }}
      />
      <span
        className="absolute bg-white/95"
        style={{
          width: Math.max(3, Math.round(size * 0.095)),
          height: Math.max(6, Math.round(size * 0.22)),
          borderRadius: 999,
          left: "61%",
          top: "32%",
          transform: "rotate(-10deg)",
        }}
      />
    </span>
  );
}
