// Icons of the coding agents agl drives, by agl agent id (web/public/agent-icons).
const icons: Record<string, string> = {
  claude: "claudecode.svg",
  codex: "codex.svg",
  cursor: "cursor.svg",
  gemini: "gemini.svg",
  grok: "grok.svg",
  hermes: "hermes.webp",
  kilocode: "kilocode.svg",
  kimi: "kimi.svg",
  omp: "omp.svg",
  openclaw: "openclaw.svg",
  opencode: "opencode.svg",
  pi: "pi.svg",
  fastclaw: "fastclaw.png",
};

// agentIconUrl returns the icon for an agl agent id, or "" (show a fallback).
export function agentIconUrl(id: string): string {
  const file = icons[id] ?? icons[id.replace(/-.*$/, "")];
  return file ? `/agent-icons/${file}` : "";
}

// White marks drawn for a dark tile (as their vendors show them).
const darkTile = new Set(["cursor", "opencode"]);

// Black marks: shown as-is on light backgrounds, inverted in dark mode.
const monoBlack = new Set(["grok", "pi"]);

export function agentIconIsMonoBlack(id: string): boolean {
  return monoBlack.has(id) || monoBlack.has(id.replace(/-.*$/, ""));
}

export function agentIconNeedsDarkTile(id: string): boolean {
  return darkTile.has(id) || darkTile.has(id.replace(/-.*$/, ""));
}
