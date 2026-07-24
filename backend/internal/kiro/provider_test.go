package kiro

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testProvider builds a Provider whose endpoints all target one test server URL
// (host header is overridden but the client dials the test server). We inject a
// custom endpoint list to point APIURL at the server.
type fakeEndpoint struct {
	name          string
	url           string
	monthly       bool
	bearerInvalid bool
}

func (e *fakeEndpoint) Name() string                      { return e.name }
func (e *fakeEndpoint) APIURL(ctx *RequestContext) string { return e.url }
func (e *fakeEndpoint) DecorateAPI(h http.Header, c *RequestContext) {
	h.Set("Authorization", "Bearer "+c.Token)
}
func (e *fakeEndpoint) TransformAPIBody(body string, c *RequestContext) string { return body }
func (e *fakeEndpoint) IsMonthlyRequestLimit(body string) bool {
	return strings.Contains(body, "MONTHLY_REQUEST_COUNT")
}
func (e *fakeEndpoint) IsBearerTokenInvalid(body string) bool {
	return strings.Contains(body, "bearer token")
}

func newTestProvider(endpoints map[string]Endpoint, order []string) *Provider {
	p := NewProvider(http.DefaultClient, endpoints, order)
	p.sleep = func(_ time.Duration) {} // no real delays in tests
	return p
}

func baseInput(body string) *ForwardInput {
	return &ForwardInput{
		Credentials: &Credentials{AuthMethod: AuthSocial},
		Token:       "tok",
		MachineID:   "mid",
		Config:      DefaultConfig(),
		RequestBody: body,
		Model:       "claude-sonnet-4",
	}
}

func TestProviderForwardSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok-body")
	}))
	defer srv.Close()

	eps := map[string]Endpoint{EndpointIDE: &fakeEndpoint{name: EndpointIDE, url: srv.URL}}
	p := newTestProvider(eps, []string{EndpointIDE})

	resp, err := p.Forward(context.Background(), baseInput("{}"))
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	defer resp.Body.Close()
	if resp.Endpoint != EndpointIDE {
		t.Errorf("endpoint = %q", resp.Endpoint)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "ok-body" {
		t.Errorf("body = %q", b)
	}
}

func TestProviderFallsBackIdeToCli(t *testing.T) {
	// IDE returns 500 always; CLI succeeds. Provider should fall back to CLI.
	ideHits := int32(0)
	ide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&ideHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "boom")
	}))
	defer ide.Close()
	cli := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "cli-ok")
	}))
	defer cli.Close()

	eps := map[string]Endpoint{
		EndpointIDE: &fakeEndpoint{name: EndpointIDE, url: ide.URL},
		EndpointCLI: &fakeEndpoint{name: EndpointCLI, url: cli.URL},
	}
	p := newTestProvider(eps, []string{EndpointIDE, EndpointCLI})

	resp, err := p.Forward(context.Background(), baseInput("{}"))
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	defer resp.Body.Close()
	if resp.Endpoint != EndpointCLI {
		t.Errorf("expected fallback to CLI, got %q", resp.Endpoint)
	}
	if atomic.LoadInt32(&ideHits) != maxAttemptsPerEndpoint {
		t.Errorf("ide hits = %d; want %d (retried then fell back)", ideHits, maxAttemptsPerEndpoint)
	}
}

func TestProviderQuotaExhaustedNoFallback(t *testing.T) {
	// 402 monthly-limit is account-scoped: must NOT try the other endpoint.
	cliHit := int32(0)
	ide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(402)
		io.WriteString(w, `{"reason":"MONTHLY_REQUEST_COUNT"}`)
	}))
	defer ide.Close()
	cli := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&cliHit, 1)
		io.WriteString(w, "cli-ok")
	}))
	defer cli.Close()

	eps := map[string]Endpoint{
		EndpointIDE: &fakeEndpoint{name: EndpointIDE, url: ide.URL},
		EndpointCLI: &fakeEndpoint{name: EndpointCLI, url: cli.URL},
	}
	p := newTestProvider(eps, []string{EndpointIDE, EndpointCLI})

	_, err := p.Forward(context.Background(), baseInput("{}"))
	ue, ok := err.(*UpstreamError)
	if !ok {
		t.Fatalf("expected *UpstreamError, got %T", err)
	}
	if ue.Disposition != DispQuotaExhausted {
		t.Errorf("disposition = %v; want DispQuotaExhausted", ue.Disposition)
	}
	if atomic.LoadInt32(&cliHit) != 0 {
		t.Error("CLI must not be tried after account-scoped quota exhaustion")
	}
}

func TestProviderForceRefreshOnBearerInvalid(t *testing.T) {
	// First hit: 401 bearer-invalid. After force-refresh, second hit: 200.
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.WriteHeader(401)
			io.WriteString(w, "the bearer token is invalid")
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer refreshed" {
			t.Errorf("second attempt auth = %q; want refreshed token", got)
		}
		io.WriteString(w, "ok-after-refresh")
	}))
	defer srv.Close()

	eps := map[string]Endpoint{EndpointIDE: &fakeEndpoint{name: EndpointIDE, url: srv.URL}}
	p := newTestProvider(eps, []string{EndpointIDE})

	refreshCalls := int32(0)
	input := baseInput("{}")
	input.ForceRefresh = func(ctx context.Context) (string, error) {
		atomic.AddInt32(&refreshCalls, 1)
		return "refreshed", nil
	}

	resp, err := p.Forward(context.Background(), input)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	defer resp.Body.Close()
	if atomic.LoadInt32(&refreshCalls) != 1 {
		t.Errorf("force-refresh calls = %d; want 1", refreshCalls)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "ok-after-refresh" {
		t.Errorf("body = %q", b)
	}
}

func TestProviderBadRequestNoRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(400)
		io.WriteString(w, "bad")
	}))
	defer srv.Close()

	eps := map[string]Endpoint{EndpointIDE: &fakeEndpoint{name: EndpointIDE, url: srv.URL}}
	p := newTestProvider(eps, []string{EndpointIDE})

	_, err := p.Forward(context.Background(), baseInput("{}"))
	ue, _ := err.(*UpstreamError)
	if ue == nil || ue.Disposition != DispBadRequest {
		t.Fatalf("expected DispBadRequest, got %v", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("400 should not retry; hits = %d", hits)
	}
}

func TestProviderExplicitEndpointOverride(t *testing.T) {
	// Credential pins endpoint=cli; ide must never be called.
	ide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("ide should not be called when credential pins cli")
	}))
	defer ide.Close()
	cli := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "cli-only")
	}))
	defer cli.Close()

	eps := map[string]Endpoint{
		EndpointIDE: &fakeEndpoint{name: EndpointIDE, url: ide.URL},
		EndpointCLI: &fakeEndpoint{name: EndpointCLI, url: cli.URL},
	}
	p := newTestProvider(eps, []string{EndpointIDE, EndpointCLI})

	input := baseInput("{}")
	input.Credentials.Endpoint = EndpointCLI
	resp, err := p.Forward(context.Background(), input)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	defer resp.Body.Close()
	if resp.Endpoint != EndpointCLI {
		t.Errorf("endpoint = %q; want cli", resp.Endpoint)
	}
}
