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
	c := &Credentials{ProfileArn: "arn:aws:codewhisperer:eu-central-1:123456789012:profile/X", AuthMethod: AuthSocial}
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
	if h.Get("x-amzn-kiro-profile-arn") != "" {
		t.Errorf("generate API must not send profile-arn header; got %q", h.Get("x-amzn-kiro-profile-arn"))
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
	c := &Credentials{ProfileArn: "arn:aws:codewhisperer:us-east-1:123456789012:profile/REAL"}
	body := ep.TransformAPIBody(`{"conversationState":{"conversationId":"c1"}}`, newCtx(c))
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	if v["profileArn"] != c.ProfileArn {
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
	arn := "arn:aws:codewhisperer:ap-southeast-1:123456789012:profile/ABCDEF"
	if got := regionFromProfileArn(arn); got != "ap-southeast-1" {
		t.Errorf("region = %q; want ap-southeast-1", got)
	}
	if got := regionFromProfileArn("malformed"); got != "" {
		t.Errorf("malformed arn region = %q; want empty", got)
	}
}

func TestProfileARNRegionRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{
		"arn:aws:codewhisperer:bad.domain:123456789012:profile/X",
		"arn:aws:codewhisperer:us-east-1:1:profile/X",
		"arn:aws:s3:us-east-1:123456789012:profile/X",
		"arn:aws:codewhisperer:us-east-1:123456789012:not-a-profile/X",
		"arn:aws:codewhisperer:us-east-1:123456789012:profile/X:extra",
		"arn:aws:codewhisperer:us-east-1:123456789012:profile/bad.name",
	} {
		if regionFromProfileArn(value) != "" {
			t.Errorf("malformed ARN supplied a region: %q", value)
		}
		if got := (&Credentials{ProfileArn: value}).EffectiveAPIRegion(DefaultConfig()); got != defaultRegion {
			t.Errorf("unsafe region for malformed ARN: %q", got)
		}
	}
}

func TestIDEProfileInjectionRequiresConfirmedScan(t *testing.T) {
	ep := NewEndpointRegistry()[EndpointIDE]
	c := &Credentials{AuthMethod: AuthIDC, ProfileArn: BuilderIDProfileArn}
	h := make(http.Header)
	ep.DecorateAPI(h, newCtx(c))
	if h.Get("x-amzn-kiro-profile-arn") != "" || strings.Contains(ep.TransformAPIBody(`{"x":1}`, newCtx(c)), "profileArn") {
		t.Fatal("unconfirmed placeholder must not be sent")
	}
	c.ProfileScanConfirmed = true
	if body := ep.TransformAPIBody(`{"x":1}`, newCtx(c)); !strings.Contains(body, BuilderIDProfileArn) {
		t.Errorf("confirmed IdC no-profile missing streaming placeholder: %s", body)
	}
	c.AuthMethod = AuthSocial
	c.ProfileArn = ""
	if body := ep.TransformAPIBody(`{"x":1}`, newCtx(c)); !strings.Contains(body, SocialProfileArn) {
		t.Errorf("confirmed social fallback missing: %s", body)
	}
	c.KiroAPIKey = "ksk_example"
	if body := ep.TransformAPIBody(`{"x":1}`, newCtx(c)); strings.Contains(body, "profileArn") {
		t.Errorf("API key must not carry profile: %s", body)
	}
}

func TestEffectiveRegions(t *testing.T) {
	cfg := DefaultConfig()
	// api region falls back to profile arn region.
	c := &Credentials{ProfileArn: "arn:aws:codewhisperer:eu-west-1:123456789012:profile/X"}
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
