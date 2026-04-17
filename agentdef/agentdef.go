// Package agentdef loads and manages Persona Agent definitions from
// ~/.claw/agents/<name>/ directories.
package agentdef

import (
	"os"
	"path/filepath"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/XHao/claw-go/knowledge"
)

// AgentDef holds the loaded configuration for a single Persona Agent.
type AgentDef struct {
	Name        string   // directory name, e.g. "lawyer"
	Persona     string   // contents of persona.md (system prompt suffix)
	ExtraTools  []string // tool names from tools.yaml extra_tools
	Dir         string   // absolute path to the agent directory
	Experiences *knowledge.ExperienceStore // rooted at Dir/experiences/
	Procedures  *knowledge.ProcedureStore  // rooted at Dir/procedures/
}

// toolsFile is the YAML schema for agents/<name>/tools.yaml.
type toolsFile struct {
	ExtraTools []string `yaml:"extra_tools"`
}

// Registry holds all loaded AgentDef entries keyed by name.
type Registry struct {
	mu   sync.RWMutex
	defs map[string]*AgentDef
}

// LoadRegistry scans agentsDir for subdirectories, loads each as an AgentDef,
// and returns a Registry. agentsDir not existing is not an error (returns empty registry).
func LoadRegistry(agentsDir string) (*Registry, error) {
	reg := &Registry{defs: make(map[string]*AgentDef)}

	entries, err := os.ReadDir(agentsDir)
	if os.IsNotExist(err) {
		return reg, nil
	}
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		def, err := loadAgentDef(agentsDir, name)
		if err != nil {
			continue // skip malformed agent dirs silently
		}
		reg.defs[name] = def
	}
	return reg, nil
}

// loadAgentDef loads a single AgentDef from agentsDir/name/.
func loadAgentDef(agentsDir, name string) (*AgentDef, error) {
	dir := filepath.Join(agentsDir, name)
	def := &AgentDef{Name: name, Dir: dir}

	// Load persona.md (optional).
	if data, err := os.ReadFile(filepath.Join(dir, "persona.md")); err == nil {
		def.Persona = string(data)
	}

	// Load tools.yaml (optional).
	if data, err := os.ReadFile(filepath.Join(dir, "tools.yaml")); err == nil {
		var tf toolsFile
		if yaml.Unmarshal(data, &tf) == nil {
			def.ExtraTools = tf.ExtraTools
		}
	}

	// Initialize knowledge stores rooted at the agent directory.
	def.Experiences = knowledge.NewExperienceStore(filepath.Join(dir, "experiences"))
	def.Procedures = knowledge.NewProcedureStore(filepath.Join(dir, "procedures"))

	return def, nil
}

// Get returns the AgentDef for name, or false if not found.
func (r *Registry) Get(name string) (*AgentDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.defs[name]
	return d, ok
}

// List returns all agent names in sorted order.
func (r *Registry) List() []string {
	r.mu.RLock()
	names := make([]string, 0, len(r.defs))
	for n := range r.defs {
		names = append(names, n)
	}
	r.mu.RUnlock()
	sort.Strings(names)
	return names
}

// IsMultiAgent reports whether more than one agent is registered.
// When false, /agent commands are suppressed and UI shows no agent label.
func (r *Registry) IsMultiAgent() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.defs) > 1
}

// Create initialises a new agent directory with a persona.md template and memory/ subdir.
// Also reloads the new agent into the registry.
func (r *Registry) Create(agentsDir, name string) error {
	dir := filepath.Join(agentsDir, name)
	for _, sub := range []string{"memory", "skills", "experiences", "procedures"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return err
		}
	}
	personaPath := filepath.Join(dir, "persona.md")
	if _, err := os.Stat(personaPath); os.IsNotExist(err) {
		template := "# " + name + "\n\n在此描述该 Agent 的角色和专长。\n"
		if err := os.WriteFile(personaPath, []byte(template), 0o600); err != nil {
			return err
		}
	}
	// Reload into registry.
	def, err := loadAgentDef(agentsDir, name)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.defs[name] = def
	r.mu.Unlock()
	return nil
}
