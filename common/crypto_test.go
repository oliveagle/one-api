package common

import (
	"testing"
)

func TestPassword2Hash(t *testing.T) {
	password := "my-secret-password-123"
	hash, err := Password2Hash(password)
	if err != nil {
		t.Fatalf("Password2Hash failed: %v", err)
	}
	if hash == "" {
		t.Fatal("Password2Hash returned empty hash")
	}
	if hash == password {
		t.Fatal("Password2Hash returned the plaintext password")
	}
}

func TestValidatePasswordAndHash_Valid(t *testing.T) {
	password := "test-password"
	hash, err := Password2Hash(password)
	if err != nil {
		t.Fatalf("Password2Hash failed: %v", err)
	}
	if !ValidatePasswordAndHash(password, hash) {
		t.Fatal("ValidatePasswordAndHash should return true for correct password")
	}
}

func TestValidatePasswordAndHash_Invalid(t *testing.T) {
	password := "correct-password"
	hash, err := Password2Hash(password)
	if err != nil {
		t.Fatalf("Password2Hash failed: %v", err)
	}
	if ValidatePasswordAndHash("wrong-password", hash) {
		t.Fatal("ValidatePasswordAndHash should return false for wrong password")
	}
}

func TestValidatePasswordAndHash_EmptyPassword(t *testing.T) {
	hash, err := Password2Hash("some-password")
	if err != nil {
		t.Fatalf("Password2Hash failed: %v", err)
	}
	if ValidatePasswordAndHash("", hash) {
		t.Fatal("ValidatePasswordAndHash should return false for empty password")
	}
}
