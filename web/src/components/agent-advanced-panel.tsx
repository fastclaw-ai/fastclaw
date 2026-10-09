"use client";

import * as React from "react";
import { Download, Loader2, Upload } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  exportAgentArchive,
  importAgentArchive,
  type AgentArchivePreview,
} from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useLocale } from "@/components/locale-provider";

const MAX_ARCHIVE_BYTES = 64 * 1024 * 1024;

// AgentAdvancedPanel is the "Advanced" tab: export the agent's
// configuration (identity files + its own skills) as a ZIP, or import one
// to replace them. Import is two-step — the server validates the archive
// in a dry run, the owner reviews what will change, then confirms.
export default function AgentAdvancedPanel() {
  const { tr } = useLocale();
  const agentId = useAgentIdFromURL();
  const inputRef = React.useRef<HTMLInputElement>(null);
  const [busy, setBusy] = React.useState<"" | "export" | "parse" | "import">("");
  const [file, setFile] = React.useState<File | null>(null);
  const [preview, setPreview] = React.useState<AgentArchivePreview | null>(null);
  const [error, setError] = React.useState("");
  const [status, setStatus] = React.useState("");

  const run = async (kind: "export" | "parse" | "import", fn: () => Promise<void>) => {
    if (busy) return;
    setBusy(kind);
    setError("");
    setStatus("");
    try {
      await fn();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  };

  const onExport = () =>
    run("export", async () => {
      const { blob, filename } = await exportAgentArchive(agentId);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(url);
      setStatus(tr("Configuration exported.", "已导出配置。"));
    });

  const onSelect = (picked?: File) => {
    if (!picked) return;
    setPreview(null);
    setFile(null);
    void run("parse", async () => {
      if (!/\.(zip|tar\.gz|tgz)$/i.test(picked.name) || picked.size > MAX_ARCHIVE_BYTES) {
        throw new Error(
          tr("Choose a ZIP or TAR.GZ up to 64 MB.", "请选择不超过 64 MB 的 ZIP 或 TAR.GZ 文件。"),
        );
      }
      setPreview(await importAgentArchive(agentId, picked, true));
      setFile(picked);
    });
  };

  const onConfirm = () =>
    run("import", async () => {
      if (!file) return;
      await importAgentArchive(agentId, file, false);
      setPreview(null);
      setFile(null);
      setStatus(
        tr(
          "Configuration imported. It applies from the next message.",
          "已导入配置，从下一条消息开始生效。",
        ),
      );
    });

  const cancel = () => {
    setPreview(null);
    setFile(null);
  };

  const none = tr("None", "无");

  return (
    <div className="p-6 max-w-3xl space-y-6">
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">{tr("Advanced", "高级")}</h2>
        <p className="text-sm text-muted-foreground mt-1">
          {tr(
            "Back up, move or share this agent's configuration.",
            "备份、迁移或分享这个 Agent 的配置。",
          )}
        </p>
      </div>

      <section className="flex flex-col gap-4 rounded-lg border border-border bg-card p-5 sm:flex-row sm:items-center sm:justify-between">
        <div className="space-y-1">
          <h3 className="text-sm font-medium">{tr("Export configuration", "导出配置")}</h3>
          <p className="text-xs text-muted-foreground">
            {tr(
              "Download the identity files (SOUL.md, AGENTS.md, …), prompt mode and this agent's skills as a ZIP. Excludes model keys, MCP servers, channels, chat history and personal memory.",
              "将身份文件（SOUL.md、AGENTS.md 等）、提示词模式和该 Agent 的技能导出为 ZIP。不含模型密钥、MCP 服务、渠道、聊天记录和个人记忆。",
            )}
          </p>
        </div>
        <Button variant="outline" className="shrink-0" onClick={onExport} disabled={!!busy}>
          {busy === "export" ? (
            <Loader2 className="h-4 w-4 mr-2 animate-spin" />
          ) : (
            <Download className="h-4 w-4 mr-2" />
          )}
          {tr("Export ZIP", "导出 ZIP")}
        </Button>
      </section>

      <section className="space-y-4 rounded-lg border border-border bg-card p-5">
        <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
          <div className="space-y-1">
            <h3 className="text-sm font-medium">{tr("Import configuration", "导入配置")}</h3>
            <p className="text-xs text-muted-foreground">
              {tr(
                "Supports ZIP, TAR.GZ and TGZ — a FastClaw export or a workspace folder with SOUL.md / AGENTS.md and skills/. Replaces identity files and skills; name, avatar, model, channels and memory stay unchanged.",
                "支持 ZIP、TAR.GZ 和 TGZ：FastClaw 导出的配置包，或包含 SOUL.md / AGENTS.md 和 skills/ 的工作区目录。会覆盖身份文件和技能；名称、头像、模型、渠道和记忆保持不变。",
              )}
            </p>
          </div>
          <Button
            variant="outline"
            className="shrink-0"
            onClick={() => inputRef.current?.click()}
            disabled={!!busy}
          >
            {busy === "parse" ? (
              <Loader2 className="h-4 w-4 mr-2 animate-spin" />
            ) : (
              <Upload className="h-4 w-4 mr-2" />
            )}
            {tr("Import archive", "导入配置包")}
          </Button>
          <input
            ref={inputRef}
            hidden
            type="file"
            accept=".zip,.tar.gz,.tgz,application/zip,application/gzip"
            onChange={(e) => {
              onSelect(e.target.files?.[0]);
              e.target.value = "";
            }}
          />
        </div>

        {preview && (
          <div
            className="space-y-2 rounded-md border border-border bg-muted/40 p-4 text-sm"
            role="region"
            aria-label={tr("Import preview", "导入预览")}
          >
            <h4 className="font-medium">
              {tr("Import preview", "导入预览")}
              {preview.name ? ` · ${preview.name}` : ""}
            </h4>
            <p>
              <span className="text-muted-foreground">{tr("Identity files", "身份文件")}：</span>
              {preview.files.join("、") || none}
            </p>
            <p>
              <span className="text-muted-foreground">{tr("Skills", "技能")}：</span>
              {preview.skills.join("、") || none}
            </p>
            {preview.ignored.length > 0 && (
              <p className="text-xs text-muted-foreground">
                {tr(
                  "{{n}} other files will be ignored (memory, sessions, config and anything outside identity files and skills).",
                  "另有 {{n}} 个文件不会导入（记忆、会话、配置等身份文件和技能以外的内容）。",
                  { n: preview.ignored.length },
                )}
              </p>
            )}
            <p className="text-xs text-amber-700 dark:text-amber-400">
              {tr(
                "This replaces all current identity files and skills of this agent. Anything not in the archive will be removed.",
                "确认后将替换该 Agent 当前全部身份文件和技能，配置包中没有的项目会被清除。",
              )}
            </p>
            <div className="flex justify-end gap-2 pt-1">
              <Button variant="ghost" size="sm" onClick={cancel} disabled={!!busy}>
                {tr("Cancel", "取消")}
              </Button>
              <Button size="sm" onClick={onConfirm} disabled={!!busy}>
                {busy === "import" && <Loader2 className="h-4 w-4 mr-2 animate-spin" />}
                {tr("Confirm replacement", "确认覆盖")}
              </Button>
            </div>
          </div>
        )}
      </section>

      {error && (
        <div
          className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive"
          role="alert"
        >
          {error}
        </div>
      )}
      {status && (
        <p className="text-sm text-emerald-700 dark:text-emerald-400" role="status">
          {status}
        </p>
      )}
    </div>
  );
}
