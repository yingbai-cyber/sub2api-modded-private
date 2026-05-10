package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/cespare/xxhash/v2"
	gocache "github.com/patrickmn/go-cache"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/sync/singleflight"
)

const (
	claudeAPIURL            = "https://api.anthropic.com/v1/messages?beta=true"
	claudeAPICountTokensURL = "https://api.anthropic.com/v1/messages/count_tokens?beta=true"
	stickySessionTTL        = time.Hour // 粘性会话TTL
	defaultMaxLineSize      = 500 * 1024 * 1024
	// Canonical Claude Code banner. Keep it EXACT (no trailing whitespace/newlines)
	// to match real Claude CLI traffic as closely as possible. When we need a visual
	// separator between system blocks, we add "\n\n" at concatenation time.
	claudeCodeSystemPrompt = "You are Claude Code, Anthropic's official CLI for Claude."
	// claudeCodeSystemPromptExpansion 是真实 Claude Code 主系统提示词中"与具体工具无关"
	// 的通用段落（身份/用途总述 + 安全声明 + URL 告警 + Tone and style），逐字取自真实
	// CLI（2.1.x 一致）。伪装路径用它把 system 块数从 2 提升到 3、体量贴近真实 CC，同时
	// 刻意排除 # Doing tasks / # Using your tools / # Executing actions 等会污染被代理
	// 用户行为的工具专属指令。
	claudeCodeSystemPromptExpansion = `You are an interactive agent that helps users with software engineering tasks. Use the instructions below and the tools available to you to assist the user.

IMPORTANT: Assist with authorized security testing, defensive security, CTF challenges, and educational contexts. Refuse requests for destructive techniques, DoS attacks, mass targeting, supply chain compromise, or detection evasion for malicious purposes. Dual-use security tools (C2 frameworks, credential testing, exploit development) require clear authorization context: pentesting engagements, CTF competitions, security research, or defensive use cases.
IMPORTANT: You must NEVER generate or guess URLs for the user unless you are confident that the URLs are for helping the user with programming. You may use URLs provided by the user in their messages or local files.

# Tone and style
 - Only use emojis if the user explicitly requests it. Avoid using emojis in all communication unless asked.
 - Your responses should be short and concise.
 - When referencing specific functions or pieces of code include the pattern file_path:line_number to allow the user to easily navigate to the source code location.
 - When referencing GitHub issues or pull requests, use the owner/repo#123 format (e.g. anthropics/claude-code#100) so they render as clickable links.
 - Do not use a colon before tool calls. Your tool calls may not be shown directly in the output, so text like "Let me read the file:" followed by a read tool call should just be "Let me read the file." with a period.`
	maxCacheControlBlocks = 4 // Anthropic API 允许的最大 cache_control 块数量

	defaultUserGroupRateCacheTTL           = 30 * time.Second
	defaultModelsListCacheTTL              = 15 * time.Second
	postUsageBillingTimeout                = 15 * time.Second
	claudeCodeNoopDeltaKeepaliveMinVersion = "2.1.193"
	debugGatewayBodyEnv                    = "SUB2API_DEBUG_GATEWAY_BODY"
	// 上游错误体只需要提取错误 JSON/日志摘要，默认 512KiB 避免错误风暴叠加大请求体。
	gatewayUpstreamErrorBodyReadLimit int64 = 512 << 10
)

const (
	claudeMimicDebugInfoKey = "claude_mimic_debug_info"
)

const (
	cacheTTLTarget5m                   = "5m"
	cacheTTLTarget1h                   = "1h"
	compositeModelOwnershipCachePrefix = "composite-owner|"
)

// ForceCacheBillingContextKey 强制缓存计费上下文键
// 用于粘性会话切换时，将 input_tokens 转为 cache_read_input_tokens 计费
type forceCacheBillingKeyType struct{}

// accountWithLoad 账号与负载信息的组合，用于负载感知调度
type accountWithLoad struct {
	account  *Account
	loadInfo *AccountLoadInfo
}

var ForceCacheBillingContextKey = forceCacheBillingKeyType{}

var (
	windowCostPrefetchCacheHitTotal  atomic.Int64
	windowCostPrefetchCacheMissTotal atomic.Int64
	windowCostPrefetchBatchSQLTotal  atomic.Int64
	windowCostPrefetchFallbackTotal  atomic.Int64
	windowCostPrefetchErrorTotal     atomic.Int64

	userGroupRateCacheHitTotal      atomic.Int64
	userGroupRateCacheMissTotal     atomic.Int64
	userGroupRateCacheLoadTotal     atomic.Int64
	userGroupRateCacheSFSharedTotal atomic.Int64
	userGroupRateCacheFallbackTotal atomic.Int64

	modelsListCacheHitTotal   atomic.Int64
	modelsListCacheMissTotal  atomic.Int64
	modelsListCacheStoreTotal atomic.Int64

	// Deprecated: flusher_enabled=true 后不再增长(仅 flag=false 降级直写路径使用);新主路径见 FlusherMetrics。remove after 2026-09。
	// userPlatformQuotaDBIncrErrorTotal 统计 finalizePostUsageBilling 异步 goroutine
	// 中 IncrementUsageWithReset 失败次数。Redis 已成功累加 + DB 写失败意味着
	// Redis cache TTL 过期或被清后该笔 cost 会丢失（与实际消费偏差）。
	// oncall 通过 GatewayUserPlatformQuotaIncrStats() 暴露给 ops 面板做阈值告警。
	userPlatformQuotaDBIncrErrorTotal atomic.Int64
	// Deprecated: flusher_enabled=true 后不再增长(仅 flag=false 降级直写路径使用);新主路径见 FlusherMetrics。remove after 2026-09。
	// userPlatformQuotaDBIncrLegacyErrorTotal 统计 legacy postUsageBilling
	// （applyUsageBilling 在 repo==nil 时 fallback）路径下的失败次数；
	// 与 DB Incr 失败分开计数，便于区分"主路径暂时故障"vs"基础设施长期未配齐"。
	userPlatformQuotaDBIncrLegacyErrorTotal atomic.Int64
	// userPlatformQuotaSentinelSetCacheErrorTotal 统计 checkUserPlatformQuotaEligibility
	// 在 DB 无行时回填 sentinel cache entry 写 Redis 失败的次数（phase A）。
	userPlatformQuotaSentinelSetCacheErrorTotal atomic.Int64
)

func GatewayWindowCostPrefetchStats() (cacheHit, cacheMiss, batchSQL, fallback, errCount int64) {
	return windowCostPrefetchCacheHitTotal.Load(),
		windowCostPrefetchCacheMissTotal.Load(),
		windowCostPrefetchBatchSQLTotal.Load(),
		windowCostPrefetchFallbackTotal.Load(),
		windowCostPrefetchErrorTotal.Load()
}

func GatewayUserGroupRateCacheStats() (cacheHit, cacheMiss, load, singleflightShared, fallback int64) {
	return userGroupRateCacheHitTotal.Load(),
		userGroupRateCacheMissTotal.Load(),
		userGroupRateCacheLoadTotal.Load(),
		userGroupRateCacheSFSharedTotal.Load(),
		userGroupRateCacheFallbackTotal.Load()
}

func GatewayModelsListCacheStats() (cacheHit, cacheMiss, store int64) {
	return modelsListCacheHitTotal.Load(), modelsListCacheMissTotal.Load(), modelsListCacheStoreTotal.Load()
}

// GatewayUserPlatformQuotaIncrStats 返回 (mainPathErr, legacyPathErr, sentinelSetErr)。
// mainPathErr：finalizePostUsageBilling 异步 goroutine 写 DB 失败累计次数；
// legacyPathErr：postUsageBilling fallback 路径写 DB 失败累计次数；
// sentinelSetErr：DB 无行时回填 sentinel cache entry 写 Redis 失败累计次数。
// ops 监控面板可以按"持续上升斜率"做告警阈值。
func GatewayUserPlatformQuotaIncrStats() (mainPathErr, legacyPathErr, sentinelSetErr int64) {
	return userPlatformQuotaDBIncrErrorTotal.Load(),
		userPlatformQuotaDBIncrLegacyErrorTotal.Load(),
		userPlatformQuotaSentinelSetCacheErrorTotal.Load()
}

// GatewayUserPlatformQuotaFlusherStats 暴露 flusher 运行指标供 ops/health 面板查询。
func GatewayUserPlatformQuotaFlusherStats(f *UserPlatformQuotaUsageFlusher) map[string]int64 {
	if f == nil || f.metrics == nil {
		return nil
	}
	m := f.metrics
	return map[string]int64{
		"flush_success":        m.FlushSuccessTotal.Load(),
		"flush_error":          m.FlushErrorTotal.Load(),
		"flush_batch_size":     m.FlushBatchSizeTotal.Load(),
		"flush_latency_ms_max": m.FlushLatencyMsMax.Load(),
		"dirty_readd":          m.DirtyReaddTotal.Load(),
		"dirty_lost":           m.DirtyLostTotal.Load(),
		"flush_fk_violation":   m.FlushFKViolationTotal.Load(),
	}
}

func openAIStreamEventIsTerminal(data string) bool {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return false
	}
	if trimmed == "[DONE]" {
		return true
	}
	return openAIStreamEventTypeIsTerminal(gjson.Get(trimmed, "type").String())
}

// openAIStreamEventIsTerminalWithType 复用已提取的 type，避免 SSE 热路径重复扫描 JSON。
func openAIStreamEventIsTerminalWithType(data, eventType string) bool {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return false
	}
	if trimmed == "[DONE]" {
		return true
	}
	return openAIStreamEventTypeIsTerminal(eventType)
}

func openAIStreamEventTypeIsTerminal(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "error":
		return true
	default:
		return false
	}
}

func anthropicStreamEventIsTerminal(eventName, data string) bool {
	if strings.EqualFold(strings.TrimSpace(eventName), "message_stop") {
		return true
	}
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return false
	}
	if trimmed == "[DONE]" {
		return true
	}
	return gjson.Get(trimmed, "type").String() == "message_stop"
}

func cloneStringSlice(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	dst := make([]string, len(src))
	copy(dst, src)
	return dst
}

// IsForceCacheBilling 检查是否启用强制缓存计费
func IsForceCacheBilling(ctx context.Context) bool {
	v, _ := ctx.Value(ForceCacheBillingContextKey).(bool)
	return v
}

// WithForceCacheBilling 返回带有强制缓存计费标记的上下文
func WithForceCacheBilling(ctx context.Context) context.Context {
	return context.WithValue(ctx, ForceCacheBillingContextKey, true)
}

func (s *GatewayService) debugModelRoutingEnabled() bool {
	if s == nil {
		return false
	}
	return s.debugModelRouting.Load()
}

func (s *GatewayService) debugClaudeMimicEnabled() bool {
	if s == nil {
		return false
	}
	return s.debugClaudeMimic.Load()
}

func parseDebugEnvBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func shortSessionHash(sessionHash string) string {
	if sessionHash == "" {
		return ""
	}
	if len(sessionHash) <= 8 {
		return sessionHash
	}
	return sessionHash[:8]
}

func redactAuthHeaderValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	// Keep scheme for debugging, redact secret.
	if strings.HasPrefix(strings.ToLower(v), "bearer ") {
		return "Bearer [redacted]"
	}
	return "[redacted]"
}

func safeHeaderValueForLog(key string, v string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	switch key {
	case "authorization", "x-api-key":
		return redactAuthHeaderValue(v)
	default:
		return strings.TrimSpace(v)
	}
}

func extractSystemPreviewFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	sys := gjson.GetBytes(body, "system")
	if !sys.Exists() {
		return ""
	}

	switch {
	case sys.IsArray():
		for _, item := range sys.Array() {
			if !item.IsObject() {
				continue
			}
			if strings.EqualFold(item.Get("type").String(), "text") {
				if t := item.Get("text").String(); strings.TrimSpace(t) != "" {
					return t
				}
			}
		}
		return ""
	case sys.Type == gjson.String:
		return sys.String()
	default:
		return ""
	}
}

func buildClaudeMimicDebugLine(req *http.Request, body []byte, account *Account, tokenType string, mimicClaudeCode bool) string {
	if req == nil {
		return ""
	}

	// Only log a minimal fingerprint to avoid leaking user content.
	interesting := []string{
		"user-agent",
		"x-app",
		"anthropic-dangerous-direct-browser-access",
		"anthropic-version",
		"anthropic-beta",
		"x-stainless-lang",
		"x-stainless-package-version",
		"x-stainless-os",
		"x-stainless-arch",
		"x-stainless-runtime",
		"x-stainless-runtime-version",
		"x-stainless-retry-count",
		"x-stainless-timeout",
		"authorization",
		"x-api-key",
		"content-type",
		"accept",
		"x-stainless-helper-method",
	}

	h := make([]string, 0, len(interesting))
	for _, k := range interesting {
		if v := req.Header.Get(k); v != "" {
			h = append(h, fmt.Sprintf("%s=%q", k, safeHeaderValueForLog(k, v)))
		}
	}

	metaUserID := strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String())
	sysPreview := strings.TrimSpace(extractSystemPreviewFromBody(body))

	// Truncate preview to keep logs sane.
	if len(sysPreview) > 300 {
		sysPreview = sysPreview[:300] + "..."
	}
	sysPreview = strings.ReplaceAll(sysPreview, "\n", "\\n")
	sysPreview = strings.ReplaceAll(sysPreview, "\r", "\\r")

	aid := int64(0)
	aname := ""
	if account != nil {
		aid = account.ID
		aname = account.Name
	}

	return fmt.Sprintf(
		"url=%s account=%d(%s) tokenType=%s mimic=%t meta.user_id=%q system.preview=%q headers={%s}",
		req.URL.String(),
		aid,
		aname,
		tokenType,
		mimicClaudeCode,
		metaUserID,
		sysPreview,
		strings.Join(h, " "),
	)
}

func logClaudeMimicDebug(req *http.Request, body []byte, account *Account, tokenType string, mimicClaudeCode bool) {
	line := buildClaudeMimicDebugLine(req, body, account, tokenType, mimicClaudeCode)
	if line == "" {
		return
	}
	logger.LegacyPrintf("service.gateway", "[ClaudeMimicDebug] %s", line)
}

func isClaudeCodeCredentialScopeError(msg string) bool {
	m := strings.ToLower(strings.TrimSpace(msg))
	if m == "" {
		return false
	}
	return strings.Contains(m, "only authorized for use with claude code") &&
		strings.Contains(m, "cannot be used for other api requests")
}

// sseDataRe matches SSE data lines with optional whitespace after colon.
// Some upstream APIs return non-standard "data:" without space (should be "data: ").
var (
	sseDataRe            = regexp.MustCompile(`^data:\s*`)
	claudeCliUserAgentRe = regexp.MustCompile(`(?i)^claude-cli/\d+\.\d+\.\d+`)

	// claudeCodePromptPrefixes 用于检测 Claude Code 系统提示词的前缀列表
	// 支持多种变体：标准版、Agent SDK 版、Explore Agent 版、Compact 版等
	// 注意：前缀之间不应存在包含关系，否则会导致冗余匹配
	claudeCodePromptPrefixes = []string{
		"You are Claude Code, Anthropic's official CLI for Claude",             // 标准版 & Agent SDK 版（含 running within...）
		"You are a Claude agent, built on Anthropic's Claude Agent SDK",        // Agent SDK 变体
		"You are a file search specialist for Claude Code",                     // Explore Agent 版
		"You are a helpful AI assistant tasked with summarizing conversations", // Compact 版
	}
)

// ErrNoAvailableAccounts 表示没有可用的账号
var ErrNoAvailableAccounts = errors.New("no available accounts")

// ErrClaudeCodeOnly 表示分组仅允许 Claude Code 客户端访问
var ErrClaudeCodeOnly = errors.New("this group only allows Claude Code clients")

// allowedHeaders 白名单headers（参考CRS项目）
var allowedHeaders = map[string]bool{
	"accept":                                    true,
	"x-stainless-retry-count":                   true,
	"x-stainless-timeout":                       true,
	"x-stainless-lang":                          true,
	"x-stainless-package-version":               true,
	"x-stainless-os":                            true,
	"x-stainless-arch":                          true,
	"x-stainless-runtime":                       true,
	"x-stainless-runtime-version":               true,
	"x-stainless-helper-method":                 true,
	"anthropic-dangerous-direct-browser-access": true,
	"anthropic-version":                         true,
	"x-app":                                     true,
	"anthropic-beta":                            true,
	"accept-language":                           true,
	"sec-fetch-mode":                            true,
	"user-agent":                                true,
	"content-type":                              true,
	"accept-encoding":                           true,
	"x-claude-code-session-id":                  true,
	"x-client-request-id":                       true,
}

// ErrStickySessionNotFound is returned by GatewayCache.GetSessionAccountID
// when no binding exists for the session. It abstracts away the underlying
// cache implementation (e.g. redis.Nil), mirroring ErrRefreshTokenNotFound.
var ErrStickySessionNotFound = errors.New("sticky session not found")

// ErrReasoningContentNotFound is returned by GatewayCache.GetReasoningContent
// when no cached reasoning content exists for the reasoning item ID.
var ErrReasoningContentNotFound = errors.New("reasoning content not found")

// GatewayCache 定义网关服务的缓存操作接口。
// 提供粘性会话（Sticky Session）的存储、查询、刷新和删除功能。
//
// GatewayCache defines cache operations for gateway service.
// Provides sticky session storage, retrieval, refresh and deletion capabilities.
type GatewayCache interface {
	// GetSessionAccountID 获取粘性会话绑定的账号 ID；无绑定时返回
	// ErrStickySessionNotFound，使 service 层无需依赖具体缓存实现即可
	// 区分"未绑定"与真实读取失败。
	// Get the account ID bound to a sticky session. Returns
	// ErrStickySessionNotFound when no binding exists so service code can
	// distinguish a miss from a real read failure without importing the
	// cache driver.
	GetSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error)
	// SetSessionAccountID 设置粘性会话与账号的绑定关系
	// Set the binding between sticky session and account
	SetSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error
	// RefreshSessionTTL 刷新粘性会话的过期时间
	// Refresh the expiration time of a sticky session
	RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error
	// DeleteSessionAccountID 删除粘性会话绑定，用于账号不可用时主动清理
	// Delete sticky session binding, used to proactively clean up when account becomes unavailable
	DeleteSessionAccountID(ctx context.Context, groupID int64, sessionHash string) error

	// Grok async video billing snapshot (create → status success).
	// SetGrokVideoPendingBilling stores create-time model/duration/resolution for status billing.
	SetGrokVideoPendingBilling(ctx context.Context, key string, payload []byte, ttl time.Duration) error
	// GetGrokVideoPendingBilling returns the create-time billing snapshot; miss → nil, nil.
	GetGrokVideoPendingBilling(ctx context.Context, key string) ([]byte, error)
	// ClaimGrokVideoBilled atomically marks a video request as billed (SetNX).
	// Returns true when this caller won the claim; false when already billed or claim unavailable.
	ClaimGrokVideoBilled(ctx context.Context, key string, ttl time.Duration) (bool, error)
	// ReleaseGrokVideoBilled clears a claim so a failed RecordUsage can retry billing.
	ReleaseGrokVideoBilled(ctx context.Context, key string) error

	// Reasoning content cache (Responses→Chat Completions 桥接）。
	// SetReasoningContent 按 reasoning item id 缓存 reasoning 全文，供后续请求
	// 在客户端不回传明文 summary 时回注 reasoning_content（DeepSeek thinking
	// mode 要求回传，否则 400）。
	SetReasoningContent(ctx context.Context, itemID string, content string, ttl time.Duration) error
	// GetReasoningContent 返回缓存的 reasoning 全文；未命中返回
	// ErrReasoningContentNotFound，使 service 层无需依赖具体缓存实现即可
	// 区分"未缓存"与真实读取失败。
	GetReasoningContent(ctx context.Context, itemID string) (string, error)
}

// derefGroupID safely dereferences *int64 to int64, returning 0 if nil
func derefGroupID(groupID *int64) int64 {
	if groupID == nil {
		return 0
	}
	return *groupID
}

func resolveUserGroupRateCacheTTL(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.Gateway.UserGroupRateCacheTTLSeconds <= 0 {
		return defaultUserGroupRateCacheTTL
	}
	return time.Duration(cfg.Gateway.UserGroupRateCacheTTLSeconds) * time.Second
}

func resolveModelsListCacheTTL(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.Gateway.ModelsListCacheTTLSeconds <= 0 {
		return defaultModelsListCacheTTL
	}
	return time.Duration(cfg.Gateway.ModelsListCacheTTLSeconds) * time.Second
}

func modelsListCacheKey(groupID *int64, platform string) string {
	return fmt.Sprintf("%d|%s", derefGroupID(groupID), strings.TrimSpace(platform))
}

func compositeModelOwnershipCacheKey(groupID int64, model string) string {
	return fmt.Sprintf("%s%d|%s", compositeModelOwnershipCachePrefix, groupID, strings.TrimSpace(model))
}

func prefetchedStickyGroupIDFromContext(ctx context.Context) (int64, bool) {
	return PrefetchedStickyGroupIDFromContext(ctx)
}

func prefetchedStickyAccountIDFromContext(ctx context.Context, groupID *int64) int64 {
	prefetchedGroupID, ok := prefetchedStickyGroupIDFromContext(ctx)
	if !ok || prefetchedGroupID != derefGroupID(groupID) {
		return 0
	}
	if accountID, ok := PrefetchedStickyAccountIDFromContext(ctx); ok && accountID > 0 {
		return accountID
	}
	return 0
}

// shouldClearStickySession 检查账号是否处于不可调度状态，需要清理粘性会话绑定。
// 委托 IsSchedulable() 判断账号级可调度性（状态、配额、过载、限流等），
// 额外检查模型级限流。
//
// shouldClearStickySession checks if an account is in an unschedulable state
// and the sticky session binding should be cleared.
// Delegates to IsSchedulable() for account-level checks, plus model-level rate limiting.
func shouldClearStickySession(account *Account, requestedModel string) bool {
	if account == nil {
		return false
	}
	if !account.IsSchedulable() {
		return true
	}
	if remaining := account.GetRateLimitRemainingTimeWithContext(context.Background(), requestedModel); remaining > 0 {
		return true
	}
	return false
}

