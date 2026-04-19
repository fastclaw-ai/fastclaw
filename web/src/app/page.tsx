"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { getStatus } from "@/lib/api";
import { isLoggedIn, login } from "@/lib/auth";

export default function RootPage() {
  const router = useRouter();
  const [showLogin, setShowLogin] = useState(false);
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (isLoggedIn()) {
      router.replace("/overview/");
      return;
    }

    getStatus()
      .then((status) => {
        if (!status.configured) {
          router.replace("/onboard/");
        } else {
          setShowLogin(true);
          setLoading(false);
        }
      })
      .catch(() => {
        router.replace("/onboard/");
      });
  }, [router]);

  const handleLogin = async () => {
    if (!token.trim()) return;
    setError("");
    login(token.trim());
    try {
      const status = await getStatus();
      if (status.isAdmin) {
        router.replace("/overview/");
      } else {
        setError("Invalid admin token");
        login("");
      }
    } catch {
      setError("Connection failed");
      login("");
    }
  };

  if (loading && !showLogin) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <div className="h-8 w-8 animate-spin rounded-full border-2 border-muted border-t-primary" />
      </div>
    );
  }

  if (showLogin) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <div className="w-full max-w-sm space-y-6 p-6">
          <div className="flex flex-col items-center gap-3">
            <img src="/logo.png" alt="FastClaw" className="h-12 w-12" />
            <h1 className="text-xl font-bold">FastClaw</h1>
            <p className="text-sm text-muted-foreground">
              Enter your admin token to sign in
            </p>
          </div>

          <div className="space-y-4">
            <input
              type="password"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && handleLogin()}
              placeholder="Paste your gateway token"
              autoFocus
              className="w-full rounded-lg border border-border bg-card px-4 py-3 font-mono text-sm outline-none focus:ring-1 focus:ring-primary/30"
            />
            {error && <p className="text-sm text-red-500">{error}</p>}
            <button
              onClick={handleLogin}
              disabled={!token.trim()}
              className="w-full rounded-lg bg-primary px-4 py-3 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50 transition-colors"
            >
              Sign In
            </button>
          </div>
        </div>
      </div>
    );
  }

  return null;
}
