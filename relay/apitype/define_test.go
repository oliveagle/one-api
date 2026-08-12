package apitype

import "testing"

// Dummy is the upper bound for the apitype enum. The dispatcher relies on
// `for i := 0; i < apitype.Dummy; i++` ranges; if Dummy drifts away from the
// real number of types, off-by-one channel types like apitype[0] would route
// to a wrong adaptor.
func TestApitypeDistinct(t *testing.T) {
	seen := map[int]string{}
	for _, name := range []struct {
		id   int
		name string
	}{
		{OpenAI, "OpenAI"},
		{Anthropic, "Anthropic"},
		{PaLM, "PaLM"},
		{Baidu, "Baidu"},
		{Zhipu, "Zhipu"},
		{Ali, "Ali"},
		{Xunfei, "Xunfei"},
		{AIProxyLibrary, "AIProxyLibrary"},
		{Tencent, "Tencent"},
		{Gemini, "Gemini"},
		{Ollama, "Ollama"},
		{AwsClaude, "AwsClaude"},
		{Coze, "Coze"},
		{Cohere, "Cohere"},
		{Cloudflare, "Cloudflare"},
		{DeepL, "DeepL"},
		{VertexAI, "VertexAI"},
		{Proxy, "Proxy"},
		{Replicate, "Replicate"},
		{Mock, "Mock"},
	} {
		if prev, ok := seen[name.id]; ok {
			t.Errorf("id collision: %s and %s share id %d", prev, name.name, name.id)
		}
		seen[name.id] = name.name
	}
}

// apitype.Dummy is the exclusive upper bound; i < Dummy must cover every
// usable id.
func TestDummyUpperBound(t *testing.T) {
	if Dummy <= OpenAI {
		t.Fatalf("Dummy (%d) must exceed OpenAI (%d)", Dummy, OpenAI)
	}
	if Dummy <= Replicate {
		t.Fatalf("Dummy (%d) must exceed Replicate (%d)", Dummy, Replicate)
	}
	if Dummy <= Mock {
		t.Fatalf("Dummy (%d) must exceed Mock (%d)", Dummy, Mock)
	}
}
