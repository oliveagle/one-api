package model

import (
	"encoding/json"
)

// Tool mirrors the OpenAI ChatCompletions tool schema:
//
//	{ "type": "function", "function": { "name": ..., "parameters": ... } }
//
// codex-cli's Responses API emitter, however, sends the same data in a flat
// shape (which is what the OpenAI Responses API documents):
//
//	{ "type": "function", "name": ..., "parameters": ... }
//
// Both shapes are accepted on the wire. UnmarshalJSON promotes the flat
// top-level `name`/`description`/`parameters` fields into Function so the
// rest of the relay pipeline (and every upstream channel) only ever sees the
// nested shape. Without this promotion, the OpenAI upstream rejects the
// request with `400 missing tools.name` because Type is set but
// Function.Name is empty.
type Tool struct {
	Id       string   `json:"id,omitempty"`
	Type     string   `json:"type,omitempty"` // when splicing claude tools stream messages, it is empty
	Function Function `json:"function"`
}

// toolFlatAlias is the wire shape used by the Responses API. Only the fields
// relevant for promotion are declared; anything else (mcp_tool, server-side
// definitions, etc.) round-trips through the nested Function so the upstream
// still gets a complete view.
//
// Parameters is declared as json.RawMessage so we don't lose fidelity when
// the value is a non-string JSON object (e.g. `{"type":"object",...}`); we
// then hand it off to Function as-is.
type toolFlatAlias struct {
	Id          string          `json:"id,omitempty"`
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Function    *Function       `json:"function,omitempty"`
}

type Function struct {
	Description string `json:"description,omitempty"`
	Name        string `json:"name,omitempty"`       // when splicing claude tools stream messages, it is empty
	Parameters  any    `json:"parameters,omitempty"` // request
	Arguments   any    `json:"arguments,omitempty"`  // response
}

// UnmarshalJSON accepts both the nested ChatCompletions shape and the flat
// Responses API shape. The flat fields are promoted into Function when
// Function is absent so the rest of the pipeline only deals with the nested
// representation.
func (t *Tool) UnmarshalJSON(data []byte) error {
	var alias toolFlatAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	t.Id = alias.Id
	t.Type = alias.Type

	if alias.Function != nil {
		// Nested shape — author wins.
		t.Function = *alias.Function
		return nil
	}
	if alias.Name != "" || alias.Description != "" || len(alias.Parameters) > 0 {
		// Flat shape — promote top-level fields into Function. Keep
		// Parameters as json.RawMessage so the nested round-trip is
		// byte-identical (no map[string]interface{} re-encoding).
		t.Function = Function{
			Name:        alias.Name,
			Description: alias.Description,
			Parameters:  alias.Parameters,
		}
	}
	// Empty function block (e.g. mid-stream Claude splice). Leave at zero
	// value; the marshal path will emit `"function":{}`.
	return nil
}
