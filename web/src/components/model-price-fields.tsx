"use client";

import { useState } from "react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useLocale } from "@/components/locale-provider";
import type { ModelCost } from "@/lib/api";

const FIELDS: { key: keyof ModelCost; en: string; zh: string }[] = [
  { key: "input", en: "Input", zh: "输入" },
  { key: "output", en: "Output", zh: "输出" },
  { key: "cacheRead", en: "Cache read", zh: "缓存读" },
  { key: "cacheWrite", en: "Cache write", zh: "缓存写" },
];

const EMPTY: ModelCost = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };

// A price of 0 means "not filled in" — the server then falls back to its
// built-in price for well-known models, or reports the cost as unknown.
function show(n: number | undefined): string {
  return n ? String(n) : "";
}

function parse(s: string): number {
  const n = Number(s.trim());
  return Number.isFinite(n) && n > 0 ? n : 0;
}

// ModelPriceFields edits a provider model's unit prices (USD per 1M
// tokens). Drafts are kept as strings so partial input like "0." types.
export function ModelPriceFields({
  cost,
  onChange,
}: {
  cost: ModelCost | undefined;
  onChange: (cost: ModelCost) => void;
}) {
  const { tr } = useLocale();
  const value = cost ?? EMPTY;
  const [drafts, setDrafts] = useState<Record<keyof ModelCost, string>>(() => ({
    input: show(value.input),
    output: show(value.output),
    cacheRead: show(value.cacheRead),
    cacheWrite: show(value.cacheWrite),
  }));

  // A draft shows only while it still parses to the stored price, so an
  // outside change (row removal shifts which model this row shows) wins.
  const shown = (key: keyof ModelCost) =>
    parse(drafts[key]) === (value[key] || 0) ? drafts[key] : show(value[key]);

  return (
    <div className="space-y-1">
      <Label className="text-xs">{tr("Price (USD per 1M tokens)", "单价（美元 / 百万 token）")}</Label>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {FIELDS.map(({ key, en, zh }) => (
          <div key={key} className="space-y-0.5">
            <span className="text-[11px] text-muted-foreground">{tr(en, zh)}</span>
            <Input
              inputMode="decimal"
              value={shown(key)}
              onChange={(e) => {
                const s = e.target.value;
                setDrafts((prev) => ({ ...prev, [key]: s }));
                onChange({ ...EMPTY, ...value, [key]: parse(s) });
              }}
              placeholder="—"
              className="font-mono text-xs h-8"
            />
          </div>
        ))}
      </div>
      <p className="text-[11px] text-muted-foreground">
        {tr(
          "Used to report each turn's cost. Leave blank to use the built-in price for well-known models; a model with no price reports its cost as unknown.",
          "用于上报每轮成本。留空则对常见模型使用内置价格；没有价格的模型，成本会报告为未知。",
        )}
      </p>
    </div>
  );
}
