package client

import (
	"strings"
	"testing"
)

func TestWrapPlainRespectsWidth(t *testing.T) {
	lines := wrapPlain("token report should wrap on narrow terminals", 12)
	if len(lines) < 2 {
		t.Fatalf("expected wrapped lines, got %v", lines)
	}
	for _, line := range lines {
		if visWidth(line) > 12 {
			t.Fatalf("line exceeds width: %q", line)
		}
	}
	joined := strings.Join(lines, " ")
	if !strings.Contains(joined, "token") || !strings.Contains(joined, "terminals") {
		t.Fatalf("unexpected wrapped content: %v", lines)
	}
}
