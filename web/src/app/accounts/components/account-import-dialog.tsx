"use client";

import { useRouter } from "next/navigation";
import { useRef, useState, type ChangeEvent } from "react";
import {
  ArrowLeft,
  Bot,
  Copy,
  ExternalLink,
  FileJson,
  FileText,
  Files,
  KeyRound,
  LoaderCircle,
  LogIn,
  ServerCog,
  Upload,
} from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import {
  createAccounts,
  finishOAuthLogin,
  startOAuthLogin,
  type Account,
  type AccountImportRecord,
  type OAuthLoginStartResponse,
} from "@/lib/api";
import { cn } from "@/lib/utils";

type ImportMethod = "menu" | "token" | "session" | "cpa" | "oauth";
type AccountSourceType = "web" | "codex";

type AccountImportDialogProps = {
  disabled?: boolean;
  onImported: (items: Account[]) => void;
};

type PendingCpaImport = {
  tokens: string[];
  records: AccountImportRecord[];
  parsedFileCount: number;
  parsedAccountCount: number;
  codexCount: number;
  webCount: number;
  errorCount: number;
};

const sessionUrl = "https://chatgpt.com/api/auth/session";

function splitTokens(value: string) {
  return value
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function getSessionAccessToken(value: unknown) {
  const token = (value as { accessToken?: unknown })?.accessToken;
  return typeof token === "string" ? token.trim() : "";
}

function getCpaAccessToken(value: unknown) {
  const payload = value as { access_token?: unknown; accessToken?: unknown };
  const token = payload?.access_token ?? payload?.accessToken;
  return typeof token === "string" ? token.trim() : "";
}

function extractAccountsFromValue(
  parsed: unknown,
  sourceFileName?: string,
): { tokens: string[]; records: AccountImportRecord[] } {
  const items = Array.isArray(parsed) ? parsed : [parsed];
  const tokens: string[] = [];
  const records: AccountImportRecord[] = [];

  for (const item of items) {
    if (item && typeof item === "object") {
      const token = getCpaAccessToken(item);
      if (token) {
        tokens.push(token);
        records.push({
          ...(item as Record<string, unknown>),
          ...(sourceFileName ? { cpa_file_name: sourceFileName } : {}),
        } as AccountImportRecord);
      }
    }
  }

  return { tokens, records };
}

function readFileAsText(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(typeof reader.result === "string" ? reader.result : "");
    reader.onerror = () => reject(reader.error ?? new Error(`读取文件失败: ${file.name}`));
    reader.readAsText(file);
  });
}

function MethodCard({
  title,
  description,
  icon: Icon,
  onClick,
}: {
  title: string;
  description: string;
  icon: typeof KeyRound;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="w-full rounded-2xl border border-border bg-card p-0 text-left transition hover:border-foreground/20 hover:bg-accent"
    >
      <Card className="rounded-2xl border-0 bg-transparent shadow-none">
        <CardContent className="flex items-start gap-4 p-4">
          <div className="rounded-xl bg-secondary p-3 text-foreground">
            <Icon className="size-5" />
          </div>
          <div className="space-y-1">
            <div className="text-sm font-semibold text-foreground">{title}</div>
            <div className="text-sm leading-6 text-muted-foreground">{description}</div>
          </div>
        </CardContent>
      </Card>
    </button>
  );
}

