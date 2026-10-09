"use client";

// The card an agent puts in a conversation when it needs one of the
// person's accounts (connectors tool: request_connection / request_access).
// The authorization link is minted only when the person clicks (in a new
// tab, /connectors/open). When it succeeds the server wakes the
// conversation with a [connector] note and the agent carries on; cancelling
// tells the agent once, so it answers without the account.

import * as React from "react";
import { CheckCircle2, Loader2, Plug } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useLocale } from "@/components/locale-provider";
import {
  cancelConnectorRequest,
  ConnectorApiError,
  getConnectorRequest,
  recheckConnectorRequest,
  type ConnectorRequestView,
} from "@/lib/api";
import { connectorErrorText, connectorRequestSettled, openConnectorAuthorization } from "@/lib/connectors";

export interface ConnectorRequestRef {
  id: string;
  connector: string;
  title: string;
  kind: "connect" | "reconnect" | "access";
}

const POLL_MS = 5_000;

export function ConnectorRequestCard({ request }: { request: ConnectorRequestRef }) {
  const { tr } = useLocale();
  const [view, setView] = React.useState<ConnectorRequestView | null>(null);
  const [error, setError] = React.useState("");
  const [working, setWorking] = React.useState(false);

  const refresh = React.useCallback(
    async (recheck = false) => {
      try {
        setView(await (recheck ? recheckConnectorRequest(request.id) : getConnectorRequest(request.id)));
        setError("");
      } catch (e) {
        setError(connectorErrorText(e instanceof ConnectorApiError ? e.code : "", tr));
      }
    },
    [request.id, tr],
  );

  React.useEffect(() => {
    void refresh();
  }, [refresh]);

  // While the person is authorizing in the other tab: poll, and re-read
  // at once when that tab reports back or this one regains focus.
  const pending = view?.status === "pending";
  React.useEffect(() => {
    if (!pending) return;
    const timer = setInterval(() => {
      if (!document.hidden) void refresh();
    }, POLL_MS);
    const onFocus = () => void refresh();
    const onStorage = (e: StorageEvent) => {
      if (e.key === "fastclaw:connectors-changed") void refresh();
    };
    window.addEventListener("focus", onFocus);
    window.addEventListener("storage", onStorage);
    return () => {
      clearInterval(timer);
      window.removeEventListener("focus", onFocus);
      window.removeEventListener("storage", onStorage);
    };
  }, [pending, refresh]);

  const open = () => {
    openConnectorAuthorization({ requestId: request.id });
    // The open page marks the request pending; start watching it.
    setTimeout(() => void refresh(), 1500);
  };

  const run = async (fn: () => Promise<unknown>) => {
    setWorking(true);
    try {
      await fn();
    } finally {
      setWorking(false);
    }
  };

  const status = view?.status;
  const action =
    request.kind === "access"
      ? tr("Grant access on {{title}}", "在 {{title}} 中授权访问", { title: request.title })
      : request.kind === "reconnect"
        ? tr("Reconnect {{title}}", "重新授权 {{title}}", { title: request.title })
        : tr("Connect {{title}}", "连接 {{title}}", { title: request.title });

  return (
    <div className="my-2 flex max-w-md items-start gap-3 rounded-xl border bg-card p-3">
      {/* eslint-disable-next-line @next/next/no-img-element */}
      <img
        src={`/api/connectors/${encodeURIComponent(request.connector)}/avatar`}
        alt=""
        className="mt-0.5 size-8 shrink-0 rounded-lg object-contain"
        onError={(e) => {
          e.currentTarget.style.visibility = "hidden";
        }}
      />
      <div className="min-w-0 flex-1 space-y-2">
        <div className="text-sm font-medium">{request.title}</div>
        {!view && !error && <Loader2 className="size-4 animate-spin text-muted-foreground" />}

        {(status === "connected" || status === "granted") && (
          <div className="flex items-center gap-1.5 text-sm text-emerald-600 dark:text-emerald-400">
            <CheckCircle2 className="size-4" />
            {status === "granted" ? tr("Access granted", "已授权访问") : tr("Connected", "已连接")}
          </div>
        )}

        {status === "pending" && (
          <>
            <p className="text-xs text-muted-foreground">
              {tr("Finish in the tab that opened. This updates on its own.", "请在新打开的标签页中完成授权，完成后这里会自动更新。")}
            </p>
            <div className="flex flex-wrap gap-2">
              <Button size="sm" disabled={working} onClick={() => run(() => refresh(true))}>
                {tr("I've finished", "我已完成")}
              </Button>
              <Button size="sm" variant="outline" onClick={open}>
                {tr("Open again", "重新打开")}
              </Button>
              <Button size="sm" variant="ghost" disabled={working} onClick={() => run(async () => setView(await cancelConnectorRequest(request.id)))}>
                {tr("Cancel", "取消")}
              </Button>
            </div>
          </>
        )}

        {view && !connectorRequestSettled(view.status) && view.status !== "pending" && (
          <div className="flex flex-wrap gap-2">
            <Button size="sm" onClick={open}>
              <Plug className="mr-1.5 size-3.5" />
              {action}
            </Button>
            <Button size="sm" variant="ghost" disabled={working} onClick={() => run(async () => setView(await cancelConnectorRequest(request.id)))}>
              {tr("Not now", "暂不连接")}
            </Button>
          </div>
        )}

        {(status === "cancelled" || status === "expired" || status === "error") && (
          <div className="space-y-2">
            <p className="text-xs text-muted-foreground">
              {status === "cancelled"
                ? tr("Cancelled.", "已取消。")
                : status === "expired"
                  ? tr("The link expired.", "授权链接已过期。")
                  : tr("Authorization failed.", "授权失败。")}
            </p>
            <Button size="sm" variant="outline" onClick={open}>
              {action}
            </Button>
          </div>
        )}

        {error && <p className="text-xs text-destructive">{error}</p>}
      </div>
    </div>
  );
}
