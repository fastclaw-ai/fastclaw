// i18n runtime configuration.
//
// The web UI is statically exported and embedded into the Go binary, so
// there is no per-request locale negotiation. Locale resolution instead
// happens in three layers (see locale-provider.tsx):
//
//   1. user preference persisted in localStorage (set via the language
//      switcher in the sidebar user menu / login screen)
//   2. browser language (navigator.languages) matched by primary subtag
//   3. build-time default — NEXT_PUBLIC_DEFAULT_LOCALE, inlined by Next
//      at `next build` time, e.g.:
//
//        NEXT_PUBLIC_DEFAULT_LOCALE=zh-CN make build-web
//
// The build-time default also decides which language the prerendered
// HTML ships in, i.e. what users see on first paint before hydration.

export const LOCALES = ["en", "zh-CN"] as const;

export type Locale = (typeof LOCALES)[number];

// Native-language names, shown by the language switcher.
export const LOCALE_LABELS: Record<Locale, string> = {
  en: "English",
  "zh-CN": "中文",
};

export const LOCALE_STORAGE_KEY = "fastclaw-locale";

function isLocale(value: string | undefined): value is Locale {
  return !!value && (LOCALES as readonly string[]).includes(value);
}

const envDefault = process.env.NEXT_PUBLIC_DEFAULT_LOCALE;

export const DEFAULT_LOCALE: Locale = isLocale(envDefault) ? envDefault : "en";
