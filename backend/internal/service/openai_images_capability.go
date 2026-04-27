package service

import (
	"context"
	"fmt"
	"strings"
)

const openAIImagesDefaultModel = "gpt-image-2"

type OpenAIImagesUnsupportedParamsError struct {
	Mode        string
	Unsupported []string
}

func (e *OpenAIImagesUnsupportedParamsError) Error() string {
	if e == nil {
		return "unsupported image parameters"
	}
	mode := strings.TrimSpace(e.Mode)
	if mode == "" {
		mode = "current image mode"
	}
	if len(e.Unsupported) == 0 {
		return fmt.Sprintf("%s does not support the requested image parameters", mode)
	}
	return fmt.Sprintf("%s does not support parameters: %s", mode, strings.Join(e.Unsupported, ", "))
}

type OpenAIImagesCapabilityInfo struct {
	Available             bool           `json:"available"`
	UIMode                string         `json:"ui_mode"`
	ImageMode             string         `json:"image_mode"`
	Transport             string         `json:"transport"`
	Model                 string         `json:"model"`
	SupportsBasic         bool           `json:"supports_basic"`
	SupportsAdvanced      bool           `json:"supports_advanced_options"`
	SupportsStream        bool           `json:"supports_stream"`
	SupportsExactSize     bool           `json:"supports_exact_size"`
	SupportsCustomSize    bool           `json:"supports_custom_size"`
	SupportsQuality       bool           `json:"supports_quality"`
	SupportsOutputFormat  bool           `json:"supports_output_format"`
	SupportsPartialImages bool           `json:"supports_partial_images"`
	SupportsEdits         bool           `json:"supports_edits"`
	SupportsInputImages   bool           `json:"supports_input_images"`
	SupportsUploads       bool           `json:"supports_uploads"`
	MaxN                  int            `json:"max_n"`
	UnsupportedParams     []string       `json:"unsupported_params,omitempty"`
	AccountCounts         map[string]int `json:"account_counts,omitempty"`
	Warnings              []string       `json:"warnings,omitempty"`
}

func (s *OpenAIGatewayService) GetOpenAIImagesCapabilityForAPIKey(ctx context.Context, apiKey *APIKey) (*OpenAIImagesCapabilityInfo, error) {
	info := &OpenAIImagesCapabilityInfo{
		UIMode:        "basic",
		ImageMode:     "none",
		Transport:     "none",
		Model:         openAIImagesDefaultModel,
		MaxN:          1,
		AccountCounts: map[string]int{},
	}
	if apiKey == nil || apiKey.Group == nil || apiKey.Group.Platform != PlatformOpenAI {
		info.Warnings = append(info.Warnings, "api key is not assigned to an OpenAI group")
		return info, nil
	}

	accounts, err := s.listSchedulableAccounts(ctx, apiKey.GroupID)
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		account := &accounts[i]
		if account == nil || !account.IsOpenAI() || !account.IsModelSupported(openAIImagesDefaultModel) {
			continue
		}
		if account.SupportsOpenAIImageCapability(OpenAIImagesCapabilityBasic) {
			info.SupportsBasic = true
			info.AccountCounts["basic"]++
			if isOpenAIImagesWeb2APIOnlyAccount(account) {
				info.SupportsInputImages = true
				info.SupportsUploads = true
			}
		}
		if account.SupportsOpenAIImageCapability(OpenAIImagesCapabilityNative) {
			info.SupportsAdvanced = true
			info.AccountCounts["advanced"]++
			switch account.Type {
			case AccountTypeAPIKey:
				info.AccountCounts["api_key"]++
			default:
				info.AccountCounts["responses"]++
			}
			continue
		}
		if isOpenAIImagesWeb2APIOnlyAccount(account) {
			info.AccountCounts["web2api"]++
		}
	}

	info.Available = info.SupportsBasic || info.SupportsAdvanced
	if info.SupportsAdvanced {
		info.UIMode = "advanced"
		info.ImageMode = "advanced_responses"
		info.Transport = "responses"
		info.SupportsStream = true
		info.SupportsExactSize = true
		info.SupportsCustomSize = true
		info.SupportsQuality = true
		info.SupportsOutputFormat = true
		info.SupportsPartialImages = true
		info.SupportsEdits = true
		info.SupportsInputImages = true
		info.SupportsUploads = true
		return info, nil
	}
	if info.SupportsBasic {
		info.UIMode = "basic"
		info.ImageMode = "basic_web2api"
		info.Transport = "web2api"
		info.UnsupportedParams = BasicOpenAIImagesUnsupportedParams()
		return info, nil
	}
	info.Warnings = append(info.Warnings, "no schedulable OpenAI image accounts are available")
	return info, nil
}

func isOpenAIImagesWeb2APIOnlyAccount(account *Account) bool {
	if account == nil || !account.IsOpenAIOAuth() {
		return false
	}
	if transport := normalizeOpenAIImagesTransport(account.GetExtraString("openai_images_transport")); transport != "" {
		return transport == "web2api"
	}
	if transport := normalizeOpenAIImagesTransport(account.GetCredential("openai_images_transport")); transport != "" {
		return transport == "web2api"
	}
	return isOpenAIFreePlan(account)
}

func BasicOpenAIImagesUnsupportedParams() []string {
	return []string{
		"model",
		"size",
		"quality",
		"output_format",
		"background",
		"moderation",
		"output_compression",
		"stream",
		"partial_images",
		"n",
		"response_format:url",
		"edits",
		"mask",
		"input_image_urls",
	}
}
