package kiro

import "testing"

func TestEffectiveAuthMethod(t *testing.T) {
	cases := []struct {
		name string
		c    Credentials
		want AuthMethod
	}{
		{"explicit wins", Credentials{AuthMethod: AuthIDC, KiroAPIKey: "ksk_x"}, AuthIDC},
		{"api key inferred", Credentials{KiroAPIKey: "ksk_x"}, AuthAPIKey},
		{"idc inferred", Credentials{ClientID: "c", ClientSecret: "s"}, AuthIDC},
		{"external idp inferred", Credentials{TokenEndpoint: "https://login"}, AuthExternalIDP},
		{"social default", Credentials{RefreshToken: "rt"}, AuthSocial},
	}
	for _, c := range cases {
		if got := c.c.EffectiveAuthMethod(); got != c.want {
			t.Errorf("%s: EffectiveAuthMethod() = %q; want %q", c.name, got, c.want)
		}
	}
}

func TestIsAPIKey(t *testing.T) {
	if !(&Credentials{KiroAPIKey: "ksk_x"}).IsAPIKey() {
		t.Error("kiro_api_key should be api key")
	}
	if !(&Credentials{AuthMethod: AuthAPIKey}).IsAPIKey() {
		t.Error("auth_method=api_key should be api key")
	}
	if (&Credentials{RefreshToken: "rt"}).IsAPIKey() {
		t.Error("refresh token should not be api key")
	}
}

func TestUsesNativeUpstream(t *testing.T) {
	if !(&Credentials{RefreshToken: "rt"}).UsesNativeUpstream() {
		t.Error("refresh token => native")
	}
	if !(&Credentials{KiroAPIKey: "ksk_x"}).UsesNativeUpstream() {
		t.Error("api key => native")
	}
	if (&Credentials{BaseURL: "http://ext"}).UsesNativeUpstream() {
		t.Error("only base_url => legacy passthrough, not native")
	}
}

func TestTokenTypeHeader(t *testing.T) {
	if got := (&Credentials{KiroAPIKey: "ksk_x"}).TokenTypeHeader(); got != "API_KEY" {
		t.Errorf("api key TokenType = %q; want API_KEY", got)
	}
	if got := (&Credentials{AuthMethod: AuthExternalIDP}).TokenTypeHeader(); got != "EXTERNAL_IDP" {
		t.Errorf("external idp TokenType = %q; want EXTERNAL_IDP", got)
	}
	if got := (&Credentials{RefreshToken: "rt"}).TokenTypeHeader(); got != "" {
		t.Errorf("social TokenType = %q; want empty", got)
	}
}

func TestParseCredentials(t *testing.T) {
	creds := map[string]any{
		"access_token":  "at",
		"refresh_token": "rt",
		"profile_arn":   "arn:aws:codewhisperer:...",
		"auth_method":   "builder-id",
		"client_id":     "cid",
		"client_secret": "sec",
	}
	c := ParseCredentials(42, creds, nil)
	if c.ID != 42 {
		t.Errorf("ID = %d; want 42", c.ID)
	}
	if c.AccessToken != "at" || c.RefreshToken != "rt" {
		t.Error("tokens not parsed")
	}
	if c.AuthMethod != AuthIDC {
		t.Errorf("auth_method = %q; want idc (canonicalized from builder-id)", c.AuthMethod)
	}
	if c.ProfileArn == "" {
		t.Error("profile_arn not parsed")
	}
}

func TestParseCredentialsCamelCase(t *testing.T) {
	creds := map[string]any{
		"refreshToken": "rt",
		"kiroApiKey":   "ksk_abc",
	}
	c := ParseCredentials(1, creds, nil)
	if c.RefreshToken != "rt" || c.KiroAPIKey != "ksk_abc" {
		t.Error("camelCase keys not parsed")
	}
	if !c.IsAPIKey() {
		t.Error("kiroApiKey should imply api key")
	}
}
