// Package kiro implements native AWS CodeWhisperer / Kiro upstream support.
//
// This package is a self-contained, loosely-coupled Go reimplementation of the
// core upstream logic of the external kiro-rs proxy. It converts Anthropic
// Messages requests into CodeWhisperer conversationState requests, calls the
// Kiro/CodeWhisperer upstream (IDE or CLI endpoints) with Bearer-token auth,
// decodes the AWS event-stream binary response frames and converts them back
// into Anthropic SSE / message JSON.
//
// It intentionally has NO dependency on the parent service package (Account
// data is passed in as plain values), so it can be maintained as an isolated
// module with minimal rebase surface against upstream sub2api.
package kiro

import (
	"strings"
)

// AuthMethod identifies how a Kiro credential authenticates to the upstream.
type AuthMethod string

const (
	// AuthAPIKey uses a ksk_* Kiro API Key directly as the Bearer token.
	AuthAPIKey AuthMethod = "api_key"
	// AuthSocial refreshes via prod.{region}.auth.desktop.kiro.dev/refreshToken.
	AuthSocial AuthMethod = "social"
	// AuthIDC refreshes via AWS SSO OIDC oidc.{region}.amazonaws.com/token.
	AuthIDC AuthMethod = "idc"
	// AuthExternalIDP refreshes via an external IdP token endpoint (Entra/Azure).
	AuthExternalIDP AuthMethod = "external_idp"
)

// Credentials is the normalized Kiro credential set parsed from an account's
// Credentials/Extra maps. Field names mirror kiro-rs KiroCredentials.
type Credentials struct {
	// ID is the owning account id (used for stable fallback machine_id).
	ID int64

	AccessToken  string
	RefreshToken string
	KiroAPIKey   string // ksk_* ; when set, used directly as Bearer

	ProfileArn string
	ExpiresAt  string // RFC3339 or unix; parsed by caller

	AuthMethod   AuthMethod
	ClientID     string // IdC / external_idp
	ClientSecret string // IdC only

	TokenEndpoint string // external_idp
	IssuerURL     string // external_idp
	Scopes        string // external_idp

	Region     string // credential-level region fallback
	AuthRegion string
	APIRegion  string

	MachineID string // explicit machine id override
	Endpoint  string // "ide" / "cli" ; empty => default

	// BaseURL, when set and no native auth is configured, selects the legacy
	// external kiro-rs proxy passthrough mode instead of native upstream.
	BaseURL string
}

// canonicalizeAuthMethod normalizes legacy / alias auth method values.
func canonicalizeAuthMethod(v string) AuthMethod {
	m := strings.ToLower(strings.TrimSpace(v))
	switch m {
	case "api_key", "apikey":
		return AuthAPIKey
	case "idc", "builder-id", "builderid", "iam", "iam_sso":
		return AuthIDC
	case "external_idp", "azuread", "azure", "entra", "microsoft", "m365":
		return AuthExternalIDP
	case "social", "github", "google":
		return AuthSocial
	default:
		return AuthMethod(m)
	}
}

// IsAPIKey reports whether this credential authenticates with a ksk_* key
// directly (no token refresh). Mirrors kiro-rs is_api_key_credential: a
// non-empty kiro_api_key OR auth_method=api_key.
func (c *Credentials) IsAPIKey() bool {
	return c.KiroAPIKey != "" || c.AuthMethod == AuthAPIKey
}

// IsExternalIDP reports whether this is an external IdP (Entra/Azure) credential.
func (c *Credentials) IsExternalIDP() bool {
	return c.AuthMethod == AuthExternalIDP
}

// EffectiveAuthMethod resolves the auth method, inferring from available fields
// when unset (mirrors kiro-rs refresh_token dispatch):
//   - kiro_api_key present            => api_key
//   - client_id + client_secret       => idc
//   - token_endpoint present          => external_idp
//   - otherwise                       => social
func (c *Credentials) EffectiveAuthMethod() AuthMethod {
	if c.AuthMethod != "" {
		return c.AuthMethod
	}
	switch {
	case c.KiroAPIKey != "":
		return AuthAPIKey
	case c.ClientID != "" && c.ClientSecret != "":
		return AuthIDC
	case c.TokenEndpoint != "":
		return AuthExternalIDP
	default:
		return AuthSocial
	}
}

// UsesNativeUpstream reports whether this credential should use the native
// CodeWhisperer upstream path. When false (only base_url set, no native auth),
// the caller should fall back to the legacy external kiro-rs proxy passthrough.
func (c *Credentials) UsesNativeUpstream() bool {
	if c.KiroAPIKey != "" || c.RefreshToken != "" {
		return true
	}
	// auth_method explicitly set to a known native method also counts.
	switch c.AuthMethod {
	case AuthAPIKey, AuthSocial, AuthIDC, AuthExternalIDP:
		return true
	}
	return false
}

// TokenTypeHeader returns the value for the upstream "TokenType" header, if any.
func (c *Credentials) TokenTypeHeader() string {
	switch {
	case c.IsAPIKey():
		return "API_KEY"
	case c.IsExternalIDP():
		return "EXTERNAL_IDP"
	default:
		return ""
	}
}
