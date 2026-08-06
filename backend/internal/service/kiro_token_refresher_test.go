package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/kiro"
)

// longRT is a syntactically valid (>=100 char, untruncated) refresh token.
const longRT = "rt_0123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789"

type fakeKiroLister struct {
	accounts []Account
	err      error
	calls    int
}

func (f *fakeKiroLister) ListKiroRefreshCandidates(ctx context.Context) ([]Account, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.accounts, nil
}

// fakeKiroErrorMarker records SetError calls so tests can assert which accounts
// were marked as needing re-auth.
type fakeKiroErrorMarker struct {
	marked []int64
	msgs   map[int64]string
	err    error
}

func (f *fakeKiroErrorMarker) SetError(ctx context.Context, id int64, errorMsg string) error {
	if f.err != nil {
		return f.err
	}
	f.marked = append(f.marked, id)
	if f.msgs == nil {
		f.msgs = make(map[int64]string)
	}
	f.msgs[id] = errorMsg
	return nil
}

// fakeLeaderLock lets a test simulate the lock being held by a peer.
type fakeLeaderLock struct {
	acquired bool
}

func (f *fakeLeaderLock) TryAcquireLeaderLock(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	return f.acquired, nil
}

func (f *fakeLeaderLock) ReleaseLeaderLock(ctx context.Context, key, owner string) error {
	return nil
}

func rfc3339In(d time.Duration) string {
	return time.Now().Add(d).Format(time.RFC3339)
}

// newTestRefresher builds a refresher with the loop-critical fields set and a
// spy refreshFn, bypassing NewKiroTokenRefresher's provider/HTTP wiring.
func newTestRefresher(lister kiroRefreshCandidateLister, spy func(ctx context.Context, account *Account, cred *kiro.Credentials) (string, error)) *KiroTokenRefresher {
	return &KiroTokenRefresher{
		lister:        lister,
		refreshWindow: 30 * time.Minute,
		interval:      time.Minute,
		enabled:       true,
		stopCh:        make(chan struct{}),
		refreshFn:     spy,
	}
}

func TestKiroTokenRefresher_RefreshesExpiringNative(t *testing.T) {
	lister := &fakeKiroLister{accounts: []Account{
		{ID: 1, Credentials: map[string]any{
			"auth_method":   "social",
			"access_token":  "at",
			"refresh_token": longRT,
			"expires_at":    rfc3339In(10 * time.Minute), // inside 30m window
		}},
	}}
	var refreshedIDs []int64
	r := newTestRefresher(lister, func(ctx context.Context, account *Account, cred *kiro.Credentials) (string, error) {
		refreshedIDs = append(refreshedIDs, account.ID)
		return "new-token", nil
	})

	r.runOnce()

	if len(refreshedIDs) != 1 || refreshedIDs[0] != 1 {
		t.Fatalf("expected account 1 refreshed, got %v", refreshedIDs)
	}
}

func TestKiroTokenRefresher_SkipsAPIKeyAndFreshAndLegacy(t *testing.T) {
	lister := &fakeKiroLister{accounts: []Account{
		// api_key: static ksk_*, must be skipped.
		{ID: 1, Credentials: map[string]any{"kiro_api_key": "ksk_abc"}},
		// native but not expiring (2h out, beyond 30m window): skip.
		{ID: 2, Credentials: map[string]any{
			"auth_method":   "social",
			"access_token":  "at",
			"refresh_token": longRT,
			"expires_at":    rfc3339In(2 * time.Hour),
		}},
		// legacy passthrough (base_url only, no native auth): skip.
		{ID: 3, Credentials: map[string]any{"base_url": "https://legacy.example"}},
		// native, missing expires_at (unparseable): background stays conservative.
		{ID: 4, Credentials: map[string]any{
			"auth_method":   "social",
			"refresh_token": longRT,
		}},
	}}
	var refreshedIDs []int64
	r := newTestRefresher(lister, func(ctx context.Context, account *Account, cred *kiro.Credentials) (string, error) {
		refreshedIDs = append(refreshedIDs, account.ID)
		return "x", nil
	})

	r.runOnce()

	if len(refreshedIDs) != 0 {
		t.Fatalf("expected no refresh, got %v", refreshedIDs)
	}
}

func TestKiroTokenRefresher_InvalidTokenDoesNotAbortBatch(t *testing.T) {
	lister := &fakeKiroLister{accounts: []Account{
		{ID: 1, Credentials: map[string]any{
			"auth_method": "social", "refresh_token": longRT, "expires_at": rfc3339In(5 * time.Minute),
		}},
		{ID: 2, Credentials: map[string]any{
			"auth_method": "social", "refresh_token": longRT, "expires_at": rfc3339In(5 * time.Minute),
		}},
	}}
	var seen []int64
	r := newTestRefresher(lister, func(ctx context.Context, account *Account, cred *kiro.Credentials) (string, error) {
		seen = append(seen, account.ID)
		if account.ID == 1 {
			return "", kiro.ErrRefreshTokenInvalid
		}
		return "ok", nil
	})

	r.runOnce()

	if len(seen) != 2 {
		t.Fatalf("expected both accounts attempted despite invalid token on #1, got %v", seen)
	}
}

