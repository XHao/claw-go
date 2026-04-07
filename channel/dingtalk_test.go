package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// isContextError reports whether err is or wraps a context cancellation/deadline error.
func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// TestDingTalkHandleCallback_AcksBeforeDispatch verifies that the ACK is sent
// immediately and the message is dispatched to the agent.
func TestDingTalkHandleCallback_AcksBeforeDispatch(t *testing.T) {
	ch := NewDingTalkChannel("test", "key", "secret", nil)

	var dispatched []InboundMessage
	var mu sync.Mutex
	dispatch := func(_ context.Context, msg InboundMessage) {
		mu.Lock()
		dispatched = append(dispatched, msg)
		mu.Unlock()
	}

	// Capture ACK sent back over the WebSocket mock.
	var sentFrames []streamAck
	var wsMu sync.Mutex
	mockSend := func(ack streamAck) {
		wsMu.Lock()
		sentFrames = append(sentFrames, ack)
		wsMu.Unlock()
	}

	msg := dingTalkMessage{
		MsgType:        "text",
		Text:           struct{ Content string `json:"content"` }{"hello dingtalk"},
		SenderID:       "user-001",
		SenderStaffID:  "staff-001",
		SenderNick:     "Alice",
		ConversationID: "conv-001",
		SessionWebhook: "https://example.com/webhook",
	}
	data, _ := json.Marshal(msg)

	frame := streamFrame{
		Type:    "CALLBACK",
		Headers: map[string]string{"topic": topicRobot, "messageId": "msg-001"},
		Data:    string(data),
	}

	// Use the internal ackFunc-based helper to avoid needing a real WebSocket.
	if err := ch.handleCallbackWithAck(context.Background(), mockSend, frame, dispatch); err != nil {
		t.Fatalf("handleCallbackWithAck error: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	// ACK must have been sent.
	wsMu.Lock()
	if len(sentFrames) != 1 {
		t.Fatalf("expected 1 ACK, got %d", len(sentFrames))
	}
	if sentFrames[0].Code != 200 {
		t.Errorf("ACK code mismatch: got %d", sentFrames[0].Code)
	}
	if sentFrames[0].Headers["messageId"] != "msg-001" {
		t.Errorf("ACK messageId mismatch: got %q", sentFrames[0].Headers["messageId"])
	}
	wsMu.Unlock()

	// Message must have been dispatched.
	mu.Lock()
	if len(dispatched) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(dispatched))
	}
	if dispatched[0].Text != "hello dingtalk" {
		t.Errorf("text mismatch: got %q", dispatched[0].Text)
	}
	if dispatched[0].Username != "Alice" {
		t.Errorf("Username mismatch: got %q", dispatched[0].Username)
	}
	mu.Unlock()
}

// TestDingTalkHandleCallback_NonTextIgnored verifies non-text messages are dropped.
func TestDingTalkHandleCallback_NonTextIgnored(t *testing.T) {
	ch := NewDingTalkChannel("test", "key", "secret", nil)

	var dispatched []InboundMessage
	dispatch := func(_ context.Context, msg InboundMessage) {
		dispatched = append(dispatched, msg)
	}

	msg := dingTalkMessage{
		MsgType:        "image",
		ConversationID: "conv-002",
		SessionWebhook: "https://example.com/webhook",
	}
	data, _ := json.Marshal(msg)

	frame := streamFrame{
		Type:    "CALLBACK",
		Headers: map[string]string{"topic": topicRobot, "messageId": "msg-002"},
		Data:    string(data),
	}

	if err := ch.handleCallbackWithAck(context.Background(), func(streamAck) {}, frame, dispatch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	if len(dispatched) != 0 {
		t.Errorf("expected 0 dispatches for image message, got %d", len(dispatched))
	}
}

// TestDingTalkHandleCallback_SessionKeyFallback verifies that when ConversationID
// is empty, sessionKey falls back to SenderStaffID, and UserID is consistent.
func TestDingTalkHandleCallback_SessionKeyFallback(t *testing.T) {
	ch := NewDingTalkChannel("test", "key", "secret", nil)

	var dispatched []InboundMessage
	var mu sync.Mutex
	dispatch := func(_ context.Context, msg InboundMessage) {
		mu.Lock()
		dispatched = append(dispatched, msg)
		mu.Unlock()
	}

	// No ConversationID — should fall back to SenderStaffID.
	msg := dingTalkMessage{
		MsgType:        "text",
		Text:           struct{ Content string `json:"content"` }{"fallback test"},
		SenderID:       "sender-raw",
		SenderStaffID:  "staff-002",
		SenderNick:     "Bob",
		ConversationID: "",
		SessionWebhook: "https://example.com/webhook",
	}
	data, _ := json.Marshal(msg)

	frame := streamFrame{
		Type:    "CALLBACK",
		Headers: map[string]string{"topic": topicRobot, "messageId": "msg-003"},
		Data:    string(data),
	}

	if err := ch.handleCallbackWithAck(context.Background(), func(streamAck) {}, frame, dispatch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(dispatched) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(dispatched))
	}
	d := dispatched[0]
	if d.SessionKey != "staff-002" {
		t.Errorf("SessionKey: want %q got %q", "staff-002", d.SessionKey)
	}
	// UserID must match the resolved sessionKey's sender, not be empty.
	if d.UserID == "" {
		t.Errorf("UserID must not be empty when sessionKey falls back to SenderStaffID")
	}
	if d.UserID != "staff-002" {
		t.Errorf("UserID: want %q got %q", "staff-002", d.UserID)
	}
}

