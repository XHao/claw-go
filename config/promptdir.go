package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// defaultSafetyPrompt is prepended to the assembled prompt when no file with
// layer: safety is found in the prompt directory. It cannot be removed by
// deleting files — only overridden by adding a 00-safety.md with layer: safety.
const defaultSafetyPrompt = `You must never execute commands that could cause irreversible damage to the host system (e.g. deleting system files, overwriting critical configs, escalating privileges without explicit user confirmation). When a tool call would be destructive and irreversible, ask the user to confirm first.`

// PromptFile represents a single parsed prompt layer file.
type PromptFile struct {
	Name     string
	Layer    string
	Enabled  bool
	Priority int
	Body     string
}

// promptFrontmatter is the YAML structure parsed from the --- block.
type promptFrontmatter struct {
	Name     string `yaml:"name"`
	Layer    string `yaml:"layer"`
	Enabled  *bool  `yaml:"enabled"`  // pointer so we can detect absence
	Priority *int   `yaml:"priority"` // pointer so we can detect absence
}

// parsePromptFile reads a single .md file and splits it into frontmatter + body.
// If no frontmatter is present, the whole file is treated as body with
// Enabled=true and Priority=50.
func parsePromptFile(path string) (PromptFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PromptFile{}, err
	}
	content := string(data)

	pf := PromptFile{
		Enabled:  true,
		Priority: 50,
	}

	// Check for YAML frontmatter: file must start with "---\n".
	if !strings.HasPrefix(content, "---\n") {
		pf.Body = strings.TrimSpace(content)
		return pf, nil
	}

	// Scan lines after the opening "---\n" to find the closing "---" line.
	rest := content[4:]
	lines := strings.SplitN(rest, "\n", -1)
	closingIdx := -1
	for i, line := range lines {
		if line == "---" || line == "---\r" {
			closingIdx = i
			break
		}
	}

	if closingIdx < 0 {
		// No closing delimiter found: treat entire file as body (not frontmatter).
		pf.Body = strings.TrimSpace(content)
		return pf, nil
	}

	yamlBlock := strings.Join(lines[:closingIdx], "\n")
	body := strings.TrimSpace(strings.Join(lines[closingIdx+1:], "\n"))

	var fm promptFrontmatter
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err == nil {
		pf.Name = fm.Name
		pf.Layer = fm.Layer
		if fm.Enabled != nil {
			pf.Enabled = *fm.Enabled
		}
		if fm.Priority != nil {
			pf.Priority = *fm.Priority
		}
	}
	pf.Body = body
	return pf, nil
}

// LoadPromptDir scans dir for *.md files, parses each one, filters disabled
// files, sorts by priority (then filename), concatenates bodies with "\n\n",
// and returns the assembled prompt string.
//
// Returns ("", nil) if dir does not exist or contains no enabled .md files.
func LoadPromptDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	type indexedFile struct {
		pf       PromptFile
		filename string
	}

	var files []indexedFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		pf, err := parsePromptFile(path)
		if err != nil {
			// Skip unreadable files silently.
			continue
		}
		if !pf.Enabled {
			continue
		}
		files = append(files, indexedFile{pf: pf, filename: e.Name()})
	}

	if len(files) == 0 {
		return "", nil
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].pf.Priority != files[j].pf.Priority {
			return files[i].pf.Priority < files[j].pf.Priority
		}
		return files[i].filename < files[j].filename
	})

	// Check whether any file declares layer: safety.
	hasSafety := false
	for _, f := range files {
		if f.pf.Layer == "safety" {
			hasSafety = true
			break
		}
	}

	parts := make([]string, 0, len(files)+1)
	if !hasSafety {
		parts = append(parts, defaultSafetyPrompt)
	}
	for _, f := range files {
		if f.pf.Body != "" {
			parts = append(parts, f.pf.Body)
		}
	}

	return strings.Join(parts, "\n\n"), nil
}
