package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/kiro"
)

type kiroProfileRepoStub struct {
	AccountRepository
	calls   int
	profile string
	written bool
	current *Account
}

func (r *kiroProfileRepoStub) GetByID(context.Context, int64) (*Account, error) {
	return r.current, nil
}

func (r *kiroProfileRepoStub) UpdateKiroProfileArnIfUnchanged(_ context.Context, _ int64, _ map[string]any, _ *int64, arn string) (bool, error) {
	r.calls++
	r.profile = arn
	return r.written, nil
}

type kiroRefreshRepoStub struct {
	AccountRepository
	written    bool
	writeErr   error
	calls      int
	current    *Account
	retry      bool
	lastFields map[string]any
}

func (r *kiroRefreshRepoStub) GetByID(context.Context, int64) (*Account, error) {
	return r.current, nil
}

func (r *kiroRefreshRepoStub) UpdateKiroRefreshedCredentialsIfUnchanged(_ context.Context, _ int64, _ map[string]any, _ *int64, fields map[string]any) (bool, error) {
	r.calls++
	r.lastFields = make(map[string]any, len(fields))
	for k, v := range fields {
		r.lastFields[k] = v
	}
	return r.written || r.retry && r.calls == 2, r.writeErr
}

type kiroLegacyRefreshRepoStub struct {
	AccountRepository
	calls int
}

func (r *kiroLegacyRefreshRepoStub) UpdateCredentials(context.Context, int64, map[string]any) error {
	r.calls++
	return errors.New("simulated storage failure")
}

type kiroProfileTransport func(*http.Request) (*http.Response, error)

