package provider

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestNewAnthropic_defaults(t *testing.T) {
	p := NewAnthropic("", "sk-ant-test", "claude-opus-4-5", 4096, 120, 0, true, nil, nil)
	if p.apiKey != "sk-ant-test" {
		t.Fatalf("apiKey: got %q", p.apiKey)
	}
	if p.model != "claude-opus-4-5" {
		t.Fatalf("model: got %q", p.model)
	}
	if p.maxTokens != 4096 {
		t.Fatalf("maxTokens: got %d", p.maxTokens)
	}
	if p.httpClient == nil {
		t.Fatal("httpClient is nil")
	}
}

func TestNewAnthropic_timeoutDefault(t *testing.T) {
	p := NewAnthropic("", "key", "model", 1024, 0, 0, true, nil, nil)
	if p.httpClient.Timeout.Seconds() != 120 {
		t.Fatalf("expected 120s timeout, got %v", p.httpClient.Timeout)
	}
}

func TestExtractSystem(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "system", Content: "Extra context."},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi"},
	}
	system, rest := extractSystem(msgs)
	if system != "You are helpful.\n\nExtra context." {
		t.Fatalf("system: got %q", system)
	}
	if len(rest) != 2 {
		t.Fatalf("rest len: got %d", len(rest))
	}
	if rest[0].Role != "user" {
		t.Fatalf("rest[0].Role: got %q", rest[0].Role)
	}
}

func TestExtractSystem_none(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "Hi"},
	}
	system, rest := extractSystem(msgs)
	if system != "" {
		t.Fatalf("expected empty system, got %q", system)
	}
	if len(rest) != 1 {
		t.Fatalf("rest len: got %d", len(rest))
	}
}

func TestConvertMessages_plainText(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
	}
	wire := convertMessages(msgs)
	if len(wire) != 2 {
		t.Fatalf("len: got %d", len(wire))
	}
	if wire[0].Role != "user" {
		t.Fatalf("wire[0].Role: got %q", wire[0].Role)
	}
	blocks, ok := wire[0].Content.([]antContentBlock)
	if !ok {
		t.Fatalf("wire[0].Content type: %T", wire[0].Content)
	}
	if blocks[0].Type != "text" || blocks[0].Text != "Hello" {
		t.Fatalf("blocks[0]: %+v", blocks[0])
	}
}

func TestConvertMessages_toolCall(t *testing.T) {
	msgs := []Message{
		{
			Role: "assistant",
			ToolCalls: []ToolCallRequest{
				{ID: "call_1", Type: "function", Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "bash", Arguments: `{"command":"ls"}`}},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_1",
			Name:       "bash",
			Content:    "file1.go\nfile2.go",
		},
	}
	wire := convertMessages(msgs)
	if len(wire) != 2 {
		t.Fatalf("len: got %d", len(wire))
	}
	assistantBlocks, ok := wire[0].Content.([]antContentBlock)
	if !ok {
		t.Fatalf("assistant content type: %T", wire[0].Content)
	}
	if assistantBlocks[0].Type != "tool_use" {
		t.Fatalf("expected tool_use, got %q", assistantBlocks[0].Type)
	}
	if assistantBlocks[0].ID != "call_1" {
		t.Fatalf("tool_use id: got %q", assistantBlocks[0].ID)
	}
	if wire[1].Role != "user" {
		t.Fatalf("tool result role: got %q", wire[1].Role)
	}
	resultBlocks, ok := wire[1].Content.([]antContentBlock)
	if !ok {
		t.Fatalf("tool result content type: %T", wire[1].Content)
	}
	if resultBlocks[0].Type != "tool_result" {
		t.Fatalf("expected tool_result, got %q", resultBlocks[0].Type)
	}
	if resultBlocks[0].ToolUseID != "call_1" {
		t.Fatalf("tool_use_id: got %q", resultBlocks[0].ToolUseID)
	}
}

