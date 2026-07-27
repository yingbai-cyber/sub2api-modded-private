package kiro

// This file implements the Kiro OAuth authorization flows (Authorization Code +
// PKCE) for social login, AWS Builder ID / IAM Identity Center, and external
// IdPs (Microsoft Entra / Azure AD). It is used by the KiroOAuthService to
// drive the browser-based login flow from the admin panel.
//
// Three flows are supported:
//   - Social: app.kiro.dev portal → prod.{region}.auth.desktop.kiro.dev/oauth/token
//   - IDC:   register OIDC client → oidc.{region}.amazonaws.com/authorize → /token
//   - ExternalIdP: OIDC discovery → authorize → token endpoint
//
// All flows use PKCE (S256) for security. Redirect URIs target localhost ports
// (matching Kiro IDE behavior) so the browser popup can be intercepted by the
// frontend JavaScript.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAuth flow constants matching Kiro IDE / kiro-rs behavior.
const (
	// Social login portal and token endpoint.
	SocialPortalURL    = "https://app.kiro.dev"
	SocialAuthEndpoint = "https://prod.us-east-1.auth.desktop.kiro.dev"

	// AWS OIDC endpoint template (region-parameterized).
	DefaultOIDCRegion = "us-east-1"

	// Redirect URIs (localhost loopback, matching Kiro IDE).
	SocialRedirectURI      = "http://localhost:49153"
	IDCRedirectURI         = "http://127.0.0.1:9876/oauth/callback"
	ExternalIdPRedirectURI = "http://localhost:3128/oauth/callback"

	// IDC client registration parameters.
	IDCClientName = "Kiro IDE"
	IDCClientType = "public"

	// External SSO (Microsoft Enterprise) two-leg flow constants.
	ExternalSSOBaseURI       = "http://localhost:3128"
	ExternalSSOCallbackPath  = "/oauth/callback"
	ExternalSSODefaultScopes = "codewhisperer:conversations codewhisperer:completions offline_access"
)

// IDC scopes for CodeWhisperer / Kiro.
var IDCScopes = []string{
	"codewhisperer:completions",
	"codewhisperer:analysis",
	"codewhisperer:conversations",
	"codewhisperer:transformations",
	"codewhisperer:taskassist",
}

// BuilderIDStartURL is the default start URL for AWS Builder ID.
const BuilderIDStartURL = "https://view.awsapps.com/start"

