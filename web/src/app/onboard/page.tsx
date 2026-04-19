"use client";

import { useState, useCallback, useEffect } from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { Textarea } from "@/components/ui/textarea";
import { testProvider, saveConfig } from "@/lib/api";
import { login as loginWithToken } from "@/lib/auth";

const PROVIDERS: Record<string, { apiBase: string; apiType: string; models: string[] }> = {
  openrouter: {
    apiBase: "https://openrouter.ai/api/v1",
    apiType: "openai-chat",
    models: [
      "openai/gpt-5.4",
      "anthropic/claude-sonnet-4.6",
      "google/gemini-3.1-flash-lite-preview",
    ],
  },
  ollama: {
    apiBase: "http://localhost:11434/v1",
    apiType: "openai-chat",
    models: ["llama3", "mistral", "codellama"],
  },
  custom: { apiBase: "", apiType: "openai-chat", models: [] },
};

const API_TYPE_OPTIONS = [
  { value: "openai-chat", label: "OpenAI Completions" },
  { value: "anthropic-messages", label: "Anthropic Messages" },
];

const AUTH_TYPE_OPTIONS = [
  { value: "api-key", label: "API Key" },
  { value: "bearer-token", label: "Bearer Token" },
];

interface OnboardConfig {
  provider: string;
  providerName: string;
  apiBase: string;
  apiKey: string;
  apiType: string;
  authType: string;
  model: string;
  telegramEnabled: boolean;
  telegramToken: string;
  port: number;
  agentName: string;
  personality: string;
  gatewayToken: string; // returned after save, for display + auto-login
}

const STEP_LABELS = [
  "Welcome",
  "LLM Provider",
  "Gateway",
  "Launch",
];

function ConfettiEffect() {
  const colors = [
    "#8b5cf6",
    "#06b6d4",
    "#10b981",
    "#f59e0b",
    "#ef4444",
    "#ec4899",
    "#6366f1",
  ];
  const pieces = Array.from({ length: 50 }, (_, i) => ({
    id: i,
    left: Math.random() * 100,
    delay: Math.random() * 2,
    color: colors[i % colors.length],
    size: 6 + Math.random() * 8,
    rotation: Math.random() * 360,
  }));

  return (
    <div className="pointer-events-none fixed inset-0 z-50 overflow-hidden">
      {pieces.map((p) => (
        <div
          key={p.id}
          className="confetti-piece"
          style={{
            left: `${p.left}%`,
            animationDelay: `${p.delay}s`,
            backgroundColor: p.color,
            width: `${p.size}px`,
            height: `${p.size}px`,
            borderRadius: p.id % 3 === 0 ? "50%" : "2px",
            transform: `rotate(${p.rotation}deg)`,
          }}
        />
      ))}
    </div>
  );
}

