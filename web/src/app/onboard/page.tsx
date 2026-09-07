"use client";

import { useState, useCallback, useEffect } from "react";
import { useRouter } from "next/navigation";
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
import { useLocale } from "@/components/locale-provider";

const STEPS = [
  { id: "welcome", label: "Welcome", icon: PartyPopper },
  { id: "admin", label: "Admin", icon: UserPlus },
  { id: "provider", label: "Provider", icon: KeyRound },
  { id: "agent", label: "Agent", icon: Bot },
  { id: "sandbox", label: "Sandbox", icon: Container },
  { id: "launch", label: "Launch", icon: Sparkles },
] as const;

// Display label maps. base-ui's <Select.Value /> renders the raw `value`
// (the SelectItem's `value` prop) by default, not the SelectItem's
// children — so we explicitly map keys to titles via the children render
// prop on SelectValue. Keep these in sync with the SelectItem lists.
const PROVIDER_LABELS: Record<string, string> = {
  openai: "OpenAI",
  openrouter: "OpenRouter",
  anthropic: "Anthropic",
  deepseek: "DeepSeek",
  ollama: "Ollama",
  custom: "Custom",
};

const API_TYPE_LABELS: Record<string, string> = {
  "openai-chat": "OpenAI Chat Completions",
  "anthropic-messages": "Anthropic Messages",
};

