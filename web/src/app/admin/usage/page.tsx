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
  adminGetTokenUsage,
  adminListAgents,
  adminListUsers,
  type TokenUsageRange,
  type TokenUsageReport,
} from "@/lib/api";

// Built inside the component (via useMemo) so the labels can go through
// the translation dictionary while the array reference stays stable.
const buildRanges = (
  t: ReturnType<typeof useTranslations<"adminUsage">>,
): { value: TokenUsageRange; label: string }[] => [
  { value: "24h", label: t("range24h") },
  { value: "7d", label: t("range7d") },
  { value: "30d", label: t("range30d") },
];

// fmt collapses big counts into 12.3K / 4.5M / 1.2B so the cards stay
// readable when traffic ramps. Below 1000 we keep the exact count.
function fmt(n: number): string {
  if (!Number.isFinite(n)) return "—";
  if (Math.abs(n) < 1000) return n.toString();
  const abs = Math.abs(n);
  if (abs < 1_000_000) return (n / 1_000).toFixed(1) + "K";
  if (abs < 1_000_000_000) return (n / 1_000_000).toFixed(2) + "M";
  return (n / 1_000_000_000).toFixed(2) + "B";
}

export default function AdminUsagePage() {
  const t = useTranslations("adminUsage");
  const ranges = useMemo(() => buildRanges(t), [t]);
  const [range, setRange] = useState<TokenUsageRange>("7d");
  const [report, setReport] = useState<TokenUsageReport | null>(null);
  const [agentNames, setAgentNames] = useState<Record<string, string>>({});
  const [userNames, setUserNames] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  // Resolve ID → display name for both axes. Done once on mount; the
  // ranking lists send back raw IDs so the dashboard wouldn't be
  // legible without this.
  useEffect(() => {
    let aborted = false;
    (async () => {
      try {
        const [u, a] = await Promise.all([adminListUsers(), adminListAgents()]);
        if (aborted) return;
        const um: Record<string, string> = {};
        for (const row of u.users ?? []) {
          um[row.id] = row.displayName || row.username || row.id;
        }
        const am: Record<string, string> = {};
        for (const row of a.agents ?? []) {
          am[row.id] = row.name || row.id;
        }
        setAgentNames(am);
        setUserNames(um);
      } catch {
        // Non-fatal — rankings still render with raw IDs.
      }
    })();
    return () => {
      aborted = true;
    };
  }, []);

  async function load(r: TokenUsageRange) {
    setLoading(true);
    setError("");
    try {
      const data = await adminGetTokenUsage(r, 10);
      setReport(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : t("errorLoadFailed"));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    load(range);
  }, [range]);

  const totals = report?.totals;
  const totalTokens = useMemo(() => {
    if (!totals) return 0;
    return (
      totals.inputTokens +
      totals.outputTokens +
      totals.cacheReadTokens +
      totals.cacheCreationTokens
    );
  }, [totals]);

  function renderKey(rawKey: string, names: Record<string, string>): string {
    if (rawKey === "") return t("systemLabel");
    return names[rawKey] ?? rawKey;
  }

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">{t("title")}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {t("subtitle")}
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={() => load(range)} disabled={loading}>
          <RefreshCcw className={`h-4 w-4 mr-2 ${loading ? "animate-spin" : ""}`} />
          {t("refresh")}
        </Button>
      </div>

      <Tabs value={range} onValueChange={(v) => setRange(v as TokenUsageRange)}>
        <TabsList>
          {ranges.map((r) => (
            <TabsTrigger key={r.value} value={r.value}>
              {r.label}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>

      {error && (
        <Card className="border-destructive/40 bg-destructive/5">
          <CardContent>
            <p className="text-sm text-destructive">{error}</p>
          </CardContent>
        </Card>
      )}

      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <SummaryCard
          label={t("totalTokensLabel")}
          value={fmt(totalTokens)}
          hint={t("requestsHint", { count: totals?.requestCount ?? 0 })}
        />
        <SummaryCard label={t("inputLabel")} value={fmt(totals?.inputTokens ?? 0)} />
        <SummaryCard label={t("outputLabel")} value={fmt(totals?.outputTokens ?? 0)} />
        <SummaryCard
          label={t("cacheLabel")}
          value={`${fmt(totals?.cacheReadTokens ?? 0)} / ${fmt(totals?.cacheCreationTokens ?? 0)}`}
        />
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <RankCard
          title={t("topAgents")}
          rows={report?.topAgents ?? []}
          resolve={(k) => renderKey(k, agentNames)}
          icon="agent"
        />
        <RankCard
          title={t("topUsers")}
          rows={report?.topUsers ?? []}
          resolve={(k) => renderKey(k, userNames)}
          icon="user"
        />
      </div>
    </div>
  );
}

function SummaryCard({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <Card>
      <CardContent>
        <div className="flex items-center justify-between">
          <p className="text-sm text-muted-foreground">{label}</p>
          <Coins className="h-4 w-4 text-muted-foreground" />
        </div>
        <p className="text-2xl font-semibold mt-2">{value}</p>
        {hint && <p className="text-xs text-muted-foreground mt-1">{hint}</p>}
      </CardContent>
    </Card>
  );
}

interface RankCardProps {
  title: string;
  rows: { key: string; tokens: number; inputTokens: number; outputTokens: number; requestCount: number }[];
  resolve: (key: string) => string;
  icon: "agent" | "user";
}

function RankCard({ title, rows, resolve }: RankCardProps) {
  const t = useTranslations("adminUsage");
  return (
    <Card>
      <CardContent>
        <h3 className="text-sm font-medium mb-3">{title}</h3>
        {rows.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("noUsage")}</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("colName")}</TableHead>
                <TableHead className="text-right">{t("colTokens")}</TableHead>
                <TableHead className="text-right">{t("colRequests")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((r) => (
                <TableRow key={r.key || "system"}>
                  <TableCell className="font-medium truncate max-w-[200px]" title={r.key}>
                    {resolve(r.key)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">{fmt(r.tokens)}</TableCell>
                  <TableCell className="text-right tabular-nums">{r.requestCount}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}