func (f kiroProfileTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func kiroRefreshFailureAccount() *Account {
	return &Account{ID: 5003, Platform: PlatformAnthropic, Type: AccountTypeKiro, Credentials: map[string]any{
		"auth_method": "social", "refresh_token": strings.Repeat("r", 120),
		"access_token": "old-token", "expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339),
	}}
}

func stubKiroRefreshClient(p *KiroTokenProvider) {
	p.refreshClient = func(string) (*http.Client, error) {
		return &http.Client{Transport: kiroProfileTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"accessToken":"new-token","expiresIn":3600}`)), Header: make(http.Header)}, nil
		})}, nil
	}
}

func TestKiroResolveRefreshPersistenceFailureIsFatal(t *testing.T) {
	for _, tc := range []struct {
		name string
		repo AccountRepository
	}{
		{"CAS miss", &kiroRefreshRepoStub{}},
		{"repository error", &kiroRefreshRepoStub{writeErr: errors.New("simulated storage failure")}},
		{"legacy fallback error", &kiroLegacyRefreshRepoStub{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewKiroTokenProvider(tc.repo)
			stubKiroRefreshClient(p)
			p.profileClient = func(string) (*http.Client, error) {
				t.Fatal("uncommitted bearer must not reach profile discovery")
				return nil, nil
			}
			account := kiroRefreshFailureAccount()
			cred, token, err := p.Resolve(context.Background(), account)
			if !errors.Is(err, errKiroCredentialPersistence) || cred != nil || token != "" {
				t.Fatalf("uncommitted token must fail closed: error=%v returnedCredential=%t returnedToken=%t", err, cred != nil, token != "")
			}
			if account.Credentials["access_token"] != "old-token" {
				t.Fatal("failed credential write mutated account snapshot")
			}
		})
	}
}

func TestKiroRefreshCASRetriesOnlyProfileChange(t *testing.T) {
	for _, tc := range []struct {
		name       string
		key        string
		value      string
		allowRetry bool
	}{
		{"profile only", "", "", true},
		{"client id edit", "client_id", "new-client", false},
		{"auth method edit", "auth_method", "idc", false},
		{"region edit", "region", "eu-central-1", false},
		{"expiry edit", "expires_at", "2030-01-01T00:00:00Z", false},
		{"endpoint edit", "endpoint", "cli", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := kiroRefreshFailureAccount()
			current := &Account{ID: account.ID, Platform: account.Platform, Type: account.Type, Credentials: map[string]any{}}
			for key, value := range account.Credentials {
				current.Credentials[key] = value
			}
			profile := "arn:aws:codewhisperer:us-east-1:123456789012:profile/FOUND"
			current.Credentials["profile_arn"] = profile
			if tc.key != "" {
				current.Credentials[tc.key] = tc.value
			}
			repo := &kiroRefreshRepoStub{current: current, retry: true}
			provider := NewKiroTokenProvider(repo)
			stubKiroRefreshClient(provider)
			cred, token, err := provider.Resolve(context.Background(), account)
			if tc.allowRetry {
				if err != nil || cred == nil || token != "new-token" || repo.calls != 2 || cred.EffectiveProfileArn() != profile || repo.lastFields["profile_arn"] != nil {
					t.Fatalf("profile-only retry failed: error=%v returnedCredential=%t calls=%d", err, cred != nil, repo.calls)
				}
			} else if !errors.Is(err, errKiroCredentialPersistence) || cred != nil || token != "" || repo.calls != 1 {
				t.Fatalf("admin credential edit must abort stale refresh: error=%v returnedCredential=%t calls=%d", err, cred != nil, repo.calls)
			}
		})
	}
}

func TestKiroRefreshCASRejectsExtraIdentityChange(t *testing.T) {
	account := kiroRefreshFailureAccount()
	current := &Account{ID: account.ID, Type: account.Type, Platform: account.Platform, Extra: map[string]any{"profile_arn": "changed"}, Credentials: map[string]any{}}
	for key, value := range account.Credentials {
		current.Credentials[key] = value
	}
	current.Credentials["profile_arn"] = "arn:aws:codewhisperer:us-east-1:123456789012:profile/FOUND"
	repo := &kiroRefreshRepoStub{current: current, retry: true}
	provider := NewKiroTokenProvider(repo)
	stubKiroRefreshClient(provider)
	cred, token, err := provider.Resolve(context.Background(), account)
	if !errors.Is(err, errKiroCredentialPersistence) || cred != nil || token != "" || repo.calls != 1 {
		t.Fatalf("extra identity edit must abort stale refresh: error=%v calls=%d", err, repo.calls)
	}
}

func TestKiroResolveRefreshCASCommitsOnAnthropicKiro(t *testing.T) {
	repo := &kiroRefreshRepoStub{written: true}
	p := NewKiroTokenProvider(repo)
	stubKiroRefreshClient(p)
	account := kiroRefreshFailureAccount()
	account.Credentials["endpoint"] = "cli"
	cred, token, err := p.Resolve(context.Background(), account)
	if err != nil || cred == nil || token != "new-token" || repo.calls != 1 || account.Credentials["access_token"] != "new-token" {
		t.Fatalf("type=kiro refresh did not commit: error=%v credential=%t writeCount=%d committed=%t", err, cred != nil, repo.calls, account.Credentials["access_token"] == "new-token")
	}
}

func TestKiroResolveDiscoversProfileAndPersists(t *testing.T) {
	repo := &kiroProfileRepoStub{written: true}
	provider := NewKiroTokenProvider(repo)
	var requests int
	provider.profileClient = func(proxy string) (*http.Client, error) {
		if proxy != "" {
			t.Fatalf("unexpected proxy: %q", proxy)
		}
		return &http.Client{Transport: kiroProfileTransport(func(req *http.Request) (*http.Response, error) {
			requests++
			body := `{"profiles":[]}`
			if strings.Contains(req.URL.Host, "eu-central-1") {
				body = `{"profiles":[{"profileArn":"arn:aws:codewhisperer:eu-central-1:123456789012:profile/FOUND"}]}`
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}, nil
	}
	account := &Account{ID: 501, Platform: PlatformAnthropic, Type: AccountTypeKiro, Credentials: map[string]any{
		"auth_method": "idc", "access_token": "safe-token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	}}
	cred, token, err := provider.Resolve(context.Background(), account)
	if err != nil || cred == nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if token != "safe-token" || cred.EffectiveProfileArn() != repo.profile || repo.calls != 1 || requests != 2 {
		t.Fatalf("resolve: profile=%q writes=%d requests=%d", cred.EffectiveProfileArn(), repo.calls, requests)
	}
	if account.Credentials["profile_arn"] != repo.profile {
		t.Fatal("updated account snapshot lacks profile")
	}
}

func TestKiroResolveDiscoveredProfileRequiresAtomicWriter(t *testing.T) {
	repo := &kiroLegacyRefreshRepoStub{}
	provider := NewKiroTokenProvider(repo)
	provider.profileClient = func(string) (*http.Client, error) {
		return &http.Client{Transport: kiroProfileTransport(func(*http.Request) (*http.Response, error) {
			body := `{"profiles":[{"profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/FOUND"}]}`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}, nil
	}
	account := &Account{ID: 509, Platform: PlatformAnthropic, Type: AccountTypeKiro, Credentials: map[string]any{
		"auth_method": "idc", "access_token": "safe-token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	}}
	cred, token, err := provider.Resolve(context.Background(), account)
	if err == nil || !strings.Contains(err.Error(), "atomic credential persistence") || cred != nil || token != "" || repo.calls != 0 {
		t.Fatalf("missing atomic writer must fail without legacy writes: error=%v credential=%t writes=%d", err, cred != nil, repo.calls)
	}
	if account.Credentials["profile_arn"] != nil {
		t.Fatal("discovered ARN must not mutate account without persistence")
	}
}

func TestKiroResolveDoesNotPersistProfileFromPartialScan(t *testing.T) {
	repo := &kiroProfileRepoStub{written: true}
	provider := NewKiroTokenProvider(repo)
	var requests int
	provider.profileClient = func(string) (*http.Client, error) {
		return &http.Client{Transport: kiroProfileTransport(func(req *http.Request) (*http.Response, error) {
			requests++
			if strings.Contains(req.URL.Host, "us-east-1") {
				body := `{"profiles":[{"profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/PARTIAL"}]}`
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			}
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{"message":"sensitive"}`)), Header: make(http.Header)}, nil
		})}, nil
	}
	account := &Account{ID: 508, Platform: PlatformAnthropic, Type: AccountTypeKiro, Credentials: map[string]any{
		"auth_method": "idc", "access_token": "safe-token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	}}
	cred, token, err := provider.Resolve(context.Background(), account)
	if err == nil || cred != nil || token != "" || repo.calls != 0 || requests != 2 || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("partial discovery must not persist/bypass: error=%v writes=%d requests=%d", err, repo.calls, requests)
	}
}

