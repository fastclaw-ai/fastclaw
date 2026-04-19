export function generateStaticParams() {
  return [{ id: "default" }];
}

export default function AgentLayout({ children }: { children: React.ReactNode }) {
  return <>{children}</>;
}
