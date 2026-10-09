"use client";

// Settings → Connectors: the person's own third-party accounts (Notion,
// GitHub, …). Their agents read these in the person's own web chats
// (internal/connectors). Connecting opens the platform's sign-in in a new
// tab; this pane re-reads when that tab reports back or the window regains
// focus.

import * as React from "react";
import { CheckCircle2, Loader2, MoreHorizontal, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { useLocale } from "@/components/locale-provider";
import {
  checkConnectorAccount,
  ConnectorApiError,
  disconnectConnectorAccount,
  getConnectorAccess,
  getConnectorAccounts,
  getConnectorCatalog,
  updateConnectorAccount,
  type ConnectorAccess,
  type ConnectorAccount,
  type ConnectorCatalog,
} from "@/lib/api";
import { connectorErrorText, openConnectorAuthorization } from "@/lib/connectors";

const CHANGED_KEY = "fastclaw:connectors-changed";

export function ConnectorsSettingsPage() {
  const { tr, locale } = useLocale();
  const [catalog, setCatalog] = React.useState<ConnectorCatalog | null>(null);
  const [accounts, setAccounts] = React.useState<ConnectorAccount[]>([]);
  const [error, setError] = React.useState("");
  const [category, setCategory] = React.useState("");
  const [busy, setBusy] = React.useState("");
  const [notice, setNotice] = React.useState("");
  const [renaming, setRenaming] = React.useState<{ id: string; name: string } | null>(null);
  const [confirmDisconnect, setConfirmDisconnect] = React.useState<ConnectorAccount | null>(null);
  const [access, setAccess] = React.useState<{ account: ConnectorAccount; data: ConnectorAccess | null } | null>(null);

  const fail = React.useCallback(
    (e: unknown) => setError(connectorErrorText(e instanceof ConnectorApiError ? e.code : "", tr)),
    [tr],
  );

  // Fresh reads: the pane is where a new or removed platform should show
  // up at once, and where a just-finished authorization is reflected.
  const load = React.useCallback(async () => {
    try {
      const cat = await getConnectorCatalog(locale, true);
      setCatalog(cat);
      if (cat.configured) setAccounts(await getConnectorAccounts(true));
      setError("");
    } catch (e) {
      fail(e);
    }
  }, [locale, fail]);

  React.useEffect(() => {
    void load();
    const onFocus = () => void load();
    const onStorage = (e: StorageEvent) => {
      if (e.key === CHANGED_KEY) void load();
    };
    window.addEventListener("focus", onFocus);
    window.addEventListener("storage", onStorage);
    return () => {
      window.removeEventListener("focus", onFocus);
      window.removeEventListener("storage", onStorage);
    };
  }, [load]);

  const act = async (key: string, run: () => Promise<unknown>, done?: string) => {
    setBusy(key);
    setNotice("");
    try {
      await run();
      if (done) setNotice(done);
      await load();
    } catch (e) {
      fail(e);
    } finally {
      setBusy("");
    }
  };

  if (!catalog) {
    return (
      <div className="flex items-center gap-2 p-6 text-sm text-muted-foreground">
        {error || (
          <>
            <Loader2 className="size-4 animate-spin" />
            {tr("Loading…", "加载中…")}
          </>
        )}
      </div>
    );
  }
  if (!catalog.configured) {
    return (
      <div className="max-w-3xl p-4 md:p-6">
        <Header />
        <p className="mt-6 text-sm text-muted-foreground">
          {tr("Connectors are not available on this server yet.", "当前服务器尚未开放连接器。")}
        </p>
      </div>
    );
  }

  const shown = catalog.connectors.filter((c) => !category || c.category === category);
  const byConnector = (name: string) => accounts.filter((a) => a.connector === name);
  // Connected platforms first, then the catalog's own order.
  const ordered = [...shown].sort((a, b) => Number(byConnector(b.name).length > 0) - Number(byConnector(a.name).length > 0));

  return (
    <div className="max-w-3xl p-4 md:p-6">
      <Header />
      {catalog.categories.length > 1 && (
        <div className="mt-5 flex flex-wrap gap-1.5">
          {[{ name: "", title: tr("All", "全部") }, ...catalog.categories].map((c) => (
            <button
              key={c.name || "all"}
              type="button"
              onClick={() => setCategory(c.name)}
              className={`rounded-full px-3 py-1 text-xs transition-colors ${
                category === c.name ? "bg-foreground text-background" : "bg-muted text-muted-foreground hover:text-foreground"
              }`}
            >
              {c.title}
            </button>
          ))}
        </div>
      )}
      {error && <p className="mt-4 text-sm text-destructive">{error}</p>}
      {notice && <p className="mt-4 text-sm text-emerald-600 dark:text-emerald-400">{notice}</p>}

      <div className="mt-4 divide-y rounded-xl border">
        {ordered.map((c) => {
          const mine = byConnector(c.name);
          return (
            <div key={c.name} className="p-4">
              <div className="flex items-start gap-3">
                {/* eslint-disable-next-line @next/next/no-img-element */}
                <img src={c.avatarUrl} alt="" className="mt-0.5 size-8 shrink-0 rounded-lg object-contain" />
                <div className="min-w-0 flex-1">
                  <div className="text-sm font-medium">{c.title}</div>
                  {c.description && <div className="mt-0.5 text-xs text-muted-foreground">{c.description}</div>}
                </div>
                <Button
                  size="sm"
                  variant="outline"
                  className="shrink-0"
                  onClick={() => openConnectorAuthorization({ connector: c.name })}
                >
                  <Plus className="mr-1 size-3.5" />
                  {mine.length > 0 ? tr("Add account", "添加账号") : tr("Connect", "连接")}
                </Button>
              </div>

              {mine.length > 0 && (
                <ul className="mt-3 space-y-1.5 pl-11">
                  {mine.map((a) => (
                    <li key={a.id} className="flex items-center gap-2 rounded-lg bg-muted/40 px-3 py-2 text-sm">
                      {renaming?.id === a.id ? (
                        <form
                          className="flex min-w-0 flex-1 gap-2"
                          onSubmit={(e) => {
                            e.preventDefault();
                            const name = renaming.name;
                            setRenaming(null);
                            void act(a.id, () => updateConnectorAccount(a.id, { name }));
                          }}
                        >
                          <Input
                            autoFocus
                            value={renaming.name}
                            maxLength={80}
                            onChange={(e) => setRenaming({ id: a.id, name: e.target.value })}
                            className="h-7 text-sm"
                          />
                          <Button size="sm" type="submit" className="h-7">{tr("Save", "保存")}</Button>
                          <Button size="sm" type="button" variant="ghost" className="h-7" onClick={() => setRenaming(null)}>
                            {tr("Cancel", "取消")}
                          </Button>
                        </form>
                      ) : (
                        <>
                          <span className="min-w-0 truncate">{a.name}</span>
                          {a.accountName && a.accountName !== a.name && (
                            <span className="min-w-0 truncate text-xs text-muted-foreground">{a.accountName}</span>
                          )}
                          {a.isDefault && mine.length > 1 && (
                            <span className="shrink-0 rounded bg-background px-1.5 py-0.5 text-[10px] text-muted-foreground">
                              {tr("Default", "默认")}
                            </span>
                          )}
                          {a.status !== "connected" ? (
                            <button
                              type="button"
                              className="shrink-0 text-xs text-amber-600 underline-offset-2 hover:underline dark:text-amber-400"
                              onClick={() => openConnectorAuthorization({ reconnectId: a.id })}
                            >
                              {tr("Reconnect", "重新授权")}
                            </button>
                          ) : a.needsAccess ? (
                            <button
                              type="button"
                              className="shrink-0 text-xs text-amber-600 underline-offset-2 hover:underline dark:text-amber-400"
                              onClick={() => openConnectorAuthorization({ accessId: a.id })}
                            >
                              {tr("Grant access", "授权访问")}
                            </button>
                          ) : (
                            <CheckCircle2 className="size-3.5 shrink-0 text-emerald-500" aria-label={tr("Connected", "已连接")} />
                          )}
                          <span className="flex-1" />
                          {busy === a.id && <Loader2 className="size-3.5 animate-spin text-muted-foreground" />}
                          <DropdownMenu>
                            <DropdownMenuTrigger
                              render={
                                <button
                                  type="button"
                                  className="inline-flex size-7 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-background hover:text-foreground"
                                  aria-label={tr("Account actions", "账号操作")}
                                >
                                  <MoreHorizontal className="size-4" />
                                </button>
                              }
                            />
                            <DropdownMenuContent align="end" className="w-44">
                              {!a.isDefault && a.status === "connected" && (
                                <DropdownMenuItem onClick={() => void act(a.id, () => updateConnectorAccount(a.id, { isDefault: true }))}>
                                  {tr("Set as default", "设为默认")}
                                </DropdownMenuItem>
                              )}
                              <DropdownMenuItem onClick={() => setRenaming({ id: a.id, name: a.displayName || a.name })}>
                                {tr("Rename", "重命名")}
                              </DropdownMenuItem>
                              <DropdownMenuItem
                                onClick={() =>
                                  void act(a.id, async () => {
                                    const r = await checkConnectorAccount(a.id);
                                    setNotice(tr("Working — {{n}} tools available.", "连接正常，可用工具 {{n}} 个。", { n: r.toolCount }));
                                  })
                                }
                              >
                                {tr("Check connection", "检查连接")}
                              </DropdownMenuItem>
                              <DropdownMenuItem onClick={() => openConnectorAuthorization({ reconnectId: a.id })}>
                                {tr("Reconnect", "重新授权")}
                              </DropdownMenuItem>
                              <DropdownMenuItem
                                onClick={() => {
                                  setAccess({ account: a, data: null });
                                  getConnectorAccess(a.id)
                                    .then((data) => setAccess({ account: a, data }))
                                    .catch((e) => {
                                      setAccess(null);
                                      fail(e);
                                    });
                                }}
                              >
                                {tr("Access scope", "授权范围")}
                              </DropdownMenuItem>
                              <DropdownMenuSeparator />
                              <DropdownMenuItem className="text-destructive" onClick={() => setConfirmDisconnect(a)}>
                                {tr("Disconnect", "断开")}
                              </DropdownMenuItem>
                            </DropdownMenuContent>
                          </DropdownMenu>
                        </>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          );
        })}
      </div>

      <AlertDialog open={!!confirmDisconnect} onOpenChange={(v) => !v && setConfirmDisconnect(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tr("Disconnect account", "断开账号")}</AlertDialogTitle>
            <AlertDialogDescription>
              {tr(
                "Disconnect {{name}}? Your agents will no longer be able to read it.",
                "要断开 {{name}} 吗？断开后你的 Agent 将无法再读取它。",
                { name: confirmDisconnect?.name || "" },
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tr("Cancel", "取消")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => {
                const a = confirmDisconnect;
                setConfirmDisconnect(null);
                if (a) void act(a.id, () => disconnectConnectorAccount(a.id), tr("Disconnected.", "已断开。"));
              }}
            >
              {tr("Disconnect", "断开")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={!!access} onOpenChange={(v) => !v && setAccess(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tr("Access scope", "授权范围")}</AlertDialogTitle>
            <AlertDialogDescription>
              {tr(
                "What {{name}} shares with your agents beyond its sign-in.",
                "除了登录本身，{{name}} 还向你的 Agent 开放了以下资源。",
                { name: access?.account.name || "" },
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {!access?.data ? (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
              {tr("Loading…", "加载中…")}
            </div>
          ) : access.data.grants.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {access.data.canAdd
                ? tr("Nothing is shared yet.", "目前还没有开放任何资源。")
                : tr("This platform has no separate access step: the account reads what it can read on the platform.", "该平台没有单独的授权范围：账号在平台上能读到什么，Agent 就能读到什么。")}
            </p>
          ) : (
            <ul className="max-h-64 space-y-1 overflow-auto text-sm">
              {access.data.grants.map((g) => (
                <li key={String(g.id)} className="flex items-center gap-2">
                  <span className="text-xs text-muted-foreground">{g.type}</span>
                  <span className="min-w-0 truncate">{g.name}</span>
                  {g.selection === "selected" && (
                    <span className="text-xs text-muted-foreground">{tr("(selected)", "（部分）")}</span>
                  )}
                  <span className="flex-1" />
                  {g.manage_url && (
                    <a href={g.manage_url} target="_blank" rel="noreferrer" className="shrink-0 text-xs underline-offset-2 hover:underline">
                      {tr("Manage", "管理")}
                    </a>
                  )}
                </li>
              ))}
            </ul>
          )}
          <AlertDialogFooter>
            {access?.data?.canAdd && (
              <Button variant="outline" onClick={() => access && openConnectorAuthorization({ accessId: access.account.id })}>
                {tr("Grant more", "授权更多")}
              </Button>
            )}
            <AlertDialogCancel>{tr("Close", "关闭")}</AlertDialogCancel>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function Header() {
  const { tr } = useLocale();
  return (
    <div>
      <h2 className="text-lg font-semibold">{tr("Connectors", "连接器")}</h2>
      <p className="mt-1 text-sm text-muted-foreground">
        {tr(
          "Connect your own accounts so your agents can read them when you chat with them on the web. Only you can use them, and only with agents you own.",
          "连接你自己的账号，在网页上和你的 Agent 对话时，它们就能读取这些账号里的数据。只有你本人、并且只在你自己的 Agent 里可以使用。",
        )}
      </p>
    </div>
  );
}
