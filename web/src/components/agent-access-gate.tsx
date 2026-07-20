"use client";

import { useEffect, useState } from "react";
import { usePathname } from "next/navigation";
import { useTranslations } from "next-intl";
import { Bot } from "lucide-react";
import { getAgentStatus } from "@/lib/api";

// Pull the agent id straight from the URL. Under output:'export' the
// HTML served for /agents/agt_xxx/chat/ is actually the prebuilt
// /agents/default/chat/index.html (see the server's static fallback);
// useParams() in that bundle resolves to {id: "default"} during the
// initial render which would fail the access probe even for the real
// owner. Reading the pathname is unambiguous on the client.
function agentIdFromPath(pathname: string | null | undefined): string {
  if (!pathname) return "";
  // Match /agents/<id>(/...)? — strict on the prefix so we don't
  // accidentally pull an id off some other route.
  const m = pathname.match(/^\/agents\/([^/]+)/);
  return m ? m[1] : "";
}

// AgentAccessGate probes /api/agents/{id} once on mount and:
//   - 200: renders children (caller is owner / super_admin / public-link
//     visitor / apikey ACL grantee)
//   - 401: redirects to /login (handled at apiFetch level normally)
//   - 403/404 or any other failure: shows a "no access" screen that
//     overlays the entire viewport (sidebar included), so a non-owner
//     can't peek at the agent's name / sessions / admin tabs by typing
//     the URL.
//
// Lives at the [id]/layout level so every nested route (chat, customize,
// skills, etc.) inherits the same gate. The agent id is read from the
// URL via useParams() — passing it from the layout's `params` doesn't
// work under output:'export' because params resolve at build time to
// whatever generateStaticParams returned ("default"), not the URL.
export default function AgentAccessGate({
  children,
}: {
  children: React.ReactNode;
}) {
  const t = useTranslations("accessGate");
  const pathname = usePathname();
  const agentId = agentIdFromPath(pathname);
  const [state, setState] = useState<"checking" | "ok" | "denied">("checking");

  useEffect(() => {
    // The "default" id is the prebuilt static-export placeholder, not
    // a real agent — skip the probe and let children render. The real
    // /agents/default/* route is super_admin's local-mode dashboard
    // which has its own server-side gating already.
    if (!agentId || agentId === "default") {
      setState("ok");
      return;
    }
    let aborted = false;
    setState("checking");
    getAgentStatus(agentId)
      .then(({ status, agent }) => {
        if (aborted) return;
        if (status === 200 && agent) {
          setState("ok");
          return;
        }
        setState("denied");
      })
      .catch(() => {
        if (!aborted) setState("denied");
      });
    return () => {
      aborted = true;
    };
  }, [agentId]);

  if (state === "checking") {
    // Full-viewport placeholder while the probe runs — z-50 lifts it
    // over the AppShell sidebar so non-owners don't briefly see the
    // chat UI / admin tabs while the 403 is in flight.
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-background">
        <div className="h-2 w-2 animate-pulse rounded-full bg-muted-foreground/40" />
      </div>
    );
  }

  if (state === "denied") {
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-background p-6">
        <div className="max-w-md text-center space-y-4">
          <div className="mx-auto flex h-14 w-14 items-center justify-center rounded-2xl bg-muted/60">
            <Bot className="h-7 w-7 text-muted-foreground" />
          </div>
          <h2 className="text-lg font-semibold">{t("deniedTitle")}</h2>
          <p className="text-sm text-muted-foreground">
            {t("deniedDescription")}
          </p>
        </div>
      </div>
    );
  }

  return <>{children}</>;
}
