"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import {
  LayoutDashboard,
  MessageSquare,
  Bot,
  Sparkles,
  Brain,
  Radio,
  Settings,
  Menu,
  X,
  Sun,
  Moon,
  LogOut,
  Users,
  ArrowLeft,
  BookOpen,
  History,
  Wrench,
  Heart,
  PanelLeftClose,
  PanelLeftOpen,
} from "lucide-react";
import { useState, useEffect } from "react";
import { useTheme } from "@/components/theme-provider";
import { isLoggedIn, logout } from "@/lib/auth";
import { getStatus } from "@/lib/api";

// Global navigation (FastClaw level)
const globalNavItems = [
  { href: "/overview/", label: "Overview", icon: LayoutDashboard },
  { href: "/agents/", label: "Agents", icon: Bot },
  { href: "/skills/", label: "Skills", icon: Sparkles },
  { href: "/models/", label: "Models", icon: Brain },
  { href: "/channels/", label: "Channels", icon: Radio },
  { href: "/settings/", label: "Settings", icon: Settings },
];

const adminNavItem = { href: "/users/", label: "API Keys", icon: Users };

// Agent-level navigation (inside /agents/{id}/)
const agentNavItems = (agentId: string) => [
  { href: `/agents/${agentId}/chat/`, label: "Chat", icon: MessageSquare },
  { href: `/agents/${agentId}/files/`, label: "Files", icon: BookOpen },
  { href: `/agents/${agentId}/skills/`, label: "Skills", icon: Sparkles },
  { href: `/agents/${agentId}/models/`, label: "Models", icon: Brain },
  { href: `/agents/${agentId}/sessions/`, label: "Sessions", icon: History },
  { href: `/agents/${agentId}/settings/`, label: "Settings", icon: Wrench },
];

// Extract agent ID from pathname like /agents/default/chat/
function extractAgentId(pathname: string): string | null {
  const match = pathname.match(/^\/agents\/([^/]+)\/(chat|files|skills|models|sessions|settings)/);
  return match ? match[1] : null;
}

