'use client';

import { useCallback, useEffect, useState } from 'react';
import { toast } from 'sonner';
import { RefreshCcw, RotateCw, Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { ScrollArea } from '@/components/ui/scroll-area';
import {
  EmptyHint,
  InfoCard,
  PanelHeader,
  StatusPill,
  formatMaybeDate,
} from '@/components/admin/shared';
import { AdminService } from '@/lib/services/admin/admin.service';
import type { WorkspaceLifecycleItem } from '@/lib/services/admin/types';

const STATUS_LABEL: Record<string, string> = {
  active: 'active',
  exhausted: 'exhausted',
  to_delete: 'to_delete',
  deleted: 'deleted',
};

export function WorkspacesPanel() {
  const [items, setItems] = useState<WorkspaceLifecycleItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState<string>('');

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const payload = await AdminService.getWorkspaces();
      setItems(payload?.items ?? []);
    } catch (err) {
      toast.error(`加载工作空间失败: ${String(err)}`);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function handleRotate(accountEmail: string) {
    setBusy(`rotate:${accountEmail}`);
    try {
      const result = await AdminService.rotateWorkspace(accountEmail);
      toast.success(`轮换完成: ${JSON.stringify(result)}`);
      await load();
    } catch (err) {
      toast.error(`轮换失败: ${String(err)}`);
    } finally {
      setBusy('');
    }
  }

  async function handleDelete(spaceId: string) {
    setBusy(`delete:${spaceId}`);
    try {
      const result = await AdminService.deleteWorkspace(spaceId);
      toast.success(`删除已发起: ${JSON.stringify(result)}`);
      await load();
    } catch (err) {
      toast.error(`删除失败: ${String(err)}`);
    } finally {
      setBusy('');
    }
  }

  return (
    <div className="space-y-4">
      <PanelHeader
        eyebrow="Workspace Rotation"
        title="工作空间"
        actions={
          <Button variant="outline" size="sm" onClick={() => void load()} disabled={loading}>
            <RefreshCcw className="mr-1 h-3.5 w-3.5" />
            {loading ? '加载中…' : '刷新'}
          </Button>
        }
      />
      <InfoCard title="说明">
          工作空间生命周期管理：额度耗尽自动轮换的新空间在此登记；to_delete 状态由删除执行器后台软删。
        </InfoCard>
      <ScrollArea className="h-[420px] rounded-lg border">
        {items.length === 0 ? (
          <div className="p-8">
            <EmptyHint title="暂无工作空间记录" description="轮换引擎触发后自动登记" />
          </div>
        ) : (
          <table className="w-full text-left text-sm">
            <thead className="sticky top-0 bg-background text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2">空间 ID</th>
                <th className="px-3 py-2">账号</th>
                <th className="px-3 py-2">状态</th>
                <th className="px-3 py-2">创建时间</th>
                <th className="px-3 py-2 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {items.map((item) => (
                <tr key={item.space_id} className="border-t">
                  <td className="max-w-[220px] truncate px-3 py-2 font-mono text-xs">
                    {item.space_id}
                  </td>
                  <td className="max-w-[180px] truncate px-3 py-2 text-xs">{item.account_email || '-'}</td>
                  <td className="px-3 py-2">
                    <StatusPill status={STATUS_LABEL[item.status] ?? item.status} />
                  </td>
                  <td className="px-3 py-2 text-xs text-muted-foreground">
                    {formatMaybeDate(item.created_at)}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <div className="inline-flex gap-1">
                      <Button
                        variant="ghost"
                        size="sm"
                        title="删除该空间（软删）"
                        disabled={busy === `delete:${item.space_id}` || item.status === 'deleted'}
                        onClick={() => void handleDelete(item.space_id)}
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                      {item.account_email ? (
                        <Button
                          variant="ghost"
                          size="sm"
                          title="手动轮换（新建一个空间）"
                          disabled={busy === `rotate:${item.account_email}`}
                          onClick={() => void handleRotate(item.account_email)}
                        >
                          <RotateCw className="h-3.5 w-3.5" />
                        </Button>
                      ) : null}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </ScrollArea>
    </div>
  );
}