const AUTH_TYPE_LABELS: Record<string, string> = {
  "bearer-token": "Bearer Token",
  "api-key": "API Key Header",
};

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
  const { tr } = useLocale();
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
      setTestError(tr("API key required", "请输入 API 密钥"));
      return;
    }
    setTestStatus("running");
    setTestError("");
    const res = await testProvider({ apiBase, apiKey, model, apiType, authType });
    if (res.ok) {
      setTestStatus("ok");
    } else {
      setTestStatus("fail");
      setTestError(res.error || tr("Connection test failed", "连接测试失败"));
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
      setSubmitError(res.error || tr("Setup failed", "初始化失败"));
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
              <ArrowLeft className="mr-1 size-4" /> {tr("Back", "上一步")}
            </Button>
            {step < STEPS.length - 2 ? (
              <Button
                onClick={() => setStep((s) => s + 1)}
                disabled={!stepValid[step]}
              >
                {tr("Next", "下一步")} <ArrowRight className="ml-1 size-4" />
              </Button>
            ) : (
              <Button
                onClick={handleSubmit}
                disabled={!stepValid[step] || submitting}
              >
                {submitting ? (
                  <>
                    <Loader2 className="mr-1 size-4 animate-spin" /> {tr("Setting up", "正在初始化")}
                  </>
                ) : (
                  <>
                    {tr("Create & launch", "创建并启动")} <Sparkles className="ml-1 size-4" />
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
  const { tr } = useLocale();
  const labels: Record<(typeof STEPS)[number]["id"], string> = {
    welcome: tr("Welcome", "欢迎"),
    admin: tr("Admin", "管理员"),
    provider: tr("Provider", "提供商"),
    agent: "Agent",
    sandbox: tr("Sandbox", "沙盒"),
    launch: tr("Launch", "启动"),
  };
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
              {labels[s.id]}
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
  const { tr } = useLocale();
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <PartyPopper className="size-5 text-primary" />
          {tr("Welcome to FastClaw", "欢迎使用 FastClaw")}
        </CardTitle>
        <CardDescription>
          {tr("A few quick steps will set up your platform: an admin account, your first LLM provider, and your first agent. It takes about a minute.", "只需几个步骤即可完成平台初始化：创建管理员账户、配置首个模型提供商和第一个 Agent，约需一分钟。")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 text-sm text-muted-foreground">
        <p>{tr("You will become the super administrator when setup is complete. You can add more users from the admin panel later.", "初始化完成后，你将成为超级管理员，之后可从管理后台添加更多用户。")}</p>
        <p>
          {tr("All user-facing configuration, including providers, channels, agents, and settings, is stored in the database and can be changed in the UI later.", "提供商、渠道、Agent 和设置等用户配置都会存储在数据库中，之后可随时通过界面修改。")}
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
  const { tr } = useLocale();
  const passwordTooShort =
    props.password.length > 0 && props.password.length < 6;
  const mismatch =
    props.passwordConfirm.length > 0 && props.password !== props.passwordConfirm;
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <UserPlus className="size-5 text-primary" />
          {tr("Create super-admin account", "创建超级管理员账户")}
        </CardTitle>
        <CardDescription>
          {tr("You can sign in with either your username or email afterward.", "创建后可使用用户名或邮箱登录。")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="ob-username">{tr("Username", "用户名")}</Label>
            <Input
              id="ob-username"
              value={props.username}
              onChange={(e) => props.setUsername(e.target.value)}
              autoComplete="username"
              placeholder="alice"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="ob-email">{tr("Email", "邮箱")}</Label>
            <Input
              id="ob-email"
              type="email"
              value={props.email}
              onChange={(e) => props.setEmail(e.target.value)}
              autoComplete="email"
              placeholder="alice@example.com"
            />
          </div>
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="ob-display">{tr("Display Name (optional)", "显示名称（可选）")}</Label>
          <Input
            id="ob-display"
            value={props.displayName}
            onChange={(e) => props.setDisplayName(e.target.value)}
            placeholder="Alice"
          />
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="ob-password">{tr("Password", "密码")}</Label>
            <Input
              id="ob-password"
              type="password"
              value={props.password}
              onChange={(e) => props.setPassword(e.target.value)}
              autoComplete="new-password"
              placeholder={tr("6+ characters", "至少 6 个字符")}
            />
            {passwordTooShort && (
              <p className="text-xs text-destructive">{tr("at least 6 characters", "至少需要 6 个字符")}</p>
            )}
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="ob-password2">{tr("Confirm Password", "确认密码")}</Label>
            <Input
              id="ob-password2"
              type="password"
              value={props.passwordConfirm}
              onChange={(e) => props.setPasswordConfirm(e.target.value)}
              autoComplete="new-password"
            />
            {mismatch && (
              <p className="text-xs text-destructive">{tr("passwords do not match", "两次输入的密码不一致")}</p>
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
  const { tr } = useLocale();
  const preset = PROVIDERS[props.providerKey];
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <KeyRound className="size-5 text-primary" />
          {tr("First LLM provider", "配置首个模型提供商")}
        </CardTitle>
        <CardDescription>
          {tr("Connect at least one model. You can add more providers and per-user or per-agent overrides later, or skip this step and configure everything afterward.", "连接至少一个模型。之后可继续添加提供商，或设置用户级、Agent 级覆盖；也可以跳过此步骤，稍后再统一配置。")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm font-medium">{tr("Configure a provider now", "现在配置提供商")}</p>
            <p className="text-xs text-muted-foreground">
              {tr("Turn this off to skip. You can add providers later.", "关闭即可跳过，之后仍可添加提供商。")}
            </p>
          </div>
          <Switch checked={props.enabled} onCheckedChange={props.setEnabled} />
        </div>
        {props.enabled && <Separator />}
        {!props.enabled && (
          <p className="text-xs text-muted-foreground">
            {tr("Skipping this step creates the admin account and agent without a default model. Add one from Models after launch.", "跳过后，系统会创建管理员账户和 Agent，但不配置默认模型。启动后可在「模型」中添加。")}
          </p>
        )}
        {props.enabled && (
        <>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label>{tr("Provider", "提供商")}</Label>
            <Select
              value={props.providerKey}
              onValueChange={(v) => v && props.onProviderChange(v)}
            >
              <SelectTrigger className="w-full">
                <SelectValue>
                  {(v: unknown) => PROVIDER_LABELS[v as string] ?? (v as string) ?? ""}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="openai">OpenAI</SelectItem>
                <SelectItem value="openrouter">OpenRouter</SelectItem>
                <SelectItem value="anthropic">Anthropic</SelectItem>
                <SelectItem value="deepseek">DeepSeek</SelectItem>
                <SelectItem value="ollama">Ollama</SelectItem>
                <SelectItem value="custom">{tr("Custom", "自定义")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label>{tr("Provider Name", "提供商名称")}</Label>
            <Input
              value={props.providerName}
              onChange={(e) => props.setProviderName(e.target.value)}
              placeholder="openai"
              className="font-mono text-sm"
            />
          </div>
        </div>

        <div className="space-y-1.5">
          <Label>{tr("Default Model", "默认模型")}</Label>
          <Input
            value={props.model}
            onChange={(e) => props.setModel(e.target.value)}
            placeholder={preset?.models[0] || "model-id"}
            className="font-mono text-sm"
          />
        </div>
        <div className="space-y-1.5">
          <Label>{tr("API Base URL", "API 基础 URL")}</Label>
          <Input
            value={props.apiBase}
            onChange={(e) => props.setApiBase(e.target.value)}
            className="font-mono text-sm"
          />
        </div>
        <div className="space-y-1.5">
          <Label>{tr("API Key", "API 密钥")}</Label>
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
            <Label>{tr("API Type", "API 类型")}</Label>
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
            <Label>{tr("Auth Type", "认证类型")}</Label>
            <Select value={props.authType} onValueChange={(v) => v && props.setAuthType(v)}>
              <SelectTrigger className="w-full">
                <SelectValue>
                  {(v: unknown) => AUTH_TYPE_LABELS[v as string] ?? (v as string) ?? ""}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="bearer-token">Bearer Token</SelectItem>
                <SelectItem value="api-key">API Key Header</SelectItem>
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
                <Loader2 className="mr-1 size-4 animate-spin" /> {tr("Testing", "正在测试")}
              </>
            ) : (
              tr("Test connection", "测试连接")
            )}
          </Button>
          {props.testStatus === "ok" && (
            <Badge className="bg-emerald-500/15 text-emerald-700 hover:bg-emerald-500/15">
              <Check className="mr-1 size-3" /> {tr("connected", "已连接")}
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
  const { tr } = useLocale();
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Bot className="size-5 text-primary" />
          {tr("First agent", "创建第一个 Agent")}
        </CardTitle>
        <CardDescription>
          {tr("Give it a name for now. You can edit its personality, skills, and tools after launch.", "现在只需设置名称；启动后可继续编辑个性、技能和工具。")}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <div className="space-y-1.5">
          <Label htmlFor="ob-agent">{tr("Agent Name", "Agent 名称")}</Label>
          <Input
            id="ob-agent"
            value={props.agentName}
            onChange={(e) => props.setAgentName(e.target.value)}
            placeholder="default"
          />
          <p className="text-xs text-muted-foreground">
            {tr("The agent receives a globally unique ID (for example", "Agent 会获得全局唯一 ID（例如")} {" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">agt_a1b2c3…</code>
            {tr("); this name is only for display.", "），此名称仅用于展示。")}
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
  const { tr } = useLocale();
  const SANDBOX_BACKEND_LABELS: Record<string, string> = {
    docker: "Docker",
    e2b: "E2B (cloud)",
    boxlite: "BoxLite (cloud)",
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Container className="size-5 text-primary" />
          {tr("Sandbox (optional)", "沙盒（可选）")}
        </CardTitle>
        <CardDescription>
          {tr("Run code executed by agents in an isolated environment. If you are unsure, skip this step and enable it later in Settings.", "在隔离环境中运行 Agent 执行的代码。如果不确定，可跳过此步骤，之后在设置中启用。")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm font-medium">{tr("Enable sandbox", "启用沙盒")}</p>
            <p className="text-xs text-muted-foreground">
              {tr("Off by default. Code runs in the agent's own workspace.", "默认关闭；代码会在 Agent 自己的工作区中运行。")}
            </p>
          </div>
          <Switch checked={props.enabled} onCheckedChange={props.setEnabled} />
        </div>
        {props.enabled && (
          <>
            <Separator />
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label>{tr("Backend", "后端")}</Label>
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
                    <SelectItem value="e2b">E2B ({tr("cloud", "云端")})</SelectItem>
                    <SelectItem value="boxlite">BoxLite ({tr("cloud", "云端")})</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              {props.backend === "e2b" ? (
                <>
                  <div className="space-y-1.5">
                    <Label>E2B API Key</Label>
                    <Input
                      type="password"
                      value={props.e2bKey}
                      onChange={(e) => props.setE2BKey(e.target.value)}
                      placeholder="e2b_…"
                      className="font-mono text-sm"
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label>E2B Template</Label>
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
                    <Label>BoxLite API Key</Label>
                    <Input
                      type="password"
                      value={props.boxliteKey}
                      onChange={(e) => props.setBoxliteKey(e.target.value)}
                      placeholder="client_secret"
                      className="font-mono text-sm"
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label>{tr("Snapshot", "快照")}</Label>
                    <Input
                      value={props.boxliteImage}
                      onChange={(e) => props.setBoxliteImage(e.target.value)}
                      placeholder="fastclaw-sandbox"
                      className="font-mono text-sm"
                    />
                    <p className="text-xs text-muted-foreground">
                      {tr("BoxLite snapshot name imported through the BoxLite Dashboard, not a Docker Hub image reference.", "填写通过 BoxLite Dashboard 导入的快照名称，而不是 Docker Hub 镜像地址。")}
                    </p>
                  </div>
                  <div className="space-y-1.5 sm:col-span-2">
                    <Label>{tr("API URL (optional)", "API URL（可选）")}</Label>
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
                  <Label>{tr("Docker Image", "Docker 镜像")}</Label>
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
  const { tr } = useLocale();
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <PartyPopper className="size-5 text-emerald-500" />
          {tr("You are ready!", "准备就绪！")}
        </CardTitle>
        <CardDescription>
          {tr("The admin account and first agent are ready, and the selected provider has been configured.", "管理员账户和第一个 Agent 已准备就绪，所选提供商也已完成配置。")}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <p className="text-sm text-muted-foreground">
          {tr("Your session is ready. Continue to open the dashboard.", "登录会话已创建，点击继续即可进入控制台。")}
        </p>
      </CardContent>
      <CardFooter>
        <Button onClick={onContinue} className="w-full">
          {tr("Open dashboard", "进入控制台")} <ArrowRight className="ml-1 size-4" />
        </Button>
      </CardFooter>
    </Card>
  );
}
