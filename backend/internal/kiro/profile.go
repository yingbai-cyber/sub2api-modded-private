package kiro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
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

// SelectProfileArn selects a profile deterministically. When several profiles
// exist, an explicitly configured API region takes precedence over the SSO
// region; without a unique regional match, require user configuration instead
// of silently assigning another enterprise's profile. Errors contain regions,
// never full ARNs or bearer credentials.
func SelectProfileArn(profiles []AvailableProfile, c *Credentials) (string, error) {
	if len(profiles) == 0 {
		return "", nil
	}
	if len(profiles) == 1 {
		if c != nil && strings.TrimSpace(c.APIRegion) != "" && strings.TrimSpace(c.APIRegion) != profiles[0].Region {
			return "", fmt.Errorf("kiro: discovered profile region [%s] conflicts with configured api_region", profiles[0].Region)
		}
		return profiles[0].ProfileArn, nil
	}
	if c == nil {
		return "", fmt.Errorf("kiro: multiple profiles require credentials with a configured region")
	}
	regions := make([]string, 0, len(profiles))
	unique := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		if _, ok := unique[profile.Region]; !ok {
			unique[profile.Region] = struct{}{}
			regions = append(regions, profile.Region)
		}
	}
	sort.Strings(regions)
	region := strings.TrimSpace(c.APIRegion)
	kind := "api_region"
	if region == "" {
		region = strings.TrimSpace(c.AuthRegion)
		kind = "auth_region"
	}
	if region == "" {
		region = strings.TrimSpace(c.Region)
		kind = "region"
	}
	if region == "" {
		return "", fmt.Errorf("kiro: %d profiles in regions [%s]; set api_region to select one", len(profiles), strings.Join(regions, ", "))
	}
	var match string
	matches := 0
	for _, profile := range profiles {
		if profile.Region == region {
			match = profile.ProfileArn
			matches++
		}
	}
	if matches != 1 {
		return "", fmt.Errorf("kiro: %d profiles match configured %s (total=%d, available regions=[%s]); set an unambiguous api_region or explicit profile_arn", matches, kind, len(profiles), strings.Join(regions, ", "))
	}
	return match, nil
}

// ListAvailableProfiles discovers profile ARNs for a credential across the
// scan regions, de-duplicating by ARN and paginating via nextToken.
// The request fingerprint matches Zyphr / usage GET (runtime 0.9.2), not the
// IDE streaming User-Agent; Q endpoints 403 mismatched client fingerprints.
func ListAvailableProfiles(ctx context.Context, client *http.Client, c *Credentials, cfg *Config, token string) ([]AvailableProfile, error) {
	machineID := GenerateMachineID(c, "")
	var result []AvailableProfile
	seen := map[string]struct{}{}
	var scanErrors []error

	for _, region := range profileScanRegions {
		host := "q." + region + ".amazonaws.com"
		url := "https://" + host + "/"
		var nextToken string
		pages := map[string]struct{}{}

		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}

			payload := map[string]any{"maxResults": 10}
			if nextToken != "" {
				payload["nextToken"] = nextToken
			}

			headers := listAvailableProfilesHeaders(host, machineID, cfg, c, token)

			status, body, err := doJSON(ctx, client, url, headers, payload)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				// Transport errors may contain proxy URLs or headers; do not
				// expose them in a credential-bearing discovery path.
				scanErrors = append(scanErrors, fmt.Errorf("kiro: ListAvailableProfiles %s: request failed", region))
				break
			}
			if status < 200 || status >= 300 {
				// Never include response bodies: upstream may echo credentials.
				scanErrors = append(scanErrors, fmt.Errorf("kiro: ListAvailableProfiles %s: HTTP %d", region, status))
				break
			}

			var data listProfilesResponse
			if err := json.Unmarshal(body, &data); err != nil {
				scanErrors = append(scanErrors, fmt.Errorf("kiro: ListAvailableProfiles %s: invalid response: %w", region, err))
				break
			}
			for _, p := range data.allProfiles() {
				arn := p.arn()
				if arn == BuilderIDProfileArn || regionFromProfileArn(arn) == "" {
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
			if _, duplicate := pages[nextToken]; duplicate {
				scanErrors = append(scanErrors, fmt.Errorf("kiro: ListAvailableProfiles %s: repeated nextToken", region))
				break
			}
			pages[nextToken] = struct{}{}
		}
	}

	// A partial list cannot establish uniqueness: another region or page may
	// contain a different profile. Never persist a profile from incomplete scans.
	if len(scanErrors) > 0 {
		return nil, errors.Join(scanErrors...)
	}
	return result, nil
}

// listAvailableProfilesHeaders reuses the Q runtime fingerprint from
// setRESTHeaders, then adds the JSON-RPC target headers. Zyphr does not send
// Accept or x-amzn-codewhisperer-optout on this call.
func listAvailableProfilesHeaders(host, machineID string, cfg *Config, c *Credentials, token string) map[string]string {
	h := make(http.Header)
	setRESTHeaders(h, host, machineID, cfg, c, token)
	h.Set("Content-Type", "application/x-amz-json-1.0")
	h.Set("X-Amz-Target", listAvailableProfilesTarget)
	headers := make(map[string]string, len(h))
	for name, values := range h {
		if len(values) > 0 {
			headers[name] = values[0]
		}
	}
	return headers
}