// TestDingTalkGetAccessToken_CachesToken verifies that a second call within the
// validity window does NOT make another HTTP request.
func TestDingTalkGetAccessToken_CachesToken(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"accessToken": "tok-cached",
			"expireIn":    7200,
		})
	}))
	defer srv.Close()

	ch := NewDingTalkChannel("test", "key", "secret", nil)
	ch.tokenURL = srv.URL

	ctx := context.Background()
	tok1, err := ch.getAccessToken(ctx)
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	tok2, err := ch.getAccessToken(ctx)
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}

	if tok1 != "tok-cached" || tok2 != "tok-cached" {
		t.Errorf("token mismatch: %q %q", tok1, tok2)
	}
	if calls != 1 {
		t.Errorf("expected 1 HTTP call (cache hit), got %d", calls)
	}
}

// TestDingTalkGetAccessToken_RefreshesExpired verifies that an expired token
// triggers a new HTTP request.
func TestDingTalkGetAccessToken_RefreshesExpired(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"accessToken": "tok-fresh",
			"expireIn":    7200,
		})
	}))
	defer srv.Close()

	ch := NewDingTalkChannel("test", "key", "secret", nil)
	ch.tokenURL = srv.URL
	// Pre-populate an expired token (expiry in the past).
	ch.cachedToken = "tok-old"
	ch.tokenExpiry = time.Now().Add(-1 * time.Minute)

	tok, err := ch.getAccessToken(context.Background())
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if tok != "tok-fresh" {
		t.Errorf("expected fresh token, got %q", tok)
	}
	if calls != 1 {
		t.Errorf("expected 1 HTTP call for refresh, got %d", calls)
	}
}

