package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxAttachmentBytes                    = 20 * 1024 * 1024
	bestEffortUpstreamTimeout             = 5 * time.Second
	bestEffortPostRunTimeout              = 3 * time.Second
	browserFallbackTimeout                = 60 * time.Second
	browserFallbackTimeoutMax             = 120 * time.Second
	browserFallbackTimeoutStep            = 5 * time.Second
	browserFallbackTimeoutStepBytes       = 4 * 1024
	browserFallbackTimeoutThreshold       = 12 * 1024
	bestEffortBudgetDivisor         int64 = 4
)

var leadingLangTagPattern = regexp.MustCompile(`(?is)^\s*(?:<lang\b[^>]*>|</lang>)\s*`)
var prefixedTranscriptStepIDPattern = regexp.MustCompile(`^(?:cfg|ctx|upd)_([0-9a-fA-F]{32})$`)
var notionHTTPTransportCacheMetric = expvar.NewMap("notion2api_http_transport_cache_total")

// maxCachedNotionTransports — P1-3:transport 缓存上限(账号×代理粒度),
// 超限淘汰一个并释放其空闲连接,防长期运行 goroutine/连接累积
const maxCachedNotionTransports = 64

type notionHTTPTransportCacheKey struct {
	UpstreamBaseURL       string
	UpstreamOriginURL     string
	UpstreamHostHeader    string
	UpstreamTLSServerName string
	UpstreamUseEnvProxy   bool
	ProxyMode             string
	ProxyURL              string
	ProxyHTTPURL          string
	ProxyHTTPSURL         string
	ResinEnabled          bool
	ResinURL              string
	ResinPlatform         string
	ResinMode             string
	AccountEmailKey       string
}

var notionTransportCache = struct {
	mu    sync.RWMutex
	items map[notionHTTPTransportCacheKey]*http.Transport
}{
	items: map[notionHTTPTransportCacheKey]*http.Transport{},
}

func bestEffortTimeout(parent context.Context, cap time.Duration) time.Duration {
	if cap <= 0 {
		return 0
	}
	if parent == nil {
		return cap
	}
	if err := parent.Err(); err != nil {
		return 0
	}
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0
		}
		budget := remaining / time.Duration(bestEffortBudgetDivisor)
		if budget <= 0 {
			budget = remaining
		}
		if budget < cap {
			cap = budget
		}
	}
	return cap
}

func boundedTimeout(parent context.Context, cap time.Duration) time.Duration {
	if cap <= 0 {
		return 0
	}
	if parent == nil {
		return cap
	}
	if err := parent.Err(); err != nil {
		return 0
	}
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0
		}
		if remaining < cap {
			cap = remaining
		}
	}
	return cap
}

func browserFallbackPayloadBytes(payload map[string]any) int {
	if len(payload) == 0 {
		return 0
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0
	}
	return len(body)
}

func browserFallbackTimeoutForPayload(parent context.Context, payload map[string]any) time.Duration {
	timeout := browserFallbackTimeout
	if payloadBytes := browserFallbackPayloadBytes(payload); payloadBytes > browserFallbackTimeoutThreshold {
		extraBytes := payloadBytes - browserFallbackTimeoutThreshold
		extraSteps := (extraBytes + browserFallbackTimeoutStepBytes - 1) / browserFallbackTimeoutStepBytes
		timeout += time.Duration(extraSteps) * browserFallbackTimeoutStep
		if timeout > browserFallbackTimeoutMax {
			timeout = browserFallbackTimeoutMax
		}
	}
	return boundedTimeout(parent, timeout)
}

func bestEffortContext(parent context.Context, cap time.Duration) (context.Context, context.CancelFunc, bool) {
	timeout := bestEffortTimeout(parent, cap)
	if timeout <= 0 {
		return nil, func() {}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return ctx, cancel, true
}

func sanitizeAssistantVisibleText(text string) string {
	clean := sanitizeToValidUTF8(text)
	if strings.HasPrefix(clean, "\uFEFF") {
		ledgerDrop(sanLedgerRuleBOM, "\uFEFF")
		clean = clean[len("\uFEFF"):]
	}
	clean = ledgerTrimPrefixSuffix(clean)
	clean = cleanAllLangTags(clean)
	clean = stripToolActionBlocks(clean)
	clean = ledgerTrimPrefixSuffix(trimTrailingIncompleteCitation(clean))
	if strings.HasPrefix(strings.ToLower(clean), "<lang") && !strings.Contains(clean, ">") {
		ledgerDrop(sanLedgerRuleLangUnclosed, clean)
		return ""
	}
	for clean != "" {
		next := strings.TrimSpace(leadingLangTagPattern.ReplaceAllString(clean, ""))
		if next == clean {
			break
		}
		ledgerDrop(sanLedgerRuleLangLead, clean)
		clean = ledgerTrimPrefixSuffix(next)
	}
	return clean
}

// stripToolActionBlocks — 工具 action 块剥离（REQ-TOOL-15/16、INV-01/INV-14）
// 形态：```json { "name": ..., "arguments": ... } ``` 或 <tool_call>...</tool_call>
// 完整闭合且内容是工具调用（含 "name"+"arguments" 键）→ 整块剥离；
// 未闭合 → 剥离围栏/标记与 JSON 载荷（保留前面的人类文本，不泄漏半个工具调用）。
// 非工具块的 ```json（用户合法代码块）原样保留。
func stripToolActionBlocks(text string) string {
	if !strings.Contains(text, "```") && !strings.Contains(text, "<tool_call") {
		return text
	}
	text = stripUnclosedToolCallXML(text)
	var b strings.Builder
	rest := text
	for {
		idx := strings.Index(rest, "```")
		if idx < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:idx])
		rest = rest[idx:]
		after := rest[3:]
		lineEnd := strings.Index(after, "\n")
		header := strings.TrimSpace(after)
		if lineEnd >= 0 {
			header = strings.TrimSpace(after[:lineEnd])
		}
		lowerHeader := strings.ToLower(header)
		// 结构规则（review 轮 2：白名单漏项即漏检）——任意语言标识（字母/数字开头）的 fence 都是候选；
		// 是否剥离由 looksLikeToolActionBlock 裁决（非工具块照常保留）
		isFencedBlock := header == "" || isFenceLanguageHeader(lowerHeader)
		if !isFencedBlock {
			b.WriteString("```")
			rest = after
			continue
		}
		contentStart := 0
		if lineEnd >= 0 {
			contentStart = lineEnd + 1
		}
		content := after[contentStart:]
		closeIdx := strings.Index(content, "```")
		if closeIdx < 0 {
			// 未闭合：剥离围栏标记与载荷（降级策略 A：标记与半截 JSON 均剥离）
			ledgerDrop(sanLedgerRuleToolFenceUcl, rest)
			rest = ""
			break
		}
		block := content[:closeIdx]
		if looksLikeToolActionBlock(block) {
			ledgerDrop(sanLedgerRuleToolBlock, rest[:3+contentStart])
			ledgerDrop(sanLedgerRuleToolBlock, block)
			ledgerDrop(sanLedgerRuleToolBlock, "```")
			rest = content[closeIdx+3:]
			continue
		}
		b.WriteString("```" + header)
		if lineEnd >= 0 {
			b.WriteString("\n")
		}
		b.WriteString(block)
		b.WriteString("```")
		rest = content[closeIdx+3:]
	}
	return b.String()
}

// isFenceLanguageHeader — fence 语言标识结构判定：首字符为字母/数字（任意语言，非白名单）。
func isFenceLanguageHeader(lowerHeader string) bool {
	if lowerHeader == "" {
		return false
	}
	c := lowerHeader[0]
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// stripUnclosedToolCallXML — <tool_call> 未闭合区间：剥标记与载荷（INV-14 降级策略 A）
func stripUnclosedToolCallXML(text string) string {
	if !strings.Contains(text, "<tool_call") {
		return text
	}
	start := strings.Index(text, "<tool_call")
	closeIdx := strings.Index(text[start:], "</tool_call>")
	if closeIdx >= 0 {
		// 完整闭合：整块剥离（含闭合标记）
		end := start + closeIdx + len("</tool_call>")
		ledgerDrop(sanLedgerRuleToolXML, text[start:end])
		return text[:start] + stripUnclosedToolCallXML(text[end:])
	}
	// 未闭合：剥到文本末尾
	ledgerDrop(sanLedgerRuleToolXMLUcl, text[start:])
	return text[:start]
}

// looksLikeToolActionBlock — 工具调用块判据（review 循环 4 收紧：宁漏勿误删）
// name 键需同时含 arguments；action/tool 扁平格式需同时含参数键之一；否则不判
func looksLikeToolActionBlock(block string) bool {
	var raw map[string]any
	if err := json.Unmarshal([]byte(block), &raw); err != nil {
		return false
	}
	if name, ok := raw["name"].(string); ok && strings.TrimSpace(name) != "" {
		if _, hasArgs := raw["arguments"]; hasArgs {
			return true
		}
		return false
	}
	toolName := ""
	if a, ok := raw["action"].(string); ok && strings.TrimSpace(a) != "" {
		toolName = a
	}
	if toolName == "" {
		if t, ok := raw["tool"].(string); ok && strings.TrimSpace(t) != "" {
			toolName = t
		}
	}
	if toolName == "" {
		return false
	}
	for _, key := range []string{"arguments", "parameters", "path", "query", "content", "file_path", "command", "url", "input"} {
		if _, ok := raw[key]; ok {
			return true
		}
	}
	return false
}

func cleanAllLangTags(text string) string {
	for {
		start := strings.Index(text, "<lang")
		if start < 0 {
			break
		}
		rest := text[start:]
		selfClosingEnd := strings.Index(rest, "/>")
		openTagEnd := strings.Index(rest, ">")
		switch {
		case selfClosingEnd >= 0 && (openTagEnd < 0 || selfClosingEnd <= openTagEnd):
			ledgerDrop(sanLedgerRuleLang, rest[:selfClosingEnd+2])
			text = text[:start] + rest[selfClosingEnd+2:]
		case openTagEnd >= 0:
			ledgerDrop(sanLedgerRuleLang, rest[:openTagEnd+1])
			text = text[:start] + rest[openTagEnd+1:]
		default:
			prefix := dropAllSubstrings(text[:start], "</lang>", sanLedgerRuleLang)
			ledgerDrop(sanLedgerRuleLangUnclosed, rest)
			return prefix
		}
	}
	return dropAllSubstrings(text, "</lang>", sanLedgerRuleLang)
}

func trimTrailingIncompleteCitation(text string) string {
	state := 0
	start := -1
	for i, ch := range text {
		switch state {
		case 0:
			if ch == '[' {
				state = 1
				start = i
			}
		case 1:
			if ch == '^' {
				state = 2
			} else if ch == '[' {
				start = i
			} else {
				state = 0
				start = -1
			}
		case 2:
			if ch == ']' {
				state = 0
				start = -1
			}
		}
	}
	if state != 0 && start >= 0 {
		ledgerDrop(sanLedgerRuleCiteTail, text[start:])
		return text[:start]
	}
	return text
}

type ProbeCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type probePayload struct {
	Email         string        `json:"email"`
	UserID        string        `json:"user_id"`
	UserName      string        `json:"user_name,omitempty"`
	SpaceID       string        `json:"space_id"`
	SpaceViewID   string        `json:"space_view_id,omitempty"`
	SpaceName     string        `json:"space_name,omitempty"`
	ClientVersion string        `json:"client_version"`
	Cookies       []ProbeCookie `json:"cookies"`
}

type SessionInfo struct {
	ProbePath     string
	ClientVersion string
	UserID        string
	UserEmail     string
	UserName      string
	SpaceID       string
	SpaceViewID   string
	SpaceName     string
	Cookies       []ProbeCookie
}

type UploadedAttachment struct {
	Name          string         `json:"name"`
	ContentType   string         `json:"content_type"`
	SizeBytes     int            `json:"size_bytes"`
	Source        string         `json:"source"`
	FileID        string         `json:"file_id,omitempty"`
	ThreadMounted bool           `json:"thread_mounted,omitempty"`
	AttachmentURL string         `json:"attachment_url"`
	SignedGetURL  string         `json:"signed_get_url,omitempty"`
	TaskID        string         `json:"task_id,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

type InferenceResult struct {
	Prompt           string               `json:"prompt"`
	Model            string               `json:"model"`
	NotionModel      string               `json:"notion_model"`
	AccountEmail     string               `json:"account_email,omitempty"`
	ThreadID         string               `json:"thread_id"`
	TraceID          string               `json:"trace_id"`
	Text             string               `json:"text"`
	Reasoning        string               `json:"reasoning,omitempty"`
	MessageID        string               `json:"message_id"`
	CompletedTime    any                  `json:"completed_time,omitempty"`
	NDJSONLineCount  int                  `json:"ndjson_line_count"`
	RawMessageIDs    []string             `json:"raw_message_ids,omitempty"`
	Attachments      []UploadedAttachment `json:"attachments,omitempty"`
	ConfigID         string               `json:"config_id,omitempty"`
	ContextID        string               `json:"context_id,omitempty"`
	OriginalDatetime string               `json:"original_datetime,omitempty"`
	ToolUses         []InferenceToolUse   `json:"tool_uses,omitempty"`
	// Truncated is set when the response was cut short locally to satisfy
	// max_tokens / max_completion_tokens. It maps to finish_reason=length.
	Truncated bool `json:"truncated,omitempty"`
}

type InferenceTranscriptSummary struct {
	ThreadID         string    `json:"thread_id"`
	Title            string    `json:"title"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	CreatedByDisplay string    `json:"created_by_display_name,omitempty"`
	TranscriptType   string    `json:"type,omitempty"`
}

type PromptRunRequest struct {
	Prompt                            string
	LatestUserPrompt                  string
	HiddenPrompt                      string
	PublicModel                       string
	NotionModel                       string
	ClientProfile                     string
	ClientMode                        string
	ClientSessionKey                  string
	PromptProfileOverride             string
	PromptEscalationStep              int
	UpstreamThreadID                  string
	UseWebSearch                      bool
	Attachments                       []InputAttachment
	PinnedAccountEmail                string
	AllowPinnedAccountFallback        bool
	StreamReasoningWarmup             bool
	SuppressReasoningOutput           bool
	SuppressUpstreamThreadPersistence bool
	SessionFingerprint                string
	RawMessageCount                   int
	ConversationID                    string
	EphemeralConversation             bool
	EphemeralReason                   string
	EphemeralDeleteAfter              time.Time
	ForceLocalConversationContinue    bool
	MaskLocalPaths                    bool
	ClientWorkingDirectory            string
	AllowTextToolSynthesis            bool
	ToolBridgeSection                 string
	ToolBridgeAssistantSample         string
	ToolsRaw                          []map[string]any
	StopSequences                     []string
	MaxOutputTokens                   int
	ParallelToolCalls                 *bool
	PromptAdapterApplied              bool
	PromptAdapterName                 string
	SessionRepeatTurn                 bool
	ForceSessionRepeatTurn            bool
	attachmentThreadReady             bool
	continuationDraft                 *continuationTurnDraft
	continuationScaffold              *continuationTurnScaffold
}

type agentMessage struct {
	MessageID     string
	Completed     bool
	CompletedTime any
	Text          string
	Reasoning     string
}

type inferenceStepError struct {
	ThreadID   string
	MessageID  string
	Message    string
	SubType    string
	TraceID    string
	Retryable  bool
	StackTrace string
}

func (e *inferenceStepError) Error() string {
	if e == nil {
		return ""
	}
	message := firstNonEmpty(strings.TrimSpace(e.Message), "upstream inference failed")
	details := []string{}
	if clean := strings.TrimSpace(e.SubType); clean != "" {
		details = append(details, "sub_type="+clean)
	}
	if clean := strings.TrimSpace(e.TraceID); clean != "" {
		details = append(details, "trace_id="+clean)
	}
	if e.Retryable {
		details = append(details, "retryable=true")
	}
	if len(details) == 0 {
		return fmt.Sprintf("thread %s failed: %s", strings.TrimSpace(e.ThreadID), message)
	}
	return fmt.Sprintf("thread %s failed: %s (%s)", strings.TrimSpace(e.ThreadID), message, strings.Join(details, " "))
}

type inferenceTransportError struct {
	Message string
}

func (e *inferenceTransportError) Error() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.Message)
}

// errAccountStarved — 上游未启动推理（无 agent-inference 行）即返回。
// 判定为账号级故障：dispatch 应快速标记该账号并换下一个候选，不做长轮询。
var errAccountStarved = errors.New("upstream did not start inference for this account (account may be limited); switching account")

// accountSyncBlackholeTimeout — 被限制账号的 sync 请求黑洞检测窗口（正常账号 0.3s 返回，8s 余量充足）。
const accountSyncBlackholeTimeout = 8 * time.Second

// accountPollBlackholeTimeout — 轮询阶段黑洞检测总窗口（单轮 8s × 多轮会叠加，20s 上限防拖死）。
const accountPollBlackholeTimeout = 20 * time.Second

type uploadDescriptor struct {
	URL                 string         `json:"url"`
	SignedGetURL        string         `json:"signedGetUrl"`
	SignedUploadPostURL string         `json:"signedUploadPostUrl"`
	PostHeaders         []string       `json:"postHeaders"`
	Fields              map[string]any `json:"fields"`
	ChatID              string         `json:"chatId"`
}

type notionAPIError struct {
	URL        string
	StatusCode int
	Message    string
}

