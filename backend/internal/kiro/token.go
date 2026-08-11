package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

// This file ports kiro-rs kiro::token_manager refresh logic: token-expiry
// detection and the four credential-type refresh flows (api_key is refuseable,
// social / idc / external_idp each hit a different token endpoint). HTTP calls
// take an *http.Client so proxy/TLS policy stays a caller concern (decoupled).

// tokenRefreshSkewMinutes / tokenSoonMinutes mirror kiro-rs 5/10 minute windows.
const (
	tokenExpiredSkewMinutes = 5
	tokenSoonMinutes        = 10
)

// RefreshResult is the outcome of a successful token refresh. Only populated
// fields should overwrite the stored credential.
type RefreshResult struct {
	AccessToken  string
	RefreshToken string // empty => keep existing
	ProfileArn   string // empty => keep existing
	ExpiresAt    string // RFC3339; empty => keep existing
}

// ErrRefreshTokenInvalid marks a permanently-invalid refresh token: OAuth
// invalid_grant or a refresh endpoint's HTTP 401 verdict. Callers should mark
// the credential for re-auth rather than retry indefinitely.
var ErrRefreshTokenInvalid = errors.New("kiro: refresh token permanently invalid (invalid_grant)")

// IsTokenExpiringWithin reports whether the token expires within `minutes`.
// The bool result is only meaningful when ok is true (expires_at parseable).
func IsTokenExpiringWithin(c *Credentials, minutes int) (expiring bool, ok bool) {
	if c.ExpiresAt == "" {
		return false, false
	}
	exp, err := parseExpiresAt(c.ExpiresAt)
	if err != nil {
		return false, false
	}
	return !exp.After(time.Now().Add(time.Duration(minutes) * time.Minute)), true
}

// IsTokenExpired reports whether the token is expired (5-minute early skew).
// Unparseable/missing expiry is treated as expired (conservative).
func IsTokenExpired(c *Credentials) bool {
	expiring, ok := IsTokenExpiringWithin(c, tokenExpiredSkewMinutes)
	if !ok {
		return true
	}
	return expiring
}

// IsTokenExpiringSoon reports whether the token expires within 10 minutes.
// Unparseable/missing expiry is treated as not-soon (matches kiro-rs).
func IsTokenExpiringSoon(c *Credentials) bool {
	expiring, ok := IsTokenExpiringWithin(c, tokenSoonMinutes)
	if !ok {
		return false
	}
	return expiring
}

// parseExpiresAt accepts RFC3339 (with or without nanos).
func parseExpiresAt(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// ValidateRefreshToken performs the basic sanity checks kiro-rs applies before
// attempting a refresh (present, non-empty, not visibly truncated).
func ValidateRefreshToken(c *Credentials) error {
	rt := c.RefreshToken
	if rt == "" {
		return errors.New("kiro: missing refreshToken")
	}
	if len(rt) < 100 || strings.HasSuffix(rt, "...") || strings.Contains(rt, "...") {
		return fmt.Errorf("kiro: refreshToken appears truncated (len=%d)", len(rt))
	}
	return nil
}

// RefreshToken refreshes a credential's access token, dispatching by auth
// method (mirrors kiro-rs refresh_token). API-key credentials are refused.
func RefreshToken(ctx context.Context, client *http.Client, c *Credentials, cfg *Config) (*RefreshResult, error) {
	if c.IsAPIKey() {
		return nil, errors.New("kiro: API Key credentials do not support token refresh")
	}
	if err := ValidateRefreshToken(c); err != nil {
		return nil, err
	}

	method := c.EffectiveAuthMethod()
	switch method {
	case AuthExternalIDP:
		return refreshExternalIDPToken(ctx, client, c, cfg)
	case AuthIDC:
		return refreshIDCToken(ctx, client, c, cfg)
	default:
		return refreshSocialToken(ctx, client, c, cfg)
	}
}

// socialRefreshResponse mirrors kiro-rs RefreshResponse (camelCase).
type socialRefreshResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ProfileArn   string `json:"profileArn"`
	ExpiresIn    *int64 `json:"expiresIn"`
}

// refreshSocialToken refreshes via prod.{region}.auth.desktop.kiro.dev/refreshToken.
func refreshSocialToken(ctx context.Context, client *http.Client, c *Credentials, cfg *Config) (*RefreshResult, error) {
	region := c.EffectiveAuthRegion(cfg)
	url := "https://prod." + region + ".auth.desktop.kiro.dev/refreshToken"
	domain := "prod." + region + ".auth.desktop.kiro.dev"
	machineID := GenerateMachineID(c, "")

	headers := map[string]string{
		"Accept":          "application/json, text/plain, */*",
		"Content-Type":    "application/json",
		"User-Agent":      "KiroIDE-" + cfg.kiroVersion() + "-" + machineID,
		"Accept-Encoding": "gzip, compress, deflate, br",
		"host":            domain,
		"Connection":      "close",
	}
	payload := map[string]string{"refreshToken": c.RefreshToken}

	status, body, err := doJSON(ctx, client, url, headers, payload)
	if err != nil {
		return nil, err
	}
	return parseSocialRefreshResponse(status, body)
}

