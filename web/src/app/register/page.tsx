"use client";

import { useEffect } from "react";
import { AlertTriangle, LoaderCircle } from "lucide-react";

import { useAuthGuard } from "@/lib/use-auth-guard";
import { useSettingsStore } from "@/app/settings/store";

import { RegisterCard } from "./components/register-card";

function RegisterPageContent() {
  const loadRegister = useSettingsStore((state) => state.loadRegister);
  const registerConfig = useSettingsStore((state) => state.registerConfig);
  const enabled = useSettingsStore((state) => state.registerConfig?.enabled);
  const running = useSettingsStore((state) => state.registerConfig?.stats?.running ?? 0);

  useEffect(() => {
    void loadRegister();
  }, [loadRegister]);

  useEffect(() => {
    if (!enabled && running === 0) return;
    const timer = window.setInterval(() => void loadRegister(true), 3000);
    return () => window.clearInterval(timer);
  }, [enabled, running, loadRegister]);

  return (
    <main className="mx-auto w-full max-w-7xl space-y-5 px-4 py-6 sm:px-6">
      <h1 className="text-2xl font-semibold text-foreground">自动注册</h1>
      {registerConfig && (!registerConfig.mail || !registerConfig.stats) ? (
        <div className="flex items-start gap-3 border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          <p>当前 Go 进程仍在使用旧版注册接口。重启服务后刷新此页。</p>
        </div>
      ) : (
        <RegisterCard />
      )}
    </main>
  );
}

export default function RegisterPage() {
  const { isCheckingAuth, session } = useAuthGuard(["admin"]);

  if (isCheckingAuth || !session || session.role !== "admin") {
    return (
      <div className="flex min-h-[40vh] items-center justify-center">
        <LoaderCircle className="size-5 animate-spin text-stone-400" />
      </div>
    );
  }

  return <RegisterPageContent />;
}
