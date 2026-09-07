"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { getAgents, getStatus, getMe, login as loginApi } from "@/lib/api";
import { logout } from "@/lib/auth";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useLocale } from "@/components/locale-provider";

async function resolveAppLanding() {
  const agents = await getAgents().catch(() => []);
  return agents.length > 0
    ? `/agents/${encodeURIComponent(agents[0].id)}/chat/`
    : "/agents/?manage=1";
}

export default function RootPage() {
  const { tr } = useLocale();
  const router = useRouter();
  const [showLogin, setShowLogin] = useState(false);
  const [loginField, setLoginField] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    getStatus()
      .then(async (status) => {
        // Never trust a stale localStorage token when the backend reports
        // the system is unconfigured — that token belongs to a previous
        // deployment and would otherwise short-circuit onboarding.
        if (!status.configured) {
          logout();
          router.replace("/onboard/");
          return;
        }
        const me = await getMe().catch(() => null);
        if (me?.ok && me.user) {
          router.replace(await resolveAppLanding());
        } else {
          setShowLogin(true);
          setLoading(false);
        }
      })
      .catch(() => {
        router.replace("/onboard/");
      });
  }, [router]);

  const handleLogin = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!loginField.trim() || !password) return;
    setError("");
    setSubmitting(true);
    try {
      const res = await loginApi(loginField.trim(), password);
      if (!res.ok) {
        setError(res.error || tr("Invalid username or password", "用户名或密码错误"));
        return;
      }
      router.replace(await resolveAppLanding());
    } catch {
      setError(tr("Connection failed", "连接失败"));
    } finally {
      setSubmitting(false);
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
              {tr("Sign in to continue", "登录后继续")}
            </p>
          </div>

          <form onSubmit={handleLogin} className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="login-field">{tr("Username or email", "用户名或邮箱")}</Label>
              <Input
                id="login-field"
                value={loginField}
                onChange={(e) => setLoginField(e.target.value)}
                autoComplete="username"
                autoFocus
                placeholder="alice"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="login-password">{tr("Password", "密码")}</Label>
              <Input
                id="login-password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
              />
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button
              type="submit"
              disabled={!loginField.trim() || !password || submitting}
              className="w-full"
            >
              {submitting ? tr("Signing in…", "正在登录…") : tr("Sign in", "登录")}
            </Button>
          </form>
        </div>
      </div>
    );
  }

  return null;
}
