"use client";

import { useState } from "react";
import { ListPlus, LoaderCircle } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import { importProxyNodes, type ProxyNodeImportResult } from "@/lib/api";

export function ProxyNodeImportDialog({ existingURLs, onApply }: {
  existingURLs: string[];
  onApply: (nodes: ProxyNodeImportResult["nodes"]) => void;
}) {
  const [open, setOpen] = useState(false);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [invalidItems, setInvalidItems] = useState<ProxyNodeImportResult["invalid_items"]>([]);
  const [summary, setSummary] = useState("");

  const submit = async () => {
    if (!text.trim() || busy) return;
    setBusy(true);
    try {
      const result = await importProxyNodes({ text, existing_urls: existingURLs });
      if (result.nodes.length > 0) onApply(result.nodes);
      const message = [
        result.added_count ? `已添加 ${result.added_count} 个` : "",
        result.duplicate_count ? `跳过重复 ${result.duplicate_count} 个` : "",
        result.invalid_count ? `${result.invalid_count} 行格式错误` : "",
      ].filter(Boolean).join("，") || "没有可添加的节点";
      setSummary(message);
      setInvalidItems(result.invalid_items);
      if (result.invalid_count > 0) {
        setText(result.invalid_items.map((item) => item.raw).join("\n"));
      } else if (result.added_count > 0) {
        toast.success(`${message}；保存代理组后生效`);
        setOpen(false);
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "导入节点失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(value) => {
      setOpen(value);
      if (value) { setText(""); setInvalidItems([]); setSummary(""); }
    }}>
      <DialogTrigger asChild>
        <Button type="button" variant="outline" className="h-8 rounded-md"><ListPlus className="size-4" />批量导入节点</Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90vh] max-w-xl overflow-y-auto rounded-md p-5">
        <DialogHeader><DialogTitle>批量导入代理节点</DialogTitle></DialogHeader>
        <label className="space-y-2 text-sm">
          <span>代理地址与图片并发</span>
          <Textarea
            value={text}
            onChange={(event) => setText(event.target.value)}
            aria-label="每行一个代理地址，可在地址后填写图片并发"
            placeholder={"http://127.0.0.1:7890 30\nsocks5h://127.0.0.1:1080 0"}
            className="min-h-48 resize-y rounded-md font-mono text-xs"
            autoComplete="off"
            spellCheck={false}
          />
        </label>
        <p className="text-xs text-muted-foreground">每行一个地址，后面可填 0 到 10000 的图片并发；省略时为 30，0 表示不限。</p>
        {summary ? <p className="text-sm" role="status">{summary}</p> : null}
        {invalidItems.length > 0 ? (
          <div className="max-h-40 overflow-y-auto border-y border-border" aria-label="格式错误">
            {invalidItems.map((item) => (
              <div key={`${item.line}-${item.raw}`} className="grid grid-cols-[2rem_minmax(0,1fr)] gap-2 border-b border-border py-2 text-xs last:border-b-0">
                <span className="text-right font-mono text-muted-foreground">{item.line}</span>
                <div className="min-w-0"><div className="break-all font-mono">{item.raw}</div><div className="text-destructive">{item.reason}</div></div>
              </div>
            ))}
          </div>
        ) : null}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => setOpen(false)}>取消</Button>
          <Button type="button" onClick={() => void submit()} disabled={!text.trim() || busy}>
            {busy ? <LoaderCircle className="size-4 animate-spin" /> : <ListPlus className="size-4" />}添加到草稿
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
