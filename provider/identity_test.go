package provider

import (
	"context"
	"testing"
)

type stubIdentityProvider struct {
	result CompleteResult
}

func (s *stubIdentityProvider) Complete(ctx context.Context, messages []Message) (string, error) {
	return s.result.Content, nil
}

func (s *stubIdentityProvider) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDef) (CompleteResult, error) {
	return s.result, nil
}

func TestWrapIdentitySetsModelKey(t *testing.T) {
	inner := &stubIdentityProvider{result: CompleteResult{Model: ModelMeta{Model: "gpt-4o-mini"}}}
	p := WrapIdentity(inner, "task_model")
	res, err := p.CompleteWithTools(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Model.ModelKey != "task_model" {
		t.Fatalf("want model key task_model, got %q", res.Model.ModelKey)
	}
	if res.Model.Model != "gpt-4o-mini" {
		t.Fatalf("want model gpt-4o-mini, got %q", res.Model.Model)
	}
}
