"use client";

import { useEffect, useState } from "react";
import { useParams } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Save, Check, Loader2 } from "lucide-react";
import { apiFetch } from "@/lib/api";

const WORKSPACE_FILES = [
  { name: "SOUL.md", label: "Soul" },
  { name: "IDENTITY.md", label: "Identity" },
  { name: "USER.md", label: "User" },
  { name: "TOOLS.md", label: "Tools" },
  { name: "BOOTSTRAP.md", label: "Bootstrap" },
  { name: "HEARTBEAT.md", label: "Heartbeat" },
  { name: "MEMORY.md", label: "Memory" },
  { name: "AGENTS.md", label: "Agents" },
];

export default function AgentFilesPage() {
  const params = useParams();
  const agentId = params.id as string;
  const [activeTab, setActiveTab] = useState("SOUL.md");
  const [files, setFiles] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    setLoading(true);
    // Load all workspace files for this agent
    Promise.all(
      WORKSPACE_FILES.map(async (f) => {
        try {
          const res = await apiFetch(`/api/agents/${agentId}/files/${f.name}`);
          if (res.ok) {
            const data = await res.json();
            return [f.name, data.content || ""] as [string, string];
          }
        } catch {}
        return [f.name, ""] as [string, string];
      })
    ).then((entries) => {
      setFiles(Object.fromEntries(entries));
      setLoading(false);
    });
  }, [agentId]);

  const handleSave = async () => {
    setSaving(true);
    try {
      await apiFetch(`/api/agents/${agentId}/files/${activeTab}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ content: files[activeTab] || "" }),
      });
      setSaved(true);
      setTimeout(() => setSaved(false), 2000);
    } catch {}
    setSaving(false);
  };

  if (loading) {
    return (
      <div className="p-6 space-y-4">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-96 w-full" />
      </div>
    );
  }

  return (
    <div className="p-6 max-w-4xl mx-auto">
      <div className="flex items-center justify-between mb-4">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">Files</h2>
          <p className="text-sm text-muted-foreground mt-1">
            Agent workspace files for <strong>{agentId}</strong>
          </p>
        </div>
        <Button
          onClick={handleSave}
          disabled={saving}
          variant={saved ? "outline" : "default"}
          className={saved ? "border-emerald-500/30 text-emerald-600" : ""}
        >
          {saved ? (
            <><Check className="h-4 w-4 mr-2" /> Saved</>
          ) : saving ? (
            <><Loader2 className="h-4 w-4 mr-2 animate-spin" /> Saving...</>
          ) : (
            <><Save className="h-4 w-4 mr-2" /> Save</>
          )}
        </Button>
      </div>

      {/* Tabs */}
      <div className="flex gap-1 border-b border-border mb-4 overflow-x-auto">
        {WORKSPACE_FILES.map((f) => (
          <button
            key={f.name}
            onClick={() => setActiveTab(f.name)}
            className={`px-3 py-2 text-sm font-medium whitespace-nowrap border-b-2 transition-colors ${
              activeTab === f.name
                ? "border-primary text-primary"
                : "border-transparent text-muted-foreground hover:text-foreground"
            }`}
          >
            {f.label}
          </button>
        ))}
      </div>

      {/* Editor */}
      <textarea
        value={files[activeTab] || ""}
        onChange={(e) =>
          setFiles((prev) => ({ ...prev, [activeTab]: e.target.value }))
        }
        spellCheck={false}
        className="w-full rounded-lg border border-border bg-card px-4 py-3 font-mono text-sm leading-relaxed outline-none focus:ring-1 focus:ring-primary/30 resize-none"
        style={{ minHeight: 400 }}
        placeholder={`# ${activeTab}\n\nWrite your content here...`}
      />
    </div>
  );
}
