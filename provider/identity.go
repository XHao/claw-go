package provider

import "context"

// IdentityProvider decorates any Provider with a logical model key.
// This key comes from config.models and is useful for per-model observability.
type IdentityProvider struct {
	inner    Provider
	modelKey string
}

// WrapIdentity wraps inner and stamps the result with modelKey.
func WrapIdentity(inner Provider, modelKey string) Provider {
	if inner == nil {
		return nil
	}
	return &IdentityProvider{inner: inner, modelKey: modelKey}
}

func (p *IdentityProvider) Complete(ctx context.Context, messages []Message) (string, error) {
	result, err := p.CompleteWithTools(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

func (p *IdentityProvider) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDef) (CompleteResult, error) {
	result, err := p.inner.CompleteWithTools(ctx, messages, tools)
	if result.Model.ModelKey == "" {
		result.Model.ModelKey = p.modelKey
	}
	return result, err
}
