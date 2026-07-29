package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Native-upstream Kiro accounts bill by token; credits_per_dollar only applies
// to legacy kiro-rs proxy accounts (base_url passthrough).

func kiroAccount(creds map[string]any, creditsPerDollar float64) *Account {
	a := &Account{
		ID:          1,
		Type:        AccountTypeKiro,
		Platform:    PlatformAnthropic,
		Credentials: creds,
	}
	if creditsPerDollar > 0 {
		a.Extra = map[string]any{"credits_per_dollar": creditsPerDollar}
	}
	return a
}

func TestAccount_UsesNativeKiroUpstream(t *testing.T) {
	t.Run("refresh token is native", func(t *testing.T) {
		require.True(t, kiroAccount(map[string]any{"refresh_token": "rt"}, 0).UsesNativeKiroUpstream())
	})
	t.Run("api key is native", func(t *testing.T) {
		require.True(t, kiroAccount(map[string]any{"kiro_api_key": "ksk_x"}, 0).UsesNativeKiroUpstream())
	})
	t.Run("base_url only is legacy proxy", func(t *testing.T) {
		require.False(t, kiroAccount(map[string]any{"base_url": "http://127.0.0.1:8080"}, 0).UsesNativeKiroUpstream())
	})
	t.Run("non-kiro account is never native kiro", func(t *testing.T) {
		a := &Account{ID: 1, Type: AccountTypeAPIKey, Platform: PlatformAnthropic}
		require.False(t, a.UsesNativeKiroUpstream())
	})
}

func TestAccount_IsCreditsBasedBilling_NativeUsesTokenBilling(t *testing.T) {
	t.Run("native account ignores credits_per_dollar", func(t *testing.T) {
		a := kiroAccount(map[string]any{"refresh_token": "rt"}, 50)
		require.Equal(t, 50.0, a.GetCreditsPerDollar(), "value stays readable for admin cost reference")
		require.False(t, a.IsCreditsBasedBilling(), "native upstream must bill by token")
	})

	t.Run("legacy proxy account keeps credits billing", func(t *testing.T) {
		a := kiroAccount(map[string]any{"base_url": "http://127.0.0.1:8080"}, 50)
		require.True(t, a.IsCreditsBasedBilling())
	})

	t.Run("legacy proxy without credits_per_dollar bills by token", func(t *testing.T) {
		a := kiroAccount(map[string]any{"base_url": "http://127.0.0.1:8080"}, 0)
		require.False(t, a.IsCreditsBasedBilling())
	})

	t.Run("non-kiro account never uses credits billing", func(t *testing.T) {
		a := &Account{ID: 1, Type: AccountTypeAPIKey, Platform: PlatformAnthropic,
			Extra: map[string]any{"credits_per_dollar": 50.0}}
		require.False(t, a.IsCreditsBasedBilling())
	})
}
