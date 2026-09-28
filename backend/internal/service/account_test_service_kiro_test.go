package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/gin-gonic/gin"
)

type kiroAccountTestUpstream struct {
	HTTPUpstream
	do func(*http.Request, string, int64, int) (*http.Response, error)
}

func (u *kiroAccountTestUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	return u.do(req, proxy, id, concurrency)
}

func kiroAccountTestResponse(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
}

func kiroAccountTestFrame(t *testing.T, eventType, payload string) []byte {
	t.Helper()
	var out bytes.Buffer
	headers := []eventstream.Header{{Name: ":message-type", Value: eventstream.StringValue("event")}, {Name: ":event-type", Value: eventstream.StringValue(eventType)}}
	if eventType == "error" {
		headers = []eventstream.Header{{Name: ":message-type", Value: eventstream.StringValue("error")}, {Name: ":error-code", Value: eventstream.StringValue("TestError")}}
	}
	if err := eventstream.NewEncoder().Encode(&out, eventstream.Message{Headers: headers, Payload: []byte(payload)}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func kiroAccountTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
	return c, recorder
}

func kiroAccountForConnectionTest() *Account {
	return &Account{
		ID: 701, Platform: PlatformAnthropic, Type: AccountTypeKiro,
		Credentials: map[string]any{"kiro_api_key": "ksk_test", "profile_arn": "arn:aws:codewhisperer:us-east-1:123:profile/test"},
	}
}

func TestKiroNativeConnectionRejectsFalseSuccess(t *testing.T) {
	text := kiroAccountTestFrame(t, "assistantResponseEvent", `{"content":"hello"}`)
	errorFrame := kiroAccountTestFrame(t, "error", "upstream rejected")
	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"empty", nil, "no assistant text"},
		{"invalid framing", []byte("not an event stream"), "decode failed"},
		{"error frame", errorFrame, "upstream rejected"},
		{"error after content", append(bytes.Clone(text), errorFrame...), "upstream rejected"},
		{"corrupt frame after content", append(bytes.Clone(text), append(bytes.Clone(errorFrame[:len(errorFrame)-1]), 'x')...), "decode failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			svc := &AccountTestService{
				kiroTokenProvider: &KiroTokenProvider{},
				httpUpstream: &kiroAccountTestUpstream{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
					calls++
					return kiroAccountTestResponse(http.StatusOK, tc.body), nil
				}},
			}
			c, recorder := kiroAccountTestContext()
			err := svc.testClaudeAccountConnection(c, kiroAccountForConnectionTest(), "claude-sonnet-4.5")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v; want %q", err, tc.want)
			}
			if calls != 1 || !strings.Contains(recorder.Body.String(), `"type":"error"`) || strings.Contains(recorder.Body.String(), `"type":"test_complete"`) {
				t.Fatalf("calls=%d, unexpected SSE: %s", calls, recorder.Body.String())
			}
		})
	}
}

func TestKiroNativeConnectionUsesProxyAndEndpointFallback(t *testing.T) {
	account := kiroAccountForConnectionTest()
	proxyID := int64(16)
	account.ProxyID = &proxyID
	account.Proxy = &Proxy{Protocol: "http", Host: "127.0.0.1", Port: 3128}
	var endpoints []string
	upstream := &kiroAccountTestUpstream{do: func(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
		if proxy != account.Proxy.URL() || id != account.ID || concurrency != account.Concurrency {
			return nil, fmt.Errorf("account routing mismatch: proxy=%q id=%d concurrency=%d", proxy, id, concurrency)
		}
		if req.Header.Get("Authorization") != "Bearer ksk_test" {
			return nil, fmt.Errorf("unexpected authorization header")
		}
		endpoints = append(endpoints, req.URL.String())
		if len(endpoints) == 1 {
			return kiroAccountTestResponse(http.StatusForbidden, []byte("forbidden")), nil
		}
		return kiroAccountTestResponse(http.StatusOK, kiroAccountTestFrame(t, "assistantResponseEvent", `{"content":"ok"}`)), nil
	}}
	svc := &AccountTestService{kiroTokenProvider: &KiroTokenProvider{}, httpUpstream: upstream}
	c, recorder := kiroAccountTestContext()
	if err := svc.testClaudeAccountConnection(c, account, "claude-sonnet-4.5"); err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 2 || !strings.Contains(endpoints[0], "amazonaws.com/generateAssistantResponse") || !strings.Contains(endpoints[1], "runtime.") || !strings.Contains(recorder.Body.String(), `"success":true`) {
		t.Fatalf("endpoints=%v, SSE=%s", endpoints, recorder.Body.String())
	}
}

func TestKiroNativeConnectionHonorsPinnedEndpoint(t *testing.T) {
	account := kiroAccountForConnectionTest()
	account.Credentials["endpoint"] = "cli"
	calls := 0
	var requestURL string
	svc := &AccountTestService{kiroTokenProvider: &KiroTokenProvider{}, httpUpstream: &kiroAccountTestUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls++
		requestURL = req.URL.String()
		return kiroAccountTestResponse(http.StatusForbidden, []byte("forbidden")), nil
	}}}
	c, _ := kiroAccountTestContext()
	if err := svc.testClaudeAccountConnection(c, account, "claude-sonnet-4.5"); err == nil {
		t.Fatal("pinned endpoint failure must not succeed")
	}
	if calls != 1 || !strings.Contains(requestURL, "runtime.") {
		t.Fatalf("pinned endpoint attempted %d times at %s; want one CLI request", calls, requestURL)
	}
}

func TestKiroModelSyncNativeFailureDoesNotUseStaticList(t *testing.T) {
	// The local mock proxy rejects CONNECT; no production upstream is contacted.
	calls := 0
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer proxyServer.Close()
	parsed, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portString, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatal(err)
	}
	account := kiroAccountForConnectionTest()
	proxyID := int64(17)
	account.ProxyID = &proxyID
	account.Proxy = &Proxy{Protocol: "http", Host: host, Port: port}
	svc := &AccountTestService{kiroTokenProvider: &KiroTokenProvider{}}
	models, _, err := svc.fetchUpstreamModelList(context.Background(), account)
	if err == nil || len(models) != 0 || calls == 0 {
		t.Fatalf("native sync returned models=%v, err=%v, proxy calls=%d; want an upstream error", models, err, calls)
	}
	legacy := &Account{ID: 702, Platform: PlatformAnthropic, Type: AccountTypeKiro, Credentials: map[string]any{"base_url": "https://kiro-rs.example", "api_key": "legacy"}}
	models, _, err = svc.fetchUpstreamModelList(context.Background(), legacy)
	if err != nil || len(models) == 0 {
		t.Fatalf("legacy sync returned models=%v, err=%v; want static models", models, err)
	}
}

type kiroUsageErrorRepo struct {
	AccountRepository
	cleared bool
}

func (r *kiroUsageErrorRepo) ClearError(_ context.Context, _ int64) error {
	r.cleared = true
	return nil
}

func TestKiroUsageErrorDoesNotClearAccountError(t *testing.T) {
	repo := &kiroUsageErrorRepo{}
	account := kiroAccountForConnectionTest()
	account.Status = StatusError
	account.ErrorMessage = "token refresh failed"
	svc := &AccountUsageService{accountRepo: repo}
	usage, err := svc.getUsageForAccount(context.Background(), account, false)
	if err != nil || usage == nil || usage.Error == "" || repo.cleared || account.Status != StatusError {
		t.Fatalf("usage=%+v, err=%v, cleared=%v, status=%q", usage, err, repo.cleared, account.Status)
	}
}
