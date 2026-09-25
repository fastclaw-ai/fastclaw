// Provider custom headers are edited as one "Name: value" pair per line.

export function headersToText(headers?: Record<string, string>): string {
  return Object.entries(headers ?? {})
    .map(([name, value]) => `${name}: ${value}`)
    .join("\n");
}

export function parseHeadersText(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const idx = line.indexOf(":");
    if (idx <= 0) continue;
    const name = line.slice(0, idx).trim();
    if (name) out[name] = line.slice(idx + 1).trim();
  }
  return out;
}
