package service

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIImagesCapabilityPrincipalScopesAreStableAndOpaque(t *testing.T) {
	svc := newOpenAIImagesCapabilityTestService()
	groupID := int64(501)

	apiKeyA := newOpenAIImagesCapabilityTestAPIKey(1001, 101, &groupID)
	apiKeyB := newOpenAIImagesCapabilityTestAPIKey(1002, 101, &groupID)
	apiKeyC := newOpenAIImagesCapabilityTestAPIKey(1003, 202, &groupID)

	infoA1, err := svc.GetOpenAIImagesCapabilityForAPIKey(context.Background(), apiKeyA)
	require.NoError(t, err)
	infoA2, err := svc.GetOpenAIImagesCapabilityForAPIKey(context.Background(), apiKeyA)
	require.NoError(t, err)
	infoB, err := svc.GetOpenAIImagesCapabilityForAPIKey(context.Background(), apiKeyB)
	require.NoError(t, err)
	infoC, err := svc.GetOpenAIImagesCapabilityForAPIKey(context.Background(), apiKeyC)
	require.NoError(t, err)

	require.NotNil(t, infoA1.Principal)
	require.NotNil(t, infoA2.Principal)
	require.NotNil(t, infoB.Principal)
	require.NotNil(t, infoC.Principal)
	require.Equal(t, infoA1.Principal.OwnerScope, infoA1.OwnerScope)
	require.Equal(t, infoA1.Principal.GroupScope, infoA1.GroupScope)
	require.Equal(t, infoA1.Principal.APIKeyScope, infoA1.APIKeyScope)

	require.Equal(t, infoA1.Principal.OwnerScope, infoB.Principal.OwnerScope, "same Sub2API user should share owner scope")
	require.NotEqual(t, infoA1.Principal.APIKeyScope, infoB.Principal.APIKeyScope, "different API keys should still get isolated API key scopes")
	require.NotEqual(t, infoA1.Principal.OwnerScope, infoC.Principal.OwnerScope, "different Sub2API users must not share owner scope")
	require.Equal(t, infoA1.Principal, infoA2.Principal, "same API key should produce stable scopes")

	requireOpenAIImagesPrincipalOpaque(t, apiKeyA, infoA1.Principal)
	body, err := json.Marshal(infoA1)
	require.NoError(t, err)
	jsonBody := string(body)
	require.Contains(t, jsonBody, `"principal"`)
	require.Contains(t, jsonBody, `"owner_scope"`)
	require.Contains(t, jsonBody, `"group_scope"`)
	require.Contains(t, jsonBody, `"api_key_scope"`)
	require.NotContains(t, jsonBody, "user_id")
	require.NotContains(t, jsonBody, "group_id")
	require.NotContains(t, jsonBody, "api_key_id")
	require.NotContains(t, jsonBody, "email")
	require.NotContains(t, jsonBody, "username")
	require.NotContains(t, jsonBody, "user:"+strconv.FormatInt(apiKeyA.UserID, 10))
	require.NotContains(t, jsonBody, "group:"+strconv.FormatInt(groupID, 10))
	require.NotContains(t, jsonBody, "api_key:"+strconv.FormatInt(apiKeyA.ID, 10))
}

func TestOpenAIImagesCapabilityPrincipalKeepsEmptyGroupScope(t *testing.T) {
	svc := newOpenAIImagesCapabilityTestService()
	apiKey := newOpenAIImagesCapabilityTestAPIKey(1001, 101, nil)

	info, err := svc.GetOpenAIImagesCapabilityForAPIKey(context.Background(), apiKey)
	require.NoError(t, err)
	require.NotNil(t, info.Principal)
	require.NotEmpty(t, info.Principal.OwnerScope)
	require.NotEmpty(t, info.Principal.APIKeyScope)
	require.Empty(t, info.Principal.GroupScope)
	require.Empty(t, info.GroupScope)

	body, err := json.Marshal(info)
	require.NoError(t, err)
	jsonBody := string(body)
	require.Contains(t, jsonBody, `"principal"`)
	require.Contains(t, jsonBody, `"group_scope"`)
}

func TestOpenAIImagesCapabilityKeepsScopeFieldsWhenSecretUnavailable(t *testing.T) {
	groupID := int64(501)
	svc := &OpenAIGatewayService{accountRepo: &stubOpenAIAccountRepo{}}

	info, err := svc.GetOpenAIImagesCapabilityForAPIKey(context.Background(), newOpenAIImagesCapabilityTestAPIKey(1001, 101, &groupID))
	require.NoError(t, err)
	require.NotNil(t, info.Principal)
	require.Empty(t, info.Principal.OwnerScope)
	require.Empty(t, info.OwnerScope)
	require.Empty(t, info.Principal.GroupScope)
	require.Empty(t, info.GroupScope)
	require.Empty(t, info.Principal.APIKeyScope)
	require.Empty(t, info.APIKeyScope)

	body, err := json.Marshal(info)
	require.NoError(t, err)
	jsonBody := string(body)
	require.Contains(t, jsonBody, `"principal"`)
	require.Contains(t, jsonBody, `"owner_scope"`)
	require.Contains(t, jsonBody, `"group_scope"`)
	require.Contains(t, jsonBody, `"api_key_scope"`)
}

