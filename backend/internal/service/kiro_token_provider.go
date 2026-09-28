package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyutil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
)

// kiroRefreshHTTPTimeout bounds an OAuth token-refresh round trip.
const kiroRefreshHTTPTimeout = 30 * time.Second

// A persistence failure must not be mistaken for a transient OAuth failure:
// never fall back to a stale or uncommitted bearer token in this case.
var errKiroCredentialPersistence = errors.New("kiro: credential persistence failed")

// KiroTokenProvider resolves a valid Kiro bearer token for an account, driving
// request-time lazy OAuth refresh for social/idc/external_idp credentials and
// returning the static ksk_* key for api_key credentials. Token refresh HTTP
// goes through a per-account proxied client (mirrors the OAuth service pattern),
// independent of the gateway upstream connection pool. Refreshed credentials are
// persisted back to the account so subsequent requests reuse them.
type KiroTokenProvider struct {
	accountRepo AccountRepository
	// Fixed stripes bound memory even with many accounts; do not retain secrets.
	profileLocks  [64]sync.Mutex
	cacheMu       sync.Mutex
	emptyProfiles map[int64]kiroEmptyProfileCache
	profileClient func(string) (*http.Client, error) // optional test seam
	refreshClient func(string) (*http.Client, error) // optional test seam
}

type kiroEmptyProfileCache struct {
	tokenHash      [32]byte
	proxyHash      [32]byte
	credentialHash [32]byte
	until          time.Time
}

// Optional narrow contracts keep the shared AccountRepository interface intact.
type kiroProfileWriter interface {
	UpdateKiroProfileArnIfUnchanged(context.Context, int64, map[string]any, *int64, string) (bool, error)
}

type kiroRefreshWriter interface {
	UpdateKiroRefreshedCredentialsIfUnchanged(context.Context, int64, map[string]any, *int64, map[string]any) (bool, error)
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
			if errors.Is(err, kiro.ErrRefreshTokenInvalid) || errors.Is(err, errKiroCredentialPersistence) || cred.AccessToken == "" {
				return nil, "", err
			}
			// Otherwise fall through with the stale token: the provider's
			// upstream force-refresh path gets a second chance.
		} else if token != "" {
			return p.ensureProfile(ctx, account, cred, token)
		}
	}

	if cred.AccessToken == "" {
		return nil, "", errors.New("kiro: no access_token available")
	}
	return p.ensureProfile(ctx, account, cred, cred.AccessToken)
}

// ForceRefresh unconditionally refreshes the credential, used as the upstream
// provider's mid-request force-refresh hook when the bearer token is rejected.
func (p *KiroTokenProvider) ForceRefresh(ctx context.Context, account *Account, cred *kiro.Credentials) (string, error) {
	if cred.IsAPIKey() {
		return "", errors.New("kiro: api_key credential cannot refresh")
	}
	token, err := p.refreshAndPersist(ctx, account, cred)
	if err != nil {
		return "", err
	}
	_, token, err = p.ensureProfile(ctx, account, cred, token)
	return token, err
}

