package app

// unknown_marker.go — INV-02 未知标记探测器（T-09 金丝雀支撑）
// 扫描上游原始响应流（NDJSON 原始字节），命中"未登记疑似控制标记"即告警并落盘候选 fixture。
// 已登记标记（方言登记表 v1）不会触发；启发式只抓"像控制标记但不认识"的形态。
// 开关：config debug.unknown_marker_scan=true + debug.unknown_marker_scan_dir=<dir>。

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// unknownMarkerScanConfig — 运行时扫描配置（由 ServerState 注入，nil=关闭）
type unknownMarkerScanConfig struct {
	Enabled bool
	Dir     string
}

var (
	unknownMarkerScanMu    sync.Mutex
	unknownMarkerScanCfg   *unknownMarkerScanConfig
	unknownMarkerScanCount int
)

// SetUnknownMarkerScanConfig — 由服务装配时调用（config debug 段）
func SetUnknownMarkerScanConfig(cfg *unknownMarkerScanConfig) {
	unknownMarkerScanMu.Lock()
	defer unknownMarkerScanMu.Unlock()
	unknownMarkerScanCfg = cfg
}

func unknownMarkerScanEnabled() bool {
	unknownMarkerScanMu.Lock()
	defer unknownMarkerScanMu.Unlock()
	return unknownMarkerScanCfg != nil && unknownMarkerScanCfg.Enabled
}

// 已登记 XML 标签名（lang/tool 族 + 上游注入物 + Claude Code 工具文档族）——命中不告警
// system-reminder：Notion 上游在 transcript instructions 注入的 system 提示（2026-08-25 金丝雀实锤），
// 属"上游注入物"族（REQ-STATE-05 已知偏差），不进入正文通道，登记为已知。
// Claude Code 工具文档族（2026-08-26 CC 实测）：CC 请求的工具 schema 被 Notion 序列化为
// XML 工具文档注入 transcript instructions（<function>/<command-name>/<path>/<any>/<transcriptDir> 等），
// 属输入侧结构，不进入正文通道。
var registeredXMLTagNames = map[string]bool{
	"lang": true, "tool_call": true, "tool_calls": true,
	"function_call": true, "invoke": true, "system-reminder": true,
	// Claude Code 工具文档族
	"function": true, "functions": true, "command-name": true, "path": true,
	"any": true, "transcriptdir": true, "id": true, "task-notification": true,
	"name": true, "description": true, "parameter": true, "parameters": true,
	"tool": true, "argument": true, "arguments": true,
}

var (
	xmlOpenTagPattern  = regexp.MustCompile(`<([a-zA-Z_][a-zA-Z0-9_-]*)(?:\s[^>]*)?>`)
	xmlCloseTagPattern = regexp.MustCompile(`</([a-zA-Z_][a-zA-Z0-9_-]*)>`)
)

// unknownMarkerHit — 单个未登记标记命中
type unknownMarkerHit struct {
	Marker  string `json:"marker"`
	Rule    string `json:"rule"`
	Snippet string `json:"snippet"`
	Offset  int    `json:"offset"`
}

// scanUnknownUpstreamMarkers — 启发式扫描原始流。
// 规则：
//   U1 未登记的 XML 标签（<name...> / </name>，name ∉ 登记表）
//   U2 未登记的尖括号控制形态（<|...|>、{{...}} 等）
//   U3 全角方括号引用角标之外的疑似控制块（【...】内非 citation 数字）
func scanUnknownUpstreamMarkers(raw string) []unknownMarkerHit {
	var hits []unknownMarkerHit
	for _, m := range xmlOpenTagPattern.FindAllStringSubmatchIndex(raw, -1) {
		name := raw[m[2]:m[3]]
		if registeredXMLTagNames[strings.ToLower(name)] {
			continue
		}
		hits = append(hits, unknownMarkerHit{
			Marker:  raw[m[0]:m[1]],
			Rule:    "U1-unregistered-xml-tag",
			Snippet: snippetAround(raw, m[0]),
			Offset:  m[0],
		})
	}
	for _, m := range xmlCloseTagPattern.FindAllStringSubmatchIndex(raw, -1) {
		name := raw[m[2]:m[3]]
		if registeredXMLTagNames[strings.ToLower(name)] {
			continue
		}
		hits = append(hits, unknownMarkerHit{
			Marker:  raw[m[0]:m[1]],
			Rule:    "U1-unregistered-xml-tag",
			Snippet: snippetAround(raw, m[0]),
			Offset:  m[0],
		})
	}
	// U2：<|...|> / {{...}} 控制形态（已登记的 im_start/im_end/eot_id/endoftext 家族在登记表，其余告警）
	for _, m := range regexp.MustCompile(`<\|[^>|]{1,40}\|>`).FindAllStringSubmatchIndex(raw, -1) {
		marker := raw[m[0]:m[1]]
		if isRegisteredControlToken(marker) {
			continue
		}
		hits = append(hits, unknownMarkerHit{
			Marker:  marker,
			Rule:    "U2-unregistered-control-token",
			Snippet: snippetAround(raw, m[0]),
			Offset:  m[0],
		})
	}
	return hits
}

func isRegisteredControlToken(marker string) bool {
	switch marker {
	case "<|im_start|>", "<|im_end|>", "<|eot_id|>", "<|endoftext|>", "</s>":
		return true
	}
	return false
}

func snippetAround(text string, offset int) string {
	lo := offset - 40
	if lo < 0 {
		lo = 0
	}
	hi := offset + 60
	if hi > len(text) {
		hi = len(text)
	}
	return text[lo:hi]
}

// maybeScanUnknownMarkers — 流终止后调用：命中即日志告警 + 候选 fixture 落盘（T-09 取证）。
func maybeScanUnknownMarkers(raw string) {
	if !unknownMarkerScanEnabled() {
		return
	}
	hits := scanUnknownUpstreamMarkers(raw)
	if len(hits) == 0 {
		return
	}
	unknownMarkerScanMu.Lock()
	scanDir := ""
	if unknownMarkerScanCfg != nil {
		scanDir = unknownMarkerScanCfg.Dir
	}
	unknownMarkerScanCount++
	n := unknownMarkerScanCount
	unknownMarkerScanMu.Unlock()

	log.Printf("[unknown-marker] HIT %d hits=%d (总命中计数=%d)", n, len(hits), n)
	for i, h := range hits {
		log.Printf("[unknown-marker]   #%d rule=%s marker=%q offset=%d", i+1, h.Rule, h.Marker, h.Offset)
	}
	if scanDir == "" {
		return
	}
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		log.Printf("[unknown-marker] mkdir %s: %v", scanDir, err)
		return
	}
	ts := time.Now().Format("20060102T150405.000")
	path := filepath.Join(scanDir, "candidate_"+ts+".ndjson")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		log.Printf("[unknown-marker] write candidate fixture failed: %v", err)
		return
	}
	log.Printf("[unknown-marker] candidate fixture 落盘 %s", path)
}