func TestOpenAIImagesCapabilityPreservesExistingFieldsWithPrincipal(t *testing.T) {
	groupID := int64(501)
	svc := newOpenAIImagesCapabilityTestService(
		Account{
			ID:          1,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
		},
		Account{
			ID:          2,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"plan_type": "free"},
		},
	)

	info, err := svc.GetOpenAIImagesCapabilityForAPIKey(context.Background(), newOpenAIImagesCapabilityTestAPIKey(1001, 101, &groupID))
	require.NoError(t, err)
	require.NotNil(t, info.Principal)

	require.True(t, info.Available)
	require.Equal(t, "advanced", info.UIMode)
	require.Equal(t, "advanced_responses", info.ImageMode)
	require.Equal(t, "responses", info.Transport)
	require.Equal(t, openAIImagesDefaultModel, info.Model)
	require.True(t, info.SupportsBasic)
	require.True(t, info.SupportsAdvanced)
	require.True(t, info.SupportsStream)
	require.True(t, info.SupportsExactSize)
	require.True(t, info.SupportsCustomSize)
	require.True(t, info.SupportsQuality)
	require.True(t, info.SupportsOutputFormat)
	require.True(t, info.SupportsPartialImages)
	require.True(t, info.SupportsEdits)
	require.True(t, info.SupportsInputImages)
	require.True(t, info.SupportsUploads)
	require.Equal(t, 1, info.MaxN)
	require.Equal(t, map[string]int{
		"basic":    2,
		"advanced": 1,
		"api_key":  1,
		"web2api":  1,
	}, info.AccountCounts)
}

func TestOpenAIImagesCapabilityPreservesWeb2APILaneForFreeOnlyAccounts(t *testing.T) {
	groupID := int64(501)
	svc := newOpenAIImagesCapabilityTestService(Account{
		ID:          2,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"plan_type": "free"},
	})

	info, err := svc.GetOpenAIImagesCapabilityForAPIKey(context.Background(), newOpenAIImagesCapabilityTestAPIKey(1001, 101, &groupID))
	require.NoError(t, err)
	require.True(t, info.Available)
	require.Equal(t, "basic_web2api", info.ImageMode)
	require.Equal(t, "web2api", info.Transport)
	require.True(t, info.SupportsBasic)
	require.False(t, info.SupportsAdvanced)
	require.True(t, info.SupportsInputImages)
	require.True(t, info.SupportsUploads)
	require.Equal(t, map[string]int{
		"basic":   1,
		"web2api": 1,
	}, info.AccountCounts)
}

func newOpenAIImagesCapabilityTestService(accounts ...Account) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		accountRepo: &stubOpenAIAccountRepo{accounts: accounts},
		cfg: &config.Config{
			JWT: config.JWTConfig{Secret: strings.Repeat("s", 32)},
		},
	}
}

func newOpenAIImagesCapabilityTestAPIKey(id, userID int64, groupID *int64) *APIKey {
	return &APIKey{
		ID:      id,
		UserID:  userID,
		GroupID: groupID,
		Group:   &Group{Platform: PlatformOpenAI},
	}
}

func requireOpenAIImagesPrincipalOpaque(t *testing.T, apiKey *APIKey, principal *OpenAIImagesPrincipal) {
	t.Helper()
	require.NotNil(t, apiKey)
	require.NotNil(t, principal)

	requireOpaqueOpenAIImagesScope(t, principal.OwnerScope, openAIImagesUserScopePrefix, strconv.FormatInt(apiKey.UserID, 10))
	requireOpaqueOpenAIImagesScope(t, principal.APIKeyScope, openAIImagesAPIKeyScopePrefix, strconv.FormatInt(apiKey.ID, 10))
	if apiKey.GroupID != nil {
		requireOpaqueOpenAIImagesScope(t, principal.GroupScope, openAIImagesGroupScopePrefix, strconv.FormatInt(*apiKey.GroupID, 10))
	}
}

func requireOpaqueOpenAIImagesScope(t *testing.T, scope, prefix, rawID string) {
	t.Helper()
	require.True(t, strings.HasPrefix(scope, prefix))
	opaque := strings.TrimPrefix(scope, prefix)
	require.Len(t, opaque, 64)
	require.NotEqual(t, rawID, opaque)
	require.NotEqual(t, prefix+rawID, scope)
}
