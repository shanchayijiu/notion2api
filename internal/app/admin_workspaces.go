package app

// admin_workspaces.go — /admin/workspaces 工作空间生命周期管理（P1）
// GET 列表 / POST rotate（手动轮换）/ POST delete（软删）

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

func (a *App) handleAdminWorkspaces(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	if a.State == nil || a.State.Store == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "storage unavailable", "server_error", "storage_unavailable")
		return
	}
	switch {
	case r.Method == http.MethodGet:
		account := strings.TrimSpace(r.URL.Query().Get("account"))
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		items, err := a.State.Store.LoadSpaceLifecycles(account, status)
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, err.Error(), "server_error", "workspace_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rotate"):
		a.handleAdminWorkspaceRotate(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/delete"):
		a.handleAdminWorkspaceDelete(w, r)
	default:
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error", "method_not_allowed")
	}
}

// handleAdminWorkspaceRotate — 手动触发指定账号轮换（换新空间）
func (a *App) handleAdminWorkspaceRotate(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.currentConfig()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error(), "server_error", "config_unavailable")
		return
	}
	var req struct {
		AccountEmail string `json:"account_email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error(), "invalid_request_error", "invalid_json")
		return
	}
	email := strings.TrimSpace(req.AccountEmail)
	if email == "" {
		if len(cfg.Accounts) == 1 {
			email = cfg.Accounts[0].Email
		} else {
			writeOpenAIError(w, http.StatusBadRequest, "account_email required", "invalid_request_error", "missing_account")
			return
		}
	}
	account, ok := findAccountByEmail(cfg, email)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "account not found", "invalid_request_error", "account_not_found")
		return
	}
	account = ensureAccountPaths(cfg, account)
	session, serr := loadSessionInfo(account.ProbeJSON, account.UserName, account.SpaceName)
	if serr != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "load session: "+serr.Error(), "server_error", "session_load_failed")
		return
	}
	if a.rotator == nil {
		a.rotator = NewWorkspaceRotatorWithState(a.State.Store, a.State)
	}
	newSession, rerr := a.rotator.Rotate(r.Context(), cfg, session)
	if rerr != nil {
		writeOpenAIError(w, http.StatusTooManyRequests, "rotate failed: "+rerr.Error(), "server_error", "rotate_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "old_space_id": session.SpaceID,
		"new_space_id": newSession.SpaceID, "space_view_id": newSession.SpaceViewID,
	})
}

// handleAdminWorkspaceDelete — 软删指定空间
func (a *App) handleAdminWorkspaceDelete(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.currentConfig()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error(), "server_error", "config_unavailable")
		return
	}
	var req struct {
		SpaceID string `json:"space_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json: "+err.Error(), "invalid_request_error", "invalid_json")
		return
	}
	spaceID := strings.TrimSpace(req.SpaceID)
	if spaceID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "space_id required", "invalid_request_error", "missing_space")
		return
	}
	email := ""
	if len(cfg.Accounts) == 1 {
		email = cfg.Accounts[0].Email
	}
	account, ok := findAccountByEmail(cfg, email)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "account not found", "invalid_request_error", "account_not_found")
		return
	}
	account = ensureAccountPaths(cfg, account)
	session, serr := loadSessionInfo(account.ProbeJSON, account.UserName, account.SpaceName)
	if serr != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "load session: "+serr.Error(), "server_error", "session_load_failed")
		return
	}
	if a.rotator == nil {
		a.rotator = NewWorkspaceRotatorWithState(a.State.Store, a.State)
	}
	if derr := a.rotator.DeleteSpace(r.Context(), cfg, session, spaceID); derr != nil {
		writeOpenAIError(w, http.StatusBadGateway, "delete failed: "+derr.Error(), "server_error", "delete_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "space_id": spaceID})
}

// currentConfig — 从 State 拿当前生效配置
func (a *App) currentConfig() (AppConfig, error) {
	if a.State == nil {
		return AppConfig{}, errors.New("server state unavailable")
	}
	return a.State.Config, nil
}

func findAccountByEmail(cfg AppConfig, email string) (NotionAccount, bool) {
	key := canonicalEmailKey(strings.TrimSpace(email))
	for _, acc := range cfg.Accounts {
		if canonicalEmailKey(acc.Email) == key {
			return acc, true
		}
	}
	return NotionAccount{}, false
}