func (e *notionAPIError) Error() string {
	if e == nil {
		return ""
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("%s failed: %d %s", e.URL, e.StatusCode, message)
}

type NotionAIClient struct {
	Session                     SessionInfo
	Config                      AppConfig
	AccountEmail                string
	ProxyResolver               *ProxyResolver
	Timeout                     time.Duration
	PollInterval                time.Duration
	PollMaxRounds               int
	HTTPClient                  *http.Client
	browserRunInferenceFallback func(context.Context, map[string]any) (string, error)
}

type ndjsonPatchOperation struct {
	O string `json:"o"`
	P string `json:"p"`
	V any    `json:"v"`
}

type ndjsonStreamLine struct {
	Type       string                      `json:"type"`
	V          []ndjsonPatchOperation      `json:"v,omitempty"`
	RecordMap  map[string]any              `json:"recordMap,omitempty"`
	ID         string                      `json:"id,omitempty"`
	FinishedAt any                         `json:"finishedAt,omitempty"`
	Value      []ndjsonAgentInferenceValue `json:"value,omitempty"`
}

type ndjsonAgentInferenceValue struct {
	Type      string `json:"type"`
	Content   string `json:"content"`
	Signature string `json:"signature,omitempty"`
}

type ndjsonAgentInferenceEvent struct {
	Type       string                      `json:"type"`
	ID         string                      `json:"id"`
	FinishedAt any                         `json:"finishedAt,omitempty"`
	Value      []ndjsonAgentInferenceValue `json:"value,omitempty"`
}

var clientDebugEnabled = false // DEBUG

type ndjsonStepState struct {
	ID        string
	Type      string
	Text      string
	Reasoning string
	Completed bool
}

// InferenceToolUse — Notion 原生 agent 工具调用（NDJSON tool_use 事件，对齐 notion_manager 透传）
type InferenceToolUse struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ndjsonParseResult struct {
	LineCount  int
	MessageIDs []string
	FinalAgent agentMessage
	Reasoning  string
	ToolUses   []InferenceToolUse
	// HasAgentInference 表示上游是否启动了 agent-inference 步骤。
	// 账号被上游标记（限制）时 runInferenceTranscript 只回 config/context/user
	// 三行、从不启动推理 —— 这是账号级故障信号，应快速失败并换号。
	HasAgentInference bool
}

type ndjsonTranscriptState struct {
	LineCount        int
	Steps            []ndjsonStepState
	ActiveAgentIndex int
	EmittedText      string
	EmittedReasoning string
	MessageIDs       []string
	FinalAgent       agentMessage
	ToolUses         []InferenceToolUse
	patchValueTypes  map[string]string
	patchValueText   map[string]string
	patchValueCounts map[string]int
}

func (s *ndjsonTranscriptState) hasTerminalAnswer() bool {
	if !s.FinalAgent.Completed {
		return false
	}
	return strings.TrimSpace(s.FinalAgent.Text) != ""
}

func (s *ndjsonTranscriptState) hasVisibleAnswer() bool {
	return strings.TrimSpace(firstNonEmpty(s.FinalAgent.Text, s.EmittedText)) != ""
}

type continuationTurnDraft struct {
	SessionID              string
	ConfigID               string
	ConfigValue            map[string]any
	ContextID              string
	ContextValue           map[string]any
	UpdatedConfigIDs       []string
	LastUpdatedConfigValue map[string]any
	OriginalDatetime       string
	TurnCount              int
	RawMessageCount        int
	Fingerprint            string
}

type continuationTurnScaffold struct {
	UpdatedConfigID    string
	UserStepID         string
	UserCreatedAt      string
	UpdatedConfigValue map[string]any
}

func loadSessionInfo(probePath string, userName string, spaceName string) (SessionInfo, error) {
	absPath, err := filepath.Abs(probePath)
	if err != nil {
		return SessionInfo{}, err
	}
	rawBytes, err := os.ReadFile(absPath)
	if err != nil {
		return SessionInfo{}, err
	}
	var payload probePayload
	if err := json.Unmarshal(rawBytes, &payload); err != nil {
		return SessionInfo{}, fmt.Errorf("decode probe json: %w", err)
	}
	if strings.TrimSpace(payload.Email) == "" {
		return SessionInfo{}, fmt.Errorf("probe json missing email: %s", absPath)
	}
	spaceID := strings.TrimSpace(payload.SpaceID)
	if spaceID == "" {
		// spaceless 注册产物：允许空 space_id，仅用于 workspace_pool 预建（createspace 不依赖此字段）。
		// 请求路径 x-notion-space-id 空值会被 baseHeaders 自动丢弃，安全。
		log.Printf("[probe] %s has empty space_id (spaceless register); usable for pool-precreate only", absPath)
	}
	if strings.TrimSpace(payload.UserID) == "" || strings.TrimSpace(payload.ClientVersion) == "" {
		return SessionInfo{}, fmt.Errorf("probe json missing required fields: %s", absPath)
	}
	if len(payload.Cookies) == 0 {
		return SessionInfo{}, fmt.Errorf("probe json missing cookies: %s", absPath)
	}
	localPart := payload.Email
	if idx := strings.Index(payload.Email, "@"); idx >= 0 {
		localPart = payload.Email[:idx]
	}
	resolvedUserName := strings.TrimSpace(userName)
	if resolvedUserName == "" {
		resolvedUserName = firstNonEmpty(strings.TrimSpace(payload.UserName), localPart)
	}
	resolvedSpaceName := strings.TrimSpace(spaceName)
	if resolvedSpaceName == "" {
		resolvedSpaceName = firstNonEmpty(strings.TrimSpace(payload.SpaceName), resolvedUserName+"'s Space")
	}
	return SessionInfo{
		ProbePath:     absPath,
		ClientVersion: strings.TrimSpace(payload.ClientVersion),
		UserID:        strings.TrimSpace(payload.UserID),
		UserEmail:     strings.TrimSpace(payload.Email),
		UserName:      resolvedUserName,
		SpaceID:       strings.TrimSpace(payload.SpaceID),
		SpaceViewID:   strings.TrimSpace(payload.SpaceViewID),
		SpaceName:     resolvedSpaceName,
		Cookies:       payload.Cookies,
	}, nil
}

func (c *NotionAIClient) persistSessionProbe() error {
	probePath := strings.TrimSpace(c.Session.ProbePath)
	if probePath == "" {
		return nil
	}
	return writePrettyJSONFile(probePath, probePayload{
		Email:         c.Session.UserEmail,
		UserID:        c.Session.UserID,
		UserName:      c.Session.UserName,
		SpaceID:       c.Session.SpaceID,
		SpaceViewID:   c.Session.SpaceViewID,
		SpaceName:     c.Session.SpaceName,
		ClientVersion: c.Session.ClientVersion,
		Cookies:       c.Session.Cookies,
	})
}

func (c *NotionAIClient) probeMetadataNeedsBackfill() bool {
	if strings.TrimSpace(c.Session.ProbePath) == "" {
		return strings.TrimSpace(c.Session.SpaceViewID) == "" ||
			strings.TrimSpace(c.Session.UserName) == "" ||
			strings.TrimSpace(c.Session.SpaceName) == ""
	}
	rawBytes, err := os.ReadFile(strings.TrimSpace(c.Session.ProbePath))
	if err != nil {
		return strings.TrimSpace(c.Session.SpaceViewID) == "" ||
			strings.TrimSpace(c.Session.UserName) == "" ||
			strings.TrimSpace(c.Session.SpaceName) == ""
	}
	var payload probePayload
	if err := json.Unmarshal(rawBytes, &payload); err != nil {
		return strings.TrimSpace(c.Session.SpaceViewID) == "" ||
			strings.TrimSpace(c.Session.UserName) == "" ||
			strings.TrimSpace(c.Session.SpaceName) == ""
	}
	return strings.TrimSpace(payload.UserName) == "" ||
		strings.TrimSpace(payload.SpaceName) == "" ||
		strings.TrimSpace(payload.SpaceViewID) == ""
}

func (c *NotionAIClient) ensureSessionLiveMetadata(ctx context.Context) {
	if !c.probeMetadataNeedsBackfill() && strings.TrimSpace(c.Session.SpaceViewID) != "" {
		return
	}
	if len(c.Session.Cookies) == 0 || strings.TrimSpace(c.Session.UserID) == "" || strings.TrimSpace(c.Session.ClientVersion) == "" {
		return
	}
	bestEffortCtx, cancel, ok := bestEffortContext(ctx, bestEffortUpstreamTimeout)
	if !ok {
		return
	}
	defer cancel()
	body, err := c.postJSONWithReferer(
		bestEffortCtx,
		c.Config.NotionUpstream().API("loadUserContent"),
		map[string]any{},
		"application/json",
		c.Config.NotionUpstream().HomeURL(),
	)
	if err == nil {
		var payload map[string]any
		if json.Unmarshal(body, &payload) == nil {
			meta := parseLoadUserContentMetadata(payload)
			c.Session.UserEmail = firstNonEmpty(strings.TrimSpace(c.Session.UserEmail), strings.TrimSpace(meta.Email))
			c.Session.UserName = firstNonEmpty(strings.TrimSpace(meta.UserName), strings.TrimSpace(c.Session.UserName))
			c.Session.SpaceID = firstNonEmpty(strings.TrimSpace(c.Session.SpaceID), strings.TrimSpace(meta.SpaceID))
			c.Session.SpaceViewID = firstNonEmpty(strings.TrimSpace(c.Session.SpaceViewID), strings.TrimSpace(meta.SpaceViewID))
			c.Session.SpaceName = firstNonEmpty(strings.TrimSpace(meta.SpaceName), strings.TrimSpace(c.Session.SpaceName))
		}
	} else {
		c.logBestEffortFailure("loadUserContent", err)
	}
	if strings.TrimSpace(c.Session.SpaceViewID) != "" && strings.TrimSpace(c.Session.SpaceName) != "" && strings.TrimSpace(c.Session.UserName) != "" {
		_ = c.persistSessionProbe()
		return
	}
	body, err = c.postJSONWithReferer(
		bestEffortCtx,
		c.Config.NotionUpstream().API("getSpacesInitial"),
		map[string]any{},
		"application/json",
		c.Config.NotionUpstream().HomeURL(),
	)
	if err != nil {
		c.logBestEffortFailure("getSpacesInitial", err)
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return
	}
	bootstrap := parseSpacesInitial(payload, c.Session.UserID)
	if strings.TrimSpace(bootstrap.SpaceViewID) == "" {
		return
	}
	c.Session.UserEmail = firstNonEmpty(strings.TrimSpace(c.Session.UserEmail), strings.TrimSpace(bootstrap.Email))
	c.Session.UserName = firstNonEmpty(strings.TrimSpace(bootstrap.UserName), strings.TrimSpace(c.Session.UserName))
	c.Session.SpaceID = firstNonEmpty(strings.TrimSpace(c.Session.SpaceID), strings.TrimSpace(bootstrap.SpaceID))
	c.Session.SpaceViewID = strings.TrimSpace(bootstrap.SpaceViewID)
	_ = c.persistSessionProbe()
}

func newNotionAIClient(session SessionInfo, cfg AppConfig, accountEmail string) *NotionAIClient {
	return newNotionAIClientWithMode(session, cfg, accountEmail, false)
}

func newNotionAIStreamingClient(session SessionInfo, cfg AppConfig, accountEmail string) *NotionAIClient {
	return newNotionAIClientWithMode(session, cfg, accountEmail, true)
}

func buildNotionHTTPTransportCacheKey(cfg AppConfig, accountEmail string) notionHTTPTransportCacheKey {
	normalizedCfg := normalizeConfig(cfg)
	upstream := normalizedCfg.NotionUpstream()
	policy := normalizedCfg.ResolveProxyPolicyForAccount(accountEmail)
	return notionHTTPTransportCacheKey{
		UpstreamBaseURL:       strings.TrimSpace(upstream.BaseURL),
		UpstreamOriginURL:     strings.TrimSpace(upstream.OriginURL),
		UpstreamHostHeader:    strings.TrimSpace(upstream.HostHeader),
		UpstreamTLSServerName: strings.TrimSpace(upstream.TLSServerName),
		UpstreamUseEnvProxy:   upstream.UseEnvProxy,
		ProxyMode:             strings.TrimSpace(policy.Mode),
		ProxyURL:              strings.TrimSpace(policy.URL),
		ProxyHTTPURL:          strings.TrimSpace(policy.HTTPURL),
		ProxyHTTPSURL:         strings.TrimSpace(policy.HTTPSURL),
		ResinEnabled:          policy.Resin.Enabled,
		ResinURL:              strings.TrimSpace(policy.Resin.URL),
		ResinPlatform:         strings.TrimSpace(policy.Resin.Platform),
		ResinMode:             strings.TrimSpace(policy.Resin.Mode),
		AccountEmailKey:       canonicalEmailKey(accountEmail),
	}
}

func cachedNotionHTTPTransport(cfg AppConfig, accountEmail string, resolver *ProxyResolver, upstream NotionUpstream) *http.Transport {
	key := buildNotionHTTPTransportCacheKey(cfg, accountEmail)
	notionTransportCache.mu.RLock()
	cached := notionTransportCache.items[key]
	notionTransportCache.mu.RUnlock()
	if cached != nil {
		notionHTTPTransportCacheMetric.Add("hit_rlock", 1)
		return cached
	}
	tlsConfig := &tls.Config{InsecureSkipVerify: true}
	if strings.TrimSpace(upstream.TLSServerName) != "" {
		tlsConfig.ServerName = strings.TrimSpace(upstream.TLSServerName)
	}
	proxyFunc := upstream.ProxyFunc()
	// P0-2 修复：Transport 不能零值裸配——无 DialContext/TLSHandshakeTimeout/
	// ResponseHeaderTimeout 时，代理/上游半黑洞只能靠请求 ctx(60s/900s)兜底;
	// IdleConnTimeout=0 导致已被对端关闭的连接永久滞留池内、复用即 EOF 散发随机失败。
	transport := &http.Transport{
		TLSClientConfig:       tlsConfig,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 45 * time.Second, // 只约束"等响应头"(上游 TTFB ~2.9s,远不触发);流式读 body 不受限
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		Proxy: func(req *http.Request) (*url.URL, error) {
			if resolver != nil {
				proxyURL, _, err := resolver.ResolveProxyForRequest(accountEmail, req.URL)
				if err != nil {
					return nil, err
				}
				if proxyURL != nil {
					return proxyURL, nil
				}
			}
			if proxyFunc == nil {
				return nil, nil
			}
			return proxyFunc(req)
		},
	}
	notionTransportCache.mu.Lock()
	if existing := notionTransportCache.items[key]; existing != nil {
		notionTransportCache.mu.Unlock()
		notionHTTPTransportCacheMetric.Add("hit_lock", 1)
		return existing
	}
	// P1-3 修复：缓存只增不减会随账号轮换/配置热更积累 transport(各自持有 goroutine
	// 与连接)。设上限并淘汰一个条目(CloseIdleConnections 释放闲置连接)。
	if len(notionTransportCache.items) >= maxCachedNotionTransports {
		for evictKey, victim := range notionTransportCache.items {
			delete(notionTransportCache.items, evictKey)
			victim.CloseIdleConnections()
			notionHTTPTransportCacheMetric.Add("evicted", 1)
			break
		}
	}
	notionTransportCache.items[key] = transport
	notionTransportCache.mu.Unlock()
	notionHTTPTransportCacheMetric.Add("miss_new", 1)
	return transport
}

func newNotionAIClientWithMode(session SessionInfo, cfg AppConfig, accountEmail string, streaming bool) *NotionAIClient {
	normalizedCfg := normalizeConfig(cfg)
	resolver := NewProxyResolver(normalizedCfg)
	upstream := normalizedCfg.NotionUpstream()
	transport := cachedNotionHTTPTransport(normalizedCfg, accountEmail, resolver, upstream)
	timeout := requestTimeout(normalizedCfg)
	clientTimeout := timeout
	if streaming {
		timeout = streamRequestTimeout(normalizedCfg)
		clientTimeout = 0
	}
	return &NotionAIClient{
		Session:       session,
		Config:        normalizedCfg,
		AccountEmail:  strings.TrimSpace(accountEmail),
		ProxyResolver: resolver,
		Timeout:       timeout,
		PollInterval:  time.Duration(maxFloat(normalizedCfg.PollIntervalSec, 0.5) * float64(time.Second)),
		PollMaxRounds: maxInt(normalizedCfg.PollMaxRounds, 1),
		HTTPClient: &http.Client{
			Timeout:   clientTimeout,
			Transport: transport,
		},
	}
}

func (c *NotionAIClient) cookieHeader() string {
	parts := make([]string, 0, len(c.Session.Cookies))
	for _, item := range c.Session.Cookies {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		parts = append(parts, name+"="+item.Value)
	}
	return strings.Join(parts, "; ")
}

func (c *NotionAIClient) cookieValue(name string) string {
	clean := strings.TrimSpace(name)
	if clean == "" {
		return ""
	}
	for _, item := range c.Session.Cookies {
		if !strings.EqualFold(strings.TrimSpace(item.Name), clean) {
			continue
		}
		return strings.TrimSpace(item.Value)
	}
	return ""
}

func normalizeLocaleHeader(value string) string {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return ""
	}
	if idx := strings.Index(clean, "/"); idx >= 0 {
		clean = clean[:idx]
	}
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return ""
	}
	return clean
}

func (c *NotionAIClient) acceptLanguageHeader() string {
	for _, name := range []string{"NEXT_LOCALE", "notion_locale"} {
		if locale := normalizeLocaleHeader(c.cookieValue(name)); locale != "" {
			return locale
		}
	}
	return "en-US,en;q=0.9"
}

func (c *NotionAIClient) chatReferer(threadID string) string {
	base := strings.TrimRight(c.Config.NotionUpstream().OriginURL, "/")
	clean := strings.ReplaceAll(strings.TrimSpace(threadID), "-", "")
	if clean == "" {
		return c.Config.NotionUpstream().AIURL()
	}
	return base + "/chat?t=" + clean + "&wfv=chat"
}

func (c *NotionAIClient) requestThreadID(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if threadID := strings.TrimSpace(stringValue(payload["threadId"])); threadID != "" {
		return threadID
	}
	if requests := sliceValue(payload["requests"]); len(requests) > 0 {
		for _, raw := range requests {
			pointer := mapValue(mapValue(raw)["pointer"])
			if strings.TrimSpace(stringValue(pointer["table"])) != "thread" {
				continue
			}
			if threadID := strings.TrimSpace(stringValue(pointer["id"])); threadID != "" {
				return threadID
			}
		}
	}
	if transactions := sliceValue(payload["transactions"]); len(transactions) > 0 {
		for _, rawTxn := range transactions {
			transaction := mapValue(rawTxn)
			for _, rawOp := range sliceValue(transaction["operations"]) {
				operation := mapValue(rawOp)
				pointer := mapValue(operation["pointer"])
				if strings.TrimSpace(stringValue(pointer["table"])) == "thread" {
					if threadID := strings.TrimSpace(stringValue(pointer["id"])); threadID != "" {
						return threadID
					}
				}
				args := mapValue(operation["args"])
				if threadID := strings.TrimSpace(stringValue(args["parent_id"])); threadID != "" && strings.EqualFold(strings.TrimSpace(stringValue(args["parent_table"])), "thread") {
					return threadID
				}
			}
		}
	}
	return strings.TrimSpace(stringValue(payload["threadId"]))
}

func (c *NotionAIClient) requestReferer(url string, payload map[string]any) string {
	endpoint := strings.TrimSpace(url)
	switch {
	case strings.Contains(endpoint, "runInferenceTranscript"):
		if booleanValue(payload["createThread"]) {
			return c.Config.NotionUpstream().AIURL()
		}
		return c.chatReferer(c.requestThreadID(payload))
	case strings.Contains(endpoint, "saveTransactionsFanout"):
		return c.chatReferer(c.requestThreadID(payload))
	case strings.Contains(endpoint, "syncRecordValuesSpaceInitial"):
		return c.chatReferer(c.requestThreadID(payload))
	case strings.Contains(endpoint, "markInferenceTranscriptSeen"):
		return c.chatReferer(c.requestThreadID(payload))
	case strings.Contains(endpoint, "getInferenceTranscriptsForUser"):
		return c.Config.NotionUpstream().AIURL()
	default:
		return c.Config.NotionUpstream().AIURL()
	}
}

func (c *NotionAIClient) baseHeaders(accept string, referer string) map[string]string {
	upstream := c.Config.NotionUpstream()
	return map[string]string{
		"accept":                      accept,
		"content-type":                "application/json",
		"notion-client-version":       c.Session.ClientVersion,
		"notion-audit-log-platform":   "web",
		"x-notion-active-user-header": c.Session.UserID,
		"x-notion-space-id":           c.Session.SpaceID,
		"accept-language":             c.acceptLanguageHeader(),
		"cookie":                      c.cookieHeader(),
		"origin":                      upstream.OriginURL,
		"referer":                     firstNonEmpty(strings.TrimSpace(referer), upstream.AIURL()),
		"user-agent":                  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36",
		"sec-ch-ua":                   "\"Google Chrome\";v=\"145\", \"Not?A_Brand\";v=\"8\", \"Chromium\";v=\"145\"",
		"sec-ch-ua-mobile":            "?0",
		"sec-ch-ua-platform":          "\"Windows\"",
		"sec-fetch-dest":              "empty",
		"sec-fetch-mode":              "cors",
		"sec-fetch-site":              "same-origin",
	}
}

