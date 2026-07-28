package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// ──────────────────────────────────────────────────────────────────────────────
// ListAvailableModels
// ──────────────────────────────────────────────────────────────────────────────

// UpstreamModelInfo mirrors kiro-rs KiroModelInfo from the ListAvailableModels API.
type UpstreamModelInfo struct {
	ModelID             string   `json:"modelId"`
	ModelName           string   `json:"modelName"`
	Description         string   `json:"description"`
	SupportedInputTypes []string `json:"supportedInputTypes"`
	RateMultiplier      float64  `json:"rateMultiplier"`
	TokenLimits         *struct {
		MaxInputTokens  int64 `json:"maxInputTokens"`
		MaxOutputTokens int64 `json:"maxOutputTokens"`
	} `json:"tokenLimits,omitempty"`
}

// ListModelsResponse is the response from ListAvailableModels.
type ListModelsResponse struct {
	Models []UpstreamModelInfo `json:"models"`
}

// ListAvailableModels calls the Kiro upstream ListAvailableModels API.
// URL: GET https://q.{region}.amazonaws.com/ListAvailableModels?origin=AI_EDITOR&maxResults=50
func ListAvailableModels(ctx context.Context, cred *Credentials, token string, cfg *Config) ([]UpstreamModelInfo, error) {
	region := cred.EffectiveAPIRegion(cfg)
	host := "q." + region + ".amazonaws.com"
	url := "https://" + host + "/ListAvailableModels?origin=AI_EDITOR&maxResults=50"

	machineID := GenerateMachineID(cred, "")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("kiro ListAvailableModels: build request: %w", err)
	}

	setIDEHeaders(req.Header, host, machineID, cfg, cred, token)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro ListAvailableModels: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("kiro ListAvailableModels: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result ListModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("kiro ListAvailableModels: decode: %w", err)
	}
	return result.Models, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// GetUsageLimits (余量查询)
// ──────────────────────────────────────────────────────────────────────────────

// UsageLimitsResponse mirrors kiro-rs usage_limits::UsageLimitsResponse.
type UsageLimitsResponse struct {
	NextDateReset      *float64           `json:"nextDateReset,omitempty"`
	SubscriptionInfo   *SubscriptionInfo  `json:"subscriptionInfo,omitempty"`
	UsageBreakdownList []UsageBreakdown   `json:"usageBreakdownList"`
}

// SubscriptionInfo holds the subscription tier.
type SubscriptionInfo struct {
	SubscriptionTitle *string `json:"subscriptionTitle,omitempty"`
}

// UsageBreakdown details a single usage category.
type UsageBreakdown struct {
	CurrentUsage              int64          `json:"currentUsage"`
	CurrentUsageWithPrecision float64        `json:"currentUsageWithPrecision"`
	UsageLimit                int64          `json:"usageLimit"`
	UsageLimitWithPrecision   float64        `json:"usageLimitWithPrecision"`
	NextDateReset             *float64       `json:"nextDateReset,omitempty"`
	Bonuses                   []UsageBonus   `json:"bonuses"`
	FreeTrialInfo             *FreeTrialInfo `json:"freeTrialInfo,omitempty"`
}

// UsageBonus is a bonus credit entry.
type UsageBonus struct {
	CurrentUsage float64 `json:"currentUsage"`
	UsageLimit   float64 `json:"usageLimit"`
	Status       *string `json:"status,omitempty"`
}

// FreeTrialInfo holds free trial details.
type FreeTrialInfo struct {
	CurrentUsage              int64    `json:"currentUsage"`
	CurrentUsageWithPrecision float64  `json:"currentUsageWithPrecision"`
	UsageLimit                int64    `json:"usageLimit"`
	UsageLimitWithPrecision   float64  `json:"usageLimitWithPrecision"`
	FreeTrialStatus           *string  `json:"freeTrialStatus,omitempty"`
}

// BalanceResult is the computed balance from UsageLimitsResponse.
type BalanceResult struct {
	SubscriptionTitle string  `json:"subscriptionTitle"`
	CurrentUsage      float64 `json:"currentUsage"`
	UsageLimit        float64 `json:"usageLimit"`
	Remaining         float64 `json:"remaining"`
	UsagePercentage   float64 `json:"usagePercentage"`
	NextResetAt       float64 `json:"nextResetAt,omitempty"`
}

