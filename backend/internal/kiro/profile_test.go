package kiro

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type profileRoundTrip func(*http.Request) (*http.Response, error)

func (f profileRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestListAvailableProfilesContinuesAfterRegionalError(t *testing.T) {
	var regions []string
	client := &http.Client{Transport: profileRoundTrip(func(r *http.Request) (*http.Response, error) {
		regions = append(regions, r.URL.Host)
		if r.Header.Get("X-Amz-Target") != listAvailableProfilesTarget {
			t.Error("missing ListAvailableProfiles target")
		}
		body, status := `{"message":"sensitive"}`, http.StatusBadGateway
		if strings.Contains(r.URL.Host, "eu-central-1") {
			body, status = `{"profiles":[{"profileArn":"arn:aws:codewhisperer:eu-central-1:123456789012:profile/REAL"}]}`, http.StatusOK
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	profiles, err := ListAvailableProfiles(context.Background(), client, &Credentials{AuthMethod: AuthIDC}, DefaultConfig(), "test-token")
	if err == nil || len(profiles) != 0 || len(regions) != 2 || strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "test-token") {
		t.Fatalf("partial regional scan must fail without leaking response or token: profiles=%v error=%v regions=%v", profiles, err, regions)
	}
}

func TestListAvailableProfilesMasksTransportSecrets(t *testing.T) {
	client := &http.Client{Transport: profileRoundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("Bearer secret-from-proxy")
	})}
	profiles, err := ListAvailableProfiles(context.Background(), client, &Credentials{}, DefaultConfig(), "secret-token")
	if err == nil || len(profiles) != 0 || strings.Contains(err.Error(), "secret") {
		t.Fatalf("transport secret leaked from incomplete discovery: profiles=%v error=%v", profiles, err)
	}
}

func TestListAvailableProfilesPartialFailureCannotConfirmEmpty(t *testing.T) {
	client := &http.Client{Transport: profileRoundTrip(func(r *http.Request) (*http.Response, error) {
		status := http.StatusOK
		if strings.Contains(r.URL.Host, "us-east-1") {
			status = http.StatusBadGateway
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"profiles":[]}`)), Header: make(http.Header)}, nil
	})}
	profiles, err := ListAvailableProfiles(context.Background(), client, &Credentials{}, DefaultConfig(), "test-token")
	if err == nil || len(profiles) != 0 || strings.Contains(err.Error(), "test-token") {
		t.Fatalf("expected sanitized partial failure: profiles=%v error=%v", profiles, err)
	}
}

func TestSelectProfileArnRegionalPriorityAndConflicts(t *testing.T) {
	us := AvailableProfile{ProfileArn: "arn:aws:codewhisperer:us-east-1:123456789012:profile/US", Region: "us-east-1"}
	eu := AvailableProfile{ProfileArn: "arn:aws:codewhisperer:eu-central-1:123456789012:profile/EU", Region: "eu-central-1"}
	cases := []struct {
		name     string
		profiles []AvailableProfile
		cred     Credentials
		want     string
		conflict bool
	}{
		{"single", []AvailableProfile{eu}, Credentials{}, eu.ProfileArn, false},
		{"single explicit API mismatch", []AvailableProfile{eu}, Credentials{APIRegion: "us-east-1"}, "", true},
		{"explicit API wins", []AvailableProfile{us, eu}, Credentials{APIRegion: "eu-central-1", AuthRegion: "us-east-1"}, eu.ProfileArn, false},
		{"SSO region", []AvailableProfile{us, eu}, Credentials{AuthRegion: "eu-central-1"}, eu.ProfileArn, false},
		{"credential region", []AvailableProfile{us, eu}, Credentials{Region: "us-east-1"}, us.ProfileArn, false},
		{"ambiguous without region", []AvailableProfile{us, eu}, Credentials{}, "", true},
		{"explicit API no match", []AvailableProfile{us, eu}, Credentials{APIRegion: "ap-south-1", AuthRegion: "eu-central-1"}, "", true},
		{"multiple in same region", []AvailableProfile{us, us}, Credentials{APIRegion: "us-east-1"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelectProfileArn(tc.profiles, &tc.cred)
			if got != tc.want || (err != nil) != tc.conflict {
				t.Fatalf("got=%q error=%v; want=%q conflict=%t", got, err, tc.want, tc.conflict)
			}
			if err != nil && (strings.Contains(err.Error(), us.ProfileArn) || strings.Contains(err.Error(), eu.ProfileArn)) {
				t.Fatal("conflict diagnostic exposed a profile ARN")
			}
		})
	}
}

func TestListAvailableProfilesPaginationAndInvalidARN(t *testing.T) {
	var pages int
	client := &http.Client{Transport: profileRoundTrip(func(r *http.Request) (*http.Response, error) {
		body := `{"profiles":[]}`
		if strings.Contains(r.URL.Host, "us-east-1") {
			pages++
			if pages == 1 {
				body = `{"profiles":[{"profileArn":"arn:aws:codewhisperer:bad.domain:123456789012:profile/EVIL"}],"nextToken":"page2"}`
			} else {
				body = `{"profiles":[{"profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/REAL"}]}`
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	profiles, err := ListAvailableProfiles(context.Background(), client, &Credentials{}, DefaultConfig(), "test-token")
	if err != nil || pages != 2 || len(profiles) != 1 || profiles[0].Region != "us-east-1" {
		t.Fatalf("profiles=%v pages=%d error=%v", profiles, pages, err)
	}
}
