package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestProbe(t *testing.T, dir string, userID string) string {
	t.Helper()
	path := filepath.Join(dir, "probe.json")
	payload := map[string]any{
		"email":          "user@example.com",
		"user_id":        userID,
		"client_version": "23.13.0",
		"space_id":       "space-1",
		"space_view_id":  "view-1",
		"cookies":        []map[string]any{{"name": "token_v2", "value": "abc"}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal probe: %v", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	return path
}

func TestCachedSessionInfoReusesAndClones(t *testing.T) {
	resetSessionInfoCacheForTest()
	t.Cleanup(resetSessionInfoCacheForTest)

	dir := t.TempDir()
	probePath := writeTestProbe(t, dir, "user-1")
	cfg := AppConfig{}
	account := NotionAccount{Email: "user@example.com", ProbeJSON: probePath}

	first, err := cachedLoadSessionInfoForAccount(cfg, account)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if first.UserID != "user-1" {
		t.Fatalf("first user id=%q", first.UserID)
	}
	// Mutating the returned copy must not poison the cache.
	if len(first.Cookies) > 0 {
		first.Cookies[0].Value = "mutated"
	}
	second, err := cachedLoadSessionInfoForAccount(cfg, account)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if len(second.Cookies) == 0 || second.Cookies[0].Value != "abc" {
		t.Fatalf("cache returned mutated cookies: %#v", second.Cookies)
	}
}

func TestCachedSessionInfoInvalidatesOnProbeChange(t *testing.T) {
	resetSessionInfoCacheForTest()
	t.Cleanup(resetSessionInfoCacheForTest)

	dir := t.TempDir()
	probePath := writeTestProbe(t, dir, "user-1")
	cfg := AppConfig{}
	account := NotionAccount{Email: "user@example.com", ProbeJSON: probePath}

	if _, err := cachedLoadSessionInfoForAccount(cfg, account); err != nil {
		t.Fatalf("initial load: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	writeTestProbe(t, dir, "user-2")
	refreshed, err := cachedLoadSessionInfoForAccount(cfg, account)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if refreshed.UserID != "user-2" {
		t.Fatalf("cache did not invalidate on probe change: %q", refreshed.UserID)
	}
}

func TestCachedSessionInfoInvalidatesOnIdentitySignature(t *testing.T) {
	resetSessionInfoCacheForTest()
	t.Cleanup(resetSessionInfoCacheForTest)

	dir := t.TempDir()
	probePath := writeTestProbe(t, dir, "user-1")
	cfg := AppConfig{}
	account := NotionAccount{Email: "user@example.com", ProbeJSON: probePath, SpaceID: "space-A"}
	if _, err := cachedLoadSessionInfoForAccount(cfg, account); err != nil {
		t.Fatalf("initial load: %v", err)
	}

	changed := account
	changed.SpaceID = "space-B"
	// The probe file itself did not change, but the account identity did; the
	// signature check must force a reload.
	if _, err := cachedLoadSessionInfoForAccount(cfg, changed); err != nil {
		t.Fatalf("reload with changed signature: %v", err)
	}
	first := sessionCacheSignature(cfg, account)
	second := sessionCacheSignature(cfg, changed)
	if first == second {
		t.Fatalf("signature did not change with account identity")
	}
}

func TestPersistedConfigEqualIgnoresAccountCountersButDetectsRealChanges(t *testing.T) {
	base := AppConfig{ConfigPath: "config.json"}
	base.Accounts = []NotionAccount{{Email: "a@example.com", WindowRequestCount: 1, ConsecutiveFailures: 0}}
	changed := base
	changed.Accounts = []NotionAccount{{Email: "a@example.com", WindowRequestCount: 99, ConsecutiveFailures: 3, LastUsedAt: "2026-01-01T00:00:00Z"}}
	// With a config path set, normalizeConfig assumes SQLite-backed state, so
	// routine account counter churn must not force a config file write.
	if !persistedConfigEqual(base, changed) {
		t.Fatalf("account counter changes should not force a config file write")
	}

	// A real configuration change must still be persisted.
	realChange := base
	realChange.TimeoutSec = 240
	if persistedConfigEqual(base, realChange) {
		t.Fatalf("real config change must not compare equal")
	}
}
