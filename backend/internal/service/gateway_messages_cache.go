package service

import (
	"bytes"
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// stripMessageCacheControl 移除 $.messages[*].content[*].cache_control。
// 与 Parrot _strip_message_cache_control 语义一致。
//
// 旧策略为什么整体清空：客户端（特别是 Claude Code）经常把 cache_control 打在
// "当前最后一条 user message" 上；下一轮对话 messages 追加后，原本的最后一条
// 变成中间某条，cache_control 还挂着就导致"前缀签名变化"，破坏缓存命中。
// 统一由代理重新打断点（addMessageCacheBreakpoints）才能在多轮间稳定。
func stripMessageCacheControl(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	msgIdx := -1
	messages.ForEach(func(_, msg gjson.Result) bool {
		msgIdx++
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		blockIdx := -1
		content.ForEach(func(_, block gjson.Result) bool {
			blockIdx++
			if !block.Get("cache_control").Exists() {
				return true
			}
			path := fmt.Sprintf("messages.%d.content.%d.cache_control", msgIdx, blockIdx)
			if next, err := sjson.DeleteBytes(body, path); err == nil {
				body = next
			}
			return true
		})
		return true
	})
	return body
}

// addMessageCacheBreakpoints 在 messages 上注入两个稳定的 cache 断点：
//  1. 最后一条 message
//  2. 当 messages 数量 ≥ 4 时，倒数第二个 role=user 的 message
//
// 与 Parrot add_cache_breakpoints 一致。两个断点 + system prompt block 的断点
// + tools[-1] 的断点共同构成最多 4 个断点（Anthropic 上限）。
//
// cache_control ttl 策略：
//   - 若目标 block 已有 cache_control.ttl → 不覆盖
//   - 否则写入 {"type":"ephemeral","ttl": claude.DefaultCacheControlTTL}
//
// 调用前应先 stripMessageCacheControl 以保证幂等和稳定。
func addMessageCacheBreakpoints(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	arr := messages.Array()
	if len(arr) == 0 {
		return body
	}

	body = injectCacheControlOnLastContentBlock(body, len(arr)-1, &arr[len(arr)-1])

	if len(arr) >= 4 {
		userCount := 0
		for i := len(arr) - 1; i >= 0; i-- {
			if arr[i].Get("role").String() != "user" {
				continue
			}
			userCount++
			if userCount == 2 {
				body = injectCacheControlOnLastContentBlock(body, i, &arr[i])
				break
			}
		}
	}

	return body
}

// rewriteMessageCacheControlIfEnabled 按系统设置决定是否执行旧版 messages 缓存断点改写。
func (s *GatewayService) rewriteMessageCacheControlIfEnabled(ctx context.Context, body []byte) []byte {
	if s == nil || !s.isRewriteMessageCacheControlEnabled(ctx) {
		return body
	}
	body = stripMessageCacheControl(body)
	return addMessageCacheBreakpoints(body)
}

func (s *GatewayService) isRewriteMessageCacheControlEnabled(ctx context.Context) bool {
	if s == nil {
		return false
	}
	if s.settingService != nil {
		return s.settingService.IsRewriteMessageCacheControlEnabled(ctx)
	}
	return false
}

// injectCacheControlOnLastContentBlock 把 cache_control 断点打在 messages[idx]
// 的最后一个 content block 上。若 content 是 string，先升级成单块 text 数组
// （对齐 Parrot _inject_cache_on_msg 的行为）。
//
// msg 是调用方已持有的 gjson.Result 快照，用于省一次 GetBytes。
func injectCacheControlOnLastContentBlock(body []byte, idx int, msg *gjson.Result) []byte {
	content := msg.Get("content")

	if content.Type == gjson.String {
		text := content.String()
		blockRaw := fmt.Sprintf(
			`[{"type":"text","text":%s,"cache_control":{"type":"ephemeral","ttl":%q}}]`,
			mustJSONString(text), claude.DefaultCacheControlTTL,
		)
		if next, err := sjson.SetRawBytes(body, fmt.Sprintf("messages.%d.content", idx), []byte(blockRaw)); err == nil {
			body = next
		}
		return body
	}

	if !content.IsArray() {
		return body
	}
	contentArr := content.Array()
	if len(contentArr) == 0 {
		return body
	}
	lastBlockIdx := len(contentArr) - 1
	lastBlock := contentArr[lastBlockIdx]

	if cc := lastBlock.Get("cache_control"); cc.Exists() && cc.Get("ttl").String() != "" {
		return body
	}

	pathPrefix := fmt.Sprintf("messages.%d.content.%d.cache_control", idx, lastBlockIdx)
	existingCC := lastBlock.Get("cache_control")
	if existingCC.Exists() {
		if next, err := sjson.SetBytes(body, pathPrefix+".ttl", claude.DefaultCacheControlTTL); err == nil {
			body = next
		}
		return body
	}
	raw := fmt.Sprintf(`{"type":"ephemeral","ttl":%q}`, claude.DefaultCacheControlTTL)
	if next, err := sjson.SetRawBytes(body, pathPrefix, []byte(raw)); err == nil {
		body = next
	}
	return body
}

// mustJSONString 把一个 Go string 序列化为合法 JSON string（含引号），
// 用于 sjson.SetRawBytes 场景下手工拼 JSON。
func mustJSONString(s string) string {
	return fmt.Sprintf("%q", s)
}

// hasAnyCacheControl 判断请求体是否已经带有任何 cache_control 断点。
//
// 用途：区分"自带完整缓存断点的真实 Claude Code 客户端"与"伪装成 Claude Code
// 但不打断点的中继（如 kiro-go）"。前者代理不应再动 body；后者必须由代理补
// 上断点，否则 Anthropic prompt caching 不会命中，成本是 cache_read 的 10 倍。
//
// 检测路径覆盖 Anthropic 文档允许放 cache_control 的所有位置：
//   - $.cache_control                    （极少见，但 TTL override 用）
//   - $.system.cache_control             （system 为 object 时）
//   - $.system[*].cache_control          （system 为 array 时）
//   - $.messages[*].content[*].cache_control
//   - $.messages[*].content.cache_control（防御性：content 为 object 的异常形态）
//   - $.tools[*].cache_control
//
// 返回 true 只要命中任意一处。为了性能，先用 bytes.Contains 做纯字节预筛：
// 绝大多数 body 根本不含 "cache_control" 字符串，直接返回 false，避免 gjson
// 解析开销。
func hasAnyCacheControl(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if !bytes.Contains(body, []byte(`"cache_control"`)) {
		return false
	}

	if ccExists(gjson.GetBytes(body, "cache_control")) {
		return true
	}
	if ccExists(gjson.GetBytes(body, "system.cache_control")) {
		return true
	}

	sys := gjson.GetBytes(body, "system")
	if sys.IsArray() {
		hit := false
		sys.ForEach(func(_, block gjson.Result) bool {
			if ccExists(block.Get("cache_control")) {
				hit = true
				return false
			}
			return true
		})
		if hit {
			return true
		}
	}

	msgs := gjson.GetBytes(body, "messages")
	if msgs.IsArray() {
		hit := false
		msgs.ForEach(func(_, msg gjson.Result) bool {
			content := msg.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, block gjson.Result) bool {
					if ccExists(block.Get("cache_control")) {
						hit = true
						return false
					}
					return true
				})
			} else if content.IsObject() && ccExists(content.Get("cache_control")) {
				hit = true
			}
			return !hit
		})
		if hit {
			return true
		}
	}

	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		hit := false
		tools.ForEach(func(_, tool gjson.Result) bool {
			if ccExists(tool.Get("cache_control")) {
				hit = true
				return false
			}
			return true
		})
		if hit {
			return true
		}
	}

	return false
}

