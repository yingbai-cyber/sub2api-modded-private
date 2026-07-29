package service

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
)

// KiroBalanceInfo is the computed kiro account balance for the frontend.
type KiroBalanceInfo struct {
	SubscriptionTitle string  `json:"subscription_title"`
	CurrentUsage      float64 `json:"current_usage"`
	UsageLimit        float64 `json:"usage_limit"`
	Remaining         float64 `json:"remaining"`
	UsagePercentage   float64 `json:"usage_percentage"`
	NextResetAt       float64 `json:"next_reset_at,omitempty"`
}

// getKiroUsage fetches usage limits from Kiro upstream for native accounts.
func (s *AccountUsageService) getKiroUsage(ctx context.Context, account *Account) (*UsageInfo, error) {
	if s.kiroTokenProvider == nil {
		return &UsageInfo{Error: "kiro token provider not configured"}, nil
	}

	cred, token, err := s.kiroTokenProvider.Resolve(ctx, account)
	if err != nil {
		return &UsageInfo{Error: fmt.Sprintf("resolve token: %v", err)}, nil
	}

	// Route through the account proxy to keep the source IP consistent with the
	// account's streaming requests (upstream anti-abuse correlates IP + token).
	// Fail closed: if a proxy is configured but the client can't be built, do NOT
	// fall back to a direct connection.
	var client *http.Client
	if proxyURL := resolveAccountProxyURL(account); proxyURL != "" {
		client, err = httpclient.GetClient(httpclient.Options{
			ProxyURL: proxyURL,
			Timeout:  60 * time.Second,
		})
		if err != nil {
			return &UsageInfo{Error: fmt.Sprintf("build proxy client: %v", err)}, nil
		}
	}

	cfg := kiro.DefaultConfig()

	resp, err := kiro.GetUsageLimits(ctx, client, cred, token, cfg)
	if err != nil {
		return &UsageInfo{Error: fmt.Sprintf("getUsageLimits: %v", err)}, nil
	}

	bal := resp.ComputeBalance()
	now := time.Now()
	return &UsageInfo{
		Source:    "active",
		UpdatedAt: &now,
		KiroBalance: &KiroBalanceInfo{
			SubscriptionTitle: bal.SubscriptionTitle,
			CurrentUsage:      bal.CurrentUsage,
			UsageLimit:        bal.UsageLimit,
			Remaining:         bal.Remaining,
			UsagePercentage:   bal.UsagePercentage,
			NextResetAt:       bal.NextResetAt,
		},
	}, nil
}
