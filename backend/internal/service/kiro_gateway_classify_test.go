package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/kiro"
)

// Test_classifyKiroDisposition pins the decision table that maps a kiro provider
// Disposition onto the gateway's failover reaction. This is the trunk seam most
// likely to drift during upstream rebases, so every disposition is covered.
func Test_classifyKiroDisposition(t *testing.T) {
	tests := []struct {
		name   string
		disp   kiro.Disposition
		status int
		want   kiroDispositionAction
	}{
		{
			name:   "bad request is a client error, never fails over",
			disp:   kiro.DispBadRequest,
			status: 400,
			want:   kiroDispositionAction{ClientError: true},
		},
		{
			name:   "other 4xx client error, never fails over",
			disp:   kiro.DispClientError,
			status: 404,
			want:   kiroDispositionAction{ClientError: true},
		},
		{
			name:   "throttled fails over as 429 and is same-account retry eligible",
			disp:   kiro.DispThrottled,
			status: 429,
			want: kiroDispositionAction{
				Failover:          true,
				FailoverStatus:    429,
				RetryableEligible: true,
			},
		},
		{
			name:   "transient fails over as its status and is retry eligible",
			disp:   kiro.DispTransient,
			status: 503,
			want: kiroDispositionAction{
				Failover:          true,
				FailoverStatus:    503,
				RetryableEligible: true,
			},
		},
		{
			name:   "transient with no status falls back to 502",
			disp:   kiro.DispTransient,
			status: 0,
			want: kiroDispositionAction{
				Failover:          true,
				FailoverStatus:    http.StatusBadGateway,
				RetryableEligible: true,
			},
		},
		{
			name:   "auth failure fails over as account-auth (credential) stage",
			disp:   kiro.DispAuthFailure,
			status: 401,
			want: kiroDispositionAction{
				Failover:         true,
				FailoverStatus:   401,
				AccountAuthStage: true,
			},
		},
		{
			name:   "quota exhausted fails over as account-auth stage",
			disp:   kiro.DispQuotaExhausted,
			status: 402,
			want: kiroDispositionAction{
				Failover:         true,
				FailoverStatus:   402,
				AccountAuthStage: true,
			},
		},
		{
			name:   "unknown fails over plainly (no credential stage, not retry eligible)",
			disp:   kiro.DispUnknown,
			status: 0,
			want: kiroDispositionAction{
				Failover:       true,
				FailoverStatus: http.StatusBadGateway,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyKiroDisposition(tt.disp, tt.status)
			require.Equal(t, tt.want, got)
		})
	}
}

// Test_classifyKiroDisposition_mutuallyExclusive guarantees a client error is
// never simultaneously a failover: the two branches drive different gateway
// code paths (write-to-client vs return-error) and must stay disjoint.
func Test_classifyKiroDisposition_mutuallyExclusive(t *testing.T) {
	all := []kiro.Disposition{
		kiro.DispSuccess, kiro.DispBadRequest, kiro.DispAuthFailure,
		kiro.DispQuotaExhausted, kiro.DispThrottled, kiro.DispTransient,
		kiro.DispClientError, kiro.DispUnknown,
	}
	for _, d := range all {
		got := classifyKiroDisposition(d, 0)
		require.Falsef(t, got.ClientError && got.Failover,
			"disposition %d must not be both client-error and failover", d)
		if got.AccountAuthStage || got.RetryableEligible {
			require.Truef(t, got.Failover,
				"disposition %d sets a failover-only flag without Failover", d)
		}
	}
}

// Test_kiroFailoverStatus covers the status fallback helper.
func Test_kiroFailoverStatus(t *testing.T) {
	require.Equal(t, 503, kiroFailoverStatus(503, http.StatusBadGateway))
	require.Equal(t, http.StatusBadGateway, kiroFailoverStatus(0, http.StatusBadGateway))
	require.Equal(t, http.StatusTooManyRequests, kiroFailoverStatus(0, http.StatusTooManyRequests))
}
