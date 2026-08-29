package app

// workspace_deletion.go — P2 删除执行器：后台定期软删 to_delete 状态的空间
// 策略：只删标记为 to_delete 的空间（deleteSpace 异步软删，left_spaces 留痕），
// 不删 active（只建不删策略下 active 空间保留供轮换复用）。

import (
	"context"
	"log"
	"strings"
	"time"
)

const (
	workspaceDeletionInterval = 5 * time.Minute
	spaceStatusDeleted        = "deleted"
)

// StartWorkspaceDeletionLoop — 后台删除循环（每 interval 扫一次 to_delete）
func (a *App) StartWorkspaceDeletionLoop(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(workspaceDeletionInterval)
		defer ticker.Stop()
		a.runWorkspaceDeletionOnce(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.runWorkspaceDeletionOnce(ctx)
			}
		}
	}()
	log.Printf("[workspace_deletion] loop started interval=%s", workspaceDeletionInterval)
}

// runWorkspaceDeletionOnce — 扫 to_delete 空间并软删
func (a *App) runWorkspaceDeletionOnce(ctx context.Context) {
	if a == nil || a.State == nil || a.State.Store == nil {
		return
	}
	if a.rotator == nil {
		a.rotator = NewWorkspaceRotator(a.State.Store)
	}
	cfg := a.State.Config
	if len(cfg.Accounts) == 0 {
		return
	}
	items, err := a.State.Store.LoadSpaceLifecycles("", spaceStatusToDelete)
	if err != nil {
		log.Printf("[workspace_deletion] load to_delete failed: %v", err)
		return
	}
	if len(items) == 0 {
		return
	}
	for _, lc := range items {
		if ctx.Err() != nil {
			return
		}
		// 找所属账号的 session
		account, ok := findAccountByEmail(cfg, lc.AccountEmail)
		if !ok {
			// 单账号配置兜底
			if len(cfg.Accounts) == 1 {
				account = cfg.Accounts[0]
			} else {
				continue
			}
		}
		account = ensureAccountPaths(cfg, account)
		session, serr := loadSessionInfo(account.ProbeJSON, account.UserName, account.SpaceName)
		if serr != nil {
			log.Printf("[workspace_deletion] load session for %s failed: %v", lc.AccountEmail, serr)
			continue
		}
		if derr := a.rotator.DeleteSpace(ctx, cfg, session, lc.SpaceID); derr != nil {
			log.Printf("[workspace_deletion] delete %s failed: %v", lc.SpaceID, derr)
			continue
		}
		_ = a.State.Store.UpdateSpaceLifecycleStatus(lc.SpaceID, spaceStatusDeleted)
		log.Printf("[workspace_deletion] deleted space %s (account=%s)", lc.SpaceID, lc.AccountEmail)
		time.Sleep(2 * time.Second)
	}
}

// adminWorkspaceMarkToDelete — 手动把空间标记为待删（保留给 /admin 用）
func (a *App) adminWorkspaceMarkToDelete(spaceID string) error {
	if a == nil || a.State == nil || a.State.Store == nil || strings.TrimSpace(spaceID) == "" {
		return nil
	}
	return a.State.Store.UpdateSpaceLifecycleStatus(spaceID, spaceStatusToDelete)
}