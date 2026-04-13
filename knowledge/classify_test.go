package knowledge

import (
	"context"
	"testing"

	"github.com/XHao/claw-go/provider"
)

// stubClassifyProvider returns a fixed JSON response for Complete calls.
type stubClassifyProvider struct {
	response string
}

func (s *stubClassifyProvider) Complete(ctx context.Context, msgs []provider.Message) (string, error) {
	return s.response, nil
}

func (s *stubClassifyProvider) CompleteWithTools(ctx context.Context, msgs []provider.Message, tools []provider.ToolDef) (provider.CompleteResult, error) {
	return provider.CompleteResult{Content: s.response}, nil
}

func TestTaskClassifier_ReturnsTags(t *testing.T) {
	stub := &stubClassifyProvider{
		response: `{"tags":["debug","golang"]}`,
	}
	c := NewTaskClassifier(stub)
	tags, err := c.Classify(context.Background(), "我的 goroutine 泄漏了，怎么排查")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) == 0 {
		t.Fatal("want tags, got none")
	}
	found := false
	for _, tag := range tags {
		if tag == "debug" || tag == "golang" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected debug or golang in tags, got %v", tags)
	}
}

func TestTaskClassifier_HandlesInvalidJSON(t *testing.T) {
	stub := &stubClassifyProvider{response: "not json"}
	c := NewTaskClassifier(stub)
	tags, err := c.Classify(context.Background(), "hello")
	if err != nil {
		t.Fatal("should not error on invalid JSON, got:", err)
	}
	if tags == nil {
		tags = []string{}
	}
	_ = tags
}

func TestTaskClassifier_HandlesEmptyTags(t *testing.T) {
	stub := &stubClassifyProvider{response: `{"tags":[]}`}
	c := NewTaskClassifier(stub)
	tags, err := c.Classify(context.Background(), "你好")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 0 {
		t.Errorf("expected empty tags for empty-tags response, got %v", tags)
	}
}

func TestTaskClassifier_EmptyMessageReturnsNil(t *testing.T) {
	stub := &stubClassifyProvider{response: `{"tags":["debug"]}`}
	c := NewTaskClassifier(stub)
	tags, err := c.Classify(context.Background(), "   ")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 0 {
		t.Errorf("expected empty tags for empty message, got %v", tags)
	}
}
