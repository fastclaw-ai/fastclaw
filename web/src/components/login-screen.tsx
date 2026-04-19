"use client";

import { useState } from "react";
import { login } from "@/lib/auth";
import { getStatus } from "@/lib/api";

interface LoginScreenProps {
  onSuccess: () => void;
}

export function LoginScreen({ onSuccess }: LoginScreenProps) {
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const trimmed = token.trim();
    if (!trimmed) return;

    setLoading(true);
    setError("");

    // Set the token first so apiFetch uses it.
    login(trimmed);

    try {
      // Validate by calling status — a 401 means the token is invalid.
      const res = await fetch("/api/status", {
        headers: { Authorization: `Bearer ${trimmed}` },
      });

      if (res.status === 401) {
        setError("Invalid token");
        login(""); // clear
        setLoading(false);
        return;
      }

      const status = await res.json();
      if (status.ok === false && status.error) {
        setError(status.error);
        login("");
        setLoading(false);
        return;
      }

      onSuccess();
    } catch {
      setError("Cannot reach server");
      login("");
      setLoading(false);
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-zinc-950 p-4">
      <div className="w-full max-w-sm space-y-6">
        <div className="text-center space-y-2">
          <h1 className="text-2xl font-bold text-zinc-100">FastClaw</h1>
          <p className="text-sm text-zinc-500">Enter your access token to continue</p>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div>
            <input
              type="password"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="fc_..."
              autoFocus
              className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
            />
          </div>

          {error && (
            <p className="text-sm text-red-400">{error}</p>
          )}

          <button
            type="submit"
            disabled={loading || !token.trim()}
            className="w-full rounded-lg bg-violet-600 px-4 py-3 text-sm font-medium text-white transition hover:bg-violet-500 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {loading ? "Verifying..." : "Login"}
          </button>
        </form>

        <p className="text-center text-xs text-zinc-600">
          Token issued by your admin via <code className="text-zinc-500">fastclaw user add</code>
        </p>
      </div>
    </div>
  );
}
