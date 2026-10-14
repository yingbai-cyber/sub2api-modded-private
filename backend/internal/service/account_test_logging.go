package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	accountTestBackgroundKey = "account_test_background"
	// accountTestLoggerKey keeps this probe's logger on gin.Keys. Storing it only
	// on Request.Context loses the entry when a later call replaces c.Request:
	// FromContext then falls back to the global logger, which unit tests leave as a nop.
	accountTestLoggerKey = "account_test_logger"
)

func setAccountTestLogger(c *gin.Context, l *zap.Logger) {
	if c == nil || l == nil {
		return
	}
	c.Set(accountTestLoggerKey, l)
	if c.Request != nil {
		c.Request = c.Request.WithContext(logger.IntoContext(c.Request.Context(), l))
	}
}

func accountTestLogger(c *gin.Context) *zap.Logger {
	if c != nil {
		if v, ok := c.Get(accountTestLoggerKey); ok {
			if l, ok := v.(*zap.Logger); ok && l != nil {
				return l
			}
		}
		if c.Request != nil {
			return logger.FromContext(c.Request.Context())
		}
	}
	return logger.L()
}

// Bind attribution before lookup so missing accounts are identifiable too. The
// request logger retains HTTP request IDs or the caller's background correlation.
func initAccountTestLogger(c *gin.Context, accountID int64, modelID, mode string) {
	source := "http"
	if c != nil && c.GetBool(accountTestBackgroundKey) {
		source = "background"
	}
	l := logger.FromContext(c.Request.Context()).With(
		zap.String("component", "service.account_test"),
		zap.Int64("account_id", accountID),
		zap.String("test_id", uuid.NewString()),
		zap.String("test_source", source),
		zap.String("requested_model", modelID),
		zap.String("test_mode", normalizeAccountTestMode(mode)),
	)
	setAccountTestLogger(c, l)
}

func bindAccountTestPlatform(c *gin.Context, account *Account) {
	if c == nil || account == nil {
		return
	}
	setAccountTestLogger(c, accountTestLogger(c).With(
		zap.String("platform", account.Platform),
		zap.String("account_type", account.Type),
	))
}

func logAccountTestError(c *gin.Context, message string) {
	if c == nil {
		return
	}
	// Redact known credential fields before bounding text; truncating first can
	// cut a quoted credential in half and prevent the redactor from matching it.
	message = logredact.RedactText(message, "api_key", "authorization")
	accountTestLogger(c).Warn("account_test.failed",
		zap.String("error", truncateString(message, 2048)),
	)
}
