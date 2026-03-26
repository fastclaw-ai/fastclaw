#!/usr/bin/env node
/**
 * FastClaw WeChat Channel Plugin (ilink)
 *
 * JSON-RPC subprocess plugin that bridges WeChat messages via the ilink bot API.
 *
 * Protocol:
 *   FastClaw → plugin:  initialize, channel.send, shutdown (JSON-RPC requests)
 *   plugin → FastClaw:  message.inbound (JSON-RPC notifications)
 *
 * Accounts are stored as JSON files in the accounts directory
 * (default: ~/.fastclaw/weixin/accounts/).
 */

import { createInterface } from "node:readline";
import { readFileSync, writeFileSync, existsSync, mkdirSync } from "node:fs";
import { join, dirname } from "node:path";
import { homedir } from "node:os";
import crypto from "node:crypto";

// ── State ──

const DEFAULT_BASE_URL = "https://ilinkai.weixin.qq.com";
const LONG_POLL_TIMEOUT_MS = 35_000;
const MAX_CONSECUTIVE_FAILURES = 3;
const BACKOFF_DELAY_MS = 30_000;
const RETRY_DELAY_MS = 2_000;
const SESSION_EXPIRED_ERRCODE = -14;

let config = {};
let accounts = [];
const contextTokens = new Map(); // accountId:userId → contextToken
const syncBufs = new Map();      // accountId → get_updates_buf
const abortControllers = new Map(); // accountId → AbortController

// ── JSON-RPC I/O ──

function send(obj) {
  process.stdout.write(JSON.stringify(obj) + "\n");
}

function respond(id, result) {
  send({ jsonrpc: "2.0", result, id });
}

function respondError(id, code, message) {
  send({ jsonrpc: "2.0", error: { code, message }, id });
}

function notify(method, params) {
  send({ jsonrpc: "2.0", method, params });
}

function notifyInbound({ channel, chatId, userId, text, peerKind, senderName }) {
  notify("message.inbound", { channel, chatId, userId, text, peerKind, senderName });
}

function log(msg) {
  process.stderr.write(`[weixin] ${msg}\n`);
}

// ── ilink API ──

function randomWechatUin() {
  const uint32 = crypto.randomBytes(4).readUInt32BE(0);
  return Buffer.from(String(uint32), "utf-8").toString("base64");
}

function buildHeaders(token) {
  return {
    "Content-Type": "application/json",
    AuthorizationType: "ilink_bot_token",
    Authorization: token ? `Bearer ${token}` : "",
    "X-WECHAT-UIN": randomWechatUin(),
  };
}

async function ilinkPost(baseUrl, endpoint, body, token, timeoutMs = 15000) {
  const url = `${baseUrl.replace(/\/$/, "")}/${endpoint}`;
  const bodyStr = JSON.stringify({
    ...body,
    base_info: { channel_version: "fastclaw-weixin-1.0.0" },
  });
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const resp = await fetch(url, {
      method: "POST",
      headers: { ...buildHeaders(token), "Content-Length": String(Buffer.byteLength(bodyStr)) },
      body: bodyStr,
      signal: controller.signal,
    });
    clearTimeout(timer);
    return JSON.parse(await resp.text());
  } catch (err) {
    clearTimeout(timer);
    if (err.name === "AbortError") return { ret: 0, msgs: [] };
    throw err;
  }
}

async function getUpdates(baseUrl, token, getUpdatesBuf, timeoutMs) {
  return ilinkPost(baseUrl, "ilink/bot/getupdates", {
    get_updates_buf: getUpdatesBuf || "",
  }, token, timeoutMs);
}