type AccountWaitPlan struct {
	AccountID      int64
	MaxConcurrency int
	Timeout        time.Duration
	MaxWaiting     int
}

type AccountSelectionResult struct {
	Account     *Account
	Acquired    bool
	ReleaseFunc func()
	WaitPlan    *AccountWaitPlan // nil means no wait allowed
	// profitGate 携带本次选号真实生效的利润门（无门为 nil）。门安装在调度栈的
	// 局部 ctx 上，handler 必须经 ContextWithSelectionProfitGate 重放后才能在
	// 调度栈之外做抢槽后终检与准入后粘性绑定。
	profitGate *openAIProfitControlGate
}

// ProfitGateActive 报告本次选号是否处于利润门之下。
func (r *AccountSelectionResult) ProfitGateActive() bool {
	return r != nil && r.profitGate != nil
}

// ClaudeUsage 表示Claude API返回的usage信息
type ClaudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreation5mTokens    int // 5分钟缓存创建token（来自嵌套 cache_creation 对象）
	CacheCreation1hTokens    int // 1小时缓存创建token（来自嵌套 cache_creation 对象）
	ImageOutputTokens        int     `json:"image_output_tokens,omitempty"`
	KiroCredits              float64 `json:"kiro_credits,omitempty"` // Kiro credits 消耗（legacy kiro-rs / native Kiro 上游）
}

// ForwardResult 转发结果
type AudioUsage struct {
	Mode            string  // realtime | tts | stt
	DurationOrUnits float64 // minutes / million-chars / hours
}

type ForwardResult struct {
	RequestID string
	Usage     ClaudeUsage
	Model     string
	// UpstreamModel is the actual upstream model after mapping.
	// Prefer empty when it is identical to Model; persistence normalizes equal values away as no-op mappings.
	UpstreamModel string
	// UpstreamResponseModel is captured from the raw successful upstream
	// response before any client-facing rewrite or protocol conversion.
	UpstreamResponseModel         string
	UpstreamResponseModelConflict bool
	// UpstreamResponseServiceTier is the tier the upstream reports having used
	// (Anthropic usage.speed: "fast" / "standard"); "" when not declared.
	UpstreamResponseServiceTier string
	Stream                      bool
	Duration                    time.Duration
	FirstTokenMs                *int // 首字时间（流式请求）
	ClientDisconnect            bool // 客户端是否在流式传输过程中断开
	ReasoningEffort             *string
	// RequestedReasoningEffort is the client-requested effort before mapping.
	RequestedReasoningEffort *string
	// ServiceTier records the tier requested by the client. OpenAI uses
	// service_tier; Anthropic speed=fast is normalized to "fast". Usage recording
	// lowers it to UpstreamResponseServiceTier when the upstream reports a
	// cheaper tier (see ResolveBillingServiceTier).
	ServiceTier *string

	// 图片生成计费字段（图片生成模型使用）
	ImageCount         int    // 生成的图片数量
	ImageSize          string // 最终计费尺寸 "1K", "2K", "4K"
	ImageInputSize     string // 请求中的原始图片尺寸
	ImageOutputSize    string // 上游响应中的图片尺寸
	ImageOutputSizes   []string
	ImageSizeSource    string
	ImageSizeBreakdown map[string]int
	SearchCount        int
	AudioUsage         *AudioUsage
}

// GatewayFailureStage identifies which request stage failed. The zero value is
// intentionally treated as inference so existing UpstreamFailoverError callers
// retain their current behavior.
type GatewayFailureStage string

const (
	GatewayFailureStageInference   GatewayFailureStage = "inference"
	GatewayFailureStageAccountAuth GatewayFailureStage = "account_auth"
)

// GatewayFailureScope identifies whether selecting another account can help.
type GatewayFailureScope string

const (
	GatewayFailureScopeAccount  GatewayFailureScope = "account"
	GatewayFailureScopeProvider GatewayFailureScope = "provider"
	GatewayFailureScopeRequest  GatewayFailureScope = "request"
)

// NextAccountAction is tri-state for backwards compatibility. The zero value
// means legacy retry behavior; only NextAccountStop explicitly short-circuits.
type NextAccountAction uint8

const (
	NextAccountLegacyRetry NextAccountAction = iota
	NextAccountRetry
	NextAccountStop
)

type GatewayFailureReason string

// UpstreamFailoverError indicates an upstream or credential error that may
// trigger account failover. Additive metadata keeps existing composite literals
// source-compatible and preserves their legacy retry-next-account behavior.
type UpstreamFailoverError struct {
	StatusCode               int
	ResponseBody             []byte        // 上游响应体，用于错误透传规则匹配
	ResponseHeaders          http.Header   // 上游响应头，用于透传 cf-ray/cf-mitigated/content-type 等诊断信息
	ForceCacheBilling        bool          // Antigravity 粘性会话切换时设为 true
	RetryableOnSameAccount   bool          // 临时性错误（如 Google 间歇性 400、空响应），应在同一账号上重试 N 次再切换
	SameAccountRetryDelay    time.Duration // 同账号重试的最小间隔；零值使用 handler 默认值
	SameAccountRetryDeadline time.Time     // 同账号重试截止时间；零值表示仅受 retryLimit 限制
	SameAccountRetryMax      int           // 可选的错误级同账号重试上限，低于 handler 默认预算时优先采用
	RequestScopedTransient   bool          // 故障因素与账号无关（如上游按客户端身份/模型容量降载）：可同账号重试，但不得据此对账号做临时封禁
	SafeToFailoverAfterWrite bool          // 仅写出 SSE 注释等非语义字节时，仍可在同一客户端流中切换账号
	Stage                    GatewayFailureStage
	Scope                    GatewayFailureScope
	Reason                   GatewayFailureReason
	NextAccountAction        NextAccountAction
	ClientStatusCode         int
	ClientMessage            string
}

func (e *UpstreamFailoverError) Error() string {
	if e != nil && e.Stage == GatewayFailureStageAccountAuth {
		return fmt.Sprintf("credential failure: %s (failover)", e.Reason)
	}
	return fmt.Sprintf("upstream error: %d (failover)", e.StatusCode)
}

func (e *UpstreamFailoverError) ShouldRetryNextAccount() bool {
	return e != nil && e.NextAccountAction != NextAccountStop
}

func (e *UpstreamFailoverError) IsCredentialFailure() bool {
	return e != nil && e.Stage == GatewayFailureStageAccountAuth
}

// ShouldReportAccountScheduleFailure prevents provider- and request-scoped
// credential failures from being misattributed to the selected account. Legacy
// and inference failures retain their existing scheduler-health behavior.
func (e *UpstreamFailoverError) ShouldReportAccountScheduleFailure() bool {
	if e == nil {
		return false
	}
	return !e.IsCredentialFailure() || e.Scope == GatewayFailureScopeAccount
}

// sseStreamErrorEventError 表示上游 SSE 流体内出现 event:error 帧。
// RawData 是该事件 data: 行的原始 JSON 字符串
// （Anthropic 标准结构 {"type":"error","error":{"type":"...","message":"..."}}）。
// Error() 保持原字符串以兼容现有日志/检索；调用方应通过 errors.As
// 提取 RawData 并构造 UpstreamFailoverError.ResponseBody。
type sseStreamErrorEventError struct {
	RawData string
}

func (e *sseStreamErrorEventError) Error() string { return "have error in stream" }

// TempUnscheduleRetryableError 对 RetryableOnSameAccount 类型的 failover 错误触发临时封禁。
// 由 handler 层在同账号重试全部用尽、切换账号时调用。
func (s *GatewayService) TempUnscheduleRetryableError(ctx context.Context, accountID int64, failoverErr *UpstreamFailoverError) {
	if failoverErr == nil || !failoverErr.RetryableOnSameAccount {
		return
	}
	// 请求级瞬时故障与账号健康无关：封禁只会把与故障无关的账号一并摘掉，
	// 而故障因素（客户端身份、模型容量）在下一个账号上完全相同。
	if failoverErr.RequestScopedTransient {
		return
	}
	// 根据状态码选择封禁策略
	switch failoverErr.StatusCode {
	case http.StatusBadRequest:
		tempUnscheduleGoogleConfigError(ctx, s.accountRepo, accountID, "[handler]")
	case http.StatusBadGateway:
		tempUnscheduleEmptyResponse(ctx, s.accountRepo, accountID, "[handler]")
	}
}

// GatewayService handles API gateway operations
type GatewayService struct {
	accountRepo           AccountRepository
	groupRepo             GroupRepository
	usageLogRepo          UsageLogRepository
	usageBillingRepo      UsageBillingRepository
	userRepo              UserRepository
	userSubRepo           UserSubscriptionRepository
	userGroupRateRepo     UserGroupRateRepository
	cache                 GatewayCache
	digestStore           *DigestSessionStore
	cfg                   *config.Config
	schedulerSnapshot     *SchedulerSnapshotService
	billingService        *BillingService
	rateLimitService      *RateLimitService
	billingCacheService   *BillingCacheService
	identityService       *IdentityService
	httpUpstream          HTTPUpstream
	deferredService       *DeferredService
	concurrencyService    *ConcurrencyService
	claudeTokenProvider   *ClaudeTokenProvider
	sessionLimitCache     SessionLimitCache // 会话数量限制缓存（仅 Anthropic OAuth/SetupToken）
	rpmCache              RPMCache          // RPM 计数缓存（仅 Anthropic OAuth/SetupToken）
	userGroupRateResolver *userGroupRateResolver
	userGroupRateCache    *gocache.Cache
	userGroupRateSF       singleflight.Group
	modelsListCache       *gocache.Cache
	modelsListCacheTTL    time.Duration
	settingService        *SettingService
	responseHeaderFilter  *responseheaders.CompiledHeaderFilter
	debugModelRouting     atomic.Bool
	debugClaudeMimic      atomic.Bool
	channelService        *ChannelService
	resolver              *ModelPricingResolver
	compositeResolver     *CompositeRouteResolver
	debugGatewayBodyFile  atomic.Pointer[os.File] // non-nil when SUB2API_DEBUG_GATEWAY_BODY is set
	tlsFPProfileService   *TLSFingerprintProfileService
	balanceNotifyService  *BalanceNotifyService
	userPlatformQuotaRepo UserPlatformQuotaRepository
}

// NewGatewayService creates a new GatewayService
func NewGatewayService(
	accountRepo AccountRepository,
	groupRepo GroupRepository,
	usageLogRepo UsageLogRepository,
	usageBillingRepo UsageBillingRepository,
	userRepo UserRepository,
	userSubRepo UserSubscriptionRepository,
	userGroupRateRepo UserGroupRateRepository,
	cache GatewayCache,
	cfg *config.Config,
	schedulerSnapshot *SchedulerSnapshotService,
	concurrencyService *ConcurrencyService,
	billingService *BillingService,
	rateLimitService *RateLimitService,
	billingCacheService *BillingCacheService,
	identityService *IdentityService,
	httpUpstream HTTPUpstream,
	deferredService *DeferredService,
	claudeTokenProvider *ClaudeTokenProvider,
	sessionLimitCache SessionLimitCache,
	rpmCache RPMCache,
	digestStore *DigestSessionStore,
	settingService *SettingService,
	tlsFPProfileService *TLSFingerprintProfileService,
	channelService *ChannelService,
	resolver *ModelPricingResolver,
	compositeResolver *CompositeRouteResolver,
	balanceNotifyService *BalanceNotifyService,
	userPlatformQuotaRepo UserPlatformQuotaRepository,
) *GatewayService {
	userGroupRateTTL := resolveUserGroupRateCacheTTL(cfg)
	modelsListTTL := resolveModelsListCacheTTL(cfg)

	svc := &GatewayService{
		accountRepo:           accountRepo,
		groupRepo:             groupRepo,
		usageLogRepo:          usageLogRepo,
		usageBillingRepo:      usageBillingRepo,
		userRepo:              userRepo,
		userSubRepo:           userSubRepo,
		userGroupRateRepo:     userGroupRateRepo,
		cache:                 cache,
		digestStore:           digestStore,
		cfg:                   cfg,
		schedulerSnapshot:     schedulerSnapshot,
		concurrencyService:    concurrencyService,
		billingService:        billingService,
		rateLimitService:      rateLimitService,
		billingCacheService:   billingCacheService,
		identityService:       identityService,
		httpUpstream:          httpUpstream,
		deferredService:       deferredService,
		claudeTokenProvider:   claudeTokenProvider,
		sessionLimitCache:     sessionLimitCache,
		rpmCache:              rpmCache,
		userGroupRateCache:    gocache.New(userGroupRateTTL, time.Minute),
		settingService:        settingService,
		modelsListCache:       gocache.New(modelsListTTL, time.Minute),
		modelsListCacheTTL:    modelsListTTL,
		responseHeaderFilter:  compileResponseHeaderFilter(cfg),
		tlsFPProfileService:   tlsFPProfileService,
		channelService:        channelService,
		resolver:              resolver,
		compositeResolver:     compositeResolver,
		balanceNotifyService:  balanceNotifyService,
		userPlatformQuotaRepo: userPlatformQuotaRepo,
	}
	if compositeResolver != nil {
		compositeResolver.SetModelOwnershipResolver(svc.resolveCompositeModelOwnership)
	}
	svc.userGroupRateResolver = newUserGroupRateResolver(
		userGroupRateRepo,
		svc.userGroupRateCache,
		userGroupRateTTL,
		&svc.userGroupRateSF,
		"service.gateway",
	)
	svc.debugModelRouting.Store(parseDebugEnvBool(os.Getenv("SUB2API_DEBUG_MODEL_ROUTING")))
	svc.debugClaudeMimic.Store(parseDebugEnvBool(os.Getenv("SUB2API_DEBUG_CLAUDE_MIMIC")))
	if path := strings.TrimSpace(os.Getenv(debugGatewayBodyEnv)); path != "" {
		svc.initDebugGatewayBodyFile(path)
	}
	return svc
}

// GenerateSessionHash 从预解析请求计算粘性会话 hash
func (s *GatewayService) GenerateSessionHash(parsed *ParsedRequest) string {
	if parsed == nil {
		return ""
	}

	// 1. 最高优先级：从 metadata.user_id 提取 session_xxx
	if parsed.MetadataUserID != "" {
		uid := ParseMetadataUserID(parsed.MetadataUserID)
		if uid != nil && uid.SessionID != "" {
			slog.Info("sticky.hash_source",
				"source", "metadata_user_id",
				"session_id", uid.SessionID,
				"device_id", uid.DeviceID,
				"is_new_format", uid.IsNewFormat,
			)
			return uid.SessionID
		}
		slog.Info("sticky.hash_metadata_parse_failed",
			"metadata_user_id", parsed.MetadataUserID,
			"parsed_nil", uid == nil,
		)
	}

	// 2. 提取带 cache_control: {type: "ephemeral"} 的内容
	cacheableContent := s.extractCacheableContent(parsed)
	if cacheableContent != "" {
		hash := s.hashContent(cacheableContent)
		slog.Info("sticky.hash_source",
			"source", "cacheable_content",
			"hash", hash,
		)
		return hash
	}

	// 3. 最后 fallback: 使用 session上下文 + system + 所有消息的完整摘要串
	var combined strings.Builder
	// 混入请求上下文区分因子，避免不同用户相同消息产生相同 hash
	if parsed.SessionContext != nil {
		_, _ = combined.WriteString(parsed.SessionContext.ClientIP)
		_, _ = combined.WriteString(":")
		_, _ = combined.WriteString(NormalizeSessionUserAgent(parsed.SessionContext.UserAgent))
		_, _ = combined.WriteString(":")
		_, _ = combined.WriteString(strconv.FormatInt(parsed.SessionContext.APIKeyID, 10))
		_, _ = combined.WriteString("|")
	}
	if systemText := extractTextFromSystemRaw(parsed.SystemRaw()); systemText != "" {
		_, _ = combined.WriteString(systemText)
	}
	contentStart := combined.Len()
	appendMessageTextsFromRaw(&combined, parsed.MessagesRaw())
	if combined.Len() == contentStart {
		appendResponsesSessionAnchorFromRaw(&combined, parsed.InputRaw())
	}
	if combined.Len() > 0 {
		hash := s.hashContent(combined.String())
		slog.Info("sticky.hash_source",
			"source", "message_content_fallback",
			"hash", hash,
			"content_len", combined.Len(),
		)
		return hash
	}

	return ""
}

// BindStickySession sets session -> account binding with standard TTL.
func (s *GatewayService) BindStickySession(ctx context.Context, groupID *int64, sessionHash string, accountID int64) error {
	if sessionHash == "" || accountID <= 0 || s.cache == nil {
		return nil
	}
	return s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, accountID, stickySessionTTL)
}

// bindGatewayStickySessionDuringSelection preserves the normal eager sticky
// behavior unless a profit gate is installed. Profit-controlled requests bind
// only after the terminal post-slot check, otherwise a rejected candidate could
// overwrite a healthy pre-existing sticky binding.
func (s *GatewayService) bindGatewayStickySessionDuringSelection(ctx context.Context, groupID *int64, sessionHash string, accountID int64) error {
	if gatewayProfitControlGateActive(ctx) {
		return nil
	}
	return s.BindStickySession(ctx, groupID, sessionHash, accountID)
}

// BindStickySessionAfterProfitAdmission records a terminally admitted
// account. Without a profit gate it preserves the pre-existing eager binding
// behavior at the handler bind points. With a gate it never replaces a
// different binding that already exists: a temporarily ineligible sticky
// account remains bound and automatically becomes eligible again if its
// account rate recovers.
func (s *GatewayService) BindStickySessionAfterProfitAdmission(ctx context.Context, groupID *int64, sessionHash string, accountID int64) error {
	if sessionHash == "" || accountID <= 0 || s.cache == nil {
		return nil
	}
	if !gatewayProfitControlGateActive(ctx) {
		return s.BindStickySession(ctx, groupID, sessionHash, accountID)
	}
	existingAccountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
	if err != nil && !errors.Is(err, ErrStickySessionNotFound) {
		// 读失败时无法判断既有绑定，保守跳过而不是冒着覆盖健康绑定的风险写入。
		slog.Warn("profit_control_sticky_binding_read_failed", "group_id", derefGroupID(groupID), "account_id", accountID, "error", err)
		return nil
	}
	if existingAccountID > 0 && existingAccountID != accountID {
		return nil
	}
	return s.BindStickySession(ctx, groupID, sessionHash, accountID)
}

// GetCachedSessionAccountID retrieves the account ID bound to a sticky session.
// Returns 0 if no binding exists or on error.
func (s *GatewayService) GetCachedSessionAccountID(ctx context.Context, groupID *int64, sessionHash string) (int64, error) {
	if sessionHash == "" || s.cache == nil {
		return 0, nil
	}
	accountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
	if err != nil {
		return 0, err
	}
	return accountID, nil
}

// FindGeminiSession 查找 Gemini 会话（基于内容摘要链的 Fallback 匹配）
// 返回最长匹配的会话信息（uuid, accountID）
func (s *GatewayService) FindGeminiSession(_ context.Context, groupID int64, prefixHash, digestChain string) (uuid string, accountID int64, matchedChain string, found bool) {
	if digestChain == "" || s.digestStore == nil {
		return "", 0, "", false
	}
	return s.digestStore.Find(groupID, prefixHash, digestChain)
}

// SaveGeminiSession 保存 Gemini 会话。oldDigestChain 为 Find 返回的 matchedChain，用于删旧 key。
func (s *GatewayService) SaveGeminiSession(_ context.Context, groupID int64, prefixHash, digestChain, uuid string, accountID int64, oldDigestChain string) error {
	if digestChain == "" || s.digestStore == nil {
		return nil
	}
	s.digestStore.Save(groupID, prefixHash, digestChain, uuid, accountID, oldDigestChain)
	return nil
}

// FindAnthropicSession 查找 Anthropic 会话（基于内容摘要链的 Fallback 匹配）
func (s *GatewayService) FindAnthropicSession(_ context.Context, groupID int64, prefixHash, digestChain string) (uuid string, accountID int64, matchedChain string, found bool) {
	if digestChain == "" || s.digestStore == nil {
		return "", 0, "", false
	}
	return s.digestStore.Find(groupID, prefixHash, digestChain)
}

// SaveAnthropicSession 保存 Anthropic 会话
func (s *GatewayService) SaveAnthropicSession(_ context.Context, groupID int64, prefixHash, digestChain, uuid string, accountID int64, oldDigestChain string) error {
	if digestChain == "" || s.digestStore == nil {
		return nil
	}
	s.digestStore.Save(groupID, prefixHash, digestChain, uuid, accountID, oldDigestChain)
	return nil
}

func (s *GatewayService) extractCacheableContent(parsed *ParsedRequest) string {
	if parsed == nil {
		return ""
	}

	systemText := extractCacheableTextFromSystemRaw(parsed.SystemRaw())
	if messageText := extractCacheableTextFromMessagesRaw(parsed.MessagesRaw()); messageText != "" {
		return messageText
	}
	return systemText
}

func parseRawJSONView(raw []byte) gjson.Result {
	if len(raw) == 0 {
		return gjson.Result{}
	}
	// 这里只做同步只读解析，避免 gjson.ParseBytes 为大 messages/contents 复制整段 raw。
	return gjson.Parse(*(*string)(unsafe.Pointer(&raw)))
}

func extractTextFromSystemRaw(raw []byte) string {
	system := parseRawJSONView(raw)
	switch system.Type {
	case gjson.String:
		return system.String()
	case gjson.JSON:
		if !system.IsArray() {
			return ""
		}
		var builder strings.Builder
		system.ForEach(func(_, part gjson.Result) bool {
			if text := part.Get("text").String(); text != "" {
				_, _ = builder.WriteString(text)
			}
			return true
		})
		return builder.String()
	}
	return ""
}

func extractTextFromContentRaw(content gjson.Result) string {
	switch content.Type {
	case gjson.String:
		return content.String()
	case gjson.JSON:
		if !content.IsArray() {
			return ""
		}
		var builder strings.Builder
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() == "text" {
				if text := part.Get("text").String(); text != "" {
					_, _ = builder.WriteString(text)
				}
			}
			return true
		})
		return builder.String()
	}
	return ""
}

