package provider

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

func TestOAImageBlockFromFile_JPEG(t *testing.T) {
	// Minimal JPEG magic bytes.
	jpegBytes := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}
	f, err := os.CreateTemp("", "test-*.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.Write(jpegBytes)
	f.Close()

	block, err := oaImageBlockFromFile(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if block.Type != "image_url" {
		t.Fatalf("type: got %q, want image_url", block.Type)
	}
	if block.ImageURL == nil {
		t.Fatal("image_url is nil")
	}
	expected := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes)
	if block.ImageURL.URL != expected {
		t.Fatalf("url mismatch: got %q", block.ImageURL.URL)
	}
}

func TestBuildOAMessages_WithImagePaths(t *testing.T) {
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	f, err := os.CreateTemp("", "test-*.png")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.Write(pngBytes)
	f.Close()

	p := NewOpenAI("https://api.openai.com/v1", "key", "gpt-4o", 1024, 30, 0, false, nil)

	messages := []Message{
		{Role: "user", Content: "describe this", ImagePaths: []string{f.Name()}},
	}

	// Call CompleteWithTools with a nil context just to exercise message building.
	// We can't make a real HTTP call, so instead test the wire message construction directly.
	oaMsgs := buildOAMessages(messages)

	if len(oaMsgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(oaMsgs))
	}
	blocks, ok := oaMsgs[0].Content.([]oaContentBlock)
	if !ok {
		t.Fatalf("content is not []oaContentBlock, got %T", oaMsgs[0].Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (image + text), got %d", len(blocks))
	}
	if blocks[0].Type != "image_url" {
		t.Fatalf("first block type: got %q, want image_url", blocks[0].Type)
	}
	if blocks[0].ImageURL == nil || !strings.HasPrefix(blocks[0].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("image_url malformed: %v", blocks[0].ImageURL)
	}
	if blocks[1].Type != "text" || blocks[1].Text != "describe this" {
		t.Fatalf("second block: got type=%q text=%q", blocks[1].Type, blocks[1].Text)
	}

	_ = p // suppress unused warning
}
