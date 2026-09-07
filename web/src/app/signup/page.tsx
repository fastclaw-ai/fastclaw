"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { register, getAgents, getStatus } from "@/lib/api";
import { useLocale } from "@/components/locale-provider";

export default function SignupPage() {
  const { tr } = useLocale();
  const router = useRouter();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  // null = still loading the toggle, true/false = resolved
  const [open, setOpen] = useState<boolean | null>(null);

  useEffect(() => {
    let aborted = false;
    getStatus()
      .then((s) => { if (!aborted) setOpen(!!s.registrationOpen); })
      .catch(() => { if (!aborted) setOpen(false); });
    return () => { aborted = true; };
  }, []);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (!username.trim() || !email.trim() || !password) {
      setError(tr("All fields are required", "请填写所有字段"));
      return;
    }
    if (password.length < 8) {
      setError(tr("Password must be at least 8 characters", "密码至少需要 8 个字符"));
      return;
    }
    if (password !== confirm) {
      setError(tr("Passwords do not match", "两次输入的密码不一致"));
      return;
    }
    setLoading(true);
    try {
      const res = await register({ username: username.trim(), email: email.trim(), password });
      if (!res.ok) {
        setError(res.error || tr("Could not create account", "无法创建账户"));
        setLoading(false);
        return;
      }
      // Server set the session cookie on us. New users normally have no
      // provisioned Bot yet; existing invite flows may have one waiting.
      const agents = await getAgents().catch(() => []);
      router.replace(
        agents.length > 0
          ? `/agents/${encodeURIComponent(agents[0].id)}/chat/`
          : "/agents/",
      );
    } catch {
      setError(tr("Cannot reach server", "无法连接服务器"));
      setLoading(false);
    }
  }

  if (open === null) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-zinc-950">
        <div className="h-8 w-8 animate-spin rounded-full border-2 border-zinc-700 border-t-violet-500" />
      </div>
    );
  }

  if (!open) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-zinc-950 p-4">
        <div className="w-full max-w-sm space-y-4 text-center">
          <h1 className="text-2xl font-bold text-zinc-100">
            {tr("Registration closed", "注册已关闭")}
          </h1>
          <p className="text-sm text-zinc-500">
            {tr(
              "New accounts cannot be created right now. Ask the administrator to enable registration, or sign in if you already have an account.",
              "当前无法创建新账户。请联系管理员开放注册；如果已有账户，请直接登录。",
            )}
          </p>
          <Link
            href="/"
            className="inline-block rounded-lg bg-violet-600 px-4 py-2 text-sm font-medium text-white hover:bg-violet-500"
          >
            {tr("Back to sign in", "返回登录")}
          </Link>
        </div>
      </div>
    );
  }

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
        <form onSubmit={handleSubmit} className="space-y-4">
          <input
            type="text"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder={tr("username", "用户名")}
            autoFocus
            autoComplete="username"
            className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
          />
          <input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
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
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            placeholder={tr("confirm password", "确认密码")}
            autoComplete="new-password"
            className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
          />
          {error && <p className="text-sm text-red-400">{error}</p>}
          <button
            type="submit"
            disabled={loading || !username.trim() || !email.trim() || !password || !confirm}
            className="w-full rounded-lg bg-violet-600 px-4 py-3 text-sm font-medium text-white transition hover:bg-violet-500 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {loading ? tr("Creating account…", "正在创建账户…") : tr("Create account", "创建账户")}
          </button>
        </form>
        <p className="text-center text-sm text-zinc-500">
          {tr("Already have an account?", "已有账户？")} {" "}
          <Link href="/" className="text-violet-400 hover:text-violet-300">
            {tr("Sign in", "登录")}
          </Link>
        </p>
      </div>
    </div>
  );
}
