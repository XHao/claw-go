package provider

import (
	"encoding/json"
	"testing"
)

func TestMarshalWithExtra_nil(t *testing.T) {
	type payload struct {
		Model string `json:"model"`
	}
	b, err := marshalWithExtra(payload{Model: "gpt-4o"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["model"] != "gpt-4o" {
		t.Fatalf("got %v", m)
	}
	if len(m) != 1 {
		t.Fatalf("expected 1 key, got %d", len(m))
	}
}

func TestMarshalWithExtra_mergesTopLevel(t *testing.T) {
	type payload struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	extra := map[string]any{
		"google": map[string]any{
			"thinking_config": map[string]any{
				"include_thoughts": true,
				"thinking_budget":  10240,
			},
		},
	}
	b, err := marshalWithExtra(payload{Model: "gemini", Stream: true}, extra)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["model"] != "gemini" {
		t.Fatalf("model: got %v", m["model"])
	}
	if m["stream"] != true {
		t.Fatalf("stream: got %v", m["stream"])
	}
	google, ok := m["google"].(map[string]any)
	if !ok {
		t.Fatalf("google: got %T", m["google"])
	}
	tc, ok := google["thinking_config"].(map[string]any)
	if !ok {
		t.Fatalf("thinking_config: got %T", google["thinking_config"])
	}
	if tc["include_thoughts"] != true {
		t.Fatalf("include_thoughts: got %v", tc["include_thoughts"])
	}
}

func TestMarshalWithExtra_overridesStructField(t *testing.T) {
	type payload struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
	}
	extra := map[string]any{
		"max_tokens": 9999,
	}
	b, err := marshalWithExtra(payload{Model: "m", MaxTokens: 100}, extra)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	// extra should win
	if m["max_tokens"] != float64(9999) {
		t.Fatalf("max_tokens: got %v", m["max_tokens"])
	}
}