async function sendTextMessage(baseUrl, token, to, text, contextToken) {
  const clientId = `fastclaw-wx-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  return ilinkPost(baseUrl, "ilink/bot/sendmessage", {
    msg: {
      from_user_id: "",
      to_user_id: to,
      client_id: clientId,
      message_type: 2,  // BOT
      message_state: 2, // FINISH
      item_list: [{ type: 1, text_item: { text } }],
      context_token: contextToken || undefined,
    },
  }, token);
}

// ── Account management ──

function resolveAccountsDir() {
  if (config.accountsDir) return config.accountsDir;
  return join(homedir(), ".fastclaw", "weixin", "accounts");
}

function resolveDataDir() {
  return join(homedir(), ".fastclaw", "weixin", "data");
}

function loadAccounts() {
  const dir = resolveAccountsDir();
  const indexPath = join(dir, "..", "accounts.json");
  // Try index file first
  if (existsSync(indexPath)) {
    try {
      const ids = JSON.parse(readFileSync(indexPath, "utf-8"));
      return ids.map(id => {
        try {
          const data = JSON.parse(readFileSync(join(dir, `${id}.json`), "utf-8"));
          return {
            accountId: id,
            token: data.token,
            baseUrl: data.baseUrl || config.baseUrl || DEFAULT_BASE_URL,
            userId: data.userId,
          };
        } catch { return null; }
      }).filter(Boolean);
    } catch {}
  }
  // Fallback: also check xiama-weixin dir
  const altDir = join(homedir(), ".fastclaw", "xiama-weixin", "accounts");
  const altIndex = join(altDir, "..", "accounts.json");
  if (existsSync(altIndex)) {
    try {
      const ids = JSON.parse(readFileSync(altIndex, "utf-8"));
      return ids.map(id => {
        try {
          const data = JSON.parse(readFileSync(join(altDir, `${id}.json`), "utf-8"));
          return {
            accountId: id,
            token: data.token,
            baseUrl: data.baseUrl || config.baseUrl || DEFAULT_BASE_URL,
            userId: data.userId,
          };
        } catch { return null; }
      }).filter(Boolean);
    } catch {}
  }
  return [];
}

// ── Persistence ──

function loadSyncBuf(accountId) {
  try {
    const dir = resolveDataDir();
    const p = join(dir, `${accountId}.sync-buf`);
    if (existsSync(p)) return readFileSync(p, "utf-8");
  } catch {}
  return "";
}

function saveSyncBuf(accountId, buf) {
  try {
    const dir = resolveDataDir();
    mkdirSync(dir, { recursive: true });
    writeFileSync(join(dir, `${accountId}.sync-buf`), buf, "utf-8");
  } catch {}
}

function loadContextTokens(accountId) {
  try {
    const dir = resolveDataDir();
    const p = join(dir, `${accountId}.context-tokens.json`);
    if (existsSync(p)) {
      const data = JSON.parse(readFileSync(p, "utf-8"));
      for (const [userId, token] of Object.entries(data)) {
        contextTokens.set(`${accountId}:${userId}`, token);
      }
    }
  } catch {}
}

function saveContextTokens(accountId) {
  try {
    const dir = resolveDataDir();
    mkdirSync(dir, { recursive: true });
    const prefix = `${accountId}:`;
    const data = {};
    for (const [k, v] of contextTokens) {
      if (k.startsWith(prefix)) data[k.slice(prefix.length)] = v;
    }
    writeFileSync(join(dir, `${accountId}.context-tokens.json`), JSON.stringify(data), "utf-8");
  } catch {}
}

// ── Message text extraction ──

function extractText(msg) {
  if (!msg.item_list?.length) return "";
  for (const item of msg.item_list) {
    if (item.type === 1 && item.text_item?.text) {
      const text = item.text_item.text;
      const ref = item.ref_msg;
      if (!ref) return text;
      const parts = [];
      if (ref.title) parts.push(ref.title);
      if (ref.message_item?.text_item?.text) parts.push(ref.message_item.text_item.text);
      if (parts.length) return `[引用: ${parts.join(" | ")}]\n${text}`;
      return text;
    }
    if (item.type === 3 && item.voice_item?.text) return item.voice_item.text;
  }
  return "";
}

// ── Strip markdown for WeChat delivery ──

function stripMarkdown(text) {
  let r = text;
  r = r.replace(/```[^\n]*\n?([\s\S]*?)```/g, (_, code) => code.trim());
  r = r.replace(/!\[[^\]]*\]\([^)]*\)/g, "");
  r = r.replace(/\[([^\]]+)\]\([^)]*\)/g, "$1");
  r = r.replace(/^\|[\s:|-]+\|$/gm, "");
  r = r.replace(/^\|(.+)\|$/gm, (_, inner) =>
    inner.split("|").map(c => c.trim()).join("  "));
  r = r.replace(/(\*\*|__)(.*?)\1/g, "$2");
  r = r.replace(/(\*|_)(.*?)\1/g, "$2");
  r = r.replace(/~~(.*?)~~/g, "$1");
  r = r.replace(/^#{1,6}\s+/gm, "");
  r = r.replace(/^>\s?/gm, "");
  r = r.replace(/^[-*+]\s/gm, "• ");
  return r.trim();
}

// ── Monitor loop per account ──

async function monitorAccount(account) {
  const { accountId, token, baseUrl } = account;
  let getUpdatesBuf = syncBufs.get(accountId) || loadSyncBuf(accountId);
  loadContextTokens(accountId);
  let consecutiveFailures = 0;
  const controller = new AbortController();
  abortControllers.set(accountId, controller);

  log(`[${accountId}] monitor started (${baseUrl})`);

  while (!controller.signal.aborted) {
    try {
      const resp = await getUpdates(baseUrl, token, getUpdatesBuf, LONG_POLL_TIMEOUT_MS);

      const isError = (resp.ret !== undefined && resp.ret !== 0) ||
                      (resp.errcode !== undefined && resp.errcode !== 0);
      if (isError) {
        consecutiveFailures++;
        log(`[${accountId}] getUpdates error: ret=${resp.ret} errcode=${resp.errcode} errmsg=${resp.errmsg || ""} (${consecutiveFailures}/${MAX_CONSECUTIVE_FAILURES})`);

        if (resp.errcode === SESSION_EXPIRED_ERRCODE || resp.ret === SESSION_EXPIRED_ERRCODE) {
          log(`[${accountId}] session expired, pausing 5min`);
          consecutiveFailures = 0;
          await sleep(300_000, controller.signal);
          continue;
        }

        if (consecutiveFailures >= MAX_CONSECUTIVE_FAILURES) {
          log(`[${accountId}] ${MAX_CONSECUTIVE_FAILURES} consecutive failures, backing off 30s`);
          consecutiveFailures = 0;
          await sleep(BACKOFF_DELAY_MS, controller.signal);
        } else {
          await sleep(RETRY_DELAY_MS, controller.signal);
        }
        continue;
      }

      consecutiveFailures = 0;

      if (resp.get_updates_buf) {
        saveSyncBuf(accountId, resp.get_updates_buf);
        syncBufs.set(accountId, resp.get_updates_buf);
        getUpdatesBuf = resp.get_updates_buf;
      }

      for (const msg of (resp.msgs || [])) {
        const fromUser = msg.from_user_id || "";
        const text = extractText(msg);
        if (!text || !fromUser) continue;

        // Save context token for replies
        if (msg.context_token) {
          contextTokens.set(`${accountId}:${fromUser}`, msg.context_token);
          saveContextTokens(accountId);
        }

        log(`[${accountId}] ← ${fromUser}: ${text.slice(0, 80)}`);

        // Notify FastClaw of inbound message
        notifyInbound({
          channel: "weixin",
          chatId: fromUser,
          userId: fromUser,
          text,
          peerKind: "dm",
          senderName: fromUser.split("@")[0],
        });
      }
    } catch (err) {
      if (controller.signal.aborted) break;
      consecutiveFailures++;
      log(`[${accountId}] poll error (${consecutiveFailures}/${MAX_CONSECUTIVE_FAILURES}): ${err.message}`);
      if (consecutiveFailures >= MAX_CONSECUTIVE_FAILURES) {
        consecutiveFailures = 0;
        await sleep(BACKOFF_DELAY_MS, controller.signal);
      } else {
        await sleep(RETRY_DELAY_MS, controller.signal);
      }
    }
  }

  log(`[${accountId}] monitor stopped`);
}

function sleep(ms, signal) {
  return new Promise((resolve, reject) => {
    const t = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => { clearTimeout(t); resolve(); }, { once: true });
  });
}

// ── Handle outbound: channel.send ──

async function handleChannelSend(params) {
  const { chatId, text } = params;
  if (!chatId || !text) throw new Error("chatId and text required");

  const plainText = stripMarkdown(text);

  // Find which account can reach this chatId
  for (const account of accounts) {
    const ctxKey = `${account.accountId}:${chatId}`;
    const ctxToken = contextTokens.get(ctxKey);
    if (ctxToken || accounts.length === 1) {
      await sendTextMessage(account.baseUrl, account.token, chatId, plainText, ctxToken);
      log(`[${account.accountId}] → ${chatId}: ${plainText.slice(0, 80)}`);
      return { status: "sent", accountId: account.accountId };
    }
  }

  // Fallback: try first account
  if (accounts.length > 0) {
    const account = accounts[0];
    const ctxToken = contextTokens.get(`${account.accountId}:${chatId}`);
    await sendTextMessage(account.baseUrl, account.token, chatId, plainText, ctxToken);
    log(`[${account.accountId}] → ${chatId} (fallback): ${plainText.slice(0, 80)}`);
    return { status: "sent", accountId: account.accountId };
  }

  throw new Error("No WeChat accounts available");
}

// ── Request handler ──

async function handleRequest(req) {
  const { method, params, id } = req;

  switch (method) {
    case "initialize": {
      config = params?.config || {};
      accounts = loadAccounts();
      log(`initialized with ${accounts.length} account(s)`);

      // Start monitors for all accounts
      for (const account of accounts) {
        monitorAccount(account).catch(err => {
          log(`[${account.accountId}] monitor crashed: ${err.message}`);
        });
      }

      // Watch for new accounts
      setInterval(() => {
        const current = loadAccounts();
        for (const acct of current) {
          if (!accounts.find(a => a.accountId === acct.accountId)) {
            log(`new account detected: ${acct.accountId}`);
            accounts.push(acct);
            monitorAccount(acct).catch(err => {
              log(`[${acct.accountId}] monitor crashed: ${err.message}`);
            });
          }
        }
      }, 30_000);

      return respond(id, { status: "ok", accounts: accounts.length });
    }

    case "channel.send": {
      try {
        const result = await handleChannelSend(params);
        return respond(id, result);
      } catch (err) {
        return respondError(id, -32000, err.message);
      }
    }

    case "shutdown": {
      log("shutting down...");
      for (const [accountId, controller] of abortControllers) {
        controller.abort();
      }
      respond(id, { status: "ok" });
      setTimeout(() => process.exit(0), 100);
      return;
    }

    default:
      return respondError(id, -32601, `Unknown method: ${method}`);
  }
}

// ── Main: read JSON-RPC from stdin ──

const rl = createInterface({ input: process.stdin, terminal: false });

rl.on("line", async (line) => {
  line = line.trim();
  if (!line) return;

  try {
    const req = JSON.parse(line);
    await handleRequest(req);
  } catch (err) {
    send({ jsonrpc: "2.0", error: { code: -32700, message: `Parse error: ${err.message}` }, id: null });
  }
});

rl.on("close", () => {
  for (const [, controller] of abortControllers) controller.abort();
  process.exit(0);
});

log("plugin process started, waiting for initialize...");
