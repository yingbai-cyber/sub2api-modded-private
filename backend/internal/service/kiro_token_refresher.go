package service

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/kiro"
	"github.com/google/uuid"
)

const (
	// kiroTokenRefreshLeaderLockKey gates the per-cycle scan so only one instance
	// refreshes Kiro credentials, avoiding N× refresh HTTP and reducing
	// refresh_token rotation races across instances.
	kiroTokenRefreshLeaderLockKey = "kiro:token:refresh:leader"
	// kiroTokenRefreshLeaderLockTTL bounds crash recovery; must exceed one cycle's
	// worst-case runtime. The lock is released as soon as the cycle completes.
	kiroTokenRefreshLeaderLockTTL = 10 * time.Minute
	// kiroTokenRefreshCycleTimeout bounds a single scan+refresh cycle.
	kiroTokenRefreshCycleTimeout = 8 * time.Minute
	// Fallbacks used when config is nil or carries non-positive values.
	kiroTokenRefreshDefaultIntervalMinutes = 5
	kiroTokenRefreshDefaultWindowMinutes   = 30
)

// kiroRefreshCandidateLister is the narrow repository contract the Kiro
// background refresher needs. It is deliberately narrower than AccountRepository
// (mirrors OAuthRefreshCandidatePager) so the shared interface — and its many
// test stubs — stays untouched. The concrete *accountRepository implements it.
type kiroRefreshCandidateLister interface {
	ListKiroRefreshCandidates(ctx context.Context) ([]Account, error)
}

// KiroTokenRefresher proactively refreshes native Kiro OAuth credentials
// (social/idc/external_idp) before they expire. It is keyed on account
// type='kiro' (via the repository query), independent of account.Platform, so it
// works before and after the L9 platform migration. api_key credentials carry a
// static ksk_* key and are skipped.
//
// This is a standalone background service (mirrors AccountExpiryService) rather
// than a plug-in to the shared TokenRefreshService: the shared candidate query
// hard-codes platform + type IN ('oauth','setup-token'), so integrating Kiro
// there would require a platform migration plus a change to a shared,
// all-platform SQL query. Keeping Kiro self-contained preserves the thin-seam
// invariant (all Kiro logic stays in kiro-owned files).
type KiroTokenRefresher struct {
	lister        kiroRefreshCandidateLister
	refreshWindow time.Duration
	interval      time.Duration
	enabled       bool

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	lockCache  LeaderLockCache
	db         *sql.DB
	instanceID string

	// refreshFn performs the actual refresh+persist for one account. It defaults
	// to KiroTokenProvider.refreshAndPersist; tests override it to avoid real HTTP.
	refreshFn func(ctx context.Context, account *Account, cred *kiro.Credentials) (string, error)
}

// NewKiroTokenRefresher builds the refresher from the shared token_refresh
// config. The candidate lister is obtained by narrowing accountRepo; when the
// repository does not implement it (e.g. unit tests with a minimal stub) the
// service stays inert.
func NewKiroTokenRefresher(accountRepo AccountRepository, cfg *config.Config) *KiroTokenRefresher {
	r := &KiroTokenRefresher{
		stopCh:     make(chan struct{}),
		instanceID: uuid.NewString(),
	}
	if lister, ok := accountRepo.(kiroRefreshCandidateLister); ok {
		r.lister = lister
	}
	provider := NewKiroTokenProvider(accountRepo)
	r.refreshFn = provider.refreshAndPersist

	interval := time.Duration(kiroTokenRefreshDefaultIntervalMinutes) * time.Minute
	window := time.Duration(kiroTokenRefreshDefaultWindowMinutes) * time.Minute
	enabled := true
	if cfg != nil {
		enabled = cfg.TokenRefresh.Enabled
		if cfg.TokenRefresh.CheckIntervalMinutes > 0 {
			interval = time.Duration(cfg.TokenRefresh.CheckIntervalMinutes) * time.Minute
		}
		if cfg.TokenRefresh.RefreshBeforeExpiryHours > 0 {
			window = time.Duration(cfg.TokenRefresh.RefreshBeforeExpiryHours * float64(time.Hour))
		}
	}
	r.enabled = enabled
	r.interval = interval
	r.refreshWindow = window
	return r
}