export function AccountImportDialog({ disabled, onImported }: AccountImportDialogProps) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [method, setMethod] = useState<ImportMethod>("menu");
  const [tokenInput, setTokenInput] = useState("");
  const [sessionInput, setSessionInput] = useState("");
  const [cpaJsonInput, setCpaJsonInput] = useState("");
  const [sourceType, setSourceType] = useState<AccountSourceType>("web");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [pendingCpaImport, setPendingCpaImport] = useState<PendingCpaImport | null>(null);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [oauthEmailHint, setOauthEmailHint] = useState("");
  const [oauthSession, setOauthSession] = useState<OAuthLoginStartResponse | null>(null);
  const [oauthCallbackInput, setOauthCallbackInput] = useState("");
  const [oauthStarting, setOauthStarting] = useState(false);

  const txtInputRef = useRef<HTMLInputElement | null>(null);
  const cpaInputRef = useRef<HTMLInputElement | null>(null);

  const resetState = () => {
    setMethod("menu");
    setTokenInput("");
    setSessionInput("");
    setCpaJsonInput("");
    setSourceType("web");
    setPendingCpaImport(null);
    setConfirmOpen(false);
    setOauthEmailHint("");
    setOauthSession(null);
    setOauthCallbackInput("");
    setOauthStarting(false);
  };

  const handleOpenChange = (nextOpen: boolean) => {
    setOpen(nextOpen);
    if (!nextOpen) {
      resetState();
    }
  };

  const submitTokens = async (
    tokens: string[],
    successText?: string,
    overrideSourceType?: AccountSourceType,
    accountRecords: AccountImportRecord[] = [],
  ) => {
    const normalizedTokens = tokens.map((item) => item.trim()).filter(Boolean);
    const normalizedRecords = accountRecords.filter((item) => getCpaAccessToken(item));

    if (normalizedTokens.length === 0 && normalizedRecords.length === 0) {
      toast.error("请先提供至少一个可用 Token");
      return;
    }

    setIsSubmitting(true);
    try {
      const data = await createAccounts(normalizedTokens, overrideSourceType ?? sourceType, normalizedRecords);
      onImported(data.items);
      setOpen(false);
      resetState();

      const signupCount = data.signup_tokens ?? 0;
      const summary = `${successText ?? "导入完成"}，新增 ${data.added ?? 0} 个，资料刷新 ${data.refreshed ?? 0} 个`;
      const signupNote = signupCount > 0 ? `，其中 ${signupCount} 个带注册阶段标记` : "";
      if ((data.errors?.length ?? 0) > 0) {
        const firstError = data.errors?.[0]?.error;
        toast.error(
          `${summary}${signupNote}，资料刷新失败 ${data.errors?.length ?? 0} 个${firstError ? `，首个错误：${firstError}` : ""}`,
        );
      } else if (signupCount > 0) {
        toast.warning(`${summary}${signupNote}`);
      } else {
        toast.success(
          `${summary}，跳过 ${data.skipped ?? 0} 个重复项`,
        );
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : "导入账户失败";
      toast.error(message);
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleStartOAuth = async () => {
    setOauthStarting(true);
    try {
      const data = await startOAuthLogin(oauthEmailHint.trim());
      setOauthSession(data);
      setOauthCallbackInput("");
      if (typeof window !== "undefined") {
        window.open(data.authorize_url, "_blank", "noopener,noreferrer");
      }
      toast.success("已打开 OpenAI 授权页面，请在登录后复制 callback URL 回来");
    } catch (error) {
      const message = error instanceof Error ? error.message : "OAuth 起始失败";
      toast.error(message);
    } finally {
      setOauthStarting(false);
    }
  };

  const handleFinishOAuth = async () => {
    if (!oauthSession) {
      toast.error("请先点击\"打开授权页面\"获取 session");
      return;
    }
    const trimmed = oauthCallbackInput.trim();
    if (!trimmed) {
      toast.error("请粘贴 callback URL 或 code");
      return;
    }

    setIsSubmitting(true);
    try {
      const data = await finishOAuthLogin(oauthSession.session_id, trimmed);
      onImported(data.items);
      setOpen(false);
      resetState();

      if ((data.errors?.length ?? 0) > 0) {
        const firstError = data.errors?.[0]?.error;
        toast.error(
          `OAuth 登录完成，新增 ${data.added ?? 0} 个，已刷新 ${data.refreshed ?? 0} 个，失败 ${data.errors?.length ?? 0} 个${firstError ? `，首个错误：${firstError}` : ""}`,
        );
      } else {
        toast.success(
          `OAuth 登录完成，新增 ${data.added ?? 0} 个，跳过 ${data.skipped ?? 0} 个重复项，已自动刷新账号信息`,
        );
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : "OAuth 换 token 失败";
      toast.error(message);
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleCopyAuthorizeUrl = async () => {
    if (!oauthSession) {
      return;
    }
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(oauthSession.authorize_url);
        toast.success("授权 URL 已复制到剪贴板");
      } else {
        toast.error("当前环境不支持自动复制，请手动选择并复制");
      }
    } catch {
      toast.error("复制失败，请手动选择并复制");
    }
  };

  const handleImportTokenText = async () => {
    await submitTokens(splitTokens(tokenInput), "Access Token 导入完成");
  };

  const handleTxtSelected = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    event.target.value = "";

    if (!file) {
      return;
    }

    try {
      const content = await readFileAsText(file);
      const tokens = splitTokens(content);

      if (tokens.length === 0) {
        toast.error("TXT 文件里没有读取到有效 Token");
        return;
      }

      setTokenInput((prev) => (prev.trim() ? `${prev.trim()}\n${tokens.join("\n")}` : tokens.join("\n")));
      toast.success(`已从 ${file.name} 追加 ${tokens.length} 个 Token`);
    } catch (error) {
      const message = error instanceof Error ? error.message : "读取 TXT 文件失败";
      toast.error(message);
    }
  };

  const handleImportSessionJson = async () => {
    const raw = sessionInput.trim();
    if (!raw) {
      toast.error("请先粘贴 Session JSON");
      return;
    }

    try {
      const parsed = JSON.parse(raw) as unknown;
      const token = getSessionAccessToken(parsed);

      if (!token) {
        toast.error("未在 JSON 中找到 accessToken");
        return;
      }

      await submitTokens([token], "Session JSON 导入完成", "web");
    } catch (error) {
      const message = error instanceof Error ? error.message : "Session JSON 解析失败";
      toast.error(message);
    }
  };

  const handleCpaSelected = async (event: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(event.target.files ?? []);
    event.target.value = "";

    if (files.length === 0) {
      return;
    }

    try {
      let parsedFileCount = 0;
      let errorCount = 0;
      const allTokens: string[] = [];
      const allRecords: AccountImportRecord[] = [];

      for (const file of files) {
        try {
          const raw = await readFileAsText(file);
          const parsed = JSON.parse(raw) as unknown;
          const { tokens, records } = extractAccountsFromValue(parsed, file.name);
          if (records.length > 0) {
            allTokens.push(...tokens);
            allRecords.push(...records);
            parsedFileCount++;
          } else {
            errorCount++;
          }
        } catch {
          errorCount++;
        }
      }

      if (allRecords.length === 0) {
        toast.error("所选 JSON 文件里没有读取到可用 access_token");
        return;
      }

      const codexCount = allRecords.filter(
        (r) => String(r.type || r.source_type || "").toLowerCase() === "codex",
      ).length;
      const webCount = allRecords.length - codexCount;

      setPendingCpaImport({
        tokens: allTokens,
        records: allRecords,
        parsedFileCount,
        parsedAccountCount: allRecords.length,
        codexCount,
        webCount,
        errorCount,
      });
      setConfirmOpen(true);
    } catch (error) {
      const message = error instanceof Error ? error.message : "读取 JSON 文件失败";
      toast.error(message);
    }
  };

  const handleParsePastedCpaJson = () => {
    const raw = cpaJsonInput.trim();
    if (!raw) {
      toast.error("请先粘贴 JSON 内容");
      return;
    }

    try {
      const parsed = JSON.parse(raw) as unknown;
      const { tokens, records } = extractAccountsFromValue(parsed, "pasted-json");

      if (records.length === 0) {
        toast.error("未在粘贴的内容中识别到有效的 access_token");
        return;
      }

      const codexCount = records.filter(
        (r) => String(r.type || r.source_type || "").toLowerCase() === "codex",
      ).length;
      const webCount = records.length - codexCount;

      setPendingCpaImport({
        tokens,
        records,
        parsedFileCount: 1,
        parsedAccountCount: records.length,
        codexCount,
        webCount,
        errorCount: 0,
      });
      setConfirmOpen(true);
    } catch {
      toast.error("JSON 格式错误，请检查语法");
    }
  };

  const renderMethodBody = () => {
    if (method === "oauth") {
      return (
        <div className="space-y-4">
          <button
            type="button"
            onClick={() => setMethod("menu")}
            className="inline-flex items-center gap-1 text-sm text-muted-foreground transition hover:text-foreground"
          >
            <ArrowLeft className="size-4" />
            返回导入方式
          </button>
          <div className="rounded-2xl border border-border bg-muted/40 p-4 text-sm leading-6 text-muted-foreground space-y-2">
            <div className="font-medium text-foreground">操作步骤</div>
            <ol className="list-decimal pl-5 space-y-1">
              <li>（可选）填写你 ChatGPT 账号的邮箱，登录页会预填。</li>
              <li>点击下方"打开授权页面"，在新标签里登录自己的 ChatGPT 账号。</li>
              <li>登录完成后浏览器会跳到 <code className="rounded bg-muted px-1 font-mono text-xs text-foreground">platform.openai.com/auth/callback?code=...</code>。立刻从地址栏复制整段 URL（或开 F12 在 Network 里抓到 callback 那一行，右键 Copy → Copy URL）。</li>
              <li>把 callback URL 粘到下面输入框，点"完成导入"。</li>
            </ol>
          </div>
          <div className="space-y-2">
            <label className="text-sm font-medium text-foreground">邮箱（可选预填）</label>
            <input
              type="email"
              placeholder="you@example.com"
              value={oauthEmailHint}
              onChange={(event) => setOauthEmailHint(event.target.value)}
              disabled={Boolean(oauthSession) || oauthStarting}
              className="w-full rounded-xl border border-border bg-card px-3 py-2 text-sm text-foreground outline-none focus:border-primary"
            />
          </div>
          {!oauthSession ? (
            <Button
              type="button"
              className="h-10 rounded-xl bg-primary px-5 text-primary-foreground hover:bg-primary/90"
              onClick={() => void handleStartOAuth()}
              disabled={oauthStarting}
            >
              {oauthStarting ? <LoaderCircle className="size-4 animate-spin" /> : <ExternalLink className="size-4" />}
              打开授权页面
            </Button>
          ) : (
            <div className="space-y-3">
              <div className="rounded-2xl border border-border bg-muted/30 p-3 text-xs leading-6 text-foreground break-all font-mono">
                {oauthSession.authorize_url}
              </div>
              <div className="flex flex-wrap gap-2">
                <Button
                  type="button"
                  variant="outline"
                  className="rounded-xl border-border bg-card"
                  onClick={() => void handleCopyAuthorizeUrl()}
                >
                  <Copy className="size-4" />
                  复制授权 URL
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  className="rounded-xl border-border bg-card"
                  onClick={() => window.open(oauthSession.authorize_url, "_blank", "noopener,noreferrer")}
                >
                  <ExternalLink className="size-4" />
                  再次打开
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  className="rounded-xl border-border bg-card"
                  onClick={() => {
                    setOauthSession(null);
                    setOauthCallbackInput("");
                  }}
                >
                  重新生成
                </Button>
              </div>
              <div className="space-y-2">
                <label className="text-sm font-medium text-foreground">粘贴 callback URL（或仅 code）</label>
                <Textarea
                  placeholder="https://platform.openai.com/auth/callback?code=...&state=..."
                  value={oauthCallbackInput}
                  onChange={(event) => setOauthCallbackInput(event.target.value)}
                  className="min-h-24 resize-none rounded-xl border-border font-mono text-xs"
                />
              </div>
            </div>
          )}
          <div className="rounded-2xl border border-amber-500/20 bg-amber-500/10 p-4 text-sm leading-6 text-amber-600 dark:text-amber-400">
            <div className="font-medium">注意</div>
            <div>
              授权码（code）只能使用一次。如果浏览器的 callback 页加载完成、显示了 OpenAI 的错误页，那 code 大概率已经被消耗，
              请点击"重新生成"再走一次。整个流程在 10 分钟内完成即可。
            </div>
          </div>
        </div>
      );
    }

    if (method === "token") {
      const tokenCount = splitTokens(tokenInput).length;

      return (
        <div className="space-y-4">
          <div className="flex items-center justify-between">
            <button
              type="button"
              onClick={() => setMethod("menu")}
              className="inline-flex items-center gap-1 text-sm text-muted-foreground transition hover:text-foreground"
            >
              <ArrowLeft className="size-4" />
              返回导入方式
            </button>
            <span className="text-xs text-muted-foreground">当前识别 {tokenCount} 个 Token</span>
          </div>
          <div className="space-y-2">
            <label className="text-sm font-medium text-foreground">Access Token 列表</label>
            <Textarea
              placeholder="每行一个 Access Token..."
              value={tokenInput}
              onChange={(event) => setTokenInput(event.target.value)}
              className="min-h-56 resize-none rounded-xl border-border"
            />
          </div>
          <div className="grid grid-cols-2 gap-2">
            {([
              { value: "web", label: "Web", desc: "普通 ChatGPT Web 画图" },
              { value: "codex", label: "Codex", desc: "Codex Responses 高分辨率" },
            ] as const).map((item) => {
              const active = sourceType === item.value;
              return (
                <button
                  key={item.value}
                  type="button"
                  onClick={() => setSourceType(item.value)}
                  className={cn(
                    "rounded-xl border p-3 text-left transition",
                    active
                      ? "border-primary bg-primary text-primary-foreground"
                      : "border-border bg-card text-foreground hover:bg-accent",
                  )}
                >
                  <div className="flex items-center gap-2 text-sm font-semibold">
                    {item.value === "codex" ? <Bot className="size-4" /> : <KeyRound className="size-4" />}
                    {item.label}
                  </div>
                  <div className={cn("mt-1 text-xs", active ? "text-white/65" : "text-muted-foreground")}>
                    {item.desc}
                  </div>
                </button>
              );
            })}
          </div>
          <div className="rounded-2xl border border-dashed border-border bg-muted/40 p-4">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
              <div className="space-y-1">
                <div className="text-sm font-medium text-foreground">从 TXT 文件导入</div>
                <div className="text-sm leading-6 text-muted-foreground">支持 `.txt`，文件内容也是一行一个 Token。</div>
              </div>
              <Button
                type="button"
                variant="outline"
                className="rounded-xl border-border bg-card"
                onClick={() => txtInputRef.current?.click()}
                disabled={isSubmitting}
              >
                <FileText className="size-4" />
                选择 TXT
              </Button>
            </div>
          </div>
          <input
            ref={txtInputRef}
            type="file"
            accept=".txt,text/plain"
            className="hidden"
            onChange={(event) => void handleTxtSelected(event)}
          />
        </div>
      );
    }

    if (method === "session") {
      return (
        <div className="space-y-4">
          <button
            type="button"
            onClick={() => setMethod("menu")}
            className="inline-flex items-center gap-1 text-sm text-muted-foreground transition hover:text-foreground"
          >
            <ArrowLeft className="size-4" />
            返回导入方式
          </button>
          <div className="rounded-2xl border border-border bg-muted/40 p-4 text-sm leading-6 text-muted-foreground">
            打开
            {" "}
            <a
              href={sessionUrl}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 font-medium text-foreground underline underline-offset-4"
            >
              {sessionUrl}
              <ExternalLink className="size-3.5" />
            </a>
            ，复制页面返回的完整 JSON，系统会自动提取其中的 `accessToken` 导入。
          </div>
          <div className="rounded-2xl border border-amber-200 bg-amber-50 p-4 text-sm leading-6 text-amber-900">
            <div className="font-medium">风险提示</div>
            <div>
              不要使用自己的大号，尽量使用不常用的小号进行导入，避免出现封号风险。本项目不承担任何封号风险责任。
            </div>
          </div>
          <div className="space-y-2">
            <label className="text-sm font-medium text-foreground">Session JSON</label>
            <Textarea
              placeholder='粘贴完整 JSON，例如包含 "accessToken" 的对象...'
              value={sessionInput}
              onChange={(event) => setSessionInput(event.target.value)}
              className="min-h-56 resize-none rounded-xl border-border font-mono text-xs"
            />
          </div>
        </div>
      );
    }

    if (method === "cpa") {
      return (
        <div className="space-y-4">
          <div className="flex items-center justify-between">
            <button
              type="button"
              onClick={() => setMethod("menu")}
              className="inline-flex items-center gap-1 text-sm text-muted-foreground transition hover:text-foreground"
            >
              <ArrowLeft className="size-4" />
              返回导入方式
            </button>
          </div>

          <div className="rounded-2xl border border-dashed border-border bg-muted/40 p-5">
            <div className="space-y-2">
              <div className="text-sm font-medium text-foreground">选择本地 JSON 文件 / 备份文件</div>
              <div className="text-sm leading-6 text-muted-foreground">
                支持导入 <strong>完整账号导出备份（如 chatgpt2api 导出的 JSON 数组）</strong> 或 <strong>多选单个 CPA JSON 文件</strong>。系统将自动完整保留 <code>access_token</code>、<code>refresh_token</code>、<code>id_token</code>、<code>email</code> 等全部凭据与账号类型。
              </div>
            </div>
            <Button
              type="button"
              className="mt-4 rounded-xl bg-primary text-primary-foreground hover:bg-primary/90"
              onClick={() => cpaInputRef.current?.click()}
              disabled={isSubmitting}
            >
              <Files className="size-4" />
              选择 JSON 文件（支持单文件数组或多个文件）
            </Button>
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium text-foreground">或者直接粘贴 JSON 文本</label>
            <Textarea
              placeholder='在此粘贴 JSON 文本（支持单个账号对象，或 [...] 完整账号数组）...'
              value={cpaJsonInput}
              onChange={(event) => setCpaJsonInput(event.target.value)}
              className="min-h-32 resize-none rounded-xl border-border font-mono text-xs"
            />
            {cpaJsonInput.trim() ? (
              <Button
                type="button"
                variant="outline"
                className="mt-2 rounded-xl border-border bg-card"
                onClick={handleParsePastedCpaJson}
              >
                解析粘贴的 JSON
              </Button>
            ) : null}
          </div>

          <input
            ref={cpaInputRef}
            type="file"
            accept=".json,application/json"
            multiple
            className="hidden"
            onChange={(event) => void handleCpaSelected(event)}
          />

          {pendingCpaImport ? (
            <div className="rounded-2xl border border-border bg-card p-4 text-sm leading-6 text-muted-foreground">
              已就绪：共识别到 <strong className="text-foreground">{pendingCpaImport.parsedAccountCount}</strong> 个账号（来自 {pendingCpaImport.parsedFileCount} 个来源，其中 Codex 账号 {pendingCpaImport.codexCount} 个，Web 账号 {pendingCpaImport.webCount} 个）
              {pendingCpaImport.errorCount > 0 ? `，另有 ${pendingCpaImport.errorCount} 个文件未能读取` : ""}。
            </div>
          ) : null}
        </div>
      );
    }

    return (
      <div className="space-y-3">
        <MethodCard
          title="OAuth 登录已有账号（带自动刷新）"
          description="用浏览器登录自己的 ChatGPT 账号，回填 callback URL 即可拿到 refresh_token，后台会自动续期。"
          icon={LogIn}
          onClick={() => setMethod("oauth")}
        />
        <MethodCard
          title="导入 Access Token"
          description="支持直接粘贴，一行一个；也支持从 TXT 文件读取，一行一个。"
          icon={KeyRound}
          onClick={() => setMethod("token")}
        />
        <MethodCard
          title="导入 Session JSON"
          description="从 chatgpt.com 的 session 接口复制完整 JSON，自动提取 accessToken。"
          icon={FileJson}
          onClick={() => setMethod("session")}
        />
        <MethodCard
          title="导入 JSON 账号备份 / CPA 文件"
          description="支持导入 chatgpt2api 导出的完整备份（JSON 数组）或多个 CPA 文件，保留全部 token 与凭据元数据。"
          icon={Files}
          onClick={() => setMethod("cpa")}
        />
        <MethodCard
          title="从远程 CPA 服务器导入"
          description="前往设置页面配置远程 CPA 服务器后再执行导入。"
          icon={Files}
          onClick={() => {
            setOpen(false);
            resetState();
            router.push("/settings");
          }}
        />
        <MethodCard
          title="从 Sub2API 服务器导入"
          description="前往设置页面配置 Sub2API 服务器，再选择其中的 OpenAI 账号导入。"
          icon={ServerCog}
          onClick={() => {
            setOpen(false);
            resetState();
            router.push("/settings");
          }}
        />
      </div>
    );
  };

  const footerDisabled = disabled || isSubmitting;

  return (
    <>
      <Dialog open={open} onOpenChange={handleOpenChange}>
        <Button
          onClick={() => setOpen(true)}
          disabled={disabled}
          className="h-10 rounded-xl bg-primary px-5 text-primary-foreground hover:bg-primary/90"
        >
          <Upload className="size-4" />
          导入账号
        </Button>
        <DialogContent className="flex max-h-[85vh] w-[min(92vw,560px)] flex-col rounded-2xl p-6">
          <DialogHeader className="gap-2">
            <DialogTitle>导入账号</DialogTitle>
            <DialogDescription className="text-sm leading-6">
              请选择要导入账号的方式，支持直接输入或解析外部导出的凭据文件。
            </DialogDescription>
          </DialogHeader>

          <div className="flex-1 overflow-y-auto pr-1">
            {renderMethodBody()}
          </div>

          <DialogFooter className="pt-2">
            <Button
              variant="secondary"
              className="h-10 rounded-xl bg-secondary px-5 text-secondary-foreground hover:bg-secondary/80"
              onClick={() => {
                setOpen(false);
                resetState();
              }}
              disabled={isSubmitting}
            >
              取消
            </Button>
            {method === "oauth" ? (
              <Button
                className={cn(
                  "h-10 rounded-xl bg-primary px-5 text-primary-foreground hover:bg-primary/90",
                  !oauthSession ? "hidden" : "",
                )}
                onClick={() => void handleFinishOAuth()}
                disabled={footerDisabled || !oauthSession || !oauthCallbackInput.trim()}
              >
                {isSubmitting ? <LoaderCircle className="size-4 animate-spin" /> : null}
                完成导入
              </Button>
            ) : null}
            {method === "token" ? (
              <Button
                className="h-10 rounded-xl bg-primary px-5 text-primary-foreground hover:bg-primary/90"
                onClick={() => void handleImportTokenText()}
                disabled={footerDisabled || splitTokens(tokenInput).length === 0}
              >
                {isSubmitting ? <LoaderCircle className="size-4 animate-spin" /> : null}
                导入 Token
              </Button>
            ) : null}
            {method === "session" ? (
              <Button
                className="h-10 rounded-xl bg-primary px-5 text-primary-foreground hover:bg-primary/90"
                onClick={() => void handleImportSessionJson()}
                disabled={footerDisabled}
              >
                {isSubmitting ? <LoaderCircle className="size-4 animate-spin" /> : null}
                导入 JSON
              </Button>
            ) : null}
            {method === "cpa" ? (
              <Button
                className={cn(
                  "h-10 rounded-xl bg-primary px-5 text-primary-foreground hover:bg-primary/90",
                  !pendingCpaImport ? "hidden" : "",
                )}
                onClick={() => setConfirmOpen(true)}
                disabled={footerDisabled || !pendingCpaImport}
              >
                查看导入确认
              </Button>
            ) : null}
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent className="rounded-2xl p-6">
          <DialogHeader className="gap-2">
            <DialogTitle>确认导入账号数据</DialogTitle>
            <DialogDescription className="text-sm leading-6">
              {pendingCpaImport
                ? `共识别到 ${pendingCpaImport.parsedAccountCount} 个账号（其中 Codex 账号 ${pendingCpaImport.codexCount} 个，Web 账号 ${pendingCpaImport.webCount} 个），全部凭据（refresh_token、id_token 等）均已就绪，是否确认导入？`
                : "尚未读取到可导入的账号。"}
              {pendingCpaImport?.errorCount
                ? `另有 ${pendingCpaImport.errorCount} 个文件未能读取。`
                : ""}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter className="pt-2">
            <Button
              variant="secondary"
              className="h-10 rounded-xl bg-secondary px-5 text-secondary-foreground hover:bg-secondary/80"
              onClick={() => setConfirmOpen(false)}
              disabled={isSubmitting}
            >
              返回
            </Button>
            <Button
              className="h-10 rounded-xl bg-primary px-5 text-primary-foreground hover:bg-primary/90"
              onClick={() =>
                void submitTokens(
                  pendingCpaImport?.tokens ?? [],
                  "JSON 账号数据导入完成",
                  undefined,
                  pendingCpaImport?.records ?? [],
                )
              }
              disabled={isSubmitting || !pendingCpaImport}
            >
              {isSubmitting ? <LoaderCircle className="size-4 animate-spin" /> : null}
              确认导入 ({pendingCpaImport?.parsedAccountCount ?? 0})
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
