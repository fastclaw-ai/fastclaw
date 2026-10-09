"use client";

// Opens a connector authorization in this tab: mints the one-use link
// (POST /api/connector-authorizations) and replaces itself with it. The
// link is minted here, on the click, so it is never stored or shown in a
// conversation. The platform sends the browser back to /connectors/done.

import { useEffect, useState } from "react";
import { Loader2 } from "lucide-react";
import { authorizeConnector, ConnectorApiError } from "@/lib/api";
import { connectorErrorText } from "@/lib/connectors";
import { useLocale } from "@/components/locale-provider";

export default function ConnectorOpenPage() {
  const { tr } = useLocale();
  const [error, setError] = useState("");

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const target =
      params.get("requestId") ? { requestId: params.get("requestId")! }
      : params.get("connector") ? { connector: params.get("connector")! }
      : params.get("reconnectId") ? { reconnectId: params.get("reconnectId")! }
      : params.get("accessId") ? { accessId: params.get("accessId")! }
      // Nothing named: the server answers invalid_request.
      : { connector: "" };
    authorizeConnector(target)
      .then(({ url }) => window.location.replace(url))
      .catch((e) => setError(e instanceof ConnectorApiError ? e.code : "service_unavailable"));
  }, []);

  return (
    <main className="flex min-h-dvh items-center justify-center bg-background p-6">
      <div className="max-w-sm text-center">
        {error ? (
          <>
            <p className="text-sm text-destructive">{connectorErrorText(error, tr)}</p>
            <p className="mt-2 text-xs text-muted-foreground">{tr("You can close this tab.", "可以关闭此标签页。")}</p>
          </>
        ) : (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            {tr("Opening authorization…", "正在打开授权页面…")}
          </div>
        )}
      </div>
    </main>
  );
}
