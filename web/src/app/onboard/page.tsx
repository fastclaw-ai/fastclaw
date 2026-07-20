"use client";

import { useState, useCallback, useEffect, useMemo } from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Separator } from "@/components/ui/separator";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  ArrowLeft,
  ArrowRight,
  Bot,
  Check,
  Container,
  KeyRound,
  Loader2,
  PartyPopper,
  Sparkles,
  UserPlus,
} from "lucide-react";
import { getStatus, onboard, testProvider } from "@/lib/api";

const STEPS = [
  { id: "welcome", labelKey: "stepWelcome", icon: PartyPopper },
  { id: "admin", labelKey: "stepAdmin", icon: UserPlus },
  { id: "provider", labelKey: "stepProvider", icon: KeyRound },
  { id: "agent", labelKey: "stepAgent", icon: Bot },
  { id: "sandbox", labelKey: "stepSandbox", icon: Container },
  { id: "launch", labelKey: "stepLaunch", icon: Sparkles },
] as const;

type OnboardTranslator = ReturnType<typeof useTranslations<"onboard">>;

// Display label maps. base-ui's <Select.Value /> renders the raw `value`
// (the SelectItem's `value` prop) by default, not the SelectItem's
// children — so we explicitly map keys to titles via the children render
// prop on SelectValue. Keep these in sync with the SelectItem lists.
const PROVIDER_LABELS = (t: OnboardTranslator): Record<string, string> => ({
  openai: "OpenAI",
  openrouter: "OpenRouter",
  anthropic: "Anthropic",
  deepseek: "DeepSeek",
  ollama: "Ollama",
  custom: t("providerCustom"),
});

const API_TYPE_LABELS: Record<string, string> = {
  "openai-chat": "OpenAI Chat Completions",
  "anthropic-messages": "Anthropic Messages",
};

const AUTH_TYPE_LABELS = (t: OnboardTranslator): Record<string, string> => ({
  "bearer-token": t("authTypeBearer"),
  "api-key": t("authTypeApiKeyHeader"),
});

// PROVIDERS holds the per-preset defaults the form pre-fills when the
// user picks a provider from the dropdown. `models[0]` is shown as the
// placeholder in the Default model input — the user types over it.
// authType is synced too so switching from Anthropic (api-key) to a
// Bearer-token provider doesn't leave the form on the wrong auth.
const PROVIDERS: Record<
  string,
  { apiBase: string; apiType: string; authType: string; models: string[] }
> = {
  openai: {
    apiBase: "https://api.openai.com/v1",
    apiType: "openai-chat",
    authType: "bearer-token",
    models: ["gpt-5.5"],
  },
  openrouter: {
    apiBase: "https://openrouter.ai/api/v1",
    apiType: "openai-chat",
    authType: "bearer-token",
    models: ["google/gemini-3-flash-preview"],
  },
  anthropic: {
    apiBase: "https://api.anthropic.com",
    apiType: "anthropic-messages",
    authType: "api-key",
    models: ["claude-opus-4-7", "claude-sonnet-4-7", "claude-haiku-4-5"],
  },
  deepseek: {
    apiBase: "https://api.deepseek.com",
    apiType: "openai-chat",
    authType: "bearer-token",
    models: ["deepseek-v4-pro"],
  },
  ollama: {
    apiBase: "http://localhost:11434/v1",
    apiType: "openai-chat",
    authType: "bearer-token",
    models: ["qwen3.5:35b-a3b-int4"],
  },
  custom: { apiBase: "", apiType: "openai-chat", authType: "bearer-token", models: [] },
};

