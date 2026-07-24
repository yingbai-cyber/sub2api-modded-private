package kiro

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// This file ports kiro-rs kiro::endpoint (mod/ide/cli). Different Kiro upstream
// endpoints (IDE vs CLI/runtime) differ in URL, headers and body shape but
// share the credential pool, token refresh, retry and event-stream decoding.
//
// The Endpoint interface abstracts the request-side differences; a Provider
// holds a registry and selects an implementation per credential.endpoint.

// Endpoint names.
const (
	EndpointIDE = "ide"
	EndpointCLI = "cli"
)

// RequestContext carries all per-call runtime info needed to decorate a request.
type RequestContext struct {
	Credentials *Credentials
	Token       string // effective bearer token (kiroApiKey for API-key creds)
	MachineID   string
	Config      *Config
}

// Endpoint abstracts the request-side differences between Kiro upstreams.
type Endpoint interface {
	// Name returns the endpoint identifier ("ide" / "cli").
	Name() string
	// APIURL returns the generateAssistantResponse URL.
	APIURL(ctx *RequestContext) string
	// DecorateAPI sets endpoint-specific headers on an outgoing API request.
	DecorateAPI(h http.Header, ctx *RequestContext)
	// TransformAPIBody rewrites the serialized request body (e.g. inject profileArn).
	TransformAPIBody(body string, ctx *RequestContext) string
	// IsMonthlyRequestLimit reports whether the body signals a monthly quota exhaustion.
	IsMonthlyRequestLimit(body string) bool
	// IsBearerTokenInvalid reports whether the body signals an invalid bearer token.
	IsBearerTokenInvalid(body string) bool
}

// defaultIsMonthlyRequestLimit recognises both the top-level `reason` field and
// the nested `error.reason` field (mirrors kiro-rs default).
func defaultIsMonthlyRequestLimit(body string) bool {
	if strings.Contains(body, "MONTHLY_REQUEST_COUNT") {
		return true
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return false
	}
	if r, ok := v["reason"].(string); ok && r == "MONTHLY_REQUEST_COUNT" {
		return true
	}
	if errObj, ok := v["error"].(map[string]any); ok {
		if r, ok := errObj["reason"].(string); ok && r == "MONTHLY_REQUEST_COUNT" {
			return true
		}
	}
	return false
}

// defaultIsBearerTokenInvalid mirrors kiro-rs default.
func defaultIsBearerTokenInvalid(body string) bool {
	return strings.Contains(body, "The bearer token included in the request is invalid")
}

// NewEndpointRegistry returns the default endpoint registry (ide + cli).
func NewEndpointRegistry() map[string]Endpoint {
	return map[string]Endpoint{
		EndpointIDE: &ideEndpoint{},
		EndpointCLI: &cliEndpoint{},
	}
}

// ideEndpoint targets the AWS CodeWhisperer q.{region}.amazonaws.com upstream
// used by the Kiro IDE client (aws-sdk-js UA, profileArn injected into body).
type ideEndpoint struct{}

func (e *ideEndpoint) Name() string { return EndpointIDE }

func (e *ideEndpoint) apiRegion(ctx *RequestContext) string {
	return ctx.Credentials.EffectiveAPIRegion(ctx.Config)
}

func (e *ideEndpoint) host(ctx *RequestContext) string {
	return "q." + e.apiRegion(ctx) + ".amazonaws.com"
}

func (e *ideEndpoint) APIURL(ctx *RequestContext) string {
	return "https://q." + e.apiRegion(ctx) + ".amazonaws.com/generateAssistantResponse"
}

func (e *ideEndpoint) xAmzUserAgent(ctx *RequestContext) string {
	return "aws-sdk-js/1.0.34 KiroIDE-" + ctx.Config.kiroVersion() + "-" + ctx.MachineID
}

func (e *ideEndpoint) userAgent(ctx *RequestContext) string {
	return "aws-sdk-js/1.0.34 ua/2.1 os/" + ctx.Config.systemVersion() +
		" lang/js md/nodejs#" + ctx.Config.nodeVersion() +
		" api/codewhispererstreaming#1.0.34 m/E KiroIDE-" +
		ctx.Config.kiroVersion() + "-" + ctx.MachineID
}

