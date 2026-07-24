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
	// arn : partition : service : region : account : resource
	if len(parts) < 4 {
		return ""
	}
	return parts[3]
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
	if c.ProfileArn != "" {
		if r := regionFromProfileArn(c.ProfileArn); r != "" {
			return r
		}
	}
	return cfg.effectiveAPIRegion()
}