// ComputeBalance calculates remaining/limit/percentage from raw usage limits.
func (r *UsageLimitsResponse) ComputeBalance() BalanceResult {
	var title string
	if r.SubscriptionInfo != nil && r.SubscriptionInfo.SubscriptionTitle != nil {
		title = *r.SubscriptionInfo.SubscriptionTitle
	}

	var usageLimit, currentUsage float64
	if len(r.UsageBreakdownList) > 0 {
		bd := r.UsageBreakdownList[0]
		usageLimit = bd.UsageLimitWithPrecision
		currentUsage = bd.CurrentUsageWithPrecision

		// Add active free trial
		if bd.FreeTrialInfo != nil && bd.FreeTrialInfo.FreeTrialStatus != nil && *bd.FreeTrialInfo.FreeTrialStatus == "ACTIVE" {
			usageLimit += bd.FreeTrialInfo.UsageLimitWithPrecision
			currentUsage += bd.FreeTrialInfo.CurrentUsageWithPrecision
		}

		// Add active bonuses
		for _, bonus := range bd.Bonuses {
			if bonus.Status != nil && *bonus.Status == "ACTIVE" {
				usageLimit += bonus.UsageLimit
				currentUsage += bonus.CurrentUsage
			}
		}
	}

	remaining := usageLimit - currentUsage
	if remaining < 0 {
		remaining = 0
	}
	var pct float64
	if usageLimit > 0 {
		pct = currentUsage / usageLimit * 100
		if pct > 100 {
			pct = 100
		}
	}

	var nextReset float64
	if r.NextDateReset != nil {
		nextReset = *r.NextDateReset
	} else if len(r.UsageBreakdownList) > 0 && r.UsageBreakdownList[0].NextDateReset != nil {
		nextReset = *r.UsageBreakdownList[0].NextDateReset
	}

	return BalanceResult{
		SubscriptionTitle: title,
		CurrentUsage:      currentUsage,
		UsageLimit:        usageLimit,
		Remaining:         remaining,
		UsagePercentage:   pct,
		NextResetAt:       nextReset,
	}
}

// GetUsageLimits calls the Kiro upstream getUsageLimits API.
// URL: GET https://q.{region}.amazonaws.com/getUsageLimits?origin=AI_EDITOR&resourceType=AGENTIC_REQUEST
func GetUsageLimits(ctx context.Context, cred *Credentials, token string, cfg *Config) (*UsageLimitsResponse, error) {
	region := cred.EffectiveAPIRegion(cfg)
	host := "q." + region + ".amazonaws.com"
	url := "https://" + host + "/getUsageLimits?origin=AI_EDITOR&resourceType=AGENTIC_REQUEST"

	machineID := GenerateMachineID(cred, "")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("kiro getUsageLimits: build request: %w", err)
	}

	setIDEHeaders(req.Header, host, machineID, cfg, cred, token)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro getUsageLimits: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("kiro getUsageLimits: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result UsageLimitsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("kiro getUsageLimits: decode: %w", err)
	}
	return &result, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Shared header helpers
// ──────────────────────────────────────────────────────────────────────────────

// setIDEHeaders sets the common IDE-style request headers for Kiro upstream calls.
func setIDEHeaders(h http.Header, host, machineID string, cfg *Config, cred *Credentials, token string) {
	kiroVer := cfg.kiroVersion()
	nodeVer := cfg.nodeVersion()
	sysVer := cfg.systemVersion()

	userAgent := "aws-sdk-js/1.0.34 ua/2.1 os/" + sysVer +
		" lang/js md/nodejs#" + nodeVer +
		" api/codewhispererstreaming#1.0.34 m/E KiroIDE-" +
		kiroVer + "-" + machineID
	amzUserAgent := "aws-sdk-js/1.0.34 KiroIDE-" + kiroVer + "-" + machineID

	h.Set("user-agent", userAgent)
	h.Set("x-amz-user-agent", amzUserAgent)
	h.Set("x-amzn-codewhisperer-optout", "true")
	h.Set("host", host)
	h.Set("amz-sdk-invocation-id", uuid.NewString())
	h.Set("amz-sdk-request", "attempt=1; max=1")
	h.Set("Authorization", "Bearer "+token)
	h.Set("Connection", "close")

	if cred.ProfileArn != "" {
		h.Set("x-amzn-kiro-profile-arn", cred.ProfileArn)
	}
	if tt := cred.TokenTypeHeader(); tt != "" {
		h.Set("TokenType", tt)
	}
}
