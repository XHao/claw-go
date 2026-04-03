// Package channel - DingTalk (钉钉) Stream channel.
//
// Uses DingTalk's Stream API (WebSocket long connection) — no public HTTP server needed.
// The bot connects to DingTalk's gateway, receives messages via CALLBACK frames,
// and replies via the sessionWebhook URL embedded in each inbound message.
//
// Protocol (dingtalk-stream SDK v2):
//  1. POST /v1.0/gateway/connections/open → {endpoint, ticket}
//  2. Connect WebSocket to endpoint?ticket=<ticket>
//  3. Receive SYSTEM/CALLBACK/EVENT frames
//  4. ACK CALLBACK frames: {"code":200,"headers":{"contentType":"application/json","messageId":"..."},"message":"OK","data":"{}"}
//  5. Respond to SYSTEM ping frames immediately
//  6. Reply to user via sessionWebhook + access token
//
// Credentials: clientId (AppKey) + clientSecret (AppSecret) from
// https://open.dingtalk.com/ → your app → AppKey and AppSecret.
package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

// DingTalkChannel implements Channel using DingTalk's Stream (WebSocket) API.
type DingTalkChannel struct {
	id           string
	clientID     string // AppKey
	clientSecret string // AppSecret
	log          *slog.Logger

	running atomic.Bool

	mu       sync.RWMutex
	webhooks map[string]string // sessionKey → sessionWebhook URL

	tokenMu     sync.Mutex
	cachedToken string
	tokenExpiry time.Time
}

// NewDingTalkChannel creates a DingTalkChannel.
// clientID is the DingTalk AppKey; clientSecret is the AppSecret.
func NewDingTalkChannel(id, clientID, clientSecret string, log *slog.Logger) *DingTalkChannel {
	if log == nil {
		log = slog.Default()
	}
	return &DingTalkChannel{
		id:           id,
		clientID:     clientID,
		clientSecret: clientSecret,
		log:          log,
		webhooks:     make(map[string]string),
	}
}

// ID returns the unique channel identifier.
func (d *DingTalkChannel) ID() string { return "dingtalk:" + d.id }

// Status returns the current health of the channel.
func (d *DingTalkChannel) Status() Status {
	return Status{
		ID:      d.ID(),
		Type:    "dingtalk",
		Name:    d.id,
		Running: d.running.Load(),
	}
}

