"use client";

import * as React from "react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "@/components/ui/sidebar";
import {
  ChevronsUpDownIcon,
  LogOutIcon,
  SettingsIcon,
} from "lucide-react";
import { useLocale } from "@/components/locale-provider";
import { logout as doLogout } from "@/lib/auth";

export function NavUser({
  name,
  subtitle = "Gateway running",
}: {
  name?: string;
  subtitle?: string;
}) {
  const { isMobile } = useSidebar();
  const { t, tr } = useLocale();

  // `name` comes from the current user's profile. Render it verbatim — a
  // value such as "Admin" may be the user's chosen display name, not a role
  // label that should be translated.
  const displayName = name?.trim() || t("common.user");
  const displaySubtitle =
    subtitle === "Gateway running"
      ? tr("Gateway running", "网关运行中")
      : subtitle;

  const initials = displayName.slice(0, 2).toUpperCase();

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <SidebarMenuButton
                size="lg"
                className="data-open:bg-sidebar-accent data-open:text-sidebar-accent-foreground"
              />
            }
          >
            <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-emerald-500/20 text-emerald-500 text-xs font-bold">
              {initials}
            </div>
            <div className="grid flex-1 text-left text-sm leading-tight">
              <span className="truncate font-medium">{displayName}</span>
              <span className="truncate text-xs text-muted-foreground">
                {displaySubtitle}
              </span>
            </div>
            <ChevronsUpDownIcon className="ml-auto size-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="min-w-56 rounded-lg"
            side={isMobile ? "bottom" : "right"}
            align="end"
            sideOffset={4}
          >
            <DropdownMenuGroup>
              <DropdownMenuLabel className="p-0 font-normal">
                <div className="flex items-center gap-2 px-1 py-1.5 text-left text-sm">
                  <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-emerald-500/20 text-emerald-500 text-xs font-bold">
                    {initials}
                  </div>
                  <div className="grid flex-1 text-left text-sm leading-tight">
                    <span className="truncate font-medium">{displayName}</span>
                    <span className="truncate text-xs text-muted-foreground">
                      {displaySubtitle}
                    </span>
                  </div>
                </div>
              </DropdownMenuLabel>
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onClick={() => {
                window.dispatchEvent(new CustomEvent("fastclaw:open-user-settings"));
              }}
            >
              <SettingsIcon />
              <span>{t("common.settings")}</span>
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onClick={() => {
                doLogout();
                window.location.href = "/";
              }}
            >
              <LogOutIcon />
              <span>{t("common.logOut")}</span>
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
