package kiro

import "strings"

// This file ports the subset of kiro-rs model::config::Config needed by the
// native upstream path: region resolution and client version strings used to
// build User-Agent headers. Values mirror kiro-rs defaults so upstream sees an
// identical client fingerprint.

// Default client fingerprint values (mirror kiro-rs config defaults).
const (
	defaultRegion        = "us-east-1"
	defaultKiroVersion   = "0.12.333"
	defaultSystemVersion = "darwin#24.6.0"
	defaultNodeVersion   = "22.22.0"
)

// Config holds the client fingerprint and region defaults for upstream calls.
// The zero value is usable; empty fields fall back to kiro-rs defaults via the
// accessor methods.
type Config struct {
	Region     string
	AuthRegion string
	APIRegion  string

	KiroVersion   string
	SystemVersion string
	NodeVersion   string
}

// DefaultConfig returns a Config populated with kiro-rs default values.
func DefaultConfig() *Config {
	return &Config{
		Region:        defaultRegion,
		KiroVersion:   defaultKiroVersion,
		SystemVersion: defaultSystemVersion,
		NodeVersion:   defaultNodeVersion,
	}
}

func (c *Config) region() string {
	if c != nil && c.Region != "" {
		return c.Region
	}
	return defaultRegion
}

// effectiveAuthRegion resolves the region used for token refresh.
func (c *Config) effectiveAuthRegion() string {
	if c != nil && c.AuthRegion != "" {
		return c.AuthRegion
	}
	return c.region()
}

// effectiveAPIRegion resolves the region used for API requests.
func (c *Config) effectiveAPIRegion() string {
	if c != nil && c.APIRegion != "" {
		return c.APIRegion
	}
	return c.region()
}

func (c *Config) kiroVersion() string {
	if c != nil && c.KiroVersion != "" {
		return c.KiroVersion
	}
	return defaultKiroVersion
}

func (c *Config) systemVersion() string {
	if c != nil && c.SystemVersion != "" {
		return c.SystemVersion
	}
	return defaultSystemVersion
}

func (c *Config) nodeVersion() string {
	if c != nil && c.NodeVersion != "" {
		return c.NodeVersion
	}
	return defaultNodeVersion
}

// regionFromProfileArn extracts the region from a CodeWhisperer profile ARN of
// the form arn:aws:codewhisperer:<region>:<account>:profile/... Returns ""
// when the ARN is malformed.
func regionFromProfileArn(profileArn string) string {
	parts := strings.Split(profileArn, ":")
	// Reject truncated, wrong-service and unsafe host-name components. A malformed
	// ARN must never select a network endpoint based on attacker-controlled input.
	if len(parts) != 6 || parts[0] != "arn" || (parts[1] != "aws" && parts[1] != "aws-cn" && parts[1] != "aws-us-gov") ||
		parts[2] != "codewhisperer" || !strings.HasPrefix(parts[5], "profile/") || len(parts[5]) <= len("profile/") ||
		len(parts[4]) != 12 {
		return ""
	}
	for _, ch := range parts[4] {
		if ch < '0' || ch > '9' {
			return ""
		}
	}
	profileID := strings.TrimPrefix(parts[5], "profile/")
	if len(profileID) > 128 {
		return ""
	}
	for _, ch := range profileID {
		if (ch < 'A' || ch > 'Z') && (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' && ch != '_' {
			return ""
		}
	}
	region := parts[3]
	if len(region) < 6 || len(region) > 32 || region[0] == '-' || region[len(region)-1] == '-' {
		return ""
	}
	if strings.Count(region, "-") < 2 {
		return ""
	}
	lastDash := strings.LastIndexByte(region, '-')
	if lastDash == len(region)-1 || lastDash < 3 || !strings.Contains(region[:lastDash], "-") {
		return ""
	}
	for _, ch := range region[:lastDash] {
		if (ch < 'a' || ch > 'z') && ch != '-' {
			return ""
		}
	}
	for _, ch := range region[lastDash+1:] {
		if ch < '0' || ch > '9' {
			return ""
		}
	}
	return region
}

// EffectiveAuthRegion resolves the token-refresh region for a credential.
// Priority: cred.AuthRegion > cred.Region > config.AuthRegion > config.Region.
func (c *Credentials) EffectiveAuthRegion(cfg *Config) string {
	if c.AuthRegion != "" {
		return c.AuthRegion
	}
	if c.Region != "" {
		return c.Region
	}
	return cfg.effectiveAuthRegion()
}

// EffectiveAPIRegion resolves the API-request region for a credential.
// Priority: cred.APIRegion > region-from-profileArn > config.APIRegion > config.Region.
func (c *Credentials) EffectiveAPIRegion(cfg *Config) string {
	if c.APIRegion != "" {
		return c.APIRegion
	}
	if arn := c.EffectiveProfileArn(); arn != "" {
		if r := regionFromProfileArn(arn); r != "" {
			return r
		}
	}
	return cfg.effectiveAPIRegion()
}