// TestDingTalkPostReply_HTTPError verifies that a non-2xx reply returns an error.
func TestDingTalkPostReply_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"errcode":500}`))
	}))
	defer srv.Close()

	ch := NewDingTalkChannel("test", "key", "secret", nil)
	err := ch.postReply(context.Background(), srv.URL+"/reply", "tok", "hello")
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}

// TestDingTalkRunOnce_RespectsPreCancelledContext verifies that runOnce returns
// quickly when the context is already cancelled before the gateway call.
func TestDingTalkRunOnce_RespectsPreCancelledContext(t *testing.T) {
	ch := NewDingTalkChannel("test", "key", "secret", nil)

	// Point gateway URL at a server that never responds (simulate slow network).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the test is done — simulates a hung gateway.
		time.Sleep(10 * time.Second)
	}))
	defer srv.Close()
	ch.gatewayURL = srv.URL

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	done := make(chan error, 1)
	go func() {
		done <- ch.runOnce(ctx, func(_ context.Context, _ InboundMessage) {})
	}()

	select {
	case err := <-done:
		// runOnce should return quickly with nil or a context-derived error.
		// Any non-context error would indicate it blocked past cancellation.
		if err != nil {
			// Unwrap to check the root cause is context cancellation.
			if !isContextError(err) {
				t.Errorf("unexpected non-context error: %v", err)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runOnce did not return within 2s after context cancellation")
	}
}

// TestMustMarshal_PanicsOnUnmarshalable verifies that mustMarshal panics for
// types that cannot be JSON-encoded (e.g. a channel value).
func TestMustMarshal_PanicsOnUnmarshalable(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for unmarshalable value, got none")
		}
	}()
	mustMarshal(map[string]any{"ch": make(chan int)})
}

// TestMustMarshal_ReturnsValidJSON verifies normal usage produces valid JSON.
func TestMustMarshal_ReturnsValidJSON(t *testing.T) {
	b := mustMarshal(map[string]string{"key": "value"})
	var out map[string]string
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if out["key"] != "value" {
		t.Errorf("unexpected value: %q", out["key"])
	}
}

// TestDingTalkHandleCallback_TrimsTextContent verifies that leading/trailing
// whitespace in the message text is stripped before dispatch (group @-mention
// messages often have a leading space).
func TestDingTalkHandleCallback_TrimsTextContent(t *testing.T) {
	ch := NewDingTalkChannel("test", "key", "secret", nil)

	var dispatched []InboundMessage
	var mu sync.Mutex
	dispatch := func(_ context.Context, msg InboundMessage) {
		mu.Lock()
		dispatched = append(dispatched, msg)
		mu.Unlock()
	}

	msg := dingTalkMessage{
		MsgID:          "trim-001",
		MsgType:        "text",
		Text:           struct{ Content string `json:"content"` }{"  hello world  "},
		ConversationID: "conv-trim",
		SessionWebhook: "https://example.com/wh",
	}
	data, _ := json.Marshal(msg)
	frame := streamFrame{
		Type:    "CALLBACK",
		Headers: map[string]string{"topic": topicRobot, "messageId": "proto-trim"},
		Data:    string(data),
	}

	if err := ch.handleCallbackWithAck(context.Background(), func(streamAck) {}, frame, dispatch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(dispatched) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(dispatched))
	}
	if dispatched[0].Text != "hello world" {
		t.Errorf("expected trimmed text %q, got %q", "hello world", dispatched[0].Text)
	}
}

// TestDingTalkDedup_ProtocolLayer verifies that a message with the same
// headers.messageId is dispatched only once even if delivered twice.
func TestDingTalkDedup_ProtocolLayer(t *testing.T) {
	ch := NewDingTalkChannel("test", "key", "secret", nil)

	var dispatched int
	var mu sync.Mutex
	dispatch := func(_ context.Context, _ InboundMessage) {
		mu.Lock()
		dispatched++
		mu.Unlock()
	}

	msg := dingTalkMessage{
		MsgID:          "biz-001",
		MsgType:        "text",
		Text:           struct{ Content string `json:"content"` }{"hello"},
		ConversationID: "conv-001",
		SessionWebhook: "https://example.com/wh",
	}
	data, _ := json.Marshal(msg)
	frame := streamFrame{
		Type:    "CALLBACK",
		Headers: map[string]string{"topic": topicRobot, "messageId": "proto-001"},
		Data:    string(data),
	}

	_ = ch.handleCallbackWithAck(context.Background(), func(streamAck) {}, frame, dispatch)
	_ = ch.handleCallbackWithAck(context.Background(), func(streamAck) {}, frame, dispatch) // duplicate
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if dispatched != 1 {
		t.Errorf("expected 1 dispatch (dedup), got %d", dispatched)
	}
}

// TestDingTalkDedup_BusinessLayer verifies that a server-retransmitted message
// (same data.msgId, different headers.messageId) is dispatched only once.
func TestDingTalkDedup_BusinessLayer(t *testing.T) {
	ch := NewDingTalkChannel("test", "key", "secret", nil)

	var dispatched int
	var mu sync.Mutex
	dispatch := func(_ context.Context, _ InboundMessage) {
		mu.Lock()
		dispatched++
		mu.Unlock()
	}

	msg := dingTalkMessage{
		MsgID:          "biz-002",
		MsgType:        "text",
		Text:           struct{ Content string `json:"content"` }{"hello"},
		ConversationID: "conv-002",
		SessionWebhook: "https://example.com/wh",
	}
	data, _ := json.Marshal(msg)

	// First delivery: proto-A
	frame1 := streamFrame{
		Type:    "CALLBACK",
		Headers: map[string]string{"topic": topicRobot, "messageId": "proto-A"},
		Data:    string(data),
	}
	// Server retransmit: same biz msgId, new proto messageId
	frame2 := streamFrame{
		Type:    "CALLBACK",
		Headers: map[string]string{"topic": topicRobot, "messageId": "proto-B"},
		Data:    string(data),
	}

	_ = ch.handleCallbackWithAck(context.Background(), func(streamAck) {}, frame1, dispatch)
	_ = ch.handleCallbackWithAck(context.Background(), func(streamAck) {}, frame2, dispatch)
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if dispatched != 1 {
		t.Errorf("expected 1 dispatch (biz dedup), got %d", dispatched)
	}
}

// TestDingTalkHeartbeat_ReconnectsOnSilentDrop verifies that runOnce returns
// (triggering reconnect) when the WebSocket receives no frames within the
// heartbeat timeout window.
func TestDingTalkHeartbeat_ReconnectsOnSilentDrop(t *testing.T) {
	ch := NewDingTalkChannel("test", "key", "secret", nil)
	// Use a very short timeout so the test finishes quickly.
	ch.heartbeatInterval = 50 * time.Millisecond
	ch.heartbeatTimeout = 150 * time.Millisecond

	// WS server: accepts connection but never sends frames (silent drop).
	wsConnected := make(chan struct{}, 1)
	wsServer := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		wsConnected <- struct{}{}
		// Block forever — simulates a silently dead connection.
		select {}
	}))
	defer wsServer.Close()

	// Gateway server returns the WS server URL as the endpoint.
	gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wsURL := "ws" + wsServer.URL[len("http"):]
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"endpoint":%q,"ticket":"t1"}`, wsURL)
	}))
	defer gatewayServer.Close()
	ch.gatewayURL = gatewayServer.URL

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- ch.runOnce(ctx, func(_ context.Context, _ InboundMessage) {})
	}()

	// Wait for WS connection to be established before asserting timeout.
	select {
	case <-wsConnected:
	case <-ctx.Done():
		t.Fatal("WS connection never established")
	}

	select {
	case err := <-done:
		// runOnce must return due to heartbeat timeout before ctx expires.
		if ctx.Err() != nil {
			t.Fatal("runOnce did not return within 3s — heartbeat timeout not implemented")
		}
		if err == nil {
			t.Error("expected non-nil error from heartbeat timeout")
		}
	case <-ctx.Done():
		t.Fatal("test timed out — heartbeat timeout not implemented")
	}
}

