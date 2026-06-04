package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha3"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/imroc/req/v3"
	"github.com/tidwall/gjson"
)

const (
	openAIChatGPTConversationInitURL    = "https://chatgpt.com/backend-api/conversation/init"
	openAIChatGPTConversationURL        = "https://chatgpt.com/backend-api/f/conversation"
	openAIChatGPTConversationPrepareURL = "https://chatgpt.com/backend-api/f/conversation/prepare"
	openAIChatGPTChatRequirementsURL    = "https://chatgpt.com/backend-api/sentinel/chat-requirements"

	openAIImageRequirementsDiff = "0fffff"
	openAIImageLifecycleTimeout = 2 * time.Minute
)

func shouldUseOpenAIImagesWeb2API(account *Account, parsed *OpenAIImagesRequest) bool {
	if account == nil || parsed == nil || !account.IsOpenAIOAuth() {
		return false
	}
	if err := validateOpenAIImagesWeb2APIRequest(parsed); err != nil {
		return false
	}
	transport := normalizeOpenAIImagesTransport(account.GetExtraString("openai_images_transport"))
	if transport == "" {
		transport = normalizeOpenAIImagesTransport(account.GetCredential("openai_images_transport"))
	}
	switch transport {
	case "web2api":
		return true
	case "responses":
		return false
	}
	return isOpenAIFreePlan(account)
}

func normalizeOpenAIImagesTransport(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "web2api", "web", "web-api", "web_api", "chatgpt", "chatgpt-web", "chatgpt_web":
		return "web2api"
	case "responses", "codex", "codex-responses", "codex_responses", "image_generation", "native":
		return "responses"
	default:
		return ""
	}
}

func validateOpenAIImagesWeb2APIRequest(parsed *OpenAIImagesRequest) error {
	unsupported := unsupportedOpenAIImagesWeb2APIParams(parsed)
	if len(unsupported) == 0 {
		return nil
	}
	return &OpenAIImagesUnsupportedParamsError{
		Mode:        "basic_web2api",
		Unsupported: unsupported,
	}
}

func unsupportedOpenAIImagesWeb2APIParams(parsed *OpenAIImagesRequest) []string {
	if parsed == nil {
		return []string{"request"}
	}
	unsupported := make([]string, 0, 8)
	if parsed.Endpoint != openAIImagesGenerationsEndpoint {
		unsupported = append(unsupported, "edits")
	}
	if parsed.ExplicitModel {
		unsupported = append(unsupported, "model")
	}
	if parsed.ExplicitSize {
		unsupported = append(unsupported, "size")
	}
	if parsed.Stream {
		unsupported = append(unsupported, "stream")
	}
	if parsed.N != 1 {
		unsupported = append(unsupported, "n")
	}
	if parsed.ResponseFormat != "" && parsed.ResponseFormat != "b64_json" {
		unsupported = append(unsupported, "response_format")
	}
	if parsed.Quality != "" {
		unsupported = append(unsupported, "quality")
	}
	if parsed.Background != "" {
		unsupported = append(unsupported, "background")
	}
	if parsed.OutputFormat != "" {
		unsupported = append(unsupported, "output_format")
	}
	if parsed.Moderation != "" {
		unsupported = append(unsupported, "moderation")
	}
	if parsed.InputFidelity != "" {
		unsupported = append(unsupported, "input_fidelity")
	}
	if parsed.Style != "" {
		unsupported = append(unsupported, "style")
	}
	if parsed.OutputCompression != nil {
		unsupported = append(unsupported, "output_compression")
	}
	if parsed.PartialImages != nil {
		unsupported = append(unsupported, "partial_images")
	}
	if parsed.HasMask || parsed.MaskImageURL != "" || parsed.MaskUpload != nil {
		unsupported = append(unsupported, "mask")
	}
	if len(parsed.InputImageURLs) > 0 {
		unsupported = append(unsupported, "input_image_urls")
	}
	return unsupported
}

func isOpenAIFreePlan(account *Account) bool {
	if account == nil || !account.IsOpenAIOAuth() {
		return false
	}
	plan := strings.ToLower(strings.TrimSpace(account.GetCredential("plan_type")))
	plan = strings.ReplaceAll(plan, "-", "_")
	plan = strings.ReplaceAll(plan, " ", "_")
	switch plan {
	case "free", "chatgpt_free", "openai_free", "free_tier", "free_plan":
		return true
	default:
		return false
	}
}

