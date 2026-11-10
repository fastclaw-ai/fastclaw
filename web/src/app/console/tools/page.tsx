"use client";

import { useState } from "react";
import Link from "next/link";
import { OwnConnanyKey } from "@/components/connectors-own-key";
import { useLocale } from "@/components/locale-provider";

// Console → Tools: the caller's own tool settings. Like models and skills,
// what a user sets here is used first and the deployment's tools
// (Admin → Tools) are the fallback. Settings only: for now which Connany
// holds the person's accounts (their own key or this site's); the accounts
// themselves are in Console → Connectors. Per-user tool providers
// (search, image, …) can join the rail later.
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
      <div className="flex-1 min-w-0">{active === "connectors" && <ConnectorsConfig />}</div>
    </div>
  );
}

function ConnectorsConfig() {
  const { tr } = useLocale();
  return (
    <div className="max-w-3xl">
      <h2 className="text-2xl font-semibold tracking-tight">{tr("Connectors", "连接器")}</h2>
      <p className="mt-1 text-sm text-muted-foreground">
        {tr(
          "Which Connany connector service holds your accounts: your own project key, or the one this site provides. Connect and manage the accounts in ",
          "你的账号接在哪个 Connany 连接服务上：你自己的项目 Key，或本站提供的服务。连接和管理账号请到",
        )}
        <Link href="/console/connectors/" className="text-foreground underline underline-offset-4">
          {tr("Connectors", "「连接器」")}
        </Link>
        {tr(".", "。")}
      </p>
      <OwnConnanyKey />
    </div>
  );
}
