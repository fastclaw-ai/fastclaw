"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslations } from "next-intl";
import {
  MessagesSquare,
  ChevronLeft,
  ChevronRight,
  Bot,
  User as UserIcon,
  ExternalLink,
  Loader2,
  RefreshCw,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { adminListChats, type AdminChatSessionEntry } from "@/lib/api";
import { ChannelIcon, channelLabel } from "@/components/channel-icon";

const PAGE_SIZE = 30;

export default function AdminChatsPage() {
  const t = useTranslations("adminChats");
  const [sessions, setSessions] = useState<AdminChatSessionEntry[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [page, setPage] = useState(1);

  // load is shared by the initial mount effect and the refresh button.
  // The mount path passes initial=true so it owns the full-page spinner
  // (sessions still empty); manual refreshes use the smaller in-button
  // spinner instead so the existing rows stay visible while the fetch
  // is in flight.
  const load = useCallback(async (initial: boolean) => {
    if (initial) setLoading(true);
    else setRefreshing(true);
    try {
      const list = await adminListChats();
      setSessions(list);
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : t("errorLoadFailed"));
    } finally {
      if (initial) setLoading(false);
      else setRefreshing(false);
    }
  }, [t]);

  useEffect(() => {
    void load(true);
  }, [load]);

  // Newest first — backend doesn't guarantee order across (user, agent)
  // pairs because it concatenates per-agent lists.
  const sorted = useMemo(
    () =>
      [...sessions].sort((a, b) => (b.updatedAt ?? 0) - (a.updatedAt ?? 0)),
    [sessions],
  );

  const totalPages = Math.max(1, Math.ceil(sorted.length / PAGE_SIZE));
  const safePage = Math.min(page, totalPages);
  const pageStart = (safePage - 1) * PAGE_SIZE;
  const pageRows = sorted.slice(pageStart, pageStart + PAGE_SIZE);

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between gap-4">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">{t("title")}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {t("subtitle")}
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => void load(false)}
          disabled={loading || refreshing}
          title={t("refreshTitle")}
        >
          <RefreshCw className={`h-4 w-4 mr-2 ${refreshing ? "animate-spin" : ""}`} />
          {t("refresh")}
        </Button>
      </div>

      {error && (
        <Card className="border-destructive/40 bg-destructive/5">
          <CardContent>
            <p className="text-sm text-destructive">{error}</p>
          </CardContent>
        </Card>
      )}

      {loading ? (
        <div className="rounded-lg border border-border bg-card">
          <div className="flex flex-col items-center justify-center py-16">
            <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
            <p className="mt-3 text-xs text-muted-foreground/60">{t("loading")}</p>
          </div>
        </div>
      ) : sorted.length === 0 ? (
        <div className="rounded-lg border border-border bg-card">
          <div className="flex flex-col items-center justify-center py-16">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-primary/10 mb-4">
              <MessagesSquare className="h-7 w-7 text-primary" />
            </div>
            <p className="text-sm text-muted-foreground mb-1">{t("empty")}</p>
            <p className="text-xs text-muted-foreground/60">
              {t("emptyHint")}
            </p>
          </div>
        </div>
      ) : (
        <>
          <div className="rounded-lg border border-border bg-card overflow-hidden">
            <Table className="table-fixed w-full">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("colTitle")}</TableHead>
                  <TableHead className="hidden md:table-cell w-[200px]">
                    {t("colAgent")}
                  </TableHead>
                  <TableHead className="hidden lg:table-cell w-[180px]">
                    {t("colOwner")}
                  </TableHead>
                  <TableHead className="hidden md:table-cell w-[120px]">
                    {t("colChannel")}
                  </TableHead>
                  <TableHead className="hidden sm:table-cell w-[160px]">
                    {t("colUpdated")}
                  </TableHead>
                  <TableHead className="w-[60px] text-right">{t("colOpen")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {pageRows.map((s) => (
                  <TableRow key={`${s.agentId}:${s.id}`}>
                    <TableCell className="font-medium">
                      <div className="flex items-center gap-2 min-w-0">
                        {s.thumbnailUrl ? (
                          // eslint-disable-next-line @next/next/no-img-element
                          <img
                            src={s.thumbnailUrl}
                            alt=""
                            className="h-6 w-6 shrink-0 rounded object-cover"
                          />
                        ) : (
                          <ChannelIcon
                            channel={s.channel}
                            className="size-4 shrink-0 text-muted-foreground"
                          />
                        )}
                        <span
                          className="truncate"
                          title={s.title || s.preview || s.id}
                        >
                          {s.title || s.preview || s.id}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell className="hidden md:table-cell text-xs text-muted-foreground whitespace-nowrap">
                      <div className="flex items-center gap-1.5 min-w-0">
                        <Bot className="size-3.5 shrink-0" />
                        <span className="truncate" title={s.agentId}>
                          {s.agentName || s.agentId}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell className="hidden lg:table-cell text-xs text-muted-foreground whitespace-nowrap">
                      <div className="flex items-center gap-1.5 min-w-0">
                        <UserIcon className="size-3.5 shrink-0" />
                        <span className="truncate" title={s.ownerEmail}>
                          {s.ownerDisplayName || s.ownerUsername || s.userId}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell className="hidden md:table-cell text-xs text-muted-foreground whitespace-nowrap">
                      <div className="flex items-center gap-1.5">
                        <ChannelIcon
                          channel={s.channel}
                          className="size-3.5 text-muted-foreground"
                        />
                        <span>{channelLabel(s.channel)}</span>
                      </div>
                    </TableCell>
                    <TableCell className="hidden sm:table-cell text-xs text-muted-foreground whitespace-nowrap">
                      {formatTime(s.updatedAt)}
                    </TableCell>
                    <TableCell className="text-right">
                      <a
                        href={`/agents/${encodeURIComponent(s.agentId)}/chat/${encodeURIComponent(s.id)}/?actAs=${encodeURIComponent(s.userId)}`}
                        target="_blank"
                        rel="noopener noreferrer"
                        title={t("openInNewTab")}
                        className="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground transition-colors"
                      >
                        <ExternalLink className="size-4" />
                      </a>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>

          {totalPages > 1 && (
            <div className="flex items-center justify-between text-sm">
              <span className="text-muted-foreground">
                {t("pageRange", {
                  from: pageStart + 1,
                  to: Math.min(pageStart + PAGE_SIZE, sorted.length),
                  total: sorted.length,
                })}
              </span>
              <div className="flex items-center gap-1">
                <Button
                  variant="outline"
                  size="icon"
                  onClick={() => setPage((p) => Math.max(1, p - 1))}
                  disabled={safePage <= 1}
                >
                  <ChevronLeft className="size-4" />
                </Button>
                <span className="px-3 text-muted-foreground">
                  {t("pageIndicator", { page: safePage, total: totalPages })}
                </span>
                <Button
                  variant="outline"
                  size="icon"
                  onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
                  disabled={safePage >= totalPages}
                >
                  <ChevronRight className="size-4" />
                </Button>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function formatTime(ms?: number): string {
  if (!ms) return "—";
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}