func (s *OpenAIGatewayService) forwardOpenAIImagesOAuthWeb2API(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *OpenAIImagesRequest,
	requestModel string,
) (*OpenAIForwardResult, error) {
	if err := validateOpenAIImagesWeb2APIRequest(parsed); err != nil {
		return nil, err
	}
	startTime := time.Now()
	if requestModel == "" {
		requestModel = openAIImagesDefaultModel
	}
	logger.LegacyPrintf(
		"service.openai_gateway",
		"[OpenAI] Images request routing request_model=%s endpoint=%s account_type=%s transport=web2api plan_type=%s uploads=%d",
		requestModel,
		parsed.Endpoint,
		account.Type,
		strings.TrimSpace(account.GetCredential("plan_type")),
		len(parsed.Uploads),
	)
	if parsed.N > 1 {
		logger.LegacyPrintf(
			"service.openai_gateway",
			"[Warning] ChatGPT web2api image request requested n=%d; falling back to n=1 request_model=%s endpoint=%s",
			parsed.N,
			requestModel,
			parsed.Endpoint,
		)
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	client, err := newOpenAIBackendAPIClient(resolveOpenAIProxyURL(account))
	if err != nil {
		return nil, err
	}
	headers, err := s.buildOpenAIBackendAPIHeaders(account, token)
	if err != nil {
		return nil, err
	}
	if bootstrapErr := bootstrapOpenAIBackendAPI(ctx, client, headers); bootstrapErr != nil {
		logger.LegacyPrintf("service.openai_gateway", "OpenAI image web2api bootstrap failed: %v", bootstrapErr)
	}

	chatReqs, err := fetchOpenAIChatRequirements(ctx, client, headers)
	if err != nil {
		return nil, s.wrapOpenAIImageBackendError(ctx, c, account, err)
	}
	if chatReqs.Arkose.Required {
		return nil, s.wrapOpenAIImageBackendError(
			ctx,
			c,
			account,
			newOpenAIImageSyntheticStatusError(
				http.StatusForbidden,
				"chat-requirements requires unsupported challenge (arkose)",
				openAIChatGPTChatRequirementsURL,
			),
		)
	}

	parentMessageID := uuid.NewString()
	proofToken := generateOpenAIProofToken(chatReqs.ProofOfWork.Required, chatReqs.ProofOfWork.Seed, chatReqs.ProofOfWork.Difficulty, headers.Get("User-Agent"))
	_ = initializeOpenAIImageConversation(ctx, client, headers)
	conduitToken, err := prepareOpenAIImageConversation(ctx, client, headers, parsed.Prompt, parentMessageID, chatReqs.Token, proofToken)
	if err != nil {
		return nil, s.wrapOpenAIImageBackendError(ctx, c, account, err)
	}

	uploads, err := uploadOpenAIImageFiles(ctx, client, headers, parsed.Uploads)
	if err != nil {
		return nil, s.wrapOpenAIImageBackendError(ctx, c, account, err)
	}

	convReq := buildOpenAIImageConversationRequest(parsed, parentMessageID, uploads)
	convHeaders := cloneHTTPHeader(headers)
	convHeaders.Set("Accept", "text/event-stream")
	convHeaders.Set("Content-Type", "application/json")
	convHeaders.Set("openai-sentinel-chat-requirements-token", chatReqs.Token)
	if conduitToken != "" {
		convHeaders.Set("x-conduit-token", conduitToken)
	}
	if proofToken != "" {
		convHeaders.Set("openai-sentinel-proof-token", proofToken)
	}

	upstreamStart := time.Now()
	resp, err := client.R().
		SetContext(ctx).
		DisableAutoReadResponse().
		SetHeaders(headerToMap(convHeaders)).
		SetBodyJsonMarshal(convReq).
		Post(openAIChatGPTConversationURL)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("openai image web2api conversation request failed: %w", err)
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	if resp.StatusCode >= 400 {
		return nil, s.wrapOpenAIImageBackendError(ctx, c, account, handleOpenAIImageBackendError(resp))
	}

	conversationID, pointerInfos, usage, firstTokenMs, err := readOpenAIImageConversationStream(resp, startTime)
	if err != nil {
		return nil, err
	}
	pointerInfos = mergeOpenAIImagePointerInfos(pointerInfos, nil)
	logger.LegacyPrintf(
		"service.openai_gateway",
		"[OpenAI] Image web2api extraction stream conversation_id=%s total_assets=%d file_service_assets=%d direct_assets=%d",
		conversationID,
		len(pointerInfos),
		countOpenAIFileServicePointerInfos(pointerInfos),
		countOpenAIDirectImageAssets(pointerInfos),
	)
	lifecycleCtx, releaseLifecycleCtx := detachOpenAIImageLifecycleContext(ctx, openAIImageLifecycleTimeout)
	defer releaseLifecycleCtx()
	if conversationID != "" && !hasOpenAIFileServicePointerInfos(pointerInfos) {
		polledPointers, pollErr := pollOpenAIImageConversation(lifecycleCtx, client, headers, conversationID)
		if pollErr != nil {
			return nil, s.wrapOpenAIImageBackendError(ctx, c, account, pollErr)
		}
		pointerInfos = mergeOpenAIImagePointerInfos(pointerInfos, polledPointers)
	}
	pointerInfos = preferOpenAIFileServicePointerInfos(pointerInfos)
	if len(pointerInfos) == 0 {
		logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Image web2api extraction yielded no assets conversation_id=%s", conversationID)
		return nil, fmt.Errorf("openai image web2api conversation returned no downloadable images")
	}

	responseBody, imageCount, err := buildOpenAIImageResponse(lifecycleCtx, client, headers, conversationID, pointerInfos)
	if err != nil {
		return nil, s.wrapOpenAIImageBackendError(ctx, c, account, err)
	}

	c.Data(http.StatusOK, "application/json; charset=utf-8", responseBody)
	responseHeaders := http.Header(nil)
	if resp.Response != nil {
		responseHeaders = resp.Header.Clone()
	}
	return &OpenAIForwardResult{
		RequestID:       resp.Header.Get("x-request-id"),
		Usage:           usage,
		Model:           requestModel,
		UpstreamModel:   requestModel,
		Stream:          false,
		ResponseHeaders: responseHeaders,
		Duration:        time.Since(startTime),
		FirstTokenMs:    firstTokenMs,
		ImageCount:      imageCount,
		ImageSize:       parsed.SizeTier,
	}, nil
}

func resolveOpenAIProxyURL(account *Account) string {
	if account != nil && account.ProxyID != nil && account.Proxy != nil {
		return account.Proxy.URL()
	}
	return ""
}

func newOpenAIBackendAPIClient(proxyURL string) (*req.Client, error) {
	client := req.C().
		SetTimeout(180 * time.Second).
		ImpersonateChrome()
	trimmed, _, err := proxyurl.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	if trimmed != "" {
		client.SetProxyURL(trimmed)
	}
	return client, nil
}

func (s *OpenAIGatewayService) buildOpenAIBackendAPIHeaders(account *Account, token string) (http.Header, error) {
	deviceID, sessionID := s.ensureOpenAIImageSessionCredentials(context.Background(), account)
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+token)
	headers.Set("Accept", "application/json")
	headers.Set("Origin", "https://chatgpt.com")
	headers.Set("Referer", "https://chatgpt.com/")
	headers.Set("Sec-Fetch-Dest", "empty")
	headers.Set("Sec-Fetch-Mode", "cors")
	headers.Set("Sec-Fetch-Site", "same-origin")
	headers.Set("User-Agent", openAIImageBackendUserAgent)
	if customUA := strings.TrimSpace(account.GetOpenAIUserAgent()); customUA != "" {
		headers.Set("User-Agent", customUA)
	}
	if chatgptAccountID := strings.TrimSpace(account.GetChatGPTAccountID()); chatgptAccountID != "" {
		headers.Set("chatgpt-account-id", chatgptAccountID)
	}
	if deviceID != "" {
		headers.Set("oai-device-id", deviceID)
		headers.Set("Cookie", "oai-did="+deviceID)
	}
	if sessionID != "" {
		headers.Set("oai-session-id", sessionID)
	}
	return headers, nil
}