func sameKiroProxy(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Compare the entire credential identity, not just its bearer/refresh tokens.
// Only a concurrently discovered profile may justify retrying a stale CAS.
func sameKiroCredentialsExceptProfile(a, b map[string]any) bool {
	withoutProfile := func(src map[string]any) map[string]any {
		out := make(map[string]any, len(src))
		for key, value := range src {
			if key != "profile_arn" && key != "profileArn" {
				out[key] = value
			}
		}
		return out
	}
	left, leftErr := json.Marshal(withoutProfile(a))
	right, rightErr := json.Marshal(withoutProfile(b))
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

// Hash both proxy ID and URL so an in-place proxy edit invalidates negative
// discovery evidence without retaining the URL or its authentication secrets.
func kiroProfileProxyHash(account *Account) [32]byte {
	id := "none"
	if account.ProxyID != nil {
		id = strconv.FormatInt(*account.ProxyID, 10)
	}
	return sha256.Sum256([]byte(id + "\x00" + resolveAccountProxyURL(account)))
}

// ensureProfile resolves missing IDE profiles using the same account proxy as
// token refresh. Only a complete, successful empty scan authorizes fallback.
func (p *KiroTokenProvider) ensureProfile(ctx context.Context, account *Account, cred *kiro.Credentials, token string) (*kiro.Credentials, string, error) {
	if cred.IsAPIKey() || strings.EqualFold(cred.Endpoint, kiro.EndpointCLI) || account == nil {
		return cred, token, nil
	}
	if arn := cred.EffectiveProfileArn(); arn != "" {
		if region := strings.TrimSpace(cred.APIRegion); region != "" && strings.Split(arn, ":")[3] != region {
			return nil, "", errors.New("kiro: configured api_region conflicts with profile ARN region")
		}
		return cred, token, nil
	}
	// A prior token's confirmation must not survive a refresh or a failed scan.
	cred.ProfileScanConfirmed = false
	lock := &p.profileLocks[uint64(account.ID)%uint64(len(p.profileLocks))]
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	key := sha256.Sum256([]byte(token))
	proxyHash := kiroProfileProxyHash(account)
	credentialJSON, jsonErr := json.Marshal(account.Credentials)
	if jsonErr != nil {
		return nil, "", errors.New("kiro: cannot verify profile discovery credential identity")
	}
	credentialHash := sha256.Sum256(credentialJSON)
	p.cacheMu.Lock()
	cached, ok := p.emptyProfiles[account.ID]
	p.cacheMu.Unlock()
	if ok && cached.tokenHash == key && cached.proxyHash == proxyHash && cached.credentialHash == credentialHash && time.Now().Before(cached.until) && !kiro.IsTokenExpired(cred) {
		cred.ProfileScanConfirmed = true
		return cred, token, nil
	}
	buildClient := p.profileClient
	if buildClient == nil {
		buildClient = newKiroRefreshClient
	}
	client, err := buildClient(resolveAccountProxyURL(account))
	if err != nil {
		return nil, "", fmt.Errorf("kiro: build profile discovery client: %w", err)
	}
	profiles, err := kiro.ListAvailableProfiles(ctx, client, cred, kiro.DefaultConfig(), token)
	if err != nil {
		return nil, "", fmt.Errorf("kiro: profile discovery incomplete: %w", err)
	}
	if len(profiles) == 0 {
		if cred.EffectiveAuthMethod() != kiro.AuthIDC && cred.EffectiveAuthMethod() != kiro.AuthSocial {
			return nil, "", errors.New("kiro: no profile available for credential")
		}
		if kiro.IsTokenExpired(cred) {
			return nil, "", errors.New("kiro: cannot confirm empty profiles with expired token")
		}
		cred.ProfileScanConfirmed = true
		p.cacheMu.Lock()
		if p.emptyProfiles == nil || len(p.emptyProfiles) >= 1024 {
			p.emptyProfiles = make(map[int64]kiroEmptyProfileCache)
		}
		p.emptyProfiles[account.ID] = kiroEmptyProfileCache{tokenHash: key, proxyHash: proxyHash, credentialHash: credentialHash, until: time.Now().Add(3 * time.Minute)}
		p.cacheMu.Unlock()
		return cred, token, nil
	}
	arn, err := kiro.SelectProfileArn(profiles, cred)
	if err != nil {
		return nil, "", err
	}
	if account.IsCredentialShadow() {
		return nil, "", errors.New("kiro: cannot persist discovered profile to shadow account")
	}
	writer, ok := any(p.accountRepo).(kiroProfileWriter)
	if !ok {
		return nil, "", errors.New("kiro: profile discovery requires atomic credential persistence")
	}
	{
		written, writeErr := writer.UpdateKiroProfileArnIfUnchanged(ctx, account.ID, account.Credentials, account.ProxyID, arn)
		if writeErr != nil {
			return nil, "", fmt.Errorf("kiro: persist discovered profile: %w", writeErr)
		}
		if !written {
			// CAS miss: never use a profile discovered for an obsolete token.
			current, readErr := p.accountRepo.GetByID(ctx, account.ID)
			if readErr != nil || current == nil {
				return nil, "", errors.New("kiro: profile changed concurrently; retry account resolution")
			}
			if current.ID != account.ID || current.Type != account.Type || current.Platform != account.Platform ||
				current.IsCredentialShadow() || !reflect.DeepEqual(current.Extra, account.Extra) ||
				!sameKiroProxy(current.ProxyID, account.ProxyID) || kiroProfileProxyHash(current) != proxyHash ||
				!sameKiroCredentialsExceptProfile(account.Credentials, current.Credentials) {
				return nil, "", errors.New("kiro: account identity changed during profile discovery; retry account resolution")
			}
			fresh := kiro.ParseCredentials(current.ID, current.Credentials, current.Extra)
			if fresh.AccessToken != token {
				return nil, "", errors.New("kiro: bearer changed during profile discovery; retry account resolution")
			}
			if fresh.EffectiveProfileArn() == "" {
				return nil, "", errors.New("kiro: credentials changed during profile discovery; retry account resolution")
			}
			arn = fresh.EffectiveProfileArn()
		} else {
			account.Credentials = mergeKiroRefreshedCredentials(account.Credentials, &kiro.RefreshResult{ProfileArn: arn})
		}
	}
	if region := strings.TrimSpace(cred.APIRegion); region != "" && strings.Split(arn, ":")[3] != region {
		return nil, "", errors.New("kiro: configured api_region conflicts with profile ARN region")
	}
	cred.ProfileArn = arn
	cred.ProfileScanConfirmed = false
	return cred, token, nil
}

// refreshAndPersist refreshes the credential via its OAuth token endpoint,
// applies the non-empty result fields onto cred in place, and persists the
// merged credentials back to the account. Persistence failure is fatal: do
// not return a bearer token whose rotation was not committed.
func (p *KiroTokenProvider) refreshAndPersist(ctx context.Context, account *Account, cred *kiro.Credentials) (string, error) {
	if account == nil || account.IsCredentialShadow() || p.accountRepo == nil {
		return "", fmt.Errorf("%w: invalid credential owner", errKiroCredentialPersistence)
	}
	buildClient := p.refreshClient
	if buildClient == nil {
		buildClient = newKiroRefreshClient
	}
	client, err := buildClient(resolveAccountProxyURL(account))
	if err != nil {
		return "", fmt.Errorf("kiro: build refresh client: %w", err)
	}

	expectedCredentials := account.Credentials
	result, err := kiro.RefreshToken(ctx, client, cred, kiro.DefaultConfig())
	if err != nil {
		return "", err
	}
	if result == nil || result.AccessToken == "" {
		return "", errors.New("kiro: token refresh response missing access_token")
	}

	// A refresh response cannot demote a known real profile to a compatibility
	// placeholder or malformed ARN.
	if result.ProfileArn != "" && (&kiro.Credentials{ProfileArn: result.ProfileArn}).EffectiveProfileArn() == "" {
		result.ProfileArn = ""
	}
	// Defer all in-memory mutation until durable CAS/persistence succeeds.
	updated := mergeKiroRefreshedCredentials(expectedCredentials, result)
	chosenProfileArn := result.ProfileArn
	if writer, ok := any(p.accountRepo).(kiroRefreshWriter); ok && !account.IsCredentialShadow() {
		fields := map[string]any{"access_token": result.AccessToken}
		if result.RefreshToken != "" {
			fields["refresh_token"] = result.RefreshToken
		}
		if result.ProfileArn != "" {
			fields["profile_arn"] = result.ProfileArn
		}
		if result.ExpiresAt != "" {
			fields["expires_at"] = result.ExpiresAt
		}
		written, writeErr := writer.UpdateKiroRefreshedCredentialsIfUnchanged(ctx, account.ID, expectedCredentials, account.ProxyID, fields)
		if !written && writeErr == nil {
			// Retry only a profile-only CAS collision. Admin edits to auth method,
			// client ID, region, expiry, endpoint or other credential identity must
			// never receive a refresh result computed from the old snapshot.
			if current, readErr := p.accountRepo.GetByID(ctx, account.ID); readErr == nil && current != nil &&
				current.ID == account.ID && current.Type == account.Type && current.Platform == account.Platform &&
				reflect.DeepEqual(current.Extra, account.Extra) && sameKiroProxy(current.ProxyID, account.ProxyID) && resolveAccountProxyURL(current) == resolveAccountProxyURL(account) &&
				sameKiroCredentialsExceptProfile(expectedCredentials, current.Credentials) {
				fresh := kiro.ParseCredentials(current.ID, current.Credentials, current.Extra)
				if fresh.EffectiveProfileArn() != "" {
					delete(fields, "profile_arn")
				}
				written, writeErr = writer.UpdateKiroRefreshedCredentialsIfUnchanged(ctx, account.ID, current.Credentials, current.ProxyID, fields)
				if written {
					updated = mergeKiroRefreshedCredentials(current.Credentials, result)
					if fresh.EffectiveProfileArn() != "" {
						updated["profile_arn"] = fresh.ProfileArn
						chosenProfileArn = fresh.ProfileArn
					}
				}
			}
		}
		if writeErr != nil {
			return "", fmt.Errorf("%w: repository write failed", errKiroCredentialPersistence)
		}
		if !written {
			return "", fmt.Errorf("%w: credentials changed concurrently; retry account resolution", errKiroCredentialPersistence)
		}
		account.Credentials = updated
	} else {
		if err := persistAccountCredentials(ctx, p.accountRepo, account, updated); err != nil {
			account.Credentials = expectedCredentials
			return "", fmt.Errorf("%w: legacy credential write failed", errKiroCredentialPersistence)
		}
	}
	cred.AccessToken = result.AccessToken
	if result.RefreshToken != "" {
		cred.RefreshToken = result.RefreshToken
	}
	if chosenProfileArn != "" {
		cred.ProfileArn = chosenProfileArn
	}
	if result.ExpiresAt != "" {
		cred.ExpiresAt = result.ExpiresAt
	}
	cred.ProfileScanConfirmed = false
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