// TestDingTalkWebhooks_BoundedGrowth verifies that the webhooks map does not
// grow beyond maxSessions entries.
func TestDingTalkWebhooks_BoundedGrowth(t *testing.T) {
	ch := NewDingTalkChannel("test", "key", "secret", nil)

	ctx := context.Background()
	for i := range maxSessions + 100 {
		msg := dingTalkMessage{
			MsgType:        "text",
			Text:           struct{ Content string `json:"content"` }{Content: "hi"},
			SenderID:       fmt.Sprintf("user-%d", i),
			ConversationID: fmt.Sprintf("conv-%d", i),
			SessionWebhook: fmt.Sprintf("https://example.com/wh/%d", i),
		}
		data, _ := json.Marshal(msg)
		frame := streamFrame{
			Type:    "CALLBACK",
			Headers: map[string]string{"topic": topicRobot, "messageId": fmt.Sprintf("mid-%d", i)},
			Data:    string(data),
		}
		_ = ch.handleCallbackWithAck(ctx, func(streamAck) {}, frame, func(_ context.Context, _ InboundMessage) {})
	}

	ch.mu.RLock()
	size := len(ch.sessions)
	ch.mu.RUnlock()

	if size > maxSessions {
		t.Errorf("sessions map grew to %d, expected at most %d", size, maxSessions)
	}
}

