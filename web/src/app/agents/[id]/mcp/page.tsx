"use client";

import { MCPManager } from "@/components/mcp-manager";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";
import {
  createAgentMCPServer,
  deleteAgentMCPServer,
  listAgentMCPServers,
  testAgentMCPServer,
  updateAgentMCPServer,
  type MCPServerInput,
} from "@/lib/api";

export default function AgentMCPPage() {
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);

  if (!agentId) return null;

  return (
    <MCPManager
      scopeLabel={agentName || "this agent"}
      list={() => listAgentMCPServers(agentId)}
      create={(input: MCPServerInput) => createAgentMCPServer(agentId, input)}
      update={(name: string, input: MCPServerInput) => updateAgentMCPServer(agentId, name, input)}
      remove={(name: string) => deleteAgentMCPServer(agentId, name)}
      test={(input: MCPServerInput) => testAgentMCPServer(agentId, input)}
    />
  );
}
