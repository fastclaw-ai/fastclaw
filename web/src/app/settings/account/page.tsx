"use client";

import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Save, Check, Upload, X } from "lucide-react";
import { getMe, updateMe, changeMyPassword } from "@/lib/api";
import { logout as doLogout } from "@/lib/auth";
import { useLocale } from "@/components/locale-provider";

const AVATAR_MAX_BYTES = 256 * 1024;

export default function AccountSettingsPage() {
  const { tr } = useLocale();
  const [loading, setLoading] = useState(true);
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [avatarUrl, setAvatarUrl] = useState("");

  const [profileSaving, setProfileSaving] = useState(false);
  const [profileSaved, setProfileSaved] = useState(false);
  const [profileError, setProfileError] = useState("");

  const [oldPassword, setOldPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [pwSaving, setPwSaving] = useState(false);
  const [pwSaved, setPwSaved] = useState(false);
  const [pwError, setPwError] = useState("");

  const fileRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    getMe()
      .then((m) => {
        if (m?.user) {
          setUsername(m.user.username || "");
          setEmail(m.user.email || "");
          setDisplayName(m.user.displayName || "");
          setAvatarUrl(m.user.avatarUrl || "");
        }
      })
      .finally(() => setLoading(false));
  }, []);

  function pickAvatar() {
    fileRef.current?.click();
  }

  function onAvatarFile(e: React.ChangeEvent<HTMLInputElement>) {
    setProfileError("");
    const file = e.target.files?.[0];
    if (!file) return;
    if (!file.type.startsWith("image/")) {
      setProfileError(tr("Avatar must be an image", "头像必须是图片文件"));
      return;
    }
    // Rough pre-check on raw bytes; the encoded data URL will be ~33%
    // larger, so reject anything that won't fit comfortably.
    if (file.size > Math.floor(AVATAR_MAX_BYTES * 0.7)) {
      setProfileError(tr("Image too large (max ~180KB before encoding)", "图片过大（编码前最大约 180KB）"));
      return;
    }
    const reader = new FileReader();
    reader.onload = () => {
      const result = String(reader.result || "");
      if (result.length > AVATAR_MAX_BYTES) {
        setProfileError(tr("Encoded image exceeds 256KB", "编码后的图片超过 256KB"));
        return;
      }
      setAvatarUrl(result);
    };
    reader.readAsDataURL(file);
    // Reset input so re-selecting the same file fires onchange again.
    e.target.value = "";
  }

  async function saveProfile() {
    setProfileSaving(true);
    setProfileError("");
    const res = await updateMe({ displayName, avatarUrl });
    setProfileSaving(false);
    if (res?.error) {
      setProfileError(res.error);
      return;
    }
    window.dispatchEvent(new CustomEvent("fastclaw:user-profile-changed"));
    setProfileSaved(true);
    setTimeout(() => setProfileSaved(false), 2000);
  }

  async function savePassword(e: React.FormEvent) {
    e.preventDefault();
    setPwError("");
    if (!oldPassword || !newPassword) {
      setPwError(tr("Both password fields are required", "当前密码和新密码均为必填项"));
      return;
    }
    if (newPassword !== confirmPassword) {
      setPwError(tr("New password and confirmation do not match", "两次输入的新密码不一致"));
      return;
    }
    setPwSaving(true);
    const res = await changeMyPassword({ oldPassword, newPassword });
    setPwSaving(false);
    if (res?.error) {
      setPwError(res.error);
      return;
    }
    setPwSaved(true);
    // Force re-login on the new password — also kicks any stale sessions
    // off this device. Brief delay so the user sees the success state.
    setTimeout(() => {
      doLogout();
      window.location.href = "/";
    }, 800);
  }

  if (loading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-10 w-48" />
        <Skeleton className="h-64 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  const initials = (displayName || username || "?").slice(0, 2).toUpperCase();

  return (
    <div className="space-y-6">
      <div>
        <h3 className="text-xl font-semibold tracking-tight">
          {tr("Account", "账户")}
        </h3>
        <p className="text-sm text-muted-foreground mt-1">
          {tr("Profile, password, and session.", "管理个人资料、密码和登录会话。")}
        </p>
      </div>

      {/* Profile */}
      <div className="rounded-lg border border-border bg-card p-5 space-y-4">
        <div className="flex items-center gap-4">
          <div className="relative size-16 group">
            <div className="size-16 rounded-full bg-muted overflow-hidden flex items-center justify-center text-lg font-bold text-muted-foreground">
              {avatarUrl ? (
                // eslint-disable-next-line @next/next/no-img-element
                <img src={avatarUrl} alt={tr("Avatar", "头像")} className="size-full object-cover" />
              ) : (
                initials
              )}
            </div>
            {avatarUrl && (
              <button
                type="button"
                onClick={() => setAvatarUrl("")}
                aria-label={tr("Remove avatar", "移除头像")}
                title={tr("Remove avatar", "移除头像")}
                className="absolute -top-1 -right-1 hidden group-hover:flex items-center justify-center size-5 rounded-full bg-background border border-border text-muted-foreground hover:text-destructive hover:border-destructive transition shadow-sm"
              >
                <X className="size-3" />
              </button>
            )}
          </div>
          <Button variant="outline" size="sm" onClick={pickAvatar}>
            <Upload className="size-4 mr-2" />
            {tr("Upload", "上传")}
          </Button>
          <input
            ref={fileRef}
            type="file"
            accept="image/*"
            onChange={onAvatarFile}
            className="hidden"
          />
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div className="space-y-1.5">
            <Label>{tr("Username", "用户名")}</Label>
            <Input value={username} disabled />
          </div>
          <div className="space-y-1.5">
            <Label>{tr("Email", "邮箱")}</Label>
            <Input value={email} disabled />
          </div>
          <div className="space-y-1.5 sm:col-span-2">
            <Label htmlFor="display-name">{tr("Display name", "显示名称")}</Label>
            <Input
              id="display-name"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder={tr("How your name appears in the dashboard", "此名称将显示在控制台中")}
            />
          </div>
        </div>

        {profileError && (
          <p className="text-sm text-destructive">{profileError}</p>
        )}
        <div className="flex justify-end">
          <Button
            onClick={saveProfile}
            disabled={profileSaving}
            variant={profileSaved ? "outline" : "default"}
            className={
              profileSaved
                ? "border-emerald-500/30 text-emerald-600 dark:text-emerald-400"
                : ""
            }
          >
            {profileSaved ? (
              <>
                <Check className="h-4 w-4 mr-2" />
                {tr("Saved", "已保存")}
              </>
            ) : (
              <>
                <Save className="h-4 w-4 mr-2" />
                {profileSaving ? tr("Saving…", "正在保存…") : tr("Save profile", "保存资料")}
              </>
            )}
          </Button>
        </div>
      </div>

      {/* Password */}
      <form onSubmit={savePassword} className="rounded-lg border border-border bg-card p-5 space-y-4">
        <div>
          <h4 className="font-medium">{tr("Change password", "修改密码")}</h4>
          <p className="text-sm text-muted-foreground">
            {tr(
              "You will need your current password. After updating, you will be signed out and asked to sign in again.",
              "需要先输入当前密码。更新后系统会退出登录，并要求你重新登录。",
            )}
          </p>
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
          <div className="space-y-1.5">
            <Label htmlFor="old-pw">{tr("Current password", "当前密码")}</Label>
            <Input
              id="old-pw"
              type="password"
              value={oldPassword}
              onChange={(e) => setOldPassword(e.target.value)}
              autoComplete="current-password"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="new-pw">{tr("New password", "新密码")}</Label>
            <Input
              id="new-pw"
              type="password"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
              autoComplete="new-password"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="confirm-pw">{tr("Confirm password", "确认密码")}</Label>
            <Input
              id="confirm-pw"
              type="password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              autoComplete="new-password"
            />
          </div>
        </div>
        {pwError && <p className="text-sm text-destructive">{pwError}</p>}
        <div className="flex justify-end">
          <Button
            type="submit"
            disabled={pwSaving}
            variant={pwSaved ? "outline" : "default"}
            className={
              pwSaved
                ? "border-emerald-500/30 text-emerald-600 dark:text-emerald-400"
                : ""
            }
          >
            {pwSaved ? (
              <>
                <Check className="h-4 w-4 mr-2" />
                {tr("Updated", "已更新")}
              </>
            ) : (
              pwSaving ? tr("Updating…", "正在更新…") : tr("Update password", "更新密码")
            )}
          </Button>
        </div>
      </form>

    </div>
  );
}
