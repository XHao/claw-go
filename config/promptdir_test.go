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
	if !strings.Contains(result, "Safety Rules") {
		t.Errorf("expected embedded safety in result for empty dir, got %q", result)
	}
}

func TestLoadPromptDir_MissingDir(t *testing.T) {
	result, err := LoadPromptDir("/tmp/claw-nonexistent-dir-xyz")
	if err != nil {
		t.Fatalf("unexpected error on missing dir: %v", err)
	}
	if !strings.Contains(result, "Safety Rules") {
		t.Errorf("expected embedded safety in result for missing dir, got %q", result)
	}
}

func TestLoadPromptDir_BehaviorAndUser(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "behavior.md"), []byte("Behavior content."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "user.md"), []byte("User content."), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadPromptDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Behavior content.") {
		t.Errorf("expected behavior content in result, got %q", result)
	}
	if !strings.Contains(result, "User content.") {
		t.Errorf("expected user content in result, got %q", result)
	}
	// behavior.md must appear before user.md
	if strings.Index(result, "Behavior content.") > strings.Index(result, "User content.") {
		t.Errorf("expected behavior before user, got %q", result)
	}
}

func TestLoadPromptDir_OnlyBehavior(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "behavior.md"), []byte("Behavior only."), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadPromptDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Behavior only.") {
		t.Errorf("expected behavior content, got %q", result)
	}
}

func TestLoadPromptDir_UnknownFileIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "custom.md"), []byte("Should be ignored."), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadPromptDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, "Should be ignored.") {
		t.Errorf("unknown file should be ignored, got %q", result)
	}
}

func TestLoadPromptDir_FrontmatterStripped(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: behavior\nenabled: true\n---\n\nBehavior body."
	if err := os.WriteFile(filepath.Join(dir, "behavior.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadPromptDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Behavior body.") {
		t.Errorf("expected body without frontmatter, got %q", result)
	}
	if strings.Contains(result, "name: behavior") {
		t.Errorf("frontmatter should be stripped, got %q", result)
	}
}

func TestLoadPromptDir_SafetyAlwaysPrepended(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "behavior.md"), []byte("Behavior."), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadPromptDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Safety Rules") {
		t.Errorf("embedded safety must always be prepended, got: %q", result)
	}
	if strings.Index(result, "Safety Rules") > strings.Index(result, "Behavior.") {
		t.Errorf("safety must appear before behavior, got: %q", result)
	}
}

func TestStripFrontmatter_WithFrontmatter(t *testing.T) {
	content := "---\nname: test\nenabled: true\n---\n\nBody text."
	got := stripFrontmatter(content)
	if got != "Body text." {
		t.Errorf("got %q, want %q", got, "Body text.")
	}
}

func TestStripFrontmatter_NoFrontmatter(t *testing.T) {
	content := "Plain text content."
	got := stripFrontmatter(content)
	if got != "Plain text content." {
		t.Errorf("got %q, want %q", got, "Plain text content.")
	}
}

func TestStripFrontmatter_UnclosedFrontmatter(t *testing.T) {
	content := "---\nname: test\n\nNo closing delimiter."
	got := stripFrontmatter(content)
	// Treated as plain text when no closing --- found.
	if !strings.Contains(got, "name: test") {
		t.Errorf("expected full content for unclosed frontmatter, got %q", got)
	}
}
