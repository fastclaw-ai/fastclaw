"use client";

import { useState, useEffect } from "react";
import { adminListUsers, adminCreateUser, adminDeleteUser, adminIssueToken, type AdminUser } from "@/lib/api";

export default function UsersPage() {
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [loading, setLoading] = useState(true);
  const [showCreate, setShowCreate] = useState(false);
  const [newId, setNewId] = useState("");
  const [newName, setNewName] = useState("");
  const [creating, setCreating] = useState(false);
  const [newToken, setNewToken] = useState("");
  const [issuedToken, setIssuedToken] = useState<{ userId: string; token: string } | null>(null);

  async function loadUsers() {
    setLoading(true);
    const list = await adminListUsers();
    setUsers(list);
    setLoading(false);
  }

  useEffect(() => { loadUsers(); }, []);

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    if (!newId.trim()) return;
    setCreating(true);
    try {
      const result = await adminCreateUser(newId.trim(), newName.trim());
      setNewToken(result.token);
      setNewId("");
      setNewName("");
      await loadUsers();
    } catch (err: any) {
      alert(err.message || "Failed to create user");
    }
    setCreating(false);
  }

  async function handleDelete(id: string) {
    if (!confirm(`Delete user "${id}"? Their workspace data will be preserved on disk.`)) return;
    await adminDeleteUser(id);
    await loadUsers();
  }

  async function handleIssueToken(id: string) {
    const token = await adminIssueToken(id);
    setIssuedToken({ userId: id, token });
  }

  return (
    <div className="p-6 max-w-4xl">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold text-foreground">Users</h1>
          <p className="text-sm text-muted-foreground mt-1">Manage cloud users and access tokens</p>
        </div>
        <button
          onClick={() => { setShowCreate(!showCreate); setNewToken(""); }}
          className="rounded-lg bg-violet-600 px-4 py-2 text-sm font-medium text-white hover:bg-violet-500 transition"
        >
          Add User
        </button>
      </div>

      {/* Create form */}
      {showCreate && (
        <div className="mb-6 rounded-lg border border-border bg-card p-4 space-y-3">
          <form onSubmit={handleCreate} className="flex gap-3 items-end">
            <div className="flex-1">
              <label className="text-xs text-muted-foreground">User ID</label>
              <input
                value={newId}
                onChange={(e) => setNewId(e.target.value)}
                placeholder="alice"
                className="mt-1 w-full rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground outline-none focus:border-violet-500"
                autoFocus
              />
            </div>
            <div className="flex-1">
              <label className="text-xs text-muted-foreground">Display Name</label>
              <input
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                placeholder="Alice"
                className="mt-1 w-full rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground outline-none focus:border-violet-500"
              />
            </div>
            <button
              type="submit"
              disabled={creating || !newId.trim()}
              className="rounded-md bg-violet-600 px-4 py-2 text-sm font-medium text-white hover:bg-violet-500 disabled:opacity-50"
            >
              {creating ? "Creating..." : "Create"}
            </button>
          </form>

          {newToken && (
            <div className="rounded-md border border-emerald-800 bg-emerald-950/50 p-3">
              <p className="text-xs text-emerald-400 mb-1">User created! Copy this token — it won&apos;t be shown again:</p>
              <code className="block text-sm text-emerald-300 font-mono break-all select-all">{newToken}</code>
            </div>
          )}
        </div>
      )}

      {/* Issued token banner */}
      {issuedToken && (
        <div className="mb-4 rounded-md border border-amber-800 bg-amber-950/50 p-3">
          <p className="text-xs text-amber-400 mb-1">New token for <strong>{issuedToken.userId}</strong>:</p>
          <code className="block text-sm text-amber-300 font-mono break-all select-all">{issuedToken.token}</code>
          <button onClick={() => setIssuedToken(null)} className="mt-2 text-xs text-amber-500 hover:text-amber-400">Dismiss</button>
        </div>
      )}

      {/* User list */}
      {loading ? (
        <div className="flex justify-center py-12">
          <div className="h-6 w-6 animate-spin rounded-full border-2 border-zinc-700 border-t-violet-500" />
        </div>
      ) : users.length === 0 ? (
        <div className="text-center py-12 text-muted-foreground">
          <p>No users registered.</p>
          <p className="text-sm mt-1">Click &quot;Add User&quot; to create one.</p>
        </div>
      ) : (
        <div className="space-y-2">
          {users.map((user) => (
            <div
              key={user.id}
              className="flex items-center justify-between rounded-lg border border-border bg-card px-4 py-3"
            >
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-foreground">{user.id}</span>
                  {user.name && <span className="text-sm text-muted-foreground">({user.name})</span>}
                </div>
                <div className="flex items-center gap-3 mt-1 text-xs text-muted-foreground">
                  <span>{user.tokens?.length || 0} token(s)</span>
                  <span>Created {new Date(user.createdAt).toLocaleDateString()}</span>
                </div>
              </div>
              <div className="flex items-center gap-2">
                <button
                  onClick={() => handleIssueToken(user.id)}
                  className="rounded-md border border-border px-3 py-1.5 text-xs text-muted-foreground hover:text-foreground hover:bg-muted/50 transition"
                >
                  New Token
                </button>
                <button
                  onClick={() => handleDelete(user.id)}
                  className="rounded-md border border-red-900 px-3 py-1.5 text-xs text-red-400 hover:bg-red-950/50 transition"
                >
                  Delete
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
