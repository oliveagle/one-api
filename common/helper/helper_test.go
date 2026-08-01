package helper

import (
	"context"
	"html/template"
	"math"
	"testing"
)

func TestBytes2Size(t *testing.T) {
	cases := []struct {
		name string
		bytes int64
		want string
	}{
		{"zero", 0, "0 B"},
		{"bytes", 500, "500 B"},
		{"KB", 2048, "2 KB"},
		{"exact MB becomes KB", 1048576, "1024 KB"},
		{"exact GB becomes MB", 1073741824, "1024 MB"},
		{"GB", 2147483648, "2.00 GB"},
		{"edge KB", 1023, "1023 B"},
		{"edge MB", 1048575, "1023 KB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Bytes2Size(tc.bytes); got != tc.want {
				t.Errorf("Bytes2Size(%d) = %q, want %q", tc.bytes, got, tc.want)
			}
		})
	}
}

func TestInterface2String(t *testing.T) {
	cases := []struct {
		name string
		v    interface{}
		want string
	}{
		{"string", "hello", "hello"},
		{"int", 42, "42"},
		{"float64", 3.14, "3.140000"},
		{"bool", true, "Not Implemented"},
		{"nil", nil, "Not Implemented"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Interface2String(tc.v); got != tc.want {
				t.Errorf("Interface2String(%v) = %q, want %q", tc.v, got, tc.want)
			}
		})
	}
}

func TestIntMax(t *testing.T) {
	if got := IntMax(3, 7); got != 7 {
		t.Errorf("IntMax(3,7) = %d, want 7", got)
	}
	if got := IntMax(7, 3); got != 7 {
		t.Errorf("IntMax(7,3) = %d, want 7", got)
	}
	if got := IntMax(5, 5); got != 5 {
		t.Errorf("IntMax(5,5) = %d, want 5", got)
	}
}

func TestMax(t *testing.T) {
	if got := Max(3, 7); got != 7 {
		t.Errorf("Max(3,7) = %d, want 7", got)
	}
	if got := Max(7, 3); got != 7 {
		t.Errorf("Max(7,3) = %d, want 7", got)
	}
}

func TestAssignOrDefault(t *testing.T) {
	if got := AssignOrDefault("hello", "default"); got != "hello" {
		t.Errorf("AssignOrDefault(hello, default) = %q", got)
	}
	if got := AssignOrDefault("", "default"); got != "default" {
		t.Errorf("AssignOrDefault(empty, default) = %q", got)
	}
}

func TestMessageWithRequestId(t *testing.T) {
	got := MessageWithRequestId("error occurred", "req-123")
	want := "error occurred (request id: req-123)"
	if got != want {
		t.Errorf("MessageWithRequestId = %q, want %q", got, want)
	}
}

func TestString2Int(t *testing.T) {
	cases := []struct {
		input string
		want  int
	}{
		{"42", 42},
		{"0", 0},
		{"-5", -5},
		{"not-a-number", 0},
		{"", 0},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			if got := String2Int(tc.input); got != tc.want {
				t.Errorf("String2Int(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestFloat64PtrMax(t *testing.T) {
	// nil case
	if got := Float64PtrMax(nil, 10); got != nil {
		t.Error("Float64PtrMax(nil, 10) should return nil")
	}
	// under max
	v1 := 5.0
	if got := Float64PtrMax(&v1, 10); *got != 5.0 {
		t.Errorf("Float64PtrMax(5, 10) = %f, want 5.0", *got)
	}
	// over max
	v2 := 15.0
	if got := Float64PtrMax(&v2, 10); *got != 10.0 {
		t.Errorf("Float64PtrMax(15, 10) = %f, want 10.0", *got)
	}
	// equal to max
	v3 := 10.0
	if got := Float64PtrMax(&v3, 10); *got != 10.0 {
		t.Errorf("Float64PtrMax(10, 10) = %f, want 10.0", *got)
	}
}

func TestFloat64PtrMin(t *testing.T) {
	// nil case
	if got := Float64PtrMin(nil, 10); got != nil {
		t.Error("Float64PtrMin(nil, 10) should return nil")
	}
	// above min: returns original (no clamping needed)
	v1 := 15.0
	if got := Float64PtrMin(&v1, 10); *got != 15.0 {
		t.Errorf("Float64PtrMin(15, 10) = %f, want 15.0", *got)
	}
	// below min: clamps up to min
	v2 := 5.0
	if got := Float64PtrMin(&v2, 10); *got != 10.0 {
		t.Errorf("Float64PtrMin(5, 10) = %f, want 10.0", *got)
	}
	// equal to min
	v3 := 10.0
	if got := Float64PtrMin(&v3, 10); *got != 10.0 {
		t.Errorf("Float64PtrMin(10, 10) = %f, want 10.0", *got)
	}
}

func TestUnescapeHTML(t *testing.T) {
	got := UnescapeHTML("<b>bold</b>")
	want := template.HTML("<b>bold</b>")
	if got != want {
		t.Errorf("UnescapeHTML = %v, want %v", got, want)
	}
}

func TestGenRequestID(t *testing.T) {
	id := GenRequestID()
	if len(id) == 0 {
		t.Fatal("GenRequestID returned empty string")
	}
	// Should contain only digits (timestamp + random)
	for _, c := range id {
		if c < '0' || c > '9' {
			t.Fatalf("GenRequestID contains non-digit char %c", c)
		}
	}
}

func TestSetAndGetRequestID(t *testing.T) {
	ctx := context.Background()
	ctx = SetRequestID(ctx, "req-abc")
	if got := GetRequestID(ctx); got != "req-abc" {
		t.Errorf("GetRequestID = %q, want req-abc", got)
	}
}

func TestGetRequestID_Empty(t *testing.T) {
	ctx := context.Background()
	if got := GetRequestID(ctx); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestGetTimestamp(t *testing.T) {
	ts := GetTimestamp()
	if ts <= 0 {
		t.Errorf("GetTimestamp = %d, want > 0", ts)
	}
}

func TestGetTimeString(t *testing.T) {
	s := GetTimeString()
	if len(s) == 0 {
		t.Fatal("GetTimeString returned empty")
	}
}

func TestIntMaxConsistency(t *testing.T) {
	// IntMax and Max should behave identically
	cases := []struct{ a, b int }{
		{1, 2}, {2, 1}, {0, 0}, {-1, 1}, {math.MaxInt32, math.MinInt32},
	}
	for _, tc := range cases {
		if got1, got2 := IntMax(tc.a, tc.b), Max(tc.a, tc.b); got1 != got2 {
			t.Errorf("IntMax(%d,%d)=%d != Max(%d,%d)=%d", tc.a, tc.b, got1, tc.a, tc.b, got2)
		}
	}
}
