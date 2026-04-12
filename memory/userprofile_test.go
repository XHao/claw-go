package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendUserFacts_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "user-profile-dynamic.md")

	facts := []string{"Prefers short answers", "Uses Go primarily"}
	if err := AppendUserFacts(path, facts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Prefers short answers") {
		t.Errorf("expected fact in file, got: %s", content)
	}
	if !strings.Contains(content, "Uses Go primarily") {
		t.Errorf("expected fact in file, got: %s", content)
	}
}

func TestAppendUserFacts_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "user-profile-dynamic.md")

	if err := AppendUserFacts(path, []string{"First fact"}); err != nil {
		t.Fatal(err)
	}
	if err := AppendUserFacts(path, []string{"Second fact"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "First fact") {
		t.Errorf("first fact missing: %s", content)
	}
	if !strings.Contains(content, "Second fact") {
		t.Errorf("second fact missing: %s", content)
	}
}

func TestAppendUserFacts_EmptySlice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "user-profile-dynamic.md")

	if err := AppendUserFacts(path, nil); err != nil {
		t.Fatalf("unexpected error on empty facts: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected no file created for empty facts")
	}
}

func TestAppendUserFacts_WhitespaceOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.md")

	if err := AppendUserFacts(path, []string{"  ", "\t", ""}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// File should not be created for whitespace-only facts.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected no file created for whitespace-only facts")
	}
}

func TestLoadDynamicProfile_Missing(t *testing.T) {
	result, err := LoadDynamicProfile("/tmp/claw-nonexistent-profile-xyz.md")
	if err != nil {
		t.Fatalf("unexpected error on missing file: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestLoadDynamicProfile_ReturnsContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.md")
	content := "## Observed preferences\n\n- Prefers Go"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadDynamicProfile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != content {
		t.Errorf("expected %q, got %q", content, result)
	}
}
