// Package knowledge manages the user's long-term experience library.
//
// Experience files are Markdown documents stored at:
//
//	~/.claw/data/experiences/{safe_topic}.md
//
// Each file represents distilled, reusable knowledge about a named topic
// (e.g. "Linux 开发", "Docker", "Go 并发"). Files are written by the
// /learn command and can be injected as session context via /exp use.
package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ExperienceMeta holds display metadata for one experience file.
type ExperienceMeta struct {
	Topic     string // human-readable topic name (derived from filename)
	Filename  string // safe filename without directory
	Size      int64  // bytes
	UpdatedAt time.Time
}

// ExperienceStore is a simple file-based store for Markdown experience files.
type ExperienceStore struct {
	dir string
}

// NewExperienceStore returns a store rooted at dir.
// The directory is created on first Save.
func NewExperienceStore(dir string) *ExperienceStore {
	return &ExperienceStore{dir: dir}
}

// Dir returns the storage directory path.
func (s *ExperienceStore) Dir() string { return s.dir }

// SafeName converts a topic string to a safe filename stem (no extension).
// e.g. "Linux 开发" → "linux_开发", "Docker/compose" → "docker_compose"
func (s *ExperienceStore) SafeName(topic string) string {
	topic = strings.TrimSpace(topic)
	// Strip surrounding quotes users may type from shell completion hint.
	topic = strings.Trim(topic, `"'`)
	var b strings.Builder
	for _, r := range strings.ToLower(topic) {
		switch {
		case r == ' ' || r == '-' || r == '/' || r == '\\':
			b.WriteRune('_')
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.':
			b.WriteRune(r)
			// skip other punctuation
		}
	}
	name := b.String()
	name = strings.Trim(name, "_")
	if name == "" {
		name = "unnamed"
	}
	return name
}

// Path returns the full file path for a topic.
func (s *ExperienceStore) Path(topic string) string {
	return filepath.Join(s.dir, s.SafeName(topic)+".md")
}

// Exists reports whether an experience file for topic already exists.
func (s *ExperienceStore) Exists(topic string) bool {
	_, err := os.Stat(s.Path(topic))
	return err == nil
}

// Load reads the Markdown content for topic. Returns ("", nil) if not found.
func (s *ExperienceStore) Load(topic string) (string, error) {
	data, err := os.ReadFile(s.Path(topic))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("knowledge: load %q: %w", topic, err)
	}
	return string(data), nil
}

// Save writes content to the experience file for topic, creating the
// directory if needed.
func (s *ExperienceStore) Save(topic, content string) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("knowledge: mkdir: %w", err)
	}
	path := s.Path(topic)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return fmt.Errorf("knowledge: write %q: %w", topic, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("knowledge: rename %q: %w", topic, err)
	}
	return nil
}

// Delete removes the experience file for topic.
func (s *ExperienceStore) Delete(topic string) error {
	err := os.Remove(s.Path(topic))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// List returns metadata for all stored experience files, sorted by name.
func (s *ExperienceStore) List() ([]ExperienceMeta, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("knowledge: list: %w", err)
	}
	var out []ExperienceMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".md")
		out = append(out, ExperienceMeta{
			Topic:     stem,
			Filename:  e.Name(),
			Size:      info.Size(),
			UpdatedAt: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Topic < out[j].Topic })
	return out, nil
}