func randomUUID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

// shortID — 生成安全的短随机 ID（hex，长度 min(n, 32)）。
// 替代散落的 strings.ReplaceAll(randomUUID(), "-", "")[:N] 裸切片：
// 若 randomUUID 实现变更导致字符串变短，裸切片会 panic（在 ping goroutine 里即进程崩溃）。
func shortID(n int) string {
	s := strings.ReplaceAll(randomUUID(), "-", "")
	if n <= 0 {
		return s
	}
	if len(s) > n {
		return s[:n]
	}
	return s
}

func canonicalUUIDString(value string) (string, bool) {
	clean := strings.ToLower(strings.TrimSpace(value))
	if len(clean) != 36 {
		return "", false
	}
	for idx, ch := range clean {
		switch idx {
		case 8, 13, 18, 23:
			if ch != '-' {
				return "", false
			}
		default:
			if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
				return "", false
			}
		}
	}
	return clean, true
}

func prefixedTranscriptStepIDToUUID(value string) (string, bool) {
	match := prefixedTranscriptStepIDPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 2 {
		return "", false
	}
	hexValue := strings.ToLower(match[1])
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexValue[0:8], hexValue[8:12], hexValue[12:16], hexValue[16:20], hexValue[20:32]), true
}

func normalizeTranscriptStepID(value string) string {
	if uuid, ok := canonicalUUIDString(value); ok {
		return uuid
	}
	if uuid, ok := prefixedTranscriptStepIDToUUID(value); ok {
		return uuid
	}
	return randomUUID()
}

func extractStepText(value any) string {
	if parts := sliceValue(value); len(parts) > 0 {
		textParts := make([]string, 0, len(parts))
		for _, raw := range parts {
			item := mapValue(raw)
			partType := strings.ToLower(strings.TrimSpace(stringValue(item["type"])))
			if partType != "text" {
				continue
			}
			if content := strings.TrimSpace(extractAssistantPartContent(item)); content != "" {
				textParts = append(textParts, content)
			}
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "")
		}
		// Structured transcript parts without any `text` entry are usually
		// reasoning-only/tool-only snapshots. Falling back to generic content
		// extraction here leaks thinking into visible assistant text.
		return ""
	}
	if wrapper := mapValue(value); len(wrapper) > 0 {
		if parts := sliceValue(wrapper["value"]); len(parts) > 0 {
			textParts := make([]string, 0, len(parts))
			for _, raw := range parts {
				item := mapValue(raw)
				partType := strings.ToLower(strings.TrimSpace(stringValue(item["type"])))
				if partType != "text" {
					continue
				}
				if content := strings.TrimSpace(extractAssistantPartContent(item)); content != "" {
					textParts = append(textParts, content)
				}
			}
			if len(textParts) > 0 {
				return strings.Join(textParts, "")
			}
			return ""
		}
		switch strings.ToLower(strings.TrimSpace(stringValue(wrapper["type"]))) {
		case "thinking", "reasoning":
			return ""
		}
	}
	if text := strings.TrimSpace(extractAssistantPartContent(value)); text != "" {
		return text
	}
	return strings.TrimSpace(extractUserStepText(value))
}

func extractStepReasoning(value any) string {
	if parts := sliceValue(value); len(parts) > 0 {
		reasoning := []string{}
		for _, raw := range parts {
			item := mapValue(raw)
			partType := strings.ToLower(strings.TrimSpace(stringValue(item["type"])))
			if partType != "thinking" && partType != "reasoning" {
				continue
			}
			if content := strings.TrimSpace(extractAssistantPartContent(item)); content != "" {
				reasoning = append(reasoning, content)
			}
		}
		if len(reasoning) > 0 {
			return strings.Join(reasoning, "")
		}
	}
	if wrapper := mapValue(value); len(wrapper) > 0 {
		for _, key := range []string{"reasoning", "thinking", "thought", "analysis"} {
			if nested := strings.TrimSpace(extractAssistantPartContent(wrapper[key])); nested != "" {
				return nested
			}
		}
		if parts := sliceValue(wrapper["value"]); len(parts) > 0 {
			reasoning := []string{}
			for _, raw := range parts {
				item := mapValue(raw)
				partType := strings.ToLower(strings.TrimSpace(stringValue(item["type"])))
				if partType != "thinking" && partType != "reasoning" {
					continue
				}
				if content := strings.TrimSpace(extractAssistantPartContent(item)); content != "" {
					reasoning = append(reasoning, content)
				}
			}
			if len(reasoning) > 0 {
				return strings.Join(reasoning, "")
			}
		}
	}
	return ""
}

func (c *NotionAIClient) postJSON(ctx context.Context, url string, payload map[string]any, contentType string) ([]byte, error) {
	return c.postJSONWithReferer(ctx, url, payload, contentType, "")
}

func (c *NotionAIClient) postJSONWithReferer(ctx context.Context, url string, payload map[string]any, contentType string, referer string) ([]byte, error) {
	resp, err := c.postJSONResponseWithReferer(ctx, url, payload, contentType, referer)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return respBody, nil
}

func (c *NotionAIClient) postJSONResponse(ctx context.Context, url string, payload map[string]any, contentType string) (*http.Response, error) {
	return c.postJSONResponseWithReferer(ctx, url, payload, contentType, "")
}

func (c *NotionAIClient) postJSONResponseWithReferer(ctx context.Context, url string, payload map[string]any, contentType string, refererOverride string) (*http.Response, error) {
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/json"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	accept := "application/json"
	if strings.Contains(strings.ToLower(strings.TrimSpace(contentType)), "application/x-ndjson") {
		accept = "application/x-ndjson"
	}
	referer := strings.TrimSpace(refererOverride)
	if referer == "" {
		referer = c.requestReferer(url, payload)
	}
	headers := c.baseHeaders(accept, referer)
	for key, value := range headers {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		req.Header.Set(key, value)
	}
	req.Header.Set("content-type", "application/json")
	if c.ProxyResolver != nil {
		if _, extraHeaders, resolveErr := c.ProxyResolver.ResolveProxyForRequest(c.AccountEmail, req.URL); resolveErr == nil {
			for key, value := range extraHeaders {
				if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
					continue
				}
				req.Header.Set(key, value)
			}
		}
	}
	c.captureDebugUpstreamRequestFromHeader(url, req.Header, payload, body)
	c.Config.NotionUpstream().ApplyHost(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, readErr
		}
		return nil, &notionAPIError{
			URL:        url,
			StatusCode: resp.StatusCode,
			Message:    strings.TrimSpace(string(respBody)),
		}
	}
	return resp, nil
}

func isTrustRuleDeniedInferenceError(err error) bool {
	if err == nil {
		return false
	}
	var stepErr *inferenceStepError
	if !errors.As(err, &stepErr) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(stepErr.SubType), "trust-rule-denied")
}

func (c *NotionAIClient) runInferenceTranscriptHTTP(ctx context.Context, payload map[string]any, threadID string, sink InferenceStreamSink) (ndjsonParseResult, error) {
	resp, err := c.postJSONResponse(ctx, c.Config.NotionUpstream().API("runInferenceTranscript"), payload, "application/x-ndjson")
	if err != nil {
		return ndjsonParseResult{}, err
	}
	defer resp.Body.Close()

	stopKeepAlive := make(chan struct{})
	defer close(stopKeepAlive)
	if sink.KeepAlive != nil {
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-stopKeepAlive:
					return
				case <-ticker.C:
					_ = sink.EmitKeepAlive()
				}
			}
		}()
	}

	return consumeNDJSONStreamWithIdleClose(resp.Body, threadID, sink, ndjsonIdleAfterAnswerTimeout)
}

func (c *NotionAIClient) runInferenceTranscriptInBrowser(ctx context.Context, payload map[string]any) (string, error) {
	return runInferenceTranscriptInBrowser(ctx, c, payload)
}

func (c *NotionAIClient) runInferenceTranscriptWithFallback(ctx context.Context, payload map[string]any, threadID string, sink InferenceStreamSink) (ndjsonParseResult, error) {
	if c.Config.DebugUpstream {
		log.Printf("[debug_upstream] runInferenceTranscript http start thread_id=%s", threadID)
	}
	callStartedAt := time.Now()
	parsed, err := c.runInferenceTranscriptHTTP(ctx, payload, threadID, sink)
	observeTransportCallDuration(time.Since(callStartedAt))
	if c.Config.DebugUpstream {
		log.Printf("[debug_upstream] runInferenceTranscript http done thread_id=%s line_count=%d message_ids=%d err=%v", threadID, parsed.LineCount, len(parsed.MessageIDs), err)
	}
	if !isTrustRuleDeniedInferenceError(err) {
		return parsed, err
	}

	runFallback := c.browserRunInferenceFallback
	if runFallback == nil && !c.supportsBrowserRunInferenceFallback() {
		return parsed, err
	}
	if runFallback == nil {
		runFallback = c.runInferenceTranscriptInBrowser
	}
	fallbackTimeout := browserFallbackTimeoutForPayload(ctx, payload)
	payloadBytes := browserFallbackPayloadBytes(payload)
	fallbackCtx := ctx
	fallbackCancel := func() {}
	if fallbackTimeout > 0 {
		fallbackCtx, fallbackCancel = context.WithTimeout(ctx, fallbackTimeout)
	}
	defer fallbackCancel()
	if c.Config.DebugUpstream {
		log.Printf("[debug_upstream] runInferenceTranscript browser fallback start thread_id=%s timeout=%s payload_bytes=%d", threadID, fallbackTimeout, payloadBytes)
	}
	body, fallbackErr := runFallback(fallbackCtx, payload)
	if fallbackErr != nil {
		c.logBestEffortFailure("runInferenceTranscriptInBrowser", fallbackErr)
		c.logRunInferenceTranscriptBrowserFallbackFailure(threadID, fallbackTimeout, payloadBytes, fallbackErr)
		message := formatRunInferenceTranscriptBrowserFallbackError(err, fallbackErr, fallbackTimeout, payloadBytes)
		return ndjsonParseResult{}, &inferenceTransportError{Message: message}
	}
	if c.Config.DebugUpstream {
		log.Printf("[debug_upstream] runInferenceTranscript browser fallback body thread_id=%s bytes=%d", threadID, len(body))
	}
	if formatErr := detectInferenceStreamResponseFormat(body); formatErr != nil {
		if c.Config.DebugUpstream {
			log.Printf("[debug_upstream] runInferenceTranscript browser fallback rejected thread_id=%s err=%v", threadID, formatErr)
		}
		return ndjsonParseResult{}, formatErr
	}
	return consumeNDJSONStream(strings.NewReader(body), threadID, sink)
}

func formatRunInferenceTranscriptBrowserFallbackError(upstreamErr error, fallbackErr error, fallbackTimeout time.Duration, payloadBytes int) string {
	switch {
	case errors.Is(fallbackErr, context.DeadlineExceeded):
		return fmt.Sprintf("%v; browser fallback timed out after %s before response stream completed (payload_bytes=%d)", upstreamErr, fallbackTimeout, payloadBytes)
	case errors.Is(fallbackErr, context.Canceled):
		return fmt.Sprintf("%v; request was canceled by caller/client before browser fallback completed (payload_bytes=%d)", upstreamErr, payloadBytes)
	default:
		return fmt.Sprintf("%v; browser fallback failed: %v", upstreamErr, fallbackErr)
	}
}

func (c *NotionAIClient) logRunInferenceTranscriptBrowserFallbackFailure(threadID string, fallbackTimeout time.Duration, payloadBytes int, fallbackErr error) {
	if !c.Config.DebugUpstream || fallbackErr == nil {
		return
	}
	switch {
	case errors.Is(fallbackErr, context.DeadlineExceeded):
		log.Printf("[debug_upstream] runInferenceTranscript browser fallback timed out thread_id=%s timeout=%s payload_bytes=%d err=%v", threadID, fallbackTimeout, payloadBytes, fallbackErr)
	case errors.Is(fallbackErr, context.Canceled):
		log.Printf("[debug_upstream] runInferenceTranscript browser fallback canceled thread_id=%s payload_bytes=%d reason=caller/client canceled request err=%v", threadID, payloadBytes, fallbackErr)
	default:
		log.Printf("[debug_upstream] runInferenceTranscript browser fallback failed thread_id=%s timeout=%s payload_bytes=%d err=%v", threadID, fallbackTimeout, payloadBytes, fallbackErr)
	}
}

func (c *NotionAIClient) captureDebugUpstreamRequest(url string, headers map[string]string, payload map[string]any, body []byte) {
	if !c.Config.DebugUpstream {
		return
	}
	bodyPath := ""
	metaPath := ""
	switch {
	case strings.Contains(url, "runInferenceTranscript"):
		bodyPath = "tmp_last_runInferenceTranscript_body.json"
		metaPath = "tmp_last_runInferenceTranscript_meta.json"
	case strings.Contains(url, "saveTransactionsFanout"):
		bodyPath = "tmp_last_saveTransactionsFanout_body.json"
		metaPath = "tmp_last_saveTransactionsFanout_meta.json"
	default:
		return
	}
	meta := map[string]any{
		"url": url,
		"headers": map[string]any{
			"accept":                      strings.TrimSpace(headers["accept"]),
			"accept-language":             strings.TrimSpace(headers["accept-language"]),
			"content-type":                "application/json",
			"notion-client-version":       strings.TrimSpace(headers["notion-client-version"]),
			"notion-audit-log-platform":   strings.TrimSpace(headers["notion-audit-log-platform"]),
			"origin":                      strings.TrimSpace(headers["origin"]),
			"referer":                     strings.TrimSpace(headers["referer"]),
			"user-agent":                  strings.TrimSpace(headers["user-agent"]),
			"x-notion-active-user-header": strings.TrimSpace(headers["x-notion-active-user-header"]),
			"x-notion-space-id":           strings.TrimSpace(headers["x-notion-space-id"]),
			"sec-ch-ua":                   strings.TrimSpace(headers["sec-ch-ua"]),
			"sec-ch-ua-mobile":            strings.TrimSpace(headers["sec-ch-ua-mobile"]),
			"sec-ch-ua-platform":          strings.TrimSpace(headers["sec-ch-ua-platform"]),
			"sec-fetch-dest":              strings.TrimSpace(headers["sec-fetch-dest"]),
			"sec-fetch-mode":              strings.TrimSpace(headers["sec-fetch-mode"]),
			"sec-fetch-site":              strings.TrimSpace(headers["sec-fetch-site"]),
		},
		"payload": payload,
	}
	if err := os.WriteFile(bodyPath, body, 0o600); err != nil {
		log.Printf("[debug_upstream] write request body failed: %v", err)
	}
	if metaBytes, err := json.MarshalIndent(meta, "", "  "); err != nil {
		log.Printf("[debug_upstream] encode request meta failed: %v", err)
	} else if err := os.WriteFile(metaPath, metaBytes, 0o600); err != nil {
		log.Printf("[debug_upstream] write request meta failed: %v", err)
	}
}

