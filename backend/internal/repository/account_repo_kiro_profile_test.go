package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// These SQL expectations deliberately model production Kiro accounts:
// platform=anthropic, type=kiro. The write guard must use type, not platform.
func TestKiroCredentialCASGuardsTypeAndEmitsOutbox(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile bool
	}{
		{name: "discover profile", profile: true},
		{name: "refresh bearer", profile: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, affected := range []int64{0, 1} {
				db, mock, err := sqlmock.New()
				require.NoError(t, err)
				// The CTE inserts an outbox row in the same SQL statement as the update.
				query := `(?s)` + regexp.QuoteMeta("UPDATE accounts AS a") + `.*` +
					regexp.QuoteMeta("a.type = $3") + `.*` +
					regexp.QuoteMeta("a.parent_account_id IS NULL") + `.*` +
					regexp.QuoteMeta("a.credentials = $4::jsonb") + `.*` +
					regexp.QuoteMeta("a.proxy_id IS NOT DISTINCT FROM $5") + `.*` +
					regexp.QuoteMeta("INSERT INTO scheduler_outbox")
				expected := `{"access_token":"existing"}`
				if tc.profile {
					mock.ExpectExec(query).WithArgs("arn:aws:codewhisperer:us-east-1:123456789012:profile/REAL", int64(5003), service.AccountTypeKiro, expected, nil, service.SchedulerOutboxEventAccountChanged).
						WillReturnResult(sqlmock.NewResult(0, affected))
				} else {
					mock.ExpectExec(query).WithArgs(`{"access_token":"new"}`, int64(5003), service.AccountTypeKiro, expected, nil, service.SchedulerOutboxEventAccountChanged).
						WillReturnResult(sqlmock.NewResult(0, affected))
				}
				repo := &accountRepository{sql: db}
				account := &service.Account{ID: 5003, Platform: service.PlatformAnthropic, Type: service.AccountTypeKiro, Credentials: map[string]any{"access_token": "existing"}}
				var written bool
				if tc.profile {
					written, err = repo.UpdateKiroProfileArnIfUnchanged(context.Background(), account.ID, account.Credentials, account.ProxyID, "arn:aws:codewhisperer:us-east-1:123456789012:profile/REAL")
				} else {
					written, err = repo.UpdateKiroRefreshedCredentialsIfUnchanged(context.Background(), account.ID, account.Credentials, account.ProxyID, map[string]any{"access_token": "new"})
				}
				require.NoError(t, err)
				require.Equal(t, affected == 1, written)
				require.NoError(t, mock.ExpectationsWereMet())
				require.NoError(t, db.Close())
			}
		})
	}
}

func TestKiroCredentialCASOutboxFailureAbortsWrite(t *testing.T) {
	for _, profile := range []bool{true, false} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		mock.ExpectExec(`(?s)` + regexp.QuoteMeta("a.type = $3") + `.*` + regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
			WillReturnError(errors.New("outbox unavailable"))
		repo := &accountRepository{sql: db}
		var written bool
		if profile {
			written, err = repo.UpdateKiroProfileArnIfUnchanged(context.Background(), 5003, map[string]any{}, nil, "arn:aws:codewhisperer:us-east-1:123456789012:profile/REAL")
		} else {
			written, err = repo.UpdateKiroRefreshedCredentialsIfUnchanged(context.Background(), 5003, map[string]any{}, nil, map[string]any{"access_token": "new"})
		}
		require.Error(t, err)
		require.False(t, written)
		require.NoError(t, mock.ExpectationsWereMet())
		require.NoError(t, db.Close())
	}
}
