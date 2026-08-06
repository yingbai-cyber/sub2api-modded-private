package kiro

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func longToken() string {
	return strings.Repeat("a", 120)
}

func TestTokenExpiryHelpers(t *testing.T) {
	future := &Credentials{ExpiresAt: time.Now().Add(30 * time.Minute).Format(time.RFC3339)}
	if IsTokenExpired(future) {
		t.Error("token 30min out should not be expired")
	}
	if IsTokenExpiringSoon(future) {
		t.Error("token 30min out should not be expiring soon")
	}

	soon := &Credentials{ExpiresAt: time.Now().Add(3 * time.Minute).Format(time.RFC3339)}
	if !IsTokenExpired(soon) {
		t.Error("token 3min out should be expired (5min skew)")
	}
	if !IsTokenExpiringSoon(soon) {
		t.Error("token 3min out should be expiring soon")
	}

	// Missing expiry => expired (conservative), not soon.
	none := &Credentials{}
	if !IsTokenExpired(none) {
		t.Error("missing expiry should be treated as expired")
	}
	if IsTokenExpiringSoon(none) {
		t.Error("missing expiry should not be 'soon'")
	}
}

func TestValidateRefreshToken(t *testing.T) {
	if err := ValidateRefreshToken(&Credentials{}); err == nil {
		t.Error("expected error for missing refresh token")
	}
	if err := ValidateRefreshToken(&Credentials{RefreshToken: "short"}); err == nil {
		t.Error("expected error for short refresh token")
	}
	if err := ValidateRefreshToken(&Credentials{RefreshToken: longToken() + "..."}); err == nil {
		t.Error("expected error for truncated refresh token")
	}
	if err := ValidateRefreshToken(&Credentials{RefreshToken: longToken()}); err != nil {
		t.Errorf("valid token rejected: %v", err)
	}
}

func TestRefreshAPIKeyRefused(t *testing.T) {
	c := &Credentials{KiroAPIKey: "ksk_x", AuthMethod: AuthAPIKey}
	_, err := RefreshToken(context.Background(), http.DefaultClient, c, DefaultConfig())
	if err == nil || !strings.Contains(err.Error(), "API Key") {
		t.Errorf("expected API Key refusal, got %v", err)
	}
}

func TestRefreshSocialSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "refreshToken") {
			t.Errorf("social request missing refreshToken: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"accessToken":"new-access","refreshToken":"new-refresh","profileArn":"arn:p","expiresIn":3600}`)
	}))
	defer srv.Close()

	c := &Credentials{RefreshToken: longToken(), AuthMethod: AuthSocial}
	res, err := refreshViaURL(t, srv.URL, c, parseSocialRefreshResponse)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if res.AccessToken != "new-access" || res.RefreshToken != "new-refresh" || res.ProfileArn != "arn:p" {
		t.Errorf("unexpected result: %+v", res)
	}
	if res.ExpiresAt == "" {
		t.Error("expected expires_at to be set from expiresIn")
	}
}

func TestRefreshInvalidGrantIsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Invalid refresh token provided"}`)
	}))
	defer srv.Close()

	c := &Credentials{RefreshToken: longToken(), AuthMethod: AuthSocial}
	_, err := refreshViaURL(t, srv.URL, c, parseSocialRefreshResponse)
	if !errors.Is(err, ErrRefreshTokenInvalid) {
		t.Errorf("expected ErrRefreshTokenInvalid, got %v", err)
	}
}

// TestIsTerminalRefreshStatus pins the shared predicate used by both the social
// and IdC refresh paths, so one table covers both without needing a parse helper
// per path.
//
// 401 is terminal because the refresh endpoints authenticate the request by the
// refresh_token itself: "unauthorized" is a verdict on the credential, not on
// request shape or capacity. 403 stays retryable (upstream policy can be
// restored server-side without touching the credential), as do 429 and 5xx.
func TestIsTerminalRefreshStatus(t *testing.T) {
	terminal := []int{http.StatusUnauthorized}
	retryable := []int{
		http.StatusBadRequest,
		http.StatusForbidden,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
	}
	for _, status := range terminal {
		if !isTerminalRefreshStatus(status) {
			t.Errorf("status %d should be terminal", status)
		}
	}
	for _, status := range retryable {
		if isTerminalRefreshStatus(status) {
			t.Errorf("status %d should stay retryable", status)
		}
	}
}

