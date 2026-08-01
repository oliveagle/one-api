package env

import (
	"os"
	"testing"
)

func TestBool_Default(t *testing.T) {
	// Non-existent env var should return default
	if got := Bool("NONEXISTENT_ENV_VAR_12345", true); got != true {
		t.Errorf("Bool(nonexistent, true) = %v, want true", got)
	}
	if got := Bool("NONEXISTENT_ENV_VAR_12345", false); got != false {
		t.Errorf("Bool(nonexistent, false) = %v, want false", got)
	}
}

func TestBool_EmptyEnvName(t *testing.T) {
	if got := Bool("", true); got != true {
		t.Errorf("Bool(empty, true) = %v, want true", got)
	}
}

func TestBool_EnvSet(t *testing.T) {
	os.Setenv("TEST_BOOL_TRUE", "true")
	defer os.Unsetenv("TEST_BOOL_TRUE")
	if got := Bool("TEST_BOOL_TRUE", false); got != true {
		t.Errorf("Bool(true env, false default) = %v, want true", got)
	}

	os.Setenv("TEST_BOOL_FALSE", "false")
	defer os.Unsetenv("TEST_BOOL_FALSE")
	if got := Bool("TEST_BOOL_FALSE", true); got != false {
		t.Errorf("Bool(false env, true default) = %v, want false", got)
	}
}

func TestInt_Default(t *testing.T) {
	if got := Int("NONEXISTENT_INT", 42); got != 42 {
		t.Errorf("Int(nonexistent, 42) = %d, want 42", got)
	}
}

func TestInt_EmptyEnvName(t *testing.T) {
	if got := Int("", 99); got != 99 {
		t.Errorf("Int(empty, 99) = %d, want 99", got)
	}
}

func TestInt_EnvSet(t *testing.T) {
	os.Setenv("TEST_INT", "123")
	defer os.Unsetenv("TEST_INT")
	if got := Int("TEST_INT", 0); got != 123 {
		t.Errorf("Int(TEST_INT, 0) = %d, want 123", got)
	}
}

func TestInt_InvalidValue(t *testing.T) {
	os.Setenv("TEST_INT_INVALID", "not-a-number")
	defer os.Unsetenv("TEST_INT_INVALID")
	if got := Int("TEST_INT_INVALID", 42); got != 42 {
		t.Errorf("Int(invalid, 42) = %d, want 42", got)
	}
}

func TestFloat64_Default(t *testing.T) {
	if got := Float64("NONEXISTENT_FLOAT", 3.14); got != 3.14 {
		t.Errorf("Float64(nonexistent, 3.14) = %f, want 3.14", got)
	}
}

func TestFloat64_EnvSet(t *testing.T) {
	os.Setenv("TEST_FLOAT", "2.718")
	defer os.Unsetenv("TEST_FLOAT")
	if got := Float64("TEST_FLOAT", 0); got != 2.718 {
		t.Errorf("Float64(TEST_FLOAT, 0) = %f, want 2.718", got)
	}
}

func TestString_Default(t *testing.T) {
	if got := String("NONEXISTENT_STR", "fallback"); got != "fallback" {
		t.Errorf("String(nonexistent, fallback) = %q, want fallback", got)
	}
}

func TestString_EnvSet(t *testing.T) {
	os.Setenv("TEST_STR", "custom-value")
	defer os.Unsetenv("TEST_STR")
	if got := String("TEST_STR", "fallback"); got != "custom-value" {
		t.Errorf("String(TEST_STR, fallback) = %q, want custom-value", got)
	}
}

func TestString_EmptyEnvName(t *testing.T) {
	if got := String("", "default"); got != "default" {
		t.Errorf("String(empty, default) = %q, want default", got)
	}
}
