"use client";

import { useState } from "react";
import { ConnectorsSettingsPage } from "@/components/connectors-settings";
import { useLocale } from "@/components/locale-provider";

// Console → Tools: the caller's own tool settings. Like models and skills,
// what a user sets here is used first and the deployment's tools
// (Admin → Tools) are the fallback. For now the only entry is the user's
// own connector accounts; per-user tool providers (search, image, …) can
// join the rail later.
type Section = "connectors";

export default function ConsoleToolsPage() {
  const { tr } = useLocale();
  const [active, setActive] = useState<Section>("connectors");
  const sections: { id: Section; label: string }[] = [{ id: "connectors", label: tr("Connectors", "连接器") }];

  return (
    <div className="flex flex-col md:flex-row md:gap-8 p-4 md:p-6 max-w-6xl mx-auto md:min-h-[calc(100vh-3.5rem)]">
      <aside className="md:w-48 md:shrink-0 mb-4 md:mb-0">
        <h2 className="text-lg font-semibold tracking-tight mb-3 md:mb-4">{tr("Tools", "工具")}</h2>
        <nav className="flex flex-row md:flex-col gap-1 overflow-x-auto md:overflow-visible -mx-1 px-1 md:mx-0 md:px-0">
          {sections.map((s) => (
            <button
              key={s.id}
              type="button"
              onClick={() => setActive(s.id)}
              className={
                "shrink-0 whitespace-nowrap rounded-md px-3 py-2 text-sm text-left transition " +
                (active === s.id
                  ? "bg-accent text-accent-foreground"
                  : "text-muted-foreground hover:bg-muted hover:text-foreground")
              }
            >
              {s.label}
            </button>
          ))}
        </nav>
      </aside>
      <div className="flex-1 min-w-0">{active === "connectors" && <ConnectorsSettingsPage embedded />}</div>
    </div>
  );
}
