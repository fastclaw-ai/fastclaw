// In-memory only: a successful authenticated API response is enough to avoid
// showing AgentAccessGate's full-screen probe between client-side navigations.
// The backend remains authoritative for every data request, and a page reload
// starts with an empty cache so direct/shared URLs are still checked normally.
const accessibleAgentIds = new Set<string>();

export function rememberAgentAccess(agentOrIds: string | string[]) {
  const ids = Array.isArray(agentOrIds) ? agentOrIds : [agentOrIds];
  for (const id of ids) {
    if (id) accessibleAgentIds.add(id);
  }
}

export function hasRememberedAgentAccess(agentId: string): boolean {
  return agentId !== "" && accessibleAgentIds.has(agentId);
}