export default function OnboardPage() {
  const router = useRouter();
  const [step, setStep] = useState(0);
  const [config, setConfig] = useState<OnboardConfig>({
    provider: "openrouter",
    providerName: "openrouter",
    apiBase: "https://openrouter.ai/api/v1",
    apiKey: "",
    apiType: "openai-chat",
    authType: "api-key",
    model: "openai/gpt-5.4",
    telegramEnabled: false,
    telegramToken: "",
    port: 18953,
    agentName: "FastClaw",
    personality:
      "You are a helpful, friendly AI assistant. You respond concisely and accurately.",
    gatewayToken: "",
  });
  const [testStatus, setTestStatus] = useState<
    "idle" | "testing" | "success" | "error"
  >("idle");
  const [testError, setTestError] = useState("");
  const [showConfetti, setShowConfetti] = useState(false);
  const [launched, setLaunched] = useState(false);
  const [copiedToken, setCopiedToken] = useState(false);
  const [mounted, setMounted] = useState(false);

  useEffect(() => {
    setMounted(true);
  }, []);

  const updateConfig = useCallback(
    (updates: Partial<OnboardConfig>) => {
      setConfig((prev) => ({ ...prev, ...updates }));
    },
    []
  );

  // Generate gateway token when entering the final step
  useEffect(() => {
    if (step === 3 && !config.gatewayToken) {
      const chars = "abcdef0123456789";
      let token = "";
      for (let i = 0; i < 64; i++) {
        token += chars[Math.floor(Math.random() * chars.length)];
      }
      updateConfig({ gatewayToken: token });
    }
  }, [step, config.gatewayToken, updateConfig]);

  const handleProviderChange = useCallback(
    (provider: string | null) => {
      if (!provider) return;
      const preset = PROVIDERS[provider];
      updateConfig({
        provider,
        providerName: provider === "custom" ? "" : provider,
        apiBase: preset.apiBase,
        apiType: preset.apiType,
        model: preset.models[0] || "",
      });
      setTestStatus("idle");
    },
    [updateConfig]
  );

  const handleTestConnection = useCallback(async () => {
    setTestStatus("testing");
    setTestError("");
    try {
      const result = await testProvider({
        apiBase: config.apiBase,
        apiKey: config.apiKey,
        model: config.model,
        apiType: config.apiType,
        authType: config.authType,
      });
      if (result.ok) {
        setTestStatus("success");
      } else {
        const urlInfo = result.url ? `\nRequest URL: ${result.url}` : "";
        setTestStatus("error");
        setTestError((result.error || "Connection failed") + urlInfo);
      }
    } catch {
      setTestStatus("error");
      setTestError("Could not reach the server. Is FastClaw running?");
    }
  }, [config.apiBase, config.apiKey, config.model, config.apiType, config.authType]);

  const handleLaunch = useCallback(async () => {
    // Auto-login before saving (in case the server restarts and drops the connection)
    if (config.gatewayToken) {
      loginWithToken(config.gatewayToken);
    }

    try {
      await saveConfig(config as unknown as Record<string, unknown>);
    } catch {
      // Server may restart mid-request — that's expected
    }

    setShowConfetti(true);
    setLaunched(true);
    setTimeout(() => setShowConfetti(false), 4000);
    // Wait for server to restart, then redirect
    setTimeout(() => {
      window.location.href = "/overview/";
    }, 3000);
  }, [config]);

  const canProceed = useCallback(() => {
    switch (step) {
      case 0:
        return true;
      case 1:
        return config.apiBase.length > 0 && config.model.length > 0;
      case 2:
        return config.agentName.length > 0 && config.port > 0;
      case 3:
        return true;
      default:
        return false;
    }
  }, [step, config]);

  if (!mounted) return null;

  return (
    <div className="relative flex min-h-screen flex-col items-center justify-center bg-background px-4 py-12">
      {showConfetti && <ConfettiEffect />}

      <div className="pointer-events-none absolute inset-0 overflow-hidden">
        <div className="absolute -top-[40%] left-1/2 h-[800px] w-[800px] -translate-x-1/2 rounded-full bg-primary/5 blur-3xl" />
      </div>

      <div className="relative mb-10 flex items-center gap-2">
        {STEP_LABELS.map((label, i) => (
          <div key={label} className="flex items-center gap-2">
            <button
              onClick={() => i < step && setStep(i)}
              className={`flex h-9 w-9 items-center justify-center rounded-full text-sm font-medium transition-all duration-300 ${
                i === step
                  ? "bg-primary text-primary-foreground shadow-lg shadow-primary/25 scale-110"
                  : i < step
                    ? "bg-primary/20 text-primary hover:bg-primary/30 cursor-pointer"
                    : "bg-muted text-muted-foreground"
              }`}
              disabled={i > step}
            >
              {i < step ? (
                <svg
                  className="h-4 w-4"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                  strokeWidth={2.5}
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    d="M5 13l4 4L19 7"
                  />
                </svg>
              ) : (
                i + 1
              )}
            </button>
            {i < STEP_LABELS.length - 1 && (
              <div
                className={`hidden h-px w-8 sm:block ${
                  i < step ? "bg-primary/40" : "bg-border"
                }`}
              />
            )}
          </div>
        ))}
      </div>

      <p className="relative mb-6 text-sm font-medium tracking-wide text-muted-foreground uppercase">
        {STEP_LABELS[step]}
      </p>

      <div className="relative w-full max-w-lg animate-fade-in-up" key={step}>
        {step === 0 && (
          <Card className="backdrop-blur-sm">
            <CardHeader className="space-y-6 pb-4 text-center">
              <div className="mx-auto flex h-16 w-16 items-center justify-center">
                <img src="/logo.png" alt="FastClaw" className="h-16 w-16 rounded-2xl" />
              </div>
              <div>
                <CardTitle className="text-3xl font-bold">
                  <span className="animate-gradient-text bg-gradient-to-r from-violet-400 via-cyan-400 to-violet-400 bg-clip-text text-transparent">
                    FastClaw
                  </span>
                </CardTitle>
              </div>
            </CardHeader>
            <CardContent className="space-y-6 text-center">
              <p className="text-sm leading-relaxed text-muted-foreground">
                Set up your AI agent in a few simple steps. Configure your LLM
                provider, connect messaging channels, and launch your agent.
              </p>
              <Button
                onClick={() => setStep(1)}
                className="w-full"
                size="lg"
              >
                Get Started
                <svg
                  className="ml-2 h-4 w-4"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                  strokeWidth={2}
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    d="M13 7l5 5m0 0l-5 5m5-5H6"
                  />
                </svg>
              </Button>
            </CardContent>
          </Card>
        )}

        {step === 1 && (
          <Card className="backdrop-blur-sm">
            <CardHeader>
              <CardTitle className="text-xl">LLM Provider</CardTitle>
              <CardDescription>
                Choose your AI model provider and configure the connection.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-5">
              <div className="space-y-2">
                <Label>Provider</Label>
                <Select
                  value={config.provider}
                  onValueChange={handleProviderChange}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {Object.keys(PROVIDERS).map((p) => (
                      <SelectItem key={p} value={p}>
                        <span className="capitalize">{p}</span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>

              {config.provider === "custom" && (
                <div className="space-y-2">
                  <Label>Provider Name</Label>
                  <Input
                    value={config.providerName}
                    onChange={(e) => updateConfig({ providerName: e.target.value })}
                    placeholder="e.g. my-provider"
                    className="font-mono text-sm"
                  />
                </div>
              )}

              <div className="space-y-2">
                <Label>API Base URL</Label>
                <Input
                  value={config.apiBase}
                  onChange={(e) => updateConfig({ apiBase: e.target.value })}
                  placeholder="https://api.openai.com/v1"
                  className="font-mono text-sm"
                />
              </div>

              <div className="space-y-2">
                <Label>API Key</Label>
                <Input
                  type="password"
                  value={config.apiKey}
                  onChange={(e) => updateConfig({ apiKey: e.target.value })}
                  placeholder={
                    config.provider === "ollama"
                      ? "Not required for Ollama"
                      : "sk-..."
                  }
                  className="font-mono text-sm"
                />
              </div>

              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-2">
                  <Label>API Type</Label>
                  <Select
                    value={config.apiType}
                    onValueChange={(v) => v && updateConfig({ apiType: v })}
                  >
                    <SelectTrigger className="w-full text-sm">
                      <SelectValue>
                        {API_TYPE_OPTIONS.find((o) => o.value === config.apiType)?.label}
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      {API_TYPE_OPTIONS.map((opt) => (
                        <SelectItem key={opt.value} value={opt.value}>
                          {opt.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label>Auth Type</Label>
                  <Select
                    value={config.authType}
                    onValueChange={(v) => v && updateConfig({ authType: v })}
                  >
                    <SelectTrigger className="w-full text-sm">
                      <SelectValue>
                        {AUTH_TYPE_OPTIONS.find((o) => o.value === config.authType)?.label}
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      {AUTH_TYPE_OPTIONS.map((opt) => (
                        <SelectItem key={opt.value} value={opt.value}>
                          {opt.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>

              <div className="space-y-2">
                <Label>Model</Label>
                <Input
                  value={config.model}
                  onChange={(e) => updateConfig({ model: e.target.value })}
                  placeholder="Enter model name"
                  className="font-mono text-sm"
                />
                {PROVIDERS[config.provider]?.models.length > 0 && (
                  <div className="flex flex-wrap gap-1.5">
                    {PROVIDERS[config.provider].models.map((m) => (
                      <button
                        key={m}
                        type="button"
                        onClick={() => updateConfig({ model: m })}
                        className={`rounded-md border px-2 py-0.5 text-xs font-mono transition-colors ${
                          config.model === m
                            ? "border-primary bg-primary/10 text-primary"
                            : "border-border text-muted-foreground hover:border-primary/50 hover:text-foreground"
                        }`}
                      >
                        {m}
                      </button>
                    ))}
                  </div>
                )}
              </div>

              <Separator />

              <div className="space-y-2">
                <div className="flex items-center gap-3">
                  <Button
                    variant="outline"
                    onClick={handleTestConnection}
                    disabled={testStatus === "testing"}
                  >
                    {testStatus === "testing" ? (
                      <>
                        <svg
                          className="mr-2 h-4 w-4 animate-spin"
                          fill="none"
                          viewBox="0 0 24 24"
                        >
                          <circle
                            className="opacity-25"
                            cx="12"
                            cy="12"
                            r="10"
                            stroke="currentColor"
                            strokeWidth="4"
                          />
                          <path
                            className="opacity-75"
                            fill="currentColor"
                            d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
                          />
                        </svg>
                        Testing...
                      </>
                    ) : (
                      "Test Connection"
                    )}
                  </Button>
                  {testStatus === "success" && (
                    <Badge
                      variant="outline"
                      className="bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/20"
                    >
                      Connected
                    </Badge>
                  )}
                </div>
                {testStatus === "error" && (
                  <p className="text-sm text-destructive break-all">
                    {testError || "Connection failed"}
                  </p>
                )}
              </div>

              <NavigationButtons
                step={step}
                setStep={setStep}
                canProceed={canProceed()}
              />
            </CardContent>
          </Card>
        )}

        {step === 2 && (
          <Card className="backdrop-blur-sm">
            <CardHeader>
              <CardTitle className="text-xl">Gateway Settings</CardTitle>
              <CardDescription>
                Configure your agent identity and server settings.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-5">
              <div className="space-y-2">
                <Label>Agent Name</Label>
                <Input
                  value={config.agentName}
                  onChange={(e) =>
                    updateConfig({ agentName: e.target.value })
                  }
                  placeholder="My AI Agent"
                />
              </div>

              <div className="space-y-2">
                <Label>Port</Label>
                <Input
                  type="number"
                  value={config.port}
                  onChange={(e) =>
                    updateConfig({ port: parseInt(e.target.value) || 18953 })
                  }
                  className="font-mono"
                />
              </div>

              <div className="space-y-2">
                <Label>
                  Personality{" "}
                  <span className="text-xs text-muted-foreground">(SOUL.md)</span>
                </Label>
                <Textarea
                  value={config.personality}
                  onChange={(e) =>
                    updateConfig({ personality: e.target.value })
                  }
                  rows={5}
                  placeholder="Describe your agent's personality, tone, and behavior..."
                  className="text-sm resize-none"
                />
                <p className="text-xs text-muted-foreground">
                  This defines how your agent communicates and behaves.
                </p>
              </div>

              <NavigationButtons
                step={step}
                setStep={setStep}
                canProceed={canProceed()}
              />
            </CardContent>
          </Card>
        )}

        {step === 3 && (
          <Card className="backdrop-blur-sm animate-pulse-glow">
            <CardHeader>
              <CardTitle className="text-xl">
                {launched ? "You're All Set!" : "Review & Launch"}
              </CardTitle>
              <CardDescription>
                {launched
                  ? "FastClaw is now configured and ready to go."
                  : "Review your configuration before launching."}
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-5">
              {launched ? (
                <div className="space-y-4 text-center py-4">
                  <div className="mx-auto flex h-16 w-16 items-center justify-center rounded-full bg-emerald-500/20">
                    <svg
                      className="h-8 w-8 text-emerald-500"
                      fill="none"
                      viewBox="0 0 24 24"
                      stroke="currentColor"
                      strokeWidth={2}
                    >
                      <path
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        d="M5 13l4 4L19 7"
                      />
                    </svg>
                  </div>
                  <p className="text-lg font-medium">
                    Configuration saved successfully
                  </p>
                  <p className="text-sm text-muted-foreground">
                    Redirecting to dashboard...
                  </p>
                </div>
              ) : (
                <>
                  <div className="space-y-3">
                    <SummaryRow
                      label="Provider"
                      value={config.provider}
                      capitalize
                    />
                    <SummaryRow label="Model" value={config.model} mono />
                    <SummaryRow label="API Base" value={config.apiBase} mono />
                    <SummaryRow
                      label="API Key"
                      value={config.apiKey ? "********" : "Not set"}
                    />
                    <Separator />
                    <SummaryRow label="Agent Name" value={config.agentName} />
                    <SummaryRow
                      label="Port"
                      value={String(config.port)}
                      mono
                    />
                  </div>

                  <div className="rounded-lg border border-amber-500/30 bg-amber-500/5 p-4 space-y-2">
                    <p className="text-sm font-medium text-amber-500">Admin Token</p>
                    <p className="text-xs text-muted-foreground">
                      Save this token — you&apos;ll need it to log in to the admin dashboard.
                    </p>
                    <div className="flex items-center gap-2">
                      <code className="flex-1 rounded-md bg-background px-3 py-2 font-mono text-xs break-all select-all">
                        {config.gatewayToken || "auto-generated on launch"}
                      </code>
                      <button
                        onClick={() => {
                          const token = config.gatewayToken;
                          if (token) {
                            navigator.clipboard.writeText(token);
                            setCopiedToken(true);
                            setTimeout(() => setCopiedToken(false), 2000);
                          }
                        }}
                        className="shrink-0 rounded-md border border-border p-2 text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors"
                        title="Copy token"
                      >
                        {copiedToken ? (
                          <svg className="h-4 w-4 text-emerald-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}><path strokeLinecap="round" strokeLinejoin="round" d="M5 13l4 4L19 7" /></svg>
                        ) : (
                          <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}><path strokeLinecap="round" strokeLinejoin="round" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" /></svg>
                        )}
                      </button>
                    </div>
                  </div>

                  <Button
                    onClick={handleLaunch}
                    className="w-full bg-gradient-to-r from-violet-600 to-cyan-600 text-white hover:from-violet-700 hover:to-cyan-700 transition-all"
                    size="lg"
                  >
                    Launch FastClaw
                    <svg
                      className="ml-2 h-4 w-4"
                      fill="none"
                      viewBox="0 0 24 24"
                      stroke="currentColor"
                      strokeWidth={2}
                    >
                      <path
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        d="M15.59 14.37a6 6 0 01-5.84 7.38v-4.8m5.84-2.58a14.98 14.98 0 006.16-12.12A14.98 14.98 0 009.631 8.41m5.96 5.96a14.926 14.926 0 01-5.841 2.58m-.119-8.54a6 6 0 00-7.381 5.84h4.8m2.58-5.84a14.927 14.927 0 00-2.58 5.84m2.699 2.7c-.103.021-.207.041-.311.06a15.09 15.09 0 01-2.448-2.448 14.9 14.9 0 01.06-.312m-2.24 2.39a4.493 4.493 0 00-1.757 4.306 4.493 4.493 0 004.306-1.758M16.5 9a1.5 1.5 0 11-3 0 1.5 1.5 0 013 0z"
                      />
                    </svg>
                  </Button>

                  <NavigationButtons
                    step={step}
                    setStep={setStep}
                    canProceed={canProceed()}
                    hidNext
                  />
                </>
              )}
            </CardContent>
          </Card>
        )}
      </div>

      <p className="relative mt-8 text-xs text-muted-foreground/50">
        FastClaw Agent Framework
      </p>
    </div>
  );
}

function SummaryRow({
  label,
  value,
  mono,
  capitalize,
}: {
  label: string;
  value: string;
  mono?: boolean;
  capitalize?: boolean;
}) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-sm text-muted-foreground">{label}</span>
      <span
        className={`text-sm ${mono ? "font-mono" : ""} ${capitalize ? "capitalize" : ""}`}
      >
        {value}
      </span>
    </div>
  );
}

function NavigationButtons({
  step,
  setStep,
  canProceed,
  hidNext,
}: {
  step: number;
  setStep: (s: number) => void;
  canProceed: boolean;
  hidNext?: boolean;
}) {
  return (
    <div className="flex items-center justify-between pt-2">
      <Button
        variant="ghost"
        onClick={() => setStep(step - 1)}
        disabled={step === 0}
      >
        <svg
          className="mr-1 h-4 w-4"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
          strokeWidth={2}
        >
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            d="M11 17l-5-5m0 0l5-5m-5 5h12"
          />
        </svg>
        Back
      </Button>
      {!hidNext && (
        <Button
          onClick={() => setStep(step + 1)}
          disabled={!canProceed}
        >
          Next
          <svg
            className="ml-1 h-4 w-4"
            fill="none"
            viewBox="0 0 24 24"
            stroke="currentColor"
            strokeWidth={2}
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              d="M13 7l5 5m0 0l-5 5m5-5H6"
            />
          </svg>
        </Button>
      )}
    </div>
  );
}
