"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import {
  Activity, ChevronLeft, ChevronRight, Github, Images, KeyRound, LayoutDashboard,
  LogOut, Menu, MessageSquare, Paintbrush, RefreshCw, Settings2, UserRound,
  UserRoundPlus, X,
} from "lucide-react";

import { QuotaPopover } from "@/components/quota-popover";
import { ThemeToggle } from "@/components/theme-toggle";
import { clearAuthSessionCache, getValidatedAuthSession } from "@/lib/auth-session";
import { cn } from "@/lib/utils";
import { clearStoredAuthSession, type StoredAuthSession } from "@/store/auth";

const adminNavItems = [
  { href: "/", label: "概览中心", icon: LayoutDashboard },
  { href: "/chat", label: "对话", icon: MessageSquare },
  { href: "/image", label: "图像创作", icon: Paintbrush },
  { href: "/accounts", label: "账号管理", icon: UserRound },
  { href: "/register", label: "注册账号", icon: UserRoundPlus },
  { href: "/logs", label: "日志管理", icon: Activity },
  { href: "/image-manager", label: "图片管理", icon: Images },
  { href: "/gallery", label: "画廊", icon: Images },
  { href: "/keys", label: "用户密钥", icon: KeyRound },
  { href: "/settings", label: "系统设置", icon: Settings2 },
];

const userNavItems = [
  { href: "/chat", label: "对话", icon: MessageSquare },
  { href: "/image", label: "图像创作", icon: Paintbrush },
  { href: "/works", label: "我的作品", icon: Images },
  { href: "/gallery", label: "画廊", icon: Images },
];

function normalizePath(value: string) {
  return value.replace(/\/+$/, "") || "/";
}