func (c *NotionAIClient) captureDebugUpstreamRequestFromHeader(url string, header http.Header, payload map[string]any, body []byte) {
	headers := map[string]string{}
	for key, values := range header {
		if len(values) == 0 {
			continue
		}
		headers[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(values[0])
	}
	c.captureDebugUpstreamRequest(url, headers, payload, body)
}

func isoNowMillis() string {
	return time.Now().Format("2006-01-02T15:04:05.000Z07:00")
}

func (c *NotionAIClient) buildSearchScopes() []map[string]any {
	scopes := c.Config.Features.SearchScopes
	out := make([]map[string]any, 0, len(scopes))
	for _, scope := range scopes {
		clean := strings.TrimSpace(scope)
		if clean == "" {
			continue
		}
		out = append(out, map[string]any{"type": clean})
	}
	return out
}

func (c *NotionAIClient) buildDefaultWorkflowConfigValue(threadType string, useWebSearch bool, notionModel string) map[string]any {
	readOnly := c.Config.Features.UseReadOnlyMode || c.Config.Features.ForceDisableUpstreamEdits
	enableUpstreamEdits := !c.Config.Features.ForceDisableUpstreamEdits && !readOnly
	searchScopes := []map[string]any{}
	if useWebSearch {
		searchScopes = c.buildSearchScopes()
		if len(searchScopes) == 0 {
			searchScopes = []map[string]any{{"type": "everything"}}
		}
	}
	configValue := map[string]any{
		"type":                                           threadType,
		"enableAgentAutomations":                         true,
		"enableAgentIntegrations":                        true,
		"enableCustomAgents":                             true,
		"enableExperimentalIntegrations":                 false,
		"enableAgentDiffs":                               true,
		"enableAgentUpdatePagePatch":                     enableUpstreamEdits,
		"enableAgentCreateDbTemplate":                    true,
		"enableCsvAttachmentSupport":                     c.Config.Features.EnableCsvAttachmentSupport,
		"enableDatabaseAgents":                           false,
		"showDatabaseAgentsDiscoverability":              false,
		"enableAgentThreadTools":                         true,
		"enableCrdtOperations":                           false,
		"enableAgentCardCustomization":                   true,
		"enableSystemPromptAsPage":                       false,
		"enableUserSessionContext":                       false,
		"enableScriptAgentAdvanced":                      false,
		"enableScriptAgent":                              true,
		"enableScriptAgentSearchConnectorsInCustomAgent": false,
		"enableScriptAgentGoogleDriveInCustomAgent":      false,
		"enableScriptAgentGoogleDriveOAuthInCustomAgent": false,
		"enableScriptAgentSlack":                         true,
		"enableScriptAgentMcpServers":                    false,
		"enableScriptAgentMail":                          true,
		"enableScriptAgentCalendar":                      true,
		"enableScriptAgentCustomToolCalling":             false,
		"enableCreateAndRunThread":                       true,
		"enableSoftwareFactoryPage":                      false,
		"enableAgentGenerateImage":                       c.Config.Features.EnableGenerateImage,
		"enableSpeculativeSearch":                        false,
		"enableQueryCalendar":                            false,
		"enableQueryMail":                                false,
		"enableMailExplicitToolCalls":                    true,
		"enableMailNotificationPreferences":              false,
		"enableMailAgentMultiProviderSupport":            false,
		"useRulePrioritization":                          true,
		"availableConnectors":                            []any{},
		"customConnectorInfo":                            []any{},
		"searchScopes":                                   searchScopes,
		"useSearchToolV2":                                false,
		"enableUnifiedSearch":                            false,
		"useWebSearch":                                   useWebSearch,
		"isHipaa":                                        false,
		"yoloMode":                                       false,
		"useReadOnlyMode":                                readOnly,
		"writerMode":                                     c.Config.Features.WriterMode && !readOnly,
		"modelFromUser":                                  strings.TrimSpace(notionModel) != "",
		"isCustomAgent":                                  false,
		"isCustomAgentBuilder":                           false,
		"isAgentResearchRequest":                         false,
		"useCustomAgentDraft":                            false,
		"use_draft_actor_pointer":                        false,
		"enableUpdatePageAutofixer":                      enableUpstreamEdits,
		"enableMarkdownVNext":                            false,
		"enableUpdatePageOrderUpdates":                   enableUpstreamEdits,
		"enableAgentSupportPropertyReorder":              enableUpstreamEdits,
		"agentShortUpdatePageResult":                     false,
		"enableAgentAskSurvey":                           true,
		"databaseAgentConfigMode":                        false,
		"isOnboardingAgent":                              false,
		"isMobile":                                       false,
	}
	if clean := strings.TrimSpace(notionModel); clean != "" {
		configValue["model"] = clean
	}
	return configValue
}

func cloneAnySlice(values []any) []any {
	if len(values) == 0 {
		return []any{}
	}
	out := make([]any, len(values))
	copy(out, values)
	return out
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func cloneMapAny(values map[string]any) map[string]any {
	if len(values) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func attachmentMetadataValueMissing(value any) bool {
	switch x := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case json.Number:
		text := strings.TrimSpace(x.String())
		return text == "" || text == "0"
	case int:
		return x == 0
	case int64:
		return x == 0
	case float64:
		return x == 0
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	default:
		return false
	}
}

func buildAttachmentStepMetadata(uploaded UploadedAttachment) map[string]any {
	source := cloneMapAny(uploaded.Metadata)
	nested := cloneMapAny(mapValue(source["stepMetadata"]))
	metadata := source
	delete(metadata, "stepMetadata")
	delete(metadata, "attachmentRisk")
	if len(nested) > 0 {
		if value, ok := nested["guardrail"]; ok {
			metadata["guardrail"] = value
		}
		if value, ok := nested["estimatedTokens"]; ok && !attachmentMetadataValueMissing(value) {
			metadata["estimatedTokens"] = value
		}
		if value, ok := nested["fileSizeBytes"]; ok && !attachmentMetadataValueMissing(value) {
			metadata["fileSizeBytes"] = value
		}
		if value, ok := nested["aiTraceId"]; ok && strings.TrimSpace(stringValue(value)) != "" {
			metadata["aiTraceId"] = value
		}
		for _, key := range []string{"numRows", "numFields", "truncatedContent", "wasTruncated"} {
			if value, ok := nested[key]; ok {
				metadata[key] = value
			}
		}
		for _, key := range []string{"contentType", "width", "height", "moderation"} {
			if attachmentMetadataValueMissing(metadata[key]) {
				if value, ok := nested[key]; ok && !attachmentMetadataValueMissing(value) {
					metadata[key] = value
				}
			}
		}
	}
	if uploaded.SizeBytes > 0 {
		if attachmentMetadataValueMissing(metadata["fileSizeBytes"]) {
			metadata["fileSizeBytes"] = uploaded.SizeBytes
		}
	}
	if attachmentMetadataValueMissing(metadata["contentType"]) && strings.TrimSpace(uploaded.ContentType) != "" {
		metadata["contentType"] = uploaded.ContentType
	}
	if attachmentMetadataValueMissing(metadata["attachmentSource"]) {
		metadata["attachmentSource"] = "user_upload"
	}
	if attachmentMetadataValueMissing(metadata["estimatedTokens"]) {
		metadata["estimatedTokens"] = map[string]any{
			"openai":    0,
			"anthropic": 0,
		}
	}
	if _, ok := metadata["truncatedContent"]; !ok {
		metadata["truncatedContent"] = ""
	}
	if _, ok := metadata["wasTruncated"]; !ok {
		metadata["wasTruncated"] = false
	}
	if attachmentMetadataValueMissing(metadata["aiTraceId"]) {
		metadata["aiTraceId"] = randomUUID()
	}
	return metadata
}

func compactValuePreview(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return truncateRunes(collapseWhitespace(typed), 280)
	default:
		payload, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return truncateRunes(collapseWhitespace(string(payload)), 280)
	}
}

func timeFromTranscriptValue(value any) time.Time {
	switch typed := value.(type) {
	case time.Time:
		if typed.IsZero() {
			return time.Time{}
		}
		return typed.UTC()
	case int64:
		if typed <= 0 {
			return time.Time{}
		}
		return time.UnixMilli(typed).UTC()
	case int:
		if typed <= 0 {
			return time.Time{}
		}
		return time.UnixMilli(int64(typed)).UTC()
	case float64:
		if typed <= 0 {
			return time.Time{}
		}
		return time.UnixMilli(int64(typed)).UTC()
	case json.Number:
		if parsed, err := typed.Int64(); err == nil && parsed > 0 {
			return time.UnixMilli(parsed).UTC()
		}
	case string:
		clean := strings.TrimSpace(typed)
		if clean == "" {
			return time.Time{}
		}
		if parsedInt, err := strconv.ParseInt(clean, 10, 64); err == nil && parsedInt > 0 {
			return time.UnixMilli(parsedInt).UTC()
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z07:00"} {
			if parsed, err := time.Parse(layout, clean); err == nil {
				return parsed.UTC()
			}
		}
	}
	return time.Time{}
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value.UTC()
		}
	}
	return time.Time{}
}

func collectStringLeaves(value any, out *[]string) {
	switch typed := value.(type) {
	case string:
		clean := strings.TrimSpace(typed)
		if clean != "" {
			*out = append(*out, clean)
		}
	case []any:
		for _, item := range typed {
			collectStringLeaves(item, out)
		}
	case map[string]any:
		for _, key := range []string{"text", "label", "title", "content"} {
			if nested, ok := typed[key]; ok {
				collectStringLeaves(nested, out)
			}
		}
	}
}

func collectAssistantContentLeaves(value any, out *[]string) {
	switch typed := value.(type) {
	case string:
		if typed != "" {
			*out = append(*out, typed)
		}
	case []any:
		for _, item := range typed {
			collectAssistantContentLeaves(item, out)
		}
	case map[string]any:
		for _, key := range []string{"content", "text", "title", "label", "value"} {
			if nested, ok := typed[key]; ok {
				collectAssistantContentLeaves(nested, out)
			}
		}
	}
}

func flattenStringLeaves(value any) string {
	parts := []string{}
	collectStringLeaves(value, &parts)
	if len(parts) == 0 {
		return ""
	}
	return truncateRunes(collapseWhitespace(strings.Join(parts, " ")), 200)
}

func extractAssistantPartContent(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	}
	parts := []string{}
	collectAssistantContentLeaves(value, &parts)
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}

func extractUserStepText(value any) string {
	lines := []string{}
	collectStringLeaves(value, &lines)
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	}
	return 0
}

func extractConversationMessageFromThreadRecord(messageID string, rawItem any) (ConversationMessage, bool) {
	item := mapValue(rawItem)
	valueWrapper := mapValue(item["value"])
	value := mapValue(valueWrapper["value"])
	step := mapValue(value["step"])
	if step == nil {
		return ConversationMessage{}, false
	}
	data := mapValue(value["data"])
	createdAt := firstNonZeroTime(
		timeFromTranscriptValue(valueWrapper["created_time"]),
		timeFromTranscriptValue(valueWrapper["created_at"]),
		timeFromTranscriptValue(step["createdAt"]),
		timeFromTranscriptValue(data["completed_time"]),
	)
	updatedAt := firstNonZeroTime(
		timeFromTranscriptValue(valueWrapper["last_edited_time"]),
		timeFromTranscriptValue(valueWrapper["updated_at"]),
		timeFromTranscriptValue(data["completed_time"]),
		createdAt,
	)
	stepType := strings.TrimSpace(stringValue(step["type"]))
	switch stepType {
	case "user":
		return ConversationMessage{
			ID:        strings.TrimSpace(messageID),
			Role:      "user",
			Status:    "completed",
			Content:   extractUserStepText(step["value"]),
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		}, true
	case "agent-inference":
		completed, _ := data["completed"].(bool)
		status := "streaming"
		if completed {
			status = "completed"
		}
		return ConversationMessage{
			ID:        strings.TrimSpace(messageID),
			Role:      "assistant",
			Status:    status,
			Content:   sanitizeAssistantVisibleText(extractStepText(step["value"])),
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		}, true
	case "attachment":
		attachment := ConversationAttachment{
			Name:        strings.TrimSpace(stringValue(step["fileName"])),
			ContentType: strings.TrimSpace(stringValue(step["contentType"])),
			Source:      "notion",
			URL:         strings.TrimSpace(stringValue(step["fileUrl"])),
		}
		return ConversationMessage{
			ID:          strings.TrimSpace(messageID),
			Role:        "user",
			Status:      "completed",
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
			Attachments: []ConversationAttachment{attachment},
		}, true
	default:
		return ConversationMessage{}, false
	}
}

func messageIDsFromRecordMap(recordMap map[string]any, threadID string) []string {
	if recordMap == nil {
		return nil
	}
	return messageIDsFromThreadRecord(map[string]any{"recordMap": recordMap}, threadID)
}

func extractContinuationDraftFromThreadMessages(threadMessages map[string]any, messageIDs []string) *continuationTurnDraft {
	if len(messageIDs) == 0 {
		return nil
	}
	draft := &continuationTurnDraft{}
	for _, messageID := range messageIDs {
		item := mapValue(threadMessages[messageID])
		valueWrapper := mapValue(item["value"])
		value := mapValue(valueWrapper["value"])
		step := mapValue(value["step"])
		stepType := strings.TrimSpace(stringValue(step["type"]))
		switch stepType {
		case "config":
			draft.ConfigID = firstNonEmpty(strings.TrimSpace(stringValue(step["id"])), strings.TrimSpace(messageID))
			draft.ConfigValue = cloneMapAny(mapValue(step["value"]))
		case "context":
			draft.ContextID = firstNonEmpty(strings.TrimSpace(stringValue(step["id"])), strings.TrimSpace(messageID))
			contextValue := mapValue(step["value"])
			draft.ContextValue = cloneMapAny(contextValue)
			if draft.OriginalDatetime == "" {
				draft.OriginalDatetime = strings.TrimSpace(stringValue(contextValue["currentDatetime"]))
			}
		case "updated-config":
			draft.UpdatedConfigIDs = append(draft.UpdatedConfigIDs, firstNonEmpty(strings.TrimSpace(stringValue(step["id"])), strings.TrimSpace(messageID)))
			draft.LastUpdatedConfigValue = cloneMapAny(mapValue(step["value"]))
		}
	}
	if draft.ConfigID == "" && draft.ContextID == "" && len(draft.UpdatedConfigIDs) == 0 {
		return nil
	}
	return draft
}

func mergeContinuationDraft(preferred *continuationTurnDraft, live *continuationTurnDraft) *continuationTurnDraft {
	if live == nil {
		return preferred
	}
	if preferred == nil {
		return live
	}
	live.SessionID = firstNonEmpty(strings.TrimSpace(live.SessionID), strings.TrimSpace(preferred.SessionID))
	live.Fingerprint = firstNonEmpty(strings.TrimSpace(live.Fingerprint), strings.TrimSpace(preferred.Fingerprint))
	live.OriginalDatetime = firstNonEmpty(strings.TrimSpace(live.OriginalDatetime), strings.TrimSpace(preferred.OriginalDatetime))
	live.TurnCount = maxInt(live.TurnCount, preferred.TurnCount)
	live.RawMessageCount = maxInt(live.RawMessageCount, preferred.RawMessageCount)
	if live.ConfigID == "" {
		live.ConfigID = preferred.ConfigID
	}
	if len(live.ConfigValue) == 0 {
		live.ConfigValue = cloneMapAny(preferred.ConfigValue)
	}
	if live.ContextID == "" {
		live.ContextID = preferred.ContextID
	}
	if len(live.ContextValue) == 0 {
		live.ContextValue = cloneMapAny(preferred.ContextValue)
	}
	if len(live.UpdatedConfigIDs) == 0 {
		live.UpdatedConfigIDs = cloneStringSlice(preferred.UpdatedConfigIDs)
	}
	if len(live.LastUpdatedConfigValue) == 0 {
		live.LastUpdatedConfigValue = cloneMapAny(preferred.LastUpdatedConfigValue)
	}
	return live
}

func finalAgentFromRecordMap(recordMap map[string]any, threadID string) ([]string, agentMessage, bool) {
	messageIDs, lastAgent, outcomeErr, ok := finalThreadOutcomeFromRecordMap(recordMap, threadID)
	if !ok || outcomeErr != nil {
		return messageIDs, agentMessage{}, false
	}
	return messageIDs, lastAgent, true
}

func finalThreadOutcomeFromRecordMap(recordMap map[string]any, threadID string) ([]string, agentMessage, error, bool) {
	messageIDs := messageIDsFromRecordMap(recordMap, threadID)
	if len(messageIDs) == 0 {
		return nil, agentMessage{}, nil, false
	}
	agentMap := extractAgentMessages(recordMap)
	errorMap := extractThreadErrors(recordMap, threadID)
	var lastAgent agentMessage
	var lastErr error
	var haveOutcome bool
	for _, messageID := range messageIDs {
		if stepErr, ok := errorMap[messageID]; ok {
			copyErr := stepErr
			lastErr = &copyErr
			lastAgent = agentMessage{}
			haveOutcome = true
			continue
		}
		msg, ok := agentMap[messageID]
		if !ok {
			continue
		}
		lastAgent = msg
		lastErr = nil
		haveOutcome = true
	}
	if !haveOutcome {
		return messageIDs, agentMessage{}, nil, false
	}
	if lastErr != nil {
		return messageIDs, agentMessage{}, lastErr, true
	}
	return messageIDs, lastAgent, nil, true
}

func parsePatchStepIndex(path string) (int, string, bool) {
	if !strings.HasPrefix(path, "/s/") {
		return 0, "", false
	}
	trimmed := strings.TrimPrefix(path, "/s/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return 0, "", false
	}
	index, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, "", false
	}
	rest := ""
	if len(parts) == 2 {
		rest = "/" + parts[1]
	}
	return index, rest, true
}

func (s *ndjsonTranscriptState) ensurePatchMaps() {
	if s.patchValueTypes == nil {
		s.patchValueTypes = map[string]string{}
	}
	if s.patchValueText == nil {
		s.patchValueText = map[string]string{}
	}
	if s.patchValueCounts == nil {
		s.patchValueCounts = map[string]int{}
	}
}

func patchStatePrefix(stepIndex int) string {
	return fmt.Sprintf("/s/%d", stepIndex)
}

func patchStateEntryKey(statePrefix string, valueIndex int) string {
	return fmt.Sprintf("%s/value/%d", statePrefix, valueIndex)
}

func normalizePatchEntryType(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "text":
		return "text"
	case "thinking", "reasoning":
		return "reasoning"
	default:
		return ""
	}
}

func patchEntryKeyFromRest(stepIndex int, rest string, field string) (string, bool) {
	switch field {
	case "/content":
		contentIndex := strings.LastIndex(rest, field)
		if contentIndex < 0 {
			return "", false
		}
		entryPath := rest[:contentIndex]
		if entryPath == "" || !strings.Contains(entryPath, "/value/") {
			return "", false
		}
		return fmt.Sprintf("/s/%d%s", stepIndex, entryPath), true
	default:
		if !strings.HasSuffix(rest, field) {
			return "", false
		}
		entryPath := strings.TrimSuffix(rest, field)
		if entryPath == rest || !strings.Contains(entryPath, "/value/") {
			return "", false
		}
		return fmt.Sprintf("/s/%d%s", stepIndex, entryPath), true
	}
}

func parsePatchValueRemovalIndex(rest string) (int, bool) {
	if !strings.HasPrefix(rest, "/value/") {
		return 0, false
	}
	tail := strings.TrimPrefix(rest, "/value/")
	if tail == "" || strings.Contains(tail, "/") {
		return 0, false
	}
	index, err := strconv.Atoi(tail)
	if err != nil || index < 0 {
		return 0, false
	}
	return index, true
}

func (s *ndjsonTranscriptState) composeStepAgentContent(stepIndex int) (string, bool, string, bool) {
	s.ensurePatchMaps()
	statePrefix := patchStatePrefix(stepIndex)
	count := s.patchValueCounts[statePrefix]
	if count <= 0 {
		return "", false, "", false
	}
	textParts := make([]string, 0, count)
	reasoningParts := make([]string, 0, count)
	for valueIndex := 0; valueIndex < count; valueIndex++ {
		entryKey := patchStateEntryKey(statePrefix, valueIndex)
		entryType := normalizePatchEntryType(s.patchValueTypes[entryKey])
		if entryType == "" {
			continue
		}
		content := s.patchValueText[entryKey]
		if content == "" {
			continue
		}
		switch entryType {
		case "text":
			textParts = append(textParts, content)
		case "reasoning":
			reasoningParts = append(reasoningParts, content)
		}
	}
	text := ""
	for _, part := range textParts {
		text = combineAgentContentParts(text, part)
	}
	reasoning := ""
	for _, part := range reasoningParts {
		reasoning = combineAgentContentParts(reasoning, part)
	}
	return text, len(textParts) > 0, reasoning, len(reasoningParts) > 0
}

func (s *ndjsonTranscriptState) refreshAgentStepFromPatchState(stepIndex int, sink InferenceStreamSink) error {
	if stepIndex < 0 || stepIndex >= len(s.Steps) {
		return nil
	}
	text, hasText, reasoning, hasReasoning := s.composeStepAgentContent(stepIndex)
	if !hasText && !hasReasoning {
		return nil
	}
	step := s.Steps[stepIndex]
	s.Steps[stepIndex] = step
	if hasReasoning {
		step.Reasoning = reasoning
		s.Steps[stepIndex] = step
		if err := s.emitFullReasoning(s.composeReasoningText(), sink); err != nil {
			return err
		}
	}
	if hasText {
		step.Text = text
		s.Steps[stepIndex] = step
		if err := s.emitFullText(step.Text, sink); err != nil {
			return err
		}
	}
	return nil
}

func (s *ndjsonTranscriptState) mergeEventValueIntoPatchState(stepIndex int, valueIndex int, value ndjsonAgentInferenceValue) {
	entryType := normalizePatchEntryType(value.Type)
	if entryType == "" {
		return
	}
	s.ensurePatchMaps()
	statePrefix := patchStatePrefix(stepIndex)
	if s.patchValueCounts[statePrefix] <= valueIndex {
		s.patchValueCounts[statePrefix] = valueIndex + 1
	}
	entryKey := patchStateEntryKey(statePrefix, valueIndex)
	s.patchValueTypes[entryKey] = entryType
	if strings.TrimSpace(value.Content) != "" {
		s.patchValueText[entryKey] = mergeCumulativeAgentContent(s.patchValueText[entryKey], value.Content)
	}
}

func (s *ndjsonTranscriptState) registerStepValueTypes(stepIndex int, value any) {
	s.ensurePatchMaps()
	statePrefix := patchStatePrefix(stepIndex)
	parts := sliceValue(value)
	s.patchValueCounts[statePrefix] = len(parts)
	for valueIndex, rawPart := range parts {
		part := mapValue(rawPart)
		partType := normalizePatchEntryType(stringValue(part["type"]))
		if partType == "" {
			continue
		}
		entryKey := patchStateEntryKey(statePrefix, valueIndex)
		s.patchValueTypes[entryKey] = partType
		if content := extractAssistantPartContent(part); strings.TrimSpace(content) != "" {
			s.patchValueText[entryKey] = mergeCumulativeAgentContent(s.patchValueText[entryKey], content)
		}
	}
}