func TestKiroResolveOnlyUsesPlaceholderAfterCompleteEmptyScan(t *testing.T) {
	provider := NewKiroTokenProvider(&kiroProfileRepoStub{written: true})
	var requests int
	status := http.StatusOK
	provider.profileClient = func(string) (*http.Client, error) {
		return &http.Client{Transport: kiroProfileTransport(func(_ *http.Request) (*http.Response, error) {
			requests++
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"profiles":[]}`)), Header: make(http.Header)}, nil
		})}, nil
	}
	account := &Account{ID: 502, Platform: PlatformAnthropic, Type: AccountTypeKiro, Credentials: map[string]any{
		"auth_method": "idc", "access_token": "safe-token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	}}
	cred, _, err := provider.Resolve(context.Background(), account)
	if err != nil || cred == nil {
		t.Fatalf("empty scan failed: %v", err)
	}
	if cred.StreamingProfileArn() != kiro.BuilderIDProfileArn || cred.EffectiveProfileArn() != "" || requests != 2 {
		t.Fatalf("empty scan: profile=%q requests=%d", cred.StreamingProfileArn(), requests)
	}
	_, _, err = provider.Resolve(context.Background(), account)
	if err != nil || requests != 2 {
		t.Fatalf("negative cache miss: error=%v requests=%d", err, requests)
	}
	status = http.StatusBadGateway
	account.ID++ // bypass negative cache
	cred, _, err = provider.Resolve(context.Background(), account)
	if err == nil || cred != nil {
		t.Fatalf("partial failure must not permit fallback: %v", err)
	}
}

func TestKiroEmptyProfileCacheTracksProxyIdentity(t *testing.T) {
	provider := NewKiroTokenProvider(&kiroProfileRepoStub{})
	requests := 0
	provider.profileClient = func(string) (*http.Client, error) {
		return &http.Client{Transport: kiroProfileTransport(func(*http.Request) (*http.Response, error) {
			requests++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"profiles":[]}`)), Header: make(http.Header)}, nil
		})}, nil
	}
	account := &Account{ID: 507, Platform: PlatformAnthropic, Type: AccountTypeKiro, Credentials: map[string]any{
		"auth_method": "idc", "access_token": "safe-token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	}}
	resolve := func(want int) {
		t.Helper()
		cred, _, err := provider.Resolve(context.Background(), account)
		if err != nil || cred == nil || cred.StreamingProfileArn() != kiro.BuilderIDProfileArn || requests != want {
			t.Fatalf("negative cache proxy invalidation failed: error=%v requests=%d want=%d", err, requests, want)
		}
	}
	resolve(2)
	resolve(2) // same token + credentials + proxy: cached
	proxyID := int64(77)
	account.ProxyID = &proxyID
	account.Proxy = &Proxy{Protocol: "http", Host: "proxy.example", Port: 3128, Username: "u", Password: "secret-A"}
	resolve(4) // changing proxy ID invalidates previous negative result
	resolve(4)
	account.Proxy.Password = "secret-B"
	resolve(6) // editing URL/credentials of same proxy ID also invalidates
	account.Credentials["client_id"] = "new-client"
	resolve(8) // same bearer with changed credential identity must also rescan
}

func TestKiroResolveRejectsProfileFromChangedToken(t *testing.T) {
	repo := &kiroProfileRepoStub{current: &Account{ID: 504, Platform: PlatformAnthropic, Type: AccountTypeKiro, Credentials: map[string]any{
		"access_token": "rotated-token", "profile_arn": "arn:aws:codewhisperer:us-east-1:123456789012:profile/NEW",
	}}}
	provider := NewKiroTokenProvider(repo)
	provider.profileClient = func(string) (*http.Client, error) {
		return &http.Client{Transport: kiroProfileTransport(func(*http.Request) (*http.Response, error) {
			body := `{"profiles":[{"profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/OLD"}]}`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}, nil
	}
	account := &Account{ID: 504, Platform: PlatformAnthropic, Type: AccountTypeKiro, Credentials: map[string]any{
		"auth_method": "idc", "access_token": "old-token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	}}
	cred, _, err := provider.Resolve(context.Background(), account)
	if err == nil || cred != nil || repo.calls != 1 {
		t.Fatalf("stale-token CAS must fail closed: error=%v writes=%d", err, repo.calls)
	}
}

func TestKiroResolveProfileCASMissValidatesFullIdentity(t *testing.T) {
	const concurrentARN = "arn:aws:codewhisperer:us-east-1:123456789012:profile/CONCURRENT"
	for _, tc := range []struct {
		name   string
		change func(current *Account)
		allow  bool
	}{
		{"profile only", func(*Account) {}, true},
		{"auth_method", func(a *Account) { a.Credentials["auth_method"] = "social" }, false},
		{"client_id", func(a *Account) { a.Credentials["client_id"] = "other" }, false},
		{"region", func(a *Account) { a.Credentials["region"] = "eu-central-1" }, false},
		{"extra", func(a *Account) { a.Extra["endpoint"] = "cli" }, false},
		{"account type", func(a *Account) { a.Type = AccountTypeAPIKey }, false},
		{"account platform", func(a *Account) { a.Platform = PlatformOpenAI }, false},
		{"proxy ID", func(a *Account) { id := int64(72); a.ProxyID = &id }, false},
		{"proxy URL same ID", func(a *Account) { a.Proxy.Password = "new-password" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxyID := int64(71)
			account := &Account{ID: 510, Platform: PlatformAnthropic, Type: AccountTypeKiro, ProxyID: &proxyID,
				Proxy: &Proxy{Protocol: "http", Host: "proxy.example", Port: 3128, Username: "u", Password: "old-password"},
				Extra: map[string]any{}, Credentials: map[string]any{"auth_method": "idc", "access_token": "safe-token", "client_id": "original", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}}
			current := &Account{ID: account.ID, Platform: account.Platform, Type: account.Type, ProxyID: &proxyID,
				Proxy: &Proxy{Protocol: "http", Host: "proxy.example", Port: 3128, Username: "u", Password: "old-password"},
				Extra: map[string]any{}, Credentials: map[string]any{}}
			for key, value := range account.Credentials {
				current.Credentials[key] = value
			}
			current.Credentials["profile_arn"] = concurrentARN
			tc.change(current)
			repo := &kiroProfileRepoStub{current: current}
			provider := NewKiroTokenProvider(repo)
			provider.profileClient = func(string) (*http.Client, error) {
				return &http.Client{Transport: kiroProfileTransport(func(*http.Request) (*http.Response, error) {
					body := `{"profiles":[{"profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/DISCOVERED"}]}`
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})}, nil
			}
			cred, token, err := provider.Resolve(context.Background(), account)
			if tc.allow {
				if err != nil || cred == nil || cred.EffectiveProfileArn() != concurrentARN || token != "safe-token" || repo.calls != 1 {
					t.Fatalf("profile-only CAS miss should reuse committed profile: error=%v credential=%t writes=%d", err, cred != nil, repo.calls)
				}
			} else if err == nil || cred != nil || token != "" || repo.calls != 1 {
				t.Fatalf("changed identity must reject CAS miss: error=%v credential=%t writes=%d", err, cred != nil, repo.calls)
			}
		})
	}
}

func TestKiroResolveMultiProfileRequiresRegionalMatch(t *testing.T) {
	repo := &kiroProfileRepoStub{written: true}
	provider := NewKiroTokenProvider(repo)
	provider.profileClient = func(string) (*http.Client, error) {
		return &http.Client{Transport: kiroProfileTransport(func(*http.Request) (*http.Response, error) {
			body := `{"profiles":[{"profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/US"},{"profileArn":"arn:aws:codewhisperer:eu-central-1:123456789012:profile/EU"}]}`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}, nil
	}
	account := &Account{ID: 505, Platform: PlatformAnthropic, Type: AccountTypeKiro, Credentials: map[string]any{
		"auth_method": "idc", "access_token": "safe-token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	}}
	if cred, _, err := provider.Resolve(context.Background(), account); err == nil || cred != nil || repo.calls != 0 {
		t.Fatalf("ambiguous profiles must fail without write: error=%v writes=%d", err, repo.calls)
	}
	account.Credentials["auth_region"] = "eu-central-1"
	cred, _, err := provider.Resolve(context.Background(), account)
	if err != nil || cred == nil {
		t.Fatalf("regional match failed: %v", err)
	}
	if repo.profile != "arn:aws:codewhisperer:eu-central-1:123456789012:profile/EU" {
		t.Fatalf("wrong regional profile selected: %q", repo.profile)
	}
}

func TestKiroResolveRejectsExplicitAPIRegionConflict(t *testing.T) {
	provider := NewKiroTokenProvider(&kiroProfileRepoStub{})
	account := &Account{ID: 506, Platform: PlatformAnthropic, Type: AccountTypeKiro, Credentials: map[string]any{
		"access_token": "safe-token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		"profile_arn": "arn:aws:codewhisperer:eu-central-1:123456789012:profile/EU", "api_region": "us-east-1",
	}}
	if cred, token, err := provider.Resolve(context.Background(), account); err == nil || cred != nil || token != "" {
		t.Fatalf("conflicting api_region should fail closed: error=%v", err)
	}
}

func TestKiroResolveSkipsDiscoveryForCLIAndAPIKey(t *testing.T) {
	provider := NewKiroTokenProvider(&kiroProfileRepoStub{})
	provider.profileClient = func(string) (*http.Client, error) { t.Fatal("unexpected profile discovery"); return nil, nil }
	for _, credentials := range []map[string]any{
		{"endpoint": "cli", "auth_method": "idc", "access_token": "tok", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)},
		{"auth_method": "api_key", "kiro_api_key": "ksk_example"},
	} {
		if _, _, err := provider.Resolve(context.Background(), &Account{ID: 503, Platform: PlatformAnthropic, Type: AccountTypeKiro, Credentials: credentials}); err != nil {
			t.Fatal(err)
		}
	}
}