func appendMessageTextsFromRaw(builder *strings.Builder, raw []byte) {
	if builder == nil || len(raw) == 0 {
		return
	}
	messages := parseRawJSONView(raw)
	if !messages.IsArray() {
		return
	}
	messages.ForEach(func(_, msg gjson.Result) bool {
		if content := msg.Get("content"); content.Exists() {
			_, _ = builder.WriteString(extractTextFromContentRaw(content))
			return true
		}
		parts := msg.Get("parts")
		if parts.IsArray() {
			parts.ForEach(func(_, part gjson.Result) bool {
				if text := part.Get("text").String(); text != "" {
					_, _ = builder.WriteString(text)
				}
				return true
			})
		}
		return true
	})
}

func appendResponsesSessionAnchorFromRaw(builder *strings.Builder, raw []byte) {
	if builder == nil || len(raw) == 0 {
		return
	}
	input := parseRawJSONView(raw)
	if input.Type == gjson.String {
		_, _ = builder.WriteString(input.String())
		return
	}
	if !input.IsArray() {
		return
	}

	input.ForEach(func(_, item gjson.Result) bool {
		if item.Type == gjson.String {
			_, _ = builder.WriteString(item.String())
			return false
		}

		switch item.Get("role").String() {
		case "system", "developer":
			appendResponsesContentText(builder, item.Get("content"))
		case "user":
			appendResponsesContentText(builder, item.Get("content"))
			return false
		default:
			if item.Get("type").String() == "input_text" {
				if text := item.Get("text").String(); text != "" {
					_, _ = builder.WriteString(text)
				}
				return false
			}
		}
		return true
	})
}

func appendResponsesContentText(builder *strings.Builder, content gjson.Result) {
	if builder == nil || !content.Exists() {
		return
	}
	if content.Type == gjson.String {
		_, _ = builder.WriteString(content.String())
		return
	}
	if !content.IsArray() {
		return
	}
	content.ForEach(func(_, part gjson.Result) bool {
		switch part.Get("type").String() {
		case "input_text", "text":
			if text := part.Get("text").String(); text != "" {
				_, _ = builder.WriteString(text)
			}
		}
		return true
	})
}

func extractCacheableTextFromSystemRaw(raw []byte) string {
	system := parseRawJSONView(raw)
	if !system.IsArray() {
		return ""
	}
	var builder strings.Builder
	system.ForEach(func(_, part gjson.Result) bool {
		if part.Get("cache_control.type").String() == "ephemeral" {
			if text := part.Get("text").String(); text != "" {
				_, _ = builder.WriteString(text)
			}
		}
		return true
	})
	return builder.String()
}

func extractCacheableTextFromMessagesRaw(raw []byte) string {
	messages := parseRawJSONView(raw)
	if !messages.IsArray() {
		return ""
	}
	var text string
	messages.ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		found := false
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Get("cache_control.type").String() == "ephemeral" {
				found = true
				return false
			}
			return true
		})
		if found {
			text = extractTextFromContentRaw(content)
			return false
		}
		return true
	})
	return text
}

func (s *GatewayService) hashContent(content string) string {
	h := xxhash.Sum64String(content)
	return strconv.FormatUint(h, 36)
}

type anthropicCacheControlPayload struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type anthropicSystemTextBlockPayload struct {
	Type         string                        `json:"type"`
	Text         string                        `json:"text"`
	CacheControl *anthropicCacheControlPayload `json:"cache_control,omitempty"`
}

type anthropicMetadataPayload struct {
	UserID string `json:"user_id"`
}

// replaceModelInBody 替换请求体中的model字段
// 优先使用定点修改，尽量保持客户端原始字段顺序。
func (s *GatewayService) replaceModelInBody(body []byte, newModel string) []byte {
	return ReplaceModelInBody(body, newModel)
}

type claudeOAuthNormalizeOptions struct {
	injectMetadata          bool
	metadataUserID          string
	stripSystemCacheControl bool
}

// sanitizeSystemText rewrites only the fixed OpenCode identity sentence (if present).
// We intentionally avoid broad keyword replacement in system prompts to prevent
// accidentally changing user-provided instructions.
func sanitizeSystemText(text string) string {
	if text == "" {
		return text
	}
	// Some clients include a fixed OpenCode identity sentence. Anthropic may treat
	// this as a non-Claude-Code fingerprint, so rewrite it to the canonical
	// Claude Code banner before generic "OpenCode"/"opencode" replacements.
	text = strings.ReplaceAll(
		text,
		"You are OpenCode, the best coding agent on the planet.",
		strings.TrimSpace(claudeCodeSystemPrompt),
	)
	return text
}

func marshalAnthropicSystemTextBlock(text string, includeCacheControl bool) ([]byte, error) {
	block := anthropicSystemTextBlockPayload{
		Type: "text",
		Text: text,
	}
	if includeCacheControl {
		block.CacheControl = &anthropicCacheControlPayload{
			Type: "ephemeral",
			TTL:  claude.DefaultCacheControlTTL,
		}
	}
	return json.Marshal(block)
}

func marshalAnthropicSystemTextBlockWithCacheControl(text string, cacheControl any) ([]byte, error) {
	block := map[string]any{
		"type": "text",
		"text": text,
	}
	if cacheControl != nil {
		block["cache_control"] = cacheControl
	}
	return json.Marshal(block)
}

func marshalAnthropicMetadata(userID string) ([]byte, error) {
	return json.Marshal(anthropicMetadataPayload{UserID: userID})
}

func buildJSONArrayRaw(items [][]byte) []byte {
	if len(items) == 0 {
		return []byte("[]")
	}

	total := 2
	for _, item := range items {
		total += len(item)
	}
	total += len(items) - 1

	buf := make([]byte, 0, total)
	buf = append(buf, '[')
	for i, item := range items {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, item...)
	}
	buf = append(buf, ']')
	return buf
}

func setJSONValueBytes(body []byte, path string, value any) ([]byte, bool) {
	next, err := sjson.SetBytes(body, path, value)
	if err != nil {
		return body, false
	}
	return next, true
}

func setJSONRawBytes(body []byte, path string, raw []byte) ([]byte, bool) {
	next, err := sjson.SetRawBytes(body, path, raw)
	if err != nil {
		return body, false
	}
	return next, true
}

func deleteJSONPathBytes(body []byte, path string) ([]byte, bool) {
	next, err := sjson.DeleteBytes(body, path)
	if err != nil {
		return body, false
	}
	return next, true
}

func normalizeClaudeOAuthSystemBody(body []byte, opts claudeOAuthNormalizeOptions) ([]byte, bool) {
	sys := gjson.GetBytes(body, "system")
	if !sys.Exists() {
		return body, false
	}

	out := body
	modified := false

	switch {
	case sys.Type == gjson.String:
		sanitized := sanitizeSystemText(sys.String())
		if sanitized != sys.String() {
			if next, ok := setJSONValueBytes(out, "system", sanitized); ok {
				out = next
				modified = true
			}
		}
	case sys.IsArray():
		index := 0
		sys.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "text" {
				textResult := item.Get("text")
				if textResult.Exists() && textResult.Type == gjson.String {
					text := textResult.String()
					sanitized := sanitizeSystemText(text)
					if sanitized != text {
						if next, ok := setJSONValueBytes(out, fmt.Sprintf("system.%d.text", index), sanitized); ok {
							out = next
							modified = true
						}
					}
				}
			}

			if opts.stripSystemCacheControl && item.Get("cache_control").Exists() {
				if next, ok := deleteJSONPathBytes(out, fmt.Sprintf("system.%d.cache_control", index)); ok {
					out = next
					modified = true
				}
			}

			index++
			return true
		})
	}

	return out, modified
}

func ensureClaudeOAuthMetadataUserID(body []byte, userID string) ([]byte, bool) {
	if strings.TrimSpace(userID) == "" {
		return body, false
	}

	metadata := gjson.GetBytes(body, "metadata")
	if !metadata.Exists() || metadata.Type == gjson.Null {
		raw, err := marshalAnthropicMetadata(userID)
		if err != nil {
			return body, false
		}
		return setJSONRawBytes(body, "metadata", raw)
	}

	trimmedRaw := strings.TrimSpace(metadata.Raw)
	if strings.HasPrefix(trimmedRaw, "{") {
		existing := metadata.Get("user_id")
		if existing.Exists() && existing.Type == gjson.String && existing.String() != "" {
			return body, false
		}
		return setJSONValueBytes(body, "metadata.user_id", userID)
	}

	raw, err := marshalAnthropicMetadata(userID)
	if err != nil {
		return body, false
	}
	return setJSONRawBytes(body, "metadata", raw)
}

func normalizeClaudeOAuthRequestBody(body []byte, modelID string, opts claudeOAuthNormalizeOptions) ([]byte, string) {
	if len(body) == 0 {
		return body, modelID
	}

	out := body
	modified := false

	if next, changed := normalizeClaudeOAuthSystemBody(out, opts); changed {
		out = next
		modified = true
	}

	rawModel := gjson.GetBytes(out, "model")
	if rawModel.Exists() && rawModel.Type == gjson.String {
		normalized := claude.NormalizeModelID(rawModel.String())
		if normalized != rawModel.String() {
			if next, ok := setJSONValueBytes(out, "model", normalized); ok {
				out = next
				modified = true
			}
			modelID = normalized
		}
	}

	// 确保 tools 字段存在（即使为空数组）
	if !gjson.GetBytes(out, "tools").Exists() {
		if next, ok := setJSONRawBytes(out, "tools", []byte("[]")); ok {
			out = next
			modified = true
		}
	}

	if opts.injectMetadata && opts.metadataUserID != "" {
		if next, changed := ensureClaudeOAuthMetadataUserID(out, opts.metadataUserID); changed {
			out = next
			modified = true
		}
	}

	// temperature：真实 Claude Code CLI 总是发送 temperature（默认 1，客户端可覆盖）。
	// 之前的实现直接 delete 会导致 payload 缺字段，与真实 CLI 字节级不一致。
	// 策略：客户端传了什么就透传；没传则补默认 1。
	if !gjson.GetBytes(out, "temperature").Exists() {
		if next, ok := setJSONValueBytes(out, "temperature", 1); ok {
			out = next
			modified = true
		}
	}

	// max_tokens：真实 CLI 的默认值是 128000。缺失时补齐以对齐指纹。
	if !gjson.GetBytes(out, "max_tokens").Exists() {
		if next, ok := setJSONValueBytes(out, "max_tokens", 128000); ok {
			out = next
			modified = true
		}
	}

	// context_management：thinking.type 为 enabled/adaptive 时，真实 CLI 会自动
	// 附带 {"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}。
	// 客户端显式传了就透传；否则按 CLI 行为补齐。
	//
	// 注：本函数不按 model 名决定是否保留 context_management。“最终 beta
	// header 不含 context-management-2025-06-27 时 strip 字段”的能力维度
	// 对称约束由 sanitizeAnthropicBodyForBetaTokens 在 buildUpstreamRequest /
	// buildCountTokensRequest 层统一执行，与 Bedrock 路径的
	// sanitizeBedrockFieldsForBetaTokens 对称。
	if !gjson.GetBytes(out, "context_management").Exists() {
		thinkingType := gjson.GetBytes(out, "thinking.type").String()
		if thinkingType == "enabled" || thinkingType == "adaptive" {
			const cmDefault = `{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`
			if next, ok := setJSONRawBytes(out, "context_management", []byte(cmDefault)); ok {
				out = next
				modified = true
			}
		}
	}

	// tool_choice：与 Parrot 对齐，不再无条件删除。
	// - 客户端传了 {"type":"tool","name":"X"} → 保留结构，name 由
	//   applyToolNameRewriteToBody 同步映射为假名
	// - 其他形态（auto/any/none）原样透传
	// 如果 body 里完全没有 tools（空数组），tool_choice 没意义时才删除
	if !gjson.GetBytes(out, "tools").IsArray() || len(gjson.GetBytes(out, "tools").Array()) == 0 {
		if gjson.GetBytes(out, "tool_choice").Exists() {
			if next, ok := deleteJSONPathBytes(out, "tool_choice"); ok {
				out = next
				modified = true
			}
		}
	}

	if !modified {
		return body, modelID
	}

	return out, modelID
}

func (s *GatewayService) buildOAuthMetadataUserID(parsed *ParsedRequest, account *Account, fp *Fingerprint) string {
	if parsed == nil || account == nil {
		return ""
	}
	if parsed.MetadataUserID != "" {
		return ""
	}

	userID := strings.TrimSpace(account.GetClaudeUserID())
	if userID == "" && fp != nil {
		userID = fp.ClientID
	}
	if userID == "" {
		// Fall back to a random, well-formed client id so we can still satisfy
		// Claude Code OAuth requirements when account metadata is incomplete.
		userID = generateClientID()
	}

	// session_id 用"会话级稳定种子"派生（账号 + 客户端区分因子 + 首条 user 文本）：
	// 随对话在尾部追加 messages 时保持不变，贴近真实 CC 进程级稳定的 session_id。
	// 不复用 GenerateSessionHash —— 后者是粘性路由键、按设计逐轮变化（见其测试）。
	var firstUserText string
	if parsed.Body != nil {
		firstUserText = extractFirstUserText(parsed.Body.Bytes())
	}
	seed := buildStableSessionSeed(account.ID, sessionContextDiscriminator(parsed.SessionContext), firstUserText)
	sessionID := generateSessionUUID(seed)

	// 根据指纹 UA 版本选择输出格式
	var uaVersion string
	if fp != nil {
		uaVersion = ExtractCLIVersion(fp.UserAgent)
	}
	accountUUID := strings.TrimSpace(account.GetExtraString("account_uuid"))
	return FormatMetadataUserID(userID, accountUUID, sessionID, uaVersion)
}

// applyClaudeCodeOAuthMimicryToBody 将"非 Claude Code 客户端 + Claude OAuth 账号"
// 路径上原本只在 /v1/messages 里做的完整伪装应用到任意 body 上。
//
// 这是 /v1/messages 主路径上 rewriteSystemForNonClaudeCode +
// normalizeClaudeOAuthRequestBody 流程的通用版，供 OpenAI 协议兼容层
// (ForwardAsChatCompletions / ForwardAsResponses) 复用。
//
// 未抽离之前，OpenAI 协议兼容层仅做 injectClaudeCodePrompt（前置追加），
// 而仓内 /v1/messages 路径自己的注释明确说过"仅前置追加无法通过 Anthropic
// 第三方检测"；那条注释就是本函数存在的根因。
//
// 参数：
//   - ctx / c：用于读取指纹和 gateway settings；c 可为 nil（如 count_tokens）。
//   - account：必须是 OAuth 账号，且调用方已判断不是 Claude Code 客户端。
//   - body：已经 marshal 成 Anthropic /v1/messages 格式的请求体。
//   - systemRaw：body 中原始 system 字段（用于判断是否需要 rewrite）。
//   - model：最终会发给上游的模型 ID（用于 haiku 旁路 + metadata 版本选择）。
//
// 返回：改写后的 body。即使中间任何一步失败，也会退化成原 body（不会 panic）。
func (s *GatewayService) applyClaudeCodeOAuthMimicryToBody(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	systemRaw any,
	model string,
) []byte {
	if account == nil || !account.IsOAuth() || len(body) == 0 {
		return body
	}

	systemPromptInjectionEnabled, systemPrompt, systemPromptBlocks := s.claudeOAuthSystemPromptInjectionSettings(ctx)
	systemRewritten := false
	if systemPromptInjectionEnabled && !strings.Contains(strings.ToLower(model), "haiku") {
		body = rewriteSystemForNonClaudeCodeWithPromptBlocks(body, normalizeSystemParam(systemRaw), systemPrompt, systemPromptBlocks)
		systemRewritten = true
	}

	normalizeOpts := claudeOAuthNormalizeOptions{stripSystemCacheControl: !systemRewritten}

	if s.identityService != nil && c != nil && c.Request != nil {
		if fp, err := s.identityService.GetOrCreateFingerprint(ctx, account.ID, c.Request.Header); err == nil && fp != nil {
			mimicMPT := false
			if s.settingService != nil {
				_, mimicMPT, _ = s.settingService.GetGatewayForwardingSettings(ctx)
			}
			if !mimicMPT {
				if uid := s.buildOAuthMetadataUserIDFromBody(ctx, account, fp, body); uid != "" {
					normalizeOpts.injectMetadata = true
					normalizeOpts.metadataUserID = uid
				}
			}
		}
	}

	body, _ = normalizeClaudeOAuthRequestBody(body, model, normalizeOpts)

	// Phase D+E+F: messages cache 策略 + 工具名混淆 + tools[-1] 断点
	// 对齐 Parrot transform_request 里剩余的字段级改写。顺序有语义约束：
	//   1) messages cache：仅在配置开启时清除客户端断点并注入代理断点
	//   2) tool rewrite：最后改 tools[*].name / tool_choice.name 并在 tools[-1]
	//      上打断点；mapping 存入 gin.Context 供响应侧 bytes.Replace 还原。
	body = s.rewriteMessageCacheControlIfEnabled(ctx, body)

	if rw := buildToolNameRewriteFromBody(body); rw != nil {
		body = applyToolNameRewriteToBody(body, rw)
		if c != nil {
			c.Set(toolNameRewriteKey, rw)
		}
	} else {
		body = applyToolsLastCacheBreakpoint(body)
	}

	return body
}

// buildOAuthMetadataUserIDFromBody 是 buildOAuthMetadataUserID 的变体，
// 适用于调用方手上没有 ParsedRequest 的场景（如 OpenAI 协议兼容层）。
//
// 与 buildOAuthMetadataUserID 的唯一区别：
//   - session hash 从 body 本体按同样规则重算，而不是读取 ParsedRequest 缓存值。
//   - 如果 body 里已经存在 metadata.user_id，则返回空（由 ensureClaudeOAuthMetadataUserID
//     自行决定是否覆盖）。
func (s *GatewayService) buildOAuthMetadataUserIDFromBody(
	ctx context.Context,
	account *Account,
	fp *Fingerprint,
	body []byte,
) string {
	_ = ctx
	if account == nil {
		return ""
	}
	if existing := gjson.GetBytes(body, "metadata.user_id").String(); existing != "" {
		return ""
	}

	userID := strings.TrimSpace(account.GetClaudeUserID())
	if userID == "" && fp != nil {
		userID = fp.ClientID
	}
	if userID == "" {
		userID = generateClientID()
	}

	// 与 buildOAuthMetadataUserID 一致：用会话级稳定种子，避免整 body 哈希导致
	// 每轮（甚至每个 token 变化）都重算出不同的 session_id。
	var clientDiscriminator string
	if fp != nil {
		clientDiscriminator = fp.ClientID
	}
	seed := buildStableSessionSeed(account.ID, clientDiscriminator, extractFirstUserText(body))
	sessionID := generateSessionUUID(seed)

	var uaVersion string
	if fp != nil {
		uaVersion = ExtractCLIVersion(fp.UserAgent)
	}
	accountUUID := strings.TrimSpace(account.GetExtraString("account_uuid"))
	return FormatMetadataUserID(userID, accountUUID, sessionID, uaVersion)
}

// buildStableSessionSeed 为伪装路径合成的 metadata.user_id session_id 生成"会话级稳定"种子。
//
// 真实 Claude Code 的 session_id 是进程级随机 UUID，在一段会话内跨请求保持不变。无状态代理
// 无法恢复该值，这里用"会话内不变的锚点"近似：账号 ID + 客户端区分因子 + 首条 user 消息文本。
// 对话在尾部追加 messages 时这三者都不变，因此 generateSessionUUID(seed) 跨轮稳定。
//
// 注意：粘性路由键 GenerateSessionHash 按设计逐轮变化（见其测试），本函数与之独立、互不影响。
// accountID 恒存在，故 seed 永不为空 —— 输出始终是确定性 UUID，而非随机值。
func buildStableSessionSeed(accountID int64, clientDiscriminator, firstUserText string) string {
	var b strings.Builder
	_, _ = b.WriteString(strconv.FormatInt(accountID, 10))
	_, _ = b.WriteString("::")
	_, _ = b.WriteString(clientDiscriminator)
	_, _ = b.WriteString("::")
	_, _ = b.WriteString(firstUserText)
	return b.String()
}

// sessionContextDiscriminator 把请求上下文（客户端 IP / 归一化 UA / API Key ID）拼成
// 一个跨客户端的区分因子，避免不同用户的相同首条消息派生出相同 session_id。
func sessionContextDiscriminator(sc *SessionContext) string {
	if sc == nil {
		return ""
	}
	return sc.ClientIP + ":" + NormalizeSessionUserAgent(sc.UserAgent) + ":" + strconv.FormatInt(sc.APIKeyID, 10)
}

// GenerateSessionUUID creates a deterministic UUID4 from a seed string.
func GenerateSessionUUID(seed string) string {
	return generateSessionUUID(seed)
}

