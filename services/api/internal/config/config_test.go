package config

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestValidateAdminPasswordHash(t *testing.T) {
	hashBytes, err := bcrypt.GenerateFromPassword([]byte("governance-test"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("generate bcrypt hash: %v", err)
	}
	valid := string(hashBytes)
	if err := validateAdminPasswordHash(valid); err != nil {
		t.Fatalf("valid bcrypt hash rejected: %v", err)
	}

	for _, invalid := range []string{
		"'" + valid + "'",
		strings.Replace(valid, "$2a$", "$2y$", 1),
		valid[:len(valid)-1],
	} {
		if err := validateAdminPasswordHash(invalid); err == nil {
			t.Fatalf("invalid bcrypt hash accepted")
		}
	}
}
