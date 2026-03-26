package provider

import "context"

// StreamFunc is a callback that receives incremental text content deltas
// during a streaming LLM call.  It is invoked from within CompleteWithTools
// each time a new text chunk arrives from the upstream API.
//
// The concrete provider (e.g. OpenAIProvider) checks the context for this
// callback.  When present, it uses streaming transport (SSE) and pushes
// deltas through the function while still accumulating the full response
// into the returned CompleteResult.
//
// All decorator wrappers (Observe, Metrics, Debug, …) are unaware of
// streaming — they see the normal synchronous CompleteResult return.
type StreamFunc func(delta string)

type streamKey struct{}

// WithStreamFunc attaches a StreamFunc to ctx.  The innermost concrete
// Provider checks for this to decide whether to enable streaming.
func WithStreamFunc(ctx context.Context, fn StreamFunc) context.Context {
	return context.WithValue(ctx, streamKey{}, fn)
}

// streamFuncFromContext retrieves the StreamFunc, or nil if not set.
func streamFuncFromContext(ctx context.Context) StreamFunc {
	fn, _ := ctx.Value(streamKey{}).(StreamFunc)
	return fn
}
