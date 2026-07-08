// Augments next-intl so translation keys are type-checked against the
// English dictionary (the source of truth) and locale strings against
// the supported list. A typo'd key or a key missing from en.json fails
// `next build` instead of rendering a raw key path at runtime.
import type en from "../../messages/en.json";
import type { Locale } from "./config";

declare module "next-intl" {
  interface AppConfig {
    Locale: Locale;
    Messages: typeof en;
  }
}
