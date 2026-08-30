package app

// register_live_probe_test.go — 现网购件诊断（非 CI；用 -run TestAdGuardLiveProbe 手动跑）
// 验证 adguardWaitCode 在真邮箱里能看到已到达的 Notion 验证码邮件。

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestAdGuardLiveProbe(t *testing.T) {
	if os.Getenv("N2A_LIVE_TEST") == "" {
		t.Skip("set N2A_LIVE_TEST=1 to run")
	}
	raw, err := os.ReadFile("/tmp/mailbox.json")
	if err != nil {
		t.Fatalf("read mailbox: %v", err)
	}
	var mj struct {
		Cookies map[string]string `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &mj); err != nil {
		t.Fatal(err)
	}
	mb := adguardMailbox{Address: "curly.crab.inmn@hidesit.net", Cookies: mj.Cookies}
	// notBefore 放到远古 → 任何 Notion 邮件都应该命中
	code, cerr := adguardWaitCode(context.Background(), "", mb, time.Now().Add(-720*time.Hour))
	t.Logf("code=%q err=%v", code, cerr)
	if code == "" {
		t.Fatalf("expected code, err=%v", cerr)
	}
}