func (s *ndjsonTranscriptState) registerPatchValueAppend(path string, rawValue any) {
	valueIndexMarker := strings.Index(path, "/value/")
	if valueIndexMarker < 0 {
		return
	}
	s.ensurePatchMaps()
	statePrefix := path[:valueIndexMarker]
	part := mapValue(rawValue)
	partType := normalizePatchEntryType(stringValue(part["type"]))
	if partType == "" {
		return
	}
	valueIndex := s.patchValueCounts[statePrefix]
	s.patchValueCounts[statePrefix] = valueIndex + 1
	entryKey := patchStateEntryKey(statePrefix, valueIndex)
	s.patchValueTypes[entryKey] = partType
	if content := extractAssistantPartContent(part); strings.TrimSpace(content) != "" {
		s.patchValueText[entryKey] = mergeCumulativeAgentContent(s.patchValueText[entryKey], content)
	}
}

func (s *ndjsonTranscriptState) removePatchValueEntry(stepIndex int, valueIndex int) {
	if valueIndex < 0 {
		return
	}
	s.ensurePatchMaps()
	statePrefix := patchStatePrefix(stepIndex)
	count := s.patchValueCounts[statePrefix]
	if count <= 0 || valueIndex >= count {
		return
	}
	for idx := valueIndex; idx < count-1; idx++ {
		currentKey := patchStateEntryKey(statePrefix, idx)
		nextKey := patchStateEntryKey(statePrefix, idx+1)
		nextType, hasNextType := s.patchValueTypes[nextKey]
		nextText, hasNextText := s.patchValueText[nextKey]
		if hasNextType {
			s.patchValueTypes[currentKey] = nextType
		} else {
			delete(s.patchValueTypes, currentKey)
		}
		if hasNextText {
			s.patchValueText[currentKey] = nextText
		} else {
			delete(s.patchValueText, currentKey)
		}
	}
	lastKey := patchStateEntryKey(statePrefix, count-1)
	delete(s.patchValueTypes, lastKey)
	delete(s.patchValueText, lastKey)
	s.patchValueCounts[statePrefix] = count - 1
}

func (s *ndjsonTranscriptState) patchEntryType(stepIndex int, rest string) string {
	entryKey, ok := patchEntryKeyFromRest(stepIndex, rest, "/content")
	if !ok {
		return ""
	}
	s.ensurePatchMaps()
	if entryType := normalizePatchEntryType(s.patchValueTypes[entryKey]); entryType != "" {
		return entryType
	}
	return ""
}

func (s *ndjsonTranscriptState) composeReasoningText() string {
	parts := make([]string, 0, len(s.Steps))
	for _, step := range s.Steps {
		if strings.TrimSpace(step.Reasoning) == "" {
			continue
		}
		parts = append(parts, sanitizeAssistantVisibleText(step.Reasoning))
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n\n")
	}
	return sanitizeAssistantVisibleText(s.FinalAgent.Reasoning)
}

func (s *ndjsonTranscriptState) emitFullText(fullText string, sink InferenceStreamSink) error {
	fullText = sanitizeAssistantVisibleText(fullText)
	delta := textDeltaSuffix(s.EmittedText, fullText)
	if delta != "" {
		if err := sink.EmitText(delta); err != nil {
			return err
		}
	}
	if fullText != "" || s.EmittedText == "" {
		s.EmittedText = fullText
	}
	if s.ActiveAgentIndex >= 0 && s.ActiveAgentIndex < len(s.Steps) {
		step := s.Steps[s.ActiveAgentIndex]
		if step.ID != "" {
			s.FinalAgent.MessageID = step.ID
		}
	}
	s.FinalAgent.Text = fullText
	return nil
}

func (s *ndjsonTranscriptState) emitFullReasoning(fullReasoning string, sink InferenceStreamSink) error {
	fullReasoning = strings.TrimSpace(fullReasoning)
	delta := textDeltaSuffix(s.EmittedReasoning, fullReasoning)
	if delta != "" {
		if err := sink.EmitReasoning(delta); err != nil {
			return err
		}
	}
	if fullReasoning != "" || s.EmittedReasoning == "" {
		s.EmittedReasoning = fullReasoning
	}
	s.FinalAgent.Reasoning = fullReasoning
	return nil
}

func (s *ndjsonTranscriptState) mergeFinalAgent(agent agentMessage, sink InferenceStreamSink) error {
	if agent.MessageID != "" {
		s.FinalAgent.MessageID = agent.MessageID
	}
	s.FinalAgent.Completed = agent.Completed
	s.FinalAgent.CompletedTime = agent.CompletedTime
	reasoningText := s.composeReasoningText()
	agentReasoning := sanitizeAssistantVisibleText(agent.Reasoning)
	if strings.TrimSpace(reasoningText) == "" || len([]rune(agentReasoning)) > len([]rune(reasoningText)) {
		reasoningText = agentReasoning
	}
	if strings.TrimSpace(reasoningText) == "" {
		reasoningText = agent.Reasoning
	}
	if strings.TrimSpace(reasoningText) != "" {
		s.FinalAgent.Reasoning = reasoningText
		if err := s.emitFullReasoning(reasoningText, sink); err != nil {
			return err
		}
	}
	if strings.TrimSpace(agent.Text) == "" {
		return nil
	}
	if err := s.emitFullText(agent.Text, sink); err != nil {
		return err
	}
	s.FinalAgent.Completed = agent.Completed
	s.FinalAgent.CompletedTime = agent.CompletedTime
	return nil
}

func (s *ndjsonTranscriptState) ensureAgentStep(stepID string) int {
	for index, step := range s.Steps {
		if step.ID == stepID {
			return index
		}
	}
	s.Steps = append(s.Steps, ndjsonStepState{
		ID:   stepID,
		Type: "agent-inference",
	})
	return len(s.Steps) - 1
}

func mergeCumulativeAgentContent(existing string, next string) string {
	if next == "" {
		return existing
	}
	if existing == "" || next == existing {
		return next
	}
	existingClean := sanitizeAssistantVisibleText(existing)
	nextClean := sanitizeAssistantVisibleText(next)
	switch {
	case nextClean != "" && nextClean == existingClean:
		return existing
	case nextClean != "" && (existingClean == "" || strings.HasPrefix(nextClean, existingClean)):
		return next
	case existingClean != "" && strings.HasPrefix(existingClean, nextClean):
		return existing
	case strings.HasPrefix(next, existing):
		return next
	case strings.HasPrefix(existing, next):
		return existing
	default:
		return next
	}
}

func combineAgentContentParts(existing string, next string) string {
	if next == "" {
		return existing
	}
	if existing == "" {
		return next
	}
	switch {
	case strings.HasPrefix(next, existing):
		return next
	case strings.HasPrefix(existing, next):
		return existing
	default:
		return existing + next
	}
}

func applyAgentPatchContent(existing string, op string, patch string) string {
	switch op {
	case "x":
		return appendAgentPatchDelta(existing, patch)
	case "p":
		return handleAgentPatchReplace(existing, patch)
	default:
		return mergeCumulativeAgentContent(existing, patch)
	}
}

func appendAgentPatchDelta(existing string, delta string) string {
	if delta == "" {
		return existing
	}
	if existing == "" {
		return delta
	}
	existingClean := sanitizeAssistantVisibleText(existing)
	deltaClean := sanitizeAssistantVisibleText(delta)
	switch {
	case strings.HasSuffix(existing, delta):
		return existing
	case deltaClean != "" && strings.HasSuffix(existingClean, deltaClean):
		return existing
	case strings.HasPrefix(delta, existing):
		return delta
	case deltaClean != "" && existingClean != "" && strings.HasPrefix(deltaClean, existingClean):
		return delta
	default:
		return existing + delta
	}
}

func handleAgentPatchReplace(current string, replacement string) string {
	if replacement == "" {
		return current
	}
	if current == "" || replacement == current {
		return replacement
	}
	if idx := strings.LastIndex(current, "<lang"); idx >= 0 {
		return current[:idx] + replacement
	}
	currentClean := sanitizeAssistantVisibleText(current)
	replacementClean := sanitizeAssistantVisibleText(replacement)
	if strings.HasPrefix(replacement, current) {
		return replacement
	}
	if replacementClean != "" && currentClean != "" && strings.HasPrefix(replacementClean, currentClean) {
		return replacement
	}
	return current + replacement
}

func (s *ndjsonTranscriptState) mergeAgentInferenceEvent(event ndjsonAgentInferenceEvent, sink InferenceStreamSink) error {
	index := s.ensureAgentStep(strings.TrimSpace(event.ID))
	s.ActiveAgentIndex = index
	step := s.Steps[index]
	if step.ID != "" {
		s.FinalAgent.MessageID = step.ID
	}
	for valueIndex, value := range event.Value {
		s.mergeEventValueIntoPatchState(index, valueIndex, value)
		switch strings.TrimSpace(strings.ToLower(value.Type)) {
		case "thinking", "reasoning":
			step.Reasoning = mergeCumulativeAgentContent(step.Reasoning, value.Content)
			s.Steps[index] = step
			if err := s.emitFullReasoning(s.composeReasoningText(), sink); err != nil {
				return err
			}
		case "text":
			step.Text = mergeCumulativeAgentContent(step.Text, value.Content)
			s.Steps[index] = step
			if err := s.emitFullText(step.Text, sink); err != nil {
				return err
			}
		}
	}
	if event.FinishedAt != nil {
		step.Completed = true
		s.Steps[index] = step
		s.FinalAgent.Completed = true
		s.FinalAgent.CompletedTime = event.FinishedAt
	}
	return nil
}

func (s *ndjsonTranscriptState) applyAgentPatchField(stepIndex int, rest string, opType string, rawValue any, sink InferenceStreamSink) (bool, error) {
	if entryKey, ok := patchEntryKeyFromRest(stepIndex, rest, "/type"); ok {
		s.ensurePatchMaps()
		s.patchValueTypes[entryKey] = normalizePatchEntryType(stringValue(rawValue))
		return true, s.refreshAgentStepFromPatchState(stepIndex, sink)
	}
	if entryKey, ok := patchEntryKeyFromRest(stepIndex, rest, "/content"); ok {
		s.ensurePatchMaps()
		if content := extractAssistantPartContent(rawValue); content != "" {
			s.patchValueText[entryKey] = applyAgentPatchContent(s.patchValueText[entryKey], opType, content)
		}
		return true, s.refreshAgentStepFromPatchState(stepIndex, sink)
	}
	return false, nil
}

func (s *ndjsonTranscriptState) applyPatchOperation(op ndjsonPatchOperation, sink InferenceStreamSink) error {
	switch op.O {
	case "a":
		if op.P == "/s/-" {
			item := mapValue(op.V)
			if item == nil {
				return nil
			}
			step := ndjsonStepState{
				ID:   strings.TrimSpace(stringValue(item["id"])),
				Type: strings.TrimSpace(stringValue(item["type"])),
			}
			if step.Type == "agent-inference" {
				step.Text = extractStepText(item["value"])
				step.Reasoning = extractStepReasoning(item["value"])
			}
			if step.Type == "agent-tool-result" {
				// Notion 原生工具调用（callFunction 等）→ 透传 OpenAI 格式
				if use := parseAgentToolUse(item); use != nil {
					s.ToolUses = append(s.ToolUses, *use)
					if clientDebugEnabled {
						log.Printf("[tools] collected native tool use: %s args=%s", use.Name, truncateBytes([]byte(use.Arguments), 120))
					}
				}
			}
			s.Steps = append(s.Steps, step)
			s.registerStepValueTypes(len(s.Steps)-1, item["value"])
			if step.Type == "agent-inference" {
				s.ActiveAgentIndex = len(s.Steps) - 1
				if err := s.emitFullReasoning(s.composeReasoningText(), sink); err != nil {
					return err
				}
				return s.emitFullText(step.Text, sink)
			}
			return nil
		}
		index, rest, ok := parsePatchStepIndex(op.P)
		if !ok || index < 0 || index >= len(s.Steps) {
			return nil
		}
		if strings.Contains(rest, "/value/-") {
			s.registerPatchValueAppend(op.P, op.V)
			if s.Steps[index].Type == "agent-inference" {
				s.ActiveAgentIndex = index
				return s.refreshAgentStepFromPatchState(index, sink)
			}
		}
		if s.Steps[index].Type == "agent-inference" && rest == "/finishedAt" {
			s.Steps[index].Completed = true
			s.ActiveAgentIndex = index
			s.FinalAgent.Completed = true
			s.FinalAgent.CompletedTime = op.V
		}
		if s.Steps[index].Type == "agent-inference" {
			s.ActiveAgentIndex = index
			if handled, err := s.applyAgentPatchField(index, rest, op.O, op.V, sink); handled {
				return err
			}
		}
	case "x", "p":
		index, rest, ok := parsePatchStepIndex(op.P)
		if !ok || index < 0 || index >= len(s.Steps) {
			return nil
		}
		if s.Steps[index].Type != "agent-inference" {
			return nil
		}
		s.ActiveAgentIndex = index
		if handled, err := s.applyAgentPatchField(index, rest, op.O, op.V, sink); handled {
			return err
		}
	case "r":
		index, rest, ok := parsePatchStepIndex(op.P)
		if !ok || index < 0 || index >= len(s.Steps) {
			return nil
		}
		if s.Steps[index].Type != "agent-inference" {
			return nil
		}
		if valueIndex, ok := parsePatchValueRemovalIndex(rest); ok {
			s.ActiveAgentIndex = index
			s.removePatchValueEntry(index, valueIndex)
			return s.refreshAgentStepFromPatchState(index, sink)
		}
	}
	return nil
}

func (s *ndjsonTranscriptState) handleLine(line []byte, threadID string, sink InferenceStreamSink) error {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	var streamLine ndjsonStreamLine
	if err := json.Unmarshal(line, &streamLine); err != nil {
		return err
	}
	s.LineCount++
	switch streamLine.Type {
	case "patch":
		for _, op := range streamLine.V {
			if err := s.applyPatchOperation(op, sink); err != nil {
				return err
			}
		}
	case "agent-inference":
		event := ndjsonAgentInferenceEvent{
			Type:       streamLine.Type,
			ID:         streamLine.ID,
			FinishedAt: streamLine.FinishedAt,
			Value:      streamLine.Value,
		}
		return s.mergeAgentInferenceEvent(event, sink)
	case "record-map":
		messageIDs, agent, outcomeErr, ok := finalThreadOutcomeFromRecordMap(streamLine.RecordMap, threadID)
		if len(messageIDs) > 0 {
			s.MessageIDs = messageIDs
		}
		if outcomeErr != nil {
			return outcomeErr
		}
		if ok {
			return s.mergeFinalAgent(agent, sink)
		}
	}
	return nil
}

// parseAgentToolUse — 从 NDJSON agent-tool-result 事件提取工具调用（透传 Notion 原生工具）
// 结构：{id, type:"agent-tool-result", toolName:"callFunction", toolType:"callFunction",
//
//	input:{function:"connections.fs.readFiles", args:{...}}}
func parseAgentToolUse(item map[string]any) *InferenceToolUse {
	// input.function 优先（具体函数名），fallback toolName
	name := strings.TrimSpace(stringValue(mapValue(item["input"])["function"]))
	if name == "" {
		name = strings.TrimSpace(stringValue(item["toolName"]))
	}
	if name == "" {
		return nil
	}
	args := mapValue(mapValue(item["input"])["args"])
	argsJSON := "{}"
	if raw, err := json.Marshal(args); err == nil {
		argsJSON = string(raw)
	}
	return &InferenceToolUse{
		ID:        strings.TrimSpace(stringValue(item["id"])),
		Name:      name,
		Arguments: argsJSON,
	}
}

func (s *ndjsonTranscriptState) result() ndjsonParseResult {
	out := ndjsonParseResult{
		LineCount:  s.LineCount,
		MessageIDs: append([]string(nil), s.MessageIDs...),
		FinalAgent: s.FinalAgent,
		Reasoning:  s.composeReasoningText(),
		ToolUses:   append([]InferenceToolUse(nil), s.ToolUses...),
	}
	for _, step := range s.Steps {
		if step.Type == "agent-inference" {
			out.HasAgentInference = true
			break
		}
	}
	if strings.TrimSpace(out.FinalAgent.Text) == "" {
		out.FinalAgent.Text = s.EmittedText
	}
	if strings.TrimSpace(out.Reasoning) != "" {
		out.FinalAgent.Reasoning = out.Reasoning
	} else if strings.TrimSpace(out.FinalAgent.Reasoning) == "" {
		out.FinalAgent.Reasoning = out.Reasoning
	}
	if out.FinalAgent.MessageID == "" && s.ActiveAgentIndex >= 0 && s.ActiveAgentIndex < len(s.Steps) {
		out.FinalAgent.MessageID = s.Steps[s.ActiveAgentIndex].ID
	}
	if len(out.MessageIDs) == 0 && out.FinalAgent.MessageID != "" {
		out.MessageIDs = []string{out.FinalAgent.MessageID}
	}
	return out
}

func consumeNDJSONStream(reader io.Reader, threadID string, sink InferenceStreamSink) (ndjsonParseResult, error) {
	state := &ndjsonTranscriptState{ActiveAgentIndex: -1}
	scanner := newNDJSONScanner(reader)
	// INV-02 未知标记探测器：扫描原始流（T-09）
	var rawBuf strings.Builder
	rawCollect := unknownMarkerScanEnabled()
	defer func() {
		if rawCollect {
			maybeScanUnknownMarkers(rawBuf.String())
		}
	}()
	for scanner.Scan() {
		line := scanner.Bytes()
		if rawCollect && rawBuf.Len() < unknownMarkerRawCap {
			_, _ = rawBuf.Write(line)
			_, _ = rawBuf.WriteString("\n")
		}
		if handleErr := state.handleLine(line, threadID, sink); handleErr != nil {
			return state.result(), handleErr
		}
		if state.hasTerminalAnswer() {
			return state.result(), nil
		}
	}
	if err := normalizeNDJSONScanError(scanner.Err()); err != nil {
		return state.result(), err
	}
	return state.result(), nil
}

// unknownMarkerRawCap — 原始流收集上限（4MB，超出部分不收集；标记域远小于此）
const unknownMarkerRawCap = 4 << 20

var ndjsonIdleAfterAnswerTimeout = 5 * time.Second
var errNDJSONLineTooLarge = errors.New("ndjson line too large")

// ndjsonSilenceTimeout — P0-1 静默看门狗:从流开始到结束全程武装,
// 任何 NDJSON 行(含 config/context 前奏行)到达即重置。覆盖两类黑洞:
//
//	① 上游返回 200 后永不发行(首行超时):此前 idle 计时只在可见答案后才武装,
//	   无账号池 fallback 下能挂 900s,keepalive 还让客户端永不超时;
//	② agent-inference 启动后永不产出(半截推理,账号被上游标记的已知演化形态)。
//
// 上游正常账号每步推理都有 NDJSON 行(思考/工具/答案),45s = TTFB p50(~2.9s)的 15 倍余量。
var ndjsonSilenceTimeout = 45 * time.Second

// errUpstreamSilent — 上游长时间静默(无可见答案)。包装 errAccountStarved 语义:
// dispatch 视为账号级故障 → 非 retryable、进冷却、换候选。
var errUpstreamSilent = fmt.Errorf("%w: upstream stream silent", errAccountStarved)

