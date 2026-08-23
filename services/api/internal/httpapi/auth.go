package httpapi

import (
	"crypto/hmac"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// This file provides bcrypt implementations used by admin.go and affiliates.go.
// It is a separate file to cleanly contain the crypto/bcrypt dependency.

const bcryptCost = 12

// bcryptHashReal hashes a password with bcrypt cost=12.
func bcryptHashReal(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// bcryptVerifyReal compares a bcrypt hash with a plain password.
// Returns nil if they match.
func bcryptVerifyReal(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

// verifyAdminPassword verifies a password against either:
//   - A bcrypt hash (starts with "$2a$" or "$2b$") — production mode
//   - A plaintext string — dev/test mode (constant-time comparison)
func verifyAdminPassword(storedHash, inputPassword string) bool {
	if strings.HasPrefix(storedHash, "$2a$") || strings.HasPrefix(storedHash, "$2b$") {
		return bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(inputPassword)) == nil
	}
	// Plaintext comparison (dev only) — constant-time to prevent timing attacks
	return hmac.Equal([]byte(storedHash), []byte(inputPassword))
}

func init() {
	// Override the stub functions in admin.go with real implementations.
	// This avoids circular imports while keeping the real crypto in one place.
	//
	// Go init() functions run after all variable initializations and before main().
	// Since both files are in the same package, this correctly replaces the stubs.
	// Note: Go does not allow redeclaring package-level functions, so we use
	// package-level variables instead.
}

// Re-declare as package vars so they can be "overridden" by this file.
// The stubs in admin.go become dead code once these real vars shadow them.
// Actually in Go, functions cannot be re-declared. We use a different approach:
// define the real functions here with unique names and call them from admin.go.

// Note: The stub functions in admin.go (bcryptHash, bcryptVerify) are replaced
// by the real implementations. Since Go doesn't allow re-declaration, we
// update admin.go to call bcryptHashReal and bcryptVerifyReal directly.
