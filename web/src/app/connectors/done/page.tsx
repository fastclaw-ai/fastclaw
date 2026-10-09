"use client";

// Where the connector service sends the browser after an authorization.
// connany_session_id is only a pointer: the server looks it up among the
// requests this person started and asks the service how it ended, then
// wakes the conversation that asked (if any). The redirect proves nothing
// on its own.

import { useEffect, useState } from "react";
import { CheckCircle2, CircleAlert, Loader2 } from "lucide-react";
import { confirmConnectorReturn, ConnectorApiError } from "@/lib/api";
import { connectorErrorText } from "@/lib/connectors";
import { useLocale } from "@/components/locale-provider";

type State = { kind: "loading" } | { kind: "done" } | { kind: "failed"; text: string };

export default function ConnectorDonePage() {
  const { tr } = useLocale();
  const [state, setState] = useState<State>({ kind: "loading" });

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const sessionId = params.get("connany_session_id") || "";
    const platformError = params.get("connany_status") === "error";
    // No session id: the server answers invalid_request.
    confirmConnectorReturn(sessionId)
      .then((r) => {
        if (r.status === "connected" || r.status === "granted") {
          setState({ kind: "done" });
          // Settings panes and conversation cards in other tabs re-read.
          try {
            localStorage.setItem("fastclaw:connectors-changed", String(Date.now()));
          } catch {}
          // Best effort: only works when this tab was opened by script.
          setTimeout(() => window.close(), 1500);
        } else if (platformError || r.status === "error") {
          setState({
            kind: "failed",
            text: params.get("connany_error") === "access_denied"
              ? tr("Authorization was declined.", "授权已被拒绝。")
              : tr("Authorization failed. Go back and try again.", "授权失败，请返回后重试。"),
          });
        } else if (r.status === "expired") {
          setState({ kind: "failed", text: tr("This authorization link expired. Go back and try again.", "授权链接已过期，请返回后重试。") });
        } else {
          setState({ kind: "failed", text: tr("Authorization isn't finished yet.", "授权尚未完成。") });
        }
      })
      .catch((e) => setState({ kind: "failed", text: connectorErrorText(e instanceof ConnectorApiError ? e.code : "", tr) }));
  }, [tr]);

  return (
    <main className="flex min-h-dvh items-center justify-center bg-background p-6">
      <div className="flex max-w-sm flex-col items-center gap-3 text-center">
        {state.kind === "loading" && (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            {tr("Finishing up…", "正在完成…")}
          </div>
        )}
        {state.kind === "done" && (
          <>
            <CheckCircle2 className="size-8 text-emerald-500" />
            <p className="text-sm font-medium">{tr("Connected", "已连接")}</p>
            <p className="text-xs text-muted-foreground">
              {tr("You can close this tab and return to FastClaw.", "可以关闭此标签页，回到 FastClaw。")}
            </p>
          </>
        )}
        {state.kind === "failed" && (
          <>
            <CircleAlert className="size-8 text-destructive" />
            <p className="text-sm">{state.text}</p>
            <p className="text-xs text-muted-foreground">{tr("You can close this tab.", "可以关闭此标签页。")}</p>
          </>
        )}
      </div>
    </main>
  );
}
