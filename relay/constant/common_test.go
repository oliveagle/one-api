package constant

import "testing"

// Constants are referenced from the JSON encoder (Object), the openai
// adaptor (StreamObject / NonStreamObject) and the SSE terminator. Drift in
// these strings shows up as upstream-format mismatches that are very
// expensive to debug; pin them in tests.
func TestConstantsStable(t *testing.T) {
	cases := []struct {
		got  string
		want string
	}{
		{StopFinishReason, "stop"},
		{StreamObject, "chat.completion.chunk"},
		{NonStreamObject, "chat.completion"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("constant drifted: got %q, want %q", tc.got, tc.want)
		}
	}
}