// ccExists 只接受真正存在且非 null 的 cache_control 对象，避免 gjson 对
// 不存在路径返回零值带来的误判。
func ccExists(r gjson.Result) bool {
	return r.Exists() && r.Raw != "" && r.Raw != "null"
}

// ensureCacheBreakpointsIfMissing 在"账号不走 OAuth mimicry 分支、但 body
// 里没有任何 cache_control 断点"的情况下，代理帮客户端补上标准断点，使
// Anthropic prompt caching 正常命中。
//
// 调用场景：
//   - APIKey 账号（shouldMimicClaudeCode 为 false）
//   - OAuth 账号 + 真实 Claude Code 客户端自己忘打断点（极少见但无副作用）
//   - 任意"伪装成 Claude Code 但不打断点的中继"（如 kiro-go）
//
// 副作用：
//   - 仅在 hasAnyCacheControl(body) == false 时动作，保证幂等
//   - 与 mimicry 分支行为一致：strip → breakpoints → tool rewrite 或 tools[-1]
//   - 工具名 rewrite 结果会写入 gin.Context，供响应侧反向还原
//
// 不做的事：
//   - 不重写 system prompt、不注入 metadata.user_id（那是 OAuth mimicry 专属）
//   - 不检查账号类型（调用方负责判断，这里只关心 body 是否已有断点）
func (s *GatewayService) ensureCacheBreakpointsIfMissing(c *gin.Context, body []byte) []byte {
	if len(body) == 0 || hasAnyCacheControl(body) {
		return body
	}
	body = stripMessageCacheControl(body) // 幂等保险：极端情况下客户端只给 tools 打了但前面还有残留
	body = addMessageCacheBreakpoints(body)
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
