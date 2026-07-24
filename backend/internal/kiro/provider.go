package kiro

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// This file ports kiro-rs kiro::provider::KiroProvider. It owns per-credential
// upstream orchestration: multi-endpoint retry (ide -> cli), HTTP status
// classification, transient-error backoff and one-shot force-refresh when the
// upstream reports the bearer token invalid.
//
// Credential (account) selection and cross-credential failover remain the
// caller's responsibility (sub2api's gateway scheduler). Forward returns a
// typed Disposition so the caller can drive its existing account failover.

// Retry limits mirror kiro-rs (per-endpoint attempts and overall backoff).
const (
	maxAttemptsPerEndpoint = 3
	retryBaseMillis        = 200
	retryMaxMillis         = 2000
)

// Disposition classifies an upstream outcome so the caller can react.
type Disposition int

const (
	// DispSuccess indicates a 2xx response (ForwardResponse is populated).
	DispSuccess Disposition = iota
	// DispBadRequest is a 400: the request is malformed; do not fail over.
	DispBadRequest
	// DispAuthFailure is a 401/403 that survived force-refresh; fail over.
	DispAuthFailure
	// DispQuotaExhausted is a 402 monthly-limit; disable credential + fail over.
	DispQuotaExhausted
	// DispThrottled is a 429; cool down the credential + fail over.
	DispThrottled
	// DispTransient is a 408/5xx/network error after retries; may fail over.
	DispTransient
	// DispClientError is another non-retryable 4xx; do not fail over.
	DispClientError
	// DispUnknown is an unclassified failure; treat as retryable.
	DispUnknown
)

// ForwardInput carries everything needed to make one credential's upstream call.
type ForwardInput struct {
	Credentials *Credentials
	Token       string
	MachineID   string
	Config      *Config

	// RequestBody is the serialized CodeWhisperer conversationState JSON.
	RequestBody string
	Model       string

	// ForceRefresh, when non-nil, is invoked at most once (across the whole
	// Forward call) when the upstream signals the bearer token is invalid. It
	// should refresh the credential and return a fresh access token.
	ForceRefresh func(ctx context.Context) (string, error)
}

// ForwardResponse is a successful upstream response. The caller owns Body and
// must Close it.
type ForwardResponse struct {
	Body     io.ReadCloser
	Status   int
	Header   http.Header
	Endpoint string
}

// UpstreamError is a classified upstream failure.
type UpstreamError struct {
	Disposition Disposition
	Status      int
	Body        string
	Endpoint    string
	Err         error
}

func (e *UpstreamError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("kiro upstream (%s): %v", e.Endpoint, e.Err)
	}
	return fmt.Sprintf("kiro upstream (%s): %d %s", e.Endpoint, e.Status, e.Body)
}

// Provider orchestrates upstream calls across the endpoint registry.
type Provider struct {
	client        *http.Client
	endpoints     map[string]Endpoint
	endpointOrder []string
	sleep         func(time.Duration) // overridable for tests
	rng           *rand.Rand
}

