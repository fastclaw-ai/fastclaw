"use client";

import * as React from "react";
import { NextIntlClientProvider } from "next-intl";
import en from "../../messages/en.json";
import zhCN from "../../messages/zh-CN.json";
import {
  DEFAULT_LOCALE,
  LOCALES,
  LOCALE_STORAGE_KEY,
  type Locale,
} from "./config";

// Both dictionaries are imported statically and shipped in the bundle.
// That's deliberate: the app is a static export served from a Go binary,
// so there's no server to lazy-load the active dictionary from, and
// bundling both keeps locale switching instant with no fetch waterfall.
const MESSAGES: Record<Locale, typeof en> = {
  en,
  "zh-CN": zhCN,
};

interface LocaleContextValue {
  locale: Locale;
  setLocale: (locale: Locale) => void;
}

const LocaleContext = React.createContext<LocaleContextValue>({
  locale: DEFAULT_LOCALE,
  setLocale: () => {},
});

// useLocaleSetting exposes the active locale plus a persisting setter —
// this is what language switchers consume. (Named to avoid clashing
// with next-intl's own useLocale.)
export function useLocaleSetting() {
  return React.useContext(LocaleContext);
}

// Match browser languages against supported locales by primary subtag,
// so zh / zh-Hans / zh-Hans-CN / zh-TW all resolve to zh-CN until more
// specific variants ship.
function detectBrowserLocale(): Locale | null {
  const candidates = navigator.languages ?? [navigator.language];
  for (const lang of candidates) {
    if (!lang) continue;
    const primary = lang.toLowerCase().split("-")[0];
    const hit = LOCALES.find((l) => l.toLowerCase().split("-")[0] === primary);
    if (hit) return hit;
  }
  return null;
}

export function LocaleProvider({ children }: { children: React.ReactNode }) {
  // First client render must match the prerendered HTML (which was built
  // with DEFAULT_LOCALE), so the stored/browser preference is applied in
  // an effect after mount — same pattern the theme provider uses.
  const [locale, setLocaleState] = React.useState<Locale>(DEFAULT_LOCALE);

  React.useEffect(() => {
    let stored: string | null = null;
    try {
      stored = localStorage.getItem(LOCALE_STORAGE_KEY);
    } catch {
      // storage blocked — fall through to browser detection
    }
    if (stored && (LOCALES as readonly string[]).includes(stored)) {
      setLocaleState(stored as Locale);
      return;
    }
    const detected = detectBrowserLocale();
    if (detected) setLocaleState(detected);
  }, []);

  // Keep <html lang> in sync — layout.tsx renders the build-time default,
  // this corrects it whenever the effective locale differs.
  React.useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  const setLocale = React.useCallback((next: Locale) => {
    try {
      localStorage.setItem(LOCALE_STORAGE_KEY, next);
    } catch {
      // storage blocked — switch still applies for this page load
    }
    setLocaleState(next);
  }, []);

  const value = React.useMemo(() => ({ locale, setLocale }), [locale, setLocale]);

  return (
    <LocaleContext.Provider value={value}>
      <NextIntlClientProvider locale={locale} messages={MESSAGES[locale]}>
        {children}
      </NextIntlClientProvider>
    </LocaleContext.Provider>
  );
}