export function SidebarLayout({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const [mobileOpen, setMobileOpen] = useState(false);
  const [collapsed, setCollapsed] = useState(false);
  const { theme, toggleTheme } = useTheme();
  const [gatewayRunning, setGatewayRunning] = useState(false);
  const [isAdmin, setIsAdmin] = useState(false);

  useEffect(() => {
    getStatus()
      .then((s) => {
        setGatewayRunning(s.running);
        setIsAdmin(s.isAdmin || false);
        // Redirect to onboard if not configured
        if (!s.configured && !window.location.pathname.startsWith("/onboard")) {
          window.location.href = "/onboard/";
          return;
        }
        // Redirect to login if configured but not logged in
        if (s.configured && !isLoggedIn() && !window.location.pathname.startsWith("/onboard")) {
          window.location.href = "/";
        }
      })
      .catch(() => {});
    const interval = setInterval(() => {
      getStatus()
        .then((s) => {
          setGatewayRunning(s.running);
          setIsAdmin(s.isAdmin || false);
        })
        .catch(() => {});
    }, 15000);
    return () => clearInterval(interval);
  }, []);

  const activeAgentId = extractAgentId(pathname);
  const isAgentView = activeAgentId !== null;

  const navItems = isAgentView
    ? agentNavItems(activeAgentId)
    : isAdmin
    ? [...globalNavItems, adminNavItem]
    : globalNavItems;

  const NavLinks = ({ onClick, iconOnly }: { onClick?: () => void; iconOnly?: boolean }) => (
    <>
      {isAgentView && (
        <Link
          href="/agents/"
          onClick={onClick}
          className={`flex items-center rounded-lg text-sm font-medium text-muted-foreground hover:bg-muted/50 hover:text-foreground transition-colors mb-2 ${iconOnly ? "justify-center p-2" : "gap-3 px-3 py-2"}`}
          title="Back to FastClaw"
        >
          <ArrowLeft className="h-4 w-4" />
          {!iconOnly && "Back to FastClaw"}
        </Link>
      )}

      {navItems.map((item) => {
        const normPath = pathname.replace(/\/$/, "");
        const normHref = item.href.replace(/\/$/, "");
        const isActive =
          normPath === normHref || normPath.startsWith(normHref + "/");
        return (
          <Link
            key={item.href}
            href={item.href}
            onClick={onClick}
            className={`flex items-center rounded-lg text-sm font-medium transition-colors ${
              iconOnly ? "justify-center p-2" : "gap-3 px-3 py-2"
            } ${
              isActive
                ? "bg-primary/10 text-primary"
                : "text-muted-foreground hover:bg-muted/50 hover:text-foreground"
            }`}
            title={iconOnly ? item.label : undefined}
          >
            <item.icon className="h-4 w-4" />
            {!iconOnly && item.label}
          </Link>
        );
      })}
    </>
  );

  return (
    <div className="flex min-h-screen bg-background">
      {/* Desktop sidebar */}
      <aside className={`hidden flex-col border-r border-border bg-card/50 md:flex transition-all duration-200 ${collapsed ? "w-14" : "w-60"}`}>
        {/* Logo/header */}
        <div className={`flex h-14 items-center border-b border-border ${collapsed ? "justify-center px-2" : "gap-2.5 px-4"}`}>
          {collapsed ? (
            <button
              onClick={() => setCollapsed(false)}
              className="flex h-8 w-8 items-center justify-center rounded-lg hover:bg-muted/50 transition-colors"
              title="Expand sidebar"
            >
              <PanelLeftOpen className="h-4 w-4 text-muted-foreground" />
            </button>
          ) : isAgentView ? (
            <>
              <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary/10 shrink-0">
                <Bot className="h-4 w-4 text-primary" />
              </div>
              <div className="flex-1 min-w-0">
                <span className="text-sm font-semibold text-foreground truncate block">
                  {activeAgentId}
                </span>
                <p className="text-[10px] text-muted-foreground leading-none">Agent</p>
              </div>
              <button onClick={() => setCollapsed(true)} className="shrink-0 rounded-md p-1 text-muted-foreground hover:text-foreground hover:bg-muted/50" title="Collapse sidebar">
                <PanelLeftClose className="h-4 w-4" />
              </button>
            </>
          ) : (
            <>
              <div className="relative flex h-8 w-8 items-center justify-center shrink-0">
                <img src="/logo.png" alt="FastClaw" className="h-8 w-8 rounded-lg" />
                <span className={`absolute -bottom-0.5 -right-0.5 h-2.5 w-2.5 rounded-full border-2 border-card ${gatewayRunning ? "bg-emerald-500 animate-pulse" : "bg-muted-foreground/40"}`} />
              </div>
              <div className="flex-1 min-w-0">
                <span className="text-sm font-semibold text-foreground">FastClaw</span>
                <p className="text-[10px] text-muted-foreground leading-none">
                  {gatewayRunning ? "Gateway running" : "Gateway stopped"}
                </p>
              </div>
              <button onClick={() => setCollapsed(true)} className="shrink-0 rounded-md p-1 text-muted-foreground hover:text-foreground hover:bg-muted/50" title="Collapse sidebar">
                <PanelLeftClose className="h-4 w-4" />
              </button>
            </>
          )}
        </div>

        {/* Navigation */}
        <nav className={`flex-1 space-y-1 overflow-y-auto ${collapsed ? "p-1.5" : "p-3"}`}>
          <NavLinks iconOnly={collapsed} />
        </nav>

        {/* Footer */}
        <div className={`border-t border-border space-y-2 ${collapsed ? "p-1.5" : "p-3"}`}>
          {isAdmin && !isAgentView && !collapsed && (
            <div className="flex items-center gap-2 px-1">
              <div className="h-6 w-6 rounded-full bg-emerald-600/20 flex items-center justify-center">
                <span className="text-[10px] font-bold text-emerald-400">A</span>
              </div>
              <div className="flex-1 min-w-0">
                <p className="text-xs font-medium text-foreground truncate">Admin</p>
                <p className="text-[10px] text-muted-foreground/60">Gateway Manager</p>
              </div>
            </div>
          )}
          <div className={`flex items-center ${collapsed ? "flex-col gap-1" : "justify-between"}`}>
            {!collapsed && (
              <span className="text-[11px] text-muted-foreground/60 font-mono">
                v0.1.0
              </span>
            )}
            <div className={`flex items-center ${collapsed ? "flex-col" : ""} gap-1`}>
              {isLoggedIn() && (
                <button
                  onClick={() => { logout(); window.location.reload(); }}
                  className="rounded-md p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors"
                  title="Logout"
                >
                  <LogOut className="h-3.5 w-3.5" />
                </button>
              )}
              <button
                onClick={toggleTheme}
                className="rounded-md p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors"
              >
                {theme === "dark" ? (
                  <Sun className="h-3.5 w-3.5" />
                ) : (
                  <Moon className="h-3.5 w-3.5" />
                )}
              </button>
            </div>
          </div>
        </div>
      </aside>

      {/* Mobile header */}
      <div className="fixed top-0 left-0 right-0 z-40 flex h-12 items-center justify-between border-b border-border bg-card/95 px-4 backdrop-blur-sm md:hidden">
        <div className="flex items-center gap-2">
          {isAgentView ? (
            <>
              <Link href="/agents/" className="p-1">
                <ArrowLeft className="h-4 w-4 text-muted-foreground" />
              </Link>
              <Bot className="h-5 w-5 text-primary" />
              <span className="text-sm font-semibold text-foreground">{activeAgentId}</span>
            </>
          ) : (
            <>
              <img src="/logo.png" alt="FastClaw" className="h-7 w-7 rounded-md" />
              <span className="text-sm font-semibold text-foreground">FastClaw</span>
            </>
          )}
        </div>
        <button
          onClick={() => setMobileOpen(!mobileOpen)}
          className="rounded-md p-2 text-muted-foreground hover:text-foreground"
        >
          {mobileOpen ? <X className="h-5 w-5" /> : <Menu className="h-5 w-5" />}
        </button>
      </div>

      {/* Mobile menu overlay */}
      {mobileOpen && (
        <div
          className="fixed inset-0 z-30 bg-background/80 backdrop-blur-sm md:hidden"
          onClick={() => setMobileOpen(false)}
        >
          <div
            className="absolute top-12 right-0 w-64 border-l border-border bg-card p-3 space-y-1 h-[calc(100vh-3rem)] overflow-y-auto"
            onClick={(e) => e.stopPropagation()}
          >
            <NavLinks onClick={() => setMobileOpen(false)} />
          </div>
        </div>
      )}

      {/* Main content */}
      <main className="flex-1 pt-12 md:pt-0 overflow-y-auto">{children}</main>
    </div>
  );
}
