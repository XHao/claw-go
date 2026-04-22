package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPromptDir_Empty(t *testing.T) {
	dir := t.TempDir()
	result, err := LoadPromptDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Embedded safety is always returned even for an empty dir.
	if !strings.Contains(result, "Safety Rules") {
		t.Errorf("expected embedded safety in result for empty dir, got %q", result)
	}
}

func TestLoadPromptDir_MissingDir(t *testing.T) {
	result, err := LoadPromptDir("/tmp/claw-nonexistent-dir-xyz")
	if err != nil {
		t.Fatalf("unexpected error on missing dir: %v", err)
	}
	// Embedded safety is always returned even for a missing dir.
	if !strings.Contains(result, "Safety Rules") {
		t.Errorf("expected embedded safety in result for missing dir, got %q", result)
	}
}

func TestLoadPromptDir_SingleFile(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: test\nlayer: persona\nenabled: true\npriority: 1\n---\n\nHello world."
	if err := os.WriteFile(filepath.Join(dir, "01-persona.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadPromptDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Hello world.") {
		t.Errorf("expected body in result, got %q", result)
	}
}

func TestLoadPromptDir_DisabledFile(t *testing.T) {
	dir := t.TempDir()
	enabled := "---\nname: a\nlayer: persona\nenabled: true\npriority: 1\n---\n\nEnabled."
	disabled := "---\nname: b\nlayer: behavior\nenabled: false\npriority: 2\n---\n\nDisabled."
	if err := os.WriteFile(filepath.Join(dir, "01-a.md"), []byte(enabled), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "02-b.md"), []byte(disabled), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadPromptDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Enabled.") {
		t.Errorf("expected enabled content in result, got %q", result)
	}
	if strings.Contains(result, "Disabled.") {
		t.Errorf("disabled content should not appear in result, got %q", result)
	}
}

func TestLoadPromptDir_PriorityOrder(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"10-b.md": "---\nname: b\nlayer: behavior\nenabled: true\npriority: 10\n---\n\nSecond.",
		"01-a.md": "---\nname: a\nlayer: persona\nenabled: true\npriority: 1\n---\n\nFirst.",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := LoadPromptDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Safety fallback is prepended; persona content follows in priority order.
	if !strings.Contains(result, "First.") {
		t.Errorf("expected First. in result, got %q", result)
	}
	if !strings.Contains(result, "Second.") {
		t.Errorf("expected Second. in result, got %q", result)
	}
	// Verify order: First. must appear before Second.
	firstIdx := strings.Index(result, "First.")
	secondIdx := strings.Index(result, "Second.")
	if firstIdx >= secondIdx {
		t.Errorf("expected First. before Second., got %q", result)
	}
}

func TestLoadPromptDir_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	content := "Just plain text, no frontmatter."
	if err := os.WriteFile(filepath.Join(dir, "50-plain.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadPromptDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, content) {
		t.Errorf("expected %q in result, got %q", content, result)
	}
}

func TestParsePromptFile_Fields(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: safety\nlayer: safety\nenabled: true\npriority: 0\n---\n\nDo not harm."
	path := filepath.Join(dir, "00-safety.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	pf, err := parsePromptFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pf.Name != "safety" {
		t.Errorf("name: got %q, want %q", pf.Name, "safety")
	}
	if pf.Layer != "safety" {
		t.Errorf("layer: got %q, want %q", pf.Layer, "safety")
	}
	if !pf.Enabled {
		t.Error("expected enabled=true")
	}
	if pf.Priority != 0 {
		t.Errorf("priority: got %d, want 0", pf.Priority)
	}
	if pf.Body != "Do not harm." {
		t.Errorf("body: got %q, want %q", pf.Body, "Do not harm.")
	}
}

func TestLoadPromptDir_EmptyFrontmatter(t *testing.T) {
	dir := t.TempDir()
	// File with empty frontmatter block and no body.
	content := "---\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "00-empty.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadPromptDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Embedded safety is always present.
	if !strings.Contains(result, "Safety Rules") {
		t.Errorf("expected embedded safety in result, got: %q", result)
	}
	// The file had an empty body, so no extra content beyond the safety rules.
	// Frontmatter delimiters from the empty file should not appear.
	// (The safety.md itself uses --- as a section divider, so we only check
	// that no raw frontmatter opening "---\n---" from the empty file leaks through.)
	if strings.Contains(result, "---\n---") {
		t.Errorf("raw frontmatter delimiters from empty file should not appear in result, got: %q", result)
	}
}

func TestParsePromptFile_PriorityDefaultWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	// Frontmatter present but no priority field — should default to 50.
	content := "---\nname: test\nlayer: persona\nenabled: true\n---\n\nBody."
	path := filepath.Join(dir, "test.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	pf, err := parsePromptFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pf.Priority != 50 {
		t.Errorf("expected default priority 50, got %d", pf.Priority)
	}
}

func TestParsePromptFile_UnclosedFrontmatter(t *testing.T) {
	dir := t.TempDir()
	// File starts with "---\n" but has no closing "---" line.
	content := "---\nname: test\nlayer: persona\n\nThis is body text with no closing delimiter."
	path := filepath.Join(dir, "test.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	pf, err := parsePromptFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Treated as plain text (no frontmatter), body = whole file trimmed.
	if !strings.Contains(pf.Body, "name: test") {
		t.Errorf("expected YAML to appear verbatim in body for unclosed frontmatter, got: %q", pf.Body)
	}
	// Should NOT have frontmatter fields parsed.
	if pf.Name != "" {
		t.Errorf("expected no name parsed for unclosed frontmatter, got %q", pf.Name)
	}
}

func TestLoadPromptDir_SafetyFallback(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: persona\nlayer: persona\nenabled: true\npriority: 1\n---\n\nHello."
	if err := os.WriteFile(filepath.Join(dir, "01-persona.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadPromptDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Embedded safety is always prepended regardless of files in dir.
	if !strings.Contains(result, "Safety Rules") {
		t.Errorf("expected embedded safety prompt to be prepended, got: %q", result)
	}
	if !strings.Contains(result, "Hello.") {
		t.Errorf("expected persona body in result, got: %q", result)
	}
}

func TestLoadPromptDir_SafetyFileIsIgnored(t *testing.T) {
	dir := t.TempDir()
	// A file with layer: safety — must be silently ignored; embedded safety always wins.
	content := "---\nname: safety\nlayer: safety\nenabled: true\npriority: 0\n---\n\nCustom safety rules."
	if err := os.WriteFile(filepath.Join(dir, "00-safety.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadPromptDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Filesystem safety layer must be ignored.
	if strings.Contains(result, "Custom safety rules.") {
		t.Errorf("filesystem safety layer must be ignored, got: %q", result)
	}
	// Embedded safety must always be present.
	if !strings.Contains(result, "Safety Rules") {
		t.Errorf("embedded safety must always be present, got: %q", result)
	}
}