export default function OnboardPage() {
  const t = useTranslations("onboard");
  const router = useRouter();
  const [step, setStep] = useState(0);

  // Already-onboarded probe — /api/status returns configured=true once
  // any account exists, in which case the wizard has nothing to do and
  // we kick the visitor to the dashboard. Redirect via router.replace so
  // Back doesn't bounce them back into onboard.
  useEffect(() => {
    let cancelled = false;
    getStatus()
      .then((s) => {
        if (!cancelled && s?.configured) router.replace("/overview/");
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [router]);

  // Admin
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [passwordConfirm, setPasswordConfirm] = useState("");
  const [displayName, setDisplayName] = useState("");

  // Provider
  const [providerEnabled, setProviderEnabled] = useState(true);
  const [providerKey, setProviderKey] = useState("openai");
  const [providerName, setProviderName] = useState("openai");
  const [apiBase, setApiBase] = useState(PROVIDERS.openai.apiBase);
  const [apiKey, setApiKey] = useState("");
  const [apiType, setApiType] = useState(PROVIDERS.openai.apiType);
  const [authType, setAuthType] = useState("bearer-token");
  const [model, setModel] = useState(PROVIDERS.openai.models[0]);
  const [testStatus, setTestStatus] = useState<"" | "ok" | "fail" | "running">(
    "",
  );
  const [testError, setTestError] = useState("");

  // Agent
  const [agentName, setAgentName] = useState("default");

  // Sandbox (optional — disabled by default; user can flip and configure)
  const [sandboxEnabled, setSandboxEnabled] = useState(false);
  const [sandboxBackend, setSandboxBackend] = useState("docker");
  const [sandboxDockerImage, setSandboxDockerImage] = useState("thinkany/fastclaw-sandbox:latest");
  const [sandboxE2BTemplate, setSandboxE2BTemplate] = useState("base");
  const [sandboxE2BKey, setSandboxE2BKey] = useState("");
  const [sandboxBoxliteImage, setSandboxBoxliteImage] = useState("");
  const [sandboxBoxliteKey, setSandboxBoxliteKey] = useState("");
  const [sandboxBoxliteURL, setSandboxBoxliteURL] = useState("");

  // Submit state
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState("");

  const handleProviderChange = useCallback((next: string) => {
    setProviderKey(next);
    const preset = PROVIDERS[next];
    if (preset) {
      setApiBase(preset.apiBase);
      setApiType(preset.apiType);
      setAuthType(preset.authType);
      if (preset.models[0]) setModel(preset.models[0]);
    }
    // Provider name auto-fills with the preset key — user can still
    // override (lets them rename "openai" to e.g. "production" before
    // creating). Custom provider clears the field so the user types one.
    setProviderName(next === "custom" ? "" : next);
    setTestStatus("");
    setTestError("");
  }, []);

  async function handleTest() {
    if (!apiKey) {
      setTestStatus("fail");
      setTestError(t("errorApiKeyRequired"));
      return;
    }
    setTestStatus("running");
    setTestError("");
    const res = await testProvider({ apiBase, apiKey, model, apiType, authType });
    if (res.ok) {
      setTestStatus("ok");
    } else {
      setTestStatus("fail");
      setTestError(res.error || t("errorTestFailed"));
    }
  }

  async function handleSubmit() {
    setSubmitError("");
    setSubmitting(true);
    // The user can rename a preset provider; we still slugify whatever
    // they typed (lowercase, hyphens) so it's a clean key in the DB.
    // When providerEnabled is false, send empty provider/apiKey/model so
    // the backend's `if req.Provider != "" && req.APIKey != ""` guard skips
    // the provider+defaults write entirely (handlers_admin.go:240).
    const finalProviderName =
      providerName.trim().toLowerCase().replace(/\s+/g, "-") || providerKey;
    const res = await onboard({
      username,
      email,
      password,
      displayName,
      provider: providerEnabled ? finalProviderName : "",
      apiBase: providerEnabled ? apiBase : "",
      apiKey: providerEnabled ? apiKey : "",
      apiType: providerEnabled ? apiType : "",
      authType: providerEnabled ? authType : "",
      model: providerEnabled ? model : "",
      agentName,
      sandboxEnabled,
      sandboxBackend: sandboxEnabled ? sandboxBackend : undefined,
      sandboxImage: sandboxEnabled
        ? sandboxBackend === "docker"
          ? sandboxDockerImage
          : sandboxBackend === "e2b"
            ? sandboxE2BTemplate
            : sandboxBackend === "boxlite"
              ? sandboxBoxliteImage
              : undefined
        : undefined,
      sandboxE2BKey: sandboxEnabled && sandboxBackend === "e2b" ? sandboxE2BKey : undefined,
      sandboxBoxliteKey: sandboxEnabled && sandboxBackend === "boxlite" ? sandboxBoxliteKey : undefined,
      sandboxBoxliteUrl:
        sandboxEnabled && sandboxBackend === "boxlite" && sandboxBoxliteURL
          ? sandboxBoxliteURL
          : undefined,
    });
    setSubmitting(false);
    if (!res.ok) {
      setSubmitError(res.error || t("errorOnboardFailed"));
      setStep(1); // jump back to admin step where most errors come from
      return;
    }
    setStep(STEPS.length - 1);
  }

  // Validation per step — drives the Next button's disabled state.
  const sandboxValid =
    !sandboxEnabled ||
    (sandboxBackend === "docker"
      ? sandboxDockerImage.trim() !== ""
      : sandboxBackend === "e2b"
        ? sandboxE2BKey.trim() !== "" && sandboxE2BTemplate.trim() !== ""
        : sandboxBackend === "boxlite"
          ? sandboxBoxliteKey.trim() !== "" && sandboxBoxliteImage.trim() !== ""
          : false);
  const stepValid: boolean[] = [
    true,
    username.trim() !== "" &&
      email.trim() !== "" &&
      password.length >= 6 &&
      password === passwordConfirm,
    !providerEnabled ||
      (apiKey.trim() !== "" && model.trim() !== "" && apiBase.trim() !== "" && testStatus === "ok"),
    agentName.trim() !== "",
    sandboxValid,
    true,
  ];

  return (
    <div className="flex min-h-screen items-center justify-center bg-muted/30 p-4">
      <div className="w-full max-w-2xl space-y-6">
        <Stepper current={step} />

        {step === 0 && <WelcomeStep />}

        {step === 1 && (
          <AdminStep
            username={username}
            setUsername={setUsername}
            email={email}
            setEmail={setEmail}
            password={password}
            setPassword={setPassword}
            passwordConfirm={passwordConfirm}
            setPasswordConfirm={setPasswordConfirm}
            displayName={displayName}
            setDisplayName={setDisplayName}
          />
        )}

        {step === 2 && (
          <ProviderStep
            enabled={providerEnabled}
            setEnabled={setProviderEnabled}
            providerKey={providerKey}
            onProviderChange={handleProviderChange}
            providerName={providerName}
            setProviderName={setProviderName}
            apiBase={apiBase}
            setApiBase={setApiBase}
            apiKey={apiKey}
            setApiKey={setApiKey}
            apiType={apiType}
            setApiType={setApiType}
            authType={authType}
            setAuthType={setAuthType}
            model={model}
            setModel={setModel}
            onTest={handleTest}
            testStatus={testStatus}
            testError={testError}
          />
        )}

        {step === 3 && (
          <AgentStep agentName={agentName} setAgentName={setAgentName} />
        )}

        {step === 4 && (
          <SandboxStep
            enabled={sandboxEnabled}
            setEnabled={setSandboxEnabled}
            backend={sandboxBackend}
            setBackend={setSandboxBackend}
            dockerImage={sandboxDockerImage}
            setDockerImage={setSandboxDockerImage}
            e2bTemplate={sandboxE2BTemplate}
            setE2BTemplate={setSandboxE2BTemplate}
            e2bKey={sandboxE2BKey}
            setE2BKey={setSandboxE2BKey}
            boxliteImage={sandboxBoxliteImage}
            setBoxliteImage={setSandboxBoxliteImage}
            boxliteKey={sandboxBoxliteKey}
            setBoxliteKey={setSandboxBoxliteKey}
            boxliteURL={sandboxBoxliteURL}
            setBoxliteURL={setSandboxBoxliteURL}
          />
        )}

        {step === 5 && <DoneStep onContinue={() => router.replace("/")} />}

        {submitError && (
          <Card className="border-destructive/40 bg-destructive/5">
            <CardContent className="pt-6">
              <p className="text-sm text-destructive">{submitError}</p>
            </CardContent>
          </Card>
        )}

        {step !== STEPS.length - 1 && (
          <div className="flex items-center justify-between">
            <Button
              variant="ghost"
              onClick={() => setStep((s) => Math.max(0, s - 1))}
              disabled={step === 0}
            >
              <ArrowLeft className="mr-1 size-4" /> {t("back")}
            </Button>
            {step < STEPS.length - 2 ? (
              <Button
                onClick={() => setStep((s) => s + 1)}
                disabled={!stepValid[step]}
              >
                {t("next")} <ArrowRight className="ml-1 size-4" />
              </Button>
            ) : (
              <Button
                onClick={handleSubmit}
                disabled={!stepValid[step] || submitting}
              >
                {submitting ? (
                  <>
                    <Loader2 className="mr-1 size-4 animate-spin" /> {t("settingUp")}
                  </>
                ) : (
                  <>
                    {t("createAndLaunch")} <Sparkles className="ml-1 size-4" />
                  </>
                )}
              </Button>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

function Stepper({ current }: { current: number }) {
  const t = useTranslations("onboard");
  return (
    <ol className="flex items-center gap-2">
      {STEPS.map((s, i) => {
        const Icon = s.icon;
        const done = i < current;
        const active = i === current;
        return (
          <li key={s.id} className="flex flex-1 items-center gap-2">
            <div
              className={
                "flex size-8 shrink-0 items-center justify-center rounded-full border transition " +
                (done
                  ? "border-primary bg-primary text-primary-foreground"
                  : active
                    ? "border-primary text-primary"
                    : "border-border text-muted-foreground")
              }
            >
              {done ? <Check className="size-4" /> : <Icon className="size-4" />}
            </div>
            <span
              className={
                "hidden text-sm sm:inline " +
                (active
                  ? "font-medium"
                  : done
                    ? "text-muted-foreground"
                    : "text-muted-foreground/60")
              }
            >
              {t(s.labelKey)}
            </span>
            {i < STEPS.length - 1 && (
              <div
                className={
                  "h-px flex-1 " +
                  (i < current ? "bg-primary" : "bg-border")
                }
              />
            )}
          </li>
        );
      })}
    </ol>
  );
}

function WelcomeStep() {
  const t = useTranslations("onboard");
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <PartyPopper className="size-5 text-primary" />
          {t("welcomeTitle")}
        </CardTitle>
        <CardDescription>
          {t("welcomeDescription")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 text-sm text-muted-foreground">
        <p>{t("welcomeAdminHint")}</p>
        <p>
          {t("welcomeConfigHint")}
        </p>
      </CardContent>
    </Card>
  );
}

function AdminStep(props: {
  username: string;
  setUsername: (v: string) => void;
  email: string;
  setEmail: (v: string) => void;
  password: string;
  setPassword: (v: string) => void;
  passwordConfirm: string;
  setPasswordConfirm: (v: string) => void;
  displayName: string;
  setDisplayName: (v: string) => void;
}) {
  const t = useTranslations("onboard");
  const passwordTooShort =
    props.password.length > 0 && props.password.length < 6;
  const mismatch =
    props.passwordConfirm.length > 0 && props.password !== props.passwordConfirm;
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <UserPlus className="size-5 text-primary" />
          {t("adminTitle")}
        </CardTitle>
        <CardDescription>
          {t("adminDescription")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="ob-username">{t("usernameLabel")}</Label>
            <Input
              id="ob-username"
              value={props.username}
              onChange={(e) => props.setUsername(e.target.value)}
              autoComplete="username"
              placeholder={t("usernamePlaceholder")}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="ob-email">{t("emailLabel")}</Label>
            <Input
              id="ob-email"
              type="email"
              value={props.email}
              onChange={(e) => props.setEmail(e.target.value)}
              autoComplete="email"
              placeholder={t("emailPlaceholder")}
            />
          </div>
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="ob-display">{t("displayNameLabel")}</Label>
          <Input
            id="ob-display"
            value={props.displayName}
            onChange={(e) => props.setDisplayName(e.target.value)}
            placeholder={t("displayNamePlaceholder")}
          />
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="ob-password">{t("passwordLabel")}</Label>
            <Input
              id="ob-password"
              type="password"
              value={props.password}
              onChange={(e) => props.setPassword(e.target.value)}
              autoComplete="new-password"
              placeholder={t("passwordPlaceholder")}
            />
            {passwordTooShort && (
              <p className="text-xs text-destructive">{t("passwordTooShort")}</p>
            )}
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="ob-password2">{t("confirmPasswordLabel")}</Label>
            <Input
              id="ob-password2"
              type="password"
              value={props.passwordConfirm}
              onChange={(e) => props.setPasswordConfirm(e.target.value)}
              autoComplete="new-password"
            />
            {mismatch && (
              <p className="text-xs text-destructive">{t("passwordMismatch")}</p>
            )}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

function ProviderStep(props: {
  enabled: boolean;
  setEnabled: (v: boolean) => void;
  providerKey: string;
  onProviderChange: (v: string) => void;
  providerName: string;
  setProviderName: (v: string) => void;
  apiBase: string;
  setApiBase: (v: string) => void;
  apiKey: string;
  setApiKey: (v: string) => void;
  apiType: string;
  setApiType: (v: string) => void;
  authType: string;
  setAuthType: (v: string) => void;
  model: string;
  setModel: (v: string) => void;
  onTest: () => void;
  testStatus: "" | "ok" | "fail" | "running";
  testError: string;
}) {
  const t = useTranslations("onboard");
  const providerLabels = useMemo(() => PROVIDER_LABELS(t), [t]);
  const authTypeLabels = useMemo(() => AUTH_TYPE_LABELS(t), [t]);
  const preset = PROVIDERS[props.providerKey];
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <KeyRound className="size-5 text-primary" />
          {t("providerTitle")}
        </CardTitle>
        <CardDescription>
          {t("providerDescription")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm font-medium">{t("providerToggleLabel")}</p>
            <p className="text-xs text-muted-foreground">
              {t("providerToggleHint")}
            </p>
          </div>
          <Switch checked={props.enabled} onCheckedChange={props.setEnabled} />
        </div>
        {props.enabled && <Separator />}
        {!props.enabled && (
          <p className="text-xs text-muted-foreground">
            {t.rich("providerSkipHint", {
              mono: (chunks) => <span className="font-mono">{chunks}</span>,
            })}
          </p>
        )}
        {props.enabled && (
        <>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label>{t("providerSelectLabel")}</Label>
            <Select
              value={props.providerKey}
              onValueChange={(v) => v && props.onProviderChange(v)}
            >
              <SelectTrigger className="w-full">
                <SelectValue>
                  {(v: unknown) => providerLabels[v as string] ?? (v as string) ?? ""}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="openai">OpenAI</SelectItem>
                <SelectItem value="openrouter">OpenRouter</SelectItem>
                <SelectItem value="anthropic">Anthropic</SelectItem>
                <SelectItem value="deepseek">DeepSeek</SelectItem>
                <SelectItem value="ollama">Ollama</SelectItem>
                <SelectItem value="custom">{t("providerCustom")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label>{t("providerNameLabel")}</Label>
            <Input
              value={props.providerName}
              onChange={(e) => props.setProviderName(e.target.value)}
              placeholder="openai"
              className="font-mono text-sm"
            />
          </div>
        </div>

        <div className="space-y-1.5">
          <Label>{t("defaultModelLabel")}</Label>
          <Input
            value={props.model}
            onChange={(e) => props.setModel(e.target.value)}
            placeholder={preset?.models[0] || t("modelIdPlaceholder")}
            className="font-mono text-sm"
          />
        </div>
        <div className="space-y-1.5">
          <Label>{t("apiBaseLabel")}</Label>
          <Input
            value={props.apiBase}
            onChange={(e) => props.setApiBase(e.target.value)}
            className="font-mono text-sm"
          />
        </div>
        <div className="space-y-1.5">
          <Label>{t("apiKeyLabel")}</Label>
          <Input
            type="password"
            value={props.apiKey}
            onChange={(e) => props.setApiKey(e.target.value)}
            placeholder="sk-…"
            className="font-mono text-sm"
          />
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label>{t("apiTypeLabel")}</Label>
            <Select value={props.apiType} onValueChange={(v) => v && props.setApiType(v)}>
              <SelectTrigger className="w-full">
                <SelectValue>
                  {(v: unknown) => API_TYPE_LABELS[v as string] ?? (v as string) ?? ""}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="openai-chat">OpenAI Chat Completions</SelectItem>
                <SelectItem value="anthropic-messages">Anthropic Messages</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label>{t("authTypeLabel")}</Label>
            <Select value={props.authType} onValueChange={(v) => v && props.setAuthType(v)}>
              <SelectTrigger className="w-full">
                <SelectValue>
                  {(v: unknown) => authTypeLabels[v as string] ?? (v as string) ?? ""}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="bearer-token">{t("authTypeBearer")}</SelectItem>
                <SelectItem value="api-key">{t("authTypeApiKeyHeader")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        <div className="flex items-center gap-3 pt-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={props.onTest}
            disabled={props.testStatus === "running" || !props.apiKey}
          >
            {props.testStatus === "running" ? (
              <>
                <Loader2 className="mr-1 size-4 animate-spin" /> {t("testing")}
              </>
            ) : (
              t("testConnection")
            )}
          </Button>
          {props.testStatus === "ok" && (
            <Badge className="bg-emerald-500/15 text-emerald-700 hover:bg-emerald-500/15">
              <Check className="mr-1 size-3" /> {t("testConnected")}
            </Badge>
          )}
          {props.testStatus === "fail" && (
            <span className="text-xs text-destructive">{props.testError}</span>
          )}
        </div>
        </>
        )}
      </CardContent>
    </Card>
  );
}

function AgentStep(props: {
  agentName: string;
  setAgentName: (v: string) => void;
}) {
  const t = useTranslations("onboard");
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Bot className="size-5 text-primary" />
          {t("agentTitle")}
        </CardTitle>
        <CardDescription>
          {t("agentDescription")}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <div className="space-y-1.5">
          <Label htmlFor="ob-agent">{t("agentNameLabel")}</Label>
          <Input
            id="ob-agent"
            value={props.agentName}
            onChange={(e) => props.setAgentName(e.target.value)}
            placeholder={t("agentNamePlaceholder")}
          />
          <p className="text-xs text-muted-foreground">
            {t.rich("agentIdHint", {
              code: (chunks) => (
                <code className="rounded bg-muted px-1 py-0.5 text-xs">{chunks}</code>
              ),
            })}
          </p>
        </div>
      </CardContent>
    </Card>
  );
}

function SandboxStep(props: {
  enabled: boolean;
  setEnabled: (v: boolean) => void;
  backend: string;
  setBackend: (v: string) => void;
  dockerImage: string;
  setDockerImage: (v: string) => void;
  e2bTemplate: string;
  setE2BTemplate: (v: string) => void;
  e2bKey: string;
  setE2BKey: (v: string) => void;
  boxliteImage: string;
  setBoxliteImage: (v: string) => void;
  boxliteKey: string;
  setBoxliteKey: (v: string) => void;
  boxliteURL: string;
  setBoxliteURL: (v: string) => void;
}) {
  const t = useTranslations("onboard");
  const SANDBOX_BACKEND_LABELS: Record<string, string> = {
    docker: "Docker",
    e2b: t("backendE2b"),
    boxlite: t("backendBoxlite"),
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Container className="size-5 text-primary" />
          {t("sandboxTitle")}
        </CardTitle>
        <CardDescription>
          {t("sandboxDescription")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm font-medium">{t("sandboxToggleLabel")}</p>
            <p className="text-xs text-muted-foreground">
              {t("sandboxToggleHint")}
            </p>
          </div>
          <Switch checked={props.enabled} onCheckedChange={props.setEnabled} />
        </div>
        {props.enabled && (
          <>
            <Separator />
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label>{t("backendLabel")}</Label>
                <Select
                  value={props.backend}
                  onValueChange={(v) => v && props.setBackend(v)}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue>
                      {(v: unknown) =>
                        SANDBOX_BACKEND_LABELS[v as string] ?? (v as string) ?? ""
                      }
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="docker">Docker</SelectItem>
                    <SelectItem value="e2b">{t("backendE2b")}</SelectItem>
                    <SelectItem value="boxlite">{t("backendBoxlite")}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              {props.backend === "e2b" ? (
                <>
                  <div className="space-y-1.5">
                    <Label>{t("e2bKeyLabel")}</Label>
                    <Input
                      type="password"
                      value={props.e2bKey}
                      onChange={(e) => props.setE2BKey(e.target.value)}
                      placeholder="e2b_…"
                      className="font-mono text-sm"
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label>{t("e2bTemplateLabel")}</Label>
                    <Input
                      value={props.e2bTemplate}
                      onChange={(e) => props.setE2BTemplate(e.target.value)}
                      placeholder="base"
                      className="font-mono text-sm"
                    />
                  </div>
                </>
              ) : props.backend === "boxlite" ? (
                <>
                  <div className="space-y-1.5">
                    <Label>{t("boxliteKeyLabel")}</Label>
                    <Input
                      type="password"
                      value={props.boxliteKey}
                      onChange={(e) => props.setBoxliteKey(e.target.value)}
                      placeholder="client_secret"
                      className="font-mono text-sm"
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label>{t("snapshotLabel")}</Label>
                    <Input
                      value={props.boxliteImage}
                      onChange={(e) => props.setBoxliteImage(e.target.value)}
                      placeholder="fastclaw-sandbox"
                      className="font-mono text-sm"
                    />
                    <p className="text-xs text-muted-foreground">
                      {t("boxliteSnapshotHint")}
                    </p>
                  </div>
                  <div className="space-y-1.5 sm:col-span-2">
                    <Label>{t("apiUrlOptionalLabel")}</Label>
                    <Input
                      value={props.boxliteURL}
                      onChange={(e) => props.setBoxliteURL(e.target.value)}
                      placeholder="https://api.dev.boxlite.ai/api/v1"
                      className="font-mono text-sm"
                    />
                  </div>
                </>
              ) : (
                <div className="space-y-1.5">
                  <Label>{t("dockerImageLabel")}</Label>
                  <Input
                    value={props.dockerImage}
                    onChange={(e) => props.setDockerImage(e.target.value)}
                    placeholder="thinkany/fastclaw-sandbox:latest"
                    className="font-mono text-sm"
                  />
                </div>
              )}
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

function DoneStep({ onContinue }: { onContinue: () => void }) {
  const t = useTranslations("onboard");
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <PartyPopper className="size-5 text-emerald-500" />
          {t("doneTitle")}
        </CardTitle>
        <CardDescription>
          {t("doneDescription")}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <p className="text-sm text-muted-foreground">
          {t("doneHint")}
        </p>
      </CardContent>
      <CardFooter>
        <Button onClick={onContinue} className="w-full">
          {t("openDashboard")} <ArrowRight className="ml-1 size-4" />
        </Button>
      </CardFooter>
    </Card>
  );
}
