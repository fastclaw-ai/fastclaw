"use client";

import { useEffect, useState } from "react";
import { useTranslations } from "next-intl";
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

interface UserRow {
  id: string;
  username: string;
  email: string;
  displayName?: string;
  role: string;
  status: string;
}

export default function AdminUsersPage() {
  const t = useTranslations("adminUsers");
  const tc = useTranslations("common");
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
      setError(t("errorRegistrationUpdate"));
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
          <h2 className="text-2xl font-semibold tracking-tight">{t("title")}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {t("subtitle")}
          </p>
        </div>
        <Button onClick={openCreateDialog}>
          <Plus className="h-4 w-4 mr-2" />
          {t("addUser")}
        </Button>
      </div>

      <Card>
        <CardContent>
          <div className="flex items-center justify-between gap-4">
            <div className="space-y-1">
              <p className="text-sm font-medium">{t("openRegistrationLabel")}</p>
              <p className="text-xs text-muted-foreground">
                {t("openRegistrationHint")}
              </p>
            </div>
            <Switch
              checked={!!regOpen}
              onCheckedChange={toggleRegistration}
              disabled={regOpen === null}
              aria-label={t("toggleRegistrationAria")}
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
            <p className="text-sm text-muted-foreground mb-1">{t("empty")}</p>
            <p className="text-xs text-muted-foreground/60 mb-4">
              {t("emptyHint")}
            </p>
            <Button variant="outline" size="sm" onClick={openCreateDialog}>
              <Plus className="h-4 w-4 mr-2" />
              {t("addUser")}
            </Button>
          </div>
        </div>
      ) : (
        <div className="rounded-lg border border-border bg-card overflow-hidden">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("colUsername")}</TableHead>
                <TableHead>{t("colEmail")}</TableHead>
                <TableHead>{t("colRole")}</TableHead>
                <TableHead>{t("colStatus")}</TableHead>
                <TableHead className="text-right">{t("colActions")}</TableHead>
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
                        <SelectValue>
                          {(v: unknown) =>
                            v === "super_admin" ? t("roleSuperAdmin") : t("roleUser")
                          }
                        </SelectValue>
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="user">{t("roleUser")}</SelectItem>
                        <SelectItem value="super_admin">{t("roleSuperAdmin")}</SelectItem>
                      </SelectContent>
                    </Select>
                  </TableCell>
                  <TableCell>
                    <Select value={u.status} onValueChange={(v) => v && setStatus(u, v)}>
                      <SelectTrigger size="sm" className="w-32">
                        <SelectValue>
                          {(v: unknown) =>
                            v === "disabled" ? t("statusDisabled") : t("statusActive")
                          }
                        </SelectValue>
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="active">{t("statusActive")}</SelectItem>
                        <SelectItem value="disabled">{t("statusDisabled")}</SelectItem>
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
                        title={t("resetPasswordTitle")}
                      >
                        <KeyRound className="size-4" />
                      </Button>
                      <Button
                        size="icon"
                        variant="ghost"
                        className="text-destructive hover:text-destructive"
                        onClick={() => setDeleteTarget(u)}
                        title={tc("delete")}
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
            <DialogTitle>{t("addUser")}</DialogTitle>
            <DialogDescription>
              {t("createDescription")}
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={handleCreate} className="space-y-4 py-2">
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="user-username">{t("usernameLabel")}</Label>
                <Input
                  id="user-username"
                  required
                  value={form.username}
                  onChange={(e) => setForm({ ...form, username: e.target.value })}
                  placeholder={t("usernamePlaceholder")}
                  autoFocus
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="user-email">{t("emailLabel")}</Label>
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
              <Label htmlFor="user-password">{t("passwordLabel")}</Label>
              <Input
                id="user-password"
                required
                type="password"
                value={form.password}
                onChange={(e) => setForm({ ...form, password: e.target.value })}
                placeholder={t("passwordPlaceholder")}
              />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <Label htmlFor="user-display">{t("displayNameLabel")}</Label>
                <Input
                  id="user-display"
                  value={form.displayName}
                  onChange={(e) => setForm({ ...form, displayName: e.target.value })}
                  placeholder={t("displayNamePlaceholder")}
                />
              </div>
              <div className="space-y-1.5">
                <Label>{t("roleLabel")}</Label>
                <Select
                  value={form.role}
                  onValueChange={(v) => v && setForm({ ...form, role: v })}
                >
                  <SelectTrigger>
                    <SelectValue>
                      {(v: unknown) =>
                        v === "super_admin" ? t("roleSuperAdmin") : t("roleUser")
                      }
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="user">{t("roleUser")}</SelectItem>
                    <SelectItem value="super_admin">{t("roleSuperAdmin")}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setCreateOpen(false)}>
                {tc("cancel")}
              </Button>
              <Button
                type="submit"
                disabled={!form.username.trim() || !form.email.trim() || !form.password.trim()}
              >
                {t("createUser")}
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
            <DialogTitle>{t("resetPasswordTitle")}</DialogTitle>
            <DialogDescription>
              {t.rich("resetPasswordDescription", {
                username: resetTarget?.username ?? "",
                code: (chunks) => (
                  <code className="rounded bg-muted px-1.5 py-0.5 text-xs">{chunks}</code>
                ),
              })}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-1.5 py-2">
            <Label htmlFor="reset-pwd">{t("newPasswordLabel")}</Label>
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
              {tc("cancel")}
            </Button>
            <Button onClick={handleResetPassword} disabled={!resetPwd.trim()}>
              {t("resetPasswordTitle")}
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
            <AlertDialogTitle>{t("deleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t.rich("deleteConfirm", {
                username: deleteTarget?.username ?? "",
                code: (chunks) => (
                  <code className="rounded bg-muted px-1.5 py-0.5 text-xs">{chunks}</code>
                ),
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={() => deleteTarget && handleDelete(deleteTarget)}>
              {tc("delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
