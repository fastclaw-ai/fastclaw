"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { useTranslations } from "next-intl";
import { getMe } from "@/lib/api";

type SettingsTranslator = ReturnType<typeof useTranslations<"settings">>;

const NAV_ITEMS = (t: SettingsTranslator) => [
  { href: "/settings/general", label: t("navGeneral"), adminOnly: false },
  { href: "/settings/account", label: t("navAccount"), adminOnly: false },
  { href: "/settings/about", label: t("navAbout"), adminOnly: false },
  { href: "/settings/runtime", label: t("navRuntime"), adminOnly: true },
];

export default function SettingsLayout({ children }: { children: React.ReactNode }) {
  const t = useTranslations("settings");
  const pathname = usePathname();
  const [isSuperAdmin, setIsSuperAdmin] = useState(false);
  const items = useMemo(() => NAV_ITEMS(t), [t]);

  useEffect(() => {
    getMe()
      .then((m) => setIsSuperAdmin(m?.user?.role === "super_admin"))
      .catch(() => {});
  }, []);

  const visible = items.filter((i) => !i.adminOnly || isSuperAdmin);

  return (
    <div className="flex flex-col md:flex-row md:gap-8 p-4 md:p-6 max-w-6xl mx-auto md:min-h-[calc(100vh-3.5rem)]">
      <aside className="md:w-48 md:shrink-0 mb-4 md:mb-0">
        <h2 className="text-lg font-semibold tracking-tight mb-3 md:mb-4">{t("title")}</h2>
        {/* Horizontal scroll-tabs on mobile, vertical list on desktop. */}
        <nav className="flex flex-row md:flex-col gap-1 overflow-x-auto md:overflow-visible -mx-1 px-1 md:mx-0 md:px-0">
          {visible.map((it) => {
            const active = pathname === it.href || pathname.startsWith(it.href + "/");
            return (
              <Link
                key={it.href}
                href={it.href}
                className={
                  "shrink-0 md:shrink-0 whitespace-nowrap rounded-md px-3 py-2 text-sm transition " +
                  (active
                    ? "bg-accent text-accent-foreground"
                    : "text-muted-foreground hover:bg-muted hover:text-foreground")
                }
              >
                {it.label}
              </Link>
            );
          })}
        </nav>
      </aside>
      <div className="flex-1 min-w-0">{children}</div>
    </div>
  );
}
