export interface StatusResponse {
  configured: boolean;
  running: boolean;
  port: number;
  uptime: string;
  agents: AgentInfo[];
  channels: ChannelInfo[];
  provider: ProviderInfo;
  cronJobs?: number;
  plugins?: number;
  userId?: string;
  isAdmin?: boolean;
}

export interface AgentInfo {
  id: string;
  model: string;
  workspace: string;
}

export interface ChannelInfo {
  type: string;
  botUsername: string;
  enabled?: boolean;
  status?: string;
}

export interface ProviderInfo {
  name: string;
  model: string;
  apiBase: string;
  apiKey: string;
}

export interface AgentDetail {
  id: string;
  model: string;
  workspace: string;
  maxTokens?: number;
  temperature?: number;
  maxToolIterations?: number;
  thinking?: string;
  soul?: string;
  skills?: string[];
  tools?: string[];
}

export interface SkillInfo {
  name: string;
  description: string;
  location: string;
  type: string;
}

export interface PluginInfo {
  id: string;
  type: string;
  version: string;
  status: string;
  enabled: boolean;
  config?: Record<string, unknown>;
}

export interface CronJobInfo {
  id: string;
  name: string;
  type: string;
  schedule: string;
  agentId: string;
  channel: string;
  chatId: string;
  message: string;
  enabled: boolean;
  lastRun?: string;
  nextRun?: string;
}

export interface ModelCost {
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
}

export interface ModelEntry {
  id: string;
  name: string;
  reasoning: boolean;
  input: string[];
  cost: ModelCost;
  contextWindow: number;
  maxTokens: number;
}

export interface ProviderData {
  apiKey: string;
  apiBase: string;
  apiType?: string;
  authType?: string;
  models?: ModelEntry[];
}

export interface ConfigResponse {
  providers: Record<string, ProviderData>;
  agents: {
    defaults: {
      model: string;
      maxTokens: number;
      temperature: number;
      maxToolIterations: number;
    };
    list: Array<{ id: string; model?: string }>;
  };
  channels: Record<string, { enabled: boolean; botToken?: string }>;
  storage: { type: string; dsn?: string };
  sandbox?: { enabled: boolean; backend?: string; image?: string; e2bKey?: string };
  hooks: { enabled: boolean; token?: string; path?: string; port?: number };
  cronJobs?: Array<Record<string, unknown>>;
}

// Auth token for cloud mode. Set via setAuthToken() on login; empty in local mode.
let authToken = "";

export function setAuthToken(token: string) {
  authToken = token;
  if (token) {
    localStorage.setItem("fastclaw_token", token);
  } else {
    localStorage.removeItem("fastclaw_token");
  }
}

export function getAuthToken(): string {
  if (!authToken) {
    authToken = localStorage.getItem("fastclaw_token") || "";
  }
  return authToken;
}

// Wrapper around fetch that injects Authorization header when a token is set.
export async function apiFetch(url: string, init?: RequestInit): Promise<Response> {
  const token = getAuthToken();
  const headers: Record<string, string> = {
    ...(init?.headers as Record<string, string> || {}),
  };
  if (token) {
    headers["Authorization"] = `Bearer ${token}`;
  }
  return fetch(url, { ...init, headers });
}

// Status
export async function getStatus(): Promise<StatusResponse> {
  const res = await apiFetch("/api/status");
  return res.json();
}

// Provider
export async function testProvider(config: { apiBase: string; apiKey: string; model: string; apiType?: string; authType?: string }) {
  const res = await apiFetch("/api/test-provider", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(config),
  });
  return res.json();
}

// Config
export async function saveConfig(config: Record<string, unknown>) {
  const res = await apiFetch("/api/save-config", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(config),
  });
  return res.json();
}

export async function getConfig(): Promise<ConfigResponse> {
  const res = await apiFetch("/api/config");
  return res.json();
}

export async function updateConfig(config: Record<string, unknown>) {
  const res = await apiFetch("/api/config", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(config),
  });
  return res.json();
}

// Chat
export interface ChatHistoryMessage {
  role: "user" | "assistant" | "tool";
  content?: string;
  toolCalls?: { id: string; name: string; arguments: string }[];
  name?: string;
  toolCallId?: string;
}

