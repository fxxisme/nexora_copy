"use client";

import * as React from "react";
import { format, formatDistanceToNow } from "date-fns";
import { zhCN } from "date-fns/locale";
import {
  Coins,
  Gauge,
  Infinity as InfinityIcon,
  Layers,
  LoaderCircle,
  MessageSquare,
  Sparkles,
  type LucideIcon,
} from "lucide-react";

import { cn } from "@/lib/utils";
import { useAuthStore } from "@/store/auth";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";

type QuotaRow = {
  key: string;
  label: string;
  icon: LucideIcon;
  unlimited: boolean;
  used: number;
  quota: number;
  remaining?: number;
  hint: string;
};

function buildRows(identity: NonNullable<ReturnType<typeof useAuthStore.getState>["identity"]>): QuotaRow[] {
  const isSubscriber = identity.is_subscriber;
  const standardLimit = identity.quota_standard_image_limit ?? 0;
  const standardUsed = identity.quota_standard_image_used ?? 0;
  const standardRemaining = Math.max(0, standardLimit - standardUsed);

  const advancedLimit = identity.quota_advanced_image_limit ?? 0;
  const advancedUsed = identity.quota_advanced_image_used ?? 0;
  const advancedRemaining = Math.max(0, advancedLimit - advancedUsed);

  const chatLimit = identity.quota_chat_limit ?? 0;
  const chatUsed = identity.quota_chat_used ?? 0;
  const chatRemaining = Math.max(0, chatLimit - chatUsed);

  return [
    {
      key: "standard",
      label: "标准生图",
      icon: Sparkles,
      unlimited: isSubscriber,
      used: standardUsed,
      quota: standardLimit,
      remaining: isSubscriber ? undefined : standardRemaining,
      hint: isSubscriber ? "订阅者无限" : "Nano-Banana 极速出图",
    },
    {
      key: "advanced",
      label: "进阶生图",
      icon: Layers,
      unlimited: false,
      used: advancedUsed,
      quota: advancedLimit,
      remaining: advancedRemaining,
      hint: "Ultra / 4K / 超清重绘",
    },
    {
      key: "chat",
      label: "对话消息",
      icon: MessageSquare,
      unlimited: false,
      used: chatUsed,
      quota: chatLimit,
      remaining: chatRemaining,
      hint: "上游对话调用",
    },
  ];
}

export function QuotaPopover() {
  const identity = useAuthStore((s) => s.identity);
  const isLoading = useAuthStore((s) => s.isLoadingIdentity);
  const error = useAuthStore((s) => s.identityError);
  const fetchIdentity = useAuthStore((s) => s.fetchIdentity);
  const [open, setOpen] = React.useState(false);

  // 每次打开弹层静默拉最新额度
  React.useEffect(() => {
    if (open) {
      void fetchIdentity();
    }
  }, [open, fetchIdentity]);

  // 未登录或尚未拉取到身份时，不展示入口（避免在登录页或冷启阶段抢视线）
  if (!identity && !isLoading) {
    return null;
  }

  // 快捷角标：若有任一档即将耗尽（< 20%），外层 icon 加红点提示
  const hasWarning = (() => {
    if (!identity) return false;
    const rows = buildRows(identity);
    return rows.some((r) => {
      if (r.unlimited) return false;
      if (r.quota <= 0) return false;
      return (r.remaining ?? 0) <= 0 || (r.remaining ?? 0) / r.quota < 0.2;
    });
  })();

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label="查看额度使用情况"
          title="查看额度"
          className="relative grid size-8 cursor-pointer place-items-center rounded-lg border border-border/70 bg-card/60 text-muted-foreground transition hover:border-border hover:bg-card hover:text-foreground"
        >
          <Coins className="size-4" />
          {hasWarning ? (
            <span
              data-slot="badge"
              className="absolute -right-0.5 -top-0.5 size-2 rounded-full bg-rose-500 ring-2 ring-background"
            />
          ) : null}
        </button>
      </PopoverTrigger>
      <PopoverContent align="end" sideOffset={8} className="w-[320px] p-0">
        <div className="border-b border-border px-4 py-3">
          <div className="text-sm font-semibold text-foreground">额度使用情况</div>
          <p className="mt-0.5 text-xs leading-5 text-muted-foreground">
            画图与对话三档额度独立计费，任一档用完会停用对应能力。
          </p>
        </div>
        <div className="px-4 py-3">
          {isLoading ? (
            <div className="flex items-center justify-center py-6 text-muted-foreground">
              <LoaderCircle className="size-4 animate-spin" />
            </div>
          ) : error ? (
            <div className="rounded-lg bg-destructive/10 px-3 py-2 text-xs text-destructive border border-destructive/20">{error}</div>
          ) : identity ? (
            <div className="space-y-2">
              {buildRows(identity).map((row) => (
                <QuotaItem key={row.key} row={row} />
              ))}
            </div>
          ) : null}
        </div>
      </PopoverContent>
    </Popover>
  );
}

function QuotaItem({ row }: { row: QuotaRow }) {
  const Icon = row.icon;
  const exhausted = !row.unlimited && (row.remaining ?? 0) <= 0;
  const percent = row.unlimited || row.quota <= 0
    ? 0
    : Math.max(0, Math.min(100, Math.round((row.used / row.quota) * 100)));
  return (
    <div className="rounded-lg border border-border/60 bg-secondary/40 px-3 py-2.5">
      <div className="flex items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <Icon className="size-3.5 shrink-0 text-muted-foreground" />
          <div className="min-w-0">
            <div className="text-[13px] font-medium leading-tight text-foreground">{row.label}</div>
            <div className="mt-0.5 truncate text-[11px] leading-tight text-muted-foreground">{row.hint}</div>
          </div>
        </div>
        <div className="shrink-0 text-right">
          {row.unlimited ? (
            <span className="inline-flex items-center gap-1 rounded-md bg-violet-500/15 border border-violet-500/20 px-1.5 py-0.5 font-data text-[11px] font-medium text-violet-600 dark:text-violet-300">
              <InfinityIcon className="size-3" />
              不限
            </span>
          ) : exhausted ? (
            <span className="rounded-md bg-destructive/15 border border-destructive/20 px-1.5 py-0.5 font-data text-[11px] font-medium text-destructive">
              已用完
            </span>
          ) : (
            <span className="font-data tabular-nums text-[12px] font-semibold text-foreground">
              剩 {row.remaining}
            </span>
          )}
        </div>
      </div>
      {!row.unlimited ? (
        <>
          <div className="mt-2 h-1 w-full overflow-hidden rounded-full bg-secondary">
            <span
              className={cn(
                "block h-full rounded-full transition-all",
                exhausted ? "bg-rose-500" : percent >= 80 ? "bg-amber-500" : "bg-emerald-500",
              )}
              style={{ width: `${Math.min(100, percent)}%` }}
            />
          </div>
          <div className="mt-1 font-data tabular-nums text-[11px] text-muted-foreground">
            已用 {row.used} / {row.quota}
          </div>
        </>
      ) : (
        <div className="mt-1.5 font-data tabular-nums text-[11px] text-muted-foreground">
          已用 {row.used}
        </div>
      )}
    </div>
  );
}