func (s *OpenAIGatewayService) ensureOpenAIImageSessionCredentials(ctx context.Context, account *Account) (string, string) {
	if account == nil {
		return "", ""
	}
	deviceID := account.GetOpenAIDeviceID()
	sessionID := account.GetOpenAISessionID()
	if deviceID != "" && sessionID != "" {
		return deviceID, sessionID
	}

	updates := map[string]any{}
	if deviceID == "" {
		deviceID = uuid.NewString()
		updates["openai_device_id"] = deviceID
	}
	if sessionID == "" {
		sessionID = uuid.NewString()
		updates["openai_session_id"] = sessionID
	}
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	for key, value := range updates {
		account.Extra[key] = value
	}
	if len(updates) == 0 || s == nil || s.accountRepo == nil {
		return deviceID, sessionID
	}

	updateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.accountRepo.UpdateExtra(updateCtx, account.ID, updates); err != nil {
		logger.LegacyPrintf("service.openai_gateway", "persist openai image session creds failed: account=%d err=%v", account.ID, err)
	}
	return deviceID, sessionID
}

func bootstrapOpenAIBackendAPI(ctx context.Context, client *req.Client, headers http.Header) error {
	resp, err := client.R().
		SetContext(ctx).
		DisableAutoReadResponse().
		SetHeaders(headerToMap(headers)).
		Get(openAIChatGPTStartURL)
	if err != nil {
		return err
	}
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	return nil
}

func initializeOpenAIImageConversation(ctx context.Context, client *req.Client, headers http.Header) error {
	payload := map[string]any{
		"gizmo_id":                nil,
		"requested_default_model": nil,
		"conversation_id":         nil,
		"timezone_offset_min":     openAITimezoneOffsetMinutes(),
		"system_hints":            []string{"picture_v2"},
	}
	resp, err := client.R().
		SetContext(ctx).
		SetHeaders(headerToMap(headers)).
		SetBodyJsonMarshal(payload).
		Post(openAIChatGPTConversationInitURL)
	if err != nil {
		return err
	}
	if !resp.IsSuccessState() {
		return newOpenAIImageStatusError(resp, "conversation init failed", openAIUpstreamErrorBodyReadLimit)
	}
	return nil
}

