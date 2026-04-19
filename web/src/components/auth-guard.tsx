"use client";

import { useState, useEffect } from "react";
import { isLoggedIn } from "@/lib/auth";
import { LoginScreen } from "./login-screen";

interface AuthGuardProps {
  children: React.ReactNode;
}

export function AuthGuard({ children }: AuthGuardProps) {
  const [checked, setChecked] = useState(false);
  const [authed, setAuthed] = useState(false);

  useEffect(() => {
    // In local mode (no cloud), the server accepts requests without a token.
    // We detect this by trying a no-auth request: if it succeeds, skip login.
    if (isLoggedIn()) {
      setAuthed(true);
      setChecked(true);
      return;
    }

    // Try without token — local mode returns 200, cloud mode returns 401.
    fetch("/api/status")
      .then((res) => {
        if (res.ok) {
          // Local mode — no login needed.
          setAuthed(true);
        }
        setChecked(true);
      })
      .catch(() => {
        setChecked(true);
      });
  }, []);

  if (!checked) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-zinc-950">
        <div className="h-8 w-8 animate-spin rounded-full border-2 border-zinc-700 border-t-violet-500" />
      </div>
    );
  }

  if (!authed) {
    return <LoginScreen onSuccess={() => setAuthed(true)} />;
  }

  return <>{children}</>;
}
