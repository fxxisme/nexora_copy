"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { ArrowLeft, BookOpen, Check, LoaderCircle, RefreshCw, Search } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  fetchPromptLibrary,
  refreshPromptSources,
  updatePromptSource,
  type PromptLibraryItem,
  type PromptLibraryView,
} from "@/lib/api";

type PromptLibraryDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  isAdmin: boolean;
  onSelect: (item: PromptLibraryItem) => void;
};

function promptPreviewURL(item: PromptLibraryItem, brokenURLs: Set<string>) {
  return [item.preview, ...(item.reference_image_urls ?? [])].find((url) => url && !brokenURLs.has(url)) || "";
}

export function PromptLibraryDialog({ open, onOpenChange, isAdmin, onSelect }: PromptLibraryDialogProps) {
  const [library, setLibrary] = useState<PromptLibraryView | null>(null);
  const [loading, setLoading] = useState(false);
  const [busySource, setBusySource] = useState<string | null>(null);
  const [tab, setTab] = useState<"prompts" | "sources">("prompts");
  const [query, setQuery] = useState("");
  const [source, setSource] = useState("");
  const [category, setCategory] = useState("");
  const [mode, setMode] = useState("");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [mobileDetail, setMobileDetail] = useState(false);
  const [visibleCount, setVisibleCount] = useState(60);
  const [brokenPreviewURLs, setBrokenPreviewURLs] = useState<Set<string>>(() => new Set());
  const requestVersion = useRef(0);

  useEffect(() => {
    const version = ++requestVersion.current;
    if (!open) {
      setMobileDetail(false);
      setLoading(false);
      return;
    }
    setLoading(true);
    void fetchPromptLibrary()
      .then((result) => {
        if (requestVersion.current === version) setLibrary(result);
      })
      .catch((error) => {
        if (requestVersion.current === version) toast.error(error instanceof Error ? error.message : "加载提示词库失败");
      })
      .finally(() => {
        if (requestVersion.current === version) setLoading(false);
      });
    return () => { if (requestVersion.current === version) requestVersion.current++; };
  }, [open]);

  const categories = useMemo(() =>
    [...new Set((library?.items ?? []).filter((item) => !source || item.source_id === source).map((item) => item.category).filter(Boolean))].sort(),
  [library, source]);
  const filtered = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase();
    return (library?.items ?? []).filter((item) => {
      if (source && item.source_id !== source) return false;
      if (category && item.category !== category) return false;
      if (mode && item.image_mode !== mode) return false;
      if (!needle) return true;
      return [item.title, item.description, item.prompt, item.author, ...item.tags]
        .some((value) => value.toLocaleLowerCase().includes(needle));
    });
  }, [library, query, source, category, mode]);
  const selected = filtered.find((item) => item.id === selectedId) ?? filtered[0] ?? null;
  const selectedPreview = selected ? promptPreviewURL(selected, brokenPreviewURLs) : "";
  const visible = filtered.slice(0, visibleCount);

  useEffect(() => {
    if (library && source && !library.sources.some((item) => item.id === source && item.enabled)) {
      setSource("");
      setCategory("");
    } else if (category && !categories.includes(category)) {
      setCategory("");
    }
  }, [library, source, category, categories]);

  const markPreviewBroken = (url: string) => {
    setBrokenPreviewURLs((current) => new Set(current).add(url));
  };

  useEffect(() => {
    setVisibleCount(60);
    setMobileDetail(false);
  }, [query, source, category, mode]);

  const runSourceAction = async (id: string, action: "refresh" | "toggle", enabled?: boolean) => {
    requestVersion.current++;
    setLoading(false);
    setBusySource(id);
    try {
      const next = action === "refresh"
        ? await refreshPromptSources(id || undefined)
        : await updatePromptSource(id, Boolean(enabled));
      requestVersion.current++;
      setLibrary(next);
      const refreshError = id
        ? next.sources.find((item) => item.id === id)?.last_error
        : next.source_error_count > 0 ? next.source_errors[0]?.error || "词源同步失败" : "";
      if (action === "refresh" && refreshError) {
        toast.error(refreshError);
      } else {
        toast.success(action === "refresh" ? "词源已更新" : "词源设置已保存");
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "词源操作失败");
      const version = ++requestVersion.current;
      try {
        const latest = await fetchPromptLibrary();
        if (requestVersion.current === version) setLibrary(latest);
      } catch {
        // The operation error is already visible; the next open reloads the library.
      }
    } finally {
      setBusySource(null);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex h-[min(88dvh,820px)] w-[min(96vw,1040px)] max-w-none flex-col gap-0 overflow-hidden rounded-lg p-0">
        <DialogHeader className="shrink-0 border-b border-border px-5 py-4 sm:px-6">
          <DialogTitle className="flex items-center gap-2 text-base">
            <BookOpen className="size-4" />提示词库
            <span className="text-xs font-normal text-muted-foreground">{library?.prompt_count ?? 0}</span>
          </DialogTitle>
        </DialogHeader>
        <div className="flex shrink-0 items-center gap-1 border-b border-border px-5 sm:px-6">
          <button type="button" onClick={() => setTab("prompts")}
            className={`h-10 border-b-2 px-3 text-sm ${tab === "prompts" ? "border-foreground font-medium" : "border-transparent text-muted-foreground"}`}>
            提示词
          </button>
          {isAdmin ? <button type="button" onClick={() => setTab("sources")}
            className={`h-10 border-b-2 px-3 text-sm ${tab === "sources" ? "border-foreground font-medium" : "border-transparent text-muted-foreground"}`}>
            词源
          </button> : null}
        </div>
        {loading ? <div className="flex flex-1 items-center justify-center"><LoaderCircle className="size-5 animate-spin" /></div> : null}
        {!loading && tab === "prompts" ? (
          <>
            <div className={`${mobileDetail ? "hidden sm:grid" : "grid"} shrink-0 gap-2 border-b border-border p-4 sm:grid-cols-[minmax(180px,1fr)_repeat(3,minmax(0,150px))] sm:px-6`}>
              <div className="relative">
                <Search className="pointer-events-none absolute left-3 top-2.5 size-4 text-muted-foreground" />
                <Input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索标题、内容、标签" className="h-9 pl-9" />
              </div>
              <select aria-label="词源" value={source} onChange={(event) => { setSource(event.target.value); setCategory(""); }} className="h-9 min-w-0 rounded-md border border-border bg-background px-2 text-sm">
                <option value="">全部词源</option>
                {library?.sources.filter((item) => item.enabled).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
              </select>
              <select aria-label="分类" value={category} onChange={(event) => setCategory(event.target.value)} className="h-9 min-w-0 rounded-md border border-border bg-background px-2 text-sm">
                <option value="">全部分类</option>
                {categories.map((item) => <option key={item} value={item}>{item}</option>)}
              </select>
              <select aria-label="模式" value={mode} onChange={(event) => setMode(event.target.value)} className="h-9 min-w-0 rounded-md border border-border bg-background px-2 text-sm">
                <option value="">全部模式</option><option value="generate">生图</option><option value="edit">改图</option>
              </select>
            </div>
            <div className="grid min-h-0 flex-1 sm:grid-cols-[minmax(250px,40%)_minmax(0,1fr)]">
              <div className={`${mobileDetail ? "hidden sm:block" : "block"} min-h-0 overflow-y-auto border-b border-border sm:border-b-0 sm:border-r`}
                onScroll={(event) => {
                  const list = event.currentTarget;
                  if (list.scrollTop + list.clientHeight >= list.scrollHeight - 200) {
                    setVisibleCount((count) => Math.min(count + 60, filtered.length));
                  }
                }}>
                {filtered.length === 0 ? <p className="p-6 text-sm text-muted-foreground">没有匹配的提示词</p> : null}
                {visible.map((item) => {
                  const preview = promptPreviewURL(item, brokenPreviewURLs);
                  return (
                    <button key={item.id} type="button" onClick={() => { setSelectedId(item.id); setMobileDetail(true); }}
                      className={`flex w-full gap-3 border-b border-border/60 p-3 text-left transition hover:bg-muted/60 sm:px-5 ${selected?.id === item.id ? "bg-muted" : ""}`}>
                      {preview ? <img src={preview} alt="" loading="lazy" onError={() => markPreviewBroken(preview)} className="size-14 shrink-0 rounded object-cover" /> : <span className="flex size-14 shrink-0 items-center justify-center rounded bg-muted"><BookOpen className="size-4 text-muted-foreground" /></span>}
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-sm font-medium">{item.title}</span>
                        <span className="mt-1 line-clamp-2 break-words text-xs leading-4 text-muted-foreground">{item.description || item.prompt}</span>
                        <span className="mt-1 block truncate text-[11px] text-muted-foreground">{item.category || item.source_name}{item.image_mode === "edit" ? " · 改图" : ""}</span>
                      </span>
                    </button>
                  );
                })}
                {visible.length < filtered.length ? <div className="p-3 text-center"><Button variant="ghost" size="sm" onClick={() => setVisibleCount((count) => Math.min(count + 60, filtered.length))}>加载更多</Button></div> : null}
              </div>
              <div className={`${mobileDetail ? "block" : "hidden sm:block"} min-h-0 overflow-y-auto p-5`}>
                {selected ? <div className="space-y-4">
                  <button type="button" className="flex items-center gap-2 text-sm text-muted-foreground sm:hidden" onClick={() => setMobileDetail(false)} aria-label="返回提示词列表"><ArrowLeft className="size-4" />返回列表</button>
                  <div className="flex gap-4">
                    {selectedPreview ? <img src={selectedPreview} alt="" onError={() => markPreviewBroken(selectedPreview)} className="size-24 shrink-0 rounded object-cover" /> : null}
                    <div className="min-w-0"><h3 className="text-base font-semibold">{selected.title}</h3><p className="mt-1 text-xs text-muted-foreground">{selected.source_name}{selected.category ? ` · ${selected.category}` : ""}</p><p className="mt-2 text-sm text-muted-foreground">{selected.description}</p></div>
                  </div>
                  <p className="whitespace-pre-wrap break-words text-sm leading-6">{selected.prompt}</p>
                </div> : null}
              </div>
            </div>
            <div className="flex shrink-0 items-center justify-between gap-2 border-t border-border px-4 py-3 sm:px-6">
              <span className="text-xs text-muted-foreground">{filtered.length} 条{selected?.image_mode === "edit" ? " · 选用后上传参考图" : ""}</span>
              <Button disabled={!selected} onClick={() => { if (selected) { onSelect(selected); onOpenChange(false); } }}>
                <Check className="size-4" />选用
              </Button>
            </div>
          </>
        ) : null}
        {!loading && tab === "sources" && isAdmin ? (
          <div className="min-h-0 flex-1 overflow-y-auto px-5 py-3 sm:px-6">
            <div className="flex items-center justify-between gap-3 py-3">
              <span className="text-sm text-muted-foreground">{library?.sync_summary.message}</span>
              <Button variant="outline" size="sm" disabled={busySource !== null} onClick={() => void runSourceAction("", "refresh")}><RefreshCw className={`size-4 ${busySource === "" ? "animate-spin" : ""}`} />刷新全部</Button>
            </div>
            {library?.sources.map((item) => <div key={item.id} className="flex flex-wrap items-center gap-3 border-t border-border py-4">
              <div className="min-w-[180px] flex-1"><div className="text-sm font-medium">{item.name}</div><div className="mt-0.5 text-xs text-muted-foreground">{item.prompt_count} 条 · {item.sync_label}{item.last_error ? ` · ${item.last_error}` : ""}</div></div>
              <label className="flex items-center gap-2 text-xs"><input type="checkbox" checked={item.enabled} disabled={busySource !== null} onChange={(event) => void runSourceAction(item.id, "toggle", event.target.checked)} />启用</label>
              <Button variant="outline" size="sm" disabled={busySource !== null} onClick={() => void runSourceAction(item.id, "refresh")} title={`刷新 ${item.name}`}><RefreshCw className={`size-4 ${busySource === item.id ? "animate-spin" : ""}`} /></Button>
            </div>)}
          </div>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