// Start connects to DingTalk Stream and dispatches inbound messages until ctx is cancelled.
// Reconnects automatically with exponential back-off on disconnection.
func (d *DingTalkChannel) Start(ctx context.Context, dispatch DispatchFunc) error {
	d.running.Store(true)
	defer d.running.Store(false)

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := d.runOnce(ctx, dispatch); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			d.log.Warn("dingtalk: connection lost, reconnecting", "err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

// Send posts a text reply to the DingTalk session webhook URL.
// Streaming deltas are ignored; only the final reply is sent.
func (d *DingTalkChannel) Send(ctx context.Context, msg OutboundMessage) error {
	if msg.Delta != "" {
		return nil
	}
	if msg.Text == "" {
		return nil
	}

	d.mu.RLock()
	webhookURL, ok := d.webhooks[msg.ChatID]
	d.mu.RUnlock()
	if !ok || webhookURL == "" {
		return fmt.Errorf("dingtalk: no webhook URL for chat %q", msg.ChatID)
	}

	token, err := d.getAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("dingtalk: get access token: %w", err)
	}

	return d.postReply(ctx, webhookURL, token, msg.Text)
}

// ── Stream connection ─────────────────────────────────────────────────────────

const (
	dingtalkGatewayURL = "https://api.dingtalk.com/v1.0/gateway/connections/open"
	dingtalkTokenURL   = "https://api.dingtalk.com/v1.0/oauth2/accessToken"
	topicRobot         = "/v1.0/im/bot/messages/get"
)

// gatewayResponse is returned by the gateway open endpoint.
type gatewayResponse struct {
	Endpoint string `json:"endpoint"`
	Ticket   string `json:"ticket"`
}

// streamFrame is the envelope used by the DingTalk Stream protocol.
type streamFrame struct {
	Type    string            `json:"type"`
	Headers map[string]string `json:"headers"`
	Data    string            `json:"data"`
}

// streamAck is sent back to DingTalk to acknowledge a CALLBACK frame.
type streamAck struct {
	Code    int               `json:"code"`
	Headers map[string]string `json:"headers"`
	Message string            `json:"message"`
	Data    string            `json:"data"`
}

func (d *DingTalkChannel) runOnce(ctx context.Context, dispatch DispatchFunc) error {
	// Step 1: Get WebSocket endpoint + ticket.
	gw, err := d.openGateway(ctx)
	if err != nil {
		return fmt.Errorf("open gateway: %w", err)
	}

	wsURL := gw.Endpoint + "?ticket=" + gw.Ticket
	d.log.Info("dingtalk: connecting to stream", "endpoint", gw.Endpoint)

	// Step 2: Connect WebSocket.
	wsConf, err := websocket.NewConfig(wsURL, "https://api.dingtalk.com")
	if err != nil {
		return fmt.Errorf("ws config: %w", err)
	}
	ws, err := websocket.DialConfig(wsConf)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer ws.Close()

	d.log.Info("dingtalk: stream connected")
	d.running.Store(true)

	// Step 3: Read frames until error or ctx cancellation.
	errCh := make(chan error, 1)
	go func() {
		for {
			var frame streamFrame
			if err := websocket.JSON.Receive(ws, &frame); err != nil {
				errCh <- err
				return
			}
			if err := d.handleFrame(ctx, ws, frame, dispatch); err != nil {
				d.log.Warn("dingtalk: handle frame", "err", err)
			}
		}
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

// openGateway calls DingTalk's REST API to get a WebSocket endpoint and ticket.
func (d *DingTalkChannel) openGateway(ctx context.Context) (*gatewayResponse, error) {
	body, _ := json.Marshal(map[string]any{
		"clientId":     d.clientID,
		"clientSecret": d.clientSecret,
		"ua":           "claw-go/1.0",
		"subscriptions": []map[string]string{
			{"type": "CALLBACK", "topic": topicRobot},
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dingtalkGatewayURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned %d: %s", resp.StatusCode, respBody)
	}

	var gw gatewayResponse
	if err := json.Unmarshal(respBody, &gw); err != nil {
		return nil, fmt.Errorf("parse gateway response: %w", err)
	}
	if gw.Endpoint == "" || gw.Ticket == "" {
		return nil, fmt.Errorf("gateway returned empty endpoint or ticket: %s", respBody)
	}
	return &gw, nil
}

// handleFrame processes a single WebSocket frame from DingTalk.
func (d *DingTalkChannel) handleFrame(ctx context.Context, ws *websocket.Conn, frame streamFrame, dispatch DispatchFunc) error {
	switch frame.Type {
	case "SYSTEM":
		return d.handleSystem(ws, frame)
	case "CALLBACK":
		return d.handleCallback(ctx, ws, frame, dispatch)
	}
	return nil
}

// handleSystem responds to SYSTEM frames (ping, KEEPALIVE, etc.).
func (d *DingTalkChannel) handleSystem(ws *websocket.Conn, frame streamFrame) error {
	topic := frame.Headers["topic"]
	switch topic {
	case "ping":
		// Must respond to ping immediately.
		pong := streamAck{
			Code:    200,
			Headers: frame.Headers,
			Message: "OK",
			Data:    frame.Data,
		}
		return websocket.JSON.Send(ws, pong)
	case "CONNECTED":
		d.log.Info("dingtalk: CONNECTED")
	case "REGISTERED":
		d.log.Info("dingtalk: REGISTERED")
	case "disconnect":
		d.log.Warn("dingtalk: server requested disconnect")
	}
	return nil
}

// handleCallback processes CALLBACK frames (robot messages).
func (d *DingTalkChannel) handleCallback(ctx context.Context, ws *websocket.Conn, frame streamFrame, dispatch DispatchFunc) error {
	msgID := frame.Headers["messageId"]

	// ACK immediately so DingTalk doesn't retry (60s window).
	if msgID != "" {
		ack := streamAck{
			Code: 200,
			Headers: map[string]string{
				"contentType": "application/json",
				"messageId":   msgID,
			},
			Message: "OK",
			Data:    "{}",
		}
		if err := websocket.JSON.Send(ws, ack); err != nil {
			d.log.Warn("dingtalk: send ack", "err", err)
		}
	}

	// Only handle robot messages.
	if frame.Headers["topic"] != topicRobot {
		return nil
	}

	var msg dingTalkMessage
	if err := json.Unmarshal([]byte(frame.Data), &msg); err != nil {
		return fmt.Errorf("parse message: %w", err)
	}

	if msg.MsgType != "text" {
		return nil // only handle text for now
	}

	sessionKey := msg.ConversationID
	if sessionKey == "" {
		sessionKey = msg.SenderStaffID
		if sessionKey == "" {
			sessionKey = msg.SenderID
		}
	}

	if msg.SessionWebhook != "" {
		d.mu.Lock()
		d.webhooks[sessionKey] = msg.SessionWebhook
		d.mu.Unlock()
	}

	inbound := InboundMessage{
		ChannelName: d.id,
		ChannelType: "dingtalk",
		SessionKey:  sessionKey,
		ChatID:      sessionKey,
		UserID:      msg.SenderStaffID,
		Username:    msg.SenderNick,
		Text:        msg.Text.Content,
		Timestamp:   time.Now(),
	}

	d.log.Info("dingtalk: message received",
		"from", msg.SenderNick,
		"session", sessionKey,
		"text", msg.Text.Content,
	)

	go dispatch(ctx, inbound)
	return nil
}

// dingTalkMessage is the JSON payload inside a robot CALLBACK frame.
type dingTalkMessage struct {
	MsgType       string `json:"msgtype"`
	Text          struct {
		Content string `json:"content"`
	} `json:"text"`
	SenderID      string `json:"senderId"`
	SenderStaffID string `json:"senderStaffId"`
	SenderNick    string `json:"senderNick"`
	ConversationID string `json:"conversationId"`
	SessionWebhook string `json:"sessionWebhook"`
}

// ── Access Token ──────────────────────────────────────────────────────────────

func (d *DingTalkChannel) getAccessToken(ctx context.Context) (string, error) {
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()

	if d.cachedToken != "" && time.Now().Before(d.tokenExpiry.Add(-60*time.Second)) {
		return d.cachedToken, nil
	}

	body, _ := json.Marshal(map[string]string{
		"appKey":    d.clientID,
		"appSecret": d.clientSecret,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dingtalkTokenURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token API returned %d: %s", resp.StatusCode, respBody)
	}

	var result struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int    `json:"expireIn"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}

	d.cachedToken = result.AccessToken
	d.tokenExpiry = time.Now().Add(time.Duration(result.ExpireIn) * time.Second)
	return d.cachedToken, nil
}

// ── Outbound reply ────────────────────────────────────────────────────────────

func (d *DingTalkChannel) postReply(ctx context.Context, webhookURL, token, text string) error {
	body, _ := json.Marshal(map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": text},
		"at":      map[string]bool{"isAtAll": false},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post reply: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("reply status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}
