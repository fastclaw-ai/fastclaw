"use client";

// Shared helpers for the connectors UI (settings pane, conversation card,
// the /connectors/open and /connectors/done pages).

type Tr = (en: string, zh: string, vars?: Record<string, string | number>) => string;

export type ConnectorOpenTarget =
  | { requestId: string }
  | { connector: string }
  | { reconnectId: string }
  | { accessId: string };

/** Opens the authorization in a new tab. The tab mints the one-use link
 *  when it loads (/connectors/open), so no link is ever stored or shown. */
export function openConnectorAuthorization(target: ConnectorOpenTarget): Window | null {
  const [key, value] = Object.entries(target)[0];
  return window.open(`/connectors/open/?${new URLSearchParams({ [key]: value })}`, "_blank");
}

/** Words a closed connector error code for people. */
export function connectorErrorText(code: string, tr: Tr): string {
  switch (code) {
    case "not_configured":
      return tr("Connectors are not available on this server yet.", "当前服务器尚未开放连接器。");
    case "rate_limited":
      return tr("Too many requests right now. Try again in a minute.", "请求太频繁，请稍后再试。");
    case "not_found":
      return tr("That account or request no longer exists.", "该账号或请求已不存在。");
    case "connector_not_found":
      return tr("That platform is not available.", "该平台不可用。");
    case "already_done":
      return tr("This was already completed.", "已经完成了。");
    case "no_access_step":
      return tr("This platform has no separate access step.", "该平台没有单独的授权范围设置。");
    case "reauth_required":
      return tr("The account needs to be reconnected first.", "该账号需要先重新授权。");
    case "invalid_request":
      return tr("Invalid request.", "请求无效。");
    case "unauthorized":
      return tr("Please sign in again.", "请重新登录。");
    default:
      return tr("The connector service is unavailable right now. Try again later.", "连接服务暂时不可用，请稍后再试。");
  }
}

/** A request is settled once it can no longer change by itself. */
export function connectorRequestSettled(status: string): boolean {
  return status === "connected" || status === "granted" || status === "cancelled" || status === "error" || status === "expired";
}