export function TopNav() {
  const pathname = usePathname();
  const router = useRouter();
  const [session, setSession] = useState<StoredAuthSession | null>();
  const [mobileOpen, setMobileOpen] = useState(false);
  const [isMobile, setIsMobile] = useState(false);
  const [collapsed, setCollapsed] = useState(false);

  useEffect(() => {
    let active = true;
    if (normalizePath(pathname) === "/login") return;
    void getValidatedAuthSession().then((value) => {
      if (active) setSession(value);
    });
    return () => { active = false; };
  }, [pathname]);

  useEffect(() => { setMobileOpen(false); }, [pathname]);
  useEffect(() => {
    const query = window.matchMedia("(max-width: 1023px)");
    const update = () => setIsMobile(query.matches);
    update();
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);
  useEffect(() => {
    document.documentElement.classList.toggle("app-authenticated", !!session && normalizePath(pathname) !== "/login");
    return () => document.documentElement.classList.remove("app-authenticated");
  }, [session, pathname]);
  useEffect(() => {
    document.documentElement.classList.toggle("sidebar-collapsed", collapsed);
    return () => document.documentElement.classList.remove("sidebar-collapsed");
  }, [collapsed]);
  useEffect(() => {
    if (!mobileOpen) return;
    const close = (event: KeyboardEvent) => {
      if (event.key === "Escape") setMobileOpen(false);
    };
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [mobileOpen]);

  if (normalizePath(pathname) === "/login" || !session) return null;

  const navItems = session.role === "admin" ? adminNavItems : userNavItems;
  const current = navItems.find((item) => normalizePath(item.href) === normalizePath(pathname));
  const title = current?.label || "工作台";

  const logout = async () => {
    await clearStoredAuthSession();
    clearAuthSessionCache();
    router.replace("/login");
  };

  return (
    <>
      {mobileOpen && <button type="button" aria-label="关闭导航" className="fixed inset-0 z-40 bg-black/35 lg:hidden" onClick={() => setMobileOpen(false)} />}
      <aside id="app-sidebar" inert={isMobile && !mobileOpen} className={cn(
        "fixed inset-y-0 left-0 z-50 flex w-[240px] flex-col border-r border-border bg-card transition-transform duration-200 lg:translate-x-0",
        mobileOpen ? "translate-x-0" : "-translate-x-full",
        collapsed && "lg:w-[72px]",
      )}>
        <div className="flex h-16 shrink-0 items-center gap-2 px-5">
          <a href="https://github.com/zoreAI/nexora" target="_blank" rel="noreferrer" aria-label="GitHub 仓库" className="shrink-0 text-foreground"><Github className="size-6" /></a>
          <span className={cn("truncate text-[16px] font-semibold text-foreground", collapsed && "lg:hidden")}>Nexora</span>
          <button type="button" className="ml-auto grid size-8 place-items-center rounded-md text-muted-foreground lg:hidden" aria-label="关闭导航" onClick={() => setMobileOpen(false)}><X className="size-5" /></button>
        </div>
        <nav aria-label="主导航" className="min-h-0 flex-1 overflow-y-auto px-3 py-5">
          <p className={cn("px-2 pb-2 text-xs text-muted-foreground", collapsed && "lg:sr-only")}>导航</p>
          <div className="space-y-1">
            {navItems.map((item) => {
              const active = normalizePath(item.href) === normalizePath(pathname);
              const Icon = item.icon;
              return <Link key={item.href} href={item.href} title={collapsed ? item.label : undefined} aria-current={active ? "page" : undefined} className={cn(
                "flex h-11 items-center gap-3 rounded-md px-2 text-sm font-medium transition-colors",
                active ? "bg-secondary text-foreground" : "text-muted-foreground hover:bg-secondary/65 hover:text-foreground",
              )}>
                <span className={cn("grid size-8 shrink-0 place-items-center rounded-full border border-border", active && "border-foreground/20 bg-card")}><Icon className="size-4" /></span>
                <span className={cn("truncate", collapsed && "lg:sr-only")}>{item.label}</span>
              </Link>;
            })}
          </div>
        </nav>
        <div className="flex items-center gap-2 border-t border-border p-3">
          <button type="button" onClick={() => void logout()} title="退出登录" aria-label="退出登录" className={cn("flex h-9 flex-1 items-center justify-center gap-2 rounded-md border border-border text-sm text-muted-foreground hover:bg-secondary", collapsed && "lg:w-9 lg:flex-none")}>
            <LogOut className="size-4" /><span className={cn(collapsed && "lg:sr-only")}>退出登录</span>
          </button>
          <button type="button" onClick={() => setCollapsed(!collapsed)} title={collapsed ? "展开侧栏" : "收起侧栏"} aria-label={collapsed ? "展开侧栏" : "收起侧栏"} className="hidden size-9 shrink-0 place-items-center rounded-md border border-border text-muted-foreground hover:bg-secondary lg:grid">
            {collapsed ? <ChevronRight className="size-4" /> : <ChevronLeft className="size-4" />}
          </button>
        </div>
      </aside>
      <header className={cn("fixed inset-x-0 top-0 z-30 flex h-14 items-center gap-3 border-b border-border bg-card px-4 sm:px-6 lg:left-[240px] lg:h-16", collapsed && "lg:left-[72px]")}>
        <button type="button" aria-label="打开导航" aria-controls="app-sidebar" aria-expanded={mobileOpen} className="grid size-9 shrink-0 place-items-center rounded-md border border-border lg:hidden" onClick={() => setMobileOpen(true)}><Menu className="size-5" /></button>
        <h1 className="min-w-0 flex-1 truncate text-lg font-semibold">{title}</h1>
        {session.role === "user" && <QuotaPopover />}
        <ThemeToggle />
        <button type="button" title="刷新页面" aria-label="刷新页面" onClick={() => window.location.reload()} className="grid size-9 shrink-0 place-items-center rounded-md border border-border text-muted-foreground hover:bg-secondary hover:text-foreground"><RefreshCw className="size-4" /></button>
      </header>
    </>
  );
}
