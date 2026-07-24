package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyutil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
)

// kiroRefreshHTTPTimeout bounds an OAuth token-refresh round trip.
const kiroRefreshHTTPTimeout = 30 * time.Second

// KiroTokenProvider resolves a valid Kiro bearer token for an account, driving
// request-time lazy OAuth refresh for social/idc/external_idp credentials and
// returning the static ksk_* key for api_key credentials. Token refresh HTTP
// goes through a per-account proxied client (mirrors the OAuth service pattern),
// independent of the gateway upstream connection pool. Refreshed credentials are
// persisted back to the account so subsequent requests reuse them.
type KiroTokenProvider struct {
	accountRepo AccountRepository
}

// NewKiroTokenProvider builds a KiroTokenProvider.
func NewKiroTokenProvider(accountRepo AccountRepository) *KiroTokenProvider {
	return &KiroTokenProvider{accountRepo: accountRepo}
}

// Resolve parses the account's Kiro credentials and returns the parsed
// credential set plus the effective bearer token. For api_key credentials the
// token is the ksk_* key. For native OAuth credentials it refreshes lazily when
// the stored token is expired (5-minute skew), persisting the new credentials.
func (p *KiroTokenProvider) Resolve(ctx context.Context, account *Account) (*kiro.Credentials, string, error) {
	cred := kiro.ParseCredentials(account.ID, account.Credentials, account.Extra)

	// API-key credentials use the ksk_* key directly; no refresh possible.
	if cred.IsAPIKey() {
		if cred.KiroAPIKey == "" {
			return nil, "", errors.New("kiro: api_key credential missing kiro_api_key")
		}
		return cred, cred.KiroAPIKey, nil
	}

	// Native OAuth credentials: refresh lazily when expired.
	if kiro.IsTokenExpired(cred) {
		token, err := p.refreshAndPersist(ctx, account, cred)
		if err != nil {
			// A permanently-invalid refresh token will never recover on this
			// account; surface the error so the gateway fails over immediately
			// instead of wasting an upstream round trip on a stale token.
			if errors.Is(err, kiro.ErrRefreshTokenInvalid) || cred.AccessToken == "" {
				return nil, "", err
			}
			// Otherwise fall through with the stale token: the provider's
			// upstream force-refresh path gets a second chance.
		} else if token != "" {
			return cred, token, nil
		}
	}

	if cred.AccessToken == "" {
		return nil, "", errors.New("kiro: no access_token available")
	}
	return cred, cred.AccessToken, nil
}

// ForceRefresh unconditionally refreshes the credential, used as the upstream
// provider's mid-request force-refresh hook when the bearer token is rejected.
func (p *KiroTokenProvider) ForceRefresh(ctx context.Context, account *Account, cred *kiro.Credentials) (string, error) {
	if cred.IsAPIKey() {
		return "", errors.New("kiro: api_key credential cannot refresh")
	}
	return p.refreshAndPersist(ctx, account, cred)
}

// refreshAndPersist refreshes the credential via its OAuth token endpoint,
// applies the non-empty result fields onto cred in place, and persists the
// merged credentials back to the account. Persistence failure is non-fatal for
// the current request (the in-memory token is still fresh).
func (p *KiroTokenProvider) refreshAndPersist(ctx context.Context, account *Account, cred *kiro.Credentials) (string, error) {
	client, err := newKiroRefreshClient(resolveAccountProxyURL(account))
	if err != nil {
		return "", fmt.Errorf("kiro: build refresh client: %w", err)
	}

	result, err := kiro.RefreshToken(ctx, client, cred, kiro.DefaultConfig())
	if err != nil {
		return "", err
	}

	// Apply refreshed fields in place (mirrors kiro-rs: only populated fields
	// overwrite the stored credential).
	cred.AccessToken = result.AccessToken
	if result.RefreshToken != "" {
		cred.RefreshToken = result.RefreshToken
	}
	if result.ProfileArn != "" {
		cred.ProfileArn = result.ProfileArn
	}
	if result.ExpiresAt != "" {
		cred.ExpiresAt = result.ExpiresAt
	}

	updated := mergeKiroRefreshedCredentials(account.Credentials, result)
	_ = persistAccountCredentials(ctx, p.accountRepo, account, updated)
	return cred.AccessToken, nil
}

// mergeKiroRefreshedCredentials returns a copy of existing with the refreshed
// token fields overlaid (snake_case, sub2api convention). Empty result fields
// leave the existing value untouched.
func mergeKiroRefreshedCredentials(existing map[string]any, r *kiro.RefreshResult) map[string]any {
	merged := make(map[string]any, len(existing)+4)
	for k, v := range existing {
		merged[k] = v
	}
	merged["access_token"] = r.AccessToken
	if r.RefreshToken != "" {
		merged["refresh_token"] = r.RefreshToken
	}
	if r.ProfileArn != "" {
		merged["profile_arn"] = r.ProfileArn
	}
	if r.ExpiresAt != "" {
		merged["expires_at"] = r.ExpiresAt
	}
	return merged
}

// newKiroRefreshClient builds an HTTP client for token refresh, honoring the
// account proxy (mirrors newVertexServiceAccountHTTPClient).
func newKiroRefreshClient(proxyURL string) (*http.Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return servertiming.InstrumentClient(&http.Client{Timeout: kiroRefreshHTTPTimeout}), nil
	}
	_, parsedProxy, err := proxyurl.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("unexpected default transport type %T", http.DefaultTransport)
	}
	transport := defaultTransport.Clone()
	transport.Proxy = nil
	if err := proxyutil.ConfigureTransportProxy(transport, parsedProxy); err != nil {
		return nil, err
	}
	return servertiming.InstrumentClient(&http.Client{Timeout: kiroRefreshHTTPTimeout, Transport: transport}), nil
}