func generateSessionUUID(seed string) string {
	if seed == "" {
		return uuid.NewString()
	}
	hash := sha256.Sum256([]byte(seed))
	bytes := hash[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

// SelectAccount 选择账号（粘性会话+优先级）
func (s *GatewayService) SelectAccount(ctx context.Context, groupID *int64, sessionHash string) (*Account, error) {
	return s.SelectAccountForModel(ctx, groupID, sessionHash, "")
}

// SelectAccountForModel 选择支持指定模型的账号（粘性会话+优先级+模型映射）
func (s *GatewayService) SelectAccountForModel(ctx context.Context, groupID *int64, sessionHash string, requestedModel string) (*Account, error) {
	return s.SelectAccountForModelWithExclusions(ctx, groupID, sessionHash, requestedModel, nil)
}

// SelectAccountForModelWithExclusions selects an account supporting the requested model while excluding specified accounts.
func (s *GatewayService) SelectAccountForModelWithExclusions(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*Account, error) {
	// 优先检查 context 中的强制平台（/antigravity 路由）
	var platform string
	forcePlatform, hasForcePlatform := ctx.Value(ctxkey.ForcePlatform).(string)
	if hasForcePlatform && forcePlatform != "" {
		platform = forcePlatform
	} else if groupID != nil {
		group, resolvedGroupID, err := s.resolveGatewayGroup(ctx, groupID)
		if err != nil {
			return nil, err
		}
		groupID = resolvedGroupID
		ctx = s.withGroupContext(ctx, group)
		platform = group.Platform
	} else {
		// 无分组时只使用原生 anthropic 平台
		platform = PlatformAnthropic
	}

	// Claude Code 限制可能已将 groupID 解析为 fallback group，
	// 渠道限制预检查必须使用解析后的分组。
	if s.checkChannelPricingRestriction(ctx, groupID, requestedModel) {
		slog.Warn("channel pricing restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		return nil, fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, requestedModel)
	}

	// anthropic/gemini 分组支持混合调度（包含启用了 mixed_scheduling 的 antigravity 账户）
	// 注意：强制平台模式不走混合调度
	if (platform == PlatformAnthropic || platform == PlatformGemini) && !hasForcePlatform {
		account, err := s.selectAccountWithMixedScheduling(ctx, groupID, sessionHash, requestedModel, excludedIDs, platform)
		if err != nil {
			return nil, err
		}
		return s.hydrateSelectedAccount(ctx, account)
	}

	// antigravity 分组、强制平台模式或无分组使用单平台选择
	// 注意：强制平台模式也必须遵守分组限制，不再回退到全平台查询
	account, err := s.selectAccountForModelWithPlatform(ctx, groupID, sessionHash, requestedModel, excludedIDs, platform)
	if err != nil {
		return nil, err
	}
	return s.hydrateSelectedAccount(ctx, account)
}

// SelectAccountWithLoadAwareness selects account with load-awareness and wait plan.
// metadataUserID: 用于客户端亲和调度，从中提取客户端 ID
// sub2apiUserID: 系统用户 ID，用于二维亲和调度
func (s *GatewayService) SelectAccountWithLoadAwareness(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, metadataUserID string, sub2apiUserID int64) (*AccountSelectionResult, error) {
	// 调试日志：记录调度入口参数
	excludedIDsList := make([]int64, 0, len(excludedIDs))
	for id := range excludedIDs {
		excludedIDsList = append(excludedIDsList, id)
	}
	slog.Debug("account_scheduling_starting",
		"group_id", derefGroupID(groupID),
		"model", requestedModel,
		"session", shortSessionHash(sessionHash),
		"excluded_ids", excludedIDsList)

	cfg := s.schedulingConfig()

	// 检查 Claude Code 客户端限制（可能会替换 groupID 为降级分组）
	group, groupID, err := s.checkClaudeCodeRestriction(ctx, groupID)
	if err != nil {
		return nil, err
	}
	ctx = s.withGroupContext(ctx, group)

	// Claude Code 限制可能已将 groupID 解析为 fallback group，
	// 渠道限制预检查必须使用解析后的分组。
	if s.checkChannelPricingRestriction(ctx, groupID, requestedModel) {
		slog.Warn("channel pricing restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		return nil, fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, requestedModel)
	}

	var stickyAccountID int64
	var stickySource string
	if prefetch := prefetchedStickyAccountIDFromContext(ctx, groupID); prefetch > 0 {
		stickyAccountID = prefetch
		stickySource = "prefetch"
	} else if sessionHash != "" && s.cache != nil {
		if accountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash); err == nil {
			stickyAccountID = accountID
			stickySource = "cache"
		}
	}

	// [DEBUG-STICKY] 调度器入口日志
	slog.Info("sticky.scheduler_entry",
		"group_id", derefGroupID(groupID),
		"session_hash", shortSessionHash(sessionHash),
		"sticky_account_id", stickyAccountID,
		"sticky_source", stickySource,
		"model", requestedModel,
		"load_batch", cfg.LoadBatchEnabled,
		"has_concurrency_svc", s.concurrencyService != nil,
		"excluded_count", len(excludedIDs),
	)

	if s.debugModelRoutingEnabled() && requestedModel != "" {
		groupPlatform := ""
		if group != nil {
			groupPlatform = group.Platform
		}
		logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] select entry: group_id=%v group_platform=%s model=%s session=%s sticky_account=%d load_batch=%v concurrency=%v",
			derefGroupID(groupID), groupPlatform, requestedModel, shortSessionHash(sessionHash), stickyAccountID, cfg.LoadBatchEnabled, s.concurrencyService != nil)
	}

	if s.concurrencyService == nil || !cfg.LoadBatchEnabled {
		// 复制排除列表，用于会话限制拒绝时的重试
		localExcluded := make(map[int64]struct{})
		for k, v := range excludedIDs {
			localExcluded[k] = v
		}

		for {
			account, err := s.SelectAccountForModelWithExclusions(ctx, groupID, sessionHash, requestedModel, localExcluded)
			if err != nil {
				return nil, err
			}

			result, err := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
			if err == nil && result.Acquired {
				// 获取槽位后检查会话限制（使用 sessionHash 作为会话标识符）
				if !s.checkAndRegisterSession(ctx, account, sessionHash) {
					result.ReleaseFunc()                   // 释放槽位
					localExcluded[account.ID] = struct{}{} // 排除此账号
					continue                               // 重新选择
				}
				return s.newSelectionResult(ctx, account, true, result.ReleaseFunc, nil)
			}

			// 对于等待计划的情况，也需要先检查会话限制
			if !s.checkAndRegisterSession(ctx, account, sessionHash) {
				localExcluded[account.ID] = struct{}{}
				continue
			}

			if stickyAccountID > 0 && stickyAccountID == account.ID && s.concurrencyService != nil {
				waitingCount, _ := s.concurrencyService.GetAccountWaitingCount(ctx, account.ID)
				if waitingCount < cfg.StickySessionMaxWaiting {
					return s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
						AccountID:      account.ID,
						MaxConcurrency: account.Concurrency,
						Timeout:        cfg.StickySessionWaitTimeout,
						MaxWaiting:     cfg.StickySessionMaxWaiting,
					})
				}
			}
			return s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
				AccountID:      account.ID,
				MaxConcurrency: account.Concurrency,
				Timeout:        cfg.FallbackWaitTimeout,
				MaxWaiting:     cfg.FallbackMaxWaiting,
			})
		}
	}

	platform, hasForcePlatform, err := s.resolvePlatform(ctx, groupID, group)
	if err != nil {
		return nil, err
	}
	preferOAuth := platform == PlatformGemini
	if s.debugModelRoutingEnabled() && platform == PlatformAnthropic && requestedModel != "" {
		logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] load-aware enabled: group_id=%v model=%s session=%s platform=%s", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), platform)
	}

	accounts, useMixed, err := s.listSchedulableAccounts(ctx, groupID, platform, hasForcePlatform)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, ErrNoAvailableAccounts
	}
	ctx = s.withWindowCostPrefetch(ctx, accounts)
	ctx = s.withRPMPrefetch(ctx, accounts)

	// 提前构建 accountByID（供 Layer 1 和 Layer 1.5 使用）
	accountByID := make(map[int64]*Account, len(accounts))
	for i := range accounts {
		accountByID[accounts[i].ID] = &accounts[i]
	}
	isExcluded := func(accountID int64) bool {
		if excludedIDs == nil {
			return false
		}
		_, excluded := excludedIDs[accountID]
		return excluded
	}

	// 获取模型路由配置（仅 anthropic 平台）
	var routingAccountIDs []int64
	if group != nil && requestedModel != "" && group.Platform == PlatformAnthropic {
		routingAccountIDs = group.GetRoutingAccountIDs(requestedModel)
		if s.debugModelRoutingEnabled() {
			logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] context group routing: group_id=%d model=%s enabled=%v rules=%d matched_ids=%v session=%s sticky_account=%d",
				group.ID, requestedModel, group.ModelRoutingEnabled, len(group.ModelRouting), routingAccountIDs, shortSessionHash(sessionHash), stickyAccountID)
			if len(routingAccountIDs) == 0 && group.ModelRoutingEnabled && len(group.ModelRouting) > 0 {
				keys := make([]string, 0, len(group.ModelRouting))
				for k := range group.ModelRouting {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				const maxKeys = 20
				if len(keys) > maxKeys {
					keys = keys[:maxKeys]
				}
				logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] context group routing miss: group_id=%d model=%s patterns(sample)=%v", group.ID, requestedModel, keys)
			}
		}
	}

	// ============ Layer 1: 模型路由优先选择（优先级高于粘性会话） ============
	if len(routingAccountIDs) > 0 && s.concurrencyService != nil {
		// 1. 过滤出路由列表中可调度的账号
		var routingCandidates []*Account
		var filteredExcluded, filteredMissing, filteredUnsched, filteredPlatform, filteredModelScope, filteredModelMapping, filteredWindowCost int
		var modelScopeSkippedIDs []int64 // 记录因模型限流被跳过的账号 ID
		for _, routingAccountID := range routingAccountIDs {
			if isExcluded(routingAccountID) {
				filteredExcluded++
				continue
			}
			account, ok := accountByID[routingAccountID]
			if !ok || !s.isAccountSchedulableForSelection(account) {
				if !ok {
					filteredMissing++
				} else {
					filteredUnsched++
				}
				continue
			}
			if !s.isAccountAllowedForPlatform(account, platform, useMixed) {
				filteredPlatform++
				continue
			}
			if requestedModel != "" && !s.isModelSupportedByAccountWithContext(ctx, account, requestedModel) {
				filteredModelMapping++
				continue
			}
			if !s.isAccountSchedulableForModelSelection(ctx, account, requestedModel) {
				filteredModelScope++
				modelScopeSkippedIDs = append(modelScopeSkippedIDs, account.ID)
				continue
			}
			// 配额检查
			if !s.isAccountSchedulableForQuota(account) {
				continue
			}
			// 窗口费用检查（非粘性会话路径）
			if !s.isAccountSchedulableForWindowCost(ctx, account, false) {
				filteredWindowCost++
				continue
			}
			// RPM 检查（非粘性会话路径）
			if !s.isAccountSchedulableForRPM(ctx, account, false) {
				continue
			}
			routingCandidates = append(routingCandidates, account)
		}

		if s.debugModelRoutingEnabled() {
			logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] routed candidates: group_id=%v model=%s routed=%d candidates=%d filtered(excluded=%d missing=%d unsched=%d platform=%d model_scope=%d model_mapping=%d window_cost=%d)",
				derefGroupID(groupID), requestedModel, len(routingAccountIDs), len(routingCandidates),
				filteredExcluded, filteredMissing, filteredUnsched, filteredPlatform, filteredModelScope, filteredModelMapping, filteredWindowCost)
			if len(modelScopeSkippedIDs) > 0 {
				logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] model_rate_limited accounts skipped: group_id=%v model=%s account_ids=%v",
					derefGroupID(groupID), requestedModel, modelScopeSkippedIDs)
			}
		}

		if len(routingCandidates) > 0 {
			// 1.5. 在路由账号范围内检查粘性会话
			if sessionHash != "" && stickyAccountID > 0 {
				slog.Debug("sticky.layer1_5_checking",
					"sticky_account_id", stickyAccountID,
					"in_routing_list", containsInt64(routingAccountIDs, stickyAccountID),
					"is_excluded", isExcluded(stickyAccountID),
					"in_account_map", func() bool { _, ok := accountByID[stickyAccountID]; return ok }(),
					"session", shortSessionHash(sessionHash),
				)
				if containsInt64(routingAccountIDs, stickyAccountID) && !isExcluded(stickyAccountID) {
					// 粘性账号在路由列表中，优先使用
					if stickyAccount, ok := accountByID[stickyAccountID]; ok {
						var stickyCacheMissReason string

						gatePass := s.isAccountSchedulableForSelection(stickyAccount) &&
							s.isAccountAllowedForPlatform(stickyAccount, platform, useMixed) &&
							(requestedModel == "" || s.isModelSupportedByAccountWithContext(ctx, stickyAccount, requestedModel)) &&
							s.isAccountSchedulableForModelSelection(ctx, stickyAccount, requestedModel) &&
							s.isAccountSchedulableForQuota(stickyAccount) &&
							s.isAccountSchedulableForWindowCost(ctx, stickyAccount, true)

						rpmPass := gatePass && s.isAccountSchedulableForRPM(ctx, stickyAccount, true)

						if rpmPass { // 粘性会话窗口费用+RPM 检查
							result, err := s.tryAcquireAccountSlot(ctx, stickyAccountID, stickyAccount.Concurrency)
							if err == nil && result.Acquired {
								// 会话数量限制检查
								if !s.checkAndRegisterSession(ctx, stickyAccount, sessionHash) {
									result.ReleaseFunc() // 释放槽位
									stickyCacheMissReason = "session_limit"
									// 继续到负载感知选择
								} else {
									slog.Debug("sticky.layer1_5_hit",
										"account_id", stickyAccountID,
										"session", shortSessionHash(sessionHash),
										"result", "slot_acquired",
									)
									if s.debugModelRoutingEnabled() {
										logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] routed sticky hit: group_id=%v model=%s session=%s account=%d", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), stickyAccountID)
									}
									return s.newSelectionResult(ctx, stickyAccount, true, result.ReleaseFunc, nil)
								}
							}

							if stickyCacheMissReason == "" {
								waitingCount, _ := s.concurrencyService.GetAccountWaitingCount(ctx, stickyAccountID)
								if waitingCount < cfg.StickySessionMaxWaiting {
									// 会话数量限制检查（等待计划也需要占用会话配额）
									if !s.checkAndRegisterSession(ctx, stickyAccount, sessionHash) {
										stickyCacheMissReason = "session_limit"
										// 会话限制已满，继续到负载感知选择
									} else {
										return &AccountSelectionResult{
											Account: stickyAccount,
											WaitPlan: &AccountWaitPlan{
												AccountID:      stickyAccountID,
												MaxConcurrency: stickyAccount.Concurrency,
												Timeout:        cfg.StickySessionWaitTimeout,
												MaxWaiting:     cfg.StickySessionMaxWaiting,
											},
										}, nil
									}
								} else {
									stickyCacheMissReason = "wait_queue_full"
								}
							}
							// 粘性账号槽位满且等待队列已满，继续使用负载感知选择
						} else if !gatePass {
							stickyCacheMissReason = "gate_check"
						} else {
							stickyCacheMissReason = "rpm_red"
						}

						// 记录粘性缓存未命中的结构化日志
						if stickyCacheMissReason != "" {
							baseRPM := stickyAccount.GetBaseRPM()
							var currentRPM int
							if count, ok := rpmFromPrefetchContext(ctx, stickyAccount.ID); ok {
								currentRPM = count
							}
							logger.LegacyPrintf("service.gateway", "[StickyCacheMiss] reason=%s account_id=%d session=%s current_rpm=%d base_rpm=%d",
								stickyCacheMissReason, stickyAccountID, shortSessionHash(sessionHash), currentRPM, baseRPM)
						}
					} else {
						_ = s.cache.DeleteSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
						logger.LegacyPrintf("service.gateway", "[StickyCacheMiss] reason=account_cleared account_id=%d session=%s current_rpm=0 base_rpm=0",
							stickyAccountID, shortSessionHash(sessionHash))
					}
				}
			}

			// 2. 批量获取负载信息
			routingLoads := make([]AccountWithConcurrency, 0, len(routingCandidates))
			for _, acc := range routingCandidates {
				routingLoads = append(routingLoads, AccountWithConcurrency{
					ID:             acc.ID,
					MaxConcurrency: acc.EffectiveLoadFactor(),
				})
			}
			routingLoadMap, _ := s.concurrencyService.GetAccountsLoadBatch(ctx, routingLoads)

			// 3. 按负载感知排序
			var routingAvailable []accountWithLoad
			for _, acc := range routingCandidates {
				loadInfo := routingLoadMap[acc.ID]
				if loadInfo == nil {
					loadInfo = &AccountLoadInfo{AccountID: acc.ID}
				}
				if loadInfo.LoadRate < 100 {
					routingAvailable = append(routingAvailable, accountWithLoad{account: acc, loadInfo: loadInfo})
				}
			}

			if len(routingAvailable) > 0 {
				// 排序：优先级 > 负载率 > 最后使用时间
				sort.SliceStable(routingAvailable, func(i, j int) bool {
					a, b := routingAvailable[i], routingAvailable[j]
					if a.account.Priority != b.account.Priority {
						return a.account.Priority < b.account.Priority
					}
					if a.loadInfo.LoadRate != b.loadInfo.LoadRate {
						return a.loadInfo.LoadRate < b.loadInfo.LoadRate
					}
					switch {
					case a.account.LastUsedAt == nil && b.account.LastUsedAt != nil:
						return true
					case a.account.LastUsedAt != nil && b.account.LastUsedAt == nil:
						return false
					case a.account.LastUsedAt == nil && b.account.LastUsedAt == nil:
						return false
					default:
						return a.account.LastUsedAt.Before(*b.account.LastUsedAt)
					}
				})
				shuffleWithinSortGroups(routingAvailable)

				// 4. 尝试获取槽位
				for _, item := range routingAvailable {
					result, err := s.tryAcquireAccountSlot(ctx, item.account.ID, item.account.Concurrency)
					if err == nil && result.Acquired {
						// 会话数量限制检查
						if !s.checkAndRegisterSession(ctx, item.account, sessionHash) {
							result.ReleaseFunc() // 释放槽位，继续尝试下一个账号
							continue
						}
						if sessionHash != "" && s.cache != nil {
							_ = s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, item.account.ID, stickySessionTTL)
						}
						if s.debugModelRoutingEnabled() {
							logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] routed select: group_id=%v model=%s session=%s account=%d", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), item.account.ID)
						}
						return s.newSelectionResult(ctx, item.account, true, result.ReleaseFunc, nil)
					}
				}

				// 5. 所有路由账号槽位满，尝试返回等待计划（选择负载最低的）
				// 遍历找到第一个满足会话限制的账号
				for _, item := range routingAvailable {
					if !s.checkAndRegisterSession(ctx, item.account, sessionHash) {
						continue // 会话限制已满，尝试下一个
					}
					if s.debugModelRoutingEnabled() {
						logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] routed wait: group_id=%v model=%s session=%s account=%d", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), item.account.ID)
					}
					return s.newSelectionResult(ctx, item.account, false, nil, &AccountWaitPlan{
						AccountID:      item.account.ID,
						MaxConcurrency: item.account.Concurrency,
						Timeout:        cfg.StickySessionWaitTimeout,
						MaxWaiting:     cfg.StickySessionMaxWaiting,
					})
				}
				// 所有路由账号会话限制都已满，继续到 Layer 2 回退
			}
			// 路由列表中的账号都不可用（负载率 >= 100），继续到 Layer 2 回退
			logger.LegacyPrintf("service.gateway", "[ModelRouting] All routed accounts unavailable for model=%s, falling back to normal selection", requestedModel)
		}
	}

	// ============ Layer 1.5: 粘性会话（仅在无模型路由配置时生效） ============
	if len(routingAccountIDs) == 0 && sessionHash != "" && stickyAccountID > 0 && !isExcluded(stickyAccountID) {
		accountID := stickyAccountID
		if accountID > 0 && !isExcluded(accountID) {
			account, ok := accountByID[accountID]
			if ok {
				// 检查账户是否需要清理粘性会话绑定
				clearSticky := shouldClearStickySession(account, requestedModel)
				if clearSticky {
					slog.Debug("sticky.layer1_5_no_routing_clear",
						"account_id", accountID,
						"reason", "should_clear_sticky_session",
						"session", shortSessionHash(sessionHash),
					)
					_ = s.cache.DeleteSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
				}

				// 注意：不再检查 isAccountInGroup，因为 accountByID 已经从按分组过滤的
				// accounts 列表构建，账号一定在分组内。而 scheduler snapshot 缓存
				// 反序列化后 AccountGroups 字段为空，导致 isAccountInGroup 永远返回 false。
				platformOK := s.isAccountAllowedForPlatform(account, platform, useMixed)
				modelSupported := requestedModel == "" || s.isModelSupportedByAccountWithContext(ctx, account, requestedModel)
				modelSchedulable := s.isAccountSchedulableForModelSelection(ctx, account, requestedModel)
				quotaOK := s.isAccountSchedulableForQuota(account)
				windowCostOK := s.isAccountSchedulableForWindowCost(ctx, account, true)
				rpmOK := s.isAccountSchedulableForRPM(ctx, account, true)
				schedulable := s.isAccountSchedulableForSelection(account)

				slog.Debug("sticky.layer1_5_no_routing_checks",
					"account_id", accountID,
					"session", shortSessionHash(sessionHash),
					"clear_sticky", clearSticky,
					"schedulable", schedulable,
					"platform_ok", platformOK,
					"model_supported", modelSupported,
					"model_schedulable", modelSchedulable,
					"quota_ok", quotaOK,
					"window_cost_ok", windowCostOK,
					"rpm_ok", rpmOK,
				)

				if !clearSticky && platformOK && modelSupported && modelSchedulable && quotaOK && windowCostOK && rpmOK && schedulable {
					result, err := s.tryAcquireAccountSlot(ctx, accountID, account.Concurrency)
					if err == nil && result.Acquired {
						// 会话数量限制检查
						if !s.checkAndRegisterSession(ctx, account, sessionHash) {
							result.ReleaseFunc() // 释放槽位，继续到 Layer 2
							slog.Debug("sticky.layer1_5_no_routing_miss",
								"account_id", accountID,
								"reason", "session_limit",
								"session", shortSessionHash(sessionHash),
							)
						} else {
							slog.Debug("sticky.layer1_5_no_routing_hit",
								"account_id", accountID,
								"session", shortSessionHash(sessionHash),
								"result", "slot_acquired",
							)
							if s.cache != nil {
								_ = s.cache.RefreshSessionTTL(ctx, derefGroupID(groupID), sessionHash, stickySessionTTL)
							}
							return s.newSelectionResult(ctx, account, true, result.ReleaseFunc, nil)
						}
					} else {
						slog.Debug("sticky.layer1_5_no_routing_slot_busy",
							"account_id", accountID,
							"session", shortSessionHash(sessionHash),
						)
					}

					waitingCount, _ := s.concurrencyService.GetAccountWaitingCount(ctx, accountID)
					if waitingCount < cfg.StickySessionMaxWaiting {
						// 会话数量限制检查（等待计划也需要占用会话配额）
						if !s.checkAndRegisterSession(ctx, account, sessionHash) {
							// 会话限制已满，继续到 Layer 2
						} else {
							slog.Debug("sticky.layer1_5_no_routing_hit",
								"account_id", accountID,
								"session", shortSessionHash(sessionHash),
								"result", "wait_plan",
							)
							return s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
								AccountID:      accountID,
								MaxConcurrency: account.Concurrency,
								Timeout:        cfg.StickySessionWaitTimeout,
								MaxWaiting:     cfg.StickySessionMaxWaiting,
							})
						}
					}
				} else if !clearSticky {
					slog.Debug("sticky.layer1_5_no_routing_miss",
						"account_id", accountID,
						"reason", "gate_check_failed",
						"session", shortSessionHash(sessionHash),
					)
				}
			} else {
				slog.Debug("sticky.layer1_5_no_routing_miss",
					"account_id", accountID,
					"reason", "account_not_in_map",
					"session", shortSessionHash(sessionHash),
				)
			}
		}
	} else if len(routingAccountIDs) == 0 && sessionHash != "" {
		slog.Debug("sticky.layer1_5_no_routing_skip",
			"sticky_account_id", stickyAccountID,
			"is_excluded", func() bool { return stickyAccountID > 0 && isExcluded(stickyAccountID) }(),
			"session", shortSessionHash(sessionHash),
			"reason", func() string {
				if stickyAccountID == 0 {
					return "no_sticky_binding"
				}
				return "sticky_account_excluded"
			}(),
		)
	}

	// ============ Layer 2: 负载感知选择 ============
	slog.Debug("sticky.layer2_fallback",
		"session", shortSessionHash(sessionHash),
		"sticky_account_id", stickyAccountID,
		"reason", "sticky_not_used_falling_back_to_load_balance",
		"total_accounts", len(accounts),
	)
	candidates := make([]*Account, 0, len(accounts))
	for i := range accounts {
		acc := &accounts[i]
		if isExcluded(acc.ID) {
			continue
		}
		// Scheduler snapshots can be temporarily stale (bucket rebuild is throttled);
		// re-check schedulability here so recently rate-limited/overloaded accounts
		// are not selected again before the bucket is rebuilt.
		if !s.isAccountSchedulableForSelection(acc) {
			continue
		}
		if !s.isAccountAllowedForPlatform(acc, platform, useMixed) {
			continue
		}
		if requestedModel != "" && !s.isModelSupportedByAccountWithContext(ctx, acc, requestedModel) {
			continue
		}
		if !s.isAccountSchedulableForModelSelection(ctx, acc, requestedModel) {
			continue
		}
		// 配额检查
		if !s.isAccountSchedulableForQuota(acc) {
			continue
		}
		// 窗口费用检查（非粘性会话路径）
		if !s.isAccountSchedulableForWindowCost(ctx, acc, false) {
			continue
		}
		// RPM 检查（非粘性会话路径）
		if !s.isAccountSchedulableForRPM(ctx, acc, false) {
			continue
		}
		candidates = append(candidates, acc)
	}

	if len(candidates) == 0 {
		return nil, ErrNoAvailableAccounts
	}

	accountLoads := make([]AccountWithConcurrency, 0, len(candidates))
	for _, acc := range candidates {
		accountLoads = append(accountLoads, AccountWithConcurrency{
			ID:             acc.ID,
			MaxConcurrency: acc.EffectiveLoadFactor(),
		})
	}

	loadMap, err := s.concurrencyService.GetAccountsLoadBatch(ctx, accountLoads)
	if err != nil {
		if result, ok, legacyErr := s.tryAcquireByLegacyOrder(ctx, candidates, groupID, sessionHash, preferOAuth); legacyErr != nil {
			return nil, legacyErr
		} else if ok {
			return result, nil
		}
	} else {
		var available []accountWithLoad
		for _, acc := range candidates {
			loadInfo := loadMap[acc.ID]
			if loadInfo == nil {
				loadInfo = &AccountLoadInfo{AccountID: acc.ID}
			}
			if loadInfo.LoadRate < 100 {
				available = append(available, accountWithLoad{
					account:  acc,
					loadInfo: loadInfo,
				})
			}
		}

		// 分层过滤选择：优先级 →（可选）最早重置 → 负载率 → LRU
		for len(available) > 0 {
			// 1. 取优先级最小的集合
			candidates := filterByMinPriority(available)
			// 2. （可选）use-it-or-lose-it：优先选用会话窗口最早重置的账号
			if cfg.PreferSoonestReset {
				candidates = filterBySoonestReset(candidates)
			}
			// 3. 取负载率最低的集合
			candidates = filterByMinLoadRate(candidates)
			// 4. LRU 选择最久未用的账号
			selected := selectByLRU(candidates, preferOAuth)
			if selected == nil {
				break
			}

			result, err := s.tryAcquireAccountSlot(ctx, selected.account.ID, selected.account.Concurrency)
			if err == nil && result.Acquired {
				// 会话数量限制检查
				if !s.checkAndRegisterSession(ctx, selected.account, sessionHash) {
					result.ReleaseFunc() // 释放槽位，继续尝试下一个账号
				} else {
					if sessionHash != "" && s.cache != nil {
						_ = s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, selected.account.ID, stickySessionTTL)
					}
					return s.newSelectionResult(ctx, selected.account, true, result.ReleaseFunc, nil)
				}
			}

			// 移除已尝试的账号，重新进行分层过滤
			selectedID := selected.account.ID
			newAvailable := make([]accountWithLoad, 0, len(available)-1)
			for _, acc := range available {
				if acc.account.ID != selectedID {
					newAvailable = append(newAvailable, acc)
				}
			}
			available = newAvailable
		}
	}

	// ============ Layer 3: 兜底排队 ============
	s.sortCandidatesForFallback(candidates, preferOAuth, cfg.FallbackSelectionMode)
	for _, acc := range candidates {
		// 会话数量限制检查（等待计划也需要占用会话配额）
		if !s.checkAndRegisterSession(ctx, acc, sessionHash) {
			continue // 会话限制已满，尝试下一个账号
		}
		return s.newSelectionResult(ctx, acc, false, nil, &AccountWaitPlan{
			AccountID:      acc.ID,
			MaxConcurrency: acc.Concurrency,
			Timeout:        cfg.FallbackWaitTimeout,
			MaxWaiting:     cfg.FallbackMaxWaiting,
		})
	}
	return nil, ErrNoAvailableAccounts
}