// NewProvider builds a Provider. endpointOrder is the ide->cli fallback order;
// a credential's explicit Endpoint field overrides it (single endpoint only).
func NewProvider(client *http.Client, endpoints map[string]Endpoint, endpointOrder []string) *Provider {
	if client == nil {
		client = http.DefaultClient
	}
	if len(endpointOrder) == 0 {
		endpointOrder = []string{EndpointIDE, EndpointCLI}
	}
	return &Provider{
		client:        client,
		endpoints:     endpoints,
		endpointOrder: endpointOrder,
		sleep:         time.Sleep,
		rng:           rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// endpointsFor resolves the ordered endpoint list for a credential.
func (p *Provider) endpointsFor(c *Credentials) []Endpoint {
	if c.Endpoint != "" {
		if ep, ok := p.endpoints[c.Endpoint]; ok {
			return []Endpoint{ep}
		}
	}
	var out []Endpoint
	for _, name := range p.endpointOrder {
		if ep, ok := p.endpoints[name]; ok {
			out = append(out, ep)
		}
	}
	return out
}

// Forward performs the upstream call for a single credential, trying each
// endpoint in order with per-endpoint retry. On success it returns a live
// ForwardResponse (caller must Close Body). On failure it returns a classified
// *UpstreamError so the caller can drive account-level failover.
func (p *Provider) Forward(ctx context.Context, input *ForwardInput) (*ForwardResponse, error) {
	cfg := input.Config
	if cfg == nil {
		cfg = DefaultConfig()
	}
	token := input.Token
	forceRefreshUsed := false
	var lastErr *UpstreamError

	endpoints := p.endpointsFor(input.Credentials)
	if len(endpoints) == 0 {
		return nil, &UpstreamError{Disposition: DispUnknown, Err: fmt.Errorf("no endpoint configured")}
	}

	for _, ep := range endpoints {
		attempt := 0
		for attempt < maxAttemptsPerEndpoint {
			rctx := &RequestContext{
				Credentials: input.Credentials,
				Token:       token,
				MachineID:   input.MachineID,
				Config:      cfg,
			}
			resp, err := p.doRequest(ctx, ep, rctx, input.RequestBody)
			if err != nil {
				// Network/transport error: retryable, does not disable credential.
				lastErr = &UpstreamError{Disposition: DispTransient, Endpoint: ep.Name(), Err: err}
				attempt++
				if attempt < maxAttemptsPerEndpoint {
					p.sleep(p.retryDelay(attempt))
				}
				continue
			}

			status := resp.StatusCode
			if status >= 200 && status < 300 {
				return &ForwardResponse{
					Body:     resp.Body,
					Status:   status,
					Header:   resp.Header,
					Endpoint: ep.Name(),
				}, nil
			}

			body := readBodyString(resp.Body)
			_ = resp.Body.Close()

			switch {
			case status == 402 && ep.IsMonthlyRequestLimit(body):
				// Monthly quota is account-scoped: other endpoints won't help.
				return nil, &UpstreamError{Disposition: DispQuotaExhausted, Status: status, Body: body, Endpoint: ep.Name()}

			case status == 400:
				return nil, &UpstreamError{Disposition: DispBadRequest, Status: status, Body: body, Endpoint: ep.Name()}

			case status == 401 || status == 403:
				if ep.IsBearerTokenInvalid(body) && input.ForceRefresh != nil && !forceRefreshUsed {
					forceRefreshUsed = true
					if newToken, rerr := input.ForceRefresh(ctx); rerr == nil && newToken != "" {
						token = newToken
						continue // retry same endpoint with refreshed token
					}
				}
				lastErr = &UpstreamError{Disposition: DispAuthFailure, Status: status, Body: body, Endpoint: ep.Name()}
				// Auth won't recover on this endpoint; try the next endpoint.
				attempt = maxAttemptsPerEndpoint

			case status == 429:
				// Throttle is credential-level: cool down + fail over.
				return nil, &UpstreamError{Disposition: DispThrottled, Status: status, Body: body, Endpoint: ep.Name()}

			case status == 408 || status >= 500:
				lastErr = &UpstreamError{Disposition: DispTransient, Status: status, Body: body, Endpoint: ep.Name()}
				attempt++
				if attempt < maxAttemptsPerEndpoint {
					p.sleep(p.retryDelay(attempt))
				}

			case status >= 400 && status < 500:
				return nil, &UpstreamError{Disposition: DispClientError, Status: status, Body: body, Endpoint: ep.Name()}

			default:
				lastErr = &UpstreamError{Disposition: DispUnknown, Status: status, Body: body, Endpoint: ep.Name()}
				attempt++
				if attempt < maxAttemptsPerEndpoint {
					p.sleep(p.retryDelay(attempt))
				}
			}
		}
		// This endpoint exhausted; fall through to the next endpoint.
	}

	if lastErr == nil {
		lastErr = &UpstreamError{Disposition: DispUnknown, Err: fmt.Errorf("all endpoints exhausted")}
	}
	return nil, lastErr
}

// doRequest builds, decorates and sends one upstream request.
func (p *Provider) doRequest(ctx context.Context, ep Endpoint, rctx *RequestContext, requestBody string) (*http.Response, error) {
	url := ep.APIURL(rctx)
	body := ep.TransformAPIBody(requestBody, rctx)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("Connection", "close")
	ep.DecorateAPI(req.Header, rctx)
	// Host header must be set on req.Host (net/http ignores Header["Host"]).
	if h := req.Header.Get("host"); h != "" {
		req.Host = h
	}
	return p.client.Do(req)
}

// readBodyString reads up to 2MiB of an error body for classification/logging.
func readBodyString(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 2<<20))
	return string(b)
}

// retryDelay computes exponential backoff with jitter.
func (p *Provider) retryDelay(attempt int) time.Duration {
	exp := retryBaseMillis << uint(min(attempt, 6))
	if exp > retryMaxMillis {
		exp = retryMaxMillis
	}
	jitterMax := exp / 4
	if jitterMax < 1 {
		jitterMax = 1
	}
	jitter := p.rng.Intn(jitterMax + 1)
	return time.Duration(exp+jitter) * time.Millisecond
}
