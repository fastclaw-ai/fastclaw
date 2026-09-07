"use client";

import { useEffect, useState } from "react";
import { login as apiLogin, register, getStatus } from "@/lib/api";
import { useLocale } from "@/components/locale-provider";

interface LoginScreenProps {
  onSuccess: () => void;
}

// LoginScreen flips between sign-in and sign-up inline rather than
// navigating to /signup. Two reasons: (a) the agent share URL the user
// landed on stays in the address bar throughout the flow, so a
// successful sign-up lands them straight on the page they came for; (b)
// /signup as a separate route was being rendered inside AppShell's
// SidebarLayout, leaking authenticated app chrome to a visitor who
// hasn't even registered yet. Registration on the server sets the
// session cookie, so a sign-up success is functionally a sign-in
// success — we route both through `onSuccess` and let AuthGuard render
// the originally-requested page.
export function LoginScreen({ onSuccess }: LoginScreenProps) {
  const { tr } = useLocale();
  const [mode, setMode] = useState<"signin" | "signup">("signin");
  const [loginField, setLoginField] = useState("");
  const [password, setPassword] = useState("");
  const [signupUsername, setSignupUsername] = useState("");
  const [signupEmail, setSignupEmail] = useState("");
  const [signupConfirm, setSignupConfirm] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [registrationOpen, setRegistrationOpen] = useState(false);

  useEffect(() => {
    let aborted = false;
    getStatus()
      .then((s) => { if (!aborted) setRegistrationOpen(!!s.registrationOpen); })
      .catch(() => { /* leave default false — sign-up link stays hidden */ });
    return () => { aborted = true; };
  }, []);

  function switchMode(next: "signin" | "signup") {
    setError("");
    setMode(next);
  }

  async function handleSignIn(e: React.FormEvent) {
    e.preventDefault();
    if (!loginField.trim() || !password) return;
    setLoading(true);
    setError("");
    try {
      const res = await apiLogin(loginField.trim(), password);
      if (!res.ok) {
        setError(res.error || tr("Invalid credentials", "登录凭据无效"));
        setLoading(false);
        return;
      }
      onSuccess();
    } catch {
      setError(tr("Cannot reach server", "无法连接服务器"));
      setLoading(false);
    }
  }

  async function handleSignUp(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (!signupUsername.trim() || !signupEmail.trim() || !password) {
      setError(tr("All fields are required", "请填写所有字段"));
      return;
    }
    if (password.length < 8) {
      setError(tr("Password must be at least 8 characters", "密码至少需要 8 个字符"));
      return;
    }
    if (password !== signupConfirm) {
      setError(tr("Passwords do not match", "两次输入的密码不一致"));
      return;
    }
    setLoading(true);
    try {
      const res = await register({
        username: signupUsername.trim(),
        email: signupEmail.trim(),
        password,
      });
      if (!res.ok) {
        setError(res.error || tr("Could not create account", "无法创建账户"));
        setLoading(false);
        return;
      }
      // Register handler set the session cookie on our response, so the
      // app is effectively already signed in. Reuse the same callback
      // sign-in uses and AuthGuard will render the originally-requested
      // route without any redirect.
      onSuccess();
    } catch {
      setError(tr("Cannot reach server", "无法连接服务器"));
      setLoading(false);
    }
  }

  if (mode === "signup") {
    return (
      <div className="flex min-h-screen items-center justify-center bg-zinc-950 p-4">
        <div className="w-full max-w-sm space-y-6">
          <div className="text-center space-y-2">
            <h1 className="text-2xl font-bold text-zinc-100">
              {tr("Create your account", "创建账户")}
            </h1>
            <p className="text-sm text-zinc-500">
              {tr("Sign up to start using FastClaw", "注册后开始使用 FastClaw")}
            </p>
          </div>
          <form onSubmit={handleSignUp} className="space-y-4">
            <input
              type="text"
              value={signupUsername}
              onChange={(e) => setSignupUsername(e.target.value)}
              placeholder={tr("username", "用户名")}
              autoFocus
              autoComplete="username"
              className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
            />
            <input
              type="email"
              value={signupEmail}
              onChange={(e) => setSignupEmail(e.target.value)}
              placeholder={tr("email", "邮箱")}
              autoComplete="email"
              className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
            />
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder={tr("password (at least 8 characters)", "密码（至少 8 个字符）")}
              autoComplete="new-password"
              className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
            />
            <input
              type="password"
              value={signupConfirm}
              onChange={(e) => setSignupConfirm(e.target.value)}
              placeholder={tr("confirm password", "确认密码")}
              autoComplete="new-password"
              className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
            />
            {error && <p className="text-sm text-red-400">{error}</p>}
            <button
              type="submit"
              disabled={loading || !signupUsername.trim() || !signupEmail.trim() || !password || !signupConfirm}
              className="w-full rounded-lg bg-violet-600 px-4 py-3 text-sm font-medium text-white transition hover:bg-violet-500 disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {loading ? tr("Creating account…", "正在创建账户…") : tr("Create account", "创建账户")}
            </button>
          </form>
          <p className="text-center text-sm text-zinc-500">
            {tr("Already have an account?", "已有账户？")} {" "}
            <button
              type="button"
              onClick={() => switchMode("signin")}
              className="text-violet-400 hover:text-violet-300"
            >
              {tr("Sign in", "登录")}
            </button>
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-zinc-950 p-4">
      <div className="w-full max-w-sm space-y-6">
        <div className="text-center space-y-2">
          <h1 className="text-2xl font-bold text-zinc-100">FastClaw</h1>
          <p className="text-sm text-zinc-500">
            {tr("Sign in with your username or email", "使用用户名或邮箱登录")}
          </p>
        </div>
        <form onSubmit={handleSignIn} className="space-y-4">
          <input
            type="text"
            value={loginField}
            onChange={(e) => setLoginField(e.target.value)}
            placeholder={tr("username or email", "用户名或邮箱")}
            autoFocus
            autoComplete="username"
            className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
          />
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={tr("password", "密码")}
            autoComplete="current-password"
            className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
          />
          {error && <p className="text-sm text-red-400">{error}</p>}
          <button
            type="submit"
            disabled={loading || !loginField.trim() || !password}
            className="w-full rounded-lg bg-violet-600 px-4 py-3 text-sm font-medium text-white transition hover:bg-violet-500 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {loading ? tr("Signing in…", "正在登录…") : tr("Sign in", "登录")}
          </button>
        </form>
        {registrationOpen && (
          <p className="text-center text-sm text-zinc-500">
            {tr("Do not have an account?", "还没有账户？")} {" "}
            <button
              type="button"
              onClick={() => switchMode("signup")}
              className="text-violet-400 hover:text-violet-300"
            >
              {tr("Sign up", "注册")}
            </button>
          </p>
        )}
      </div>
    </div>
  );
}
