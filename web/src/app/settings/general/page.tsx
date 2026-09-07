"use client";

import { Languages, Monitor, Moon, Sun } from "lucide-react";
import { useTheme, type Theme } from "@/components/theme-provider";
import {
  useLocale,
  type Locale,
  type MessageKey,
} from "@/components/locale-provider";

const themeChoices: Array<{
  value: Theme;
  labelKey: MessageKey;
  icon: React.ComponentType<{ className?: string }>;
}> = [
  { value: "light", labelKey: "settings.theme.light", icon: Sun },
  { value: "dark", labelKey: "settings.theme.dark", icon: Moon },
  { value: "system", labelKey: "settings.theme.system", icon: Monitor },
];

const languageChoices: Array<{ value: Locale; labelKey: MessageKey }> = [
  { value: "en", labelKey: "settings.language.english" },
  { value: "zh-CN", labelKey: "settings.language.chinese" },
];

export default function GeneralSettingsPage() {
  const { theme, setTheme } = useTheme();
  const { locale, setLocale, t } = useLocale();

  return (
    <div className="space-y-6">
      <div>
        <h3 className="text-xl font-semibold tracking-tight">
          {t("settings.general.title")}
        </h3>
        <p className="text-sm text-muted-foreground mt-1">
          {t("settings.general.description")}
        </p>
      </div>

      <div className="rounded-lg border border-border bg-card p-5">
        <h4 className="font-medium mb-1">{t("settings.theme.title")}</h4>
        <p className="text-sm text-muted-foreground mb-4">
          {t("settings.theme.description")}
        </p>
        <div className="grid grid-cols-3 gap-3 max-w-md">
          {themeChoices.map((c) => {
            const active = theme === c.value;
            const Icon = c.icon;
            return (
              <button
                key={c.value}
                type="button"
                onClick={() => setTheme(c.value)}
                className={
                  "flex flex-col items-center gap-2 rounded-md border px-3 py-4 text-sm transition " +
                  (active
                    ? "border-primary bg-primary/10 text-primary"
                    : "border-border hover:bg-muted")
                }
              >
                <Icon className="size-5" />
                {t(c.labelKey)}
              </button>
            );
          })}
        </div>
      </div>

      <div className="rounded-lg border border-border bg-card p-5">
        <div className="mb-1 flex items-center gap-2">
          <Languages className="size-4 text-muted-foreground" />
          <h4 className="font-medium">{t("settings.language.title")}</h4>
        </div>
        <p className="mb-4 text-sm text-muted-foreground">
          {t("settings.language.description")}
        </p>
        <div className="grid max-w-sm grid-cols-2 gap-3">
          {languageChoices.map((choice) => {
            const active = locale === choice.value;
            return (
              <button
                key={choice.value}
                type="button"
                onClick={() => setLocale(choice.value)}
                className={
                  "rounded-md border px-3 py-3 text-sm font-medium transition " +
                  (active
                    ? "border-primary bg-primary/10 text-primary"
                    : "border-border hover:bg-muted")
                }
              >
                {t(choice.labelKey)}
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
}
