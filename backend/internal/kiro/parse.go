package kiro

import (
	"strings"
)

// asString coerces an arbitrary JSON-decoded value to a trimmed string.
// Supports string and json.Number-ish numeric via fmt; returns "" otherwise.
func asString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case nil:
		return ""
	default:
		return ""
	}
}

// getStr looks up key in the credentials map and returns a trimmed string.
func getStr(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s := asString(v); s != "" {
				return s
			}
		}
	}
	return ""
}

// ParseCredentials builds a normalized Credentials from an account's
// Credentials and Extra maps. It accepts both snake_case (sub2api convention)
// and camelCase (kiro-rs convention) keys for resilience.
//
// accountID is used as the stable fallback machine_id seed.
func ParseCredentials(accountID int64, creds, extra map[string]any) *Credentials {
	c := &Credentials{ID: accountID}

	c.AccessToken = getStr(creds, "access_token", "accessToken")
	c.RefreshToken = getStr(creds, "refresh_token", "refreshToken")
	c.KiroAPIKey = getStr(creds, "kiro_api_key", "kiroApiKey")
	c.ProfileArn = getStr(creds, "profile_arn", "profileArn")
	c.ExpiresAt = getStr(creds, "expires_at", "expiresAt")

	c.ClientID = getStr(creds, "client_id", "clientId")
	c.ClientSecret = getStr(creds, "client_secret", "clientSecret")
	c.TokenEndpoint = getStr(creds, "token_endpoint", "tokenEndpoint")
	c.IssuerURL = getStr(creds, "issuer_url", "issuerUrl")
	c.Scopes = getStr(creds, "scopes")

	c.Region = getStr(creds, "region")
	c.AuthRegion = getStr(creds, "auth_region", "authRegion")
	c.APIRegion = getStr(creds, "api_region", "apiRegion")
	c.MachineID = getStr(creds, "machine_id", "machineId")
	c.Endpoint = strings.ToLower(getStr(creds, "endpoint"))
	c.BaseURL = getStr(creds, "base_url", "baseUrl")

	if am := getStr(creds, "auth_method", "authMethod"); am != "" {
		c.AuthMethod = canonicalizeAuthMethod(am)
	}

	// A few fields are conventionally stored in Extra (non-sensitive).
	if c.ProfileArn == "" {
		c.ProfileArn = getStr(extra, "profile_arn", "profileArn")
	}
	if c.Endpoint == "" {
		c.Endpoint = strings.ToLower(getStr(extra, "endpoint"))
	}

	return c
}
