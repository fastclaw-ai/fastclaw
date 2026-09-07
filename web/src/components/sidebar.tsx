"use client";

import * as React from "react";
import { usePathname, useSearchParams } from "next/navigation";
import { AppSidebar } from "@/components/app-sidebar";
import {
  SidebarInset,
  SidebarProvider,
  SidebarTrigger,
} from "@/components/ui/sidebar";
// Page-header slot: pages call `usePageHeader(<jsx/>)` to render content
// to the right of the sidebar-trigger in the global sticky header. When
// the page unmounts the slot empties. Chat uses this to show an editable
// session title next to the sidebar toggle.
interface PageHeaderContextValue {
  setNode: (node: React.ReactNode) => void;
}
const PageHeaderContext = React.createContext<PageHeaderContextValue | null>(
  null,
);

export function usePageHeader(node: React.ReactNode, deps: React.DependencyList = []) {
  const ctx = React.useContext(PageHeaderContext);
  React.useEffect(() => {
    if (!ctx) return;
    ctx.setNode(node);
    return () => ctx.setNode(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
}

export function SidebarLayout({ children }: { children: React.ReactNode }) {
  const [headerNode, setHeaderNode] = React.useState<React.ReactNode>(null);
  const searchParams = useSearchParams();
  // `?actAs=<uid>` means an admin / agent owner is viewing another
  // user's chat read-only. The platform sidebar belongs to the viewer's
  // session, not the impersonated user — hiding it (and its collapse
  // toggle) keeps the surface focused on the conversation being inspected.
  const isActAsView = !!searchParams?.get("actAs");
  const isConversationRoute = /^\/agents\/[^/]+\/(chat|project)(?:\/|$)/.test(
    usePathname() || "",
  );

  const headerCtx = React.useMemo<PageHeaderContextValue>(
    () => ({ setNode: setHeaderNode }),
    [],
  );

  if (isActAsView) {
    return (
      <PageHeaderContext.Provider value={headerCtx}>
        <div className="flex min-h-svh flex-col">
          <header
            className={`sticky top-0 z-20 flex items-center bg-background/90 backdrop-blur ${
              isConversationRoute ? "h-14" : "h-12 gap-2 px-3"
            }`}
          >
            {headerNode}
          </header>
          <div className="flex-1">{children}</div>
        </div>
      </PageHeaderContext.Provider>
    );
  }

  return (
    <PageHeaderContext.Provider value={headerCtx}>
      <SidebarProvider
        key={isConversationRoute ? "conversation-sidebar" : "platform-sidebar"}
        resizeDefaultWidth={isConversationRoute ? 320 : undefined}
        resizeMinWidth={isConversationRoute ? 240 : undefined}
        resizeMaxWidth={isConversationRoute ? 420 : undefined}
        resizeStorageKey={isConversationRoute ? "fastclaw:bot-sidebar-width" : undefined}
        resizeViewportReserve={isConversationRoute ? 480 : undefined}
        style={
          isConversationRoute
            ? ({
                "--sidebar-width-icon": "64px",
              } as React.CSSProperties)
            : undefined
        }
      >
        <AppSidebar />
        <SidebarInset className={isConversationRoute ? "min-w-0 overflow-hidden" : undefined}>
          <header
            className={`sticky top-0 z-20 flex items-center bg-background/90 backdrop-blur ${
              isConversationRoute
                ? "h-14"
                : "h-12 gap-2 px-3"
            }`}
          >
            <SidebarTrigger
              className={isConversationRoute ? "ml-2 shrink-0 md:hidden" : "-ml-1"}
            />
            {headerNode}
          </header>
          <div className="flex-1">{children}</div>
        </SidebarInset>
      </SidebarProvider>
    </PageHeaderContext.Provider>
  );
}