type openAIChatRequirements struct {
	Token     string `json:"token"`
	Turnstile struct {
		Required bool `json:"required"`
	} `json:"turnstile"`
	Arkose struct {
		Required bool `json:"required"`
	} `json:"arkose"`
	ProofOfWork struct {
		Required   bool   `json:"required"`
		Seed       string `json:"seed"`
		Difficulty string `json:"difficulty"`
	} `json:"proofofwork"`
}

func fetchOpenAIChatRequirements(ctx context.Context, client *req.Client, headers http.Header) (*openAIChatRequirements, error) {
	var lastErr error
	for _, payload := range []map[string]any{
		{"p": nil},
		{"p": generateOpenAIRequirementsToken(headers.Get("User-Agent"))},
	} {
		var result openAIChatRequirements
		resp, err := client.R().
			SetContext(ctx).
			SetHeaders(headerToMap(headers)).
			SetBodyJsonMarshal(payload).
			SetSuccessResult(&result).
			Post(openAIChatGPTChatRequirementsURL)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.IsSuccessState() && strings.TrimSpace(result.Token) != "" {
			return &result, nil
		}
		lastErr = newOpenAIImageStatusError(resp, "chat-requirements failed", openAIUpstreamErrorBodyReadLimit)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("chat-requirements failed")
	}
	return nil, lastErr
}

func prepareOpenAIImageConversation(
	ctx context.Context,
	client *req.Client,
	headers http.Header,
	prompt string,
	parentMessageID string,
	chatToken string,
	proofToken string,
) (string, error) {
	messageID := uuid.NewString()
	payload := map[string]any{
		"action":                "next",
		"client_prepare_state":  "success",
		"fork_from_shared_post": false,
		"parent_message_id":     parentMessageID,
		"model":                 "auto",
		"timezone_offset_min":   openAITimezoneOffsetMinutes(),
		"timezone":              openAITimezoneName(),
		"conversation_mode":     map[string]any{"kind": "primary_assistant"},
		"system_hints":          []string{"picture_v2"},
		"supports_buffering":    true,
		"supported_encodings":   []string{"v1"},
		"partial_query": map[string]any{
			"id":     messageID,
			"author": map[string]any{"role": "user"},
			"content": map[string]any{
				"content_type": "text",
				"parts":        []string{coalesceOpenAIFileName(prompt, "Generate an image.")},
			},
		},
		"client_contextual_info": map[string]any{
			"app_name": "chatgpt.com",
		},
	}
	prepareHeaders := cloneHTTPHeader(headers)
	prepareHeaders.Set("Accept", "*/*")
	prepareHeaders.Set("Content-Type", "application/json")
	if strings.TrimSpace(chatToken) != "" {
		prepareHeaders.Set("openai-sentinel-chat-requirements-token", strings.TrimSpace(chatToken))
	}
	if strings.TrimSpace(proofToken) != "" {
		prepareHeaders.Set("openai-sentinel-proof-token", strings.TrimSpace(proofToken))
	}
	var result struct {
		ConduitToken string `json:"conduit_token"`
	}
	resp, err := client.R().
		SetContext(ctx).
		SetHeaders(headerToMap(prepareHeaders)).
		SetBodyJsonMarshal(payload).
		SetSuccessResult(&result).
		Post(openAIChatGPTConversationPrepareURL)
	if err != nil {
		return "", err
	}
	if !resp.IsSuccessState() {
		return "", newOpenAIImageStatusError(resp, "conversation prepare failed", openAIUpstreamErrorBodyReadLimit)
	}
	return strings.TrimSpace(result.ConduitToken), nil
}

type openAIUploadedImage struct {
	FileID   string
	FileName string
	FileSize int
	MimeType string
	Width    int
	Height   int
}

