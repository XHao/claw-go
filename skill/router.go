package skill

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/XHao/claw-go/provider"
)

// Router executes skills server-side.
type Router struct {
	registry *Registry
}

// NewRouter creates a Router backed by the given Registry.
func NewRouter(reg *Registry) *Router {
	return &Router{registry: reg}
}

// Has reports whether name is a known skill (not a client-side tool).
func (r *Router) Has(name string) bool {
	return r.registry.Has(name)
}

// AsToolDefs returns all skill definitions to be merged into each LLM call.
func (r *Router) AsToolDefs() []provider.ToolDef {
	return r.registry.AsToolDefs()
}

// Names returns all registered skill names for routing classification.
func (r *Router) Names() []string {
	return r.registry.Names()
}

// Execute runs a skill by name with the provided JSON arguments.
func (r *Router) Execute(
	ctx context.Context,
	name, argsJSON, sessionKey string,
	progress func(string),
) (string, error) {
	def, ok := r.registry.Get(name)
	if !ok {
		return "", fmt.Errorf("skill %q not found", name)
	}
	if progress == nil {
		progress = func(string) {}
	}
	args := parseArgsJSON(argsJSON)
	return def.Handler(ctx, args, sessionKey, progress)
}

// parseArgsJSON decodes a JSON object into a flat string map.
func parseArgsJSON(argsJSON string) map[string]string {
	out := make(map[string]string)
	if argsJSON == "" {
		return out
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return out
	}
	for k, v := range raw {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			out[k] = s
		} else {
			out[k] = string(v)
		}
	}
	return out
}