// TestDingTalkSend_FirstDeltaCreatesCard verifies that the first Delta triggers
// card creation (POST /v1.0/card/instances) and delivery (POST .../deliver).
func TestDingTalkSend_FirstDeltaCreatesCard(t *testing.T) {
	var createCalled, deliverCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1.0/card/instances":
			if r.Method == http.MethodPost {
				createCalled = true
			}
			fmt.Fprint(w, `{"result":"ok"}`)
		case "/v1.0/card/instances/deliver":
			deliverCalled = true
			fmt.Fprint(w, `{"result":"ok"}`)
		case "/v1.0/card/streaming":
			fmt.Fprint(w, `{"result":"ok"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ch := NewDingTalkChannel("test", "key", "secret", nil)
	ch.cardInstancesURL = srv.URL + "/v1.0/card/instances"
	ch.cardDeliverURL = srv.URL + "/v1.0/card/instances/deliver"
	ch.cardStreamingURL = srv.URL + "/v1.0/card/streaming"
	ch.cachedToken = "tok"
	ch.tokenExpiry = time.Now().Add(time.Hour)
	ch.mu.Lock()
	ch.sessions["chat-1"] = dingtalkSession{
		webhookURL:       srv.URL + "/reply",
		conversationType: "1",
		senderStaffID:    "staff-1",
		robotCode:        "key",
	}
	ch.mu.Unlock()

	if err := ch.Send(context.Background(), OutboundMessage{ChatID: "chat-1", Delta: "hello"}); err != nil {
		t.Fatalf("Send error: %v", err)
	}

	if !createCalled {
		t.Error("expected card create POST, not called")
	}
	if !deliverCalled {
		t.Error("expected card deliver POST, not called")
	}
}

// TestDingTalkSend_SubsequentDeltasUpdateCard verifies that after the first
// Delta, subsequent Deltas call PUT /v1.0/card/streaming (not create again).
func TestDingTalkSend_SubsequentDeltasUpdateCard(t *testing.T) {
	streamCalls := 0
	createCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1.0/card/instances" && r.Method == http.MethodPost:
			createCalls++
			fmt.Fprint(w, `{"result":"ok"}`)
		case r.URL.Path == "/v1.0/card/instances/deliver":
			fmt.Fprint(w, `{"result":"ok"}`)
		case r.URL.Path == "/v1.0/card/instances" && r.Method == http.MethodPut:
			fmt.Fprint(w, `{"result":"ok"}`)
		case r.URL.Path == "/v1.0/card/streaming":
			streamCalls++
			fmt.Fprint(w, `{"result":"ok"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ch := NewDingTalkChannel("test", "key", "secret", nil)
	ch.cardInstancesURL = srv.URL + "/v1.0/card/instances"
	ch.cardDeliverURL = srv.URL + "/v1.0/card/instances/deliver"
	ch.cardStreamingURL = srv.URL + "/v1.0/card/streaming"
	ch.cachedToken = "tok"
	ch.tokenExpiry = time.Now().Add(time.Hour)
	ch.mu.Lock()
	ch.sessions["chat-2"] = dingtalkSession{webhookURL: srv.URL + "/reply", conversationType: "1", senderStaffID: "s2", robotCode: "key"}
	ch.mu.Unlock()

	for _, delta := range []string{"hello", " world", "!"} {
		if err := ch.Send(context.Background(), OutboundMessage{ChatID: "chat-2", Delta: delta}); err != nil {
			t.Fatalf("Send delta error: %v", err)
		}
	}

	if createCalls != 1 {
		t.Errorf("expected 1 card create, got %d", createCalls)
	}
	if streamCalls != 3 {
		t.Errorf("expected 3 streaming PUTs, got %d", streamCalls)
	}
}

// TestDingTalkSend_FinalTextFinishesCard verifies that a final Text message
// calls streaming with isFinalize=true and then sets FINISHED status.
func TestDingTalkSend_FinalTextFinishesCard(t *testing.T) {
	var finalizeBody map[string]any
	var finishedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/v1.0/card/instances" && r.Method == http.MethodPost:
			fmt.Fprint(w, `{"result":"ok"}`)
		case r.URL.Path == "/v1.0/card/instances/deliver":
			fmt.Fprint(w, `{"result":"ok"}`)
		case r.URL.Path == "/v1.0/card/instances" && r.Method == http.MethodPut:
			json.Unmarshal(body, &finishedBody)
			fmt.Fprint(w, `{"result":"ok"}`)
		case r.URL.Path == "/v1.0/card/streaming":
			json.Unmarshal(body, &finalizeBody)
			fmt.Fprint(w, `{"result":"ok"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ch := NewDingTalkChannel("test", "key", "secret", nil)
	ch.cardInstancesURL = srv.URL + "/v1.0/card/instances"
	ch.cardDeliverURL = srv.URL + "/v1.0/card/instances/deliver"
	ch.cardStreamingURL = srv.URL + "/v1.0/card/streaming"
	ch.cachedToken = "tok"
	ch.tokenExpiry = time.Now().Add(time.Hour)
	ch.mu.Lock()
	ch.sessions["chat-3"] = dingtalkSession{webhookURL: srv.URL + "/reply", conversationType: "1", senderStaffID: "s3", robotCode: "key"}
	ch.mu.Unlock()

	ch.Send(context.Background(), OutboundMessage{ChatID: "chat-3", Delta: "hello"})
	if err := ch.Send(context.Background(), OutboundMessage{ChatID: "chat-3", Text: "hello world"}); err != nil {
		t.Fatalf("Send text error: %v", err)
	}

	if isFinalize, _ := finalizeBody["isFinalize"].(bool); !isFinalize {
		t.Errorf("expected isFinalize=true in streaming PUT, got body: %v", finalizeBody)
	}
	cardData, _ := finishedBody["cardData"].(map[string]any)
	paramMap, _ := cardData["cardParamMap"].(map[string]any)
	if paramMap["flowStatus"] != "3" {
		t.Errorf("expected flowStatus=3 in FINISHED PUT, got: %v", paramMap["flowStatus"])
	}
}

// TestDingTalkSend_CardCreateFailureFallsBackToText verifies that when card
// creation fails, the final Text falls back to a plain postReply.
func TestDingTalkSend_CardCreateFailureFallsBackToText(t *testing.T) {
	var replyCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1.0/card/instances" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":"fail"}`)
		case r.URL.Path == "/reply":
			replyCalled = true
			fmt.Fprint(w, `{"errcode":0}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ch := NewDingTalkChannel("test", "key", "secret", nil)
	ch.cardInstancesURL = srv.URL + "/v1.0/card/instances"
	ch.cardDeliverURL = srv.URL + "/v1.0/card/instances/deliver"
	ch.cardStreamingURL = srv.URL + "/v1.0/card/streaming"
	ch.cachedToken = "tok"
	ch.tokenExpiry = time.Now().Add(time.Hour)
	ch.mu.Lock()
	ch.sessions["chat-4"] = dingtalkSession{webhookURL: srv.URL + "/reply", conversationType: "1", senderStaffID: "s4", robotCode: "key"}
	ch.mu.Unlock()

	ch.Send(context.Background(), OutboundMessage{ChatID: "chat-4", Delta: "hello"})
	if err := ch.Send(context.Background(), OutboundMessage{ChatID: "chat-4", Text: "hello world"}); err != nil {
		t.Fatalf("Send text error: %v", err)
	}

	if !replyCalled {
		t.Error("expected fallback postReply, not called")
	}
}

// TestDingTalkMarkdownTableFix verifies that a blank line is inserted before a
// Markdown table when the preceding line is non-empty and not itself a table row.
func TestDingTalkMarkdownTableFix(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "inserts blank line before table",
			input: "intro\n| a | b |\n|---|---|\n| 1 | 2 |",
			want:  "intro\n\n| a | b |\n|---|---|\n| 1 | 2 |",
		},
		{
			name:  "no change when blank line already present",
			input: "intro\n\n| a | b |\n|---|---|\n| 1 | 2 |",
			want:  "intro\n\n| a | b |\n|---|---|\n| 1 | 2 |",
		},
		{
			name:  "no change for plain text",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "table at start of string unchanged",
			input: "| a | b |\n|---|---|\n| 1 | 2 |",
			want:  "| a | b |\n|---|---|\n| 1 | 2 |",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fixMarkdownTables(tc.input)
			if got != tc.want {
				t.Errorf("fixMarkdownTables(%q)\n got  %q\n want %q", tc.input, got, tc.want)
			}
		})
	}
}
