package channel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestWeixinHandleMessage_TextOnly verifies that text messages are dispatched
// and non-text / bot messages are ignored.
func TestWeixinHandleMessage_TextOnly(t *testing.T) {
	ch := NewWeixinChannel("test", "", nil)

	var dispatched []InboundMessage
	var mu sync.Mutex
	dispatch := func(_ context.Context, msg InboundMessage) {
		mu.Lock()
		dispatched = append(dispatched, msg)
		mu.Unlock()
	}

	ctx := context.Background()

	// Text message (message_type=1, item type=1) — should dispatch.
	textMsg := weixinInboundMsg{
		FromUserID:   "alice@im.wechat",
		MessageType:  1,
		ContextToken: "ctx-abc",
		ItemList: []struct {
			Type     int `json:"type"`
			TextItem struct {
				Text string `json:"text"`
			} `json:"text_item"`
		}{
			{Type: 1, TextItem: struct{ Text string `json:"text"` }{"hello world"}},
		},
	}
	if err := ch.handleMessage(ctx, textMsg, dispatch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // let goroutine run

	mu.Lock()
	if len(dispatched) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(dispatched))
	}
	if dispatched[0].Text != "hello world" {
		t.Errorf("text mismatch: got %q", dispatched[0].Text)
	}
	if dispatched[0].ChatID != "alice@im.wechat" {
		t.Errorf("ChatID mismatch: got %q", dispatched[0].ChatID)
	}
	mu.Unlock()

	// Bot reply (message_type=2) — should NOT dispatch.
	botMsg := weixinInboundMsg{
		FromUserID:  "bot@im.bot",
		MessageType: 2,
		ItemList: []struct {
			Type     int `json:"type"`
			TextItem struct {
				Text string `json:"text"`
			} `json:"text_item"`
		}{
			{Type: 1, TextItem: struct{ Text string `json:"text"` }{"I am bot"}},
		},
	}
	if err := ch.handleMessage(ctx, botMsg, dispatch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	if len(dispatched) != 1 {
		t.Errorf("expected still 1 dispatch after bot message, got %d", len(dispatched))
	}
	mu.Unlock()
}

// TestWeixinSend_UsesStoredSession verifies that Send looks up the session and
// posts to the correct endpoint with context_token.
func TestWeixinSend_UsesStoredSession(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ret":0}`))
	}))
	defer srv.Close()

	ch := NewWeixinChannel("test", "", nil)
	ch.botToken = "tok-123"
	ch.baseURL = srv.URL

	// Pre-populate session.
	ch.mu.Lock()
	ch.sessions["alice@im.wechat"] = weixinSession{
		fromUserID:   "alice@im.wechat",
		contextToken: "ctx-xyz",
	}
	ch.mu.Unlock()

	err := ch.Send(context.Background(), OutboundMessage{
		ChatID: "alice@im.wechat",
		Text:   "hi there",
	})
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}

	msg, ok := received["msg"].(map[string]any)
	if !ok {
		t.Fatalf("missing 'msg' in request body: %v", received)
	}
	if msg["to_user_id"] != "alice@im.wechat" {
		t.Errorf("to_user_id mismatch: %v", msg["to_user_id"])
	}
	if msg["context_token"] != "ctx-xyz" {
		t.Errorf("context_token mismatch: %v", msg["context_token"])
	}
	items, _ := msg["item_list"].([]any)
	if len(items) == 0 {
		t.Fatal("item_list is empty")
	}
	item := items[0].(map[string]any)
	textItem, _ := item["text_item"].(map[string]any)
	if textItem["text"] != "hi there" {
		t.Errorf("text mismatch: %v", textItem["text"])
	}
}

// TestWeixinSend_IgnoresDelta verifies streaming deltas are silently dropped.
func TestWeixinSend_IgnoresDelta(t *testing.T) {
	ch := NewWeixinChannel("test", "", nil)
	ch.botToken = "tok"
	ch.baseURL = "http://should-not-be-called"

	err := ch.Send(context.Background(), OutboundMessage{
		ChatID: "alice@im.wechat",
		Delta:  "partial",
	})
	if err != nil {
		t.Errorf("expected no error for delta, got: %v", err)
	}
}

// TestWeixinEnsureToken_LoadsFromFile verifies that a valid token file is read
// and used without triggering QR login.
func TestWeixinEnsureToken_LoadsFromFile(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "weixin-token.json")

	td := weixinTokenData{BotToken: "saved-token", BaseURL: "https://example.com"}
	data, _ := json.Marshal(td)
	os.WriteFile(tokenFile, data, 0o600)

	ch := NewWeixinChannel("test", tokenFile, nil)
	if err := ch.ensureToken(context.Background()); err != nil {
		t.Fatalf("ensureToken error: %v", err)
	}
	if ch.botToken != "saved-token" {
		t.Errorf("botToken mismatch: got %q", ch.botToken)
	}
	if ch.baseURL != "https://example.com" {
		t.Errorf("baseURL mismatch: got %q", ch.baseURL)
	}
}

// TestWeixinGetUpdates_ParsesMessages verifies getUpdates parses the response correctly.
func TestWeixinGetUpdates_ParsesMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ret": 0,
			"msgs": []map[string]any{
				{
					"from_user_id":  "bob@im.wechat",
					"message_type":  1,
					"context_token": "ctx-bob",
					"item_list": []map[string]any{
						{"type": 1, "text_item": map[string]string{"text": "hello"}},
					},
				},
			},
			"get_updates_buf": "cursor-001",
		})
	}))
	defer srv.Close()

	ch := NewWeixinChannel("test", "", nil)
	ch.botToken = "tok"
	ch.baseURL = srv.URL

	client := &http.Client{Timeout: 5 * time.Second}
	msgs, cursor, err := ch.getUpdates(context.Background(), client, "")
	if err != nil {
		t.Fatalf("getUpdates error: %v", err)
	}
	if cursor != "cursor-001" {
		t.Errorf("cursor mismatch: got %q", cursor)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].FromUserID != "bob@im.wechat" {
		t.Errorf("FromUserID mismatch: %q", msgs[0].FromUserID)
	}
	if msgs[0].ContextToken != "ctx-bob" {
		t.Errorf("ContextToken mismatch: %q", msgs[0].ContextToken)
	}
}