func (s *GatewayService) tryAcquireByLegacyOrder(ctx context.Context, candidates []*Account, groupID *int64, sessionHash string, preferOAuth bool) (*AccountSelectionResult, bool, error) {
	ordered := append([]*Account(nil), candidates...)
	sortAccountsByPriorityAndLastUsed(ordered, preferOAuth)

	for _, acc := range ordered {
		result, err := s.tryAcquireAccountSlot(ctx, acc.ID, acc.Concurrency)
		if err == nil && result.Acquired {
			// 会话数量限制检查
			if !s.checkAndRegisterSession(ctx, acc, sessionHash) {
				result.ReleaseFunc() // 释放槽位，继续尝试下一个账号
				continue
			}
			if sessionHash != "" && s.cache != nil {
				_ = s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, acc.ID, stickySessionTTL)
			}
			selection, err := s.newSelectionResult(ctx, acc, true, result.ReleaseFunc, nil)
			if err != nil {
				return nil, false, err
			}
			return selection, true, nil
		}
	}

	return nil, false, nil
}

func (s *GatewayService) schedulingConfig() config.GatewaySchedulingConfig {
	if s.cfg != nil {
		return s.cfg.Gateway.Scheduling
	}
	return config.GatewaySchedulingConfig{
		StickySessionMaxWaiting:  3,
		StickySessionWaitTimeout: 45 * time.Second,
		FallbackWaitTimeout:      30 * time.Second,
		FallbackMaxWaiting:       100,
		LoadBatchEnabled:         true,
		SlotCleanupInterval:      30 * time.Second,
	}
}

func (s *GatewayService) withGroupContext(ctx context.Context, group *Group) context.Context {
	if !IsGroupContextValid(group) {
		return ctx
	}
	if existing, ok := ctx.Value(ctxkey.Group).(*Group); ok && existing != nil && existing.ID == group.ID && IsGroupContextValid(existing) {
		return ctx
	}
	return context.WithValue(ctx, ctxkey.Group, group)
}

func (s *GatewayService) groupFromContext(ctx context.Context, groupID int64) *Group {
	if group, ok := ctx.Value(ctxkey.Group).(*Group); ok && IsGroupContextValid(group) && group.ID == groupID {
		return group
	}
	return nil
}

func (s *GatewayService) resolveGroupByID(ctx context.Context, groupID int64) (*Group, error) {
	if group := s.groupFromContext(ctx, groupID); group != nil {
		return group, nil
	}
	group, err := s.groupRepo.GetByIDLite(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("get group failed: %w", err)
	}
	return group, nil
}

func (s *GatewayService) ResolveGroupByID(ctx context.Context, groupID int64) (*Group, error) {
	return s.resolveGroupByID(ctx, groupID)
}

func (s *GatewayService) routingAccountIDsForRequest(ctx context.Context, groupID *int64, requestedModel string, platform string) []int64 {
	if groupID == nil || requestedModel == "" || platform != PlatformAnthropic {
		return nil
	}
	group, err := s.resolveGroupByID(ctx, *groupID)
	if err != nil || group == nil {
		if s.debugModelRoutingEnabled() {
			logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] resolve group failed: group_id=%v model=%s platform=%s err=%v", derefGroupID(groupID), requestedModel, platform, err)
		}
		return nil
	}
	// Preserve existing behavior: model routing only applies to anthropic groups.
	if group.Platform != PlatformAnthropic {
		if s.debugModelRoutingEnabled() {
			logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] skip: non-anthropic group platform: group_id=%d group_platform=%s model=%s", group.ID, group.Platform, requestedModel)
		}
		return nil
	}
	ids := group.GetRoutingAccountIDs(requestedModel)
	if s.debugModelRoutingEnabled() {
		logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] routing lookup: group_id=%d model=%s enabled=%v rules=%d matched_ids=%v",
			group.ID, requestedModel, group.ModelRoutingEnabled, len(group.ModelRouting), ids)
	}
	return ids
}

func (s *GatewayService) resolveGatewayGroup(ctx context.Context, groupID *int64) (*Group, *int64, error) {
	if groupID == nil {
		return nil, nil, nil
	}

	currentID := *groupID
	visited := map[int64]struct{}{}
	for {
		if _, seen := visited[currentID]; seen {
			return nil, nil, fmt.Errorf("fallback group cycle detected")
		}
		visited[currentID] = struct{}{}

		group, err := s.resolveGroupByID(ctx, currentID)
		if err != nil {
			return nil, nil, err
		}

		if !group.ClaudeCodeOnly || IsClaudeCodeClient(ctx) {
			return group, &currentID, nil
		}

		if group.FallbackGroupID == nil {
			return nil, nil, ErrClaudeCodeOnly
		}
		currentID = *group.FallbackGroupID
	}
}

// checkClaudeCodeRestriction 检查分组的 Claude Code 客户端限制
// 如果分组启用了 claude_code_only 且请求不是来自 Claude Code 客户端：
//   - 有降级分组：返回降级分组的 ID
//   - 无降级分组：返回 ErrClaudeCodeOnly 错误
func (s *GatewayService) checkClaudeCodeRestriction(ctx context.Context, groupID *int64) (*Group, *int64, error) {
	if groupID == nil {
		return nil, groupID, nil
	}

	// 强制平台模式不检查 Claude Code 限制
	if forcePlatform, hasForcePlatform := ctx.Value(ctxkey.ForcePlatform).(string); hasForcePlatform && forcePlatform != "" {
		return nil, groupID, nil
	}

	group, resolvedID, err := s.resolveGatewayGroup(ctx, groupID)
	if err != nil {
		return nil, nil, err
	}

	return group, resolvedID, nil
}

func (s *GatewayService) resolvePlatform(ctx context.Context, groupID *int64, group *Group) (string, bool, error) {
	forcePlatform, hasForcePlatform := ctx.Value(ctxkey.ForcePlatform).(string)
	if hasForcePlatform && forcePlatform != "" {
		return forcePlatform, true, nil
	}
	if group != nil {
		return group.Platform, false, nil
	}
	if groupID != nil {
		group, err := s.resolveGroupByID(ctx, *groupID)
		if err != nil {
			return "", false, err
		}
		return group.Platform, false, nil
	}
	return PlatformAnthropic, false, nil
}

func (s *GatewayService) listSchedulableAccounts(ctx context.Context, groupID *int64, platform string, hasForcePlatform bool) ([]Account, bool, error) {
	if s.schedulerSnapshot != nil {
		accounts, useMixed, err := s.schedulerSnapshot.ListSchedulableAccounts(ctx, groupID, platform, hasForcePlatform)
		if err == nil {
			slog.Debug("account_scheduling_list_snapshot",
				"group_id", derefGroupID(groupID),
				"platform", platform,
				"use_mixed", useMixed,
				"count", len(accounts))
			if slog.Default().Enabled(ctx, slog.LevelDebug) {
				for _, acc := range accounts {
					slog.Debug("account_scheduling_account_detail",
						"account_id", acc.ID,
						"name", acc.Name,
						"platform", acc.Platform,
						"type", acc.Type,
						"status", acc.Status,
						"tls_fingerprint", acc.IsTLSFingerprintEnabled())
				}
			}
		}
		return accounts, useMixed, err
	}
	useMixed := (platform == PlatformAnthropic || platform == PlatformGemini) && !hasForcePlatform
	if useMixed {
		platforms := []string{platform, PlatformAntigravity}
		var accounts []Account
		var err error
		if groupID != nil {
			accounts, err = s.accountRepo.ListSchedulableByGroupIDAndPlatforms(ctx, *groupID, platforms)
		} else if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
			accounts, err = s.accountRepo.ListSchedulableByPlatforms(ctx, platforms)
		} else {
			accounts, err = s.accountRepo.ListSchedulableUngroupedByPlatforms(ctx, platforms)
		}
		if err != nil {
			slog.Debug("account_scheduling_list_failed",
				"group_id", derefGroupID(groupID),
				"platform", platform,
				"error", err)
			return nil, useMixed, err
		}
		filtered := make([]Account, 0, len(accounts))
		for _, acc := range accounts {
			if acc.Platform == PlatformAntigravity && !acc.IsMixedSchedulingEnabled() {
				continue
			}
			filtered = append(filtered, acc)
		}
		slog.Debug("account_scheduling_list_mixed",
			"group_id", derefGroupID(groupID),
			"platform", platform,
			"raw_count", len(accounts),
			"filtered_count", len(filtered))
		if slog.Default().Enabled(ctx, slog.LevelDebug) {
			for _, acc := range filtered {
				slog.Debug("account_scheduling_account_detail",
					"account_id", acc.ID,
					"name", acc.Name,
					"platform", acc.Platform,
					"type", acc.Type,
					"status", acc.Status,
					"tls_fingerprint", acc.IsTLSFingerprintEnabled())
			}
		}
		return filtered, useMixed, nil
	}

	var accounts []Account
	var err error
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		accounts, err = s.accountRepo.ListSchedulableByPlatform(ctx, platform)
	} else if groupID != nil {
		accounts, err = s.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, *groupID, platform)
		// 分组内无账号则返回空列表，由上层处理错误，不再回退到全平台查询
	} else {
		accounts, err = s.accountRepo.ListSchedulableUngroupedByPlatform(ctx, platform)
	}
	if err != nil {
		slog.Debug("account_scheduling_list_failed",
			"group_id", derefGroupID(groupID),
			"platform", platform,
			"error", err)
		return nil, useMixed, err
	}
	slog.Debug("account_scheduling_list_single",
		"group_id", derefGroupID(groupID),
		"platform", platform,
		"count", len(accounts))
	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		for _, acc := range accounts {
			slog.Debug("account_scheduling_account_detail",
				"account_id", acc.ID,
				"name", acc.Name,
				"platform", acc.Platform,
				"type", acc.Type,
				"status", acc.Status,
				"tls_fingerprint", acc.IsTLSFingerprintEnabled())
		}
	}
	return accounts, useMixed, nil
}

// IsSingleAntigravityAccountGroup 检查指定分组是否只有一个 antigravity 平台的可调度账号。
// 用于 Handler 层在首次请求时提前设置 SingleAccountRetry context，
// 避免单账号分组收到 503 时错误地设置模型限流标记导致后续请求连续快速失败。
func (s *GatewayService) IsSingleAntigravityAccountGroup(ctx context.Context, groupID *int64) bool {
	accounts, _, err := s.listSchedulableAccounts(ctx, groupID, PlatformAntigravity, true)
	if err != nil {
		return false
	}
	return len(accounts) == 1
}

func (s *GatewayService) isAccountAllowedForPlatform(account *Account, platform string, useMixed bool) bool {
	if account == nil {
		return false
	}
	if useMixed {
		if account.Platform == platform {
			return true
		}
		return account.Platform == PlatformAntigravity && account.IsMixedSchedulingEnabled()
	}
	return account.Platform == platform
}

func (s *GatewayService) isAccountSchedulableForSelection(account *Account) bool {
	if account == nil {
		return false
	}
	return account.IsSchedulable()
}

func (s *GatewayService) isAccountSchedulableForModelSelection(ctx context.Context, account *Account, requestedModel string) bool {
	if account == nil {
		return false
	}
	return account.IsSchedulableForModelWithContext(ctx, requestedModel)
}

// isAccountInGroup checks if the account belongs to the specified group.
// When groupID is nil, returns true only for ungrouped accounts (no group assignments).
func (s *GatewayService) isAccountInGroup(account *Account, groupID *int64) bool {
	if account == nil {
		return false
	}
	if groupID == nil {
		// 无分组的 API Key 只能使用未分组的账号
		return len(account.AccountGroups) == 0
	}
	for _, ag := range account.AccountGroups {
		if ag.GroupID == *groupID {
			return true
		}
	}
	return false
}

func (s *GatewayService) tryAcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int) (*AcquireResult, error) {
	if s.concurrencyService == nil {
		return &AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
	}
	return s.concurrencyService.AcquireAccountSlot(ctx, accountID, maxConcurrency)
}

type usageLogWindowStatsBatchProvider interface {
	GetAccountWindowStatsBatch(ctx context.Context, accountIDs []int64, startTime time.Time) (map[int64]*usagestats.AccountStats, error)
}

type windowCostPrefetchContextKeyType struct{}

var windowCostPrefetchContextKey = windowCostPrefetchContextKeyType{}

func windowCostFromPrefetchContext(ctx context.Context, accountID int64) (float64, bool) {
	if ctx == nil || accountID <= 0 {
		return 0, false
	}
	m, ok := ctx.Value(windowCostPrefetchContextKey).(map[int64]float64)
	if !ok || len(m) == 0 {
		return 0, false
	}
	v, exists := m[accountID]
	return v, exists
}

func (s *GatewayService) withWindowCostPrefetch(ctx context.Context, accounts []Account) context.Context {
	if ctx == nil || len(accounts) == 0 || s.sessionLimitCache == nil || s.usageLogRepo == nil {
		return ctx
	}

	accountByID := make(map[int64]*Account)
	accountIDs := make([]int64, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if account == nil || !account.IsAnthropicOAuthOrSetupToken() {
			continue
		}
		if account.GetWindowCostLimit() <= 0 {
			continue
		}
		accountByID[account.ID] = account
		accountIDs = append(accountIDs, account.ID)
	}
	if len(accountIDs) == 0 {
		return ctx
	}

	costs := make(map[int64]float64, len(accountIDs))
	cacheValues, err := s.sessionLimitCache.GetWindowCostBatch(ctx, accountIDs)
	if err == nil {
		for accountID, cost := range cacheValues {
			costs[accountID] = cost
		}
		windowCostPrefetchCacheHitTotal.Add(int64(len(cacheValues)))
	} else {
		windowCostPrefetchErrorTotal.Add(1)
		logger.LegacyPrintf("service.gateway", "window_cost batch cache read failed: %v", err)
	}
	cacheMissCount := len(accountIDs) - len(costs)
	if cacheMissCount < 0 {
		cacheMissCount = 0
	}
	windowCostPrefetchCacheMissTotal.Add(int64(cacheMissCount))

	missingByStart := make(map[int64][]int64)
	startTimes := make(map[int64]time.Time)
	for _, accountID := range accountIDs {
		if _, ok := costs[accountID]; ok {
			continue
		}
		account := accountByID[accountID]
		if account == nil {
			continue
		}
		startTime := account.GetCurrentWindowStartTime()
		startKey := startTime.Unix()
		missingByStart[startKey] = append(missingByStart[startKey], accountID)
		startTimes[startKey] = startTime
	}
	if len(missingByStart) == 0 {
		return context.WithValue(ctx, windowCostPrefetchContextKey, costs)
	}

	batchReader, hasBatch := s.usageLogRepo.(usageLogWindowStatsBatchProvider)
	for startKey, ids := range missingByStart {
		startTime := startTimes[startKey]

		if hasBatch {
			windowCostPrefetchBatchSQLTotal.Add(1)
			queryStart := time.Now()
			statsByAccount, err := batchReader.GetAccountWindowStatsBatch(ctx, ids, startTime)
			if err == nil {
				slog.Debug("window_cost_batch_query_ok",
					"accounts", len(ids),
					"window_start", startTime.Format(time.RFC3339),
					"duration_ms", time.Since(queryStart).Milliseconds())
				for _, accountID := range ids {
					stats := statsByAccount[accountID]
					cost := 0.0
					if stats != nil {
						cost = stats.StandardCost
					}
					costs[accountID] = cost
					_ = s.sessionLimitCache.SetWindowCost(ctx, accountID, cost)
				}
				continue
			}
			windowCostPrefetchErrorTotal.Add(1)
			logger.LegacyPrintf("service.gateway", "window_cost batch db query failed: start=%s err=%v", startTime.Format(time.RFC3339), err)
		}

		// 回退路径：缺少批量仓储能力或批量查询失败时，按账号单查（失败开放）。
		windowCostPrefetchFallbackTotal.Add(int64(len(ids)))
		for _, accountID := range ids {
			stats, err := s.usageLogRepo.GetAccountWindowStats(ctx, accountID, startTime)
			if err != nil {
				windowCostPrefetchErrorTotal.Add(1)
				continue
			}
			cost := stats.StandardCost
			costs[accountID] = cost
			_ = s.sessionLimitCache.SetWindowCost(ctx, accountID, cost)
		}
	}

	return context.WithValue(ctx, windowCostPrefetchContextKey, costs)
}

