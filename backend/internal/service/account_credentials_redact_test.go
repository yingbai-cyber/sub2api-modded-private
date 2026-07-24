//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergePreservingSensitiveCreds_PreservesSensitiveWhenIncomingMissing(t *testing.T) {
	existing := map[string]any{
		"refresh_token": "rt-old",
		"access_token":  "at-old",
		"api_key":       "sk-old",
		"base_url":      "https://old.example.com",
	}
	incoming := map[string]any{
		"base_url":      "https://new.example.com",
		"model_mapping": map[string]any{"foo": "bar"},
	}

	out := MergePreservingSensitiveCreds(existing, incoming)

	require.Equal(t, "rt-old", out["refresh_token"], "incoming 没传 refresh_token，应保留 existing")
	require.Equal(t, "at-old", out["access_token"])
	require.Equal(t, "sk-old", out["api_key"])
	require.Equal(t, "https://new.example.com", out["base_url"], "非敏感键由 incoming 决定")
	require.Equal(t, map[string]any{"foo": "bar"}, out["model_mapping"])
}

func TestMergePreservingSensitiveCreds_OverwritesWhenIncomingProvidesSensitive(t *testing.T) {
	existing := map[string]any{
		"refresh_token": "rt-old",
		"api_key":       "sk-old",
	}
	incoming := map[string]any{
		"refresh_token": "rt-new",
		// 显式没传 api_key —— 应保留
	}
	out := MergePreservingSensitiveCreds(existing, incoming)
	require.Equal(t, "rt-new", out["refresh_token"], "incoming 显式传入应覆盖")
	require.Equal(t, "sk-old", out["api_key"], "incoming 没传应保留")
}

func TestMergePreservingSensitiveCreds_DoesNotMutateInputs(t *testing.T) {
	existing := map[string]any{"refresh_token": "rt"}
	incoming := map[string]any{"base_url": "x"}

	_ = MergePreservingSensitiveCreds(existing, incoming)

	require.Equal(t, "rt", existing["refresh_token"])
	require.NotContains(t, existing, "base_url")
	require.Equal(t, "x", incoming["base_url"])
	require.NotContains(t, incoming, "refresh_token")
}

func TestMergePreservingSensitiveCreds_NilInputs(t *testing.T) {
	out := MergePreservingSensitiveCreds(nil, map[string]any{"base_url": "x"})
	require.Equal(t, "x", out["base_url"])
	require.NotContains(t, out, "refresh_token")

	out2 := MergePreservingSensitiveCreds(map[string]any{"refresh_token": "rt"}, nil)
	require.Equal(t, "rt", out2["refresh_token"])
}

func TestMergePreservingSensitiveCreds_NonSensitiveDeletionAllowed(t *testing.T) {
	existing := map[string]any{
		"refresh_token": "rt",
		"base_url":      "https://old",
		"project_id":    "p1",
	}
	incoming := map[string]any{
		"base_url": "https://new",
		// 不带 project_id —— 等同删除（非敏感键由 incoming 决定）
	}
	out := MergePreservingSensitiveCreds(existing, incoming)
	require.Equal(t, "rt", out["refresh_token"], "敏感键保留")
	require.Equal(t, "https://new", out["base_url"])
	require.NotContains(t, out, "project_id", "非敏感键 incoming 不传 = 删除")
}

func TestIsSensitiveCredentialKey(t *testing.T) {
	require.True(t, IsSensitiveCredentialKey("refresh_token"))
	require.True(t, IsSensitiveCredentialKey("api_key"))
	require.True(t, IsSensitiveCredentialKey("private_key"))
	require.False(t, IsSensitiveCredentialKey("base_url"))
	require.False(t, IsSensitiveCredentialKey(""))
	require.False(t, IsSensitiveCredentialKey("model_mapping"))
}

// Kiro 原生凭证的敏感子键必须被识别，否则 GET 会明文泄漏、Edit 会清空。
func TestIsSensitiveCredentialKey_KiroNative(t *testing.T) {
	require.True(t, IsSensitiveCredentialKey("kiro_api_key"), "ksk_* 直连密钥必须脱敏")
	require.True(t, IsSensitiveCredentialKey("client_secret"), "IdC client_secret 必须脱敏")
	// 非敏感的原生配置字段应保持可见（可编辑/可预填）。
	require.False(t, IsSensitiveCredentialKey("client_id"))
	require.False(t, IsSensitiveCredentialKey("token_endpoint"))
	require.False(t, IsSensitiveCredentialKey("issuer_url"))
	require.False(t, IsSensitiveCredentialKey("auth_method"))
	require.False(t, IsSensitiveCredentialKey("endpoint"))
}

// Edit 全对象 PUT 场景：前端脱敏响应不带回 kiro_api_key/client_secret，
// 合并时必须保留 existing，而非清空。
func TestMergePreservingSensitiveCreds_PreservesKiroNativeSecrets(t *testing.T) {
	existing := map[string]any{
		"kiro_api_key":  "ksk_old",
		"client_secret": "cs-old",
		"client_id":     "cid-old",
		"auth_method":   "idc",
		"base_url":      "",
	}
	incoming := map[string]any{
		// 前端 native 编辑：秘密留空 => 不带回；仅改非敏感字段。
		"auth_method": "idc",
		"client_id":   "cid-new",
	}
	out := MergePreservingSensitiveCreds(existing, incoming)
	require.Equal(t, "ksk_old", out["kiro_api_key"], "秘密未提供应保留")
	require.Equal(t, "cs-old", out["client_secret"], "秘密未提供应保留")
	require.Equal(t, "cid-new", out["client_id"], "非敏感字段由 incoming 决定")
	require.Equal(t, "idc", out["auth_method"])
}
