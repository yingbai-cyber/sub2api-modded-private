package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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

// ListAvailableModels queries the Kiro REST API with the account's SSO region
// ordering. Pass a proxied client to preserve IP consistency with streaming.
func ListAvailableModels(ctx context.Context, client *http.Client, cred *Credentials, token string, cfg *Config) ([]UpstreamModelInfo, error) {
	var result ListModelsResponse
	if err := getREST(ctx, client, cred, token, cfg, "ListAvailableModels", "origin=AI_EDITOR", &result); err != nil {
		return nil, err
	}
	return result.Models, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// GetUsageLimits (余量查询)
// ──────────────────────────────────────────────────────────────────────────────

// UsageLimitsResponse mirrors kiro-rs usage_limits::UsageLimitsResponse.
type UsageLimitsResponse struct {
	NextDateReset      *float64          `json:"nextDateReset,omitempty"`
	SubscriptionInfo   *SubscriptionInfo `json:"subscriptionInfo,omitempty"`
	UsageBreakdownList []UsageBreakdown  `json:"usageBreakdownList"`
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
	CurrentUsage              int64   `json:"currentUsage"`
	CurrentUsageWithPrecision float64 `json:"currentUsageWithPrecision"`
	UsageLimit                int64   `json:"usageLimit"`
	UsageLimitWithPrecision   float64 `json:"usageLimitWithPrecision"`
	FreeTrialStatus           *string `json:"freeTrialStatus,omitempty"`
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

// GetUsageLimits queries the Kiro REST API, requesting email-related usage too.
// Pass a proxied client to preserve IP consistency with streaming.
func GetUsageLimits(ctx context.Context, client *http.Client, cred *Credentials, token string, cfg *Config) (*UsageLimitsResponse, error) {
	var result UsageLimitsResponse
	if err := getREST(ctx, client, cred, token, cfg, "getUsageLimits", "origin=AI_EDITOR&resourceType=AGENTIC_REQUEST&isEmailRequired=true", &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// getREST follows the kiro.rs v0.9.0 REST attempt order: real ARN, then no ARN,
// for each of the two supported regions. Only permission/ARN compatibility
// failures advance to the next candidate; transport and other HTTP errors do not.
func getREST(ctx context.Context, client *http.Client, cred *Credentials, token string, cfg *Config, endpoint, query string, result any) error {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	regions := [2]string{"us-east-1", "eu-central-1"}
	if strings.HasPrefix(cred.EffectiveAuthRegion(cfg), "eu-") {
		regions[0], regions[1] = regions[1], regions[0]
	}
	profileArn := cred.EffectiveProfileArn()
	machineID := GenerateMachineID(cred, "")

	var lastErr error
	arns := []string{""}
	if profileArn != "" {
		arns = []string{profileArn, ""}
	}
	for _, region := range regions {
		host := "q." + region + ".amazonaws.com"
		for _, arn := range arns {
			requestURL := "https://" + host + "/" + endpoint + "?" + query
			if arn != "" {
				requestURL += "&profileArn=" + url.QueryEscape(arn)
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
			if err != nil {
				return fmt.Errorf("kiro %s: build request: %w", endpoint, err)
			}
			setRESTHeaders(req.Header, host, machineID, cfg, cred, token)
			req.Host = host
			req.Close = true

			resp, err := client.Do(req)
			if err != nil {
				if resp != nil && resp.Body != nil {
					_ = resp.Body.Close()
				}
				return fmt.Errorf("kiro %s: request failed: %w", endpoint, err)
			}
			if resp.StatusCode == http.StatusOK {
				err = json.NewDecoder(resp.Body).Decode(result)
				_ = resp.Body.Close()
				if err != nil {
					return fmt.Errorf("kiro %s: decode: %w", endpoint, err)
				}
				return nil
			}
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			if readErr != nil {
				return fmt.Errorf("kiro %s: read HTTP %d: %w", endpoint, resp.StatusCode, readErr)
			}
			if resp.StatusCode == http.StatusForbidden ||
				(resp.StatusCode == http.StatusBadRequest && arn != "" &&
					(strings.Contains(string(body), "Improperly formed request") || strings.Contains(string(body), "Invalid profileArn"))) {
				// The next candidate uses no ARN in the same region, then the other region.
				lastErr = fmt.Errorf("kiro %s: HTTP %d: %s", endpoint, resp.StatusCode, string(body))
				continue
			}
			return fmt.Errorf("kiro %s: HTTP %d: %s", endpoint, resp.StatusCode, string(body))
		}
	}
	return lastErr
}

// setRESTHeaders matches kiro.rs v0.9.0 token_manager.rs for both REST GETs.
// The 0.9.2 usage fingerprint is intentionally independent of the IDE version.
func setRESTHeaders(h http.Header, host, machineID string, cfg *Config, cred *Credentials, token string) {
	const kiroVersion = "0.9.2"
	h.Set("User-Agent", "aws-sdk-js/1.0.0 ua/2.1 os/"+cfg.systemVersion()+
		" lang/js md/nodejs#"+cfg.nodeVersion()+
		" api/codewhispererruntime#1.0.0 m/N,E KiroIDE-"+kiroVersion+"-"+machineID)
	h.Set("x-amz-user-agent", "aws-sdk-js/1.0.0 KiroIDE-"+kiroVersion+"-"+machineID)
	h.Set("Host", host)
	h.Set("amz-sdk-invocation-id", uuid.NewString())
	h.Set("amz-sdk-request", "attempt=1; max=1")
	h.Set("Authorization", "Bearer "+token)
	h.Set("Connection", "close")
	if tt := cred.TokenTypeHeader(); tt != "" {
		h.Set("TokenType", tt)
	}
}
