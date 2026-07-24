package kiro

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// fallbackMachineIDs caches per-account fallback machine ids for process
// lifetime stability (mirrors kiro-rs FALLBACK_MACHINE_IDS).
var (
	fallbackMu  sync.Mutex
	fallbackIDs = map[int64]string{}
)

// sha256Hex returns the lowercase hex sha256 of input.
func sha256Hex(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// normalizeMachineID validates/normalizes a machine id string. It mirrors
// kiro-rs normalize_machine_id, which validates case-insensitively (accepting
// A-F) but PRESERVES the original case in its output (it only trims whitespace,
// never lowercases). Lowercasing here would change the device fingerprint sent
// upstream for a user-provided uppercase-hex machineId.
//   - 64-char hex => returned as-is (case preserved)
//   - UUID (32 hex after removing dashes) => duplicated to 64 chars (case preserved)
//   - otherwise => ("", false)
func normalizeMachineID(machineID string) (string, bool) {
	trimmed := strings.TrimSpace(machineID)

	if len(trimmed) == 64 && isHex(trimmed) {
		return trimmed, true
	}

	withoutDashes := strings.ReplaceAll(trimmed, "-", "")
	if len(withoutDashes) == 32 && isHex(withoutDashes) {
		return withoutDashes + withoutDashes, true
	}
	return "", false
}

// isHex reports whether s is a non-empty ASCII hex string (case-insensitive,
// matching Rust's is_ascii_hexdigit).
func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

// GenerateMachineID derives the machine id for a credential.
//
// Priority (mirrors kiro-rs machine_id::generate_from_credentials):
//  1. credential-level machine_id (if valid format)
//  2. global default machine_id (if valid format)
//  3. derived by credential type (mutually exclusive):
//     - api_key credential: sha256("KiroAPIKey/" + kiro_api_key)
//     - OAuth credential:   sha256("KotlinNativeAPI/" + refresh_token)
//  4. fallback: sha256("KiroFallback/" + random uuid), stable per account id
func GenerateMachineID(c *Credentials, defaultMachineID string) string {
	if c.MachineID != "" {
		if n, ok := normalizeMachineID(c.MachineID); ok {
			return n
		}
	}
	if defaultMachineID != "" {
		if n, ok := normalizeMachineID(defaultMachineID); ok {
			return n
		}
	}

	if c.IsAPIKey() {
		if c.KiroAPIKey != "" {
			return sha256Hex("KiroAPIKey/" + c.KiroAPIKey)
		}
	} else if c.RefreshToken != "" {
		return sha256Hex("KotlinNativeAPI/" + c.RefreshToken)
	}

	return fallbackMachineID(c.ID)
}

// fallbackMachineID returns a random-but-stable (per account id) machine id.
func fallbackMachineID(accountID int64) string {
	fallbackMu.Lock()
	defer fallbackMu.Unlock()
	if existing, ok := fallbackIDs[accountID]; ok {
		return existing
	}
	derived := sha256Hex("KiroFallback/" + uuid.NewString())
	fallbackIDs[accountID] = derived
	return derived
}
