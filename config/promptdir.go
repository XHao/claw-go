package config

import (
	_ "embed"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed embedded/safety.md
var embeddedSafetyPrompt string

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
	Enabled  *bool  `yaml:"enabled"`
	Priority *int   `yaml:"priority"`
}

// splitFrontmatter splits content into (yamlBlock, body).
// If no valid frontmatter is found, returns ("", trimmed content).
func splitFrontmatter(content string) (yamlBlock, body string) {
	if !strings.HasPrefix(content, "---\n") {
		return "", strings.TrimSpace(content)
	}
	lines := strings.Split(content[4:], "\n")
	for i, line := range lines {
		if line == "---" || line == "---\r" {
			return strings.Join(lines[:i], "\n"),
				strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		}
	}
	return "", strings.TrimSpace(content)
}

// parsePromptFile reads a single .md file and splits it into frontmatter + body.
// If no frontmatter is present, the whole file is treated as body with
// Enabled=true and Priority=50.
func parsePromptFile(path string) (PromptFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PromptFile{}, err
	}

	pf := PromptFile{
		Enabled:  true,
		Priority: 50,
	}

	yamlBlock, body := splitFrontmatter(string(data))
	pf.Body = body

	if yamlBlock != "" {
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
	}
	return pf, nil
}

// LoadPromptDir scans dir for *.md files, parses each one, filters disabled
// files and any file with layer: safety (the embedded safety rules are always
// used instead), sorts by priority (then filename), concatenates bodies with
// "\n\n", and returns the assembled prompt string with the embedded safety
// rules prepended.
//
// The embedded safety rules are unconditional — they are always the first
// element of the returned prompt regardless of what files exist in dir.
func LoadPromptDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return embeddedSafetyBody(), nil
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
			continue
		}
		if !pf.Enabled {
			continue
		}
		// Safety layer files from the filesystem are always ignored;
		// the embedded safety rules are unconditionally used instead.
		if pf.Layer == "safety" {
			continue
		}
		files = append(files, indexedFile{pf: pf, filename: e.Name()})
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].pf.Priority != files[j].pf.Priority {
			return files[i].pf.Priority < files[j].pf.Priority
		}
		return files[i].filename < files[j].filename
	})

	parts := make([]string, 0, len(files)+1)
	parts = append(parts, embeddedSafetyBody())
	for _, f := range files {
		if f.pf.Body != "" {
			parts = append(parts, f.pf.Body)
		}
	}

	return strings.Join(parts, "\n\n"), nil
}

// embeddedSafetyBody strips the YAML frontmatter from the embedded safety.md
// and returns only the body text.
func embeddedSafetyBody() string {
	_, body := splitFrontmatter(embeddedSafetyPrompt)
	return body
}
