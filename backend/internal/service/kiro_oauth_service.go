package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/google/uuid"
)

// KiroOAuthService manages Kiro OAuth authorization flows (social, IDC,
// external IdP). It holds in-memory sessions (PKCE state) with a 10-minute
// TTL and dispatches token exchange to the appropriate flow.
type KiroOAuthService struct {
	sessions  sync.Map // sessionID -> *KiroOAuthSession
	proxyRepo ProxyRepository

	// cleanupOnce ensures the cleanup goroutine starts at most once.
	cleanupOnce sync.Once
}

// NewKiroOAuthService builds a KiroOAuthService.
func NewKiroOAuthService(proxyRepo ProxyRepository) *KiroOAuthService {
	svc := &KiroOAuthService{proxyRepo: proxyRepo}
	svc.startCleanup()
	return svc
}

const kiroOAuthSessionTTL = 10 * time.Minute

// KiroOAuthSession holds the PKCE and state for an in-progress OAuth flow.
type KiroOAuthSession struct {
	AuthMethod   string
	CodeVerifier string
	State        string
	Region       string
	// IDC specific (from client registration)
	ClientID     string
	ClientSecret string
	// External IdP specific
	TokenEndpoint string
	IssuerURL     string
	Scopes        string
	ClientIDExt   string
	// Meta
	CreatedAt time.Time
	ProxyID   *int64
}

// KiroAuthURLResult is returned by GenerateAuthURL / GenerateIDCAuthURL.
type KiroAuthURLResult struct {
	AuthURL   string `json:"auth_url"`
	SessionID string `json:"session_id"`
	State     string `json:"state"`
}

// --- Input types ---

// KiroOAuthGenerateAuthURLInput is the input for social/external-idp auth URL generation.
type KiroOAuthGenerateAuthURLInput struct {
	AuthMethod string `json:"auth_method"` // "social" or "external_idp"
	Region     string `json:"region"`
	ProxyID    *int64 `json:"proxy_id"`
	// External IdP only
	IssuerURL string `json:"issuer_url"`
	ClientID  string `json:"client_id"`
	Scopes    string `json:"scopes"`
}

// KiroOAuthGenerateIDCAuthURLInput is the input for IDC (Builder ID) auth URL generation.
type KiroOAuthGenerateIDCAuthURLInput struct {
	Region   string `json:"region"`
	StartURL string `json:"start_url"`
	ProxyID  *int64 `json:"proxy_id"`
}

// KiroOAuthExchangeCodeInput is the input for code exchange.
type KiroOAuthExchangeCodeInput struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
	Code      string `json:"code"`
	ProxyID   *int64 `json:"proxy_id"`
}