func uploadOpenAIImageFiles(ctx context.Context, client *req.Client, headers http.Header, uploads []OpenAIImagesUpload) ([]openAIUploadedImage, error) {
	if len(uploads) == 0 {
		return nil, nil
	}
	results := make([]openAIUploadedImage, 0, len(uploads))
	for i := range uploads {
		item := uploads[i]
		fileName := coalesceOpenAIFileName(item.FileName, "image.png")
		payload := map[string]any{
			"file_name": fileName,
			"file_size": len(item.Data),
			"use_case":  "multimodal",
		}
		var created struct {
			FileID    string `json:"file_id"`
			UploadURL string `json:"upload_url"`
		}
		resp, err := client.R().
			SetContext(ctx).
			SetHeaders(headerToMap(headers)).
			SetBodyJsonMarshal(payload).
			SetSuccessResult(&created).
			Post(openAIChatGPTFilesURL)
		if err != nil {
			return nil, err
		}
		if !resp.IsSuccessState() || strings.TrimSpace(created.FileID) == "" || strings.TrimSpace(created.UploadURL) == "" {
			return nil, newOpenAIImageStatusError(resp, "create upload slot failed", openAIUpstreamErrorBodyReadLimit)
		}

		uploadHeaders := map[string]string{
			"Content-Type":   coalesceOpenAIFileName(item.ContentType, "application/octet-stream"),
			"Origin":         "https://chatgpt.com",
			"x-ms-blob-type": "BlockBlob",
			"x-ms-version":   "2020-04-08",
			"User-Agent":     headers.Get("User-Agent"),
		}
		putResp, err := client.R().
			SetContext(ctx).
			SetHeaders(uploadHeaders).
			SetBody(item.Data).
			DisableAutoReadResponse().
			Put(created.UploadURL)
		if err != nil {
			return nil, err
		}
		if putResp.Response != nil && putResp.Body != nil {
			_, _ = io.Copy(io.Discard, putResp.Body)
			_ = putResp.Body.Close()
		}
		if putResp.StatusCode < 200 || putResp.StatusCode >= 300 {
			return nil, newOpenAIImageStatusError(putResp, "upload image bytes failed", openAIUpstreamErrorBodyReadLimit)
		}

		uploadedResp, err := client.R().
			SetContext(ctx).
			SetHeaders(headerToMap(headers)).
			SetBodyJsonMarshal(map[string]any{}).
			Post(fmt.Sprintf("%s/%s/uploaded", openAIChatGPTFilesURL, created.FileID))
		if err != nil {
			return nil, err
		}
		if !uploadedResp.IsSuccessState() {
			return nil, newOpenAIImageStatusError(uploadedResp, "mark upload complete failed", openAIUpstreamErrorBodyReadLimit)
		}

		results = append(results, openAIUploadedImage{
			FileID:   created.FileID,
			FileName: fileName,
			FileSize: len(item.Data),
			MimeType: coalesceOpenAIFileName(item.ContentType, "application/octet-stream"),
			Width:    item.Width,
			Height:   item.Height,
		})
	}
	return results, nil
}