// isAccountSchedulableForQuota 检查账号是否在配额限制内
// 适用于配置了 quota_limit 的 apikey 和 bedrock 类型账号
func (s *GatewayService) isAccountSchedulableForQuota(account *Account) bool {
	if !account.IsAPIKeyOrBedrock() {
		return true
	}
	return !account.IsQuotaExceeded()
}

// isAccountSchedulableForWindowCost 检查账号是否可根据窗口费用进行调度
// 仅适用于 Anthropic OAuth/SetupToken 账号
// 返回 true 表示可调度，false 表示不可调度
func (s *GatewayService) isAccountSchedulableForWindowCost(ctx context.Context, account *Account, isSticky bool) bool {
	// 只检查 Anthropic OAuth/SetupToken 账号
	if !account.IsAnthropicOAuthOrSetupToken() {
		return true
	}

	limit := account.GetWindowCostLimit()
	if limit <= 0 {
		return true // 未启用窗口费用限制
	}

	// 尝试从缓存获取窗口费用
	var currentCost float64
	if cost, ok := windowCostFromPrefetchContext(ctx, account.ID); ok {
		currentCost = cost
		goto checkSchedulability
	}
	if s.sessionLimitCache != nil {
		if cost, hit, err := s.sessionLimitCache.GetWindowCost(ctx, account.ID); err == nil && hit {
			currentCost = cost
			goto checkSchedulability
		}
	}

	// 缓存未命中，从数据库查询
	{
		// 使用统一的窗口开始时间计算逻辑（考虑窗口过期情况）
		startTime := account.GetCurrentWindowStartTime()

		stats, err := s.usageLogRepo.GetAccountWindowStats(ctx, account.ID, startTime)
		if err != nil {
			// 失败开放：查询失败时允许调度
			return true
		}

		// 使用标准费用（不含账号倍率）
		currentCost = stats.StandardCost

		// 设置缓存（忽略错误）
		if s.sessionLimitCache != nil {
			_ = s.sessionLimitCache.SetWindowCost(ctx, account.ID, currentCost)
		}
	}

checkSchedulability:
	schedulability := account.CheckWindowCostSchedulability(currentCost)

	switch schedulability {
	case WindowCostSchedulable:
		return true
	case WindowCostStickyOnly:
		return isSticky
	case WindowCostNotSchedulable:
		return false
	}
	return true
}

// rpmPrefetchContextKey is the context key for prefetched RPM counts.
type rpmPrefetchContextKeyType struct{}

var rpmPrefetchContextKey = rpmPrefetchContextKeyType{}

func rpmFromPrefetchContext(ctx context.Context, accountID int64) (int, bool) {
	if v, ok := ctx.Value(rpmPrefetchContextKey).(map[int64]int); ok {
		count, found := v[accountID]
		return count, found
	}
	return 0, false
}

// withRPMPrefetch 批量预取所有候选账号的 RPM 计数
func (s *GatewayService) withRPMPrefetch(ctx context.Context, accounts []Account) context.Context {
	if s.rpmCache == nil {
		return ctx
	}

	var ids []int64
	for i := range accounts {
		if accounts[i].IsAnthropicOAuthOrSetupToken() && accounts[i].GetBaseRPM() > 0 {
			ids = append(ids, accounts[i].ID)
		}
	}
	if len(ids) == 0 {
		return ctx
	}

	counts, err := s.rpmCache.GetRPMBatch(ctx, ids)
	if err != nil {
		return ctx // 失败开放
	}
	return context.WithValue(ctx, rpmPrefetchContextKey, counts)
}

// isAccountSchedulableForRPM 检查账号是否可根据 RPM 进行调度
// 仅适用于 Anthropic OAuth/SetupToken 账号
func (s *GatewayService) isAccountSchedulableForRPM(ctx context.Context, account *Account, isSticky bool) bool {
	if !account.IsAnthropicOAuthOrSetupToken() {
		return true
	}
	baseRPM := account.GetBaseRPM()
	if baseRPM <= 0 {
		return true
	}

	// 尝试从预取缓存获取
	var currentRPM int
	if count, ok := rpmFromPrefetchContext(ctx, account.ID); ok {
		currentRPM = count
	} else if s.rpmCache != nil {
		if count, err := s.rpmCache.GetRPM(ctx, account.ID); err == nil {
			currentRPM = count
		}
		// 失败开放：GetRPM 错误时允许调度
	}

	schedulability := account.CheckRPMSchedulability(currentRPM)
	switch schedulability {
	case WindowCostSchedulable:
		return true
	case WindowCostStickyOnly:
		return isSticky
	case WindowCostNotSchedulable:
		return false
	}
	return true
}

// IncrementAccountRPM increments the RPM counter for the given account.
// 已知 TOCTOU 竞态：调度时读取 RPM 计数与此处递增之间存在时间窗口，
// 高并发下可能短暂超出 RPM 限制。这是与 WindowCost 一致的 soft-limit
// 设计权衡——可接受的少量超额优于加锁带来的延迟和复杂度。
func (s *GatewayService) IncrementAccountRPM(ctx context.Context, accountID int64) error {
	if s.rpmCache == nil {
		return nil
	}
	_, err := s.rpmCache.IncrementRPM(ctx, accountID)
	return err
}

// checkAndRegisterSession 检查并注册会话，用于会话数量限制
// 仅适用于 Anthropic OAuth/SetupToken 账号
// sessionID: 会话标识符（使用粘性会话的 hash）
// 返回 true 表示允许（在限制内或会话已存在），false 表示拒绝（超出限制且是新会话）
func (s *GatewayService) checkAndRegisterSession(ctx context.Context, account *Account, sessionID string) bool {
	// 只检查 Anthropic OAuth/SetupToken 账号
	if !account.IsAnthropicOAuthOrSetupToken() {
		return true
	}

	maxSessions := account.GetMaxSessions()
	if maxSessions <= 0 || sessionID == "" {
		return true // 未启用会话限制或无会话ID
	}

	if s.sessionLimitCache == nil {
		return true // 缓存不可用时允许通过
	}

	idleTimeout := time.Duration(account.GetSessionIdleTimeoutMinutes()) * time.Minute

	allowed, err := s.sessionLimitCache.RegisterSession(ctx, account.ID, sessionID, maxSessions, idleTimeout)
	if err != nil {
		// 失败开放：缓存错误时允许通过
		return true
	}
	return allowed
}

func (s *GatewayService) getSchedulableAccount(ctx context.Context, accountID int64) (*Account, error) {
	if s.schedulerSnapshot != nil {
		return s.schedulerSnapshot.GetAccount(ctx, accountID)
	}
	return s.accountRepo.GetByID(ctx, accountID)
}

func (s *GatewayService) hydrateSelectedAccount(ctx context.Context, account *Account) (*Account, error) {
	if account == nil || s.schedulerSnapshot == nil {
		return account, nil
	}
	hydrated, err := s.schedulerSnapshot.GetAccount(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	if hydrated == nil {
		return nil, fmt.Errorf("selected gateway account %d not found during hydration", account.ID)
	}
	return hydrated, nil
}

func (s *GatewayService) newSelectionResult(ctx context.Context, account *Account, acquired bool, release func(), waitPlan *AccountWaitPlan) (*AccountSelectionResult, error) {
	hydrated, err := s.hydrateSelectedAccount(ctx, account)
	if err != nil {
		return nil, err
	}
	return &AccountSelectionResult{
		Account:     hydrated,
		Acquired:    acquired,
		ReleaseFunc: release,
		WaitPlan:    waitPlan,
	}, nil
}

// filterByMinPriority 过滤出优先级最小的账号集合
func filterByMinPriority(accounts []accountWithLoad) []accountWithLoad {
	if len(accounts) == 0 {
		return accounts
	}
	minPriority := accounts[0].account.Priority
	for _, acc := range accounts[1:] {
		if acc.account.Priority < minPriority {
			minPriority = acc.account.Priority
		}
	}
	result := make([]accountWithLoad, 0, len(accounts))
	for _, acc := range accounts {
		if acc.account.Priority == minPriority {
			result = append(result, acc)
		}
	}
	return result
}

// filterByMinLoadRate 过滤出负载率最低的账号集合
func filterByMinLoadRate(accounts []accountWithLoad) []accountWithLoad {
	if len(accounts) == 0 {
		return accounts
	}
	minLoadRate := accounts[0].loadInfo.LoadRate
	for _, acc := range accounts[1:] {
		if acc.loadInfo.LoadRate < minLoadRate {
			minLoadRate = acc.loadInfo.LoadRate
		}
	}
	result := make([]accountWithLoad, 0, len(accounts))
	for _, acc := range accounts {
		if acc.loadInfo.LoadRate == minLoadRate {
			result = append(result, acc)
		}
	}
	return result
}

// filterBySoonestReset 过滤出「会话窗口最早重置」的账号集合（use-it-or-lose-it）。
// 仅保留拥有未来重置时间（SessionWindowEnd 在当前时间之后）且最早的账号；
// 窗口为空或已过期的账号视为无活跃窗口、优先级最低。
// 当所有账号都没有活跃窗口时，返回原集合（不改变后续 LRU 选择）。
func filterBySoonestReset(accounts []accountWithLoad) []accountWithLoad {
	if len(accounts) <= 1 {
		return accounts
	}
	now := time.Now()
	var minEnd *time.Time
	for _, acc := range accounts {
		end := acc.account.SessionWindowEnd
		if end == nil || !now.Before(*end) {
			continue
		}
		if minEnd == nil || end.Before(*minEnd) {
			minEnd = end
		}
	}
	if minEnd == nil {
		// 没有任何账号拥有活跃窗口，保持原集合
		return accounts
	}
	result := make([]accountWithLoad, 0, len(accounts))
	for _, acc := range accounts {
		end := acc.account.SessionWindowEnd
		if end != nil && now.Before(*end) && end.Equal(*minEnd) {
			result = append(result, acc)
		}
	}
	return result
}

// selectByLRU 从集合中选择最久未用的账号
// 如果有多个账号具有相同的最小 LastUsedAt，则随机选择一个
func selectByLRU(accounts []accountWithLoad, preferOAuth bool) *accountWithLoad {
	if len(accounts) == 0 {
		return nil
	}
	if len(accounts) == 1 {
		return &accounts[0]
	}

	// 1. 找到最小的 LastUsedAt（nil 被视为最小）
	var minTime *time.Time
	hasNil := false
	for _, acc := range accounts {
		if acc.account.LastUsedAt == nil {
			hasNil = true
			break
		}
		if minTime == nil || acc.account.LastUsedAt.Before(*minTime) {
			minTime = acc.account.LastUsedAt
		}
	}

	// 2. 收集所有具有最小 LastUsedAt 的账号索引
	var candidateIdxs []int
	for i, acc := range accounts {
		if hasNil {
			if acc.account.LastUsedAt == nil {
				candidateIdxs = append(candidateIdxs, i)
			}
		} else {
			if acc.account.LastUsedAt != nil && acc.account.LastUsedAt.Equal(*minTime) {
				candidateIdxs = append(candidateIdxs, i)
			}
		}
	}

	// 3. 如果只有一个候选，直接返回
	if len(candidateIdxs) == 1 {
		return &accounts[candidateIdxs[0]]
	}

	// 4. 如果有多个候选且 preferOAuth，优先选择 OAuth 类型
	if preferOAuth {
		var oauthIdxs []int
		for _, idx := range candidateIdxs {
			if accounts[idx].account.Type == AccountTypeOAuth {
				oauthIdxs = append(oauthIdxs, idx)
			}
		}
		if len(oauthIdxs) > 0 {
			candidateIdxs = oauthIdxs
		}
	}

	// 5. 随机选择一个
	selectedIdx := candidateIdxs[mathrand.Intn(len(candidateIdxs))]
	return &accounts[selectedIdx]
}

func sortAccountsByPriorityAndLastUsed(accounts []*Account, preferOAuth bool) {
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i], accounts[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		switch {
		case a.LastUsedAt == nil && b.LastUsedAt != nil:
			return true
		case a.LastUsedAt != nil && b.LastUsedAt == nil:
			return false
		case a.LastUsedAt == nil && b.LastUsedAt == nil:
			if preferOAuth && a.Type != b.Type {
				return a.Type == AccountTypeOAuth
			}
			return false
		default:
			return a.LastUsedAt.Before(*b.LastUsedAt)
		}
	})
	shuffleWithinPriorityAndLastUsed(accounts, preferOAuth)
}

// shuffleWithinSortGroups 对排序后的 accountWithLoad 切片，按 (Priority, LoadRate, LastUsedAt) 分组后组内随机打乱。
// 防止并发请求读取同一快照时，确定性排序导致所有请求命中相同账号。
func shuffleWithinSortGroups(accounts []accountWithLoad) {
	if len(accounts) <= 1 {
		return
	}
	i := 0
	for i < len(accounts) {
		j := i + 1
		for j < len(accounts) && sameAccountWithLoadGroup(accounts[i], accounts[j]) {
			j++
		}
		if j-i > 1 {
			mathrand.Shuffle(j-i, func(a, b int) {
				accounts[i+a], accounts[i+b] = accounts[i+b], accounts[i+a]
			})
		}
		i = j
	}
}

// sameAccountWithLoadGroup 判断两个 accountWithLoad 是否属于同一排序组
func sameAccountWithLoadGroup(a, b accountWithLoad) bool {
	if a.account.Priority != b.account.Priority {
		return false
	}
	if a.loadInfo.LoadRate != b.loadInfo.LoadRate {
		return false
	}
	return sameLastUsedAt(a.account.LastUsedAt, b.account.LastUsedAt)
}

// shuffleWithinPriorityAndLastUsed 对排序后的 []*Account 切片，按 (Priority, LastUsedAt) 分组后组内随机打乱。
//
// 注意：当 preferOAuth=true 时，需要保证 OAuth 账号在同组内仍然优先，否则会把排序时的偏好打散掉。
// 因此这里采用"组内分区 + 分区内 shuffle"的方式：
// - 先把同组账号按 (OAuth / 非 OAuth) 拆成两段，保持 OAuth 段在前；
// - 再分别在各段内随机打散，避免热点。
func shuffleWithinPriorityAndLastUsed(accounts []*Account, preferOAuth bool) {
	if len(accounts) <= 1 {
		return
	}
	i := 0
	for i < len(accounts) {
		j := i + 1
		for j < len(accounts) && sameAccountGroup(accounts[i], accounts[j]) {
			j++
		}
		if j-i > 1 {
			if preferOAuth {
				oauth := make([]*Account, 0, j-i)
				others := make([]*Account, 0, j-i)
				for _, acc := range accounts[i:j] {
					if acc.Type == AccountTypeOAuth {
						oauth = append(oauth, acc)
					} else {
						others = append(others, acc)
					}
				}
				if len(oauth) > 1 {
					mathrand.Shuffle(len(oauth), func(a, b int) { oauth[a], oauth[b] = oauth[b], oauth[a] })
				}
				if len(others) > 1 {
					mathrand.Shuffle(len(others), func(a, b int) { others[a], others[b] = others[b], others[a] })
				}
				copy(accounts[i:], oauth)
				copy(accounts[i+len(oauth):], others)
			} else {
				mathrand.Shuffle(j-i, func(a, b int) {
					accounts[i+a], accounts[i+b] = accounts[i+b], accounts[i+a]
				})
			}
		}
		i = j
	}
}

// sameAccountGroup 判断两个 Account 是否属于同一排序组（Priority + LastUsedAt）
func sameAccountGroup(a, b *Account) bool {
	if a.Priority != b.Priority {
		return false
	}
	return sameLastUsedAt(a.LastUsedAt, b.LastUsedAt)
}

// sameLastUsedAt 判断两个 LastUsedAt 是否相同（精度到秒）
func sameLastUsedAt(a, b *time.Time) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return a.Unix() == b.Unix()
	}
}

// sortCandidatesForFallback 根据配置选择排序策略
// mode: "last_used"(按最后使用时间) 或 "random"(随机)
func (s *GatewayService) sortCandidatesForFallback(accounts []*Account, preferOAuth bool, mode string) {
	if mode == "random" {
		// 先按优先级排序，然后在同优先级内随机打乱
		sortAccountsByPriorityOnly(accounts, preferOAuth)
		shuffleWithinPriority(accounts)
	} else {
		// 默认按最后使用时间排序
		sortAccountsByPriorityAndLastUsed(accounts, preferOAuth)
	}
}

// sortAccountsByPriorityOnly 仅按优先级排序
func sortAccountsByPriorityOnly(accounts []*Account, preferOAuth bool) {
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i], accounts[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if preferOAuth && a.Type != b.Type {
			return a.Type == AccountTypeOAuth
		}
		return false
	})
}

// shuffleWithinPriority 在同优先级内随机打乱顺序
func shuffleWithinPriority(accounts []*Account) {
	if len(accounts) <= 1 {
		return
	}
	r := mathrand.New(mathrand.NewSource(time.Now().UnixNano()))
	start := 0
	for start < len(accounts) {
		priority := accounts[start].Priority
		end := start + 1
		for end < len(accounts) && accounts[end].Priority == priority {
			end++
		}
		// 对 [start, end) 范围内的账户随机打乱
		if end-start > 1 {
			r.Shuffle(end-start, func(i, j int) {
				accounts[start+i], accounts[start+j] = accounts[start+j], accounts[start+i]
			})
		}
		start = end
	}
}

