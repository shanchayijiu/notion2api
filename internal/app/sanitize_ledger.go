package app

// sanitize_ledger.go — INV-03 码点守恒账本（v4 P0）
// 净化器每个丢弃点通过 ledgerDrop 上报 (rule-id, dropped 文本)。
// 生产环境 sanitizeLedgerRecorder 为 nil，零开销；测试模式开启后，
// 可按「输出 + 按序丢弃」逐码点重建原始流并比对（守恒断言）。
// rule-id 命名规范：SAN-<族>-<子类>，见 sanitizeLedgerRuleIDs。

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	sanLedgerRuleBOM          = "SAN-TRIM-BOM"
	sanLedgerRuleTrim         = "SAN-TRIM"
	sanLedgerRuleLang         = "SAN-LANG"
	sanLedgerRuleLangLead     = "SAN-LANG-LEAD"
	sanLedgerRuleLangUnclosed = "SAN-LANG-UNCLOSED"
	sanLedgerRuleToolBlock    = "SAN-TOOL-BLOCK"
	sanLedgerRuleToolFenceUcl = "SAN-TOOL-FENCE-UNCLOSED"
	sanLedgerRuleToolXML      = "SAN-TOOL-XML"
	sanLedgerRuleToolXMLUcl   = "SAN-TOOL-XML-UNCLOSED"
	sanLedgerRuleCiteTail     = "SAN-CITE-TAIL"
	sanLedgerRuleInvalidUTF8  = "SAN-INVALID-UTF8"
)

// sanitizeToValidUTF8 — S1 增量解码兜底（review 轮 2）：非法 UTF-8 序列替换为 U+FFFD 并记账。
// 上游为 JSON（合法 UTF-8），此路径仅防御性生效；替换后账本守恒定义在合法 UTF-8 域内。
func sanitizeToValidUTF8(text string) string {
	if utf8.ValidString(text) {
		return text
	}
	var b strings.Builder
	b.Grow(len(text) + 8)
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
			ledgerDrop(sanLedgerRuleInvalidUTF8, "\uFFFD")
		} else {
			b.WriteString(text[:size])
		}
		text = text[size:]
	}
	return b.String()
}

// sanitizeLedgerRecorder — 账本上报 hook（nil = 关闭，生产零开销）。
var sanitizeLedgerRecorder func(ruleID string, dropped string)

func ledgerDrop(ruleID string, dropped string) {
	if sanitizeLedgerRecorder != nil && dropped != "" {
		sanitizeLedgerRecorder(ruleID, dropped)
	}
}

// dropAllSubstrings — 移除 text 中所有 needle 并逐次上报账本（顺序 = 从左到右）。
func dropAllSubstrings(text, needle, ruleID string) string {
	if !strings.Contains(text, needle) {
		return text
	}
	var b strings.Builder
	rest := text
	for {
		idx := strings.Index(rest, needle)
		if idx < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:idx])
		ledgerDrop(ruleID, needle)
		rest = rest[idx+len(needle):]
	}
	return b.String()
}

// ledgerTrimPrefixSuffix — 记录 TrimSpace 前后丢弃的空白码点（与 unicode.IsSpace 一致）。
func ledgerTrimPrefixSuffix(text string) string {
	if sanitizeLedgerRecorder == nil {
		return strings.TrimSpace(text)
	}
	head := 0
	for head < len(text) {
		r, size := utf8.DecodeRuneInString(text[head:])
		if !unicode.IsSpace(r) {
			break
		}
		head += size
	}
	tail := len(text)
	for tail > head {
		r, size := utf8.DecodeLastRuneInString(text[:tail])
		if !unicode.IsSpace(r) {
			break
		}
		tail -= size
	}
	if head > 0 {
		ledgerDrop(sanLedgerRuleTrim, text[:head])
	}
	if tail < len(text) {
		ledgerDrop(sanLedgerRuleTrim, text[tail:])
	}
	return text[head:tail]
}
