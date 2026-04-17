package agentdef_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/XHao/claw-go/agentdef"
)

// buildTestFS builds an in-memory FS that mimics the embedded agents/ tree.
func buildTestFS() fs.FS {
	return fstest.MapFS{
		"agents/code/template.yaml":             {Data: []byte("name: \"Code Engineer\"\ndescription: \"Software engineering assistant\"\ntags: [code]\n")},
		"agents/code/persona.md":                {Data: []byte("# Code Agent\nYou are a code engineer.")},
		"agents/code/tools.yaml":                {Data: []byte("extra_tools:\n  - bash\n")},
		"agents/code/procedures/code_review.md": {Data: []byte("# Code Review SOP\n1. Read the diff.")},
		"agents/code/experiences/seed.md":        {Data: []byte("# Seed\n- Prefer simple solutions.")},
		"agents/finance/template.yaml":           {Data: []byte("name: \"Finance Analyst\"\ndescription: \"US equity research\"\ntags: [finance]\n")},
		"agents/finance/persona.md":              {Data: []byte("# Finance Agent\nYou are a finance analyst.")},
	}
}

func TestListTemplates(t *testing.T) {
	tfs := buildTestFS()
	metas, err := agentdef.ListTemplates(tfs)
	if err != nil {
		t.Fatalf("ListTemplates error: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("expected 2 templates, got %d", len(metas))
	}
	if metas[0].TypeKey != "code" {
		t.Errorf("metas[0].TypeKey = %q, want %q", metas[0].TypeKey, "code")
	}
	if metas[1].TypeKey != "finance" {
		t.Errorf("metas[1].TypeKey = %q, want %q", metas[1].TypeKey, "finance")
	}
	if metas[0].Name != "Code Engineer" {
		t.Errorf("metas[0].Name = %q, want %q", metas[0].Name, "Code Engineer")
	}
}

func TestInstallTemplate_CreatesFiles(t *testing.T) {
	tfs := buildTestFS()
	dest := t.TempDir()

	if err := agentdef.InstallTemplate(tfs, "code", dest); err != nil {
		t.Fatalf("InstallTemplate error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dest, "code", "persona.md"))
	if err != nil {
		t.Fatalf("persona.md not created: %v", err)
	}
	if string(data) != "# Code Agent\nYou are a code engineer." {
		t.Errorf("persona.md content mismatch: %q", string(data))
	}

	if _, err := os.Stat(filepath.Join(dest, "code", "procedures")); err != nil {
		t.Errorf("procedures/ dir not created: %v", err)
	}

	for _, sub := range []string{"memory", "skills"} {
		if _, err := os.Stat(filepath.Join(dest, "code", sub)); err != nil {
			t.Errorf("%s/ dir not created: %v", sub, err)
		}
	}
}

func TestInstallTemplate_SkipsExistingFiles(t *testing.T) {
	tfs := buildTestFS()
	dest := t.TempDir()

	agentDir := filepath.Join(dest, "code")
	os.MkdirAll(agentDir, 0o700)
	os.WriteFile(filepath.Join(agentDir, "persona.md"), []byte("custom content"), 0o600)

	if err := agentdef.InstallTemplate(tfs, "code", dest); err != nil {
		t.Fatalf("InstallTemplate error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(agentDir, "persona.md"))
	if string(data) != "custom content" {
		t.Errorf("existing persona.md was overwritten, got: %q", string(data))
	}
}

func TestInstallTemplate_UnknownType(t *testing.T) {
	tfs := buildTestFS()
	dest := t.TempDir()

	err := agentdef.InstallTemplate(tfs, "nonexistent", dest)
	if err == nil {
		t.Error("expected error for unknown template type, got nil")
	}
}