// selectAccountForModelWithPlatform 选择单平台账户（完全隔离）
func (s *GatewayService) selectAccountForModelWithPlatform(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, platform string) (*Account, error) {
	preferOAuth := platform == PlatformGemini
	routingAccountIDs := s.routingAccountIDsForRequest(ctx, groupID, requestedModel, platform)

	// require_privacy_set: 获取分组信息
	var schedGroup *Group
	if groupID != nil && s.groupRepo != nil {
		schedGroup, _ = s.groupRepo.GetByID(ctx, *groupID)
	}

	var accounts []Account
	accountsLoaded := false

	// ============ Model Routing (legacy path): apply before sticky session ============
	// When load-awareness is disabled (e.g. concurrency service not configured), we still honor model routing
	// so switching model can switch upstream account within the same sticky session.
	if len(routingAccountIDs) > 0 {
		if s.debugModelRoutingEnabled() {
			logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] legacy routed begin: group_id=%v model=%s platform=%s session=%s routed_ids=%v",
				derefGroupID(groupID), requestedModel, platform, shortSessionHash(sessionHash), routingAccountIDs)
		}
		// 1) Sticky session only applies if the bound account is within the routing set.
		if sessionHash != "" && s.cache != nil {
			accountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
			if err == nil && accountID > 0 && containsInt64(routingAccountIDs, accountID) {
				if _, excluded := excludedIDs[accountID]; !excluded {
					account, err := s.getSchedulableAccount(ctx, accountID)
					// 检查账号分组归属和平台匹配（确保粘性会话不会跨分组或跨平台）
					if err == nil {
						clearSticky := shouldClearStickySession(account, requestedModel)
						if clearSticky {
							_ = s.cache.DeleteSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
						}
						if !clearSticky && s.isAccountInGroup(account, groupID) && account.Platform == platform && (requestedModel == "" || s.isModelSupportedByAccountWithContext(ctx, account, requestedModel)) && s.isAccountSchedulableForModelSelection(ctx, account, requestedModel) && s.isAccountSchedulableForQuota(account) && s.isAccountSchedulableForWindowCost(ctx, account, true) && s.isAccountSchedulableForRPM(ctx, account, true) && !s.isStickyAccountUpstreamRestricted(ctx, groupID, account, requestedModel) {
							if s.debugModelRoutingEnabled() {
								logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] legacy routed sticky hit: group_id=%v model=%s session=%s account=%d", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), accountID)
							}
							return account, nil
						}
					}
				}
			}
		}

		// 2) Select an account from the routed candidates.
		forcePlatform, hasForcePlatform := ctx.Value(ctxkey.ForcePlatform).(string)
		if hasForcePlatform && forcePlatform == "" {
			hasForcePlatform = false
		}
		var err error
		accounts, _, err = s.listSchedulableAccounts(ctx, groupID, platform, hasForcePlatform)
		if err != nil {
			return nil, fmt.Errorf("query accounts failed: %w", err)
		}
		accountsLoaded = true

		// 提前预取窗口费用+RPM 计数，确保 routing 段内的调度检查调用能命中缓存
		ctx = s.withWindowCostPrefetch(ctx, accounts)
		ctx = s.withRPMPrefetch(ctx, accounts)

		routingSet := make(map[int64]struct{}, len(routingAccountIDs))
		for _, id := range routingAccountIDs {
			if id > 0 {
				routingSet[id] = struct{}{}
			}
		}

		var selected *Account
		for i := range accounts {
			acc := &accounts[i]
			if _, ok := routingSet[acc.ID]; !ok {
				continue
			}
			if _, excluded := excludedIDs[acc.ID]; excluded {
				continue
			}
			// Scheduler snapshots can be temporarily stale; re-check schedulability here to
			// avoid selecting accounts that were recently rate-limited/overloaded.
			if !s.isAccountSchedulableForSelection(acc) {
				continue
			}
			// require_privacy_set: 跳过 privacy 未设置的账号并标记异常
			if schedGroup != nil && schedGroup.RequirePrivacySet && !acc.IsPrivacySet() {
				_ = s.accountRepo.SetError(ctx, acc.ID,
					fmt.Sprintf("Privacy not set, required by group [%s]", schedGroup.Name))
				continue
			}
			if requestedModel != "" && !s.isModelSupportedByAccountWithContext(ctx, acc, requestedModel) {
				continue
			}
			if !s.isAccountSchedulableForModelSelection(ctx, acc, requestedModel) {
				continue
			}
			if !s.isAccountSchedulableForQuota(acc) {
				continue
			}
			if !s.isAccountSchedulableForWindowCost(ctx, acc, false) {
				continue
			}
			if !s.isAccountSchedulableForRPM(ctx, acc, false) {
				continue
			}
			if selected == nil {
				selected = acc
				continue
			}
			if acc.Priority < selected.Priority {
				selected = acc
			} else if acc.Priority == selected.Priority {
				switch {
				case acc.LastUsedAt == nil && selected.LastUsedAt != nil:
					selected = acc
				case acc.LastUsedAt != nil && selected.LastUsedAt == nil:
					// keep selected (never used is preferred)
				case acc.LastUsedAt == nil && selected.LastUsedAt == nil:
					if preferOAuth && acc.Type != selected.Type && acc.Type == AccountTypeOAuth {
						selected = acc
					}
				default:
					if acc.LastUsedAt.Before(*selected.LastUsedAt) {
						selected = acc
					}
				}
			}
		}

		if selected != nil {
			if sessionHash != "" && s.cache != nil {
				if err := s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, selected.ID, stickySessionTTL); err != nil {
					logger.LegacyPrintf("service.gateway", "set session account failed: session=%s account_id=%d err=%v", sessionHash, selected.ID, err)
				}
			}
			if s.debugModelRoutingEnabled() {
				logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] legacy routed select: group_id=%v model=%s session=%s account=%d", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), selected.ID)
			}
			return selected, nil
		}
		logger.LegacyPrintf("service.gateway", "[ModelRouting] No routed accounts available for model=%s, falling back to normal selection", requestedModel)
	}

	// 1. 查询粘性会话
	if sessionHash != "" && s.cache != nil {
		accountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
		if err == nil && accountID > 0 {
			if _, excluded := excludedIDs[accountID]; !excluded {
				account, err := s.getSchedulableAccount(ctx, accountID)
				// 检查账号分组归属和平台匹配（确保粘性会话不会跨分组或跨平台）
				if err == nil {
					clearSticky := shouldClearStickySession(account, requestedModel)
					if clearSticky {
						_ = s.cache.DeleteSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
					}
					if !clearSticky && s.isAccountInGroup(account, groupID) && account.Platform == platform && (requestedModel == "" || s.isModelSupportedByAccountWithContext(ctx, account, requestedModel)) && s.isAccountSchedulableForModelSelection(ctx, account, requestedModel) && s.isAccountSchedulableForQuota(account) && s.isAccountSchedulableForWindowCost(ctx, account, true) && s.isAccountSchedulableForRPM(ctx, account, true) {
						return account, nil
					}
				}
			}
		}
	}

	// 2. 获取可调度账号列表（单平台）
	if !accountsLoaded {
		forcePlatform, hasForcePlatform := ctx.Value(ctxkey.ForcePlatform).(string)
		if hasForcePlatform && forcePlatform == "" {
			hasForcePlatform = false
		}
		var err error
		accounts, _, err = s.listSchedulableAccounts(ctx, groupID, platform, hasForcePlatform)
		if err != nil {
			return nil, fmt.Errorf("query accounts failed: %w", err)
		}
	}

	// 批量预取窗口费用+RPM 计数，避免逐个账号查询（N+1）
	ctx = s.withWindowCostPrefetch(ctx, accounts)
	ctx = s.withRPMPrefetch(ctx, accounts)

	// 3. 按优先级+最久未用选择（考虑模型支持）
	// needsUpstreamCheck 仅在主选择循环中使用；粘性会话命中时跳过此检查，
	// 因为粘性会话优先保持连接一致性，且 upstream 计费基准极少使用。
	needsUpstreamCheck := s.needsUpstreamChannelRestrictionCheck(ctx, groupID)
	var selected *Account
	for i := range accounts {
		acc := &accounts[i]
		if _, excluded := excludedIDs[acc.ID]; excluded {
			continue
		}
		// Scheduler snapshots can be temporarily stale; re-check schedulability here to
		// avoid selecting accounts that were recently rate-limited/overloaded.
		if !s.isAccountSchedulableForSelection(acc) {
			continue
		}
		// require_privacy_set: 跳过 privacy 未设置的账号并标记异常
		if schedGroup != nil && schedGroup.RequirePrivacySet && !acc.IsPrivacySet() {
			_ = s.accountRepo.SetError(ctx, acc.ID,
				fmt.Sprintf("Privacy not set, required by group [%s]", schedGroup.Name))
			continue
		}
		if requestedModel != "" && !s.isModelSupportedByAccountWithContext(ctx, acc, requestedModel) {
			continue
		}
		if needsUpstreamCheck && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, acc, requestedModel) {
			continue
		}
		if !s.isAccountSchedulableForModelSelection(ctx, acc, requestedModel) {
			continue
		}
		if !s.isAccountSchedulableForQuota(acc) {
			continue
		}
		if !s.isAccountSchedulableForWindowCost(ctx, acc, false) {
			continue
		}
		if !s.isAccountSchedulableForRPM(ctx, acc, false) {
			continue
		}
		if selected == nil {
			selected = acc
			continue
		}
		if acc.Priority < selected.Priority {
			selected = acc
		} else if acc.Priority == selected.Priority {
			switch {
			case acc.LastUsedAt == nil && selected.LastUsedAt != nil:
				selected = acc
			case acc.LastUsedAt != nil && selected.LastUsedAt == nil:
				// keep selected (never used is preferred)
			case acc.LastUsedAt == nil && selected.LastUsedAt == nil:
				if preferOAuth && acc.Type != selected.Type && acc.Type == AccountTypeOAuth {
					selected = acc
				}
			default:
				if acc.LastUsedAt.Before(*selected.LastUsedAt) {
					selected = acc
				}
			}
		}
	}

	if selected == nil {
		stats := s.logDetailedSelectionFailure(ctx, groupID, sessionHash, requestedModel, platform, accounts, excludedIDs, false)
		if requestedModel != "" {
			return nil, fmt.Errorf("%w supporting model: %s (%s)", ErrNoAvailableAccounts, requestedModel, summarizeSelectionFailureStats(stats))
		}
		return nil, ErrNoAvailableAccounts
	}

	// 4. 建立粘性绑定
	if sessionHash != "" && s.cache != nil {
		if err := s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, selected.ID, stickySessionTTL); err != nil {
			logger.LegacyPrintf("service.gateway", "set session account failed: session=%s account_id=%d err=%v", sessionHash, selected.ID, err)
		}
	}

	return selected, nil
}

// selectAccountWithMixedScheduling 选择账户（支持混合调度）
// 查询原生平台账户 + 启用 mixed_scheduling 的 antigravity 账户
func (s *GatewayService) selectAccountWithMixedScheduling(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, nativePlatform string) (*Account, error) {
	preferOAuth := nativePlatform == PlatformGemini
	routingAccountIDs := s.routingAccountIDsForRequest(ctx, groupID, requestedModel, nativePlatform)

	// require_privacy_set: 获取分组信息
	var schedGroup *Group
	if groupID != nil && s.groupRepo != nil {
		schedGroup, _ = s.groupRepo.GetByID(ctx, *groupID)
	}

	var accounts []Account
	accountsLoaded := false

	// ============ Model Routing (legacy path): apply before sticky session ============
	if len(routingAccountIDs) > 0 {
		if s.debugModelRoutingEnabled() {
			logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] legacy mixed routed begin: group_id=%v model=%s platform=%s session=%s routed_ids=%v",
				derefGroupID(groupID), requestedModel, nativePlatform, shortSessionHash(sessionHash), routingAccountIDs)
		}
		// 1) Sticky session only applies if the bound account is within the routing set.
		if sessionHash != "" && s.cache != nil {
			accountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
			if err == nil && accountID > 0 && containsInt64(routingAccountIDs, accountID) {
				if _, excluded := excludedIDs[accountID]; !excluded {
					account, err := s.getSchedulableAccount(ctx, accountID)
					// 检查账号分组归属和有效性：原生平台直接匹配，antigravity 需要启用混合调度
					if err == nil {
						clearSticky := shouldClearStickySession(account, requestedModel)
						if clearSticky {
							_ = s.cache.DeleteSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
						}
						if !clearSticky && s.isAccountInGroup(account, groupID) && (requestedModel == "" || s.isModelSupportedByAccountWithContext(ctx, account, requestedModel)) && s.isAccountSchedulableForModelSelection(ctx, account, requestedModel) && s.isAccountSchedulableForQuota(account) && s.isAccountSchedulableForWindowCost(ctx, account, true) && s.isAccountSchedulableForRPM(ctx, account, true) {
							if account.Platform == nativePlatform || (account.Platform == PlatformAntigravity && account.IsMixedSchedulingEnabled()) {
								if s.debugModelRoutingEnabled() {
									logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] legacy mixed routed sticky hit: group_id=%v model=%s session=%s account=%d", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), accountID)
								}
								return account, nil
							}
						}
					}
				}
			}
		}

		// 2) Select an account from the routed candidates.
		var err error
		accounts, _, err = s.listSchedulableAccounts(ctx, groupID, nativePlatform, false)
		if err != nil {
			return nil, fmt.Errorf("query accounts failed: %w", err)
		}
		accountsLoaded = true

		// 提前预取窗口费用+RPM 计数，确保 routing 段内的调度检查调用能命中缓存
		ctx = s.withWindowCostPrefetch(ctx, accounts)
		ctx = s.withRPMPrefetch(ctx, accounts)

		routingSet := make(map[int64]struct{}, len(routingAccountIDs))
		for _, id := range routingAccountIDs {
			if id > 0 {
				routingSet[id] = struct{}{}
			}
		}

		var selected *Account
		for i := range accounts {
			acc := &accounts[i]
			if _, ok := routingSet[acc.ID]; !ok {
				continue
			}
			if _, excluded := excludedIDs[acc.ID]; excluded {
				continue
			}
			// Scheduler snapshots can be temporarily stale; re-check schedulability here to
			// avoid selecting accounts that were recently rate-limited/overloaded.
			if !s.isAccountSchedulableForSelection(acc) {
				continue
			}
			// require_privacy_set: 跳过 privacy 未设置的账号并标记异常
			if schedGroup != nil && schedGroup.RequirePrivacySet && !acc.IsPrivacySet() {
				_ = s.accountRepo.SetError(ctx, acc.ID,
					fmt.Sprintf("Privacy not set, required by group [%s]", schedGroup.Name))
				continue
			}
			// 过滤：原生平台直接通过，antigravity 需要启用混合调度
			if acc.Platform == PlatformAntigravity && !acc.IsMixedSchedulingEnabled() {
				continue
			}
			if requestedModel != "" && !s.isModelSupportedByAccountWithContext(ctx, acc, requestedModel) {
				continue
			}
			if !s.isAccountSchedulableForModelSelection(ctx, acc, requestedModel) {
				continue
			}
			if !s.isAccountSchedulableForQuota(acc) {
				continue
			}
			if !s.isAccountSchedulableForWindowCost(ctx, acc, false) {
				continue
			}
			if !s.isAccountSchedulableForRPM(ctx, acc, false) {
				continue
			}
			if selected == nil {
				selected = acc
				continue
			}
			if acc.Priority < selected.Priority {
				selected = acc
			} else if acc.Priority == selected.Priority {
				switch {
				case acc.LastUsedAt == nil && selected.LastUsedAt != nil:
					selected = acc
				case acc.LastUsedAt != nil && selected.LastUsedAt == nil:
					// keep selected (never used is preferred)
				case acc.LastUsedAt == nil && selected.LastUsedAt == nil:
					if preferOAuth && acc.Platform == PlatformGemini && selected.Platform == PlatformGemini && acc.Type != selected.Type && acc.Type == AccountTypeOAuth {
						selected = acc
					}
				default:
					if acc.LastUsedAt.Before(*selected.LastUsedAt) {
						selected = acc
					}
				}
			}
		}

		if selected != nil {
			if sessionHash != "" && s.cache != nil {
				if err := s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, selected.ID, stickySessionTTL); err != nil {
					logger.LegacyPrintf("service.gateway", "set session account failed: session=%s account_id=%d err=%v", sessionHash, selected.ID, err)
				}
			}
			if s.debugModelRoutingEnabled() {
				logger.LegacyPrintf("service.gateway", "[ModelRoutingDebug] legacy mixed routed select: group_id=%v model=%s session=%s account=%d", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), selected.ID)
			}
			return selected, nil
		}
		logger.LegacyPrintf("service.gateway", "[ModelRouting] No routed accounts available for model=%s, falling back to normal selection", requestedModel)
	}

	// 1. 查询粘性会话
	if sessionHash != "" && s.cache != nil {
		accountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
		if err == nil && accountID > 0 {
			if _, excluded := excludedIDs[accountID]; !excluded {
				account, err := s.getSchedulableAccount(ctx, accountID)
				// 检查账号分组归属和有效性：原生平台直接匹配，antigravity 需要启用混合调度
				if err == nil {
					clearSticky := shouldClearStickySession(account, requestedModel)
					if clearSticky {
						_ = s.cache.DeleteSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
					}
					if !clearSticky && s.isAccountInGroup(account, groupID) && (requestedModel == "" || s.isModelSupportedByAccountWithContext(ctx, account, requestedModel)) && s.isAccountSchedulableForModelSelection(ctx, account, requestedModel) && s.isAccountSchedulableForQuota(account) && s.isAccountSchedulableForWindowCost(ctx, account, true) && s.isAccountSchedulableForRPM(ctx, account, true) && !s.isStickyAccountUpstreamRestricted(ctx, groupID, account, requestedModel) {
						if account.Platform == nativePlatform || (account.Platform == PlatformAntigravity && account.IsMixedSchedulingEnabled()) {
							return account, nil
						}
					}
				}
			}
		}
	}

	// 2. 获取可调度账号列表
	if !accountsLoaded {
		var err error
		accounts, _, err = s.listSchedulableAccounts(ctx, groupID, nativePlatform, false)
		if err != nil {
			return nil, fmt.Errorf("query accounts failed: %w", err)
		}
	}

	// 批量预取窗口费用+RPM 计数，避免逐个账号查询（N+1）
	ctx = s.withWindowCostPrefetch(ctx, accounts)
	ctx = s.withRPMPrefetch(ctx, accounts)

	// 3. 按优先级+最久未用选择（考虑模型支持和混合调度）
	// needsUpstreamCheck 仅在主选择循环中使用；粘性会话命中时跳过此检查。
	needsUpstreamCheck := s.needsUpstreamChannelRestrictionCheck(ctx, groupID)
	var selected *Account
	for i := range accounts {
		acc := &accounts[i]
		if _, excluded := excludedIDs[acc.ID]; excluded {
			continue
		}
		// Scheduler snapshots can be temporarily stale; re-check schedulability here to
		// avoid selecting accounts that were recently rate-limited/overloaded.
		if !s.isAccountSchedulableForSelection(acc) {
			continue
		}
		// require_privacy_set: 跳过 privacy 未设置的账号并标记异常
		if schedGroup != nil && schedGroup.RequirePrivacySet && !acc.IsPrivacySet() {
			_ = s.accountRepo.SetError(ctx, acc.ID,
				fmt.Sprintf("Privacy not set, required by group [%s]", schedGroup.Name))
			continue
		}
		// 过滤：原生平台直接通过，antigravity 需要启用混合调度
		if acc.Platform == PlatformAntigravity && !acc.IsMixedSchedulingEnabled() {
			continue
		}
		if requestedModel != "" && !s.isModelSupportedByAccountWithContext(ctx, acc, requestedModel) {
			continue
		}
		if needsUpstreamCheck && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, acc, requestedModel) {
			continue
		}
		if !s.isAccountSchedulableForModelSelection(ctx, acc, requestedModel) {
			continue
		}
		if !s.isAccountSchedulableForQuota(acc) {
			continue
		}
		if !s.isAccountSchedulableForWindowCost(ctx, acc, false) {
			continue
		}
		if !s.isAccountSchedulableForRPM(ctx, acc, false) {
			continue
		}
		if selected == nil {
			selected = acc
			continue
		}
		if acc.Priority < selected.Priority {
			selected = acc
		} else if acc.Priority == selected.Priority {
			switch {
			case acc.LastUsedAt == nil && selected.LastUsedAt != nil:
				selected = acc
			case acc.LastUsedAt != nil && selected.LastUsedAt == nil:
				// keep selected (never used is preferred)
			case acc.LastUsedAt == nil && selected.LastUsedAt == nil:
				if preferOAuth && acc.Platform == PlatformGemini && selected.Platform == PlatformGemini && acc.Type != selected.Type && acc.Type == AccountTypeOAuth {
					selected = acc
				}
			default:
				if acc.LastUsedAt.Before(*selected.LastUsedAt) {
					selected = acc
				}
			}
		}
	}

	if selected == nil {
		stats := s.logDetailedSelectionFailure(ctx, groupID, sessionHash, requestedModel, nativePlatform, accounts, excludedIDs, true)
		if requestedModel != "" {
			return nil, fmt.Errorf("%w supporting model: %s (%s)", ErrNoAvailableAccounts, requestedModel, summarizeSelectionFailureStats(stats))
		}
		return nil, ErrNoAvailableAccounts
	}

	// 4. 建立粘性绑定
	if sessionHash != "" && s.cache != nil {
		if err := s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, selected.ID, stickySessionTTL); err != nil {
			logger.LegacyPrintf("service.gateway", "set session account failed: session=%s account_id=%d err=%v", sessionHash, selected.ID, err)
		}
	}

	return selected, nil
}

type selectionFailureStats struct {
	Total              int
	Eligible           int
	Excluded           int
	Unschedulable      int
	PlatformFiltered   int
	ModelUnsupported   int
	ModelRateLimited   int
	SamplePlatformIDs  []int64
	SampleMappingIDs   []int64
	SampleRateLimitIDs []string
}

type selectionFailureDiagnosis struct {
	Category string
	Detail   string
}

func (s *GatewayService) logDetailedSelectionFailure(
	ctx context.Context,
	groupID *int64,
	sessionHash string,
	requestedModel string,
	platform string,
	accounts []Account,
	excludedIDs map[int64]struct{},
	allowMixedScheduling bool,
) selectionFailureStats {
	stats := s.collectSelectionFailureStats(ctx, accounts, requestedModel, platform, excludedIDs, allowMixedScheduling)
	logger.LegacyPrintf(
		"service.gateway",
		"[SelectAccountDetailed] group_id=%v model=%s platform=%s session=%s total=%d eligible=%d excluded=%d unschedulable=%d platform_filtered=%d model_unsupported=%d model_rate_limited=%d sample_platform_filtered=%v sample_model_unsupported=%v sample_model_rate_limited=%v",
		derefGroupID(groupID),
		requestedModel,
		platform,
		shortSessionHash(sessionHash),
		stats.Total,
		stats.Eligible,
		stats.Excluded,
		stats.Unschedulable,
		stats.PlatformFiltered,
		stats.ModelUnsupported,
		stats.ModelRateLimited,
		stats.SamplePlatformIDs,
		stats.SampleMappingIDs,
		stats.SampleRateLimitIDs,
	)
	return stats
}

func (s *GatewayService) collectSelectionFailureStats(
	ctx context.Context,
	accounts []Account,
	requestedModel string,
	platform string,
	excludedIDs map[int64]struct{},
	allowMixedScheduling bool,
) selectionFailureStats {
	stats := selectionFailureStats{
		Total: len(accounts),
	}

	for i := range accounts {
		acc := &accounts[i]
		diagnosis := s.diagnoseSelectionFailure(ctx, acc, requestedModel, platform, excludedIDs, allowMixedScheduling)
		switch diagnosis.Category {
		case "excluded":
			stats.Excluded++
		case "unschedulable":
			stats.Unschedulable++
		case "platform_filtered":
			stats.PlatformFiltered++
			stats.SamplePlatformIDs = appendSelectionFailureSampleID(stats.SamplePlatformIDs, acc.ID)
		case "model_unsupported":
			stats.ModelUnsupported++
			stats.SampleMappingIDs = appendSelectionFailureSampleID(stats.SampleMappingIDs, acc.ID)
		case "model_rate_limited":
			stats.ModelRateLimited++
			remaining := acc.GetRateLimitRemainingTimeWithContext(ctx, requestedModel).Truncate(time.Second)
			stats.SampleRateLimitIDs = appendSelectionFailureRateSample(stats.SampleRateLimitIDs, acc.ID, remaining)
		default:
			stats.Eligible++
		}
	}

	return stats
}

func (s *GatewayService) diagnoseSelectionFailure(
	ctx context.Context,
	acc *Account,
	requestedModel string,
	platform string,
	excludedIDs map[int64]struct{},
	allowMixedScheduling bool,
) selectionFailureDiagnosis {
	if acc == nil {
		return selectionFailureDiagnosis{Category: "unschedulable", Detail: "account_nil"}
	}
	if _, excluded := excludedIDs[acc.ID]; excluded {
		return selectionFailureDiagnosis{Category: "excluded"}
	}
	if !s.isAccountSchedulableForSelection(acc) {
		return selectionFailureDiagnosis{Category: "unschedulable", Detail: "generic_unschedulable"}
	}
	if isPlatformFilteredForSelection(acc, platform, allowMixedScheduling) {
		return selectionFailureDiagnosis{
			Category: "platform_filtered",
			Detail:   fmt.Sprintf("account_platform=%s requested_platform=%s", acc.Platform, strings.TrimSpace(platform)),
		}
	}
	if requestedModel != "" && !s.isModelSupportedByAccountWithContext(ctx, acc, requestedModel) {
		return selectionFailureDiagnosis{
			Category: "model_unsupported",
			Detail:   fmt.Sprintf("model=%s", requestedModel),
		}
	}
	if !s.isAccountSchedulableForModelSelection(ctx, acc, requestedModel) {
		remaining := acc.GetRateLimitRemainingTimeWithContext(ctx, requestedModel).Truncate(time.Second)
		return selectionFailureDiagnosis{
			Category: "model_rate_limited",
			Detail:   fmt.Sprintf("remaining=%s", remaining),
		}
	}
	return selectionFailureDiagnosis{Category: "eligible"}
}

