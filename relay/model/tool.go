package model

import "encoding/json"

type Tool struct {
	Id       string   `json:"id,omitempty"`
	Type     string   `json:"type,omitempty"` // when splicing claude tools stream messages, it is empty
	Function Function `json:"function"`
}

type Function struct {
	Description string `json:"description,omitempty"`
	Name        string `json:"name,omitempty"`       // when splicing claude tools stream messages, it is empty
	Parameters  any    `json:"parameters,omitempty"` // request
	Arguments   any    `json:"arguments,omitempty"`  // response
}

// UnmarshalJSON accepts BOTH tool declaration shapes:
//
//	chat (nested):    {"type":"function","function":{"name":"shell","parameters":{...}}}
//	responses (flat): {"type":"function","name":"shell","parameters":{...}}
//
// The Responses API declares function tools FLAT (name/description/
// parameters at the top level, no function object). Decoding a flat
// tool into the nested-only shape yields an empty Function.Name, which
// downstream sanitizers then treat as invalid and silently drop — every
// codex tool disappeared that way and the model fell back to printing
// textual tool calls. Flat fields are lifted into Function on decode;
// marshaling always emits the nested chat shape.
func (t *Tool) UnmarshalJSON(data []byte) error {
	type toolAlias Tool
	var nested toolAlias
	if err := json.Unmarshal(data, &nested); err != nil {
		return err
	}
	*t = Tool(nested)
	if t.Function.Name != "" {
		return nil
	}
	// Flat Responses shape: lift name/description/parameters.
	var flat struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters"`
	}
	if err := json.Unmarshal(data, &flat); err != nil {
		return err
	}
	if flat.Name != "" {
		t.Function.Name = flat.Name
		t.Function.Description = flat.Description
		t.Function.Parameters = flat.Parameters
	}
	return nil
}
