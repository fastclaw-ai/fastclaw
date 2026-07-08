"use client";

import { useMemo } from "react";
import { Sun, Moon, Monitor } from "lucide-react";
import { useTranslations } from "next-intl";
import { useTheme, type Theme } from "@/components/theme-provider";

type SettingsTranslator = ReturnType<typeof useTranslations<"settings">>;

const THEME_CHOICES = (
  t: SettingsTranslator,
): Array<{ value: Theme; label: string; icon: React.ComponentType<{ className?: string }> }> => [
  { value: "light", label: t("generalThemeLight"), icon: Sun },
  { value: "dark", label: t("generalThemeDark"), icon: Moon },
  { value: "system", label: t("generalThemeSystem"), icon: Monitor },
];

export default function GeneralSettingsPage() {
  const t = useTranslations("settings");
  const { theme, setTheme } = useTheme();
  const choices = useMemo(() => THEME_CHOICES(t), [t]);

  return (
    <div className="space-y-6">
      <div>
        <h3 className="text-xl font-semibold tracking-tight">{t("generalTitle")}</h3>
        <p className="text-sm text-muted-foreground mt-1">
          {t("generalDescription")}
        </p>
      </div>

      <div className="rounded-lg border border-border bg-card p-5">
        <h4 className="font-medium mb-1">{t("generalThemeTitle")}</h4>
        <p className="text-sm text-muted-foreground mb-4">
          {t("generalThemeDescription")}
        </p>
        <div className="grid grid-cols-3 gap-3 max-w-md">
          {choices.map((c) => {
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
                {c.label}
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
}
