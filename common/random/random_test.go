package random

import (
	"testing"
)

func TestGetUUID_NoDashes(t *testing.T) {
	id := GetUUID()
	if len(id) != 32 {
		t.Fatalf("GetUUID length = %d, want 32", len(id))
	}
	for _, c := range id {
		if c == '-' {
			t.Fatal("GetUUID should not contain dashes")
		}
	}
}

func TestGenerateKey_Length(t *testing.T) {
	key := GenerateKey()
	if len(key) != 48 {
		t.Fatalf("GenerateKey length = %d, want 48", len(key))
	}
}

func TestGenerateKey_Format(t *testing.T) {
	key := GenerateKey()
	// First 16 chars should be alphanumeric
	for i := 0; i < 16; i++ {
		c := key[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			t.Fatalf("char %d = %c, not alphanumeric", i, c)
		}
	}
	// Last 32 chars should be hex (uppercase or lowercase)
	for i := 16; i < 48; i++ {
		c := key[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			t.Fatalf("char %d = %c, not hex", i, c)
		}
	}
}

func TestGenerateKey_Unique(t *testing.T) {
	keys := make(map[string]bool)
	for i := 0; i < 100; i++ {
		k := GenerateKey()
		if keys[k] {
			t.Fatal("duplicate key generated")
		}
		keys[k] = true
	}
}

func TestGetRandomString_Length(t *testing.T) {
	for length := 0; length <= 20; length++ {
		s := GetRandomString(length)
		if len(s) != length {
			t.Fatalf("GetRandomString(%d) length = %d, want %d", length, len(s), length)
		}
	}
}

func TestGetRandomString_Alphanumeric(t *testing.T) {
	s := GetRandomString(100)
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			t.Fatalf("non-alphanumeric char %c", c)
		}
	}
}

func TestGetRandomNumberString_Length(t *testing.T) {
	for length := 0; length <= 10; length++ {
		s := GetRandomNumberString(length)
		if len(s) != length {
			t.Fatalf("GetRandomNumberString(%d) length = %d, want %d", length, len(s), length)
		}
	}
}

func TestGetRandomNumberString_DigitsOnly(t *testing.T) {
	s := GetRandomNumberString(100)
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("non-digit char %c", c)
		}
	}
}

func TestRandRange(t *testing.T) {
	for i := 0; i < 100; i++ {
		r := RandRange(5, 10)
		if r < 5 || r >= 10 {
			t.Fatalf("RandRange(5,10) = %d, want [5,10)", r)
		}
	}
}

func TestRandRange_ZeroRange(t *testing.T) {
	r := RandRange(0, 1)
	if r != 0 {
		t.Fatalf("RandRange(0,1) = %d, want 0", r)
	}
}
