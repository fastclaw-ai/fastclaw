"use client";

import * as React from "react";

export type Locale = "en" | "zh-CN";

const STORAGE_KEY = "fastclaw-locale";

const en = {
  "common.agent": "Agent",
  "common.user": "User",
  "common.system": "System",
  "common.settings": "Settings",
  "common.lightMode": "Light mode",
  "common.darkMode": "Dark mode",
  "common.logOut": "Log out",
  "common.cancel": "Cancel",
  "composer.message": "Message {{name}}",
  "composer.selectAgent": "Select an agent first",
  "composer.readOnly": "Read-only — viewing another user's chat",
  "composer.slashOnly": "Slash commands only — reply from {{channel}}",
  "composer.moreOptions": "More message options",
  "composer.addAttachment": "Add attachment",
  "composer.send": "Send message",
  "composer.stop": "Stop generating",
  "conversation.today": "Today",
  "conversation.yesterday": "Yesterday",
  "workspace.title": "Workspace",
  "workspace.description": "Files and previews from this conversation",
  "workspace.open": "Open workspace",
  "workspace.unavailable": "Workspace is available after the conversation starts",
  "workspace.back": "Back to projects and recent conversations",
  "sidebar.searchBots": "Search Agents",
  "sidebar.createBot": "Create Agent",
  "sidebar.expandContacts": "Expand contacts",
  "sidebar.collapseContacts": "Collapse contacts",
  "sidebar.noMatches": "No matching Agents",
  "sidebar.noBots": "No Agents yet",
  "sidebar.manageBots": "Manage Agents",
  "sidebar.loadMore": "Load more",
  "sidebar.untitledBot": "Untitled Agent",
  "sidebar.greeting": "Hey, I'm {{name}}. What should we start with?",
  "sidebar.quickApi": "API",
  "sidebar.moreSettings": "More settings",
  "createBot.title": "Create Agent",
  "createBot.description": "Give the Agent a name, purpose, and optional avatar. You can configure its model and skills afterward.",
  "createBot.uploadAvatar": "Upload Agent avatar",
  "createBot.avatarAlt": "Agent avatar",
  "createBot.name": "Name",
  "createBot.namePlaceholder": "Research assistant",
  "createBot.descriptionLabel": "Description (optional)",
  "createBot.descriptionPlaceholder": "What should this Agent help with?",
  "createBot.creating": "Creating…",
  "createBot.failed": "Failed to create Agent",
  "createBot.missingId": "Agent was created but no id was returned",
  "settings.tab.profile": "Profile",
  "settings.tab.customize": "Customize",
  "settings.tab.models": "Models",
  "settings.tab.context": "Context",
  "settings.tab.knowledge": "Knowledge",
  "settings.tab.skills": "Skills",
  "settings.tab.mcp": "MCP",
  "settings.tab.plugins": "Plugins",
  "settings.tab.channels": "Channels",
  "settings.tab.scheduler": "Scheduler",
  "settings.tab.usage": "Token Usage",
  "settings.tab.account": "Account",
  "settings.tab.general": "General",
  "settings.tab.apiKeys": "API Keys",
  "settings.tab.users": "Users",
  "settings.tab.chats": "Chats",
  "settings.tab.tools": "Tools",
  "settings.tab.about": "About",
  "settings.general.title": "General",
  "settings.general.description": "Appearance, language, and per-device preferences.",
  "settings.theme.title": "Theme",
  "settings.theme.description": "Choose the dashboard color scheme. System follows your OS.",
  "settings.theme.light": "Light",
  "settings.theme.dark": "Dark",
  "settings.theme.system": "System",
  "settings.language.title": "Language",
  "settings.language.description": "Choose the language used by FastClaw on this device.",
  "settings.language.english": "English",
  "settings.language.chinese": "简体中文",
} as const;

export type MessageKey = keyof typeof en;