func coalesceOpenAIFileName(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func buildOpenAIImageConversationRequest(parsed *OpenAIImagesRequest, parentMessageID string, uploads []openAIUploadedImage) map[string]any {
	parts := []any{coalesceOpenAIFileName(parsed.Prompt, "Generate an image.")}
	attachments := make([]map[string]any, 0, len(uploads))
	if len(uploads) > 0 {
		parts = make([]any, 0, len(uploads)+1)
		for _, upload := range uploads {
			parts = append(parts, map[string]any{
				"content_type":  "image_asset_pointer",
				"asset_pointer": "file-service://" + upload.FileID,
				"size_bytes":    upload.FileSize,
				"width":         upload.Width,
				"height":        upload.Height,
			})
			attachment := map[string]any{
				"id":       upload.FileID,
				"mimeType": upload.MimeType,
				"name":     upload.FileName,
				"size":     upload.FileSize,
			}
			if upload.Width > 0 {
				attachment["width"] = upload.Width
			}
			if upload.Height > 0 {
				attachment["height"] = upload.Height
			}
			attachments = append(attachments, attachment)
		}
		parts = append(parts, coalesceOpenAIFileName(parsed.Prompt, "Edit this image."))
	}

	contentType := "text"
	if len(uploads) > 0 {
		contentType = "multimodal_text"
	}
	metadata := map[string]any{
		"developer_mode_connector_ids": []any{},
		"selected_github_repos":        []any{},
		"selected_all_github_repos":    false,
		"system_hints":                 []string{"picture_v2"},
		"serialization_metadata": map[string]any{
			"custom_symbol_offsets": []any{},
		},
	}
	message := map[string]any{
		"id":     uuid.NewString(),
		"author": map[string]any{"role": "user"},
		"content": map[string]any{
			"content_type": contentType,
			"parts":        parts,
		},
		"metadata":    metadata,
		"create_time": float64(time.Now().UnixMilli()) / 1000,
	}
	if len(attachments) > 0 {
		metadata["attachments"] = attachments
	}

	return map[string]any{
		"action":                               "next",
		"client_prepare_state":                 "sent",
		"parent_message_id":                    parentMessageID,
		"model":                                "auto",
		"timezone_offset_min":                  openAITimezoneOffsetMinutes(),
		"timezone":                             openAITimezoneName(),
		"conversation_mode":                    map[string]any{"kind": "primary_assistant"},
		"enable_message_followups":             true,
		"system_hints":                         []string{"picture_v2"},
		"supports_buffering":                   true,
		"supported_encodings":                  []string{"v1"},
		"paragen_cot_summary_display_override": "allow",
		"force_parallel_switch":                "auto",
		"client_contextual_info": map[string]any{
			"is_dark_mode":      false,
			"time_since_loaded": 200,
			"page_height":       900,
			"page_width":        1440,
			"pixel_ratio":       1,
			"screen_height":     1080,
			"screen_width":      1920,
			"app_name":          "chatgpt.com",
		},
		"messages": []any{message},
	}
}

type openAIImageToolMessage struct {
	MessageID    string
	CreateTime   float64
	PointerInfos []openAIImagePointerInfo
}

func readOpenAIImageConversationStream(resp *req.Response, startTime time.Time) (string, []openAIImagePointerInfo, OpenAIUsage, *int, error) {
	if resp == nil || resp.Body == nil {
		return "", nil, OpenAIUsage{}, nil, fmt.Errorf("empty conversation response")
	}
	reader := bufio.NewReader(resp.Body)
	var (
		conversationID string
		firstTokenMs   *int
		usage          OpenAIUsage
		pointers       []openAIImagePointerInfo
	)

	for {
		line, err := reader.ReadString('\n')
		if strings.TrimSpace(line) != "" && firstTokenMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		if data, ok := extractOpenAISSEDataLine(strings.TrimRight(line, "\r\n")); ok && data != "" && data != "[DONE]" {
			dataBytes := []byte(data)
			if conversationID == "" {
				conversationID = strings.TrimSpace(gjson.GetBytes(dataBytes, "v.conversation_id").String())
				if conversationID == "" {
					conversationID = strings.TrimSpace(gjson.GetBytes(dataBytes, "conversation_id").String())
				}
			}
			mergeOpenAIUsage(&usage, dataBytes)
			pointers = mergeOpenAIImagePointerInfos(pointers, collectOpenAIImagePointers(dataBytes))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, OpenAIUsage{}, firstTokenMs, err
		}
	}
	return conversationID, pointers, usage, firstTokenMs, nil
}

func hasOpenAIFileServicePointerInfos(items []openAIImagePointerInfo) bool {
	for _, item := range items {
		if strings.HasPrefix(item.Pointer, "file-service://") {
			return true
		}
	}
	return false
}

func countOpenAIFileServicePointerInfos(items []openAIImagePointerInfo) int {
	count := 0
	for _, item := range items {
		if strings.HasPrefix(item.Pointer, "file-service://") {
			count++
		}
	}
	return count
}

func countOpenAIDirectImageAssets(items []openAIImagePointerInfo) int {
	count := 0
	for _, item := range items {
		if strings.TrimSpace(item.DownloadURL) != "" || strings.TrimSpace(item.B64JSON) != "" {
			count++
		}
	}
	return count
}

func preferOpenAIFileServicePointerInfos(items []openAIImagePointerInfo) []openAIImagePointerInfo {
	if !hasOpenAIFileServicePointerInfos(items) {
		return items
	}
	out := make([]openAIImagePointerInfo, 0, len(items))
	for _, item := range items {
		if strings.HasPrefix(item.Pointer, "file-service://") {
			out = append(out, item)
		}
	}
	return out
}

func extractOpenAIImageToolMessages(mapping map[string]any) []openAIImageToolMessage {
	if len(mapping) == 0 {
		return nil
	}
	out := make([]openAIImageToolMessage, 0, 4)
	for messageID, raw := range mapping {
		node, _ := raw.(map[string]any)
		if node == nil {
			continue
		}
		message, _ := node["message"].(map[string]any)
		if message == nil {
			continue
		}
		author, _ := message["author"].(map[string]any)
		metadata, _ := message["metadata"].(map[string]any)
		content, _ := message["content"].(map[string]any)
		if author == nil || metadata == nil || content == nil {
			continue
		}
		if role, _ := author["role"].(string); role != "tool" {
			continue
		}
		if asyncTaskType, _ := metadata["async_task_type"].(string); asyncTaskType != "image_gen" {
			continue
		}
		if contentType, _ := content["content_type"].(string); contentType != "multimodal_text" {
			continue
		}
		prompt := ""
		if title, _ := metadata["image_gen_title"].(string); strings.TrimSpace(title) != "" {
			prompt = strings.TrimSpace(title)
		}
		item := openAIImageToolMessage{MessageID: messageID}
		if createTime, ok := message["create_time"].(float64); ok {
			item.CreateTime = createTime
		}
		parts, _ := content["parts"].([]any)
		for _, part := range parts {
			switch value := part.(type) {
			case map[string]any:
				if assetPointer, _ := value["asset_pointer"].(string); strings.TrimSpace(assetPointer) != "" {
					for _, pointer := range openAIImagePointerMatches([]byte(assetPointer)) {
						item.PointerInfos = append(item.PointerInfos, openAIImagePointerInfo{
							Pointer: pointer,
							Prompt:  prompt,
						})
					}
				}
			case string:
				for _, pointer := range openAIImagePointerMatches([]byte(value)) {
					item.PointerInfos = append(item.PointerInfos, openAIImagePointerInfo{
						Pointer: pointer,
						Prompt:  prompt,
					})
				}
			}
		}
		if len(item.PointerInfos) == 0 {
			continue
		}
		item.PointerInfos = mergeOpenAIImagePointerInfos(nil, item.PointerInfos)
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreateTime < out[j].CreateTime
	})
	return out
}

