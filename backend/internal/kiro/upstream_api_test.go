package kiro

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type restRoundTripFunc func(*http.Request) (*http.Response, error)

func (f restRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type trackedRESTBody struct {
	io.Reader
	closed bool
}

func (b *trackedRESTBody) Close() error { b.closed = true; return nil }

func restResponse(status int, body string, bodies *[]*trackedRESTBody) *http.Response {
	b := &trackedRESTBody{Reader: strings.NewReader(body)}
	*bodies = append(*bodies, b)
	return &http.Response{StatusCode: status, Body: b, Header: make(http.Header)}
}

func restCall(ctx context.Context, client *http.Client, cred *Credentials, models bool) error {
	if models {
		_, err := ListAvailableModels(ctx, client, cred, "test-token", &Config{KiroVersion: "99.0", APIRegion: "ap-south-1"})
		return err
	}
	_, err := GetUsageLimits(ctx, client, cred, "test-token", &Config{KiroVersion: "99.0", APIRegion: "ap-south-1"})
	return err
}

func TestRESTFallbackOrderAndResponse(t *testing.T) {
	const arn = "arn:aws:codewhisperer:eu-central-1:123456789012:profile/REAL"
	for _, models := range []bool{false, true} {
		name, query, success := "getUsageLimits", "origin=AI_EDITOR&resourceType=AGENTIC_REQUEST&isEmailRequired=true", `{"subscriptionInfo":{"subscriptionTitle":"PRO"},"usageBreakdownList":[{"currentUsage":3,"usageLimit":10}]}`
		if models {
			name, query, success = "ListAvailableModels", "origin=AI_EDITOR", `{"models":[{"modelId":"claude-test","modelName":"Test","rateMultiplier":1.5}]}`
		}
		t.Run(name, func(t *testing.T) {
			const encodedARN = "arn%3Aaws%3Acodewhisperer%3Aeu-central-1%3A123456789012%3Aprofile%2FREAL"
			wantURLs := []string{
				"https://q.eu-central-1.amazonaws.com/" + name + "?" + query + "&profileArn=" + encodedARN,
				"https://q.eu-central-1.amazonaws.com/" + name + "?" + query,
				"https://q.us-east-1.amazonaws.com/" + name + "?" + query + "&profileArn=" + encodedARN,
				"https://q.us-east-1.amazonaws.com/" + name + "?" + query,
			}
			statuses := []int{400, 403, 403, 200}
			texts := []string{`{"message":"Improperly formed request."}`, "denied", "denied", success}
			var bodies []*trackedRESTBody
			calls := 0
			type ctxKey struct{}
			ctx := context.WithValue(context.Background(), ctxKey{}, "present")
			client := &http.Client{Transport: restRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if calls >= len(wantURLs) {
					t.Fatalf("unexpected extra request: %s", req.URL)
				}
				if req.Method != http.MethodGet || req.URL.String() != wantURLs[calls] {
					t.Errorf("request %d: %s %s, want GET %s", calls, req.Method, req.URL, wantURLs[calls])
				}
				if req.Context().Value(ctxKey{}) != "present" {
					t.Error("request lost caller context")
				}
				if req.Host != req.URL.Host || req.Header.Get("Authorization") != "Bearer test-token" || !req.Close {
					t.Errorf("request lost host, bearer token, or Connection: close: %+v", req)
				}
				if req.Header.Get("x-amzn-kiro-profile-arn") != "" {
					t.Error("unexpected profile ARN header")
				}
				ua := req.Header.Get("User-Agent")
				if !strings.Contains(ua, "aws-sdk-js/1.0.0") || !strings.Contains(ua, "api/codewhispererruntime#1.0.0 m/N,E KiroIDE-0.9.2-") ||
					!strings.Contains(req.Header.Get("x-amz-user-agent"), "aws-sdk-js/1.0.0 KiroIDE-0.9.2-") ||
					req.Header.Get("amz-sdk-request") != "attempt=1; max=1" || req.Header.Get("amz-sdk-invocation-id") == "" {
					t.Errorf("REST fingerprint incomplete: %+v", req.Header)
				}
				resp := restResponse(statuses[calls], texts[calls], &bodies)
				calls++
				return resp, nil
			})}
			cred := &Credentials{AuthRegion: "eu-west-1", APIRegion: "ap-south-1", ProfileArn: arn}
			if models {
				got, err := ListAvailableModels(ctx, client, cred, "test-token", &Config{KiroVersion: "99.0"})
				if err != nil || len(got) != 1 || got[0].ModelID != "claude-test" || got[0].RateMultiplier != 1.5 {
					t.Errorf("models = %+v, error = %v", got, err)
				}
			} else {
				got, err := GetUsageLimits(ctx, client, cred, "test-token", &Config{KiroVersion: "99.0"})
				if err != nil || got == nil || got.SubscriptionInfo == nil || got.SubscriptionInfo.SubscriptionTitle == nil || *got.SubscriptionInfo.SubscriptionTitle != "PRO" || len(got.UsageBreakdownList) != 1 || got.UsageBreakdownList[0].UsageLimit != 10 {
					t.Errorf("usage = %+v, error = %v", got, err)
				}
			}
			if calls != len(wantURLs) {
				t.Errorf("requests = %d, want %d", calls, len(wantURLs))
			}
			for i, b := range bodies {
				if !b.closed {
					t.Errorf("response %d body not closed", i)
				}
			}
		})
	}
}

