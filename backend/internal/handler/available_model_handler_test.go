//go:build unit

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUserAvailableModel_Unauthenticated401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AvailableModelHandler{} // nil services — 401 路径不会调用它们
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/models/available", nil)

	h.List(c)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestUserAvailableModel_FieldWhitelist(t *testing.T) {
	row := userAvailableModelSection{
		Group: userAvailableModelGroup{
			ID:               1,
			Name:             "default",
			Platform:         service.PlatformOpenAI,
			SubscriptionType: "standard",
			RateMultiplier:   1,
			IsExclusive:      false,
		},
		Models: []userAvailableModel{
			{Name: "gpt-5.4", Platform: service.PlatformOpenAI, DisplayName: "GPT-5.4"},
		},
	}

	raw, err := json.Marshal(row)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	for _, key := range []string{"group", "models"} {
		_, exists := decoded[key]
		require.Truef(t, exists, "available model section must expose %q", key)
	}
	for _, key := range []string{"channel", "pricing", "status", "billing_model_source"} {
		_, exists := decoded[key]
		require.Falsef(t, exists, "available model section must not expose %q", key)
	}
}

func TestDefaultModelIDsForPlatform_FallsBackByPlatform(t *testing.T) {
	require.Contains(t, defaultModelIDsForPlatform(service.PlatformOpenAI), "gpt-5.4")
	require.Contains(t, defaultModelIDsForPlatform(service.PlatformGemini), "gemini-2.5-pro")
	require.Contains(t, defaultModelIDsForPlatform(service.PlatformAnthropic), "claude-sonnet-4-5-20250929")
	require.Contains(t, defaultModelIDsForPlatform(service.PlatformAntigravity), "gemini-2.5-flash")
}

func TestToUserAvailableModels_DedupesAndSorts(t *testing.T) {
	models := toUserAvailableModels(service.PlatformOpenAI, []string{"gpt-5.4", "", "gpt-5.4-mini", "gpt-5.4"})

	require.Equal(t, []userAvailableModel{
		{Name: "gpt-5.4", Platform: service.PlatformOpenAI, DisplayName: "GPT-5.4"},
		{Name: "gpt-5.4-mini", Platform: service.PlatformOpenAI, DisplayName: "GPT-5.4 Mini"},
	}, models)
}
