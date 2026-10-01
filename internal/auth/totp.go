package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// TOTP (RFC 6238): SHA-1, 6 digits, 30-second steps.
const TOTPPeriod = 30

var (
	b32       = base32.StdEncoding.WithPadding(base32.NoPadding)
	spaceRE   = regexp.MustCompile(`\s+`)
	sixDigits = regexp.MustCompile(`^\d{6}$`)
)

// NewTOTPSecret returns a random 160-bit secret in unpadded base32.
func NewTOTPSecret() string {
	key := make([]byte, 20)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return b32.EncodeToString(key)
}

// TOTPCode computes the code for a time step.
func TOTPCode(secret string, step int64, digits int) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil {
		return "", fmt.Errorf("invalid TOTP secret: %w", err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, value%mod), nil
}

// TOTPMatch returns the step a code matches within one step of now. Steps at
// or before lastStep are rejected so a code can't be replayed.
func TOTPMatch(secret, code string, lastStep int64, now time.Time) (int64, bool) {
	code = spaceRE.ReplaceAllString(code, "")
	if secret == "" || !sixDigits.MatchString(code) {
		return 0, false
	}
	current := now.Unix() / TOTPPeriod
	for step := current - 1; step <= current+1; step++ {
		if step <= lastStep {
			continue
		}
		want, err := TOTPCode(secret, step, 6)
		if err != nil {
			return 0, false
		}
		if hmac.Equal([]byte(want), []byte(code)) {
			return step, true
		}
	}
	return 0, false
}

// TOTPURI builds the otpauth:// URI authenticator apps scan.
func TOTPURI(secret, issuer, account string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := "secret=" + secret + "&issuer=" + url.PathEscape(issuer) +
		"&algorithm=SHA1&digits=6&period=30"
	return "otpauth://totp/" + label + "?" + q
}

// GroupSecret formats a secret in groups of four for typing by hand.
func GroupSecret(secret string) string {
	var parts []string
	for i := 0; i < len(secret); i += 4 {
		end := min(i+4, len(secret))
		parts = append(parts, secret[i:end])
	}
	return strings.Join(parts, " ")
}

const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

var notAlnum = regexp.MustCompile(`[^a-z0-9]`)

// NewRecoveryCodes returns n single-use codes like "abcde-fghjk", avoiding
// characters that are easy to misread (0, 1, i, l, o).
func NewRecoveryCodes(n int) []string {
	codes := make([]string, 0, n)
	seen := map[string]bool{}
	for len(codes) < n {
		raw := make([]byte, 10)
		for i := range raw {
			raw[i] = recoveryAlphabet[randIndex(len(recoveryAlphabet))]
		}
		code := string(raw[:5]) + "-" + string(raw[5:])
		if !seen[code] {
			seen[code] = true
			codes = append(codes, code)
		}
	}
	return codes
}

// NormalizeRecovery lowercases a recovery code and drops separators.
func NormalizeRecovery(code string) string {
	return notAlnum.ReplaceAllString(strings.ToLower(code), "")
}

// HashRecovery returns the SHA-256 hex digest stored for a recovery code.
func HashRecovery(code string) string {
	sum := sha256.Sum256([]byte(NormalizeRecovery(code)))
	return hex.EncodeToString(sum[:])
}

func randIndex(n int) int {
	// Rejection sampling avoids modulo bias.
	limit := 256 - 256%n
	var b [1]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			panic(err)
		}
		if int(b[0]) < limit {
			return int(b[0]) % n
		}
	}
}
