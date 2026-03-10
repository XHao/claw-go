package provider

import "context"

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

type hintKey struct{}

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
