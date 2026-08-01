package common

import (
	"testing"
)

func TestGenerateVerificationCode_DefaultLength(t *testing.T) {
	code := GenerateVerificationCode(0)
	if len(code) == 0 {
		t.Fatal("GenerateVerificationCode(0) returned empty")
	}
	// UUID without dashes is 32 chars
	if len(code) != 32 {
		t.Fatalf("GenerateVerificationCode(0) length = %d, want 32", len(code))
	}
}

func TestGenerateVerificationCode_SpecificLength(t *testing.T) {
	code := GenerateVerificationCode(6)
	if len(code) != 6 {
		t.Fatalf("GenerateVerificationCode(6) length = %d, want 6", len(code))
	}
}

func TestGenerateVerificationCode_NoDashes(t *testing.T) {
	code := GenerateVerificationCode(0)
	for _, c := range code {
		if c == '-' {
			t.Fatal("GenerateVerificationCode should not contain dashes")
		}
	}
}

func TestRegisterAndVerifyCode(t *testing.T) {
	key := "user@example.com"
	code := "123456"
	purpose := EmailVerificationPurpose

	RegisterVerificationCodeWithKey(key, code, purpose)
	if !VerifyCodeWithKey(key, code, purpose) {
		t.Fatal("VerifyCodeWithKey should return true for correct code")
	}
}

func TestVerifyCode_WrongCode(t *testing.T) {
	key := "user2@example.com"
	code := "abcdef"
	purpose := EmailVerificationPurpose

	RegisterVerificationCodeWithKey(key, code, purpose)
	if VerifyCodeWithKey(key, "wrong-code", purpose) {
		t.Fatal("VerifyCodeWithKey should return false for wrong code")
	}
}

func TestVerifyCode_NonexistentKey(t *testing.T) {
	if VerifyCodeWithKey("nonexistent@example.com", "any-code", EmailVerificationPurpose) {
		t.Fatal("VerifyCodeWithKey should return false for nonexistent key")
	}
}

func TestDeleteKey(t *testing.T) {
	key := "delete-test@example.com"
	code := "999999"
	purpose := PasswordResetPurpose

	RegisterVerificationCodeWithKey(key, code, purpose)
	if !VerifyCodeWithKey(key, code, purpose) {
		t.Fatal("code should be valid before deletion")
	}

	DeleteKey(key, purpose)
	if VerifyCodeWithKey(key, code, purpose) {
		t.Fatal("code should be invalid after deletion")
	}
}

func TestVerificationCode_DifferentPurposes(t *testing.T) {
	key := "same-key"
	emailCode := "email-code"
	resetCode := "reset-code"

	RegisterVerificationCodeWithKey(key, emailCode, EmailVerificationPurpose)
	RegisterVerificationCodeWithKey(key, resetCode, PasswordResetPurpose)

	// Each purpose should have its own code
	if !VerifyCodeWithKey(key, emailCode, EmailVerificationPurpose) {
		t.Fatal("email code should be valid for email purpose")
	}
	if !VerifyCodeWithKey(key, resetCode, PasswordResetPurpose) {
		t.Fatal("reset code should be valid for reset purpose")
	}
	// Cross-purpose should fail
	if VerifyCodeWithKey(key, emailCode, PasswordResetPurpose) {
		t.Fatal("email code should not be valid for reset purpose")
	}
}