func pollOpenAIImageConversation(ctx context.Context, client *req.Client, headers http.Header, conversationID string) ([]openAIImagePointerInfo, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, nil
	}
	deadline := time.Now().Add(90 * time.Second)
	interval := 3 * time.Second
	previewWait := 15 * time.Second
	var (
		lastErr     error
		firstToolAt time.Time
	)
	for time.Now().Before(deadline) {
		resp, err := client.R().
			SetContext(ctx).
			SetHeaders(headerToMap(headers)).
			DisableAutoReadResponse().
			Get(fmt.Sprintf("https://chatgpt.com/backend-api/conversation/%s", conversationID))
		if err != nil {
			lastErr = err
		} else {
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				body, readErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if readErr != nil {
					lastErr = readErr
					goto waitNextPoll
				}
				pointers := mergeOpenAIImagePointerInfos(nil, collectOpenAIImagePointers(body))
				var decoded map[string]any
				if err := json.Unmarshal(body, &decoded); err == nil {
					if mapping, _ := decoded["mapping"].(map[string]any); len(mapping) > 0 {
						toolMessages := extractOpenAIImageToolMessages(mapping)
						if len(toolMessages) > 0 && firstToolAt.IsZero() {
							firstToolAt = time.Now()
						}
						for _, msg := range toolMessages {
							pointers = mergeOpenAIImagePointerInfos(pointers, msg.PointerInfos)
						}
					}
				}
				if hasOpenAIFileServicePointerInfos(pointers) {
					return preferOpenAIFileServicePointerInfos(pointers), nil
				}
				if len(pointers) > 0 && !firstToolAt.IsZero() && time.Since(firstToolAt) >= previewWait {
					return pointers, nil
				}
			} else {
				statusErr := newOpenAIImageStatusError(resp, "conversation poll failed", openAIUpstreamErrorBodyReadLimit)
				if isOpenAIImageTransientConversationNotFoundError(statusErr) {
					lastErr = statusErr
					goto waitNextPoll
				}
				return nil, statusErr
			}
		}

	waitNextPoll:
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func buildOpenAIImageResponse(
	ctx context.Context,
	client *req.Client,
	headers http.Header,
	conversationID string,
	pointers []openAIImagePointerInfo,
) ([]byte, int, error) {
	type responseItem struct {
		B64JSON       string `json:"b64_json"`
		RevisedPrompt string `json:"revised_prompt,omitempty"`
	}
	items := make([]responseItem, 0, len(pointers))
	for _, pointer := range pointers {
		data, err := resolveOpenAIImageBytes(ctx, client, headers, conversationID, pointer)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, responseItem{
			B64JSON:       base64.StdEncoding.EncodeToString(data),
			RevisedPrompt: pointer.Prompt,
		})
	}
	payload := map[string]any{
		"created": time.Now().Unix(),
		"data":    items,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	return body, len(items), nil
}

func detachOpenAIImageLifecycleContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	if timeout <= 0 {
		return base, func() {}
	}
	return context.WithTimeout(base, timeout)
}

func handleOpenAIImageBackendError(resp *req.Response) error {
	return newOpenAIImageStatusError(resp, "backend-api request failed", openAIUpstreamErrorBodyReadLimit)
}

func newOpenAIImageSyntheticStatusError(statusCode int, message string, requestURL string) *openAIImageStatusError {
	message = sanitizeUpstreamErrorMessage(strings.TrimSpace(message))
	if message == "" {
		message = "openai image backend request failed"
	}
	var body []byte
	if payload, err := json.Marshal(map[string]string{"detail": message}); err == nil {
		body = payload
	}
	return &openAIImageStatusError{
		StatusCode:   statusCode,
		Message:      message,
		ResponseBody: body,
		URL:          strings.TrimSpace(requestURL),
	}
}

