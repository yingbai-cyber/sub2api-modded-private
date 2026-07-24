package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// This file ports kiro-rs kiro::token_manager::list_available_profiles: it
// scans a fixed set of regions calling the CodeWhisperer ListAvailableProfiles
// JSON-RPC target to discover a credential's enterprise profile ARN(s). Used
// for lazy profileArn resolution when a social/idc credential has none.

// profileScanRegions mirrors kiro-rs PROFILE_SCAN_REGIONS.
var profileScanRegions = []string{"us-east-1", "eu-central-1"}

const listAvailableProfilesTarget = "AmazonCodeWhispererService.ListAvailableProfiles"

// AvailableProfile is a discovered enterprise profile.
type AvailableProfile struct {
	ProfileArn  string
	ProfileName string
	Region      string
}

// listProfilesResponse mirrors the JSON-RPC response (with field aliases).
type listProfilesResponse struct {
	Profiles     []availableProfileRaw `json:"profiles"`
	NextToken    string                `json:"nextToken"`
	AltProfilesA []availableProfileRaw `json:"availableProfiles"`
	AltProfilesB []availableProfileRaw `json:"profileSummaries"`
}

func (r *listProfilesResponse) allProfiles() []availableProfileRaw {
	if len(r.Profiles) > 0 {
		return r.Profiles
	}
	if len(r.AltProfilesA) > 0 {
		return r.AltProfilesA
	}
	return r.AltProfilesB
}

type availableProfileRaw struct {
	ProfileArn  string `json:"profileArn"`
	Arn         string `json:"arn"`
	ProfileName string `json:"profileName"`
	Name        string `json:"name"`
}

func (p availableProfileRaw) arn() string {
	if p.ProfileArn != "" {
		return p.ProfileArn
	}
	return p.Arn
}

func (p availableProfileRaw) name() string {
	if p.ProfileName != "" {
		return p.ProfileName
	}
	return p.Name
}

// ListAvailableProfiles discovers profile ARNs for a credential across the
// scan regions, de-duplicating by ARN and paginating via nextToken.
func ListAvailableProfiles(ctx context.Context, client *http.Client, c *Credentials, cfg *Config, token string) ([]AvailableProfile, error) {
	machineID := GenerateMachineID(c, "")
	kiroVersion := cfg.kiroVersion()
	var result []AvailableProfile
	seen := map[string]struct{}{}

	for _, region := range profileScanRegions {
		host := "q." + region + ".amazonaws.com"
		url := "https://" + host + "/"
		var nextToken string

		for {
			userAgent := "aws-sdk-js/2.0.0 ua/2.1 os/" + cfg.systemVersion() +
				" lang/js md/nodejs#" + cfg.nodeVersion() +
				" api/codewhisperer#2022-11-11 m/E KiroIDE-" + kiroVersion + "-" + machineID
			amzUserAgent := "aws-sdk-js/2.0.0 KiroIDE-" + kiroVersion + "-" + machineID

			payload := map[string]any{"maxResults": 10}
			if nextToken != "" {
				payload["nextToken"] = nextToken
			}

			headers := map[string]string{
				"Content-Type":                "application/x-amz-json-1.0",
				"Accept":                      "application/json",
				"X-Amz-Target":                listAvailableProfilesTarget,
				"user-agent":                  userAgent,
				"x-amz-user-agent":            amzUserAgent,
				"x-amzn-codewhisperer-optout": "true",
				"host":                        host,
				"amz-sdk-invocation-id":       newInvocationID(),
				"amz-sdk-request":             "attempt=1; max=1",
				"Authorization":               "Bearer " + token,
				"Connection":                  "close",
			}
			if c.IsAPIKey() {
				headers["tokentype"] = "API_KEY"
			}

			status, body, err := doJSON(ctx, client, url, headers, payload)
			if err != nil {
				return nil, err
			}
			if status < 200 || status >= 300 {
				return nil, fmt.Errorf("kiro: ListAvailableProfiles failed (%s): %d %s", region, status, string(body))
			}

			var data listProfilesResponse
			if err := json.Unmarshal(body, &data); err != nil {
				return nil, err
			}
			for _, p := range data.allProfiles() {
				arn := p.arn()
				if arn == "" {
					continue
				}
				if _, dup := seen[arn]; dup {
					continue
				}
				seen[arn] = struct{}{}
				profileRegion := regionFromProfileArn(arn)
				if profileRegion == "" {
					profileRegion = region
				}
				result = append(result, AvailableProfile{
					ProfileArn:  arn,
					ProfileName: p.name(),
					Region:      profileRegion,
				})
			}

			nextToken = data.NextToken
			if nextToken == "" {
				break
			}
		}
	}

	return result, nil
}
