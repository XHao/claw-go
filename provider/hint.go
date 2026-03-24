package provider

import (
	"context"
	"fmt"
)

// ModelHint is a routing hint that callers attach to a context to request a
// specific capability tier from a RouterProvider.
//
// When no hint is set (or the hint is ModelHintDefault), RouterProvider falls
// back to ModelHintTask — the general-purpose mid-tier model.
type ModelHint string

const (
	// ModelHintDefault means "no preference" — RouterProvider uses task tier.
	ModelHintDefault ModelHint = ""
	// ModelHintRouter selects the Tier-1 model (ultra-cheap intent classifier).
	// Use this for skill dispatch and other pure classification work.
	ModelHintRouter ModelHint = "router"
	// ModelHintTask selects the Tier-2 general task model (default channel).
	// Normal conversation, code generation, file edits.
	ModelHintTask ModelHint = "task"
	// ModelHintSummary selects the Tier-2 summarisation model.
	// Use this for knowledge distillation, log triage, map-reduce batches.
	ModelHintSummary ModelHint = "summary"
	// ModelHintThinking selects the Tier-3 deep-reasoning model.
	// Reserve for complex architecture design, multi-file refactors, hard debug.
	ModelHintThinking ModelHint = "thinking"
)

// HintSource is a typed source label that identifies which code path initiated
// an LLM call.  Using a named type (rather than a bare string) gives compile-time
// safety — new call sites must use the declared constants or builder functions
// below, preventing label drift across metrics, debug logs, and future
// observability tooling.
//
// Schema (all current labels):
//
//	Static:
//	  HintSourceAgentThink        "agent/think"
//	  HintSourceAgentInject       "agent/inject"
//	  HintSourceAutorouteClassify "autoroute/classify"
//	  HintSourceDistillReduce     "distill/reduce"
//
//	Dynamic (use builder functions):
//	  HintSourceAgentLoop(i)        "agent/loop[i=<i>]"
//	  HintSourceDistillMap(i,total) "distill/map[<i>/<total>]"
type HintSource string

// Static HintSource constants — each maps to a fixed code path.
const (
	HintSourceAgentThink        HintSource = "agent/think"
	HintSourceAgentInject       HintSource = "agent/inject"
	HintSourceAutorouteClassify HintSource = "autoroute/classify"
	HintSourceDistillReduce     HintSource = "distill/reduce"
)

// HintSourceAgentLoop returns the source label for the i-th iteration of the
// agent's tool-calling loop.
func HintSourceAgentLoop(i int) HintSource {
	return HintSource(fmt.Sprintf("agent/loop[i=%d]", i))
}

// HintSourceDistillMap returns the source label for the i-th map chunk out of
// total during a map-reduce distillation pass.
func HintSourceDistillMap(i, total int) HintSource {
	return HintSource(fmt.Sprintf("distill/map[%d/%d]", i, total))
}

type hintKey struct{}
type sourceKey struct{}

// WithModelHint attaches a ModelHint to ctx so RouterProvider can route the
// call to the appropriate tier.
func WithModelHint(ctx context.Context, hint ModelHint) context.Context {
	return context.WithValue(ctx, hintKey{}, hint)
}

// HintFromContext reads the ModelHint previously set by WithModelHint.
// Returns ModelHintDefault if no hint is present.
func HintFromContext(ctx context.Context) ModelHint {
	h, _ := ctx.Value(hintKey{}).(ModelHint)
	return h
}

// WithHintSource attaches a HintSource label to ctx.  The label is recorded in
// metrics and debug logs alongside the ModelHint.  Callers must use the typed
// constants or builder functions defined in this file.
func WithHintSource(ctx context.Context, source HintSource) context.Context {
	return context.WithValue(ctx, sourceKey{}, source)
}

// SourceFromContext reads the source label set by WithHintSource.
// Returns an empty string if none was set.
func SourceFromContext(ctx context.Context) string {
	s, _ := ctx.Value(sourceKey{}).(HintSource)
	return string(s)
}

type noFallbackKey struct{}

// withNoFallback marks ctx so that FallbackProvider will not attempt its
// fallback path if the primary call fails.  Used by AutoRouter so that a 429
// or transient error on the cheap routing-tier model does not silently escalate
// classification to the expensive task model.
func withNoFallback(ctx context.Context) context.Context {
	return context.WithValue(ctx, noFallbackKey{}, true)
}

// noFallbackFromContext reports whether the no-fallback flag is set on ctx.
func noFallbackFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(noFallbackKey{}).(bool)
	return v
}