func (s *OpenAIGatewayService) wrapOpenAIImageBackendError(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	err error,
) error {
	var statusErr *openAIImageStatusError
	if !errors.As(err, &statusErr) || statusErr == nil {
		return err
	}

	upstreamMsg := sanitizeUpstreamErrorMessage(statusErr.Message)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: statusErr.StatusCode,
		UpstreamRequestID:  statusErr.RequestID,
		UpstreamURL:        safeUpstreamURL(statusErr.URL),
		Kind:               "request_error",
		Message:            upstreamMsg,
	})
	setOpsUpstreamError(c, statusErr.StatusCode, upstreamMsg, "")

	if shouldFailoverOpenAIImagesOAuthResponse(statusErr.StatusCode, upstreamMsg, statusErr.ResponseBody) || s.shouldFailoverOpenAIUpstreamResponse(statusErr.StatusCode, upstreamMsg, statusErr.ResponseBody) {
		if s.rateLimitService != nil {
			s.rateLimitService.HandleUpstreamError(ctx, account, statusErr.StatusCode, statusErr.ResponseHeaders, statusErr.ResponseBody)
		}
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: statusErr.StatusCode,
			UpstreamRequestID:  statusErr.RequestID,
			UpstreamURL:        safeUpstreamURL(statusErr.URL),
			Kind:               "failover",
			Message:            upstreamMsg,
		})
		retryableOnSameAccount := account.IsPoolMode() && isPoolModeRetryableStatus(statusErr.StatusCode)
		if strings.Contains(strings.ToLower(statusErr.Message), "unsupported challenge") {
			retryableOnSameAccount = false
		}
		return &UpstreamFailoverError{
			StatusCode:             statusErr.StatusCode,
			ResponseBody:           statusErr.ResponseBody,
			RetryableOnSameAccount: retryableOnSameAccount,
		}
	}

	return statusErr
}

func openAITimezoneOffsetMinutes() int {
	_, offset := time.Now().Zone()
	return offset / 60
}

func openAITimezoneName() string {
	return time.Now().Location().String()
}

func generateOpenAIRequirementsToken(userAgent string) string {
	config := []any{
		"core" + strconv.Itoa(3008),
		time.Now().UTC().Format(time.RFC1123),
		nil,
		0.123456,
		coalesceOpenAIFileName(strings.TrimSpace(userAgent), openAIImageBackendUserAgent),
		nil,
		"prod-openai-images",
		"en-US",
		"en-US,en",
		0,
		"navigator.webdriver",
		"location",
		"document.body",
		float64(time.Now().UnixMilli()) / 1000,
		uuid.NewString(),
		"",
		8,
		time.Now().Unix(),
	}
	answer, solved := generateOpenAIChallengeAnswer(strconv.FormatInt(time.Now().UnixNano(), 10), openAIImageRequirementsDiff, config)
	if solved {
		return "gAAAAAC" + answer
	}
	return ""
}

func generateOpenAIChallengeAnswer(seed string, difficulty string, config []any) (string, bool) {
	diffBytes, err := hex.DecodeString(difficulty)
	if err != nil {
		return "", false
	}
	p1 := []byte(jsonCompactSlice(config[:3], true))
	p2 := []byte(jsonCompactSlice(config[4:9], false))
	p3 := []byte(jsonCompactSlice(config[10:], false))
	seedBytes := []byte(seed)

	for i := 0; i < 100000; i++ {
		payload := fmt.Sprintf("%s%d,%s,%d,%s", p1, i, p2, i>>1, p3)
		encoded := base64.StdEncoding.EncodeToString([]byte(payload))
		sum := sha3.Sum512(append(seedBytes, []byte(encoded)...))
		if bytes.Compare(sum[:len(diffBytes)], diffBytes) <= 0 {
			return encoded, true
		}
	}
	return "", false
}

func jsonCompactSlice(values []any, trimSuffixComma bool) string {
	raw, _ := json.Marshal(values)
	text := string(raw)
	if trimSuffixComma {
		return strings.TrimSuffix(text, "]")
	}
	return strings.TrimPrefix(text, "[")
}

func generateOpenAIProofToken(required bool, seed string, difficulty string, userAgent string) string {
	if !required || strings.TrimSpace(seed) == "" || strings.TrimSpace(difficulty) == "" {
		return ""
	}
	screen := 3008
	if len(seed)%2 == 0 {
		screen = 4010
	}
	proofToken := []any{
		screen,
		time.Now().UTC().Format(time.RFC1123),
		nil,
		0,
		coalesceOpenAIFileName(strings.TrimSpace(userAgent), openAIImageBackendUserAgent),
		"https://chatgpt.com/",
		"dpl=openai-images",
		"en",
		"en-US",
		nil,
		"plugins[object PluginArray]",
		"_reactListening",
		"alert",
	}
	diffLen := len(difficulty)
	for i := 0; i < 100000; i++ {
		proofToken[3] = i
		raw, _ := json.Marshal(proofToken)
		encoded := base64.StdEncoding.EncodeToString(raw)
		sum := sha3.Sum512([]byte(seed + encoded))
		if strings.Compare(hex.EncodeToString(sum[:])[:diffLen], difficulty) <= 0 {
			return "gAAAAAB" + encoded
		}
	}
	fallbackBase := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%q", seed)))
	return "gAAAAABwQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D" + fallbackBase
}