const zhCN: Record<MessageKey, string> = {
  "common.agent": "Agent",
  "common.user": "用户",
  "common.system": "系统",
  "common.settings": "设置",
  "common.lightMode": "浅色模式",
  "common.darkMode": "深色模式",
  "common.logOut": "退出登录",
  "common.cancel": "取消",
  "composer.message": "给 {{name}} 发消息",
  "composer.selectAgent": "请先选择一个 Agent",
  "composer.readOnly": "只读模式 — 正在查看其他用户的对话",
  "composer.slashOnly": "仅支持斜杠命令 — 请在 {{channel}} 中回复",
  "composer.moreOptions": "更多消息选项",
  "composer.addAttachment": "添加附件",
  "composer.send": "发送消息",
  "composer.stop": "停止生成",
  "conversation.today": "今天",
  "conversation.yesterday": "昨天",
  "workspace.title": "工作区",
  "workspace.description": "查看当前话题的文件与预览",
  "workspace.open": "打开工作区",
  "workspace.unavailable": "开始对话后即可使用工作区",
  "workspace.back": "返回项目和最近话题",
  "sidebar.searchBots": "搜索 Agent",
  "sidebar.createBot": "新建 Agent",
  "sidebar.expandContacts": "展开联系人",
  "sidebar.collapseContacts": "折叠联系人",
  "sidebar.noMatches": "没有匹配的 Agent",
  "sidebar.noBots": "还没有 Agent",
  "sidebar.manageBots": "管理 Agent",
  "sidebar.loadMore": "加载更多",
  "sidebar.untitledBot": "未命名 Agent",
  "sidebar.greeting": "Hey，我是{{name}}。想先从哪件事开始？",
  "sidebar.quickApi": "API",
  "sidebar.moreSettings": "更多设置",
  "createBot.title": "新建 Agent",
  "createBot.description": "设置 Agent 的名称、用途和可选头像。创建后还可以继续配置模型和技能。",
  "createBot.uploadAvatar": "上传 Agent 头像",
  "createBot.avatarAlt": "Agent 头像",
  "createBot.name": "名称",
  "createBot.namePlaceholder": "研究助理",
  "createBot.descriptionLabel": "描述（可选）",
  "createBot.descriptionPlaceholder": "这个 Agent 应该帮你做什么？",
  "createBot.creating": "正在创建…",
  "createBot.failed": "创建 Agent 失败",
  "createBot.missingId": "Agent 已创建，但接口没有返回 ID",
  "settings.tab.profile": "资料",
  "settings.tab.customize": "自定义",
  "settings.tab.models": "模型",
  "settings.tab.context": "上下文",
  "settings.tab.knowledge": "知识库",
  "settings.tab.skills": "技能",
  "settings.tab.mcp": "MCP",
  "settings.tab.plugins": "插件",
  "settings.tab.channels": "渠道",
  "settings.tab.scheduler": "定时任务",
  "settings.tab.usage": "Token 用量",
  "settings.tab.account": "账户",
  "settings.tab.general": "通用",
  "settings.tab.apiKeys": "API 密钥",
  "settings.tab.users": "用户",
  "settings.tab.chats": "聊天记录",
  "settings.tab.tools": "工具",
  "settings.tab.about": "关于",
  "settings.general.title": "通用",
  "settings.general.description": "外观、语言及当前设备的偏好设置。",
  "settings.theme.title": "主题",
  "settings.theme.description": "选择界面配色；系统模式会跟随操作系统。",
  "settings.theme.light": "浅色",
  "settings.theme.dark": "深色",
  "settings.theme.system": "跟随系统",
  "settings.language.title": "语言",
  "settings.language.description": "选择 FastClaw 在此设备上使用的语言。",
  "settings.language.english": "English",
  "settings.language.chinese": "简体中文",
};

const dictionaries: Record<Locale, Record<MessageKey, string>> = {
  en,
  "zh-CN": zhCN,
};

function normalizeLocale(value?: string | null): Locale {
  return value?.toLowerCase().startsWith("zh") ? "zh-CN" : "en";
}

type LocaleContextValue = {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: (key: MessageKey, values?: Record<string, string | number>) => string;
  tr: (
    english: string,
    chinese: string,
    values?: Record<string, string | number>,
  ) => string;
};

function interpolate(
  template: string,
  values?: Record<string, string | number>,
): string {
  let result = template;
  for (const [name, value] of Object.entries(values || {})) {
    result = result.replaceAll(`{{${name}}}`, String(value));
  }
  return result;
}

const LocaleContext = React.createContext<LocaleContextValue>({
  locale: "en",
  setLocale: () => {},
  t: (key, values) => interpolate(en[key], values),
  tr: (english, _chinese, values) => interpolate(english, values),
});

export function useLocale() {
  return React.useContext(LocaleContext);
}

function applyLocale(locale: Locale) {
  document.documentElement.lang = locale;
}

export function LocaleProvider({ children }: { children: React.ReactNode }) {
  const [locale, setLocaleState] = React.useState<Locale>("en");

  React.useEffect(() => {
    const stored = window.localStorage.getItem(STORAGE_KEY);
    const detected = stored ? normalizeLocale(stored) : normalizeLocale(window.navigator.language);
    // Client-only locale detection intentionally happens after hydration.
    setLocaleState(detected);
    applyLocale(detected);
  }, []);

  const setLocale = React.useCallback((next: Locale) => {
    setLocaleState(next);
    window.localStorage.setItem(STORAGE_KEY, next);
    applyLocale(next);
  }, []);

  const t = React.useCallback(
    (key: MessageKey, values?: Record<string, string | number>) => {
      return interpolate(dictionaries[locale][key] || en[key], values);
    },
    [locale],
  );

  // tr keeps one-off and highly local UI copy next to the component that
  // owns it, while t remains the shared catalog for repeated navigation and
  // product vocabulary. Both paths use the same locale and interpolation.
  const tr = React.useCallback(
    (
      english: string,
      chinese: string,
      values?: Record<string, string | number>,
    ) => interpolate(locale === "zh-CN" ? chinese : english, values),
    [locale],
  );

  const value = React.useMemo(
    () => ({ locale, setLocale, t, tr }),
    [locale, setLocale, t, tr],
  );

  return <LocaleContext.Provider value={value}>{children}</LocaleContext.Provider>;
}
