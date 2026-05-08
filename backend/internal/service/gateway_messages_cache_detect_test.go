package service

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// ---------- hasAnyCacheControl ----------

func TestHasAnyCacheControl_EmptyBody(t *testing.T) {
	require.False(t, hasAnyCacheControl(nil))
	require.False(t, hasAnyCacheControl([]byte("")))
}

func TestHasAnyCacheControl_NoHit(t *testing.T) {
	// 纯文本 body 不含 cache_control 字符串，bytes.Contains 预筛直接返回 false
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
	require.False(t, hasAnyCacheControl(body))
}

func TestHasAnyCacheControl_SystemArrayHit(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`)
	require.True(t, hasAnyCacheControl(body))
}

func TestHasAnyCacheControl_SystemObjectHit(t *testing.T) {
	// 异常但合法形态：system 是对象
	body := []byte(`{"system":{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}}`)
	require.True(t, hasAnyCacheControl(body))
}

func TestHasAnyCacheControl_MessageContentArrayHit(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`)
	require.True(t, hasAnyCacheControl(body))
}

func TestHasAnyCacheControl_ToolHit(t *testing.T) {
	body := []byte(`{"tools":[{"name":"bash","input_schema":{},"cache_control":{"type":"ephemeral","ttl":"1h"}}]}`)
	require.True(t, hasAnyCacheControl(body))
}

func TestHasAnyCacheControl_StringMatchButNullValue(t *testing.T) {
	// 防御性：body 里有 "cache_control" 字符串但值是 null，不应算命中
	body := []byte(`{"system":[{"type":"text","text":"x","cache_control":null}]}`)
	require.False(t, hasAnyCacheControl(body))
}

func TestHasAnyCacheControl_StringInsideText(t *testing.T) {
	// 防御性：用户消息里文字提到 "cache_control"，不应算命中（字节预筛会命中，
	// 但结构化检测路径都走不到，最终返回 false）
	body := []byte(`{"messages":[{"role":"user","content":"please ignore cache_control in this sentence"}]}`)
	// 注意：content 是 string 没有 cache_control 字段，所以应该返回 false
	// 但是 bytes.Contains 会命中 "cache_control" 字串
	require.False(t, hasAnyCacheControl(body))
}

// ---------- ensureCacheBreakpointsIfMissing ----------

func TestEnsureCacheBreakpointsIfMissing_NoCCInjects(t *testing.T) {
	svc := &GatewayService{}
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)

	out := svc.ensureCacheBreakpointsIfMissing(c, body)

	// 最后一条 message 的最后一个 content block 上应有 cache_control
	require.Equal(t, "ephemeral", gjson.GetBytes(out, "messages.0.content.0.cache_control.type").String())
	require.Equal(t, "5m", gjson.GetBytes(out, "messages.0.content.0.cache_control.ttl").String())
}

func TestEnsureCacheBreakpointsIfMissing_AlreadyHasCC_NoOp(t *testing.T) {
	svc := &GatewayService{}
	// 模拟真实 Claude Code 客户端已自己打断点的场景
	body := []byte(`{"system":[{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)

	before := string(body)
	out := svc.ensureCacheBreakpointsIfMissing(c, body)

	// 幂等：body 完全不变（包括 messages 里原本没有的断点也不会被补上）
	require.Equal(t, before, string(out))
	require.False(t, gjson.GetBytes(out, "messages.0.content.0.cache_control").Exists())
}

func TestEnsureCacheBreakpointsIfMissing_EmptyBody(t *testing.T) {
	svc := &GatewayService{}
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)

	require.Nil(t, svc.ensureCacheBreakpointsIfMissing(c, nil))
	require.Len(t, svc.ensureCacheBreakpointsIfMissing(c, []byte{}), 0)
}

func TestEnsureCacheBreakpointsIfMissing_WithTools(t *testing.T) {
	svc := &GatewayService{}
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"tools":[{"name":"bash","input_schema":{}}]}`)

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)

	out := svc.ensureCacheBreakpointsIfMissing(c, body)

	// messages 打断点
	require.Equal(t, "ephemeral", gjson.GetBytes(out, "messages.0.content.0.cache_control.type").String())
	// tools[-1] 打断点
	require.Equal(t, "ephemeral", gjson.GetBytes(out, "tools.0.cache_control.type").String())
	require.Equal(t, "5m", gjson.GetBytes(out, "tools.0.cache_control.ttl").String())
}

func TestEnsureCacheBreakpointsIfMissing_NilContext(t *testing.T) {
	// count_tokens 这类场景 c 可能为 nil（虽然当前实际调用方都非 nil，
	// 保留防御性以便将来复用）
	svc := &GatewayService{}
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	out := svc.ensureCacheBreakpointsIfMissing(nil, body)

	require.Equal(t, "ephemeral", gjson.GetBytes(out, "messages.0.content.0.cache_control.type").String())
}
