"use client";

import { useEffect, useState } from "react";
import { getMe } from "@/lib/api";
import { useLocale } from "@/components/locale-provider";

// ActAsBanner shows a sticky read-only warning whenever a super_admin is
// browsing another user's resources via ?actAs=. It hides itself for
// regular users and for super_admins viewing their own data.
export function ActAsBanner() {
  const { tr } = useLocale();
  const [actAs, setActAs] = useState<string>("");

  useEffect(() => {
    let aborted = false;
    (async () => {
      const me = await getMe();
      if (aborted) return;
      if (me.actAsUserId) setActAs(me.actAsUserId);
    })();
    return () => { aborted = true; };
  }, []);

  if (!actAs) return null;
  return (
    <div className="sticky top-0 z-50 bg-amber-700/80 px-4 py-2 text-center text-xs text-amber-50 backdrop-blur">
      {tr("Viewing as", "正在查看用户")} <code className="font-mono">{actAs}</code>
      {tr(" · read-only — changes are blocked", " · 只读模式 — 已禁止修改")}
    </div>
  );
}
