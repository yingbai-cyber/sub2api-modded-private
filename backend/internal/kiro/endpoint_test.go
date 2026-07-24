package kiro

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func newCtx(c *Credentials) *RequestContext {
	return &RequestContext{
		Credentials: c,
		Token:       "tok-123",
		MachineID:   "mid-abc",
		Config:      DefaultConfig(),
	}
}

func TestIdeEndpointURLAndHeaders(t *testing.T) {
	reg := NewEndpointRegistry()
	ep := reg[EndpointIDE]
	c := &Credentials{ProfileArn: "arn:aws:codewhisperer:eu-central-1:1:profile/X", AuthMethod: AuthSocial}
	ctx := newCtx(c)

	url := ep.APIURL(ctx)
	// region should come from the profile ARN.
	if url != "https://q.eu-central-1.amazonaws.com/generateAssistantResponse" {
		t.Errorf("api url = %q", url)
	}

	h := http.Header{}
	ep.DecorateAPI(h, ctx)
	if h.Get("Authorization") != "Bearer tok-123" {
		t.Errorf("authorization = %q", h.Get("Authorization"))
	}
	if !strings.Contains(h.Get("user-agent"), "KiroIDE-") {
		t.Errorf("user-agent = %q; want KiroIDE marker", h.Get("user-agent"))
	}
	if h.Get("x-amzn-kiro-profile-arn") != c.ProfileArn {
		t.Errorf("profile-arn header = %q", h.Get("x-amzn-kiro-profile-arn"))
	}
	// social credential => no TokenType header.
	if h.Get("TokenType") != "" {
		t.Errorf("unexpected TokenType = %q", h.Get("TokenType"))
	}
}

func TestIdeEndpointAPIKeyTokenType(t *testing.T) {
	ep := NewEndpointRegistry()[EndpointIDE]
	c := &Credentials{KiroAPIKey: "ksk_x", AuthMethod: AuthAPIKey}
	h := http.Header{}
	ep.DecorateAPI(h, newCtx(c))
	if h.Get("TokenType") != "API_KEY" {
		t.Errorf("TokenType = %q; want API_KEY", h.Get("TokenType"))
	}
}

func TestIdeEndpointExternalIDPTokenType(t *testing.T) {
	ep := NewEndpointRegistry()[EndpointIDE]
	c := &Credentials{AuthMethod: AuthExternalIDP}
	h := http.Header{}
	ep.DecorateAPI(h, newCtx(c))
	if h.Get("TokenType") != "EXTERNAL_IDP" {
		t.Errorf("TokenType = %q; want EXTERNAL_IDP", h.Get("TokenType"))
	}
}

func TestIdeEndpointInjectsProfileArn(t *testing.T) {
	ep := NewEndpointRegistry()[EndpointIDE]
	c := &Credentials{ProfileArn: "arn:test:profile"}
	body := ep.TransformAPIBody(`{"conversationState":{"conversationId":"c1"}}`, newCtx(c))
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	if v["profileArn"] != "arn:test:profile" {
		t.Errorf("profileArn = %v", v["profileArn"])
	}
}

func TestCliEndpointURLAndHeaders(t *testing.T) {
	ep := NewEndpointRegistry()[EndpointCLI]
	c := &Credentials{APIRegion: "us-west-2", AuthMethod: AuthSocial}
	ctx := newCtx(c)

	if url := ep.APIURL(ctx); url != "https://runtime.us-west-2.kiro.dev/" {
		t.Errorf("cli api url = %q", url)
	}
	h := http.Header{}
	ep.DecorateAPI(h, ctx)
	if h.Get("X-Amz-Target") != "AmazonCodeWhispererStreamingService.GenerateAssistantResponse" {
		t.Errorf("x-amz-target = %q", h.Get("X-Amz-Target"))
	}
	if !strings.Contains(h.Get("User-Agent"), "AmazonQ-For-CLI") {
		t.Errorf("user-agent = %q; want AmazonQ-For-CLI", h.Get("User-Agent"))
	}
	// CLI does not inject profileArn.
	body := ep.TransformAPIBody(`{"x":1}`, ctx)
	if strings.Contains(body, "profileArn") {
		t.Errorf("cli body should not carry profileArn: %q", body)
	}
}

func TestMonthlyRequestLimitDetection(t *testing.T) {
	ep := NewEndpointRegistry()[EndpointIDE]
	cases := []struct {
		body string
		want bool
	}{
		{`{"message":"limit","reason":"MONTHLY_REQUEST_COUNT"}`, true},
		{`{"error":{"reason":"MONTHLY_REQUEST_COUNT"}}`, true},
		{`raw MONTHLY_REQUEST_COUNT text`, true},
		{`{"reason":"DAILY_REQUEST_COUNT"}`, false},
	}
	for _, tc := range cases {
		if got := ep.IsMonthlyRequestLimit(tc.body); got != tc.want {
			t.Errorf("IsMonthlyRequestLimit(%q) = %v; want %v", tc.body, got, tc.want)
		}
	}
}

func TestBearerTokenInvalidDetection(t *testing.T) {
	ep := NewEndpointRegistry()[EndpointIDE]
	if !ep.IsBearerTokenInvalid("The bearer token included in the request is invalid") {
		t.Error("expected bearer-token-invalid detection")
	}
	if ep.IsBearerTokenInvalid("some other error") {
		t.Error("false positive on unrelated error")
	}
}

func TestRegionFromProfileArn(t *testing.T) {
	arn := "arn:aws:codewhisperer:ap-southeast-1:123456789:profile/ABCDEF"
	if got := regionFromProfileArn(arn); got != "ap-southeast-1" {
		t.Errorf("region = %q; want ap-southeast-1", got)
	}
	if got := regionFromProfileArn("malformed"); got != "" {
		t.Errorf("malformed arn region = %q; want empty", got)
	}
}

func TestEffectiveRegions(t *testing.T) {
	cfg := DefaultConfig()
	// api region falls back to profile arn region.
	c := &Credentials{ProfileArn: "arn:aws:codewhisperer:eu-west-1:1:profile/X"}
	if got := c.EffectiveAPIRegion(cfg); got != "eu-west-1" {
		t.Errorf("api region = %q; want eu-west-1", got)
	}
	// explicit cred region wins for auth.
	c2 := &Credentials{Region: "ap-northeast-1"}
	if got := c2.EffectiveAuthRegion(cfg); got != "ap-northeast-1" {
		t.Errorf("auth region = %q; want ap-northeast-1", got)
	}
	// default when nothing set.
	c3 := &Credentials{}
	if got := c3.EffectiveAPIRegion(cfg); got != "us-east-1" {
		t.Errorf("default api region = %q; want us-east-1", got)
	}
}
