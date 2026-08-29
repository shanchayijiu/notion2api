package app

// build_identity.go — REQ-DEP-01/02/03：产物身份绑定（build 指纹响应头 + healthz 回显）
// 指纹 = 二进制 sha256 前 12 位 + sanitizerConfigVersion + 进程启动时间（每次部署唯一）。
// 报告 deployedArtifact 必须由探测运行中进程得到（读响应头 / healthz），禁止构建脚本填写。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sync"
	"time"
)

// sanitizerConfigVersion — 净化/方言登记表版本（REQ-DEP-01 要求响应头含此值）。
// 任何方言登记表增删改必须 +1 并在此留痕。
// v1：lang 标签族 + 引用角标（trimTrailingIncompleteCitation）+ 工具 action 块（```json 剥离）
// v2：INV-02 登记 <system-reminder>（上游注入物，金丝雀 2026-08-25 实锤，不进入正文通道）
var sanitizerConfigVersion = "v2"

// dialectTableVersion — 方言登记表版本（healthz 回显用，独立于净化配置）
var dialectTableVersion = "v1"

var (
	buildIdentityOnce    sync.Once
	buildFingerprintInfo buildIdentityInfo
	buildFingerprintID   string
	buildFingerprintHeader string
)

type buildIdentityInfo struct {
	CommitSHA       string `json:"commit"`
	BinarySHA256    string `json:"binary_sha256"`
	SanitizerVer    string `json:"sanitizer_config_version"`
	DialectVer      string `json:"dialect_table_version"`
	UpstreamProfile string `json:"upstream_profile"`
	StartedAt       string `json:"process_started_at"`
	StartedUnix     int64  `json:"process_started_unix"`
	Fingerprint     string `json:"fingerprint"`
}

func resolveBuildIdentity() buildIdentityInfo {
	buildIdentityOnce.Do(func() {
		commit := "local"
		if env := os.Getenv("NOTION2API_COMMIT"); env != "" {
			commit = env
		}
		binarySHA := ""
		if exe, err := os.Executable(); err == nil {
			if data, readErr := os.ReadFile(exe); readErr == nil {
				sum := sha256.Sum256(data)
				binarySHA = hex.EncodeToString(sum[:])
			}
		}
		if binarySHA == "" {
			binarySHA = "unknown"
		}
		buildFingerprintInfo = buildIdentityInfo{
			CommitSHA:      commit,
			BinarySHA256:   binarySHA,
			SanitizerVer:   sanitizerConfigVersion,
			DialectVer:     dialectTableVersion,
			UpstreamProfile: "notion-runinferencetranscript",
			StartedAt:      time.Now().Format(time.RFC3339),
			StartedUnix:    time.Now().Unix(),
		}
		buildFingerprintID = binarySHA[:minIntBuild(12, len(binarySHA))] + "-" + sanitizerConfigVersion + "-" + itoa(buildFingerprintInfo.StartedUnix)
		buildFingerprintInfo.Fingerprint = buildFingerprintID
	})
	return buildFingerprintInfo
}

func minIntBuild(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}