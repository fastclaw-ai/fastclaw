"use client";

import { useEffect, useMemo, useState } from "react";
import { useTranslations } from "next-intl";
import { Coins, RefreshCcw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  getAgentTokenUsage,
  getChatSessions,
  type AgentTokenUsage,
  type ChatSessionEntry,
  type TokenUsageRange,
} from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";

// Labels come from the agentUsage.* message namespace, so the range
// list is a builder over the active translator instead of a constant.
type UsageTranslator = ReturnType<typeof useTranslations<"agentUsage">>;

const RANGES = (t: UsageTranslator): { value: TokenUsageRange; label: string }[] => [
  { value: "24h", label: t("range24h") },
  { value: "7d", label: t("range7d") },
  { value: "30d", label: t("range30d") },
];

// fmt collapses big counts into 12.3K / 4.5M for the table. Below 1000
// keep the raw count so a quick test session reads "47" not "0.0K".
function fmt(n: number): string {
  if (!Number.isFinite(n)) return "—";
  if (Math.abs(n) < 1000) return n.toString();
  const abs = Math.abs(n);
  if (abs < 1_000_000) return (n / 1_000).toFixed(1) + "K";
  if (abs < 1_000_000_000) return (n / 1_000_000).toFixed(2) + "M";
  return (n / 1_000_000_000).toFixed(2) + "B";
}

export default function AgentUsagePage() {
  const t = useTranslations("agentUsage");
  const agentId = useAgentIdFromURL();
  const ranges = useMemo(() => RANGES(t), [t]);
  const [range, setRange] = useState<TokenUsageRange>("7d");
  const [data, setData] = useState<AgentTokenUsage | null>(null);
  const [sessions, setSessions] = useState<ChatSessionEntry[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  // Pull session metadata once so the table can render titles
  // instead of opaque session_keys. getChatSessions returns the
  // owner's view of this agent's chats; rows whose key doesn't
  // appear there (e.g. cron-fired sessions for a different
  // chatter on a public agent) fall back to the truncated key.
  useEffect(() => {
    if (!agentId) return;
    let aborted = false;
    (async () => {
      try {
        const list = await getChatSessions(agentId);
        if (!aborted) setSessions(list);
      } catch {
        // Non-fatal — table just shows raw keys.
      }
    })();
    return () => {
      aborted = true;
    };
  }, [agentId]);

  const sessionTitles = useMemo(() => {
    const m: Record<string, string> = {};
    for (const s of sessions) {
      m[s.id] = s.title || s.preview || s.id;
    }
    return m;
  }, [sessions]);

  async function load(r: TokenUsageRange) {
    if (!agentId) return;
    setLoading(true);
    setError("");
    try {
      const d = await getAgentTokenUsage(agentId, r, 50);
      setData(d);
    } catch (e) {
      setError(e instanceof Error ? e.message : t("errorLoadFailed"));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    load(range);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agentId, range]);

  function renderSessionLabel(key: string): string {
    if (!key) return t("untracked");
    const title = sessionTitles[key];
    if (title) return title;
    // Keys are opaque hashes — truncate so the row stays readable.
    return key.length > 14 ? key.slice(0, 14) + "…" : key;
  }

  const rows = data?.sessions ?? [];

  return (
    <div className="p-6 space-y-6 max-w-3xl">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-xl font-semibold tracking-tight">{t("title")}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {t("subtitle")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Tabs value={range} onValueChange={(v) => setRange(v as TokenUsageRange)}>
            <TabsList>
              {ranges.map((r) => (
                <TabsTrigger key={r.value} value={r.value}>
                  {r.label}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
          <Button variant="outline" size="sm" onClick={() => load(range)} disabled={loading}>
            <RefreshCcw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} />
          </Button>
        </div>
      </div>

      {error && (
        <Card className="border-destructive/40 bg-destructive/5">
          <CardContent>
            <p className="text-sm text-destructive">{error}</p>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardContent>
          {rows.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-10 text-center">
              <Coins className="h-8 w-8 text-muted-foreground mb-3" />
              <p className="text-sm text-muted-foreground">
                {t("empty")}
              </p>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("colSession")}</TableHead>
                  <TableHead className="text-right">{t("colInput")}</TableHead>
                  <TableHead className="text-right">{t("colOutput")}</TableHead>
                  <TableHead className="text-right">{t("colCache")}</TableHead>
                  <TableHead className="text-right">{t("colTotal")}</TableHead>
                  <TableHead className="text-right">{t("colRequests")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((r) => {
                  // Cache = total - input - output; the API rolls
                  // cache_read + cache_creation into `tokens` but
                  // doesn't break them out on the wire (yet). Showing
                  // a single "Cache" column makes the row math add
                  // up (input + output + cache = total) without
                  // pretending prompt-cache hits don't exist.
                  const cache = Math.max(0, r.tokens - r.inputTokens - r.outputTokens);
                  return (
                    <TableRow key={r.key || "untracked"}>
                      <TableCell className="font-medium max-w-[260px] truncate" title={r.key}>
                        {renderSessionLabel(r.key)}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">{fmt(r.inputTokens)}</TableCell>
                      <TableCell className="text-right tabular-nums">{fmt(r.outputTokens)}</TableCell>
                      <TableCell className="text-right tabular-nums text-muted-foreground">{fmt(cache)}</TableCell>
                      <TableCell className="text-right tabular-nums font-medium">{fmt(r.tokens)}</TableCell>
                      <TableCell className="text-right tabular-nums">{r.requestCount}</TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