// TestRefreshUnauthorizedIsPermanent covers the social path wiring: a 401 must
// surface as ErrRefreshTokenInvalid so the background refresher stops retrying
// the credential every cycle and marks the account for re-auth instead.
func TestRefreshUnauthorizedIsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"Bad credentials"}`)
	}))
	defer srv.Close()

	c := &Credentials{RefreshToken: longToken(), AuthMethod: AuthSocial}
	_, err := refreshViaURL(t, srv.URL, c, parseSocialRefreshResponse)
	if !errors.Is(err, ErrRefreshTokenInvalid) {
		t.Errorf("401 must be terminal, got %v", err)
	}
}

// TestRefreshRetryableStatusesStayRetryable guards the other half through the
// real social path: capacity and policy failures must NOT be mistaken for a dead
// credential, otherwise a transient upstream blip would mark accounts for
// re-auth.
func TestRefreshRetryableStatusesStayRetryable(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"message":"transient"}`)
		}))

		c := &Credentials{RefreshToken: longToken(), AuthMethod: AuthSocial}
		_, err := refreshViaURL(t, srv.URL, c, parseSocialRefreshResponse)
		srv.Close()
		if err == nil {
			t.Errorf("status %d: expected an error", status)
			continue
		}
		if errors.Is(err, ErrRefreshTokenInvalid) {
			t.Errorf("status %d must stay retryable, got terminal: %v", status, err)
		}
	}
}

func TestRefreshExternalIDPForm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "x-www-form-urlencoded") {
			t.Errorf("external idp content-type = %q; want form-urlencoded", ct)
		}
		_ = r.ParseForm()
		if r.FormValue("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q", r.FormValue("grant_type"))
		}
		if !strings.Contains(r.FormValue("scope"), "offline_access") {
			t.Errorf("scope missing offline_access: %q", r.FormValue("scope"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"ext-access","refresh_token":"ext-refresh","expires_in":7200}`)
	}))
	defer srv.Close()

	c := &Credentials{
		RefreshToken:  longToken(),
		AuthMethod:    AuthExternalIDP,
		ClientID:      "client-1",
		TokenEndpoint: srv.URL,
		Scopes:        "codewhisperer:conversations",
	}
	// The loopback test server can't pass the Microsoft-domain allowlist, so
	// exercise the POST mechanics directly (endpoint validation is covered by
	// TestValidateExternalIDPEndpoint / TestRefreshExternalIDPRejectsBadEndpoint).
	res, err := postExternalIDPTokenForm(context.Background(), srv.Client(), c)
	if err != nil {
		t.Fatalf("external idp refresh: %v", err)
	}
	if res.AccessToken != "ext-access" || res.RefreshToken != "ext-refresh" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestValidateExternalIDPEndpoint(t *testing.T) {
	ok := []string{
		"https://login.microsoftonline.com/tenant/oauth2/v2.0/token",
		"https://login.microsoftonline.us/t/oauth2/v2.0/token",
		"https://login.partner.microsoftonline.cn/t/oauth2/v2.0/token",
	}
	for _, u := range ok {
		if err := validateExternalIDPEndpoint(u); err != nil {
			t.Errorf("expected %q to be allowed, got %v", u, err)
		}
	}

	bad := map[string]string{
		"http (not https)":     "http://login.microsoftonline.com/t/token",
		"embedded credentials": "https://user:pass@login.microsoftonline.com/t/token",
		"IP literal":           "https://20.190.128.1/t/token",
		"localhost":            "https://localhost/t/token",
		"not allow-listed":     "https://evil.example.com/t/token",
		"lookalike suffix":     "https://login.microsoftonline.com.evil.com/t/token",
	}
	for name, u := range bad {
		if err := validateExternalIDPEndpoint(u); err == nil {
			t.Errorf("%s: expected %q to be rejected", name, u)
		}
	}
}

func TestRefreshExternalIDPRejectsBadEndpoint(t *testing.T) {
	c := &Credentials{
		RefreshToken:  longToken(),
		AuthMethod:    AuthExternalIDP,
		ClientID:      "client-1",
		TokenEndpoint: "https://attacker.example.com/steal",
	}
	_, err := RefreshToken(context.Background(), http.DefaultClient, c, DefaultConfig())
	if err == nil || !strings.Contains(err.Error(), "allow-listed") {
		t.Errorf("expected allow-list rejection, got %v", err)
	}
}

func TestNormalizeScopes(t *testing.T) {
	if got := normalizeScopes(""); got != "" {
		t.Errorf("empty scopes = %q; want empty", got)
	}
	if got := normalizeScopes("a b"); got != "a b offline_access" {
		t.Errorf("scopes = %q; want offline_access appended", got)
	}
	if got := normalizeScopes("x offline_access"); got != "x offline_access" {
		t.Errorf("scopes = %q; should not duplicate offline_access", got)
	}
}

// refreshViaURL invokes a refresh function that hits a test server URL, by
// overriding the URL the social/idc path would use. Social builds the URL from
// region, so we test the response-handling helper directly instead.
func refreshViaURL(t *testing.T, url string, c *Credentials, fn func(int, []byte) (*RefreshResult, error)) (*RefreshResult, error) {
	t.Helper()
	status, body, err := doJSON(context.Background(), http.DefaultClient, url, nil, map[string]string{"refreshToken": c.RefreshToken})
	if err != nil {
		return nil, err
	}
	return fn(status, body)
}