func TestRESTStopsOnNonFallbackErrors(t *testing.T) {
	const arn = "arn:aws:codewhisperer:us-east-1:123456789012:profile/REAL"
	cases := []struct {
		name       string
		statuses   []int
		texts      []string
		profileArn string
		wantCalls  int
		wantError  string
	}{
		{"rate limit", []int{429}, []string{"slow down"}, arn, 1, "HTTP 429"},
		{"unauthorized", []int{401}, []string{"invalid token"}, arn, 1, "HTTP 401"},
		{"unrelated bad request", []int{400}, []string{"other error"}, arn, 1, "HTTP 400"},
		{"server error", []int{503}, []string{"unavailable"}, arn, 1, "HTTP 503"},
		{"bad request without ARN", []int{403, 400}, []string{"denied", "Invalid profileArn"}, arn, 2, "HTTP 400"},
		{"invalid ARN recovers", []int{400, 200}, []string{"Invalid profileArn", `{}`}, arn, 2, ""},
		{"no real ARN", []int{403, 403}, []string{"denied", "denied"}, BuilderIDProfileArn, 2, "HTTP 403"},
	}
	for _, models := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("models=%t/%s", models, tc.name), func(t *testing.T) {
				calls := 0
				var bodies []*trackedRESTBody
				client := &http.Client{Transport: restRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					if calls >= len(tc.statuses) {
						t.Fatalf("unexpected retry at %s", req.URL)
					}
					if tc.profileArn == BuilderIDProfileArn {
						if strings.Contains(req.URL.RawQuery, "profileArn=") {
							t.Error("Builder ID placeholder sent in query")
						}
						wantHost := []string{"q.us-east-1.amazonaws.com", "q.eu-central-1.amazonaws.com"}[calls]
						if req.URL.Host != wantHost {
							t.Errorf("no-ARN request %d host = %s, want %s", calls, req.URL.Host, wantHost)
						}
					}
					if req.Header.Get("x-amzn-kiro-profile-arn") != "" {
						t.Error("Builder ID/profile ARN header sent")
					}
					resp := restResponse(tc.statuses[calls], tc.texts[calls], &bodies)
					calls++
					return resp, nil
				})}
				err := restCall(context.Background(), client, &Credentials{ProfileArn: tc.profileArn}, models)
				if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
					t.Errorf("error = %v, want substring %q", err, tc.wantError)
				}
				if calls != tc.wantCalls {
					t.Errorf("requests = %d, want %d", calls, tc.wantCalls)
				}
				for i, b := range bodies {
					if !b.closed {
						t.Errorf("response %d body not closed", i)
					}
				}
			})
		}
	}
}

func TestRESTAPIKeyOmitsProfileARN(t *testing.T) {
	for _, models := range []bool{false, true} {
		calls := 0
		var bodies []*trackedRESTBody
		client := &http.Client{Transport: restRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if strings.Contains(req.URL.RawQuery, "profileArn=") || req.Header.Get("x-amzn-kiro-profile-arn") != "" || req.Header.Get("TokenType") != "API_KEY" {
				t.Errorf("models=%t: unexpected API key request URL/headers: %s %+v", models, req.URL, req.Header)
			}
			return restResponse(200, `{}`, &bodies), nil
		})}
		err := restCall(context.Background(), client, &Credentials{
			AuthMethod: AuthAPIKey, ProfileArn: "arn:aws:codewhisperer:us-east-1:123456789012:profile/REAL",
		}, models)
		if err != nil || calls != 1 || len(bodies) != 1 || !bodies[0].closed {
			t.Errorf("models=%t: calls=%d, error=%v, bodies=%+v", models, calls, err, bodies)
		}
	}
}

func TestRESTTransportErrorDoesNotRetry(t *testing.T) {
	for _, models := range []bool{false, true} {
		calls := 0
		client := &http.Client{Transport: restRoundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("offline")
		})}
		err := restCall(context.Background(), client, &Credentials{ProfileArn: "arn:aws:codewhisperer:us-east-1:123456789012:profile/REAL"}, models)
		if calls != 1 || err == nil || !strings.Contains(err.Error(), "offline") {
			t.Errorf("models=%t: calls=%d, error=%v", models, calls, err)
		}
	}
}