const (
	ndjsonScannerInitialBuffer = 64 * 1024
	ndjsonMaxLineBytes         = 16 * 1024 * 1024
)

type ndjsonReadEvent struct {
	line []byte
	err  error
}

func newNDJSONScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, ndjsonScannerInitialBuffer), ndjsonMaxLineBytes)
	return scanner
}

func normalizeNDJSONScanError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, bufio.ErrTooLong) {
		return fmt.Errorf("%w: exceeds %d bytes", errNDJSONLineTooLarge, ndjsonMaxLineBytes)
	}
	return err
}

func consumeNDJSONStreamWithIdleClose(reader io.ReadCloser, threadID string, sink InferenceStreamSink, idleAfterAnswer time.Duration) (ndjsonParseResult, error) {
	state := &ndjsonTranscriptState{ActiveAgentIndex: -1}
	events := make(chan ndjsonReadEvent, 1)
	done := make(chan struct{})
	defer close(done)
	// INV-02 未知标记探测器：扫描原始流（T-09）
	var rawBuf strings.Builder
	rawCollect := unknownMarkerScanEnabled()
	defer func() {
		if rawCollect {
			maybeScanUnknownMarkers(rawBuf.String())
		}
	}()

	go func() {
		scanner := newNDJSONScanner(reader)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			if rawCollect && rawBuf.Len() < unknownMarkerRawCap {
				_, _ = rawBuf.Write(line)
				_, _ = rawBuf.WriteString("\n")
			}
			select {
			case events <- ndjsonReadEvent{line: line}:
			case <-done:
				return
			}
		}
		if err := normalizeNDJSONScanError(scanner.Err()); err != nil {
			select {
			case events <- ndjsonReadEvent{err: err}:
			case <-done:
				return
			}
			return
		}
		select {
		case events <- ndjsonReadEvent{err: io.EOF}:
		case <-done:
			return
		}
	}()

	var idleTimer *time.Timer
	var idleC <-chan time.Time
	stopIdleTimer := func() {
		if idleTimer == nil {
			return
		}
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleC = nil
	}
	resetIdleTimer := func() {
		if idleAfterAnswer <= 0 || !state.hasVisibleAnswer() || state.hasTerminalAnswer() {
			return
		}
		if idleTimer == nil {
			idleTimer = time.NewTimer(idleAfterAnswer)
			idleC = idleTimer.C
			return
		}
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(idleAfterAnswer)
		idleC = idleTimer.C
	}
	defer stopIdleTimer()

	// P0-1 静默看门狗:全程武装,每收一行重置(与"答案后 idle"并存——
	// 后者管"答案收齐后多等 5s 收尾",这里管"上游完全静默"的黑洞)。
	silenceTimer := time.NewTimer(ndjsonSilenceTimeout)
	defer silenceTimer.Stop()
	resetSilenceTimer := func() {
		if !silenceTimer.Stop() {
			select {
			case <-silenceTimer.C:
			default:
			}
		}
		silenceTimer.Reset(ndjsonSilenceTimeout)
	}

	for {
		select {
		case event := <-events:
			if len(event.line) > 0 {
				resetSilenceTimer()
				if handleErr := state.handleLine(event.line, threadID, sink); handleErr != nil {
					return state.result(), handleErr
				}
				if state.hasTerminalAnswer() {
					return state.result(), nil
				}
				resetIdleTimer()
			}
			if event.err != nil {
				if errors.Is(event.err, io.EOF) {
					return state.result(), nil
				}
				return state.result(), event.err
			}
		case <-idleC:
			_ = reader.Close()
			return state.result(), nil
		case <-silenceTimer.C:
			_ = reader.Close()
			if !state.hasVisibleAnswer() {
				// 无任何可见答案的全程静默 = 账号级黑洞(被标记/半截推理),
				// 按 starved 语义上抛:dispatch 非 retryable → 冷却 → 换候选
				log.Printf("[ndjson] silence watchdog fired thread=%s lines=%d agent_inference=%v", threadID, state.LineCount, state.result().HasAgentInference)
				return state.result(), fmt.Errorf("%w (no visible answer after %s)", errUpstreamSilent, ndjsonSilenceTimeout)
			}
			// 已有部分答案:按 EOF 收尾,客户端拿到已收内容(与 idleC 语义一致)
			log.Printf("[ndjson] stream stalled after partial answer; closing thread=%s", threadID)
			return state.result(), nil
		}
	}
}

func (c *NotionAIClient) loadFinalAnswerOnce(ctx context.Context, threadID string) ([]string, agentMessage, error) {
	// 黑洞检测：正常账号 syncRecordValuesSpaceInitial 秒回；被上游限制的账号
	// 对该 thread 的 sync 可能永不响应。20s 后判账号级故障，由 dispatch 换号，
	// 而不是把整个请求拖到客户端超时。
	syncCtx, cancel, ok := bestEffortContext(ctx, accountSyncBlackholeTimeout)
	if ok {
		defer cancel()
		ctx = syncCtx
	}
	threadData, err := c.syncThread(ctx, threadID)
	if err != nil {
		return nil, agentMessage{}, err
	}
	messageIDs := messageIDsFromThreadRecord(threadData, threadID)
	if len(messageIDs) == 0 {
		return nil, agentMessage{}, fmt.Errorf("thread %s did not produce any messages", threadID)
	}
	messageData, err := c.syncThreadMessages(ctx, threadID, messageIDs)
	if err != nil {
		return nil, agentMessage{}, err
	}
	recordMap := mapValue(messageData["recordMap"])
	if _, agent, outcomeErr, ok := finalThreadOutcomeFromRecordMap(recordMap, threadID); ok {
		if outcomeErr != nil {
			return messageIDs, agentMessage{}, outcomeErr
		}
		return messageIDs, agent, nil
	}
	return messageIDs, agentMessage{}, fmt.Errorf("thread %s did not produce any agent-inference message", threadID)
}

func (c *NotionAIClient) syncThread(ctx context.Context, threadID string) (map[string]any, error) {
	// P1-4 修复:黑洞窗口下沉到 sync 本体(此前只在 loadFinalAnswerOnce/poll 等部分调用点包裹,
	// prepareContinuationDraft/saveContinuationScaffold 等路径裸传 ctx,被标记账号可挂到 60s/180s)
	if wctx, cancel, ok := bestEffortContext(ctx, accountSyncBlackholeTimeout); ok {
		defer cancel()
		ctx = wctx
	}
	payload := map[string]any{
		"requests": []map[string]any{{
			"pointer": map[string]any{
				"table":   "thread",
				"id":      threadID,
				"spaceId": c.Session.SpaceID,
			},
			"version": -1,
		}},
	}
	body, err := c.postJSON(ctx, c.Config.NotionUpstream().API("syncRecordValuesSpaceInitial"), payload, "application/json")
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *NotionAIClient) syncThreadMessages(ctx context.Context, threadID string, messageIDs []string) (map[string]any, error) {
	// P1-4 修复:同上,黑洞窗口下沉到本体
	if wctx, cancel, ok := bestEffortContext(ctx, accountSyncBlackholeTimeout); ok {
		defer cancel()
		ctx = wctx
	}
	requests := make([]map[string]any, 0, len(messageIDs))
	for _, messageID := range messageIDs {
		requests = append(requests, map[string]any{
			"pointer": map[string]any{
				"table":   "thread_message",
				"id":      messageID,
				"spaceId": c.Session.SpaceID,
			},
			"version": -1,
		})
	}
	body, err := c.postJSONWithReferer(ctx, c.Config.NotionUpstream().API("syncRecordValuesSpaceInitial"), map[string]any{"requests": requests}, "application/json", c.chatReferer(threadID))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func mapValue(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func sliceValue(v any) []any {
	switch s := v.(type) {
	case []any:
		return s
	case []map[string]any:
		out := make([]any, len(s))
		for i, item := range s {
			out[i] = item
		}
		return out
	case []string:
		out := make([]any, len(s))
		for i, item := range s {
			out[i] = item
		}
		return out
	}
	return nil
}

func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return fmt.Sprintf("%.0f", x)
	case int64:
		return fmt.Sprintf("%d", x)
	case int:
		return fmt.Sprintf("%d", x)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func booleanValue(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(x))
		return err == nil && parsed
	case json.Number:
		n, err := x.Int64()
		return err == nil && n != 0
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	default:
		return false
	}
}

