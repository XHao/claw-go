package config

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
)

//go:embed embedded/safety.md
var embeddedSafetyPrompt string

// LoadPromptDir assembles the system prompt from the prompts directory.
// Fixed load order: embedded safety → persona.md → behavior.md → user.md.
// Missing files are silently skipped.
func LoadPromptDir(dir string) (string, error) {
	parts := []string{embeddedSafetyBody()}

	for _, name := range []string{"persona.md", "behavior.md", "user.md"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		body := stripFrontmatter(string(data))
		if body != "" {
			parts = append(parts, body)
		}
	}

	return strings.Join(parts, "\n\n"), nil
}

// stripFrontmatter removes the YAML frontmatter block (if present) and
// returns the trimmed body text.
func stripFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return strings.TrimSpace(content)
	}
	lines := strings.Split(content[4:], "\n")
	for i, line := range lines {
		if line == "---" || line == "---\r" {
			return strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		}
	}
	return strings.TrimSpace(content)
}

// embeddedSafetyBody strips the YAML frontmatter from the embedded safety.md
// and returns only the body text.
func embeddedSafetyBody() string {
	return stripFrontmatter(embeddedSafetyPrompt)
}
