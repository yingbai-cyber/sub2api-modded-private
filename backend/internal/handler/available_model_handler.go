package handler

import (
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// AvailableModelHandler 处理用户侧「可用模型」查询。
//
// 与 AvailableChannelHandler 不同，这里不依赖管理员额外维护的「可用渠道」展示目录，
// 而是基于当前用户可访问分组以及这些分组下可调度账号的 model_mapping 推导模型。
type AvailableModelHandler struct {
	apiKeyService  *service.APIKeyService
	gatewayService *service.GatewayService
	settingService *service.SettingService
}

// NewAvailableModelHandler 创建用户侧可用模型 handler。
func NewAvailableModelHandler(
	apiKeyService *service.APIKeyService,
	gatewayService *service.GatewayService,
	settingService *service.SettingService,
) *AvailableModelHandler {
	return &AvailableModelHandler{
		apiKeyService:  apiKeyService,
		gatewayService: gatewayService,
		settingService: settingService,
	}
}

type userAvailableModelGroup struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	Platform         string  `json:"platform"`
	SubscriptionType string  `json:"subscription_type"`
	RateMultiplier   float64 `json:"rate_multiplier"`
	IsExclusive      bool    `json:"is_exclusive"`
}

type userAvailableModel struct {
	Name        string `json:"name"`
	Platform    string `json:"platform"`
	DisplayName string `json:"display_name"`
}

type userAvailableModelSection struct {
	Group  userAvailableModelGroup `json:"group"`
	Models []userAvailableModel    `json:"models"`
}

func (h *AvailableModelHandler) featureEnabled(c *gin.Context) bool {
	if h.settingService == nil {
		return true
	}
	return h.settingService.GetAvailableModelsRuntime(c.Request.Context()).Enabled
}

// List 列出当前用户可访问分组下可支配的模型。
// GET /api/v1/models/available
func (h *AvailableModelHandler) List(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	if !h.featureEnabled(c) {
		response.Success(c, []userAvailableModelSection{})
		return
	}

	if h.apiKeyService == nil || h.gatewayService == nil {
		response.Success(c, []userAvailableModelSection{})
		return
	}

	groups, err := h.apiKeyService.GetAvailableGroups(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	sections := make([]userAvailableModelSection, 0, len(groups))
	for i := range groups {
		g := groups[i]
		platform := strings.TrimSpace(g.Platform)
		if platform == "" {
			continue
		}
		models, useDefaultFallback, err := h.gatewayService.GetAvailableModelsForDiscovery(c.Request.Context(), &g.ID, platform)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		if len(models) == 0 && useDefaultFallback {
			models = defaultModelIDsForPlatform(platform)
		}
		modelItems := toUserAvailableModels(platform, models)
		if len(modelItems) == 0 {
			continue
		}
		sections = append(sections, userAvailableModelSection{
			Group: userAvailableModelGroup{
				ID:               g.ID,
				Name:             g.Name,
				Platform:         g.Platform,
				SubscriptionType: g.SubscriptionType,
				RateMultiplier:   g.RateMultiplier,
				IsExclusive:      g.IsExclusive,
			},
			Models: modelItems,
		})
	}

	sort.SliceStable(sections, func(i, j int) bool {
		if sections[i].Group.Platform != sections[j].Group.Platform {
			return sections[i].Group.Platform < sections[j].Group.Platform
		}
		return strings.ToLower(sections[i].Group.Name) < strings.ToLower(sections[j].Group.Name)
	})

	response.Success(c, sections)
}

func toUserAvailableModels(platform string, modelIDs []string) []userAvailableModel {
	seen := make(map[string]struct{}, len(modelIDs))
	out := make([]userAvailableModel, 0, len(modelIDs))
	for _, raw := range modelIDs {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, userAvailableModel{
			Name:        name,
			Platform:    platform,
			DisplayName: displayNameForModel(platform, name),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func defaultModelIDsForPlatform(platform string) []string {
	switch platform {
	case service.PlatformOpenAI:
		ids := make([]string, 0, len(openai.DefaultModels))
		for _, m := range openai.DefaultModels {
			ids = append(ids, m.ID)
		}
		return ids
	case service.PlatformGemini:
		ids := make([]string, 0, len(geminicli.DefaultModels))
		for _, m := range geminicli.DefaultModels {
			ids = append(ids, m.ID)
		}
		return ids
	case service.PlatformAntigravity:
		models := antigravity.DefaultModels()
		ids := make([]string, 0, len(models))
		for _, m := range models {
			ids = append(ids, m.ID)
		}
		return ids
	default:
		ids := make([]string, 0, len(claude.DefaultModels))
		for _, m := range claude.DefaultModels {
			ids = append(ids, m.ID)
		}
		return ids
	}
}

func displayNameForModel(platform, name string) string {
	switch platform {
	case service.PlatformOpenAI:
		for _, m := range openai.DefaultModels {
			if m.ID == name && m.DisplayName != "" {
				return m.DisplayName
			}
		}
	case service.PlatformGemini:
		for _, m := range geminicli.DefaultModels {
			if m.ID == name && m.DisplayName != "" {
				return m.DisplayName
			}
		}
	case service.PlatformAntigravity:
		for _, m := range antigravity.DefaultModels() {
			if m.ID == name && m.DisplayName != "" {
				return m.DisplayName
			}
		}
	default:
		for _, m := range claude.DefaultModels {
			if m.ID == name && m.DisplayName != "" {
				return m.DisplayName
			}
		}
	}
	return name
}