func (e *ideEndpoint) DecorateAPI(h http.Header, ctx *RequestContext) {
	h.Set("x-amzn-codewhisperer-optout", "true")
	h.Set("x-amzn-kiro-agent-mode", "vibe")
	h.Set("x-amz-user-agent", e.xAmzUserAgent(ctx))
	h.Set("user-agent", e.userAgent(ctx))
	h.Set("host", e.host(ctx))
	h.Set("amz-sdk-invocation-id", newInvocationID())
	h.Set("amz-sdk-request", "attempt=1; max=3")
	h.Set("Authorization", "Bearer "+ctx.Token)
	if ctx.Credentials.ProfileArn != "" {
		h.Set("x-amzn-kiro-profile-arn", ctx.Credentials.ProfileArn)
	}
	if tt := ctx.Credentials.TokenTypeHeader(); tt != "" {
		h.Set("TokenType", tt)
	}
}

func (e *ideEndpoint) TransformAPIBody(body string, ctx *RequestContext) string {
	return injectProfileArn(body, ctx.Credentials.ProfileArn)
}

func (e *ideEndpoint) IsMonthlyRequestLimit(body string) bool {
	return defaultIsMonthlyRequestLimit(body)
}

func (e *ideEndpoint) IsBearerTokenInvalid(body string) bool {
	return defaultIsBearerTokenInvalid(body)
}

// injectProfileArn sets profileArn on the request body JSON root object.
func injectProfileArn(requestBody, profileArn string) string {
	if profileArn == "" {
		return requestBody
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(requestBody), &v); err != nil {
		return requestBody
	}
	v["profileArn"] = profileArn
	out, err := json.Marshal(v)
	if err != nil {
		return requestBody
	}
	return string(out)
}

// cliEndpoint targets the runtime.{region}.kiro.dev upstream used by the Kiro
// CLI (aws-sdk-rust UA, X-Amz-Target GenerateAssistantResponse, no profileArn).
type cliEndpoint struct{}

const (
	cliAmzTarget     = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
	cliAppVersion    = "2.5.1"
	cliAWSSDKVersion = "1.3.15"
	cliCWAPIVersion  = "0.1.16551"
)

func (e *cliEndpoint) Name() string { return EndpointCLI }

func (e *cliEndpoint) apiRegion(ctx *RequestContext) string {
	return ctx.Credentials.EffectiveAPIRegion(ctx.Config)
}

func (e *cliEndpoint) host(ctx *RequestContext) string {
	return "runtime." + e.apiRegion(ctx) + ".kiro.dev"
}

func (e *cliEndpoint) APIURL(ctx *RequestContext) string {
	return "https://" + e.host(ctx) + "/"
}

func (e *cliEndpoint) userAgent() string {
	return "aws-sdk-rust/" + cliAWSSDKVersion +
		" ua/2.1 api/codewhispererstreaming/" + cliCWAPIVersion +
		" os/macos lang/rust/1.92.0 md/appVersion-" + cliAppVersion +
		" app/AmazonQ-For-CLI"
}

func (e *cliEndpoint) xAmzUserAgent() string {
	return "aws-sdk-rust/" + cliAWSSDKVersion +
		" ua/2.1 api/codewhispererstreaming/" + cliCWAPIVersion +
		" os/macos lang/rust/1.92.0 m/F app/AmazonQ-For-CLI"
}

func (e *cliEndpoint) DecorateAPI(h http.Header, ctx *RequestContext) {
	h.Set("Authorization", "Bearer "+ctx.Token)
	h.Set("Accept", "*/*")
	h.Set("X-Amz-Target", cliAmzTarget)
	h.Set("User-Agent", e.userAgent())
	h.Set("x-amz-user-agent", e.xAmzUserAgent())
	h.Set("x-amzn-codewhisperer-optout", "false")
	h.Set("host", e.host(ctx))
	h.Set("amz-sdk-invocation-id", newInvocationID())
	h.Set("amz-sdk-request", "attempt=1; max=3")
}

func (e *cliEndpoint) TransformAPIBody(body string, _ *RequestContext) string {
	return body
}

func (e *cliEndpoint) IsMonthlyRequestLimit(body string) bool {
	return defaultIsMonthlyRequestLimit(body)
}

func (e *cliEndpoint) IsBearerTokenInvalid(body string) bool {
	return defaultIsBearerTokenInvalid(body)
}

// newInvocationID returns a fresh amz-sdk-invocation-id value.
func newInvocationID() string { return uuid.NewString() }
