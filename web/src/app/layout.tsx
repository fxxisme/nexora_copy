import type { Metadata, Viewport } from "next";
import { Inter, JetBrains_Mono } from "next/font/google";
import { Toaster } from "sonner";
import "./globals.css";
import { PageTransition } from "@/components/page-transition";
import { RouteProgress } from "@/components/route-progress";
import { ThemeProvider, ThemeScript } from "@/components/theme-provider";
import { TopNav } from "@/components/top-nav";

const inter = Inter({
  subsets: ["latin"],
  variable: "--font-sans",
  display: "swap",
});

const jetbrainsMono = JetBrains_Mono({
  subsets: ["latin"],
  variable: "--font-mono",
  display: "swap",
});

export const metadata: Metadata = {
  title: "Nexora",
  description: "Nexora account and API management",
};

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  maximumScale: 1,
  userScalable: false,
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="zh-CN" className={`${inter.variable} ${jetbrainsMono.variable}`} suppressHydrationWarning>
      <head>
        <ThemeScript />
      </head>
      <body
        className="antialiased font-sans"
        style={{
          fontFamily:
            'var(--font-sans), "PingFang SC", "Microsoft YaHei", "Helvetica Neue", sans-serif',
        }}
      >
        <ThemeProvider>
          <Toaster position="top-center" richColors offset={48} />
          <RouteProgress />
          <TopNav />
          <main className="app-main h-screen overflow-x-hidden overflow-y-auto px-4 pt-14 pb-2 text-foreground [scrollbar-gutter:stable_both-edges] sm:px-6 lg:px-8">
            <div className="mx-auto box-border flex max-w-[1600px] flex-col pt-[env(safe-area-inset-top)]">
              <PageTransition>{children}</PageTransition>
            </div>
          </main>
        </ThemeProvider>
      </body>
    </html>
  );
}
