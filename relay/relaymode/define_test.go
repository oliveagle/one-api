package relaymode

import "testing"

// The numeric values for the relay mode enum are persisted in user stats and
// channel configs. Reordering the const block would silently rewrite history,
// so pin them.
func TestRelayModeEnum(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"Unknown", Unknown, 0},
		{"ChatCompletions", ChatCompletions, 1},
		{"Completions", Completions, 2},
		{"Embeddings", Embeddings, 3},
		{"Moderations", Moderations, 4},
		{"ImagesGenerations", ImagesGenerations, 5},
		{"Edits", Edits, 6},
		{"AudioSpeech", AudioSpeech, 7},
		{"AudioTranscription", AudioTranscription, 8},
		{"AudioTranslation", AudioTranslation, 9},
		{"Proxy", Proxy, 10},
		{"Responses", Responses, 11},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %d, want %d (reordering would break DBs)", tc.name, tc.got, tc.want)
			}
		})
	}
}

// Modes must be distinct so the dispatcher can use the value as a switch
// discriminant.
func TestRelayModeDistinct(t *testing.T) {
	seen := map[int]string{}
	for _, m := range []struct {
		id   int
		name string
	}{
		{Unknown, "Unknown"},
		{ChatCompletions, "ChatCompletions"},
		{Completions, "Completions"},
		{Embeddings, "Embeddings"},
		{Moderations, "Moderations"},
		{ImagesGenerations, "ImagesGenerations"},
		{Edits, "Edits"},
		{AudioSpeech, "AudioSpeech"},
		{AudioTranscription, "AudioTranscription"},
		{AudioTranslation, "AudioTranslation"},
		{Proxy, "Proxy"},
		{Responses, "Responses"},
	} {
		if prev, ok := seen[m.id]; ok {
			t.Errorf("id collision: %s and %s share %d", prev, m.name, m.id)
		}
		seen[m.id] = m.name
	}
}
