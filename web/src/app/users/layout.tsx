import { SidebarLayout } from "@/components/sidebar";

export default function UsersLayout({ children }: { children: React.ReactNode }) {
  return <SidebarLayout>{children}</SidebarLayout>;
}
