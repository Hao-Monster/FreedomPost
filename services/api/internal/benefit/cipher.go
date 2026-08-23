package benefit

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// cipher.go — AES-256-GCM subscription link encryption.
// Ported from TS subscription-cipher.ts / claim-cookie.ts.
//
// Envelope format (base64url, no padding):
//   <nonce_12B_b64> . <ciphertext+tag_b64>
//
// Additional data (AAD) = campaignId + ":" + claimId — binds the ciphertext
// to a specific campaign/claim combination to prevent cross-use.

const (
	aesKeyLen    = 32 // AES-256
	gcmNonceLen  = 12
	maxPlaintext = 4096
)

// SubscriptionCipher encrypts and decrypts subscription URLs.
type SubscriptionCipher struct {
	key []byte // 32 bytes
}

// SubscriptionCipherAAD contains additional authenticated data.
type SubscriptionCipherAAD struct {
	CampaignID string
	ClaimID    string
}

// NewSubscriptionCipher creates a cipher from a 32-byte key.
// The key is hex or raw — if it is 64 hex chars, it is decoded.
// If it is 32 raw bytes (from env), it is used directly.
func NewSubscriptionCipher(keyStr string) (*SubscriptionCipher, error) {
	if keyStr == "" {
		return nil, errors.New("BENEFIT_LINK_ENCRYPTION_KEY is required")
	}
	raw := []byte(keyStr)
	if len(raw) != aesKeyLen {
		return nil, fmt.Errorf("BENEFIT_LINK_ENCRYPTION_KEY must be exactly %d bytes, got %d", aesKeyLen, len(raw))
	}
	key := make([]byte, aesKeyLen)
	copy(key, raw)
	return &SubscriptionCipher{key: key}, nil
}

// Encrypt encrypts a plaintext subscription URL and returns the ciphertext envelope.
func (c *SubscriptionCipher) Encrypt(plaintext string, aad SubscriptionCipherAAD) (string, error) {
	if len(plaintext) > maxPlaintext {
		return "", errors.New("subscription URL is too long")
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", fmt.Errorf("cipher init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("GCM init: %w", err)
	}
	nonce := make([]byte, gcmNonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce generation: %w", err)
	}
	aadBytes := buildAAD(aad)
	ct := gcm.Seal(nil, nonce, []byte(plaintext), aadBytes)

	nonceB64 := base64.RawURLEncoding.EncodeToString(nonce)
	ctB64 := base64.RawURLEncoding.EncodeToString(ct)
	return nonceB64 + "." + ctB64, nil
}

// Decrypt decrypts a ciphertext envelope and returns the plaintext subscription URL.
func (c *SubscriptionCipher) Decrypt(envelope string, aad SubscriptionCipherAAD) (string, error) {
	parts := strings.SplitN(envelope, ".", 2)
	if len(parts) != 2 {
		return "", errors.New("invalid subscription cipher envelope")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(nonce) != gcmNonceLen {
		return "", errors.New("invalid subscription cipher nonce")
	}
	ct, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("invalid subscription cipher ciphertext")
	}

	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", fmt.Errorf("cipher init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("GCM init: %w", err)
	}
	aadBytes := buildAAD(aad)
	plain, err := gcm.Open(nil, nonce, ct, aadBytes)
	if err != nil {
		return "", errors.New("subscription cipher: authentication failed")
	}
	return string(plain), nil
}

func buildAAD(aad SubscriptionCipherAAD) []byte {
	return []byte(aad.CampaignID + ":" + aad.ClaimID)
}

// ─── Claim credential (browser key) ─────────────────────────────────────────
// BrowserCredential is a short-lived, HMAC-signed cookie token that
// identifies a browser session for duplicate claim detection.
// Format (JSON, base64url-encoded):
//   { "k": "<random 128-bit hex>", "t": <unix_ms>, "h": "<HMAC-SHA256 hex>" }

const (
	claimCookieName  = "fp_benefit_claim"
	claimCookieTTL   = 30 * 24 * 60 * 60 // 30 days in seconds
	browserKeyLen    = 16                // bytes → 32 hex chars
	claimTokenMaxAge = 31 * 24 * 60 * 60 // 31 days
)

// ClaimCredentialService issues and verifies browser claim credentials.
type ClaimCredentialService struct {
	hmacKey []byte
}

// ClaimCredential holds the parsed credential.
type ClaimCredential struct {
	BrowserKeyHash string // SHA-256 of the random browser key
}

// NewClaimCredentialService creates a service from the HMAC secret.
func NewClaimCredentialService(hmacSecret string) (*ClaimCredentialService, error) {
	if len(hmacSecret) < 20 {
		return nil, errors.New("BENEFIT_CLAIM_HMAC_SECRET must be at least 20 characters")
	}
	return &ClaimCredentialService{hmacKey: []byte(hmacSecret)}, nil
}

// Issue creates a new claim credential token (browser key + signature).
func (s *ClaimCredentialService) Issue() (cookieValue string, browserKeyHash string, err error) {
	rawKey := make([]byte, browserKeyLen)
	if _, err = io.ReadFull(rand.Reader, rawKey); err != nil {
		return "", "", fmt.Errorf("generate browser key: %w", err)
	}
	browserKeyHash = hashHex(rawKey)

	type payload struct {
		K string `json:"k"` // browser key hash
		S string `json:"s"` // signature
	}
	sig := hmacHex(s.hmacKey, []byte(browserKeyHash))
	p := payload{K: browserKeyHash, S: sig}
	b, err := json.Marshal(p)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), browserKeyHash, nil
}

// Verify validates a cookie value and returns the browser key hash.
func (s *ClaimCredentialService) Verify(cookieValue string) (string, bool) {
	if cookieValue == "" {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookieValue)
	if err != nil {
		return "", false
	}
	var p struct {
		K string `json:"k"`
		S string `json:"s"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.K == "" || p.S == "" {
		return "", false
	}
	expected := hmacHex(s.hmacKey, []byte(p.K))
	if !hmacEqual(expected, p.S) {
		return "", false
	}
	return p.K, true
}

// HashNetworkKey hashes the caller IP for rate-limiting (no PII stored).
func (s *ClaimCredentialService) HashNetworkKey(ip string) string {
	return hashHex([]byte(ip))
}

// CookieName returns the claim cookie name.
func (s *ClaimCredentialService) CookieName() string { return claimCookieName }

// CookieTTL returns the claim cookie TTL in seconds.
func (s *ClaimCredentialService) CookieTTL() int { return claimCookieTTL }
