package app

import (
	"os"
	"strings"
	"sync"
	"time"
)

// sessionInfoCache avoids re-reading and re-parsing each account's probe.json
// on every request (Phase 3 / P3-1). Entries are validated by probe file
// mtime+size and an account-identity signature, so a session refresh or config
// change invalidates them automatically.
const (
	sessionInfoCacheMaxEntries = 128
	sessionInfoCacheTTL        = 5 * time.Minute
)

type sessionInfoCacheEntry struct {
	session   SessionInfo
	signature string
	modTime   time.Time
	size      int64
	loadedAt  time.Time
}

var sessionInfoCache = struct {
	mu    sync.Mutex
	items map[string]sessionInfoCacheEntry
}{items: map[string]sessionInfoCacheEntry{}}

func sessionCacheSignature(cfg AppConfig, account NotionAccount) string {
	return strings.Join([]string{
		strings.TrimSpace(account.ProbeJSON),
		strings.TrimSpace(account.UserID),
		strings.TrimSpace(account.SpaceID),
		strings.TrimSpace(account.SpaceViewID),
		strings.TrimSpace(account.UserName),
		strings.TrimSpace(account.SpaceName),
		strings.TrimSpace(cfg.UserName),
		strings.TrimSpace(cfg.SpaceName),
	}, "\x00")
}

func cloneSessionInfo(session SessionInfo) SessionInfo {
	cloned := session
	if len(session.Cookies) > 0 {
		cloned.Cookies = append([]ProbeCookie(nil), session.Cookies...)
	}
	return cloned
}

func resetSessionInfoCacheForTest() {
	sessionInfoCache.mu.Lock()
	sessionInfoCache.items = map[string]sessionInfoCacheEntry{}
	sessionInfoCache.mu.Unlock()
}

// cachedLoadSessionInfoForAccount returns a cached SessionInfo when the probe
// file is unchanged; otherwise it loads from disk/storage-state and stores it.
func cachedLoadSessionInfoForAccount(cfg AppConfig, account NotionAccount) (SessionInfo, error) {
	account = ensureAccountPaths(cfg, account)
	probePath := strings.TrimSpace(account.ProbeJSON)
	if probePath == "" {
		return loadSessionInfoForAccountRefresh(cfg, account)
	}
	fileInfo, statErr := os.Stat(probePath)
	if statErr != nil {
		sessionInfoCache.mu.Lock()
		delete(sessionInfoCache.items, probePath)
		sessionInfoCache.mu.Unlock()
		return loadSessionInfoForAccountRefresh(cfg, account)
	}
	signature := sessionCacheSignature(cfg, account)
	modTime := fileInfo.ModTime()
	size := fileInfo.Size()

	sessionInfoCache.mu.Lock()
	entry, ok := sessionInfoCache.items[probePath]
	sessionInfoCache.mu.Unlock()
	if ok && entry.signature == signature && entry.size == size && entry.modTime.Equal(modTime) && time.Since(entry.loadedAt) < sessionInfoCacheTTL {
		return cloneSessionInfo(entry.session), nil
	}

	session, err := loadSessionInfoForAccountRefresh(cfg, account)
	if err != nil {
		return SessionInfo{}, err
	}
	sessionInfoCache.mu.Lock()
	if len(sessionInfoCache.items) >= sessionInfoCacheMaxEntries {
		sessionInfoCache.items = map[string]sessionInfoCacheEntry{}
	}
	sessionInfoCache.items[probePath] = sessionInfoCacheEntry{
		session:   cloneSessionInfo(session),
		signature: signature,
		modTime:   modTime,
		size:      size,
		loadedAt:  time.Now(),
	}
	sessionInfoCache.mu.Unlock()
	return session, nil
}
