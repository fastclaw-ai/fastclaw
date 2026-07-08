"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { register, getStatus } from "@/lib/api";

export default function SignupPage() {
  const t = useTranslations("signup");
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
      setError(t("errorAllFieldsRequired"));
      return;
    }
    if (password.length < 8) {
      setError(t("errorPasswordTooShort"));
      return;
    }
    if (password !== confirm) {
      setError(t("errorPasswordMismatch"));
      return;
    }
    setLoading(true);
    try {
      const res = await register({ username: username.trim(), email: email.trim(), password });
      if (!res.ok) {
        setError(res.error || t("errorCouldNotCreateAccount"));
        setLoading(false);
        return;
      }
      // Server set the session cookie on us; head to the app.
      router.replace("/overview/");
    } catch {
      setError(t("errorCannotReachServer"));
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
          <h1 className="text-2xl font-bold text-zinc-100">{t("registrationClosedTitle")}</h1>
          <p className="text-sm text-zinc-500">
            {t("registrationClosedHint")}
          </p>
          <Link
            href="/"
            className="inline-block rounded-lg bg-violet-600 px-4 py-2 text-sm font-medium text-white hover:bg-violet-500"
          >
            {t("backToSignIn")}
          </Link>
        </div>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-zinc-950 p-4">
      <div className="w-full max-w-sm space-y-6">
        <div className="text-center space-y-2">
          <h1 className="text-2xl font-bold text-zinc-100">{t("title")}</h1>
          <p className="text-sm text-zinc-500">{t("subtitle")}</p>
        </div>
        <form onSubmit={handleSubmit} className="space-y-4">
          <input
            type="text"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder={t("usernamePlaceholder")}
            autoFocus
            autoComplete="username"
            className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
          />
          <input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder={t("emailPlaceholder")}
            autoComplete="email"
            className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
          />
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={t("passwordPlaceholder")}
            autoComplete="new-password"
            className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
          />
          <input
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            placeholder={t("confirmPasswordPlaceholder")}
            autoComplete="new-password"
            className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
          />
          {error && <p className="text-sm text-red-400">{error}</p>}
          <button
            type="submit"
            disabled={loading || !username.trim() || !email.trim() || !password || !confirm}
            className="w-full rounded-lg bg-violet-600 px-4 py-3 text-sm font-medium text-white transition hover:bg-violet-500 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {loading ? t("creatingAccount") : t("createAccount")}
          </button>
        </form>
        <p className="text-center text-sm text-zinc-500">
          {t("haveAccount")}{" "}
          <Link href="/" className="text-violet-400 hover:text-violet-300">
            {t("signIn")}
          </Link>
        </p>
      </div>
    </div>
  );
}
