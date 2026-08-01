package conv

import (
	"testing"
)

func TestAsString(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"string", "hello", "hello"},
		{"int", 42, ""},
		{"nil", nil, ""},
		{"empty string", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AsString(tc.v); got != tc.want {
				t.Errorf("AsString(%v) = %q, want %q", tc.v, got, tc.want)
			}
		})
	}
}
