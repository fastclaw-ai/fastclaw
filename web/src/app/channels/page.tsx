"use client";

import { useEffect, useState } from "react";
import { useTranslations } from "next-intl";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Radio, MessageCircle, Hash, Send } from "lucide-react";
import { getChannels, type ChannelInfo } from "@/lib/api";

const channelIcons: Record<string, React.ElementType> = {
  telegram: Send,
  discord: Hash,
  slack: MessageCircle,
};

const channelColors: Record<string, string> = {
  telegram: "from-blue-500 to-blue-600",
  discord: "from-indigo-500 to-indigo-600",
  slack: "from-green-500 to-green-600",
};

export default function ChannelsPage() {
  const t = useTranslations("channelsAdmin");
  const tc = useTranslations("common");
  const [channels, setChannels] = useState<ChannelInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [editChannel, setEditChannel] = useState<ChannelInfo | null>(null);

  const fetchChannels = () => {
    setLoading(true);
    getChannels()
      .then(setChannels)
      .catch(() => setChannels([]))
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    fetchChannels();
  }, []);

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      <div>
        <h2 className="text-2xl font-semibold tracking-tight">{t("title")}</h2>
        <p className="text-sm text-muted-foreground mt-1">
          {t("subtitle")}
        </p>
      </div>

      {loading ? (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {[1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-48" />
          ))}
        </div>
      ) : channels.length === 0 ? (
        <div className="rounded-lg border border-border bg-card">
          <div className="flex flex-col items-center justify-center py-16">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-blue-500/10 mb-4">
              <Radio className="h-7 w-7 text-blue-500" />
            </div>
            <p className="text-sm text-muted-foreground mb-1">{t("emptyTitle")}</p>
            <p className="text-xs text-muted-foreground/60">
              {t("emptyHint")}
            </p>
          </div>
        </div>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {channels.map((channel, i) => {
            const Icon = channelIcons[channel.type] || Radio;
            const gradient = channelColors[channel.type] || "from-zinc-500 to-zinc-600";
            const isConnected = channel.enabled !== false && channel.status !== "disconnected";

            return (
              <div
                key={i}
                className="group rounded-lg border border-border bg-card p-5 transition-colors hover:bg-muted/50 cursor-pointer"
                onClick={() => setEditChannel(channel)}
              >
                <div className="flex items-start justify-between mb-4">
                  <div className={`flex h-12 w-12 items-center justify-center rounded-xl bg-gradient-to-br ${gradient}`}>
                    <Icon className="h-6 w-6 text-white" />
                  </div>
                  <Badge
                    variant="outline"
                    className={
                      isConnected
                        ? "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/20"
                        : "bg-muted text-muted-foreground border-border"
                    }
                  >
                    <span
                      className={`mr-1.5 inline-block h-1.5 w-1.5 rounded-full ${
                        isConnected ? "bg-emerald-500" : "bg-muted-foreground"
                      }`}
                    />
                    {isConnected ? t("connected") : t("disconnected")}
                  </Badge>
                </div>
                <p className="text-base font-medium capitalize mb-1">
                  {channel.type}
                </p>
                <p className="text-sm text-muted-foreground">
                  {channel.botUsername
                    ? `@${channel.botUsername}`
                    : t("clickToConfigure")}
                </p>
              </div>
            );
          })}
        </div>
      )}

      {/* Channel Config Dialog */}
      <Dialog open={!!editChannel} onOpenChange={() => setEditChannel(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="capitalize">
              {t("configTitle", { type: editChannel?.type ?? "" })}
            </DialogTitle>
            <DialogDescription>
              {t("configDescription")}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label>{t("botTokenLabel")}</Label>
              <Input
                type="password"
                defaultValue="••••••••••••"
                className="font-mono"
              />
            </div>
            {editChannel?.botUsername && (
              <div className="space-y-2">
                <Label>{t("botUsernameLabel")}</Label>
                <Input
                  value={editChannel.botUsername}
                  disabled
                  className="opacity-60"
                />
              </div>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setEditChannel(null)}>
              {tc("cancel")}
            </Button>
            <Button>{tc("save")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
