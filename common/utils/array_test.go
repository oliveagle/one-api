package utils

import (
	"sort"
	"testing"
)

func TestDeDuplication(t *testing.T) {
	cases := []struct {
		name  string
		input []string
		want  []string
	}{
		{"empty", nil, []string{}},
		{"no duplicates", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"all duplicates", []string{"a", "a", "a"}, []string{"a"}},
		{"mixed", []string{"a", "b", "a", "c", "b"}, []string{"a", "b", "c"}},
		{"single element", []string{"x"}, []string{"x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DeDuplication(tc.input)
			sort.Strings(got)
			sort.Strings(tc.want)
			if len(got) != len(tc.want) {
				t.Fatalf("DeDuplication(%v) = %v, want %v", tc.input, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("DeDuplication(%v) = %v, want %v", tc.input, got, tc.want)
				}
			}
		})
	}
}

func TestDeDuplicationPreservesNoOrder(t *testing.T) {
	// The function uses a map, so order is not preserved.
	// Just verify all elements are present.
	input := []string{"z", "y", "x", "y", "z"}
	got := DeDuplication(input)
	if len(got) != 3 {
		t.Fatalf("expected 3 unique elements, got %d: %v", len(got), got)
	}
	m := make(map[string]bool)
	for _, s := range got {
		m[s] = true
	}
	for _, want := range []string{"x", "y", "z"} {
		if !m[want] {
			t.Errorf("missing element %q in result", want)
		}
	}
}
