package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ProcedureFile represents a single procedure document with metadata.
type ProcedureFile struct {
	Name     string   // human-readable name
	Tags     []string // matching tags for task classification
	Priority int      // lower = higher priority in injection order
	Body     string   // Markdown content (without frontmatter)
	Filename string   // safe filename stem (no extension)
}

type procedureFrontmatter struct {
	Name     string   `yaml:"name"`
	Tags     []string `yaml:"tags"`
	Priority int      `yaml:"priority"`
}

// ProcedureStore manages procedure Markdown files under a directory.
type ProcedureStore struct {
	dir string
}

// NewProcedureStore returns a store rooted at dir.
func NewProcedureStore(dir string) *ProcedureStore {
	return &ProcedureStore{dir: dir}
}

// Dir returns the storage directory.
func (s *ProcedureStore) Dir() string { return s.dir }

// safeName converts a procedure name to a safe filename stem.
func (s *ProcedureStore) safeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == ' ' || r == '-' || r == '/' || r == '\\':
			b.WriteRune('-')
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.':
			b.WriteRune(r)
		case r > 0x80: // CJK and other non-ASCII
			b.WriteRune(r)
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		result = "unnamed"
	}
	return result
}

// SafeName converts a topic string to a safe filename stem (exported).
func (s *ProcedureStore) SafeName(topic string) string {
	return s.safeName(topic)
}

// path returns the full file path for a given filename stem.
func (s *ProcedureStore) path(stem string) string {
	return filepath.Join(s.dir, stem+".md")
}

// Save writes a ProcedureFile to disk, creating or overwriting it.
func (s *ProcedureStore) Save(stem string, pf ProcedureFile) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("procedure: mkdir: %w", err)
	}
	if stem == "" {
		stem = s.safeName(pf.Name)
	}
	// Priority 0 is treated as "unset"; default to 50 (mid-range).
	// Callers who need the highest precedence should use Priority 1, not 0.
	if pf.Priority == 0 {
		pf.Priority = 50
	}

	fm := procedureFrontmatter{
		Name:     pf.Name,
		Tags:     pf.Tags,
		Priority: pf.Priority,
	}
	fmBytes, err := yaml.Marshal(fm)
	if err != nil {
		return fmt.Errorf("procedure: marshal frontmatter: %w", err)
	}
	content := "---\n" + string(fmBytes) + "---\n\n" + strings.TrimSpace(pf.Body) + "\n"

	tmp := s.path(stem) + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return fmt.Errorf("procedure: write: %w", err)
	}
	if err := os.Rename(tmp, s.path(stem)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("procedure: rename: %w", err)
	}
	return nil
}

// Append adds content to an existing procedure file's body with a timestamp separator.
// If the file does not exist, it creates a new one with an empty name and no tags.
func (s *ProcedureStore) Append(stem, content string) error {
	existing, err := s.load(stem)
	if err != nil {
		return err
	}
	if existing == nil {
		return s.Save(stem, ProcedureFile{Name: stem, Body: content})
	}
	separator := fmt.Sprintf("\n\n---\n*%s 追加*\n\n", time.Now().Format("2006-01-02 15:04"))
	existing.Body = existing.Body + separator + strings.TrimSpace(content)
	return s.Save(stem, *existing)
}

// load reads and parses a single procedure file by stem. Returns nil if not found.
func (s *ProcedureStore) load(stem string) (*ProcedureFile, error) {
	data, err := os.ReadFile(s.path(stem))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("procedure: read %s: %w", stem, err)
	}
	return parseProcedureFile(stem, string(data))
}

// parseProcedureFile splits a raw file into frontmatter + body.
func parseProcedureFile(stem, content string) (*ProcedureFile, error) {
	pf := &ProcedureFile{Filename: stem, Priority: 50}
	if !strings.HasPrefix(content, "---\n") {
		pf.Body = strings.TrimSpace(content)
		return pf, nil
	}
	rest := content[4:]
	lines := strings.Split(rest, "\n")
	closingIdx := -1
	for i, line := range lines {
		if line == "---" || line == "---\r" {
			closingIdx = i
			break
		}
	}
	if closingIdx < 0 {
		pf.Body = strings.TrimSpace(content)
		return pf, nil
	}
	yamlBlock := strings.Join(lines[:closingIdx], "\n")
	body := strings.TrimSpace(strings.Join(lines[closingIdx+1:], "\n"))
	var fm procedureFrontmatter
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err == nil {
		pf.Name = fm.Name
		pf.Tags = fm.Tags
		if fm.Priority > 0 {
			pf.Priority = fm.Priority
		}
	}
	pf.Body = body
	return pf, nil
}

// List returns all procedure files sorted by priority then filename.
func (s *ProcedureStore) List() ([]ProcedureFile, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("procedure: list: %w", err)
	}
	var out []ProcedureFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".md")
		pf, err := s.load(stem)
		if err != nil || pf == nil {
			continue
		}
		out = append(out, *pf)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].Filename < out[j].Filename
	})
	return out, nil
}

// FindByTags returns procedures that contain at least one of the given tags.
// Tags are matched case-insensitively.
func (s *ProcedureStore) FindByTags(tags []string) ([]ProcedureFile, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	if len(tags) == 0 {
		return all, nil
	}
	tagSet := make(map[string]bool, len(tags))
	for _, t := range tags {
		tagSet[strings.ToLower(t)] = true
	}
	var out []ProcedureFile
	for _, pf := range all {
		for _, t := range pf.Tags {
			if tagSet[strings.ToLower(t)] {
				out = append(out, pf)
				break
			}
		}
	}
	return out, nil
}

// AssemblePromptLayer renders all procedures into a promptdir-compatible Markdown string.
// Returns ("", nil) if the store is empty.
func (s *ProcedureStore) AssemblePromptLayer() (string, error) {
	procs, err := s.List()
	if err != nil {
		return "", err
	}
	if len(procs) == 0 {
		return "", nil
	}
	var sb strings.Builder
	sb.WriteString("---\nname: procedures\nlayer: procedures\nenabled: true\npriority: 20\n---\n")
	for _, p := range procs {
		sb.WriteString(fmt.Sprintf("\n## %s\n\n%s\n", p.Name, p.Body))
	}
	return sb.String(), nil
}
