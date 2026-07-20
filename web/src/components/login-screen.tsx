"use client";

import { useEffect, useState } from "react";
import { useTranslations } from "next-intl";
import { login as apiLogin, register, getStatus } from "@/lib/api";
import { useLocaleSetting } from "@/i18n/locale-provider";
import { LOCALES, LOCALE_LABELS } from "@/i18n/config";

interface LoginScreenProps {
  onSuccess: () => void;
}

// Cycles through supported locales, labeled with the *next* locale's
// native name (like the theme toggle labels the mode you'd switch to).
// Lives on the login screen because it's the only pre-auth surface —
// signed-in users switch from the sidebar user menu instead.
function LanguageToggle() {
  const { locale, setLocale } = useLocaleSetting();
  const next = LOCALES[(LOCALES.indexOf(locale) + 1) % LOCALES.length];
  return (
    <button
      type="button"
      onClick={() => setLocale(next)}
      className="text-xs text-zinc-600 transition hover:text-zinc-400"
    >
      {LOCALE_LABELS[next]}
    </button>
  );
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
  const t = useTranslations("login");
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
        setError(res.error || t("errorInvalidCredentials"));
        setLoading(false);
        return;
      }
      onSuccess();
    } catch {
      setError(t("errorCannotReachServer"));
      setLoading(false);
    }
  }

  async function handleSignUp(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (!signupUsername.trim() || !signupEmail.trim() || !password) {
      setError(t("errorAllFieldsRequired"));
      return;
    }
    if (password.length < 8) {
      setError(t("errorPasswordTooShort"));
      return;
    }
    if (password !== signupConfirm) {
      setError(t("errorPasswordMismatch"));
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
        setError(res.error || t("errorCouldNotCreateAccount"));
        setLoading(false);
        return;
      }
      // Register handler set the session cookie on our response, so the
      // app is effectively already signed in. Reuse the same callback
      // sign-in uses and AuthGuard will render the originally-requested
      // route without any redirect.
      onSuccess();
    } catch {
      setError(t("errorCannotReachServer"));
      setLoading(false);
    }
  }

  if (mode === "signup") {
    return (
      <div className="flex min-h-screen items-center justify-center bg-zinc-950 p-4">
        <div className="w-full max-w-sm space-y-6">
          <div className="text-center space-y-2">
            <h1 className="text-2xl font-bold text-zinc-100">{t("signUpTitle")}</h1>
            <p className="text-sm text-zinc-500">{t("signUpSubtitle")}</p>
          </div>
          <form onSubmit={handleSignUp} className="space-y-4">
            <input
              type="text"
              value={signupUsername}
              onChange={(e) => setSignupUsername(e.target.value)}
              placeholder={t("usernamePlaceholder")}
              autoFocus
              autoComplete="username"
              className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
            />
            <input
              type="email"
              value={signupEmail}
              onChange={(e) => setSignupEmail(e.target.value)}
              placeholder={t("emailPlaceholder")}
              autoComplete="email"
              className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
            />
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder={t("passwordMinPlaceholder")}
              autoComplete="new-password"
              className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
            />
            <input
              type="password"
              value={signupConfirm}
              onChange={(e) => setSignupConfirm(e.target.value)}
              placeholder={t("confirmPasswordPlaceholder")}
              autoComplete="new-password"
              className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
            />
            {error && <p className="text-sm text-red-400">{error}</p>}
            <button
              type="submit"
              disabled={loading || !signupUsername.trim() || !signupEmail.trim() || !password || !signupConfirm}
              className="w-full rounded-lg bg-violet-600 px-4 py-3 text-sm font-medium text-white transition hover:bg-violet-500 disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {loading ? t("creatingAccount") : t("createAccount")}
            </button>
          </form>
          <p className="text-center text-sm text-zinc-500">
            {t("haveAccount")}{" "}
            <button
              type="button"
              onClick={() => switchMode("signin")}
              className="text-violet-400 hover:text-violet-300"
            >
              {t("signIn")}
            </button>
          </p>
          <div className="text-center">
            <LanguageToggle />
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-zinc-950 p-4">
      <div className="w-full max-w-sm space-y-6">
        <div className="text-center space-y-2">
          <h1 className="text-2xl font-bold text-zinc-100">FastClaw</h1>
          <p className="text-sm text-zinc-500">{t("signInSubtitle")}</p>
        </div>
        <form onSubmit={handleSignIn} className="space-y-4">
          <input
            type="text"
            value={loginField}
            onChange={(e) => setLoginField(e.target.value)}
            placeholder={t("usernameOrEmailPlaceholder")}
            autoFocus
            autoComplete="username"
            className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
          />
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={t("passwordPlaceholder")}
            autoComplete="current-password"
            className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-violet-500 focus:ring-1 focus:ring-violet-500"
          />
          {error && <p className="text-sm text-red-400">{error}</p>}
          <button
            type="submit"
            disabled={loading || !loginField.trim() || !password}
            className="w-full rounded-lg bg-violet-600 px-4 py-3 text-sm font-medium text-white transition hover:bg-violet-500 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {loading ? t("signingIn") : t("signIn")}
          </button>
        </form>
        {registrationOpen && (
          <p className="text-center text-sm text-zinc-500">
            {t("noAccount")}{" "}
            <button
              type="button"
              onClick={() => switchMode("signup")}
              className="text-violet-400 hover:text-violet-300"
            >
              {t("signUp")}
            </button>
          </p>
        )}
        <div className="text-center">
          <LanguageToggle />
        </div>
      </div>
    </div>
  );
}
