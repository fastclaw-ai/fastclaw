"use client";

import { useEffect, useState } from "react";
import {
  adminListUsers,
  adminCreateUser,
  adminUpdateUser,
  adminDeleteUser,
  adminResetPassword,
  getRegistration,
  setRegistration,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent } from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Users, KeyRound, Trash2, Plus } from "lucide-react";
import { useLocale } from "@/components/locale-provider";

interface UserRow {
  id: string;
  username: string;
  email: string;
  displayName?: string;
  role: string;
  status: string;
}

export default function AdminUsersPage() {
  const { tr } = useLocale();
  const [users, setUsers] = useState<UserRow[]>([]);
  const [error, setError] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [form, setForm] = useState({
    username: "",
    email: "",
    password: "",
    displayName: "",
    role: "user",
  });

  const [deleteTarget, setDeleteTarget] = useState<UserRow | null>(null);
  const [resetTarget, setResetTarget] = useState<UserRow | null>(null);
  const [resetPwd, setResetPwd] = useState("");
  const [regOpen, setRegOpen] = useState<boolean | null>(null);

  async function refresh() {
    setError("");
    const res = await adminListUsers();
    if (res.users) setUsers(res.users);
    if (res.error) setError(res.error);
  }
  useEffect(() => {
    refresh();
    getRegistration()
      .then((r) => setRegOpen(!!r.open))
      .catch(() => setRegOpen(false));
  }, []);

  async function toggleRegistration(next: boolean) {
    // Optimistic flip; revert on error so the UI never lies about the
    // backend state.
    setRegOpen(next);
    try {
      const r = await setRegistration(next);
      setRegOpen(!!r.open);
    } catch {
      setRegOpen(!next);
      setError(tr("Failed to update registration setting", "更新注册设置失败"));
    }
  }

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    const res = await adminCreateUser(form);
    if (res.error) {
      setError(res.error);
      return;
    }
    setCreateOpen(false);
    setForm({ username: "", email: "", password: "", displayName: "", role: "user" });
    refresh();
  }

  async function setRole(u: UserRow, role: string) {
    setError("");
    const res = await adminUpdateUser(u.id, { role });
    if (res.error) setError(res.error);
    refresh();
  }

  async function setStatus(u: UserRow, status: string) {
    setError("");
    const res = await adminUpdateUser(u.id, { status });
    if (res.error) setError(res.error);
    refresh();
  }

  async function handleResetPassword() {
    if (!resetTarget || !resetPwd.trim()) return;
    const res = await adminResetPassword(resetTarget.id, resetPwd);
    if (res.error) {
      setError(res.error);
      return;
    }
    setResetTarget(null);
    setResetPwd("");
  }

  async function handleDelete(u: UserRow) {
    const res = await adminDeleteUser(u.id);
    if (res.error) setError(res.error);
    setDeleteTarget(null);
    refresh();
  }

  function openCreateDialog() {
    setForm({ username: "", email: "", password: "", displayName: "", role: "user" });
    setError("");
    setCreateOpen(true);
  }

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">
            {tr("Users", "用户")}
          </h2>
          <p className="text-sm text-muted-foreground mt-1">
            {tr(
              "Manage platform members. Each user gets isolated agents, sessions, and keys.",
              "管理平台成员。每位用户都拥有相互隔离的 Agent、会话和密钥。",
            )}
          </p>
        </div>
        <Button onClick={openCreateDialog}>
          <Plus className="h-4 w-4 mr-2" />
          {tr("Add user", "添加用户")}
        </Button>
      </div>

      <Card>
        <CardContent>
          <div className="flex items-center justify-between gap-4">
            <div className="space-y-1">
              <p className="text-sm font-medium">
                {tr("Open registration", "开放注册")}
              </p>
              <p className="text-xs text-muted-foreground">
                {tr(
                  "When enabled, anyone with the URL can create an account through /signup. When disabled, only you can add users from this page.",
                  "启用后，任何获得链接的人都能通过 /signup 创建账户；关闭后，只有你能从此页面添加用户。",
                )}
              </p>
            </div>
            <Switch
              checked={!!regOpen}
              onCheckedChange={toggleRegistration}
              disabled={regOpen === null}
              aria-label={tr("Toggle public registration", "切换公开注册")}
            />
          </div>
        </CardContent>
      </Card>

      {error && (
        <Card className="border-destructive/40 bg-destructive/5">
          <CardContent className="pt-6">
            <p className="text-sm text-destructive">{error}</p>
          </CardContent>
        </Card>
      )}

      {users.length === 0 ? (
        <div className="rounded-lg border border-border bg-card">
          <div className="flex flex-col items-center justify-center py-16">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-primary/10 mb-4">
              <Users className="h-7 w-7 text-primary" />
            </div>
            <p className="text-sm text-muted-foreground mb-1">
              {tr("No users yet", "还没有用户")}
            </p>
            <p className="text-xs text-muted-foreground/60 mb-4">
              {tr(
                "Add a user to give them their own scoped workspace",
                "添加用户，为其分配独立的工作空间",
              )}
            </p>
            <Button variant="outline" size="sm" onClick={openCreateDialog}>
              <Plus className="h-4 w-4 mr-2" />
              {tr("Add user", "添加用户")}
            </Button>
          </div>
        </div>
      ) : (
        <div className="rounded-lg border border-border bg-card overflow-hidden">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{tr("Username", "用户名")}</TableHead>
                <TableHead>{tr("Email", "邮箱")}</TableHead>
                <TableHead>{tr("Role", "角色")}</TableHead>
                <TableHead>{tr("Status", "状态")}</TableHead>
                <TableHead className="text-right">{tr("Actions", "操作")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.map((u) => (
                <TableRow key={u.id}>
                  <TableCell className="font-medium">
                    <div>{u.username}</div>
                    {u.displayName && (
                      <div className="text-xs text-muted-foreground">{u.displayName}</div>
                    )}
                  </TableCell>
                  <TableCell className="text-sm text-muted-foreground">{u.email}</TableCell>
                  <TableCell>
                    <Select value={u.role} onValueChange={(v) => v && setRole(u, v)}>
                      <SelectTrigger size="sm" className="w-36">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="user">{tr("User", "普通用户")}</SelectItem>
                        <SelectItem value="super_admin">{tr("Super admin", "超级管理员")}</SelectItem>
                      </SelectContent>
                    </Select>
                  </TableCell>
                  <TableCell>
                    <Select value={u.status} onValueChange={(v) => v && setStatus(u, v)}>
                      <SelectTrigger size="sm" className="w-32">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="active">{tr("Active", "启用")}</SelectItem>
                        <SelectItem value="disabled">{tr("Disabled", "停用")}</SelectItem>
                      </SelectContent>
                    </Select>
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button
                        size="icon"
                        variant="ghost"
                        onClick={() => {
                          setResetPwd("");
                          setResetTarget(u);
                        }}
                        title={tr("Reset password", "重置密码")}
                      >
                        <KeyRound className="size-4" />
                      </Button>
                      <Button
                        size="icon"
                        variant="ghost"
                        className="text-destructive hover:text-destructive"
                        onClick={() => setDeleteTarget(u)}
                        title={tr("Delete", "删除")}
                      >
                        <Trash2 className="size-4" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{tr("Add user", "添加用户")}</DialogTitle>
            <DialogDescription>
              {tr(
                "Create a platform member with their own scoped agents, sessions, and keys.",
                "创建平台成员，并为其分配独立的 Agent、会话和密钥。",
              )}
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={handleCreate} className="space-y-4 py-2">
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="user-username">{tr("Username", "用户名")}</Label>
                <Input
                  id="user-username"
                  required
                  value={form.username}
                  onChange={(e) => setForm({ ...form, username: e.target.value })}
                  placeholder={tr("e.g. alice", "例如 alice")}
                  autoFocus
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="user-email">{tr("Email", "邮箱")}</Label>
                <Input
                  id="user-email"
                  required
                  type="email"
                  value={form.email}
                  onChange={(e) => setForm({ ...form, email: e.target.value })}
                  placeholder="alice@example.com"
                />
              </div>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="user-password">{tr("Password", "密码")}</Label>
              <Input
                id="user-password"
                required
                type="password"
                value={form.password}
                onChange={(e) => setForm({ ...form, password: e.target.value })}
                placeholder={tr("Initial password", "初始密码")}
              />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="user-display">{tr("Display name", "显示名称")}</Label>
                <Input
                  id="user-display"
                  value={form.displayName}
                  onChange={(e) => setForm({ ...form, displayName: e.target.value })}
                  placeholder={tr("Optional", "选填")}
                />
              </div>
              <div className="space-y-1.5">
                <Label>{tr("Role", "角色")}</Label>
                <Select
                  value={form.role}
                  onValueChange={(v) => v && setForm({ ...form, role: v })}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="user">{tr("User", "普通用户")}</SelectItem>
                    <SelectItem value="super_admin">{tr("Super admin", "超级管理员")}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setCreateOpen(false)}>
                {tr("Cancel", "取消")}
              </Button>
              <Button
                type="submit"
                disabled={!form.username.trim() || !form.email.trim() || !form.password.trim()}
              >
                {tr("Create user", "创建用户")}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog
        open={resetTarget !== null}
        onOpenChange={(o) => {
          if (!o) {
            setResetTarget(null);
            setResetPwd("");
          }
        }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{tr("Reset password", "重置密码")}</DialogTitle>
            <DialogDescription>
              {tr("Set a new password for", "为")} {" "}
              <code className="rounded bg-muted px-1.5 py-0.5 text-xs">
                {resetTarget?.username}
              </code>
              {tr(
                ". They will need it the next time they sign in.",
                " 设置新密码。该用户下次登录时需要使用此密码。",
              )}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-1.5 py-2">
            <Label htmlFor="reset-pwd">{tr("New password", "新密码")}</Label>
            <Input
              id="reset-pwd"
              type="password"
              value={resetPwd}
              onChange={(e) => setResetPwd(e.target.value)}
              autoFocus
            />
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                setResetTarget(null);
                setResetPwd("");
              }}
            >
              {tr("Cancel", "取消")}
            </Button>
            <Button onClick={handleResetPassword} disabled={!resetPwd.trim()}>
              {tr("Reset password", "重置密码")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog
        open={deleteTarget !== null}
        onOpenChange={(o) => !o && setDeleteTarget(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tr("Delete user?", "删除用户？")}</AlertDialogTitle>
            <AlertDialogDescription>
              <code className="rounded bg-muted px-1.5 py-0.5 text-xs">
                {deleteTarget?.username}
              </code>{" "}
              {tr(
                "will be removed along with all of their agents, sessions, and API keys. This cannot be undone.",
                "及其所有 Agent、会话和 API 密钥都将被删除。此操作无法撤销。",
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tr("Cancel", "取消")}</AlertDialogCancel>
            <AlertDialogAction onClick={() => deleteTarget && handleDelete(deleteTarget)}>
              {tr("Delete", "删除")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
