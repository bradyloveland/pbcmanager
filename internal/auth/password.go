// Package auth holds sign-in primitives: password hashing, TOTP two-step
// codes, recovery codes and brute-force throttling.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// DefaultIterations matches 1.x, so password hashes carry over unchanged.
const DefaultIterations = 310_000

const maxIterations = 10_000_000

// HashPassword returns "pbkdf2_sha256$<iterations>$<salt>$<hash>", the same
// format 1.x used.
func HashPassword(password string, iterations int) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", iterations,
		base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(dk)), nil
}

// VerifyPassword reports whether password matches a stored hash. Malformed
// hashes never match.
func VerifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 || iterations > maxIterations {
		return false
	}
	salt, err1 := base64.StdEncoding.DecodeString(parts[2])
	expected, err2 := base64.StdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(expected) == 0 {
		return false
	}
	dk, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(dk, expected) == 1
}
