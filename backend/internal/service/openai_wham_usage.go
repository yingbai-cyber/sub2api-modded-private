package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const openAIWhamUsageProbeTimeout = 15 * time.Second

func normalizeOpenAIPlanType(planType string) string {
	return strings.ToLower(strings.TrimSpace(planType))
}

func fetchOpenAIWhamUsageWithReqClient(ctx context.Context, clientFactory PrivacyClientFactory, accessToken, proxyURL, chatgptAccountID string) (*openAIWhamUsageResponse, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("access token is empty")
	}
	if clientFactory == nil {
		clientFactory = newOpenAIBackendAPIClient
	}

	reqCtx, cancel := context.WithTimeout(ctx, openAIWhamUsageProbeTimeout)
	defer cancel()

	client, err := clientFactory(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create wham usage client: %w", err)
	}

	var usage openAIWhamUsageResponse
	request := client.R().
		SetContext(reqCtx).
		SetHeader("Accept", "application/json").
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("OpenAI-Beta", "responses=experimental").
		SetHeader("Originator", "codex_cli_rs").
		SetHeader("Version", codexCLIVersion).
		SetHeader("User-Agent", codexCLIUserAgent).
		SetSuccessResult(&usage)
	if chatgptAccountID = strings.TrimSpace(chatgptAccountID); chatgptAccountID != "" {
		request.SetHeader("chatgpt-account-id", chatgptAccountID)
	}

	resp, err := request.Get(chatgptWhamUsageURL)
	if err != nil {
		return nil, fmt.Errorf("request wham usage: %w", err)
	}
	if !resp.IsSuccessState() {
		return nil, fmt.Errorf("wham usage returned status %d", resp.StatusCode)
	}
	if normalizeOpenAIPlanType(usage.PlanType) == "" {
		body, readErr := resp.ToBytes()
		if readErr != nil {
			return nil, fmt.Errorf("read wham usage response: %w", readErr)
		}
		if err := json.Unmarshal(body, &usage); err != nil {
			return nil, fmt.Errorf("parse wham usage response: %w", err)
		}
	}
	return &usage, nil
}

func persistOpenAIObservedPlanType(ctx context.Context, repo AccountRepository, account *Account, planType string, source string) {
	if repo == nil || account == nil || account.Platform != PlatformOpenAI {
		return
	}
	// Spark 影子账号恒不持凭据：即便观察到 plan_type，也不能把 plan_type 写进影子 credentials。
	// plan_type 由凭据 owner（母账号）在自己的请求 / wham usage 探测上维护。
	if account.IsCredentialShadow() {
		return
	}

	planType = normalizeOpenAIPlanType(planType)
	if planType == "" {
		return
	}

	current := strings.TrimSpace(account.GetCredential("plan_type"))
	if strings.EqualFold(current, planType) {
		return
	}

	if _, err := repo.BulkUpdate(ctx, []int64{account.ID}, AccountBulkUpdate{
		Credentials: map[string]any{"plan_type": planType},
	}); err != nil {
		slog.Warn("openai_plan_type_sync_failed", "account_id", account.ID, "source", source, "plan_type", planType, "error", err)
		return
	}

	if account.Credentials == nil {
		account.Credentials = make(map[string]any, 1)
	}
	account.Credentials["plan_type"] = planType
	slog.Info("openai_plan_type_synced", "account_id", account.ID, "source", source, "previous_plan_type", current, "plan_type", planType)
}
