// Package security provides cryptographic utilities for the API service.
package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var base32Encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewToken generates a cryptographically random token.
// Returns a base32-encoded string of 32 random bytes (51 characters).
// Matches the token format from the TypeScript implementation.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return strings.ToLower(base32Encoding.EncodeToString(b)), nil
}

// HashToken computes the SHA-256 hex hash of a token for safe storage.
// Uses constant-time comparison; never store raw tokens.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TokenValid performs a constant-time comparison between a raw cookie value
// and a stored SHA-256 hash. Prevents timing attacks.
func TokenValid(rawToken, storedHash string) bool {
	computed := HashToken(rawToken)
	return subtle.ConstantTimeCompare([]byte(computed), []byte(storedHash)) == 1
}

// HashText returns the SHA-256 hex hash of any string value.
// Used for hashing IPs, fingerprints, wechat IDs, etc.
func HashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// HashVisitorKey builds a stable visitor key from IP + salt.
// Matches the TypeScript hashVisitorKey(ip, salt) logic.
func HashVisitorKey(ip, salt string) string {
	sum := sha256.Sum256([]byte(ip + ":" + salt))
	return hex.EncodeToString(sum[:])
}

// HMACSign computes HMAC-SHA256 of canonical message with the given secret.
// Used for: internal paid-access requests, benefit claim links.
func HMACSign(secret, message string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// HMACEqual performs a constant-time comparison of two HMAC values.
func HMACEqual(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

// SignedRequestCanonical builds the canonical string for internal service
// request signing. Matches paid-access validInternalRequest() exactly.
//
//	canonical = timestamp + "\n" + nonce + "\n" + METHOD + "\n" + path + "\n" + actor + "\n" + hex(sha256(body))
func SignedRequestCanonical(timestamp, nonce, method, path, actor string, body []byte) string {
	bodyHash := sha256.Sum256(body)
	return timestamp + "\n" + nonce + "\n" + method + "\n" + path + "\n" + actor + "\n" + hex.EncodeToString(bodyHash[:])
}

// SignRequest signs an internal service request.
// Returns timestamp, one-time nonce and signature headers.
func SignRequest(secret, method, path, actor string, body []byte) (string, string, string, error) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce, err := NewToken()
	if err != nil {
		return "", "", "", err
	}
	canonical := SignedRequestCanonical(ts, nonce, method, path, actor, body)
	sig := HMACSign(secret, canonical)
	return ts, nonce, sig, nil
}

// ValidateSignedRequest checks that an incoming signed request is authentic
// and within the acceptable timestamp window (±5 minutes).
func ValidateSignedRequest(secret, method, path, actor, timestamp, nonce, signature string, body []byte) bool {
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	age := time.Now().Unix() - ts
	if age < -300 || age > 300 {
		return false
	}
	canonical := SignedRequestCanonical(timestamp, nonce, method, path, actor, body)
	expected := HMACSign(secret, canonical)
	return HMACEqual(expected, signature)
}

// GenerateSlug creates a random 8-character lowercase alphanumeric slug
// suitable for post/product slugs.
func GenerateSlug() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate slug: %w", err)
	}
	// base32 of 6 bytes = 10 chars; take first 8 and lowercase
	return strings.ToLower(base32Encoding.EncodeToString(b))[:8], nil
}

// RandomUsername generates a deterministic display name from a seed,
// matching the TypeScript randomUsername() function.
func RandomUsername(seed string) string {
	if seed == "" {
		seed = "anonymous"
	}
	h := HashText(seed)
	left, _ := strconv.ParseInt(h[:4], 16, 64)
	right, _ := strconv.ParseInt(h[4:8], 16, 64)
	adjectives := []string{"安静的", "自由的", "清醒的", "温和的", "明亮的", "专注的", "透明的", "从容的"}
	nouns := []string{"河流", "山影", "晨光", "星火", "纸页", "远帆", "云层", "石径"}
	return adjectives[int(left)%len(adjectives)] + nouns[int(right)%len(nouns)]
}

// PadPath formats an integer as a zero-padded 4-character string for
// hierarchical comment path ordering (matches TypeScript padPath).
func PadPath(n int) string {
	return fmt.Sprintf("%04d", n)
}
