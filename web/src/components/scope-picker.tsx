"use client";

import { useEffect, useState } from "react";
import { apiFetch, getMe, type ScopeName } from "@/lib/api";
import { useLocale } from "@/components/locale-provider";

interface AgentRef {
  id: string;
  name: string;
}
interface UserRef {
  id: string;
  username: string;
  email: string;
}

interface ScopePickerProps {
  scope: ScopeName;
  scopeId: string;
  onChange: (scope: ScopeName, scopeId: string) => void;
}

// ScopePicker is the shared "system / user / agent" selector for the
// providers and channels admin pages. Available scopes depend on the
// caller's role:
//
//   super_admin: all three (system, every user, every agent)
//   user:        only their own user-scope and the agents they own
export function ScopePicker({ scope, scopeId, onChange }: ScopePickerProps) {
  const { tr } = useLocale();
  const [role, setRole] = useState<string>("");
  const [meId, setMeId] = useState<string>("");
  const [users, setUsers] = useState<UserRef[]>([]);
  const [agents, setAgents] = useState<AgentRef[]>([]);

  useEffect(() => {
    let aborted = false;
    (async () => {
      const me = await getMe();
      if (aborted || !me.user) return;
      setRole(me.user.role);
      setMeId(me.user.id);

      // Pull the agent list — every caller can see their own agents.
      const ag = await apiFetch("/api/agents");
      const aj = await ag.json();
      if (!aborted && aj.agents) setAgents(aj.agents);

      // Super_admin can also enumerate users (for picking a user scope).
      if (me.user.role === "super_admin") {
        const u = await apiFetch("/api/users");
        const uj = await u.json();
        if (!aborted && uj.users) setUsers(uj.users);
      }
    })();
    return () => {
      aborted = true;
    };
  }, []);

  const isAdmin = role === "super_admin";

  function handleScopeChange(next: ScopeName) {
    if (next === "system") onChange("system", "");
    else if (next === "user") onChange("user", isAdmin && users[0]?.id ? users[0].id : meId);
    else onChange("agent", agents[0]?.id ?? "");
  }

  return (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <span className="text-zinc-400">{tr("Scope:", "作用域：")}</span>
      <select
        value={scope}
        onChange={(e) => handleScopeChange(e.target.value as ScopeName)}
        className="rounded border border-zinc-700 bg-zinc-950 px-2 py-1"
      >
        {isAdmin && <option value="system">{tr("system", "系统")}</option>}
        <option value="user">{tr("user", "用户")}</option>
        <option value="agent">Agent</option>
      </select>
      {scope === "user" && isAdmin && (
        <select
          value={scopeId}
          onChange={(e) => onChange("user", e.target.value)}
          className="rounded border border-zinc-700 bg-zinc-950 px-2 py-1"
        >
          {users.map((u) => (
            <option key={u.id} value={u.id}>
              {u.username} ({u.email})
            </option>
          ))}
        </select>
      )}
      {scope === "user" && !isAdmin && (
        <span className="rounded bg-zinc-800 px-2 py-1 text-xs text-zinc-300">{tr("you", "你")}</span>
      )}
      {scope === "agent" && (
        <select
          value={scopeId}
          onChange={(e) => onChange("agent", e.target.value)}
          className="rounded border border-zinc-700 bg-zinc-950 px-2 py-1"
        >
          {agents.map((a) => (
            <option key={a.id} value={a.id}>
              {a.id}
            </option>
          ))}
        </select>
      )}
    </div>
  );
}
