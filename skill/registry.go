package skill

import (
	"context"

	"github.com/XHao/claw-go/provider"
)

// HandlerFunc is the runtime signature for a skill.
type HandlerFunc func(
	ctx context.Context,
	args map[string]string,
	sessionKey string,
	progress func(string),
) (string, error)

// Def describes one registered skill.
type Def struct {
	provider.ToolDef
	Handler HandlerFunc
}

// Registry is an ordered collection of skill definitions.
type Registry struct {
	defs   []*Def
	byName map[string]*Def
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]*Def)}
}

// Register adds a Def to the Registry. Panics on duplicate names.
func (r *Registry) Register(d *Def) {
	if _, dup := r.byName[d.Name]; dup {
		panic("skill: duplicate name: " + d.Name)
	}
	r.defs = append(r.defs, d)
	r.byName[d.Name] = d
}

// Get looks up a skill by name.
func (r *Registry) Get(name string) (*Def, bool) {
	d, ok := r.byName[name]
	return d, ok
}

// Has reports whether a skill with the given name is registered.
func (r *Registry) Has(name string) bool {
	_, ok := r.byName[name]
	return ok
}

// AsToolDefs returns provider.ToolDef slice for CompleteWithTools.
func (r *Registry) AsToolDefs() []provider.ToolDef {
	out := make([]provider.ToolDef, len(r.defs))
	for i, d := range r.defs {
		out[i] = d.ToolDef
	}
	return out
}

// Names returns all registered skill names.
func (r *Registry) Names() []string {
	out := make([]string, len(r.defs))
	for i, d := range r.defs {
		out[i] = d.Name
	}
	return out
}