func isPlatformFilteredForSelection(acc *Account, platform string, allowMixedScheduling bool) bool {
	if acc == nil {
		return true
	}
	if allowMixedScheduling {
		if acc.Platform == PlatformAntigravity {
			return !acc.IsMixedSchedulingEnabled()
		}
		return acc.Platform != platform
	}
	if strings.TrimSpace(platform) == "" {
		return false
	}
	return acc.Platform != platform
}

func appendSelectionFailureSampleID(samples []int64, id int64) []int64 {
	const limit = 5
	if len(samples) >= limit {
		return samples
	}
	return append(samples, id)
}

func appendSelectionFailureRateSample(samples []string, accountID int64, remaining time.Duration) []string {
	const limit = 5
	if len(samples) >= limit {
		return samples
	}
	return append(samples, fmt.Sprintf("%d(%s)", accountID, remaining))
}

func summarizeSelectionFailureStats(stats selectionFailureStats) string {
	return fmt.Sprintf(
		"total=%d eligible=%d excluded=%d unschedulable=%d platform_filtered=%d model_unsupported=%d model_rate_limited=%d",
		stats.Total,
		stats.Eligible,
		stats.Excluded,
		stats.Unschedulable,
		stats.PlatformFiltered,
		stats.ModelUnsupported,
		stats.ModelRateLimited,
	)
}

// isModelSupportedByAccountWithContext 根据账户平台检查模型支持（带 context）
// 对于 Antigravity 平台，会先获取映射后的最终模型名（包括 thinking 后缀）再检查支持
func (s *GatewayService) isModelSupportedByAccountWithContext(ctx context.Context, account *Account, requestedModel string) bool {
	if account.Platform == PlatformAntigravity {
		if strings.TrimSpace(requestedModel) == "" {
			return true
		}
		// 使用与转发阶段一致的映射逻辑：自定义映射优先 → 默认映射兜底
		mapped := mapAntigravityModel(account, requestedModel)
		if mapped == "" {
			return false
		}
		// 应用 thinking 后缀后检查最终模型是否在账号映射中
		if enabled, ok := ThinkingEnabledFromContext(ctx); ok {
			finalModel := applyThinkingModelSuffix(mapped, enabled)
			if finalModel == mapped {
				return true // thinking 后缀未改变模型名，映射已通过
			}
			return account.IsModelSupported(finalModel)
		}
		return true
	}
	return s.isModelSupportedByAccount(account, requestedModel)
}

// isModelSupportedByAccount 根据账户平台检查模型支持（无 context，用于非 Antigravity 平台）
func (s *GatewayService) isModelSupportedByAccount(account *Account, requestedModel string) bool {
	if account.Platform == PlatformAntigravity {
		if strings.TrimSpace(requestedModel) == "" {
			return true
		}
		return mapAntigravityModel(account, requestedModel) != ""
	}
	if account.IsBedrock() {
		_, ok := ResolveBedrockModelID(account, requestedModel)
		return ok
	}
	// OpenAI 透传模式：仅替换认证，允许所有模型
	if account.Platform == PlatformOpenAI && account.IsOpenAIPassthroughEnabled() {
		return true
	}
	// OAuth/SetupToken 账号使用 Anthropic 标准映射（短ID → 长ID）
	if account.Platform == PlatformAnthropic && account.Type != AccountTypeAPIKey && account.Type != AccountTypeKiro {
		if account.Type == AccountTypeServiceAccount {
			requestedModel = normalizeVertexAnthropicModelID(claude.NormalizeModelID(requestedModel))
		} else {
			requestedModel = claude.NormalizeModelID(requestedModel)
		}
	}
	// 其他平台使用账户的模型支持检查
	return account.IsModelSupported(requestedModel)
}

// GetAccessToken 获取账号凭证
func (s *GatewayService) GetAccessToken(ctx context.Context, account *Account) (string, string, error) {
	switch account.Type {
	case AccountTypeOAuth, AccountTypeSetupToken:
		// Both oauth and setup-token use OAuth token flow
		return s.getOAuthToken(ctx, account)
	case AccountTypeAPIKey:
		apiKey := account.GetCredential("api_key")
		if apiKey == "" {
			return "", "", errors.New("api_key not found in credentials")
		}
		return apiKey, "apikey", nil
	case AccountTypeBedrock:
		return "", "bedrock", nil // Bedrock 使用 SigV4 签名或 API Key，由 forwardBedrock 处理
	case AccountTypeKiro:
		// Legacy Kiro passthrough credentials use the external relay's API key.
		// Native Kiro requests bypass this generic path via forwardKiro.
		apiKey := account.GetCredential("api_key")
		if apiKey == "" {
			return "", "", errors.New("kiro account: api_key not found in credentials")
		}
		return apiKey, "kiro", nil
	case AccountTypeServiceAccount:
		if account.Platform != PlatformAnthropic {
			return "", "", fmt.Errorf("unsupported service account platform: %s", account.Platform)
		}
		if s.claudeTokenProvider == nil {
			return "", "", errors.New("claude token provider not configured")
		}
		accessToken, err := s.claudeTokenProvider.GetAccessToken(ctx, account)
		if err != nil {
			return "", "", err
		}
		return accessToken, "service_account", nil
	default:
		return "", "", fmt.Errorf("unsupported account type: %s", account.Type)
	}
}

func (s *GatewayService) getOAuthToken(ctx context.Context, account *Account) (string, string, error) {
	// 对于 Anthropic OAuth 账号，使用 ClaudeTokenProvider 获取缓存的 token
	if account.Platform == PlatformAnthropic && account.Type == AccountTypeOAuth && s.claudeTokenProvider != nil {
		accessToken, err := s.claudeTokenProvider.GetAccessToken(ctx, account)
		if err != nil {
			return "", "", err
		}
		return accessToken, "oauth", nil
	}

	// Grok OAuth: prefer access_token from credentials (background refresher keeps it warm).
	if account.Platform == PlatformGrok && account.Type == AccountTypeOAuth {
		accessToken := account.GetGrokAccessToken()
		if accessToken == "" {
			return "", "", errors.New("grok access_token not found in credentials")
		}
		return accessToken, "oauth", nil
	}

	// 其他情况（Gemini 有自己的 TokenProvider，setup-token 类型等）直接从账号读取
	accessToken := account.GetCredential("access_token")
	if accessToken == "" {
		return "", "", errors.New("access_token not found in credentials")
	}
	// Token刷新由后台 TokenRefreshService 处理，此处只返回当前token
	return accessToken, "oauth", nil
}

// GetAvailableModels returns the list of models available for a group
// It aggregates model_mapping keys from all schedulable accounts in the group

// DoGrokNativeResponsesJSON POSTs a non-streaming Responses body to the account's
// Grok upstream and returns the raw JSON body. Used by /v1/web_search.
// Gin-free: UA is always the pinned Grok CLI identity (resolveGrokUpstreamUserAgent ignores inbound).
func (s *GatewayService) DoGrokNativeResponsesJSON(ctx context.Context, account *Account, body []byte) ([]byte, error) {
	if s == nil || s.httpUpstream == nil {
		return nil, errors.New("http upstream not configured")
	}
	if account == nil {
		return nil, errors.New("account is required")
	}
	if !account.IsGrok() {
		return nil, errors.New("grok account required")
	}
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		// Credential/token failures should try the next Grok account in the pool.
		return nil, &UpstreamFailoverError{
			StatusCode: http.StatusUnauthorized,
			Reason:     GatewayFailureReason("grok_search_token"),
		}
	}
	targetURL, err := buildGrokResponsesURL(account, nil, s.settingService)
	if err != nil {
		return nil, err
	}
	if json.Valid(body) {
		if model := strings.TrimSpace(gjson.GetBytes(body, "model").String()); model == "" {
			if patched, patchErr := sjson.SetBytes(body, "model", xai.DefaultTextModel); patchErr == nil {
				body = patched
			}
		}
	}
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build grok responses request: %w", err)
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+token)
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Accept", "application/json")
	upstreamReq.Header.Set("User-Agent", defaultGrokUpstreamUserAgent())
	applyGrokCLIHeaders(upstreamReq.Header)
	account.ApplyHeaderOverrides(upstreamReq.Header)
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, &UpstreamFailoverError{StatusCode: http.StatusBadGateway, Reason: GatewayFailureReason("grok_search_transport")}
	}
	defer func() { _ = resp.Body.Close() }()
	respBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return nil, &UpstreamFailoverError{
			StatusCode: http.StatusBadGateway,
			Reason:     GatewayFailureReason("grok_search_read"),
		}
	}
	if resp.StatusCode >= 400 {
		msg := string(respBytes)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusPaymentRequired || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: respBytes}
		}
		return nil, fmt.Errorf("grok upstream %d: %s", resp.StatusCode, msg)
	}
	return respBytes, nil
}

type availableModelsQueryResult struct {
	models             []string
	useDefaultFallback bool
}

type availableModelsCacheEntry struct {
	models             []string
	useDefaultFallback bool
}

// GetAvailableModels returns the list of models available for a group.
// It preserves the legacy /v1/models behavior: lookup errors, empty account sets,
// and accounts without explicit model mappings all return nil so callers can fall
// back to their historical default model catalog.
func (s *GatewayService) GetAvailableModels(ctx context.Context, groupID *int64, platform string) []string {
	result, err := s.getAvailableModels(ctx, groupID, platform)
	if err != nil {
		return nil
	}
	return result.models
}

// GetAvailableModelsForDiscovery returns models for the user-facing discovery page.
// The boolean return value is true only when accounts exist but none define a
// model mapping, which is the only case where the UI should fall back to defaults.
func (s *GatewayService) GetAvailableModelsForDiscovery(ctx context.Context, groupID *int64, platform string) ([]string, bool, error) {
	result, err := s.getAvailableModels(ctx, groupID, platform)
	if err != nil {
		return nil, false, err
	}
	return result.models, result.useDefaultFallback, nil
}

func (s *GatewayService) getAvailableModels(ctx context.Context, groupID *int64, platform string) (availableModelsQueryResult, error) {
	cacheKey := modelsListCacheKey(groupID, platform)
	if s.modelsListCache != nil {
		if cached, found := s.modelsListCache.Get(cacheKey); found {
			if entry, ok := cached.(availableModelsCacheEntry); ok {
				modelsListCacheHitTotal.Add(1)
				return availableModelsQueryResult{
					models:             cloneStringSlice(entry.models),
					useDefaultFallback: entry.useDefaultFallback,
				}, nil
			}
			if models, ok := cached.([]string); ok {
				modelsListCacheHitTotal.Add(1)
				return availableModelsQueryResult{models: cloneStringSlice(models)}, nil
			}
		}
	}
	modelsListCacheMissTotal.Add(1)

	accounts, err := s.listAccountsForAvailableModels(ctx, groupID, platform)
	if err != nil {
		return availableModelsQueryResult{}, err
	}
	if len(accounts) == 0 {
		return availableModelsQueryResult{}, nil
	}

	// Collect unique models from all accounts.
	modelSet := make(map[string]struct{})
	hasAnyMapping := false

	for i := range accounts {
		// Passthrough routing accepts models independently of model_mapping. A stale
		// mapping on any eligible passthrough account therefore cannot define the
		// public whitelist; return empty so the handler uses its default model set.
		if platform == PlatformOpenAI && accounts[i].IsOpenAIPassthroughEnabled() {
			if s.modelsListCache != nil {
				s.modelsListCache.Set(cacheKey, []string(nil), s.modelsListCacheTTL)
				modelsListCacheStoreTotal.Add(1)
			}
			return availableModelsQueryResult{}, nil
		}

		mapping := accounts[i].GetModelMapping()
		if len(mapping) > 0 {
			hasAnyMapping = true
			for model := range mapping {
				if availableModelMatchesDiscoveryPlatform(accounts[i], platform, model) {
					modelSet[model] = struct{}{}
				}
			}
		}
	}

	// If accounts exist but no account has a model_mapping, caller may use defaults.
	if !hasAnyMapping {
		if s.modelsListCache != nil {
			s.modelsListCache.Set(cacheKey, availableModelsCacheEntry{useDefaultFallback: true}, s.modelsListCacheTTL)
			modelsListCacheStoreTotal.Add(1)
		}
		return availableModelsQueryResult{useDefaultFallback: true}, nil
	}

	if len(modelSet) == 0 {
		return availableModelsQueryResult{}, nil
	}

	// Convert to slice.
	models := make([]string, 0, len(modelSet))
	for model := range modelSet {
		models = append(models, model)
	}
	sort.Strings(models)

	if s.modelsListCache != nil {
		s.modelsListCache.Set(cacheKey, availableModelsCacheEntry{models: cloneStringSlice(models)}, s.modelsListCacheTTL)
		modelsListCacheStoreTotal.Add(1)
	}
	return availableModelsQueryResult{models: cloneStringSlice(models)}, nil
}

func availableModelMatchesDiscoveryPlatform(account Account, platform string, model string) bool {
	normalizedPlatform := strings.TrimSpace(platform)
	if normalizedPlatform == "" || account.Platform != PlatformAntigravity {
		return true
	}
	model = strings.ToLower(strings.TrimSpace(model))
	switch normalizedPlatform {
	case PlatformAnthropic:
		return strings.HasPrefix(model, "claude")
	case PlatformGemini:
		return strings.HasPrefix(model, "gemini")
	default:
		return true
	}
}

func (s *GatewayService) listAccountsForAvailableModels(ctx context.Context, groupID *int64, platform string) ([]Account, error) {
	normalizedPlatform := strings.TrimSpace(platform)
	if normalizedPlatform == "" {
		if groupID != nil {
			return s.accountRepo.ListSchedulableByGroupID(ctx, *groupID)
		}
		return s.accountRepo.ListSchedulable(ctx)
	}

	if normalizedPlatform == PlatformAnthropic || normalizedPlatform == PlatformGemini {
		platforms := []string{normalizedPlatform, PlatformAntigravity}
		var accounts []Account
		var err error
		if groupID != nil {
			accounts, err = s.accountRepo.ListSchedulableByGroupIDAndPlatforms(ctx, *groupID, platforms)
		} else {
			accounts, err = s.accountRepo.ListSchedulableByPlatforms(ctx, platforms)
		}
		if err != nil {
			return nil, err
		}
		filtered := make([]Account, 0, len(accounts))
		for _, acc := range accounts {
			if acc.Platform == normalizedPlatform || (acc.Platform == PlatformAntigravity && acc.IsMixedSchedulingEnabled()) {
				filtered = append(filtered, acc)
			}
		}
		return filtered, nil
	}

	if groupID != nil {
		return s.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, *groupID, normalizedPlatform)
	}
	return s.accountRepo.ListSchedulableByPlatform(ctx, normalizedPlatform)
}

func (s *GatewayService) resolveCompositeModelOwnership(ctx context.Context, groupID int64, model string) (CompositeModelOwnership, error) {
	model = strings.TrimSpace(model)
	if s == nil || s.accountRepo == nil || groupID <= 0 || model == "" {
		return CompositeModelOwnership{}, nil
	}

	cacheKey := compositeModelOwnershipCacheKey(groupID, model)
	if s.modelsListCache != nil {
		if cached, found := s.modelsListCache.Get(cacheKey); found {
			if ownership, ok := cached.(CompositeModelOwnership); ok {
				return ownership, nil
			}
		}
	}

	accounts, err := s.accountRepo.ListSchedulableByGroupID(ctx, groupID)
	if err != nil {
		return CompositeModelOwnership{}, err
	}

	platforms := make(map[string]struct{})
	for _, account := range accounts {
		platform := strings.TrimSpace(account.Platform)
		if !isConcreteRequestPlatform(platform) || !explicitModelMappingClaims(account, model) {
			continue
		}
		platforms[platform] = struct{}{}
	}

	ownership := CompositeModelOwnership{}
	if len(platforms) == 1 {
		for platform := range platforms {
			ownership.TargetPlatform = platform
		}
		ownership.Matched = true
	} else if len(platforms) > 1 {
		ownership.Ambiguous = true
	}

	if s.modelsListCache != nil {
		s.modelsListCache.Set(cacheKey, ownership, s.modelsListCacheTTL)
	}
	return ownership, nil
}

func explicitModelMappingClaims(account Account, model string) bool {
	if account.Credentials == nil || model == "" {
		return false
	}
	mapped, ok := stringMappingFromRaw(account.Credentials["model_mapping"])[model]
	return ok && strings.TrimSpace(mapped) != ""
}

// GetSchedulablePlatforms returns the concrete platforms that currently have
// schedulable accounts in the target group.
func (s *GatewayService) GetSchedulablePlatforms(ctx context.Context, groupID *int64) map[string]struct{} {
	platforms := make(map[string]struct{})
	if s == nil || s.accountRepo == nil {
		return platforms
	}

	var accounts []Account
	var err error
	if groupID != nil {
		accounts, err = s.accountRepo.ListSchedulableByGroupID(ctx, *groupID)
	} else {
		accounts, err = s.accountRepo.ListSchedulable(ctx)
	}
	if err != nil {
		return platforms
	}

	for _, acc := range accounts {
		platform := strings.TrimSpace(acc.Platform)
		if platform != "" {
			platforms[platform] = struct{}{}
		}
	}
	return platforms
}

func (s *GatewayService) InvalidateAvailableModelsCache(groupID *int64, platform string) {
	if s == nil || s.modelsListCache == nil {
		return
	}
	s.invalidateCompositeModelOwnershipCache(groupID)

	normalizedPlatform := strings.TrimSpace(platform)
	// 完整匹配时精准失效；否则按维度批量失效。
	if groupID != nil && normalizedPlatform != "" {
		s.modelsListCache.Delete(modelsListCacheKey(groupID, normalizedPlatform))
		return
	}

	targetGroup := derefGroupID(groupID)
	for key := range s.modelsListCache.Items() {
		parts := strings.SplitN(key, "|", 2)
		if len(parts) != 2 {
			continue
		}
		groupPart, parseErr := strconv.ParseInt(parts[0], 10, 64)
		if parseErr != nil {
			continue
		}
		if groupID != nil && groupPart != targetGroup {
			continue
		}
		if normalizedPlatform != "" && parts[1] != normalizedPlatform {
			continue
		}
		s.modelsListCache.Delete(key)
	}
}

func (s *GatewayService) invalidateCompositeModelOwnershipCache(groupID *int64) {
	for key := range s.modelsListCache.Items() {
		if !strings.HasPrefix(key, compositeModelOwnershipCachePrefix) {
			continue
		}
		if groupID == nil {
			s.modelsListCache.Delete(key)
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(key, compositeModelOwnershipCachePrefix), "|", 2)
		if len(parts) != 2 {
			continue
		}
		cachedGroupID, err := strconv.ParseInt(parts[0], 10, 64)
		if err == nil && cachedGroupID == *groupID {
			s.modelsListCache.Delete(key)
		}
	}
}

const debugGatewayBodyDefaultFilename = "gateway_debug.log"

// initDebugGatewayBodyFile 初始化网关调试日志文件。
//
//   - "1"/"true" 等布尔值 → 当前目录下 gateway_debug.log
//   - 已有目录路径        → 该目录下 gateway_debug.log
//   - 其他               → 视为完整文件路径
func (s *GatewayService) initDebugGatewayBodyFile(path string) {
	if parseDebugEnvBool(path) {
		path = debugGatewayBodyDefaultFilename
	}

	// 如果 path 指向一个已存在的目录，自动追加默认文件名
	if info, err := os.Stat(path); err == nil && info.IsDir() { //nolint:gosec // G703: path 仅来自启动环境变量 SUB2API_DEBUG_GATEWAY_BODY（运维配置），非请求输入
		path = filepath.Join(path, debugGatewayBodyDefaultFilename)
	}

	// 确保父目录存在
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil { //nolint:gosec // G703: 同上
			slog.Error("failed to create gateway debug log directory", "dir", dir, "error", err)
			return
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644) //nolint:gosec // G703: 同上
	if err != nil {
		slog.Error("failed to open gateway debug log file", "path", path, "error", err)
		return
	}
	s.debugGatewayBodyFile.Store(f)
	slog.Info("gateway debug logging enabled", "path", path)
}

// debugLogGatewaySnapshot 将网关请求的完整快照（headers + body）写入独立的调试日志文件，
// 用于对比客户端原始请求和上游转发请求。
//
// 启用方式（环境变量）：
//
//	SUB2API_DEBUG_GATEWAY_BODY=1                          # 写入 gateway_debug.log
//	SUB2API_DEBUG_GATEWAY_BODY=/tmp/gateway_debug.log     # 写入指定路径
//
// tag: "CLIENT_ORIGINAL" 或 "UPSTREAM_FORWARD"
func (s *GatewayService) debugLogGatewaySnapshot(tag string, headers http.Header, body []byte, extra map[string]string) {
	f := s.debugGatewayBodyFile.Load()
	if f == nil {
		return
	}

	var buf strings.Builder
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	fmt.Fprintf(&buf, "\n========== [%s] %s ==========\n", ts, tag)

	// 1. context
	if len(extra) > 0 {
		fmt.Fprint(&buf, "--- context ---\n")
		extraKeys := make([]string, 0, len(extra))
		for k := range extra {
			extraKeys = append(extraKeys, k)
		}
		sort.Strings(extraKeys)
		for _, k := range extraKeys {
			fmt.Fprintf(&buf, "  %s: %s\n", k, extra[k])
		}
	}

	// 2. headers（按真实 Claude CLI wire 顺序排列，便于与抓包对比；auth 脱敏）
	fmt.Fprint(&buf, "--- headers ---\n")
	for _, k := range sortHeadersByWireOrder(headers) {
		for _, v := range headers[k] {
			fmt.Fprintf(&buf, "  %s: %s\n", k, safeHeaderValueForLog(k, v))
		}
	}

	// 3. body（完整输出，格式化 JSON 便于 diff）
	fmt.Fprint(&buf, "--- body ---\n")
	if len(body) == 0 {
		fmt.Fprint(&buf, "  (empty)\n")
	} else {
		var pretty bytes.Buffer
		if json.Indent(&pretty, body, "  ", "  ") == nil {
			fmt.Fprintf(&buf, "  %s\n", pretty.Bytes())
		} else {
			// JSON 格式化失败时原样输出
			fmt.Fprintf(&buf, "  %s\n", body)
		}
	}

	// 写入文件（调试用，并发写入可能交错但不影响可读性）
	_, _ = f.WriteString(buf.String())
}
