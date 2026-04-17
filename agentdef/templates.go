package agentdef

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// TemplateMeta holds the parsed metadata from a template's template.yaml.
type TemplateMeta struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tags        []string `yaml:"tags"`
	TypeKey     string   // directory name under agents/, e.g. "finance"
}

// ListTemplates reads all template.yaml files from the embedded FS (rooted at
// "agents/") and returns their metadata sorted by TypeKey.
func ListTemplates(fsys fs.FS) ([]TemplateMeta, error) {
	entries, err := fs.ReadDir(fsys, "agents")
	if err != nil {
		return nil, fmt.Errorf("list templates: read agents dir: %w", err)
	}
	var metas []TemplateMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		typeKey := e.Name()
		metaPath := "agents/" + typeKey + "/template.yaml"
		data, err := fs.ReadFile(fsys, metaPath)
		if err != nil {
			continue
		}
		var m TemplateMeta
		if err := yaml.Unmarshal(data, &m); err != nil {
			continue
		}
		m.TypeKey = typeKey
		metas = append(metas, m)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].TypeKey < metas[j].TypeKey })
	return metas, nil
}

// InstallTemplate copies the embedded template for typeKey from fsys into
// destDir/typeKey/. Files that already exist are skipped. The subdirectories
// memory/ and skills/ are always created even if absent from the template.
// Returns an error if typeKey does not exist in the embedded FS.
func InstallTemplate(fsys fs.FS, typeKey, destDir string) error {
	srcRoot := "agents/" + typeKey
	if _, err := fs.Stat(fsys, srcRoot); err != nil {
		return fmt.Errorf("unknown agent template %q", typeKey)
	}
	agentDir := filepath.Join(destDir, typeKey)

	for _, sub := range []string{"memory", "skills", "experiences", "procedures"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0o700); err != nil {
			return fmt.Errorf("install template %q: mkdir %s: %w", typeKey, sub, err)
		}
	}

	return fs.WalkDir(fsys, srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcRoot, path)
		dest := filepath.Join(agentDir, rel)

		if d.IsDir() {
			return os.MkdirAll(dest, 0o700)
		}
		if d.Name() == "template.yaml" {
			return nil
		}
		if _, err := os.Stat(dest); err == nil {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o600)
	})
}
