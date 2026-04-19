export default function AgentSettingsPage({ params }: { params: { id: string } }) {
  return (
    <div className="p-6 max-w-3xl mx-auto">
      <h2 className="text-2xl font-semibold tracking-tight">Settings</h2>
      <p className="text-sm text-muted-foreground mt-1">Agent: {params.id}</p>
      <div className="mt-6 rounded-lg border border-border bg-card p-8 text-center text-muted-foreground">
        Coming soon
      </div>
    </div>
  );
}
