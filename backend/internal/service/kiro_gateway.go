package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"sub2api-modded/internal/logger"
)

// forwardKiro 转发请求到 kiro-rs 代理（base_url + api_key 透传）
// kiro-rs 对外暴露标准 Anthropic Messages API，响应中附带 kiro_credits 字段
func (s *GatewayService) forwardKiro(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *ParsedRequest,
	startTime time.Time,
) (*ForwardResult, error) {
	// 获取上游配置
	baseURL := strings.TrimSpace(account.GetCredential("base_url"))
	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	if baseURL == "" || apiKey == "" {
		return nil, fmt.Errorf("kiro account missing base_url or api_key")
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	// 构建上游请求 URL
	upstreamURL := baseURL + "/v1/messages"

	// 创建请求
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(parsed.Body))
	if err != nil {
		return nil, fmt.Errorf("kiro: create request: %w", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-api-key", apiKey)

	// 透传 Claude 相关 headers
	if v := c.GetHeader("anthropic-version"); v != "" {
		req.Header.Set("anthropic-version", v)
	}
	if v := c.GetHeader("anthropic-beta"); v != "" {
		req.Header.Set("anthropic-beta", v)
	}

	// 代理 URL
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	// 发送请求
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		logger.LegacyPrintf("service.gateway", "[Kiro] request failed (account=%s): %v", account.Name, err)
		return nil, fmt.Errorf("kiro request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 处理错误响应
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))

		// 透传上游错误
		c.Header("Content-Type", resp.Header.Get("Content-Type"))
		c.Status(resp.StatusCode)
		_, _ = c.Writer.Write(respBody)

		return &ForwardResult{
			Model: parsed.Model,
		}, nil
	}

	// 处理成功响应
	var usage ClaudeUsage
	var firstTokenMs *int
	var clientDisconnect bool

	if parsed.Stream {
		// 流式响应：透传并提取 usage
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Status(http.StatusOK)

		streamRes := s.streamKiroResponse(c, resp, startTime)
		usage = streamRes.usage
		firstTokenMs = streamRes.firstTokenMs
		clientDisconnect = streamRes.clientDisconnect
	} else {
		// 非流式响应：直接透传
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("kiro: read response: %w", err)
		}

		// 提取 usage（含 kiro_credits）
		parsedUsage := parseClaudeUsageFromResponseBody(respBody)
		if parsedUsage != nil {
			usage = *parsedUsage
		}

		c.Header("Content-Type", resp.Header.Get("Content-Type"))
		c.Status(http.StatusOK)
		_, _ = c.Writer.Write(respBody)
	}

	duration := time.Since(startTime)
	logger.LegacyPrintf("service.gateway", "[Kiro] account=%s status=success duration_ms=%d credits=%.6f",
		account.Name, duration.Milliseconds(), usage.KiroCredits)

	return &ForwardResult{
		Model:            parsed.Model,
		Stream:           parsed.Stream,
		Duration:         duration,
		FirstTokenMs:     firstTokenMs,
		ClientDisconnect: clientDisconnect,
		Usage:            usage,
	}, nil
}

// kiroStreamResult 流式响应结果
type kiroStreamResult struct {
	usage            ClaudeUsage
	firstTokenMs     *int
	clientDisconnect bool
}

// streamKiroResponse 透传 kiro-rs 的 SSE 流并提取 usage（含 kiro_credits）
func (s *GatewayService) streamKiroResponse(c *gin.Context, resp *http.Response, startTime time.Time) *kiroStreamResult {
	usage := &ClaudeUsage{}
	var firstTokenMs *int
	clientDisconnected := false

	flusher, _ := c.Writer.(http.Flusher)

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanBuf := make([]byte, 64*1024)
	scanner.Buffer(scanBuf[:0], maxLineSize)

	for scanner.Scan() {
		line := scanner.Text()

		// 记录首 token 时间
		if firstTokenMs == nil && len(line) > 0 {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}

		// 尝试从 SSE data 行提取 usage（含 kiro_credits）
		extractKiroSSEUsage(line, usage)

		// 透传行到客户端
		if _, err := fmt.Fprintf(c.Writer, "%s\n", line); err != nil {
			clientDisconnected = true
			// 继续读取上游以获取完整 usage 用于计费
			for scanner.Scan() {
				extractKiroSSEUsage(scanner.Text(), usage)
			}
			break
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	return &kiroStreamResult{
		usage:            *usage,
		firstTokenMs:     firstTokenMs,
		clientDisconnect: clientDisconnected,
	}
}

// extractKiroSSEUsage 从 SSE data 行中提取 usage（含 kiro_credits）
func extractKiroSSEUsage(line string, usage *ClaudeUsage) {
	if !strings.HasPrefix(line, "data: ") {
		return
	}
	dataStr := strings.TrimPrefix(line, "data: ")
	var event map[string]any
	if json.Unmarshal([]byte(dataStr), &event) != nil {
		return
	}
	u, ok := event["usage"].(map[string]any)
	if !ok {
		return
	}
	if v, ok := u["input_tokens"].(float64); ok && int(v) > 0 {
		usage.InputTokens = int(v)
	}
	if v, ok := u["output_tokens"].(float64); ok && int(v) > 0 {
		usage.OutputTokens = int(v)
	}
	if v, ok := u["cache_read_input_tokens"].(float64); ok && int(v) > 0 {
		usage.CacheReadInputTokens = int(v)
	}
	if v, ok := u["cache_creation_input_tokens"].(float64); ok && int(v) > 0 {
		usage.CacheCreationInputTokens = int(v)
	}
	// Kiro credits
	if v, ok := u["kiro_credits"].(float64); ok && v > 0 {
		usage.KiroCredits = v
	}
}