// parseSocialRefreshResponse handles the social refresh HTTP outcome.
func parseSocialRefreshResponse(status int, body []byte) (*RefreshResult, error) {
	if status < 200 || status >= 300 {
		if isInvalidGrant(status, string(body)) || isTerminalRefreshStatus(status) {
			return nil, fmt.Errorf("%w: social: %d %s", ErrRefreshTokenInvalid, status, string(body))
		}
		return nil, fmt.Errorf("%s: %d %s", httpErrorMessage(status, "OAuth"), status, string(body))
	}
	var data socialRefreshResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	res := &RefreshResult{
		AccessToken:  data.AccessToken,
		RefreshToken: data.RefreshToken,
		ProfileArn:   data.ProfileArn,
	}
	if data.ExpiresIn != nil {
		res.ExpiresAt = time.Now().Add(time.Duration(*data.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	return res, nil
}

// idcRefreshResponse mirrors kiro-rs IdcRefreshResponse (camelCase).
type idcRefreshResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    *int64 `json:"expiresIn"`
	ProfileArn   string `json:"profileArn"`
}

// refreshIDCToken refreshes via AWS SSO OIDC oidc.{region}.amazonaws.com/token.
func refreshIDCToken(ctx context.Context, client *http.Client, c *Credentials, cfg *Config) (*RefreshResult, error) {
	if c.ClientID == "" {
		return nil, errors.New("kiro: IdC refresh requires clientId")
	}
	if c.ClientSecret == "" {
		return nil, errors.New("kiro: IdC refresh requires clientSecret")
	}
	region := c.EffectiveAuthRegion(cfg)
	url := "https://oidc." + region + ".amazonaws.com/token"

	xAmzUA := "aws-sdk-js/3.980.0 KiroIDE"
	userAgent := "aws-sdk-js/3.980.0 ua/2.1 os/" + cfg.systemVersion() +
		" lang/js md/nodejs#" + cfg.nodeVersion() + " api/sso-oidc#3.980.0 m/E KiroIDE"

	headers := map[string]string{
		"content-type":          "application/json",
		"x-amz-user-agent":      xAmzUA,
		"user-agent":            userAgent,
		"host":                  "oidc." + region + ".amazonaws.com",
		"amz-sdk-invocation-id": newInvocationID(),
		"amz-sdk-request":       "attempt=1; max=4",
		"Connection":            "close",
	}
	payload := map[string]string{
		"clientId":     c.ClientID,
		"clientSecret": c.ClientSecret,
		"refreshToken": c.RefreshToken,
		"grantType":    "refresh_token",
	}

	status, body, err := doJSON(ctx, client, url, headers, payload)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		if isInvalidGrant(status, string(body)) || isTerminalRefreshStatus(status) {
			return nil, fmt.Errorf("%w: idc: %d %s", ErrRefreshTokenInvalid, status, string(body))
		}
		return nil, fmt.Errorf("%s: %d %s", httpErrorMessage(status, "IdC"), status, string(body))
	}

	var data idcRefreshResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	res := &RefreshResult{
		AccessToken:  data.AccessToken,
		RefreshToken: data.RefreshToken,
		ProfileArn:   data.ProfileArn,
	}
	if data.ExpiresIn != nil {
		res.ExpiresAt = time.Now().Add(time.Duration(*data.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	return res, nil
}

// oauthTokenResponse is the standard OAuth token endpoint response (snake_case)
// returned by external IdPs (Microsoft Entra / Azure AD).
type oauthTokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        *int64 `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// refreshExternalIDPToken refreshes via the credential's OIDC tokenEndpoint
// using an x-www-form-urlencoded refresh_token grant.
func refreshExternalIDPToken(ctx context.Context, client *http.Client, c *Credentials, cfg *Config) (*RefreshResult, error) {
	if c.ClientID == "" {
		return nil, errors.New("kiro: external IdP refresh requires clientId")
	}
	if c.TokenEndpoint == "" {
		return nil, errors.New("kiro: external IdP refresh requires tokenEndpoint")
	}
	// Defense-in-depth: never POST the refresh token to an unvalidated host.
	// Mirrors kiro-rs post_token_form, which validates the endpoint every call.
	if err := validateExternalIDPEndpoint(c.TokenEndpoint); err != nil {
		return nil, err
	}
	return postExternalIDPTokenForm(ctx, client, c)
}

// postExternalIDPTokenForm performs the external-IdP refresh_token grant POST.
// It assumes the endpoint has already been validated by the caller (so tests
// can exercise the HTTP mechanics against a loopback server directly).
func postExternalIDPTokenForm(ctx context.Context, client *http.Client, c *Credentials) (*RefreshResult, error) {
	form := neturl.Values{}
	form.Set("client_id", c.ClientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", c.RefreshToken)
	if s := normalizeScopes(c.Scopes); s != "" {
		form.Set("scope", s)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var data oauthTokenResponse
	_ = json.Unmarshal(body, &data)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || data.AccessToken == "" {
		// External IdPs commonly report a dead refresh token as either HTTP 401
		// or OAuth invalid_grant (with provider-specific description text). Both
		// require re-auth and must enter the same terminal SetError path as the
		// social/IdC refreshers instead of being retried every cycle.
		terminalInvalidGrant := resp.StatusCode == http.StatusBadRequest &&
			strings.EqualFold(strings.TrimSpace(data.Error), "invalid_grant")
		if isTerminalRefreshStatus(resp.StatusCode) || terminalInvalidGrant {
			detail := strings.TrimSpace(strings.TrimSpace(data.Error) + " " + strings.TrimSpace(data.ErrorDescription))
			if detail == "" {
				detail = strings.TrimSpace(string(body))
			}
			return nil, fmt.Errorf("%w: external_idp: %d %s", ErrRefreshTokenInvalid, resp.StatusCode, detail)
		}
		if data.Error != "" {
			return nil, fmt.Errorf("kiro: external IdP token exchange failed (%d): %s %s",
				resp.StatusCode, data.Error, data.ErrorDescription)
		}
		return nil, fmt.Errorf("kiro: external IdP token exchange failed (%d): %s", resp.StatusCode, string(body))
	}

	res := &RefreshResult{
		AccessToken:  data.AccessToken,
		RefreshToken: data.RefreshToken,
	}
	if data.ExpiresIn != nil {
		res.ExpiresAt = time.Now().Add(time.Duration(*data.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	return res, nil
}

// externalIDPAllowedSuffixes lists the Microsoft Entra / Azure AD token hosts
// (global + US-gov + China national clouds), mirroring kiro-rs ALLOWED_SUFFIXES.
// Kiro's "external IdP" support is Microsoft-specific by design.
var externalIDPAllowedSuffixes = []string{
	".microsoftonline.com",
	".microsoftonline.us",
	".microsoftonline.cn",
}

// validateExternalIDPEndpoint enforces the same guarantees as kiro-rs
// validate_external_idp_endpoint before a refresh token is transmitted: https
// only, no embedded credentials, a real (non-IP, non-localhost) host, and an
// allow-listed Microsoft domain suffix.
func validateExternalIDPEndpoint(rawURL string) error {
	u, err := neturl.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("kiro: invalid external IdP URL: %w", err)
	}
	if u.Scheme != "https" {
		return errors.New("kiro: external IdP URL must be https")
	}
	if u.User != nil {
		return errors.New("kiro: external IdP URL must not contain credentials")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return errors.New("kiro: external IdP URL has no host")
	}
	if net.ParseIP(host) != nil {
		return errors.New("kiro: external IdP host must not be an IP literal")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return errors.New("kiro: external IdP host must not be localhost")
	}
	for _, s := range externalIDPAllowedSuffixes {
		if strings.HasSuffix(host, s) {
			return nil
		}
	}
	return fmt.Errorf("kiro: external IdP host %q is not allow-listed", host)
}

// normalizeScopes ensures offline_access is present (mirrors kiro-rs).
func normalizeScopes(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	fields := strings.Fields(input)
	hasOffline := false
	for _, f := range fields {
		if f == "offline_access" {
			hasOffline = true
			break
		}
	}
	if !hasOffline {
		fields = append(fields, "offline_access")
	}
	return strings.Join(fields, " ")
}

// doJSON posts a JSON body and returns status + raw body bytes.
func doJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, payload any) (int, []byte, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// isInvalidGrant reports the permanent-failure signature (400 + invalid_grant).
func isInvalidGrant(status int, body string) bool {
	return status == http.StatusBadRequest &&
		strings.Contains(body, `"invalid_grant"`) &&
		strings.Contains(body, "Invalid refresh token provided")
}

// isTerminalRefreshStatus reports whether a non-2xx refresh status means the
// refresh_token itself is rejected and will not recover without re-auth.
//
// 401 qualifies: the refresh endpoints authenticate the request *by* the
// refresh_token, so "unauthorized" is a verdict on the credential, not on
// request shape or transient capacity. httpErrorMessage already labels 401 as
// "re-auth required"; treating it as retryable contradicted that and left dead
// credentials looping on every cycle forever.
//
// Deliberately excluded: 403 (upstream permission/policy, can be restored
// server-side without touching the credential), 429 and 5xx (transient).
func isTerminalRefreshStatus(status int) bool {
	return status == http.StatusUnauthorized
}

// httpErrorMessage maps a status code to a human message (mirrors kiro-rs).
func httpErrorMessage(status int, service string) string {
	switch {
	case status == 401:
		return service + " credential expired or invalid, re-auth required"
	case status == 403:
		return "insufficient permission to refresh token"
	case status == 429:
		return "rate limited"
	case status >= 500 && status <= 599:
		return "server error, upstream auth service temporarily unavailable"
	default:
		return "token refresh failed"
	}
}