// OAuthTokenInfo is the unified result of a successful OAuth token exchange.
type OAuthTokenInfo struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	ProfileArn   string `json:"profile_arn,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	AuthMethod   string `json:"auth_method,omitempty"`
}

// IDCRegistration holds the result of OIDC client registration.
type IDCRegistration struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// --- PKCE helpers ---

// GeneratePKCE generates a PKCE code_verifier (43-128 chars, base64url) and
// the corresponding code_challenge (SHA-256, base64url-unpadded).
func GeneratePKCE() (codeVerifier, codeChallenge string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("kiro oauth: generate PKCE: %w", err)
	}
	codeVerifier = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(codeVerifier))
	codeChallenge = base64.RawURLEncoding.EncodeToString(h[:])
	return codeVerifier, codeChallenge, nil
}

// GenerateState generates a random state parameter for OAuth.
func GenerateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// --- Social login flow ---

// BuildSocialAuthURL constructs the Kiro social login portal URL with PKCE.
func BuildSocialAuthURL(state, codeChallenge, redirectURI string) string {
	if redirectURI == "" {
		redirectURI = SocialRedirectURI
	}
	u, _ := url.Parse(SocialPortalURL + "/signin")
	q := u.Query()
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	q.Set("redirect_uri", redirectURI)
	q.Set("redirect_from", "KiroIDE")
	u.RawQuery = q.Encode()
	return u.String()
}

// ExchangeSocialToken exchanges an authorization code for tokens via the
// Kiro social auth endpoint: POST /oauth/token.
func ExchangeSocialToken(ctx context.Context, client *http.Client, code, codeVerifier, redirectURI, region string) (*OAuthTokenInfo, error) {
	if region == "" {
		region = DefaultOIDCRegion
	}
	if redirectURI == "" {
		redirectURI = SocialRedirectURI
	}
	endpoint := "https://prod." + region + ".auth.desktop.kiro.dev/oauth/token"

	payload := map[string]string{
		"code":          code,
		"code_verifier": codeVerifier,
		"redirect_uri":  redirectURI,
		"grant_type":    "authorization_code",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro oauth: social token exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kiro oauth: social token exchange failed: %d %s", resp.StatusCode, string(respBody))
	}

	var data struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ProfileArn   string `json:"profileArn"`
		ExpiresIn    *int64 `json:"expiresIn"`
	}
	if err := json.Unmarshal(respBody, &data); err != nil {
		return nil, fmt.Errorf("kiro oauth: parse social token response: %w", err)
	}

	info := &OAuthTokenInfo{
		AccessToken:  data.AccessToken,
		RefreshToken: data.RefreshToken,
		ProfileArn:   data.ProfileArn,
		AuthMethod:   string(AuthSocial),
	}
	if data.ExpiresIn != nil {
		info.ExpiresIn = *data.ExpiresIn
		info.ExpiresAt = time.Now().Add(time.Duration(*data.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	return info, nil
}

// --- External SSO callback parser ---

// ExternalSSOCallbackParsed holds the parsed callback URL parameters.
type ExternalSSOCallbackParsed struct {
	Phase     string // "external_descriptor" or "oauth_callback"
	IssuerURL string
	ClientID  string
	Scopes    string
	LoginHint string
	Code      string
	State     string
}

// ParseExternalSSOCallback parses a callback URL from the External SSO flow.
// It determines whether this is a leg-1 descriptor (portal returns IdP info)
// or a leg-2 OAuth callback (Microsoft returns code).
func ParseExternalSSOCallback(callbackURL string) (*ExternalSSOCallbackParsed, error) {
	parsed, err := url.Parse(strings.TrimSpace(callbackURL))
	if err != nil {
		return nil, fmt.Errorf("invalid callback URL: %w", err)
	}

	// Merge query and fragment params (some redirects put params in fragment).
	params := make(map[string]string)
	for k, v := range parsed.Query() {
		if len(v) > 0 && v[0] != "" {
			params[k] = v[0]
		}
	}
	if parsed.Fragment != "" {
		fragParsed, err := url.ParseQuery(parsed.Fragment)
		if err == nil {
			for k, v := range fragParsed {
				if _, exists := params[k]; !exists && len(v) > 0 && v[0] != "" {
					params[k] = v[0]
				}
			}
		}
	}

	// Check if this is a leg-1 external descriptor.
	loginOption := firstOf(params, "login_option", "loginOption")
	issuerURL := firstOf(params, "issuer_url", "issuerUrl", "issuer")
	isExternalDescriptor := (strings.EqualFold(loginOption, "external_idp") || issuerURL != "") &&
		parsed.Path != ExternalSSOCallbackPath

	if isExternalDescriptor {
		clientID := firstOf(params, "client_id", "clientId", "client")
		if issuerURL == "" {
			return nil, errors.New("external IdP descriptor 缺少 issuer_url")
		}
		if clientID == "" {
			return nil, errors.New("external IdP descriptor 缺少 client_id")
		}
		return &ExternalSSOCallbackParsed{
			Phase:     "external_descriptor",
			IssuerURL: issuerURL,
			ClientID:  clientID,
			Scopes:    firstOf(params, "scopes", "scope"),
			LoginHint: firstOf(params, "login_hint", "loginHint"),
		}, nil
	}

	// Leg-2: OAuth callback with code.
	if parsed.Path == ExternalSSOCallbackPath {
		code := firstOf(params, "code")
		state := firstOf(params, "state")
		if errParam := firstOf(params, "error"); errParam != "" {
			desc := firstOf(params, "error_description", "errorDescription")
			return nil, fmt.Errorf("external IdP 授权错误: %s %s", errParam, desc)
		}
		if code == "" {
			return nil, errors.New("回调 URL 缺少 code 参数")
		}
		return &ExternalSSOCallbackParsed{
			Phase: "oauth_callback",
			Code:  code,
			State: state,
		}, nil
	}

	return nil, errors.New("无法识别的回调 URL 格式")
}

// firstOf returns the first non-empty value from params for the given keys.
func firstOf(params map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := params[k]; v != "" {
			return v
		}
	}
	return ""
}

	return "https://oidc." + region + ".amazonaws.com"
}

// RegisterIDCClient registers a public OIDC client with AWS SSO OIDC,
// returning the client credentials needed for the authorization flow.
func RegisterIDCClient(ctx context.Context, client *http.Client, region, startURL string) (*IDCRegistration, error) {
	if startURL == "" {
		startURL = BuilderIDStartURL
	}
	endpoint := oidcEndpoint(region) + "/client/register"

	payload := map[string]any{
		"clientName":   IDCClientName,
		"clientType":   IDCClientType,
		"scopes":       IDCScopes,
		"grantTypes":   []string{"authorization_code", "refresh_token"},
		"redirectUris": []string{IDCRedirectURI},
		"issuerUrl":    startURL,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro oauth: IDC register client: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kiro oauth: IDC register failed: %d %s", resp.StatusCode, string(respBody))
	}

	var reg IDCRegistration
	if err := json.Unmarshal(respBody, &reg); err != nil {
		return nil, fmt.Errorf("kiro oauth: parse IDC register response: %w", err)
	}
	return &reg, nil
}

// BuildIDCAuthURL constructs the AWS OIDC authorization URL with PKCE.
func BuildIDCAuthURL(clientID, state, codeChallenge, redirectURI, startURL, region string) string {
	if redirectURI == "" {
		redirectURI = IDCRedirectURI
	}
	if region == "" {
		region = DefaultOIDCRegion
	}
	base := oidcEndpoint(region) + "/authorize"
	u, _ := url.Parse(base)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scopes", strings.Join(IDCScopes, " "))
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return u.String()
}

// ExchangeIDCToken exchanges an authorization code for tokens via the
// AWS OIDC token endpoint.
func ExchangeIDCToken(ctx context.Context, client *http.Client, code, codeVerifier, clientID, clientSecret, redirectURI, region string) (*OAuthTokenInfo, error) {
	if region == "" {
		region = DefaultOIDCRegion
	}
	if redirectURI == "" {
		redirectURI = IDCRedirectURI
	}
	endpoint := oidcEndpoint(region) + "/token"

	payload := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"code":         code,
		"codeVerifier": codeVerifier,
		"redirectUri":  redirectURI,
		"grantType":    "authorization_code",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro oauth: IDC token exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kiro oauth: IDC token exchange failed: %d %s", resp.StatusCode, string(respBody))
	}

	var data struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    *int64 `json:"expiresIn"`
		ProfileArn   string `json:"profileArn"`
	}
	if err := json.Unmarshal(respBody, &data); err != nil {
		return nil, fmt.Errorf("kiro oauth: parse IDC token response: %w", err)
	}

	info := &OAuthTokenInfo{
		AccessToken:  data.AccessToken,
		RefreshToken: data.RefreshToken,
		ProfileArn:   data.ProfileArn,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		AuthMethod:   string(AuthIDC),
	}
	if data.ExpiresIn != nil {
		info.ExpiresIn = *data.ExpiresIn
		info.ExpiresAt = time.Now().Add(time.Duration(*data.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	return info, nil
}

// --- External IdP (Microsoft Entra / Azure AD) flow ---

// allowedExternalIdPHosts restricts external IdP token endpoints to known providers.
var allowedExternalIdPHosts = []string{
	".microsoftonline.com",
	".microsoftonline.us",
	".microsoftonline.cn",
}

// ValidateExternalIdPEndpoint checks that the token endpoint belongs to a
// known external IdP provider.
func ValidateExternalIdPEndpoint(tokenEndpoint string) error {
	u, err := url.Parse(tokenEndpoint)
	if err != nil {
		return fmt.Errorf("kiro oauth: invalid token endpoint URL: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range allowedExternalIdPHosts {
		if strings.HasSuffix(host, suffix) {
			return nil
		}
	}
	return fmt.Errorf("kiro oauth: external IdP host %q not in allowlist", host)
}

// OIDCDiscovery performs OpenID Connect discovery and returns the
// authorization_endpoint from the well-known configuration.
func OIDCDiscovery(ctx context.Context, client *http.Client, issuerURL string) (authEndpoint, tokenEndpoint string, err error) {
	wellKnown := strings.TrimSuffix(issuerURL, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("kiro oauth: OIDC discovery: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("kiro oauth: OIDC discovery failed: %d %s", resp.StatusCode, string(body))
	}

	var config struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if err := json.Unmarshal(body, &config); err != nil {
		return "", "", fmt.Errorf("kiro oauth: parse OIDC config: %w", err)
	}
	return config.AuthorizationEndpoint, config.TokenEndpoint, nil
}

// BuildExternalIdPAuthURL constructs the external IdP authorization URL with PKCE.
func BuildExternalIdPAuthURL(authEndpoint, clientID, state, codeChallenge, redirectURI, scopes string) string {
	if redirectURI == "" {
		redirectURI = ExternalIdPRedirectURI
	}
	if scopes == "" {
		scopes = "codewhisperer:conversations codewhisperer:completions offline_access"
	}
	u, _ := url.Parse(authEndpoint)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", scopes)
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return u.String()
}

// ExchangeExternalIdPToken exchanges an authorization code for tokens via the
// external IdP token endpoint (form-encoded, per OAuth2 spec).
func ExchangeExternalIdPToken(ctx context.Context, client *http.Client, tokenEndpoint, code, codeVerifier, clientID, redirectURI, scopes string) (*OAuthTokenInfo, error) {
	if err := ValidateExternalIdPEndpoint(tokenEndpoint); err != nil {
		return nil, err
	}
	if redirectURI == "" {
		redirectURI = ExternalIdPRedirectURI
	}
	if scopes == "" {
		scopes = "codewhisperer:conversations codewhisperer:completions offline_access"
	}

	// Ensure offline_access is included for token refresh capability.
	if !strings.Contains(scopes, "offline_access") {
		scopes = scopes + " offline_access"
	}

	form := url.Values{
		"client_id":     {clientID},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {codeVerifier},
		"redirect_uri":  {redirectURI},
		"scope":         {scopes},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro oauth: external IdP token exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kiro oauth: external IdP token exchange failed: %d %s", resp.StatusCode, string(respBody))
	}

	var data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    *int64 `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &data); err != nil {
		return nil, fmt.Errorf("kiro oauth: parse external IdP token response: %w", err)
	}

	info := &OAuthTokenInfo{
		AccessToken:  data.AccessToken,
		RefreshToken: data.RefreshToken,
		ClientID:     clientID,
		AuthMethod:   string(AuthExternalIDP),
	}
	if data.ExpiresIn != nil {
		info.ExpiresIn = *data.ExpiresIn
		info.ExpiresAt = time.Now().Add(time.Duration(*data.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	return info, nil
}

// --- Token import helper ---

// ImportTokenJSON parses a Kiro IDE token JSON (from local storage or
// credentials file) and returns a normalized OAuthTokenInfo.
func ImportTokenJSON(tokenJSON string) (*OAuthTokenInfo, error) {
	var raw struct {
		RefreshToken string `json:"refreshToken"`
		AccessToken  string `json:"accessToken"`
		AuthMethod   string `json:"authMethod"`
		ExpiresAt    string `json:"expiresAt"`
		ProfileArn   string `json:"profileArn"`
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
		// Device registration (Builder ID)
		DeviceRegistration *struct {
			ClientID     string `json:"clientId"`
			ClientSecret string `json:"clientSecret"`
		} `json:"deviceRegistration"`
	}
	if err := json.Unmarshal([]byte(tokenJSON), &raw); err != nil {
		return nil, fmt.Errorf("kiro oauth: parse token JSON: %w", err)
	}
	if raw.RefreshToken == "" {
		return nil, errors.New("kiro oauth: token JSON missing refreshToken")
	}

	info := &OAuthTokenInfo{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		ExpiresAt:    raw.ExpiresAt,
		ProfileArn:   raw.ProfileArn,
		AuthMethod:   raw.AuthMethod,
		ClientID:     raw.ClientID,
		ClientSecret: raw.ClientSecret,
	}
	// Fallback to device registration if present.
	if info.ClientID == "" && raw.DeviceRegistration != nil {
		info.ClientID = raw.DeviceRegistration.ClientID
		info.ClientSecret = raw.DeviceRegistration.ClientSecret
	}
	// Default auth method.
	if info.AuthMethod == "" {
		if info.ClientID != "" {
			info.AuthMethod = string(AuthIDC)
		} else {
			info.AuthMethod = string(AuthSocial)
		}
	}
	return info, nil
}