// TestKiroTokenRefresher_MarksTerminalAccount covers the fix for the endless
// retry loop: a permanently-invalid credential must be marked error so the
// candidate query (status=active) stops returning it every cycle.
func TestKiroTokenRefresher_MarksTerminalAccount(t *testing.T) {
	lister := &fakeKiroLister{accounts: []Account{
		{ID: 1, Credentials: map[string]any{
			"auth_method": "social", "refresh_token": longRT, "expires_at": rfc3339In(5 * time.Minute),
		}},
		{ID: 2, Credentials: map[string]any{
			"auth_method": "social", "refresh_token": longRT, "expires_at": rfc3339In(5 * time.Minute),
		}},
	}}
	marker := &fakeKiroErrorMarker{}
	r := newTestRefresher(lister, func(ctx context.Context, account *Account, cred *kiro.Credentials) (string, error) {
		if account.ID == 1 {
			return "", kiro.ErrRefreshTokenInvalid
		}
		return "ok", nil
	})
	r.errorMarker = marker

	r.runOnce()

	if len(marker.marked) != 1 || marker.marked[0] != 1 {
		t.Fatalf("expected only account 1 marked, got %v", marker.marked)
	}
	if msg := marker.msgs[1]; msg == "" {
		t.Error("expected a non-empty error message explaining re-auth is required")
	}
}

// TestKiroTokenRefresher_ContainsSuspectedOutage guards against mass-marking:
// when every attempted refresh fails terminally, an upstream auth outage is
// likelier than all credentials dying at once, so no account state is mutated.
func TestKiroTokenRefresher_ContainsSuspectedOutage(t *testing.T) {
	lister := &fakeKiroLister{accounts: []Account{
		{ID: 1, Credentials: map[string]any{
			"auth_method": "social", "refresh_token": longRT, "expires_at": rfc3339In(5 * time.Minute),
		}},
		{ID: 2, Credentials: map[string]any{
			"auth_method": "social", "refresh_token": longRT, "expires_at": rfc3339In(5 * time.Minute),
		}},
	}}
	marker := &fakeKiroErrorMarker{}
	r := newTestRefresher(lister, func(ctx context.Context, account *Account, cred *kiro.Credentials) (string, error) {
		return "", kiro.ErrRefreshTokenInvalid
	})
	r.errorMarker = marker

	r.runOnce()

	if len(marker.marked) != 0 {
		t.Fatalf("expected no marking when all attempts fail terminally, got %v", marker.marked)
	}
}

// TestKiroTokenRefresher_MarksLoneTerminalAccount pins the single-candidate case:
// with only one account there is no shared signal to distinguish a dead
// credential from an outage, and leaving it unmarked is what caused the loop.
func TestKiroTokenRefresher_MarksLoneTerminalAccount(t *testing.T) {
	lister := &fakeKiroLister{accounts: []Account{
		{ID: 7, Credentials: map[string]any{
			"auth_method": "social", "refresh_token": longRT, "expires_at": rfc3339In(5 * time.Minute),
		}},
	}}
	marker := &fakeKiroErrorMarker{}
	r := newTestRefresher(lister, func(ctx context.Context, account *Account, cred *kiro.Credentials) (string, error) {
		return "", kiro.ErrRefreshTokenInvalid
	})
	r.errorMarker = marker

	r.runOnce()

	if len(marker.marked) != 1 || marker.marked[0] != 7 {
		t.Fatalf("expected lone terminal account marked, got %v", marker.marked)
	}
}

// TestKiroTokenRefresher_SkippedAccountsAreNotMarked ensures api_key and legacy
// credentials — which never attempt a refresh — can never be marked, and that
// their presence does not affect the containment arithmetic.
func TestKiroTokenRefresher_SkippedAccountsAreNotMarked(t *testing.T) {
	lister := &fakeKiroLister{accounts: []Account{
		{ID: 1, Credentials: map[string]any{"kiro_api_key": "ksk_x"}},
		{ID: 2, Credentials: map[string]any{"base_url": "http://legacy"}},
		{ID: 3, Credentials: map[string]any{
			"auth_method": "social", "refresh_token": longRT, "expires_at": rfc3339In(5 * time.Minute),
		}},
	}}
	marker := &fakeKiroErrorMarker{}
	r := newTestRefresher(lister, func(ctx context.Context, account *Account, cred *kiro.Credentials) (string, error) {
		return "", kiro.ErrRefreshTokenInvalid
	})
	r.errorMarker = marker

	r.runOnce()

	// Only account 3 attempted, so it is a lone terminal failure and gets marked.
	if len(marker.marked) != 1 || marker.marked[0] != 3 {
		t.Fatalf("expected only the native account marked, got %v", marker.marked)
	}
}

func TestKiroTokenRefresher_SkipsWhenLeaderHeldByPeer(t *testing.T) {
	lister := &fakeKiroLister{accounts: []Account{
		{ID: 1, Credentials: map[string]any{
			"auth_method": "social", "refresh_token": longRT, "expires_at": rfc3339In(5 * time.Minute),
		}},
	}}
	called := false
	r := newTestRefresher(lister, func(ctx context.Context, account *Account, cred *kiro.Credentials) (string, error) {
		called = true
		return "x", nil
	})
	r.lockCache = &fakeLeaderLock{acquired: false} // peer holds the lock

	r.runOnce()

	if called {
		t.Fatal("expected refresh skipped when leader lock held by peer")
	}
	if lister.calls != 0 {
		t.Fatalf("expected candidate list not queried when not leader, got %d calls", lister.calls)
	}
}
