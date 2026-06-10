"use client";

import { MCPManager } from "@/components/mcp-manager";
import {
  createSystemMCPServer,
  deleteSystemMCPServer,
  listSystemMCPServers,
  testSystemMCPServer,
  updateSystemMCPServer,
} from "@/lib/api";

export default function AdminMCPPage() {
  return (
    <MCPManager
      scopeLabel="all agents (system-wide)"
      scopeNote="System MCP servers are inherited by every agent. An agent can override one by configuring a server of the same name."
      list={listSystemMCPServers}
      create={createSystemMCPServer}
      update={updateSystemMCPServer}
      remove={deleteSystemMCPServer}
      test={testSystemMCPServer}
    />
  );
}