// KiroOAuthRefreshTokenInput is the input for token refresh validation.
type KiroOAuthRefreshTokenInput struct {
	RefreshToken  string `json:"refresh_token"`
	AuthMethod    string `json:"auth_method"`
	Region        string `json:"region"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	TokenEndpoint string `json:"token_endpoint"`
	Scopes        string `json:"scopes"`
	ProxyID       *int64 `json:"proxy_id"`
}

// --- Service methods ---

// GenerateAuthURL generates a social or external IdP authorization URL.
func (s *KiroOAuthService) GenerateAuthURL(ctx context.Context, input *KiroOAuthGenerateAuthURLInput) (*KiroAuthURLResult, error) {
	state, err := kiro.GenerateState()
	if err != nil {
		return nil, fmt.Errorf("生成 state 失败: %w", err)
	}
	codeVerifier, codeChallenge, err := kiro.GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("生成 PKCE 失败: %w", err)
	}
	sessionID := uuid.NewString()

	session := &KiroOAuthSession{
		AuthMethod:   input.AuthMethod,
		CodeVerifier: codeVerifier,
		State:        state,
		Region:       input.Region,
		ProxyID:      input.ProxyID,
		CreatedAt:    time.Now(),
	}

	var authURL string
	switch input.AuthMethod {
	case "social", "":
		session.AuthMethod = "social"
		authURL = kiro.BuildSocialAuthURL(state, codeChallenge, "")
	case "external_idp":
		if input.IssuerURL == "" {
			return nil, errors.New("external_idp 需要 issuer_url")
		}
		if input.ClientID == "" {
			return nil, errors.New("external_idp 需要 client_id")
		}
		// OIDC discovery to find authorization endpoint.
		httpClient := s.buildHTTPClient(input.ProxyID)
		authEndpoint, tokenEndpoint, err := kiro.OIDCDiscovery(ctx, httpClient, input.IssuerURL)
		if err != nil {
			return nil, fmt.Errorf("OIDC 发现失败: %w", err)
		}
		session.TokenEndpoint = tokenEndpoint
		session.IssuerURL = input.IssuerURL
		session.ClientIDExt = input.ClientID
		session.Scopes = input.Scopes
		authURL = kiro.BuildExternalIdPAuthURL(authEndpoint, input.ClientID, state, codeChallenge, "", input.Scopes)
	default:
		return nil, fmt.Errorf("不支持的 auth_method: %s (use GenerateIDCAuthURL for idc)", input.AuthMethod)
	}

	s.sessions.Store(sessionID, session)

	return &KiroAuthURLResult{
		AuthURL:   authURL,
		SessionID: sessionID,
		State:     state,
	}, nil
}

// GenerateIDCAuthURL generates an AWS Builder ID / IAM Identity Center auth URL.
// This first registers an OIDC client, then builds the authorization URL.
func (s *KiroOAuthService) GenerateIDCAuthURL(ctx context.Context, input *KiroOAuthGenerateIDCAuthURLInput) (*KiroAuthURLResult, error) {
	state, err := kiro.GenerateState()
	if err != nil {
		return nil, fmt.Errorf("生成 state 失败: %w", err)
	}
	codeVerifier, codeChallenge, err := kiro.GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("生成 PKCE 失败: %w", err)
	}
	sessionID := uuid.NewString()

	httpClient := s.buildHTTPClient(input.ProxyID)

	// Step 1: Register OIDC client.
	reg, err := kiro.RegisterIDCClient(ctx, httpClient, input.Region, input.StartURL)
	if err != nil {
		return nil, fmt.Errorf("IDC 客户端注册失败: %w", err)
	}

	// Step 2: Build authorization URL.
	authURL := kiro.BuildIDCAuthURL(reg.ClientID, state, codeChallenge, "", input.StartURL, input.Region)

	session := &KiroOAuthSession{
		AuthMethod:   "idc",
		CodeVerifier: codeVerifier,
		State:        state,
		Region:       input.Region,
		ClientID:     reg.ClientID,
		ClientSecret: reg.ClientSecret,
		ProxyID:      input.ProxyID,
		CreatedAt:    time.Now(),
	}
	s.sessions.Store(sessionID, session)

	return &KiroAuthURLResult{
		AuthURL:   authURL,
		SessionID: sessionID,
		State:     state,
	}, nil
}

// ExchangeCode exchanges an authorization code for tokens using the stored session.
func (s *KiroOAuthService) ExchangeCode(ctx context.Context, input *KiroOAuthExchangeCodeInput) (*kiro.OAuthTokenInfo, error) {
	val, ok := s.sessions.LoadAndDelete(input.SessionID)
	if !ok {
		return nil, errors.New("OAuth 会话不存在或已过期")
	}
	session, _ := val.(*KiroOAuthSession)

	// Validate state.
	if session.State != input.State {
		return nil, errors.New("OAuth state 不匹配")
	}

	// Check TTL.
	if time.Since(session.CreatedAt) > kiroOAuthSessionTTL {
		return nil, errors.New("OAuth 会话已过期")
	}

	httpClient := s.buildHTTPClient(input.ProxyID)

	switch session.AuthMethod {
	case "social":
		return kiro.ExchangeSocialToken(ctx, httpClient, input.Code, session.CodeVerifier, "", session.Region)
	case "idc":
		return kiro.ExchangeIDCToken(ctx, httpClient, input.Code, session.CodeVerifier, session.ClientID, session.ClientSecret, "", session.Region)
	case "external_idp":
		return kiro.ExchangeExternalIdPToken(ctx, httpClient, session.TokenEndpoint, input.Code, session.CodeVerifier, session.ClientIDExt, "", session.Scopes)
	default:
		return nil, fmt.Errorf("未知的 auth_method: %s", session.AuthMethod)
	}
}

// ValidateRefreshToken performs a token refresh to validate the refresh token
// and return full token info. Used when manually entering a refresh token.
func (s *KiroOAuthService) ValidateRefreshToken(ctx context.Context, input *KiroOAuthRefreshTokenInput) (*kiro.OAuthTokenInfo, error) {
	if input.RefreshToken == "" {
		return nil, errors.New("refresh_token 不能为空")
	}

	httpClient := s.buildHTTPClient(input.ProxyID)

	// Build a temporary Credentials struct for the refresh call.
	cred := &kiro.Credentials{
		RefreshToken:  input.RefreshToken,
		ClientID:      input.ClientID,
		ClientSecret:  input.ClientSecret,
		TokenEndpoint: input.TokenEndpoint,
		Scopes:        input.Scopes,
	}
	switch input.AuthMethod {
	case "social", "":
		cred.AuthMethod = kiro.AuthSocial
	case "idc":
		cred.AuthMethod = kiro.AuthIDC
	case "external_idp":
		cred.AuthMethod = kiro.AuthExternalIDP
	default:
		return nil, fmt.Errorf("不支持的 auth_method: %s", input.AuthMethod)
	}

	cfg := &kiro.Config{}
	if input.Region != "" {
		cfg.AuthRegion = input.Region
	}

	result, err := kiro.RefreshToken(ctx, httpClient, cred, cfg)
	if err != nil {
		return nil, fmt.Errorf("token 刷新失败: %w", err)
	}

	return &kiro.OAuthTokenInfo{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresAt:    result.ExpiresAt,
		ProfileArn:   result.ProfileArn,
		ClientID:     input.ClientID,
		ClientSecret: input.ClientSecret,
		AuthMethod:   input.AuthMethod,
	}, nil
}

// ImportToken parses a Kiro IDE token JSON and validates it by performing a
// refresh, returning full token info on success.
func (s *KiroOAuthService) ImportToken(ctx context.Context, tokenJSON string, proxyID *int64) (*kiro.OAuthTokenInfo, error) {
	info, err := kiro.ImportTokenJSON(tokenJSON)
	if err != nil {
		return nil, err
	}
	// Validate by refreshing.
	refreshInput := &KiroOAuthRefreshTokenInput{
		RefreshToken: info.RefreshToken,
		AuthMethod:   info.AuthMethod,
		ClientID:     info.ClientID,
		ClientSecret: info.ClientSecret,
		ProxyID:      proxyID,
	}
	refreshed, err := s.ValidateRefreshToken(ctx, refreshInput)
	if err != nil {
		// Return the imported info even if refresh fails (token may still be valid).
		return info, nil
	}
	// Merge: keep original client info, update tokens.
	if refreshed.AccessToken != "" {
		info.AccessToken = refreshed.AccessToken
	}
	if refreshed.RefreshToken != "" {
		info.RefreshToken = refreshed.RefreshToken
	}
	if refreshed.ExpiresAt != "" {
		info.ExpiresAt = refreshed.ExpiresAt
	}
	if refreshed.ProfileArn != "" {
		info.ProfileArn = refreshed.ProfileArn
	}
	return info, nil
}

// --- Internal helpers ---

// buildHTTPClient creates an HTTP client, optionally with proxy.
func (s *KiroOAuthService) buildHTTPClient(proxyID *int64) *http.Client {
	client := &http.Client{Timeout: 30 * time.Second}
	if proxyID == nil || s.proxyRepo == nil {
		return client
	}
	proxy, err := s.proxyRepo.GetByID(context.Background(), *proxyID)
	if err != nil || proxy == nil {
		return client
	}
	proxyStr := proxy.URL()
	if proxyStr == "" {
		return client
	}
	_, proxyURL, err := proxyurl.Parse(proxyStr)
	if err != nil || proxyURL == nil {
		return client
	}
	baseTransport, _ := http.DefaultTransport.(*http.Transport)
	transport := baseTransport.Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	client.Transport = transport
	return client
}

// startCleanup starts a background goroutine that periodically purges expired sessions.
func (s *KiroOAuthService) startCleanup() {
	s.cleanupOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(2 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				s.sessions.Range(func(key, value interface{}) bool {
					session, _ := value.(*KiroOAuthSession)
					if session != nil && time.Since(session.CreatedAt) > kiroOAuthSessionTTL {
						s.sessions.Delete(key)
					}
					return true
				})
			}
		}()
	})
}