// SetLeaderLock injects the leader-lock cache and DB used to elect a single
// instance for the periodic scan. When both are nil the scan runs ungated
// (single-instance / test behavior).
func (s *KiroTokenRefresher) SetLeaderLock(lockCache LeaderLockCache, db *sql.DB) {
	if s == nil {
		return
	}
	s.lockCache = lockCache
	s.db = db
}

// Start launches the periodic refresh loop. It is a no-op when disabled, when no
// candidate lister is available, or when the interval is non-positive.
func (s *KiroTokenRefresher) Start() {
	if s == nil || !s.enabled || s.interval <= 0 || s.lister == nil || s.refreshFn == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		s.runOnce()
		for {
			select {
			case <-ticker.C:
				s.runOnce()
			case <-s.stopCh:
				return
			}
		}
	}()
}

// Stop halts the loop and waits for the in-flight cycle to finish.
func (s *KiroTokenRefresher) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()
}

// runOnce performs one scan+refresh cycle under a cluster-wide singleton leader
// lock. A single account failure never aborts the batch.
func (s *KiroTokenRefresher) runOnce() {
	if s == nil || s.lister == nil || s.refreshFn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), kiroTokenRefreshCycleTimeout)
	defer cancel()

	// Multi-instance guard: only the leader scans and refreshes, avoiding
	// redundant refresh HTTP and reducing refresh_token rotation races.
	release, ok := tryAcquireSingletonLeaderLock(ctx, s.lockCache, s.db, kiroTokenRefreshLeaderLockKey, s.instanceID, kiroTokenRefreshLeaderLockTTL)
	if !ok {
		return
	}
	defer release()

	accounts, err := s.lister.ListKiroRefreshCandidates(ctx)
	if err != nil {
		log.Printf("[KiroTokenRefresh] list candidates failed: %v", err)
		return
	}

	windowMinutes := int(s.refreshWindow / time.Minute)
	if windowMinutes <= 0 {
		windowMinutes = kiroTokenRefreshDefaultWindowMinutes
	}

	var refreshed, skipped, failed, invalid int
	for i := range accounts {
		if ctx.Err() != nil {
			log.Printf("[KiroTokenRefresh] cycle stopped: %v", ctx.Err())
			break
		}
		account := &accounts[i]
		cred := kiro.ParseCredentials(account.ID, account.Credentials, account.Extra)

		// api_key credentials use a static ksk_* key; legacy passthrough
		// credentials have no native OAuth to refresh. Both are skipped.
		if cred.IsAPIKey() || !cred.UsesNativeUpstream() {
			skipped++
			continue
		}
		// Guard against attempting a refresh with a missing/truncated token.
		if err := kiro.ValidateRefreshToken(cred); err != nil {
			skipped++
			continue
		}
		// Only refresh inside the pre-expiry window. Unparseable/missing expiry
		// (ok==false) is left to the request-time lazy path, which treats it as
		// expired; the background path stays conservative to avoid churn.
		expiring, parseOK := kiro.IsTokenExpiringWithin(cred, windowMinutes)
		if !parseOK || !expiring {
			skipped++
			continue
		}

		if _, err := s.refreshFn(ctx, account, cred); err != nil {
			if errors.Is(err, kiro.ErrRefreshTokenInvalid) {
				invalid++
				log.Printf("[KiroTokenRefresh] account=%d refresh token permanently invalid", account.ID)
			} else {
				failed++
				log.Printf("[KiroTokenRefresh] account=%d refresh failed: %v", account.ID, err)
			}
			continue
		}
		refreshed++
	}

	if refreshed > 0 || failed > 0 || invalid > 0 {
		log.Printf("[KiroTokenRefresh] cycle done: refreshed=%d skipped=%d failed=%d invalid=%d total=%d",
			refreshed, skipped, failed, invalid, len(accounts))
	}
}
