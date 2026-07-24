package dto

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactCredentials_NilInput(t *testing.T) {
	out, status := RedactCredentials(nil)
	require.Nil(t, out)
	require.Nil(t, status)
}

func TestRedactCredentials_StripsSensitiveKeysAndReportsStatus(t *testing.T) {
	in := map[string]any{
		"refresh_token":         "rt-secret",
		"access_token":          "at-secret",
		"api_key":               "sk-secret",
		"aws_secret_access_key": "aws-secret",
		"service_account_json":  map[string]any{"private_key": "..."},
		"private_key":           "raw-key",
		"agent_private_key":     "agent-key-secret",
		// 非敏感
		"base_url":      "https://api.example.com",
		"model_mapping": map[string]any{"foo": "bar"},
		"project_id":    "proj-1",
		"expires_at":    int64(123456),
	}

	out, status := RedactCredentials(in)

	require.NotContains(t, out, "refresh_token")
	require.NotContains(t, out, "access_token")
	require.NotContains(t, out, "api_key")
	require.NotContains(t, out, "aws_secret_access_key")
	require.NotContains(t, out, "service_account_json")
	require.NotContains(t, out, "private_key")
	require.NotContains(t, out, "agent_private_key")

	require.Equal(t, "https://api.example.com", out["base_url"])
	require.Equal(t, map[string]any{"foo": "bar"}, out["model_mapping"])
	require.Equal(t, "proj-1", out["project_id"])
	require.Equal(t, int64(123456), out["expires_at"])

	require.True(t, status["has_refresh_token"])
	require.True(t, status["has_access_token"])
	require.True(t, status["has_api_key"])
	require.True(t, status["has_aws_secret_access_key"])
	require.True(t, status["has_service_account_json"])
	require.True(t, status["has_private_key"])
	require.True(t, status["has_agent_private_key"])

	// 状态 map 不应携带非敏感键的 has_*
	require.NotContains(t, status, "has_base_url")
	require.NotContains(t, status, "has_project_id")
}

func TestRedactCredentials_EmptyValuesNotMarkedPresent(t *testing.T) {
	in := map[string]any{
		"refresh_token": "",
		"access_token":  nil,
		"api_key":       false,
		"id_token":      "actual-id",
	}
	out, status := RedactCredentials(in)
	require.Empty(t, out, "敏感键即使为空也不应出现在 redacted output")
	require.False(t, status["has_refresh_token"])
	require.False(t, status["has_access_token"])
	require.False(t, status["has_api_key"])
	require.True(t, status["has_id_token"])
}

func TestRedactCredentials_DoesNotMutateInput(t *testing.T) {
	in := map[string]any{
		"refresh_token": "secret",
		"base_url":      "x",
	}
	_, _ = RedactCredentials(in)
	require.Equal(t, "secret", in["refresh_token"], "原始 map 不应被修改")
	require.Equal(t, "x", in["base_url"])
}

func TestRedactCredentials_AllKnownSensitiveKeys(t *testing.T) {
	keys := []string{
		"access_token", "refresh_token", "id_token",
		"api_key", "session_key", "cookie",
		"aws_secret_access_key", "aws_session_token",
		"service_account_json", "service_account", "private_key",
		"agent_private_key",
		"kiro_api_key", "client_secret",
	}
	in := make(map[string]any, len(keys))
	for _, k := range keys {
		in[k] = "filled"
	}
	out, status := RedactCredentials(in)
	require.Empty(t, out)
	for _, k := range keys {
		require.True(t, status["has_"+k], "key %s 应在 status 中标记为已配置", k)
	}
}

// Kiro 原生凭证脱敏：kiro_api_key/client_secret 必须转成 has_* 状态而非明文返回；
// 非敏感的原生配置字段（auth_method/client_id/token_endpoint 等）必须保留以便 Edit 预填。
func TestRedactCredentials_KiroNative(t *testing.T) {
	in := map[string]any{
		"kiro_api_key":   "ksk_secret",
		"refresh_token":  "rt-secret",
		"client_secret":  "cs-secret",
		"auth_method":    "idc",
		"client_id":      "cid-1",
		"token_endpoint": "https://oidc.example.com/token",
		"issuer_url":     "https://issuer.example.com",
		"scopes":         "openid profile",
		"region":         "us-east-1",
		"endpoint":       "ide",
		"profile_arn":    "arn:aws:codewhisperer:...",
	}
	out, status := RedactCredentials(in)

	require.NotContains(t, out, "kiro_api_key")
	require.NotContains(t, out, "client_secret")
	require.NotContains(t, out, "refresh_token")
	require.True(t, status["has_kiro_api_key"])
	require.True(t, status["has_client_secret"])
	require.True(t, status["has_refresh_token"])

	// 非敏感原生字段保留（前端 Edit 预填）。
	require.Equal(t, "idc", out["auth_method"])
	require.Equal(t, "cid-1", out["client_id"])
	require.Equal(t, "https://oidc.example.com/token", out["token_endpoint"])
	require.Equal(t, "https://issuer.example.com", out["issuer_url"])
	require.Equal(t, "openid profile", out["scopes"])
	require.Equal(t, "us-east-1", out["region"])
	require.Equal(t, "ide", out["endpoint"])
	require.Equal(t, "arn:aws:codewhisperer:...", out["profile_arn"])
}
