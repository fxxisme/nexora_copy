"use client";

import { useState } from "react";
import { LoaderCircle, Pencil, Plus, Trash2, UsersRound } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { deleteAccountGroup, saveAccountGroup, type Account, type AccountGroup, type ProxyGroup } from "@/lib/api";

type ProxyMode = "inherit" | "direct" | "group" | "custom";

export function AccountGroupsDialog({ groups, proxyGroups, accounts, onChanged }: {
  groups: AccountGroup[];
  proxyGroups: ProxyGroup[];
  accounts: Account[];
  onChanged: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [editingID, setEditingID] = useState("");
  const [name, setName] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [proxyMode, setProxyMode] = useState<ProxyMode>("inherit");
  const [proxyGroupID, setProxyGroupID] = useState("");
  const [proxyURL, setProxyURL] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirmDeleteID, setConfirmDeleteID] = useState("");

  const edit = (group?: AccountGroup) => {
    const proxy = group?.proxy ?? "";
    setEditingID(group?.id ?? "");
    setName(group?.name ?? "");
    setEnabled(group?.enabled ?? true);
    setProxyMode(!proxy ? "inherit" : proxy === "direct" ? "direct" : proxy.startsWith("group:") ? "group" : "custom");
    setProxyGroupID(proxy.startsWith("group:") ? proxy.slice(6) : "");
    setProxyURL(proxy && proxy !== "direct" && !proxy.startsWith("group:") ? proxy : "");
    setConfirmDeleteID("");
  };

  const save = async () => {
    if (!name.trim()) return toast.error("请输入账号组名称");
    if (proxyMode === "group" && !proxyGroupID) return toast.error("请选择代理组");
    if (proxyMode === "custom" && !proxyURL.trim()) return toast.error("请输入代理地址");
    const proxy = proxyMode === "inherit" ? "" : proxyMode === "direct" ? "direct" : proxyMode === "group" ? `group:${proxyGroupID}` : proxyURL.trim();
    setBusy(true);
    try {
      await saveAccountGroup({ id: editingID || undefined, name: name.trim(), proxy, enabled });
      await onChanged();
      edit();
      toast.success("账号组已保存");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存账号组失败");
    } finally {
      setBusy(false);
    }
  };

  const remove = async (id: string) => {
    setBusy(true);
    try {
      await deleteAccountGroup(id);
      await onChanged();
      if (editingID === id) edit();
      setConfirmDeleteID("");
      toast.success("账号组已删除");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "删除账号组失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(value) => { setOpen(value); if (value) edit(); }}>
      <DialogTrigger asChild>
        <Button variant="outline" className="h-9 rounded-md"><UsersRound className="size-4" />账号组</Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90vh] max-w-2xl overflow-y-auto rounded-md p-5">
        <DialogHeader><DialogTitle>账号组</DialogTitle></DialogHeader>
        <div className="divide-y divide-border border-y border-border">
          {groups.length === 0 ? <div className="py-6 text-center text-sm text-muted-foreground">暂无账号组</div> : null}
          {groups.map((group) => {
            const count = accounts.filter((account) => account.group_id === group.id).length;
            return <div key={group.id} className="flex items-center gap-3 py-2.5">
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">{group.name}{!group.enabled ? <span className="ml-2 text-xs text-muted-foreground">已停用</span> : null}</div>
                <div className="text-xs text-muted-foreground">{count} 个账号 · {group.proxy === "direct" ? "直连" : group.proxy.startsWith("group:") ? proxyGroups.find((item) => item.id === group.proxy.slice(6))?.name ?? "代理组已移除" : group.proxy ? "自定义代理" : "继承默认出口"}</div>
              </div>
              <Button variant="ghost" size="icon" className="size-8" title="编辑账号组" aria-label={`编辑 ${group.name}`} onClick={() => edit(group)}><Pencil className="size-4" /></Button>
              {confirmDeleteID === group.id ? (
                <Button variant="destructive" size="sm" disabled={busy || count > 0} onClick={() => void remove(group.id)}>确认删除</Button>
              ) : (
                <Button variant="ghost" size="icon" className="size-8" title={count > 0 ? "先移出组内账号" : "删除账号组"} aria-label={`删除 ${group.name}`} disabled={busy || count > 0} onClick={() => setConfirmDeleteID(group.id)}><Trash2 className="size-4" /></Button>
              )}
            </div>;
          })}
        </div>
        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-semibold">{editingID ? "编辑账号组" : "新建账号组"}</h3>
            {editingID ? <Button variant="ghost" size="sm" onClick={() => edit()}><Plus className="size-4" />新建</Button> : null}
          </div>
          <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="账号组名称" aria-label="账号组名称" />
          <label className="flex items-center gap-2 text-sm"><Checkbox checked={enabled} onCheckedChange={(value) => setEnabled(Boolean(value))} />启用</label>
          <Select value={proxyMode} onValueChange={(value) => setProxyMode(value as ProxyMode)}>
            <SelectTrigger aria-label="账号组出口"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="inherit">继承默认出口</SelectItem>
              <SelectItem value="direct">直连</SelectItem>
              <SelectItem value="group">代理组</SelectItem>
              <SelectItem value="custom">自定义代理</SelectItem>
            </SelectContent>
          </Select>
          {proxyMode === "group" ? <Select value={proxyGroupID} onValueChange={setProxyGroupID}>
            <SelectTrigger aria-label="选择代理组"><SelectValue placeholder="选择代理组" /></SelectTrigger>
            <SelectContent>{proxyGroups.map((group) => <SelectItem key={group.id} value={group.id} disabled={!group.enabled || !group.nodes.some((node) => node.enabled)}>{group.name}</SelectItem>)}</SelectContent>
          </Select> : null}
          {proxyMode === "custom" ? <Input value={proxyURL} onChange={(event) => setProxyURL(event.target.value)} placeholder="http://user:pass@host:port" autoComplete="off" aria-label="代理地址" /> : null}
        </div>
        <DialogFooter><Button onClick={() => void save()} disabled={busy}>{busy ? <LoaderCircle className="size-4 animate-spin" /> : null}保存</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
