package service

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func Test_kiroUpstreamErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"kiro message and reason", `{"message":"Invalid tool use format.","reason":"REQUEST_BODY_INVALID"}`, "Invalid tool use format. (REQUEST_BODY_INVALID)"},
		{"message only", `{"message":"Too many requests"}`, "Too many requests"},
		{"reason only", `{"reason":"MONTHLY_REQUEST_COUNT"}`, "MONTHLY_REQUEST_COUNT"},
		{"claude style error object", `{"error":{"message":"inner msg"}}`, "inner msg"},
		{"not json falls back to the raw body", "not json at all", "not json at all"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, kiroUpstreamErrorMessage(tt.body))
		})
	}
}

func Test_kiroDriveOptions(t *testing.T) {
	require.Zero(t, (&GatewayService{}).kiroDriveOptions(), "nil config disables keepalive and idle timeout")

	cfg := &config.Config{}
	cfg.Gateway.StreamKeepaliveInterval = 10
	cfg.Gateway.StreamDataIntervalTimeout = 180
	opts := (&GatewayService{cfg: cfg}).kiroDriveOptions()
	require.Equal(t, 10*time.Second, opts.KeepaliveInterval)
	require.Equal(t, 180*time.Second, opts.IdleTimeout)
}

// Test_writeKiroClientError_returnsError pins that a Kiro client error is
// reported as a failure. It used to return a successful ForwardResult, which
// recorded the rejected request as a 0-token success and hid it from logs.
func Test_writeKiroClientError_returnsError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	result, err := (&GatewayService{}).writeKiroClientError(c, http.StatusBadRequest, `{"message":"Invalid tool use format."}`)
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "Invalid tool use format.")
	require.True(t, IsResponseCommitted(c), "handler must not append a second error response")
}