export async function getChatHistory(agentId: string, sessionId: string): Promise<ChatHistoryMessage[]> {
  const res = await apiFetch(`/api/chat/history?agentId=${encodeURIComponent(agentId)}&sessionId=${encodeURIComponent(sessionId)}`);
  return res.json();
}

export async function getChatSessions(agentId: string): Promise<{ id: string; preview: string }[]> {
  const res = await apiFetch(`/api/chat/sessions?agentId=${encodeURIComponent(agentId)}`);
  return res.json();
}

export async function sendChat(agentId: string, sessionId: string, message: string): Promise<{ response: string }> {
  const res = await apiFetch("/api/chat", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ agentId, sessionId, message }),
  });
  return res.json();
}

export interface ChatStreamEvent {
  type: "content" | "tool_call" | "tool_result" | "done";
  data?: Record<string, string>;
}

export async function sendChatStream(
  agentId: string,
  sessionId: string,
  message: string,
  onEvent: (evt: ChatStreamEvent) => void,
): Promise<void> {
  const res = await apiFetch("/api/chat/stream", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ agentId, sessionId, message }),
  });
  if (!res.ok || !res.body) throw new Error("stream failed");

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });

    const lines = buffer.split("\n");
    buffer = lines.pop() || "";

    for (const line of lines) {
      if (!line.startsWith("data: ")) continue;
      try {
        const evt = JSON.parse(line.slice(6)) as ChatStreamEvent;
        onEvent(evt);
      } catch { /* skip */ }
    }
  }
}

// Agents
export async function getAgents(): Promise<AgentDetail[]> {
  const res = await apiFetch("/api/agents");
  return res.json();
}

export async function createAgent(agent: Partial<AgentDetail>) {
  const res = await apiFetch("/api/agents", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(agent),
  });
  return res.json();
}

export async function updateAgent(id: string, agent: Partial<AgentDetail>) {
  const res = await apiFetch(`/api/agents/${id}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(agent),
  });
  return res.json();
}

export async function deleteAgent(id: string) {
  const res = await apiFetch(`/api/agents/${id}`, {
    method: "DELETE",
  });
  return res.json();
}

// Skills
export async function getSkills(): Promise<SkillInfo[]> {
  const res = await apiFetch("/api/skills");
  return res.json();
}

export async function deleteSkill(name: string) {
  const res = await apiFetch(`/api/skills/${name}`, {
    method: "DELETE",
  });
  return res.json();
}

// Plugins
export async function getPlugins(): Promise<PluginInfo[]> {
  const res = await apiFetch("/api/plugins");
  return res.json();
}

export async function updatePlugin(id: string, data: Partial<PluginInfo>) {
  const res = await apiFetch(`/api/plugins/${id}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(data),
  });
  return res.json();
}

// Channels
export async function getChannels(): Promise<ChannelInfo[]> {
  const res = await apiFetch("/api/channels");
  return res.json();
}

// Cron Jobs
export async function getCronJobs(): Promise<CronJobInfo[]> {
  const res = await apiFetch("/api/cron");
  return res.json();
}

export async function createCronJob(job: Partial<CronJobInfo>) {
  const res = await apiFetch("/api/cron", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(job),
  });
  return res.json();
}

export async function updateCronJob(id: string, job: Partial<CronJobInfo>) {
  const res = await apiFetch(`/api/cron/${id}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(job),
  });
  return res.json();
}

export async function deleteCronJob(id: string) {
  const res = await apiFetch(`/api/cron/${id}`, {
    method: "DELETE",
  });
  return res.json();
}

// --- Admin API ---

export interface AdminUser {
  id: string;
  name: string;
  tokens: string[];
  createdAt: string;
}

export async function adminListUsers(): Promise<AdminUser[]> {
  const res = await apiFetch("/v1/admin/users");
  if (!res.ok) return [];
  const data = await res.json();
  return data.users || [];
}

export async function adminCreateUser(id: string, name: string): Promise<{ user: AdminUser; token: string }> {
  const res = await apiFetch("/v1/admin/users", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ id, name }),
  });
  return res.json();
}

export async function adminDeleteUser(id: string): Promise<void> {
  await apiFetch(`/v1/admin/users/${id}`, { method: "DELETE" });
}

export async function adminIssueToken(id: string): Promise<string> {
  const res = await apiFetch(`/v1/admin/users/${id}/token`, { method: "POST" });
  const data = await res.json();
  return data.token;
}