func TestConvertTools(t *testing.T) {
	tools := []ToolDef{
		{
			Name:        "bash",
			Description: "Run shell commands",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
		},
	}
	antTools := convertTools(tools)
	if len(antTools) != 1 {
		t.Fatalf("len: got %d", len(antTools))
	}
	if antTools[0].Name != "bash" {
		t.Fatalf("name: got %q", antTools[0].Name)
	}
	if antTools[0].Description != "Run shell commands" {
		t.Fatalf("description: got %q", antTools[0].Description)
	}
	var schema map[string]any
	if err := json.Unmarshal(antTools[0].InputSchema, &schema); err != nil {
		t.Fatalf("unmarshal input_schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("schema type: got %v", schema["type"])
	}
}

func TestConvertTools_nilParameters(t *testing.T) {
	tools := []ToolDef{
		{Name: "noop", Description: "Does nothing", Parameters: nil},
	}
	antTools := convertTools(tools)
	if len(antTools) != 1 {
		t.Fatalf("len: got %d", len(antTools))
	}
	var schema map[string]any
	if err := json.Unmarshal(antTools[0].InputSchema, &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("schema type: got %v", schema["type"])
	}
}

func TestReadJSON_textReply(t *testing.T) {
	p := NewAnthropic("", "key", "model", 1024, 30, 0, false, nil, nil)
	body := `{
		"content": [{"type":"text","text":"Hello world"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`
	result, err := p.readJSON(strings.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Hello world" {
		t.Fatalf("content: got %q", result.Content)
	}
	if result.StopReason != "end_turn" {
		t.Fatalf("stop_reason: got %q", result.StopReason)
	}
	if result.Usage.PromptTokens != 10 {
		t.Fatalf("input_tokens: got %d", result.Usage.PromptTokens)
	}
}

func TestReadJSON_toolUse(t *testing.T) {
	p := NewAnthropic("", "key", "model", 1024, 30, 0, false, nil, nil)
	body := `{
		"content": [
			{"type":"tool_use","id":"toolu_01","name":"bash","input":{"command":"ls"}}
		],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 20, "output_tokens": 8}
	}`
	result, err := p.readJSON(strings.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("tool_calls len: got %d", len(result.ToolCalls))
	}
	tc := result.ToolCalls[0]
	if tc.ID != "toolu_01" {
		t.Fatalf("id: got %q", tc.ID)
	}
	if tc.Function.Name != "bash" {
		t.Fatalf("name: got %q", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"command":"ls"}` {
		t.Fatalf("arguments: got %q", tc.Function.Arguments)
	}
}

func TestReadJSON_thinking_filtered(t *testing.T) {
	p := NewAnthropic("", "key", "model", 1024, 30, 10000, false, nil, nil)
	body := `{
		"content": [
			{"type":"thinking","thinking":"Let me reason..."},
			{"type":"text","text":"The answer is 42"}
		],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 30, "output_tokens": 50}
	}`
	result, err := p.readJSON(strings.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "The answer is 42" {
		t.Fatalf("content: got %q", result.Content)
	}
}

func TestReadJSON_apiError(t *testing.T) {
	p := NewAnthropic("", "key", "model", 1024, 30, 0, false, nil, nil)
	body := `{"type":"error","error":{"type":"authentication_error","message":"Invalid API key"}}`
	_, err := p.readJSON(strings.NewReader(body))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "authentication_error") {
		t.Fatalf("error: got %q", err.Error())
	}
}

func TestReadSSE_text(t *testing.T) {
	p := NewAnthropic("", "key", "model", 1024, 30, 0, true, nil, nil)
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	var deltas []string
	streamFn := func(d string) { deltas = append(deltas, d) }
	result, err := p.readSSE(strings.NewReader(sse), streamFn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Hello world" {
		t.Fatalf("content: got %q", result.Content)
	}
	if result.StopReason != "end_turn" {
		t.Fatalf("stop_reason: got %q", result.StopReason)
	}
	if result.Usage.CompletionTokens != 5 {
		t.Fatalf("output_tokens: got %d", result.Usage.CompletionTokens)
	}
	if len(deltas) != 2 {
		t.Fatalf("deltas len: got %d", len(deltas))
	}
	if deltas[0] != "Hello" || deltas[1] != " world" {
		t.Fatalf("deltas: got %v", deltas)
	}
}

func TestReadSSE_thinking_filtered(t *testing.T) {
	p := NewAnthropic("", "key", "model", 1024, 30, 10000, true, nil, nil)
	sse := "event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think..."}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"42"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":1}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	var deltas []string
	streamFn := func(d string) { deltas = append(deltas, d) }
	result, err := p.readSSE(strings.NewReader(sse), streamFn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "42" {
		t.Fatalf("content: got %q", result.Content)
	}
	if len(deltas) != 1 || deltas[0] != "42" {
		t.Fatalf("deltas: got %v", deltas)
	}
}

func TestReadSSE_toolUse(t *testing.T) {
	p := NewAnthropic("", "key", "model", 1024, 30, 0, true, nil, nil)
	sse := "event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"bash","input":{}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":15}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	result, err := p.readSSE(strings.NewReader(sse), func(string) {})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("tool_calls len: got %d", len(result.ToolCalls))
	}
	tc := result.ToolCalls[0]
	if tc.ID != "toolu_01" {
		t.Fatalf("id: got %q", tc.ID)
	}
	if tc.Function.Name != "bash" {
		t.Fatalf("name: got %q", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"command":"ls"}` {
		t.Fatalf("arguments: got %q", tc.Function.Arguments)
	}
}

func TestImageBlockFromFile_PNG(t *testing.T) {
	// Minimal valid 1x1 red PNG (67 bytes).
	pngBytes := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // PNG signature
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, // IDAT chunk
		0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
		0x00, 0x00, 0x02, 0x00, 0x01, 0xe2, 0x21, 0xbc,
		0x33, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, // IEND chunk
		0x44, 0xae, 0x42, 0x60, 0x82,
	}
	f, err := os.CreateTemp("", "test-*.png")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.Write(pngBytes)
	f.Close()

	block, err := imageBlockFromFile(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if block.Type != "image" {
		t.Fatalf("type: got %q, want %q", block.Type, "image")
	}
	if block.Source == nil {
		t.Fatal("source is nil")
	}
	if block.Source.Type != "base64" {
		t.Fatalf("source.type: got %q, want %q", block.Source.Type, "base64")
	}
	if block.Source.MediaType != "image/png" {
		t.Fatalf("source.media_type: got %q, want %q", block.Source.MediaType, "image/png")
	}
	expected := base64.StdEncoding.EncodeToString(pngBytes)
	if block.Source.Data != expected {
		t.Fatalf("source.data mismatch")
	}
}

func TestConvertMessages_WithImagePaths(t *testing.T) {
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a} // PNG header stub
	f, err := os.CreateTemp("", "test-*.png")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.Write(pngBytes)
	f.Close()

	msgs := []Message{
		{Role: "user", Content: "describe this", ImagePaths: []string{f.Name()}},
	}
	out := convertMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	blocks, ok := out[0].Content.([]antContentBlock)
	if !ok {
		t.Fatalf("content is not []antContentBlock")
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (image+text), got %d", len(blocks))
	}
	if blocks[0].Type != "image" {
		t.Fatalf("first block type: got %q, want image", blocks[0].Type)
	}
	if blocks[1].Type != "text" || blocks[1].Text != "describe this" {
		t.Fatalf("second block: got type=%q text=%q", blocks[1].Type, blocks[1].Text)
	}
}