func messageIDsFromThreadRecord(threadData map[string]any, threadID string) []string {
	recordMap := mapValue(threadData["recordMap"])
	threadMap := mapValue(recordMap["thread"])
	threadRecord := mapValue(threadMap[threadID])
	valueWrapper := mapValue(threadRecord["value"])
	value := mapValue(valueWrapper["value"])
	rawMessages := sliceValue(value["messages"])
	out := make([]string, 0, len(rawMessages))
	for _, item := range rawMessages {
		text := strings.TrimSpace(stringValue(item))
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func threadFileIDsFromThreadRecord(threadData map[string]any, threadID string) []string {
	recordMap := mapValue(threadData["recordMap"])
	threadMap := mapValue(recordMap["thread"])
	threadRecord := mapValue(threadMap[threadID])
	valueWrapper := mapValue(threadRecord["value"])
	value := mapValue(valueWrapper["value"])
	rawFileIDs := sliceValue(value["file_ids"])
	out := make([]string, 0, len(rawFileIDs))
	for _, item := range rawFileIDs {
		text := strings.TrimSpace(stringValue(item))
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func extractAgentMessages(recordMap map[string]any) map[string]agentMessage {
	out := map[string]agentMessage{}
	threadMessages := mapValue(recordMap["thread_message"])
	for messageID, rawItem := range threadMessages {
		item := mapValue(rawItem)
		valueWrapper := mapValue(item["value"])
		value := mapValue(valueWrapper["value"])
		step := mapValue(value["step"])
		if stringValue(step["type"]) != "agent-inference" {
			continue
		}
		data := mapValue(value["data"])
		completed, _ := data["completed"].(bool)
		out[messageID] = agentMessage{
			MessageID:     messageID,
			Completed:     completed,
			CompletedTime: data["completed_time"],
			Text:          extractStepText(step["value"]),
			Reasoning:     extractStepReasoning(step["value"]),
		}
	}
	return out
}

func extractThreadErrors(recordMap map[string]any, threadID string) map[string]inferenceStepError {
	out := map[string]inferenceStepError{}
	threadMessages := mapValue(recordMap["thread_message"])
	for messageID, rawItem := range threadMessages {
		item := mapValue(rawItem)
		valueWrapper := mapValue(item["value"])
		value := mapValue(valueWrapper["value"])
		step := mapValue(value["step"])
		if stringValue(step["type"]) != "error" {
			continue
		}
		data := mapValue(value["data"])
		traceID := firstNonEmpty(strings.TrimSpace(stringValue(step["traceId"])), strings.TrimSpace(stringValue(data["inference_id"])))
		out[messageID] = inferenceStepError{
			ThreadID:   strings.TrimSpace(threadID),
			MessageID:  messageID,
			Message:    strings.TrimSpace(stringValue(step["message"])),
			SubType:    strings.TrimSpace(stringValue(step["subType"])),
			TraceID:    traceID,
			Retryable:  booleanValue(step["isRetryable"]),
			StackTrace: strings.TrimSpace(stringValue(step["stack"])),
		}
	}
	return out
}

func (c *NotionAIClient) loadTranscriptConversation(ctx context.Context, summary InferenceTranscriptSummary) (ConversationEntry, error) {
	threadID := strings.TrimSpace(summary.ThreadID)
	if threadID == "" {
		return ConversationEntry{}, fmt.Errorf("thread id is required")
	}
	threadData, err := c.syncThread(ctx, threadID)
	if err != nil {
		return ConversationEntry{}, err
	}
	messageIDs := messageIDsFromThreadRecord(threadData, threadID)
	recordMap := mapValue(threadData["recordMap"])
	if len(messageIDs) > 0 {
		messageData, err := c.syncThreadMessages(ctx, threadID, messageIDs)
		if err != nil {
			return ConversationEntry{}, err
		}
		recordMap = mapValue(messageData["recordMap"])
	}
	threadMessages := mapValue(recordMap["thread_message"])
	messages := make([]ConversationMessage, 0, len(messageIDs))
	for _, messageID := range messageIDs {
		msg, ok := extractConversationMessageFromThreadRecord(messageID, threadMessages[messageID])
		if !ok {
			continue
		}
		messages = append(messages, msg)
	}
	title := strings.TrimSpace(summary.Title)
	if title == "" {
		for _, message := range messages {
			if strings.TrimSpace(message.Role) != "user" {
				continue
			}
			title = conversationTitle(message.Content, message.Attachments)
			if title != "" {
				break
			}
		}
	}
	if title == "" {
		title = "Untitled conversation"
	}
	createdAt := summary.CreatedAt
	if createdAt.IsZero() && len(messages) > 0 {
		createdAt = messages[0].CreatedAt
	}
	updatedAt := summary.UpdatedAt
	if updatedAt.IsZero() && len(messages) > 0 {
		updatedAt = messages[len(messages)-1].UpdatedAt
	}
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	return ConversationEntry{
		ID:               notionThreadConversationID(threadID),
		Title:            title,
		Origin:           "notion",
		RemoteOnly:       true,
		Source:           "notion",
		Transport:        "transcript_sync",
		Status:           "completed",
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
		ThreadID:         threadID,
		CreatedByDisplay: summary.CreatedByDisplay,
		Messages:         messages,
	}, nil
}

func (c *NotionAIClient) listInferenceTranscripts(ctx context.Context) ([]InferenceTranscriptSummary, error) {
	body, err := c.postJSON(ctx, c.Config.NotionUpstream().API("getInferenceTranscriptsForUser"), map[string]any{
		"threadParentPointer": map[string]any{
			"table":   "space",
			"id":      c.Session.SpaceID,
			"spaceId": c.Session.SpaceID,
		},
		"limit":              50,
		"includeWriterChats": false,
	}, "application/json")
	if err != nil {
		return nil, err
	}
	var out struct {
		Transcripts []map[string]any `json:"transcripts"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	items := make([]InferenceTranscriptSummary, 0, len(out.Transcripts))
	for _, raw := range out.Transcripts {
		threadID := strings.TrimSpace(stringValue(raw["id"]))
		if threadID == "" {
			threadID = strings.TrimSpace(stringValue(raw["threadId"]))
		}
		if threadID == "" {
			continue
		}
		items = append(items, InferenceTranscriptSummary{
			ThreadID:         threadID,
			Title:            strings.TrimSpace(stringValue(raw["title"])),
			CreatedAt:        timeFromTranscriptValue(raw["created_at"]),
			UpdatedAt:        firstNonZeroTime(timeFromTranscriptValue(raw["updated_at"]), timeFromTranscriptValue(raw["last_edited_time"])),
			CreatedByDisplay: strings.TrimSpace(stringValue(raw["created_by_display_name"])),
			TranscriptType:   strings.TrimSpace(stringValue(raw["type"])),
		})
	}
	return items, nil
}

func (c *NotionAIClient) deleteThread(ctx context.Context, threadID string) error {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return fmt.Errorf("thread id is required")
	}
	// P1-4 收尾:同 saveContinuationScaffold
	if wctx, cancel, ok := bestEffortContext(ctx, accountSyncBlackholeTimeout); ok {
		defer cancel()
		ctx = wctx
	}
	_, err := c.postJSON(ctx, c.Config.NotionUpstream().API("saveTransactionsFanout"), map[string]any{
		"requestId": randomUUID(),
		"transactions": []map[string]any{{
			"id": randomUUID(),
			"operations": []map[string]any{{
				"command": "update",
				"table":   "thread",
				"id":      threadID,
				"path":    []string{},
				"args": map[string]any{
					"alive": false,
				},
			}},
		}},
	}, "application/json")
	return err
}

func (c *NotionAIClient) pollFinalAnswer(ctx context.Context, threadID string) ([]string, agentMessage, error) {
	// 黑洞检测（轮询阶段）：与流式版一致，防被限制账号的多轮黑洞拖死请求
	pollCtx, cancel, ok := bestEffortContext(ctx, accountPollBlackholeTimeout)
	if ok {
		defer cancel()
		ctx = pollCtx
	}
	var lastAgent agentMessage
	var haveAgent bool
	lastMessageIDs := []string{}
	for i := 0; i < c.PollMaxRounds; i++ {
		select {
		case <-ctx.Done():
			return nil, agentMessage{}, ctx.Err()
		case <-time.After(c.PollInterval):
		}
		threadData, err := c.syncThread(ctx, threadID)
		if err != nil {
			return nil, agentMessage{}, err
		}
		messageIDs := messageIDsFromThreadRecord(threadData, threadID)
		if len(messageIDs) == 0 {
			continue
		}
		lastMessageIDs = messageIDs
		messageData, err := c.syncThreadMessages(ctx, threadID, messageIDs)
		if err != nil {
			return nil, agentMessage{}, err
		}
		recordMap := mapValue(messageData["recordMap"])
		_, agent, outcomeErr, ok := finalThreadOutcomeFromRecordMap(recordMap, threadID)
		if outcomeErr != nil {
			return messageIDs, agentMessage{}, outcomeErr
		}
		if ok {
			lastAgent = agent
			haveAgent = true
		}
		if haveAgent && lastAgent.Completed && strings.TrimSpace(lastAgent.Text) != "" {
			return messageIDs, lastAgent, nil
		}
	}
	if haveAgent {
		return nil, agentMessage{}, fmt.Errorf("agent message incomplete after polling: %+v", lastAgent)
	}
	return nil, agentMessage{}, fmt.Errorf("thread %s did not produce any agent-inference message; last_message_ids=%v", threadID, lastMessageIDs)
}

func (c *NotionAIClient) loadAttachmentData(ctx context.Context, input InputAttachment) ([]byte, string, string, error) {
	name := strings.TrimSpace(input.Name)
	contentType := normalizeContentType(input.ContentType)
	if len(input.Data) > 0 {
		if name == "" {
			name = inferAttachmentName(input.URL, input.Path, strings.HasPrefix(contentType, "image/"))
		}
		if contentType == "" {
			contentType = inferContentTypeFromName(name, strings.HasPrefix(name, "image"))
		}
		return input.Data, name, contentType, nil
	}
	if strings.TrimSpace(input.Path) != "" {
		absPath, err := filepath.Abs(input.Path)
		if err != nil {
			return nil, "", "", err
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			return nil, "", "", err
		}
		if len(data) > maxAttachmentBytes {
			return nil, "", "", fmt.Errorf("attachment too large: %s", absPath)
		}
		if name == "" {
			name = filepath.Base(absPath)
		}
		if contentType == "" {
			contentType = inferContentTypeFromName(name, false)
		}
		return data, name, contentType, nil
	}
	if strings.TrimSpace(input.URL) != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, input.URL, nil)
		if err != nil {
			return nil, "", "", err
		}
		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return nil, "", "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			return nil, "", "", fmt.Errorf("download attachment failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentBytes+1))
		if err != nil {
			return nil, "", "", err
		}
		if len(data) > maxAttachmentBytes {
			return nil, "", "", fmt.Errorf("attachment too large: %s", input.URL)
		}
		if name == "" {
			name = inferAttachmentName(input.URL, "", strings.HasPrefix(contentType, "image/"))
		}
		if contentType == "" {
			contentType = normalizeContentType(resp.Header.Get("Content-Type"))
		}
		if contentType == "" {
			contentType = inferContentTypeFromName(name, false)
		}
		return data, name, contentType, nil
	}
	return nil, "", "", fmt.Errorf("attachment has no usable source")
}

func validateAttachmentType(contentType string) error {
	allowed := map[string]struct{}{
		"application/pdf": {},
		"text/csv":        {},
		"text/markdown":   {},
		"text/plain":      {},
		"image/png":       {},
		"image/jpeg":      {},
		"image/gif":       {},
		"image/webp":      {},
		"image/heic":      {},
	}
	contentType = normalizeContentType(contentType)
	if _, ok := allowed[contentType]; !ok {
		return fmt.Errorf("unsupported attachment content type: %s", contentType)
	}
	return nil
}

func (c *NotionAIClient) validateAttachment(contentType string) error {
	return validateAttachmentType(contentType)
}

func (c *NotionAIClient) getUploadDescriptor(ctx context.Context, threadID string, fileName string, contentType string, contentLength int, createThread bool) (uploadDescriptor, error) {
	payload := map[string]any{
		"name":        fileName,
		"contentType": contentType,
		"assistantChatTranscriptSessionPointer": map[string]any{
			"spaceId": c.Session.SpaceID,
			"table":   "thread",
			"id":      threadID,
		},
		"contentLength": contentLength,
		"createThread":  createThread,
	}
	body, err := c.postJSON(ctx, c.Config.NotionUpstream().API("getUploadFileUrlForAssistantChatTranscriptUpload"), payload, "application/json")
	if err != nil {
		return uploadDescriptor{}, err
	}
	var desc uploadDescriptor
	if err := json.Unmarshal(body, &desc); err != nil {
		return uploadDescriptor{}, err
	}
	return desc, nil
}

func (c *NotionAIClient) enqueueAttachmentProcessing(ctx context.Context, threadID string, attachmentURL string) (string, error) {
	payload := map[string]any{
		"task": map[string]any{
			"eventName": "processAgentAttachment",
			"request": map[string]any{
				"url":     attachmentURL,
				"spaceId": c.Session.SpaceID,
				"aiSessionPointer": map[string]any{
					"spaceId": c.Session.SpaceID,
					"table":   "thread",
					"id":      threadID,
				},
				"source":        "user_upload",
				"clientVersion": c.Session.ClientVersion,
			},
			"cellRouting": map[string]any{
				"spaceIds": []string{c.Session.SpaceID},
			},
		},
	}
	body, err := c.postJSON(ctx, c.Config.NotionUpstream().API("enqueueTask"), payload, "application/json")
	if err != nil {
		return "", err
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	taskID := strings.TrimSpace(stringValue(result["taskId"]))
	if taskID == "" {
		return "", fmt.Errorf("enqueueTask returned empty taskId")
	}
	return taskID, nil
}

func (c *NotionAIClient) waitAttachmentTask(ctx context.Context, taskID string) (map[string]any, error) {
	for i := 0; i < c.PollMaxRounds; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.PollInterval):
		}
		body, err := c.postJSON(ctx, c.Config.NotionUpstream().API("getTasks"), map[string]any{"taskIds": []string{taskID}}, "application/json")
		if err != nil {
			return nil, err
		}
		var result map[string]any
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, err
		}
		items := sliceValue(result["results"])
		if len(items) == 0 {
			continue
		}
		entry := mapValue(items[0])
		state := strings.TrimSpace(stringValue(entry["state"]))
		status := mapValue(entry["status"])
		statusResult := mapValue(status["result"])
		statusData := mapValue(statusResult["data"])
		successType := strings.TrimSpace(stringValue(statusResult["type"]))
		if state == "success" || successType == "success" {
			return statusData, nil
		}
		if state == "error" || successType == "error" {
			return nil, fmt.Errorf("attachment task failed: %v", entry)
		}
	}
	return nil, fmt.Errorf("attachment task timeout: %s", taskID)
}

func (c *NotionAIClient) getSignedAttachmentURL(ctx context.Context, threadID string, attachmentURL string, downloadName string) (string, error) {
	payload := map[string]any{
		"urls": []map[string]any{{
			"url":          attachmentURL,
			"download":     false,
			"downloadName": downloadName,
			"permissionRecord": map[string]any{
				"table":   "thread",
				"id":      threadID,
				"spaceId": c.Session.SpaceID,
			},
		}},
	}
	body, err := c.postJSON(ctx, c.Config.NotionUpstream().API("getSignedFileUrls"), payload, "application/json")
	if err != nil {
		return "", err
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	signedURLs := sliceValue(result["signedUrls"])
	if len(signedURLs) == 0 {
		return "", nil
	}
	return strings.TrimSpace(stringValue(signedURLs[0])), nil
}

func extractAttachmentFileID(attachmentURL string) string {
	clean := strings.TrimSpace(attachmentURL)
	if !strings.HasPrefix(clean, "attachment:") {
		return ""
	}
	parts := strings.SplitN(clean, ":", 3)
	if len(parts) != 3 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func containsTrimmedString(values []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func (c *NotionAIClient) waitThreadAttachmentMounted(ctx context.Context, threadID string, fileID string) error {
	threadID = strings.TrimSpace(threadID)
	fileID = strings.TrimSpace(fileID)
	if threadID == "" || fileID == "" {
		return nil
	}
	rounds := c.PollMaxRounds
	if rounds <= 0 {
		rounds = 5
	}
	if rounds > 8 {
		rounds = 8
	}
	for i := 0; i < rounds; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.PollInterval):
			}
		}
		threadData, err := c.syncThread(ctx, threadID)
		if err != nil {
			return err
		}
		if containsTrimmedString(threadFileIDsFromThreadRecord(threadData, threadID), fileID) {
			return nil
		}
	}
	return fmt.Errorf("thread %s missing mounted attachment file_id %s", threadID, fileID)
}

func (c *NotionAIClient) uploadAttachments(ctx context.Context, threadID string, attachments []InputAttachment, createThread bool) ([]UploadedAttachment, string, error) {
	if len(attachments) == 0 {
		return nil, threadID, nil
	}
	uploaded := make([]UploadedAttachment, 0, len(attachments))
	currentThreadID := threadID
	shouldCreateThread := createThread
	for _, input := range attachments {
		data, name, contentType, err := c.loadAttachmentData(ctx, input)
		if err != nil {
			return nil, currentThreadID, err
		}
		if err := c.validateAttachment(contentType); err != nil {
			return nil, currentThreadID, err
		}
		desc, err := c.getUploadDescriptor(ctx, currentThreadID, name, contentType, len(data), shouldCreateThread)
		if err != nil {
			return nil, currentThreadID, err
		}
		if strings.TrimSpace(desc.ChatID) != "" {
			currentThreadID = strings.TrimSpace(desc.ChatID)
		}
		shouldCreateThread = false
		if err := c.doMultipartUpload(ctx, desc.SignedUploadPostURL, desc.Fields, name, contentType, data); err != nil {
			return nil, currentThreadID, err
		}
		fileID := extractAttachmentFileID(desc.URL)
		if err := c.waitThreadAttachmentMounted(ctx, currentThreadID, fileID); err != nil {
			return nil, currentThreadID, err
		}
		taskID, err := c.enqueueAttachmentProcessing(ctx, currentThreadID, desc.URL)
		if err != nil {
			return nil, currentThreadID, err
		}
		metadata, err := c.waitAttachmentTask(ctx, taskID)
		if err != nil {
			return nil, currentThreadID, err
		}
		signedURL, _ := c.getSignedAttachmentURL(ctx, currentThreadID, desc.URL, name)
		uploaded = append(uploaded, UploadedAttachment{
			Name:          name,
			ContentType:   contentType,
			SizeBytes:     len(data),
			Source:        input.Source,
			FileID:        fileID,
			ThreadMounted: fileID != "",
			AttachmentURL: desc.URL,
			SignedGetURL:  firstNonEmptyString(signedURL, desc.SignedGetURL),
			TaskID:        taskID,
			Metadata:      metadata,
		})
	}
	return uploaded, currentThreadID, nil
}

func (c *NotionAIClient) doMultipartUpload(ctx context.Context, uploadURL string, fields map[string]any, fileName string, contentType string, data []byte) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, strings.TrimSpace(fmt.Sprint(value))); err != nil {
			return err
		}
	}
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("multipart upload failed: %d %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	return nil
}

type inferencePayloadMeta struct {
	ConfigID         string
	ContextID        string
	OriginalDatetime string
	IsPartial        bool
}

func buildContinuationBaseTranscript(draft *continuationTurnDraft, configValue map[string]any, contextValue map[string]any) []map[string]any {
	if draft == nil {
		return nil
	}
	configID := normalizeTranscriptStepID(draft.ConfigID)
	contextID := normalizeTranscriptStepID(draft.ContextID)
	cleanConfigValue := cloneMapAny(configValue)
	delete(cleanConfigValue, "availableConnectors")
	delete(cleanConfigValue, "customConnectorInfo")
	out := []map[string]any{
		{
			"id":    configID,
			"type":  "config",
			"value": cleanConfigValue,
		},
		{
			"id":    contextID,
			"type":  "context",
			"value": cloneMapAny(contextValue),
		},
	}
	return out
}

func maskIfEnabled(req PromptRunRequest) string {
	prompt := req.Prompt
	if req.MaskLocalPaths {
		prompt = maskLocalPathsForWorkingDirectory(prompt, req.ClientWorkingDirectory)
	}
	if section := strings.TrimSpace(req.ToolBridgeSection); section != "" {
		prompt = strings.TrimSpace(prompt) + "\n\n" + section
	}
	return prompt
}

func buildContinuationUpdatedConfigValue(draft *continuationTurnDraft) map[string]any {
	value := map[string]any{}
	if draft != nil {
		if len(draft.LastUpdatedConfigValue) > 0 {
			value = cloneMapAny(draft.LastUpdatedConfigValue)
		} else if len(draft.ConfigValue) > 0 {
			if raw, ok := draft.ConfigValue["availableConnectors"]; ok {
				value["availableConnectors"] = raw
			}
			if raw, ok := draft.ConfigValue["customConnectorInfo"]; ok {
				value["customConnectorInfo"] = raw
			}
		}
	}
	if _, ok := value["availableConnectors"]; !ok {
		value["availableConnectors"] = []any{}
	}
	if _, ok := value["customConnectorInfo"]; !ok {
		value["customConnectorInfo"] = []any{}
	}
	return value
}

func countValidNDJSONLines(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func (c *NotionAIClient) markInferenceTranscriptSeen(ctx context.Context, threadID string) error {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil
	}
	_, err := c.postJSON(ctx, c.Config.NotionUpstream().API("markInferenceTranscriptSeen"), map[string]any{
		"spaceId":  c.Session.SpaceID,
		"threadId": threadID,
	}, "application/json")
	return err
}

func (c *NotionAIClient) logBestEffortFailure(operation string, err error) {
	if err == nil || !c.Config.DebugUpstream {
		return
	}
	log.Printf("[best_effort] %s: %v", operation, err)
}

func (c *NotionAIClient) markInferenceTranscriptSeenBestEffort(ctx context.Context, threadID string) {
	bestEffortCtx, cancel, ok := bestEffortContext(ctx, bestEffortPostRunTimeout)
	if !ok {
		return
	}
	defer cancel()
	if err := c.markInferenceTranscriptSeen(bestEffortCtx, threadID); err != nil {
		c.logBestEffortFailure("markInferenceTranscriptSeen", err)
	}
}

func (c *NotionAIClient) prepareContinuationDraftFromThread(ctx context.Context, threadID string) (*continuationTurnDraft, error) {
	threadData, err := c.syncThread(ctx, threadID)
	if err != nil {
		return nil, err
	}
	messageIDs := messageIDsFromThreadRecord(threadData, threadID)
	if len(messageIDs) == 0 {
		return &continuationTurnDraft{}, nil
	}
	messageData, err := c.syncThreadMessages(ctx, threadID, messageIDs)
	if err != nil {
		return nil, err
	}
	recordMap := mapValue(messageData["recordMap"])
	draft := extractContinuationDraftFromThreadMessages(mapValue(recordMap["thread_message"]), messageIDs)
	if draft == nil {
		draft = &continuationTurnDraft{}
	}
	return draft, nil
}

func (c *NotionAIClient) saveContinuationScaffold(ctx context.Context, threadID string, prompt string, draft *continuationTurnDraft) (*continuationTurnScaffold, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, nil
	}
	// P1-4 收尾:被标记账号的连接对 saveTransactions* 也会黑洞——统一 best-effort 窗口
	if wctx, cancel, ok := bestEffortContext(ctx, accountSyncBlackholeTimeout); ok {
		defer cancel()
		ctx = wctx
	}
	createdAt := isoNowMillis()
	createdTime := time.Now().UnixMilli()
	updatedConfigID := randomUUID()
	userStepID := randomUUID()
	userID := strings.TrimSpace(c.Session.UserID)
	spaceID := strings.TrimSpace(c.Session.SpaceID)
	updatedConfigValue := buildContinuationUpdatedConfigValue(draft)
	payload := map[string]any{
		"requestId": randomUUID(),
		"transactions": []map[string]any{
			{
				"id":      randomUUID(),
				"spaceId": spaceID,
				"debug": map[string]any{
					"userAction": "WorkflowActions.addStepsToExistingThreadAndRun",
				},
				"operations": []map[string]any{
					{
						"pointer": map[string]any{
							"table":   "thread_message",
							"id":      updatedConfigID,
							"spaceId": spaceID,
						},
						"path":    []string{},
						"command": "set",
						"args": map[string]any{
							"id":      updatedConfigID,
							"version": 1,
							"step": map[string]any{
								"id":    updatedConfigID,
								"type":  "updated-config",
								"value": updatedConfigValue,
							},
							"parent_id":        threadID,
							"parent_table":     "thread",
							"space_id":         spaceID,
							"created_time":     createdTime,
							"created_by_id":    userID,
							"created_by_table": "notion_user",
						},
					},
					{
						"pointer": map[string]any{
							"table":   "thread_message",
							"id":      userStepID,
							"spaceId": spaceID,
						},
						"path":    []string{},
						"command": "set",
						"args": map[string]any{
							"id":      userStepID,
							"version": 1,
							"step": map[string]any{
								"id":        userStepID,
								"type":      "user",
								"value":     [][]string{{prompt}},
								"userId":    userID,
								"createdAt": createdAt,
							},
							"parent_id":        threadID,
							"parent_table":     "thread",
							"space_id":         spaceID,
							"created_time":     createdTime,
							"created_by_id":    userID,
							"created_by_table": "notion_user",
						},
					},
					{
						"args": map[string]any{
							"ids": []string{updatedConfigID, userStepID},
						},
						"command": "listAfterMulti",
						"path":    []string{"messages"},
						"pointer": map[string]any{
							"table":   "thread",
							"id":      threadID,
							"spaceId": spaceID,
						},
					},
				},
			},
			{
				"id":      randomUUID(),
				"spaceId": spaceID,
				"debug": map[string]any{
					"userAction": "unifiedChatInputActions.updateThreadUpdatedTime",
				},
				"operations": []map[string]any{
					{
						"pointer": map[string]any{
							"table":   "thread",
							"id":      threadID,
							"spaceId": spaceID,
						},
						"path":    []string{},
						"command": "update",
						"args": map[string]any{
							"updated_time":     createdTime + 1,
							"updated_by_id":    userID,
							"updated_by_table": "notion_user",
						},
					},
				},
			},
		},
	}
	if _, err := c.postJSON(ctx, c.Config.NotionUpstream().API("saveTransactionsFanout"), payload, "application/json"); err != nil {
		return nil, err
	}
	return &continuationTurnScaffold{
		UpdatedConfigID:    updatedConfigID,
		UserStepID:         userStepID,
		UserCreatedAt:      createdAt,
		UpdatedConfigValue: updatedConfigValue,
	}, nil
}

func (c *NotionAIClient) streamRunInferenceTranscript(ctx context.Context, payload map[string]any, threadID string, sink InferenceStreamSink, _ bool) (ndjsonParseResult, error) {
	return c.runInferenceTranscriptWithFallback(ctx, payload, threadID, sink)
}

func (c *NotionAIClient) buildInferencePayload(req PromptRunRequest, threadID string, attachments []UploadedAttachment) (map[string]any, inferencePayloadMeta) {
	now := isoNowMillis()
	hiddenPrompt := strings.TrimSpace(req.HiddenPrompt)
	surface := firstNonEmpty(strings.TrimSpace(c.Config.Features.AISurface), "ai_module")
	threadType := firstNonEmpty(strings.TrimSpace(c.Config.Features.ThreadType), "workflow")
	if len(attachments) > 0 {
		surface = "workflows"
	}
	configValue := c.buildDefaultWorkflowConfigValue(threadType, req.UseWebSearch, req.NotionModel)
	if req.continuationDraft != nil && len(req.continuationDraft.ConfigValue) > 0 {
		liveConfig := cloneMapAny(req.continuationDraft.ConfigValue)
		for key, value := range liveConfig {
			configValue[key] = value
		}
	}
	defaultConfig := c.buildDefaultWorkflowConfigValue(threadType, req.UseWebSearch, req.NotionModel)
	for key, value := range defaultConfig {
		configValue[key] = value
	}
	configID := randomUUID()
	contextID := randomUUID()
	originalDatetime := now
	if req.continuationDraft != nil {
		configID = normalizeTranscriptStepID(req.continuationDraft.ConfigID)
		contextID = normalizeTranscriptStepID(req.continuationDraft.ContextID)
		if clean := strings.TrimSpace(req.continuationDraft.OriginalDatetime); clean != "" {
			originalDatetime = clean
		}
	}
	contextValue := map[string]any{
		"timezone":        "Asia/Shanghai",
		"userName":        c.Session.UserName,
		"userId":          c.Session.UserID,
		"userEmail":       c.Session.UserEmail,
		"spaceName":       c.Session.SpaceName,
		"spaceId":         c.Session.SpaceID,
		"currentDatetime": originalDatetime,
		"surface":         surface,
	}
	if req.continuationDraft != nil && len(req.continuationDraft.ContextValue) > 0 {
		liveContext := cloneMapAny(req.continuationDraft.ContextValue)
		for key, value := range liveContext {
			contextValue[key] = value
		}
	}
	contextValue["timezone"] = firstNonEmpty(strings.TrimSpace(stringValue(contextValue["timezone"])), "Asia/Shanghai")
	contextValue["userName"] = firstNonEmpty(strings.TrimSpace(c.Session.UserName), strings.TrimSpace(stringValue(contextValue["userName"])))
	contextValue["userId"] = firstNonEmpty(strings.TrimSpace(c.Session.UserID), strings.TrimSpace(stringValue(contextValue["userId"])))
	contextValue["userEmail"] = firstNonEmpty(strings.TrimSpace(c.Session.UserEmail), strings.TrimSpace(stringValue(contextValue["userEmail"])))
	contextValue["spaceName"] = firstNonEmpty(strings.TrimSpace(c.Session.SpaceName), strings.TrimSpace(stringValue(contextValue["spaceName"])))
	contextValue["spaceId"] = firstNonEmpty(strings.TrimSpace(c.Session.SpaceID), strings.TrimSpace(stringValue(contextValue["spaceId"])))
	if spaceViewID := firstNonEmpty(strings.TrimSpace(c.Session.SpaceViewID), strings.TrimSpace(stringValue(contextValue["spaceViewId"]))); spaceViewID != "" {
		contextValue["spaceViewId"] = spaceViewID
	} else {
		delete(contextValue, "spaceViewId")
	}
	contextValue["currentDatetime"] = originalDatetime
	contextValue["surface"] = surface
	if req.continuationDraft != nil && hiddenPrompt != "" {
		contextValue["instructions"] = hiddenPrompt
		contextValue["runtimePromptHint"] = hiddenPrompt
	}
	transcript := []map[string]any{}
	if req.continuationDraft != nil {
		transcript = append(transcript, buildContinuationBaseTranscript(req.continuationDraft, configValue, contextValue)...)
	} else {
		transcript = append(transcript,
			map[string]any{
				"id":    configID,
				"type":  "config",
				"value": configValue,
			},
			map[string]any{
				"id":    contextID,
				"type":  "context",
				"value": contextValue,
			},
		)
		if hiddenPrompt != "" {
			transcript = append(transcript, map[string]any{
				"id":   randomUUID(),
				"type": "context",
				"value": map[string]any{
					"instructions":      hiddenPrompt,
					"runtimePromptHint": hiddenPrompt,
				},
			})
		}
		if c.Config.Features.ForceDisableUpstreamEdits {
			transcript = append(transcript, map[string]any{
				"id":   randomUUID(),
				"type": "updated-config",
				"value": map[string]any{
					"useReadOnlyMode":                   true,
					"writerMode":                        false,
					"enableUpdatePageAutofixer":         false,
					"enableUpdatePageOrderUpdates":      false,
					"enableAgentSupportPropertyReorder": false,
				},
			})
		}
	}
	if req.continuationScaffold != nil {
		if clean := strings.TrimSpace(req.continuationScaffold.UpdatedConfigID); clean != "" {
			transcript = append(transcript, map[string]any{
				"id":   clean,
				"type": "updated-config",
			})
		}
	}
	for _, item := range attachments {
		transcript = append(transcript, map[string]any{
			"id":          randomUUID(),
			"type":        "attachment",
			"fileName":    item.Name,
			"contentType": item.ContentType,
			"fileUrl":     item.AttachmentURL,
			"metadata":    buildAttachmentStepMetadata(item),
		})
	}
	userStepID := randomUUID()
	userCreatedAt := now
	if req.continuationScaffold != nil {
		if clean := strings.TrimSpace(req.continuationScaffold.UserStepID); clean != "" {
			userStepID = clean
		}
		if clean := strings.TrimSpace(req.continuationScaffold.UserCreatedAt); clean != "" {
			userCreatedAt = clean
		}
	}
	userStep := map[string]any{
		"id":        userStepID,
		"type":      "user",
		"value":     [][]string{{maskIfEnabled(req)}},
		"userId":    c.Session.UserID,
		"createdAt": userCreatedAt,
	}
	transcript = append(transcript, userStep)
	// c2a few-shot：assistant 完整输出示例（模型直接模仿格式输出工具块）
	if sample := strings.TrimSpace(req.ToolBridgeAssistantSample); sample != "" {
		transcript = append(transcript, map[string]any{
			"id":        randomUUID(),
			"type":      "assistant",
			"value":     [][]string{{sample}},
			"userId":    c.Session.UserID,
			"createdAt": now,
		})
	}
	attachmentPayloads := make([]map[string]any, 0, len(attachments))
	for _, item := range attachments {
		attachmentPayloads = append(attachmentPayloads, map[string]any{
			"type":        "attachment",
			"fileName":    item.Name,
			"contentType": item.ContentType,
			"fileUrl":     item.AttachmentURL,
		})
	}
	payload := map[string]any{
		"spaceId":                       c.Session.SpaceID,
		"threadId":                      threadID,
		"createThread":                  strings.TrimSpace(req.UpstreamThreadID) == "" && !req.attachmentThreadReady,
		"generateTitle":                 strings.TrimSpace(req.UpstreamThreadID) == "" && !req.SuppressUpstreamThreadPersistence,
		"traceId":                       randomUUID(),
		"transcript":                    transcript,
		"threadType":                    threadType,
		"asPatchResponse":               true,
		"isPartialTranscript":           req.continuationDraft != nil,
		"saveAllThreadOperations":       !req.SuppressUpstreamThreadPersistence,
		"setUnreadState":                true,
		"createdSource":                 surface,
		"isUserInAnySalesAssistedSpace": false,
		"isSpaceSalesAssisted":          false,
	}
	payload["debugOverrides"] = map[string]any{
		"annotationInferences":            map[string]any{},
		"cachedInferences":                map[string]any{},
		"emitAgentSearchExtractedResults": true,
		"emitInferences":                  false,
	}
	if strings.TrimSpace(req.UpstreamThreadID) == "" && !req.attachmentThreadReady {
		payload["threadParentPointer"] = map[string]any{
			"table":   "space",
			"id":      c.Session.SpaceID,
			"spaceId": c.Session.SpaceID,
		}
	}
	if len(attachmentPayloads) > 0 {
		payload["attachments"] = attachmentPayloads
	}
	return payload, inferencePayloadMeta{
		ConfigID:         configID,
		ContextID:        contextID,
		OriginalDatetime: originalDatetime,
		IsPartial:        req.continuationDraft != nil,
	}
}

func (c *NotionAIClient) preparePromptRequest(ctx context.Context, req PromptRunRequest) (string, []UploadedAttachment, string, map[string]any, inferencePayloadMeta, error) {
	cleanPrompt := strings.TrimSpace(req.Prompt)
	if cleanPrompt == "" && len(req.Attachments) == 0 {
		return "", nil, "", nil, inferencePayloadMeta{}, fmt.Errorf("prompt is empty")
	}
	if cleanPrompt == "" {
		cleanPrompt = defaultUploadedAttachmentPrompt
	}
	continuation := strings.TrimSpace(req.UpstreamThreadID) != ""
	threadID := firstNonEmpty(strings.TrimSpace(req.UpstreamThreadID), randomUUID())
	uploadedAttachments, actualThreadID, err := c.uploadAttachments(ctx, threadID, req.Attachments, !continuation)
	if err != nil {
		return "", nil, "", nil, inferencePayloadMeta{}, err
	}
	preparedReq := PromptRunRequest{
		Prompt:                            cleanPrompt,
		HiddenPrompt:                      strings.TrimSpace(req.HiddenPrompt),
		PublicModel:                       req.PublicModel,
		NotionModel:                       req.NotionModel,
		UseWebSearch:                      req.UseWebSearch,
		UpstreamThreadID:                  req.UpstreamThreadID,
		SuppressUpstreamThreadPersistence: req.SuppressUpstreamThreadPersistence,
		SessionFingerprint:                req.SessionFingerprint,
		RawMessageCount:                   req.RawMessageCount,
		ConversationID:                    req.ConversationID,
		MaskLocalPaths:                    req.MaskLocalPaths,
		ClientWorkingDirectory:            req.ClientWorkingDirectory,
		AllowTextToolSynthesis:            req.AllowTextToolSynthesis,
		ToolBridgeSection:                 req.ToolBridgeSection,
		ToolBridgeAssistantSample:         req.ToolBridgeAssistantSample,
		ToolsRaw:                          req.ToolsRaw,
		attachmentThreadReady:             !continuation && len(uploadedAttachments) > 0,
		continuationDraft:                 req.continuationDraft,
	}
	if strings.TrimSpace(preparedReq.UpstreamThreadID) != "" {
		bestEffortCtx, cancel, ok := bestEffortContext(ctx, bestEffortUpstreamTimeout)
		if ok {
			draft, draftErr := c.prepareContinuationDraftFromThread(bestEffortCtx, preparedReq.UpstreamThreadID)
			cancel()
			if draftErr == nil {
				preparedReq.continuationDraft = mergeContinuationDraft(preparedReq.continuationDraft, draft)
			} else {
				c.logBestEffortFailure("prepareContinuationDraftFromThread", draftErr)
			}
		}
	}
	if strings.TrimSpace(preparedReq.UpstreamThreadID) != "" {
		scaffold, saveErr := c.saveContinuationScaffold(ctx, preparedReq.UpstreamThreadID, cleanPrompt, preparedReq.continuationDraft)
		if saveErr != nil {
			return "", nil, "", nil, inferencePayloadMeta{}, saveErr
		}
		preparedReq.continuationScaffold = scaffold
	}
	c.ensureSessionLiveMetadata(ctx)
	payload, meta := c.buildInferencePayload(preparedReq, actualThreadID, uploadedAttachments)
	return cleanPrompt, uploadedAttachments, actualThreadID, payload, meta, nil
}

func textDeltaSuffix(previous string, current string) string {
	if current == "" || current == previous {
		return ""
	}
	if previous == "" {
		return current
	}
	if strings.HasPrefix(current, previous) {
		return current[len(previous):]
	}
	prevRunes := []rune(previous)
	currRunes := []rune(current)
	index := 0
	for index < len(prevRunes) && index < len(currRunes) && prevRunes[index] == currRunes[index] {
		index++
	}
	if index >= len(currRunes) {
		return ""
	}
	return string(currRunes[index:])
}

func (c *NotionAIClient) pollFinalAnswerStream(ctx context.Context, threadID string, onDelta func(string) error) ([]string, agentMessage, error) {
	// 黑洞检测（轮询阶段）：被限制账号的 sync 每次可能黑洞 20s，叠加轮询上限会
	// 拖死请求。整体限 45s，超时判账号级故障由 dispatch 换号。
	pollCtx, cancel, ok := bestEffortContext(ctx, accountPollBlackholeTimeout)
	if ok {
		defer cancel()
		ctx = pollCtx
	}
	var lastAgent agentMessage
	var haveAgent bool
	lastMessageIDs := []string{}
	emittedText := ""
	for i := 0; i < c.PollMaxRounds; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, agentMessage{}, ctx.Err()
			case <-time.After(c.PollInterval):
			}
		}
		threadData, err := c.syncThread(ctx, threadID)
		if err != nil {
			return nil, agentMessage{}, err
		}
		messageIDs := messageIDsFromThreadRecord(threadData, threadID)
		if len(messageIDs) == 0 {
			continue
		}
		lastMessageIDs = messageIDs
		messageData, err := c.syncThreadMessages(ctx, threadID, messageIDs)
		if err != nil {
			return nil, agentMessage{}, err
		}
		recordMap := mapValue(messageData["recordMap"])
		_, agent, outcomeErr, ok := finalThreadOutcomeFromRecordMap(recordMap, threadID)
		if outcomeErr != nil {
			return messageIDs, agentMessage{}, outcomeErr
		}
		if ok {
			lastAgent = agent
			haveAgent = true
		}
		if haveAgent && strings.TrimSpace(lastAgent.Text) != "" {
			delta := textDeltaSuffix(emittedText, lastAgent.Text)
			if delta != "" && onDelta != nil {
				if err := onDelta(delta); err != nil {
					return lastMessageIDs, lastAgent, err
				}
			}
			emittedText = lastAgent.Text
		}
		if haveAgent && lastAgent.Completed && strings.TrimSpace(lastAgent.Text) != "" {
			return messageIDs, lastAgent, nil
		}
	}
	if haveAgent {
		return nil, agentMessage{}, fmt.Errorf("agent message incomplete after polling: %+v", lastAgent)
	}
	return nil, agentMessage{}, fmt.Errorf("thread %s did not produce any agent-inference message; last_message_ids=%v", threadID, lastMessageIDs)
}

func (c *NotionAIClient) RunPrompt(ctx context.Context, req PromptRunRequest) (InferenceResult, error) {
	cleanPrompt, uploadedAttachments, actualThreadID, payload, meta, err := c.preparePromptRequest(ctx, req)
	if err != nil {
		return InferenceResult{}, err
	}
	traceID := stringValue(payload["traceId"])
	parsed, parseErr := c.runInferenceTranscriptWithFallback(ctx, payload, actualThreadID, InferenceStreamSink{})
	messageIDs := parsed.MessageIDs
	finalAgent := parsed.FinalAgent
	if strings.TrimSpace(finalAgent.Text) == "" {
		var stepErr *inferenceStepError
		var transportErr *inferenceTransportError
		if parseErr != nil && errors.As(parseErr, &stepErr) {
			return InferenceResult{}, stepErr
		}
		if parseErr != nil && errors.As(parseErr, &transportErr) {
			return InferenceResult{}, transportErr
		}
		if parseErr != nil && parsed.LineCount == 0 && len(parsed.MessageIDs) == 0 {
			return InferenceResult{}, parseErr
		}
		// 账号被标记时上游只回 scaffold 行（无 agent-inference，parseErr==nil）——
		// 立即判账号级故障，交给 dispatch 换号，不做 loadFinalAnswerOnce/poll 的长等待
		if parseErr == nil && !parsed.HasAgentInference {
			return InferenceResult{}, errAccountStarved
		}
		messageIDs, finalAgent, err = c.loadFinalAnswerOnce(ctx, actualThreadID)
		if err != nil {
			messageIDs, finalAgent, err = c.pollFinalAnswer(ctx, actualThreadID)
			if err != nil {
				return InferenceResult{}, err
			}
		}
	} else if parseErr != nil {
		messageIDs, finalAgent, err = c.loadFinalAnswerOnce(ctx, actualThreadID)
		if err != nil {
			return InferenceResult{}, parseErr
		}
	}
	if strings.TrimSpace(finalAgent.Text) == "" && len(parsed.ToolUses) > 0 {
		// P2-2 修复:tool_use-only 回合(模型本轮只调用工具、无最终文本)是合法结果,
		// 不应整轮判失败——否则会连带丢弃已解析的 ToolUses。
		return InferenceResult{
			Prompt:           cleanPrompt,
			Model:            strings.TrimSpace(req.PublicModel),
			NotionModel:      strings.TrimSpace(req.NotionModel),
			ThreadID:         actualThreadID,
			TraceID:          traceID,
			NDJSONLineCount:  parsed.LineCount,
			RawMessageIDs:    messageIDs,
			Attachments:      uploadedAttachments,
			ConfigID:         meta.ConfigID,
			ContextID:        meta.ContextID,
			OriginalDatetime: meta.OriginalDatetime,
			ToolUses:         append([]InferenceToolUse(nil), parsed.ToolUses...),
		}, nil
	}
	if strings.TrimSpace(finalAgent.Text) == "" {
		return InferenceResult{}, fmt.Errorf("thread %s finished without final text", actualThreadID)
	}
	lineCount := parsed.LineCount
	if strings.TrimSpace(req.UpstreamThreadID) != "" && !req.SuppressUpstreamThreadPersistence {
		c.markInferenceTranscriptSeenBestEffort(ctx, actualThreadID)
	}
	return InferenceResult{
		Prompt:           cleanPrompt,
		Model:            strings.TrimSpace(req.PublicModel),
		NotionModel:      strings.TrimSpace(req.NotionModel),
		ThreadID:         actualThreadID,
		TraceID:          traceID,
		Text:             finalAgent.Text,
		Reasoning:        firstNonEmpty(finalAgent.Reasoning, parsed.Reasoning),
		MessageID:        finalAgent.MessageID,
		CompletedTime:    finalAgent.CompletedTime,
		NDJSONLineCount:  lineCount,
		RawMessageIDs:    messageIDs,
		Attachments:      uploadedAttachments,
		ConfigID:         meta.ConfigID,
		ContextID:        meta.ContextID,
		OriginalDatetime: meta.OriginalDatetime,
		ToolUses:         append([]InferenceToolUse(nil), parsed.ToolUses...),
	}, nil
}

func (c *NotionAIClient) RunPromptStream(ctx context.Context, req PromptRunRequest, onDelta func(string) error) (InferenceResult, error) {
	return c.RunPromptStreamWithSink(ctx, req, InferenceStreamSink{Text: onDelta})
}

func (c *NotionAIClient) RunPromptStreamWithSink(ctx context.Context, req PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
	cleanPrompt, uploadedAttachments, actualThreadID, payload, meta, err := c.preparePromptRequest(ctx, req)
	if err != nil {
		return InferenceResult{}, err
	}
	traceID := stringValue(payload["traceId"])

	parsed, err := c.streamRunInferenceTranscript(ctx, payload, actualThreadID, sink, req.StreamReasoningWarmup)
	if err != nil {
		var transportErr *inferenceTransportError
		if errors.As(err, &transportErr) {
			return InferenceResult{}, transportErr
		}
		if parsed.LineCount == 0 && len(parsed.MessageIDs) == 0 {
			return InferenceResult{}, err
		}
		return InferenceResult{}, err
	}
	messageIDs := parsed.MessageIDs
	finalAgent := parsed.FinalAgent
	if strings.TrimSpace(finalAgent.Text) == "" {
		// 账号被标记时上游只回 scaffold 行（无 agent-inference，err==nil）——
		// 立即判账号级故障，交给 dispatch 换号，不做 loadFinalAnswerOnce/poll 的长等待
		if err == nil && !parsed.HasAgentInference {
			return InferenceResult{}, errAccountStarved
		}
		messageIDs, finalAgent, err = c.loadFinalAnswerOnce(ctx, actualThreadID)
		if err != nil {
			messageIDs, finalAgent, err = c.pollFinalAnswerStream(ctx, actualThreadID, sink.Text)
			if err != nil {
				return InferenceResult{}, err
			}
		}
	}
	if strings.TrimSpace(finalAgent.Text) == "" && len(parsed.ToolUses) > 0 {
		// P2-2 修复:tool_use-only 回合合法,连同已解析 ToolUses 一并返回(流式同径)
		return InferenceResult{
			Prompt:           cleanPrompt,
			Model:            strings.TrimSpace(req.PublicModel),
			NotionModel:      strings.TrimSpace(req.NotionModel),
			ThreadID:         actualThreadID,
			TraceID:          traceID,
			NDJSONLineCount:  parsed.LineCount,
			RawMessageIDs:    messageIDs,
			Attachments:      uploadedAttachments,
			ConfigID:         meta.ConfigID,
			ContextID:        meta.ContextID,
			OriginalDatetime: meta.OriginalDatetime,
			ToolUses:         append([]InferenceToolUse(nil), parsed.ToolUses...),
		}, nil
	}
	if strings.TrimSpace(finalAgent.Text) == "" {
		return InferenceResult{}, fmt.Errorf("thread %s finished without final text", actualThreadID)
	}
	if strings.TrimSpace(req.UpstreamThreadID) != "" && !req.SuppressUpstreamThreadPersistence {
		c.markInferenceTranscriptSeenBestEffort(ctx, actualThreadID)
	}
	return InferenceResult{
		Prompt:           cleanPrompt,
		Model:            strings.TrimSpace(req.PublicModel),
		NotionModel:      strings.TrimSpace(req.NotionModel),
		ThreadID:         actualThreadID,
		TraceID:          traceID,
		Text:             finalAgent.Text,
		Reasoning:        firstNonEmpty(finalAgent.Reasoning, parsed.Reasoning),
		MessageID:        finalAgent.MessageID,
		CompletedTime:    finalAgent.CompletedTime,
		NDJSONLineCount:  parsed.LineCount,
		RawMessageIDs:    messageIDs,
		Attachments:      uploadedAttachments,
		ConfigID:         meta.ConfigID,
		ContextID:        meta.ContextID,
		OriginalDatetime: meta.OriginalDatetime,
	}, nil
}
