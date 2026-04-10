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
	"mime"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

const (
	// dedupTTL is how long message IDs are remembered for deduplication.
	dedupTTL = 5 * time.Minute

	// defaultHeartbeatInterval is how often to send an application-layer ping.
	defaultHeartbeatInterval = 10 * time.Second
	// defaultHeartbeatTimeout is how long to wait for any frame before
	// treating the connection as dead and returning from runOnce.
	defaultHeartbeatTimeout = 20 * time.Second
)

// fixMarkdownTables ensures a blank line precedes every Markdown table header
// row. DingTalk AI Card fails to render tables that are not preceded by a blank
// line when the prior line contains non-table content.
func fixMarkdownTables(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines)+4)
	isTableRow := func(l string) bool { return strings.Contains(l, "|") }
	isDivider := func(l string) bool {
		if !strings.Contains(l, "|") {
			return false
		}
		for _, f := range strings.Split(l, "|") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			if !strings.ContainsAny(f, "-:") {
				return false
			}
		}
		return true
	}
	for i, line := range lines {
		if i > 0 && isTableRow(line) && isDivider(safeGet(lines, i+1)) &&
			lines[i-1] != "" && !isTableRow(lines[i-1]) {
			out = append(out, "")
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func safeGet(ss []string, i int) string {
	if i < 0 || i >= len(ss) {
		return ""
	}
	return ss[i]
}

// dingtalkSession stores per-chat context needed for outbound replies and AI Card delivery.
type dingtalkSession struct {
	webhookURL       string
	conversationType string // "1"=DM, "2"=group
	senderStaffID    string // for IM_ROBOT openSpaceId (DM only)
	conversationID   string // for IM_GROUP openSpaceId (group only)
	robotCode        string
}

// dingtalkCard tracks an in-progress AI Card streaming session for one chat.
type dingtalkCard struct {
	instanceID  string
	accessToken string    // token captured at card creation
	createdAt   time.Time // for zombie cleanup
	started     bool      // true after first streaming PUT
	content     string    // accumulated delta text for full-replace streaming
}

// DingTalkChannel implements Channel using DingTalk's Stream (WebSocket) API.
type DingTalkChannel struct {
	id           string
	clientID     string // AppKey
	clientSecret string // AppSecret
	log          *slog.Logger

	// gatewayURL and tokenURL are overridable for testing.
	gatewayURL string
	tokenURL   string

	// httpClient is used for all DingTalk REST calls.
	httpClient      *http.Client
	mediaHTTPClient *http.Client // used for image downloads (longer timeout)

	running atomic.Bool

	mu       sync.RWMutex
	sessions map[string]dingtalkSession // key = chatID

	tokenMu     sync.Mutex
	cachedToken string
	tokenExpiry time.Time

	// dedupMu guards seen, which stores message IDs (proto + biz) with their
	// insertion timestamp for TTL-based expiry.
	dedupMu sync.Mutex
	seen    map[string]time.Time

	// heartbeatInterval and heartbeatTimeout are overridable for testing.
	heartbeatInterval time.Duration
	heartbeatTimeout  time.Duration

	// cardsMu guards activeCards.
	cardsMu     sync.Mutex
	activeCards map[string]*dingtalkCard // key = chatID

	// AI Card API URLs — overridable for testing.
	cardInstancesURL string
	cardDeliverURL   string
	cardStreamingURL string

	// messageFilesDownloadURL is overridable for testing.
	messageFilesDownloadURL string
}

// NewDingTalkChannel creates a DingTalkChannel.
// clientID is the DingTalk AppKey; clientSecret is the AppSecret.
func NewDingTalkChannel(id, clientID, clientSecret string, log *slog.Logger) *DingTalkChannel {
	if log == nil {
		log = slog.Default()
	}
	return &DingTalkChannel{
		id:                id,
		clientID:          clientID,
		clientSecret:      clientSecret,
		log:               log,
		gatewayURL:        dingtalkGatewayURL,
		tokenURL:          dingtalkTokenURL,
		httpClient:        &http.Client{Timeout: 15 * time.Second},
		mediaHTTPClient:   &http.Client{Timeout: 60 * time.Second}, // image downloads can be large
		sessions:         make(map[string]dingtalkSession),
		activeCards:      make(map[string]*dingtalkCard),
		cardInstancesURL:        "https://api.dingtalk.com/v1.0/card/instances",
		cardDeliverURL:          "https://api.dingtalk.com/v1.0/card/instances/deliver",
		cardStreamingURL:        "https://api.dingtalk.com/v1.0/card/streaming",
		messageFilesDownloadURL: "https://api.dingtalk.com/v1.0/robot/messageFiles/download",
		seen:              make(map[string]time.Time),
		heartbeatInterval: defaultHeartbeatInterval,
		heartbeatTimeout:  defaultHeartbeatTimeout,
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

	go d.cleanupZombieCards(ctx)

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

// Send posts a reply to the DingTalk chat.
// Delta messages are handled via AI Card streaming (sendDelta).
// The final Text message completes the card or falls back to plain text (finishCard).
func (d *DingTalkChannel) Send(ctx context.Context, msg OutboundMessage) error {
	if msg.Delta != "" {
		return d.sendDelta(ctx, msg.ChatID, msg.Delta)
	}
	if msg.Text == "" {
		return nil
	}

	d.mu.RLock()
	sess, ok := d.sessions[msg.ChatID]
	d.mu.RUnlock()
	if !ok || sess.webhookURL == "" {
		return fmt.Errorf("dingtalk: no session for chat %q", msg.ChatID)
	}

	token, err := d.getAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("dingtalk: get access token: %w", err)
	}

	return d.finishCard(ctx, msg.ChatID, msg.Text, sess, token)
}

// sendDelta handles a streaming delta: creates the card on first delta, then
// streams subsequent deltas. Falls back silently if card creation fails
// (the final Text message will use plain postReply via finishCard).
func (d *DingTalkChannel) sendDelta(ctx context.Context, chatID, delta string) error {
	if delta == "" {
		return nil
	}

	d.cardsMu.Lock()
	card := d.activeCards[chatID]
	if card == nil {
		// First delta for this chat — create the card.
		d.mu.RLock()
		sess, ok := d.sessions[chatID]
		d.mu.RUnlock()
		if !ok {
			d.cardsMu.Unlock()
			return nil // no session yet, drop delta
		}

		token, err := d.getAccessToken(ctx)
		if err != nil {
			d.cardsMu.Unlock()
			d.log.Warn("dingtalk: card token error, dropping delta", "err", err)
			return nil
		}

		instanceID, err := d.createCard(ctx, chatID, sess, token)
		if err != nil {
			d.cardsMu.Unlock()
			d.log.Warn("dingtalk: card create failed, will use text fallback", "err", err)
			return nil
		}

		card = &dingtalkCard{
			instanceID:  instanceID,
			accessToken: token,
			createdAt:   time.Now(),
		}
		d.activeCards[chatID] = card
	}
	d.cardsMu.Unlock()

	// card.content and card.started are accessed without a lock here.
	// This is safe because the Agent dispatches at most one LLM stream per
	// chatID at a time, so sendDelta is never called concurrently for the
	// same chatID.
	card.content += delta
	return d.streamCard(ctx, card, card.content, false)
}

// finishCard completes an AI Card with the final content, or falls back to a
// plain text reply if no card is active for this chatID.
func (d *DingTalkChannel) finishCard(ctx context.Context, chatID, text string, sess dingtalkSession, token string) error {
	d.cardsMu.Lock()
	card := d.activeCards[chatID]
	delete(d.activeCards, chatID)
	d.cardsMu.Unlock()

	if card == nil {
		// No active card (streaming was never started, or card creation failed).
		if looksLikeMarkdown(text) {
			return d.postMarkdownReply(ctx, sess.webhookURL, token, text)
		}
		return d.postReply(ctx, sess.webhookURL, token, text)
	}

	// Final streaming chunk with isFinalize=true.
	if err := d.streamCard(ctx, card, text, true); err != nil {
		d.log.Warn("dingtalk: card finalize failed, falling back to text", "err", err)
		return d.postReply(ctx, sess.webhookURL, token, text)
	}

	// Set FINISHED status.
	fixed := fixMarkdownTables(text)
	finishBody := mustMarshal(map[string]any{
		"outTrackId": card.instanceID,
		"cardData": map[string]any{
			"cardParamMap": map[string]any{
				"flowStatus":        "3",
				"msgContent":        fixed,
				"staticMsgContent":  "",
				"sys_full_json_obj": `{"order":["msgContent"]}`,
				"config":            `{"autoLayout":true}`,
			},
		},
		"cardUpdateOptions": map[string]any{"updateCardDataByKey": true},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, d.cardInstancesURL, bytes.NewReader(finishBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", card.accessToken)
	resp, err := d.httpClient.Do(req)
	if err != nil {
		d.log.Warn("dingtalk: card finish status update failed", "err", err)
		return nil // card content is already finalized, non-fatal
	}
	resp.Body.Close()
	return nil
}

// createCard creates an AI Card instance and delivers it to the target chat.
// Returns the card instance ID, or an error if creation or delivery fails.
func (d *DingTalkChannel) createCard(ctx context.Context, chatID string, sess dingtalkSession, token string) (string, error) {
	instanceID := fmt.Sprintf("claw_%d_%s", time.Now().UnixMilli(), chatID[:min(8, len(chatID))])

	// 1. Create card instance.
	createBody := mustMarshal(map[string]any{
		"cardTemplateId": dingtalkCardTemplateID,
		"outTrackId":     instanceID,
		"cardData": map[string]any{
			"cardParamMap": map[string]any{
				"config": `{"autoLayout":true}`,
			},
		},
		"callbackType":          "STREAM",
		"imGroupOpenSpaceModel": map[string]any{"supportForward": true},
		"imRobotOpenSpaceModel": map[string]any{"supportForward": true},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.cardInstancesURL, bytes.NewReader(createBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("card create: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("card create status %d", resp.StatusCode)
	}

	// 2. Deliver card to target.
	openSpaceID := d.buildOpenSpaceID(chatID, sess)
	robotCode := sess.robotCode
	if robotCode == "" {
		robotCode = d.clientID
	}

	var deliverModel map[string]any
	if sess.conversationType == "2" {
		deliverModel = map[string]any{
			"outTrackId":  instanceID,
			"userIdType":  1,
			"openSpaceId": openSpaceID,
			"imGroupOpenDeliverModel": map[string]any{"robotCode": robotCode},
		}
	} else {
		deliverModel = map[string]any{
			"outTrackId":  instanceID,
			"userIdType":  1,
			"openSpaceId": openSpaceID,
			"imRobotOpenDeliverModel": map[string]any{
				"spaceType": "IM_ROBOT",
				"robotCode": robotCode,
				"extension": map[string]any{"dynamicSummary": "true"},
			},
		}
	}

	deliverBody := mustMarshal(deliverModel)
	req2, err := http.NewRequestWithContext(ctx, http.MethodPost, d.cardDeliverURL, bytes.NewReader(deliverBody))
	if err != nil {
		return "", err
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("x-acs-dingtalk-access-token", token)
	resp2, err := d.httpClient.Do(req2)
	if err != nil {
		return "", fmt.Errorf("card deliver: %w", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode >= 300 {
		return "", fmt.Errorf("card deliver status %d", resp2.StatusCode)
	}

	return instanceID, nil
}

func (d *DingTalkChannel) buildOpenSpaceID(chatID string, sess dingtalkSession) string {
	if sess.conversationType == "2" {
		convID := sess.conversationID
		if convID == "" {
			convID = chatID
		}
		return "dtv1.card//IM_GROUP." + convID
	}
	userID := sess.senderStaffID
	if userID == "" {
		userID = chatID
	}
	return "dtv1.card//IM_ROBOT." + userID
}

// streamCard sends a streaming content update to an existing AI Card.
// content is the full accumulated text so far (isFull=true).
// isFinalize=true marks the last chunk.
func (d *DingTalkChannel) streamCard(ctx context.Context, card *dingtalkCard, content string, isFinalize bool) error {
	fixed := fixMarkdownTables(content)

	if !card.started {
		// First streaming call: switch card to INPUTING status.
		statusBody := mustMarshal(map[string]any{
			"outTrackId": card.instanceID,
			"cardData": map[string]any{
				"cardParamMap": map[string]any{
					"flowStatus":        "2",
					"msgContent":        fixed,
					"staticMsgContent":  "",
					"sys_full_json_obj": `{"order":["msgContent"]}`,
					"config":            `{"autoLayout":true}`,
				},
			},
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, d.cardInstancesURL, bytes.NewReader(statusBody))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-acs-dingtalk-access-token", card.accessToken)
		resp, err := d.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("card inputing: %w", err)
		}
		resp.Body.Close() // intentional non-defer: card.started must be set after this block exits
		card.started = true
	}

	body := mustMarshal(map[string]any{
		"outTrackId": card.instanceID,
		"guid":       fmt.Sprintf("%d_%s", time.Now().UnixNano(), card.instanceID[:min(6, len(card.instanceID))]),
		"key":        "msgContent",
		"content":    fixed,
		"isFull":     true,
		"isFinalize": isFinalize,
		"isError":    false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, d.cardStreamingURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", card.accessToken)
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("card streaming: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("card streaming status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// cleanupZombieCards removes activeCards entries older than 5 minutes.
// Called as a goroutine from Start to prevent memory leaks when LLM calls
// are cancelled before the final Text message arrives.
func (d *DingTalkChannel) cleanupZombieCards(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-5 * time.Minute)
			d.cardsMu.Lock()
			for k, c := range d.activeCards {
				if c.createdAt.Before(cutoff) {
					delete(d.activeCards, k)
				}
			}
			d.cardsMu.Unlock()
		}
	}
}

// ── Stream connection ─────────────────────────────────────────────────────────

const (
	dingtalkGatewayURL       = "https://api.dingtalk.com/v1.0/gateway/connections/open"
	dingtalkTokenURL         = "https://api.dingtalk.com/v1.0/oauth2/accessToken"
	topicRobot               = "/v1.0/im/bot/messages/get"
	dingtalkCardTemplateID   = "02fcf2f4-5e02-4a85-b672-46d1f715543e.schema"
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

	// lastFrameAt tracks when we last received any frame from the server.
	// The heartbeat goroutine uses this to detect a silently dead connection.
	var lastFrameMu sync.Mutex
	lastFrameAt := time.Now()
	touchFrame := func() {
		lastFrameMu.Lock()
		lastFrameAt = time.Now()
		lastFrameMu.Unlock()
	}

	// Step 3: Read frames until error or ctx cancellation.
	errCh := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				d.log.Error("dingtalk: frame reader panic recovered", "panic", r)
				errCh <- fmt.Errorf("frame reader panic: %v", r)
			}
		}()
		for {
			var frame streamFrame
			if err := websocket.JSON.Receive(ws, &frame); err != nil {
				errCh <- err
				return
			}
			touchFrame()
			if err := d.handleFrame(ctx, ws, frame, dispatch); err != nil {
				d.log.Warn("dingtalk: handle frame", "err", err)
			}
		}
	}()

	// Step 4: Application-layer heartbeat.
	// golang.org/x/net/websocket does not expose native WebSocket ping frames,
	// so we track liveness by sending a SYSTEM ping every heartbeatInterval and
	// checking that we received at least one frame within heartbeatTimeout.
	go func() {
		ticker := time.NewTicker(d.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				lastFrameMu.Lock()
				elapsed := time.Since(lastFrameAt)
				lastFrameMu.Unlock()
				if elapsed > d.heartbeatTimeout {
					d.log.Warn("dingtalk: heartbeat timeout, closing connection",
						"elapsed", elapsed.Round(time.Millisecond))
					errCh <- fmt.Errorf("heartbeat timeout after %s", elapsed.Round(time.Millisecond))
					return
				}
				// Send an application-layer ping so the server knows we're alive.
				ping := streamAck{Code: 200, Headers: map[string]string{"topic": "ping"}, Message: "ping", Data: "{}"}
				if err := websocket.JSON.Send(ws, ping); err != nil {
					d.log.Warn("dingtalk: heartbeat send failed", "err", err)
				}
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
	body := mustMarshal(map[string]any{
		"clientId":     d.clientID,
		"clientSecret": d.clientSecret,
		"ua":           "claw-go/1.0",
		"subscriptions": []map[string]string{
			{"type": "CALLBACK", "topic": topicRobot},
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.gatewayURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := d.httpClient.Do(req)
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
		return d.handleCallbackWithAck(ctx, func(ack streamAck) {
			if err := websocket.JSON.Send(ws, ack); err != nil {
				d.log.Warn("dingtalk: send ack", "err", err)
			}
		}, frame, dispatch)
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

// checkAndMarkSeen returns true if either id has been seen recently, and marks
// both ids as seen. Expired entries are purged on each call (amortised O(n)).
// ids with empty string values are ignored.
func (d *DingTalkChannel) checkAndMarkSeen(ids ...string) bool {
	d.dedupMu.Lock()
	defer d.dedupMu.Unlock()

	now := time.Now()

	// Purge expired entries.
	for k, t := range d.seen {
		if now.Sub(t) > dedupTTL {
			delete(d.seen, k)
		}
	}

	// Check.
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := d.seen[id]; ok {
			// Mark any remaining unseen ids before returning.
			for _, id2 := range ids {
				if id2 != "" {
					d.seen[id2] = now
				}
			}
			return true
		}
	}

	// First time — mark all.
	for _, id := range ids {
		if id != "" {
			d.seen[id] = now
		}
	}
	return false
}

// handleCallbackWithAck processes CALLBACK frames. sendAck is called to
// acknowledge the frame; this indirection allows unit testing without a real
// WebSocket connection.
func (d *DingTalkChannel) handleCallbackWithAck(ctx context.Context, sendAck func(streamAck), frame streamFrame, dispatch DispatchFunc) error {
	msgID := frame.Headers["messageId"]

	// ACK immediately so DingTalk doesn't retry (60s window).
	if msgID != "" {
		sendAck(streamAck{
			Code: 200,
			Headers: map[string]string{
				"contentType": "application/json",
				"messageId":   msgID,
			},
			Message: "OK",
			Data:    "{}",
		})
	}

	// Only handle robot messages.
	if frame.Headers["topic"] != topicRobot {
		return nil
	}

	var msg dingTalkMessage
	if err := json.Unmarshal([]byte(frame.Data), &msg); err != nil {
		return fmt.Errorf("parse message: %w", err)
	}

	switch msg.MsgType {
	case "text":
		// handled below
	case "picture":
		// handled below
	case "richText":
		// handled below
	default:
		if d.log != nil {
			d.log.Info("dingtalk: unsupported message type, notifying user",
				"msgtype", msg.MsgType, "msgId", msg.MsgID)
		}
		// Notify user instead of silently dropping.
		if msg.SessionWebhook != "" {
			token, _ := d.getAccessToken(ctx)
			if token != "" {
				_ = d.postReply(ctx, msg.SessionWebhook, token,
					fmt.Sprintf("收到%s消息，暂不支持处理。", msg.MsgType))
			}
		}
		return nil
	}

	// Dedup: check both protocol-layer messageId (headers) and business-layer
	// msgId (payload). DingTalk may re-deliver with a new headers.messageId but
	// the same data.msgId ~60s after not receiving a business-level ACK.
	if d.checkAndMarkSeen(msgID, msg.MsgID) {
		d.log.Debug("dingtalk: duplicate message, skipping", "protoId", msgID, "bizId", msg.MsgID)
		return nil
	}

	// Resolve sessionKey with fallback; keep UserID consistent with the resolved key.
	sessionKey := msg.ConversationID
	userID := msg.SenderStaffID
	if sessionKey == "" {
		if msg.SenderStaffID != "" {
			sessionKey = msg.SenderStaffID
			userID = msg.SenderStaffID
		} else {
			sessionKey = msg.SenderID
			userID = msg.SenderID
		}
	}

	if msg.SessionWebhook != "" {
		d.mu.Lock()
		if len(d.sessions) >= maxSessions {
			n := len(d.sessions) / 2
			toDelete := make([]string, 0, n)
			for k := range d.sessions {
				toDelete = append(toDelete, k)
				if len(toDelete) == n {
					break
				}
			}
			for _, k := range toDelete {
				delete(d.sessions, k)
			}
		}
		robotCode := msg.RobotCode
		if robotCode == "" {
			robotCode = d.clientID
		}
		d.sessions[sessionKey] = dingtalkSession{
			webhookURL:       msg.SessionWebhook,
			conversationType: msg.ConversationType,
			senderStaffID:    msg.SenderStaffID,
			conversationID:   msg.ConversationID,
			robotCode:        robotCode,
		}
		d.mu.Unlock()
	}

	var inboundText string
	var attachments []Attachment

	switch msg.MsgType {
	case "text":
		inboundText = strings.TrimSpace(msg.Text.Content)
	case "picture":
		path, err := d.downloadDingTalkImage(ctx, msg.Picture.PictureURL, msg.Picture.DownloadCode)
		if err != nil {
			d.log.Warn("dingtalk: picture download failed", "err", err)
		} else if path != "" {
			attachments = append(attachments, Attachment{
				Path:     path,
				MimeType: mimeFromPath(path),
				AltText:  "[图片]",
			})
		}
		inboundText = ""
	case "richText":
		// Try to parse richTextList from msg.RichText or from msg.Content JSON string.
		richList := msg.RichText.RichTextList
		if len(richList) == 0 && msg.Content != "" {
			var parsed struct {
				RichText struct {
					RichTextList []dingTalkRichTextItem `json:"richTextList"`
				} `json:"richText"`
			}
			if json.Unmarshal([]byte(msg.Content), &parsed) == nil {
				richList = parsed.RichText.RichTextList
			}
		}
		var textParts []string
		for _, item := range richList {
			switch item.Type {
			case "text":
				if item.Text != "" {
					textParts = append(textParts, item.Text)
				}
			case "picture":
				path, err := d.downloadDingTalkImage(ctx, item.PictureURL, item.DownloadCode)
				if err != nil {
					d.log.Warn("dingtalk: richText picture download failed", "err", err)
				} else if path != "" {
					attachments = append(attachments, Attachment{
						Path:     path,
						MimeType: mimeFromPath(path),
						AltText:  "[图片]",
					})
				}
			}
		}
		inboundText = strings.TrimSpace(strings.Join(textParts, ""))
	}

	if inboundText == "" && len(attachments) == 0 {
		return nil
	}

	inbound := InboundMessage{
		ChannelName: d.id,
		ChannelType: "dingtalk",
		SessionKey:  sessionKey,
		ChatID:      sessionKey,
		UserID:      userID,
		Username:    msg.SenderNick,
		Text:        inboundText,
		MessageID:   msg.MsgID,
		Timestamp:   time.Now(),
		Attachments: attachments,
	}

	d.log.Info("dingtalk: message received",
		"from", msg.SenderNick,
		"session", sessionKey,
		"text", inboundText,
		"images", len(attachments),
	)

	// Start webhook keepalive to prevent the sessionWebhook from expiring
	// during long AI processing tasks.
	d.mu.RLock()
	sess, hasSess := d.sessions[sessionKey]
	d.mu.RUnlock()
	if hasSess && sess.webhookURL != "" {
		keepaliveCtx, cancelKeepalive := context.WithCancel(ctx)
		d.startWebhookKeepalive(keepaliveCtx, sess.webhookURL)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					d.log.Error("dingtalk: dispatch panic recovered", "panic", r)
				}
				cancelKeepalive()
			}()
			dispatch(ctx, inbound)
		}()
	} else {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					d.log.Error("dingtalk: dispatch panic recovered", "panic", r)
				}
			}()
			dispatch(ctx, inbound)
		}()
	}
	return nil
}

// startWebhookKeepalive sends a periodic typing indicator to the sessionWebhook
// to prevent it from expiring (DingTalk webhooks expire after ~60 seconds).
// The goroutine stops when ctx is cancelled.
// Call this when starting to process a message; cancel the ctx when done.
func (d *DingTalkChannel) startWebhookKeepalive(ctx context.Context, webhookURL string) {
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				token, err := d.getAccessToken(ctx)
				if err != nil {
					return
				}
				body := mustMarshal(map[string]any{
					"msgtype": "text",
					"text":    map[string]string{"content": "⏳"},
					"at":      map[string]bool{"isAtAll": false},
				})
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
				if err != nil {
					return
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("x-acs-dingtalk-access-token", token)
				resp, err := d.httpClient.Do(req)
				if err != nil {
					return
				}
				resp.Body.Close()
				d.log.Debug("dingtalk: webhook keepalive sent", "webhook", webhookURL[:min(len(webhookURL), 60)])
			}
		}
	}()
}

// dingTalkMessage is the JSON payload inside a robot CALLBACK frame.
type dingTalkMessage struct {
	MsgID   string `json:"msgId"`
	MsgType string `json:"msgtype"`
	Text    struct {
		Content string `json:"content"`
	} `json:"text"`
	// picture message
	Picture struct {
		PictureURL   string `json:"pictureUrl"`
		DownloadCode string `json:"downloadCode"`
	} `json:"picture"`
	// richText message
	RichText struct {
		RichTextList []dingTalkRichTextItem `json:"richTextList"`
	} `json:"richText"`
	Content          string `json:"content"` // richText sometimes puts content here as JSON string
	SenderID         string `json:"senderId"`
	SenderStaffID    string `json:"senderStaffId"`
	SenderNick       string `json:"senderNick"`
	ConversationID   string `json:"conversationId"`
	ConversationType string `json:"conversationType"`
	RobotCode        string `json:"robotCode"`
	SessionWebhook   string `json:"sessionWebhook"`
}

// dingTalkRichTextItem is one element in a richText message.
type dingTalkRichTextItem struct {
	Type         string `json:"type"`         // "text" | "picture"
	Text         string `json:"text"`
	PictureURL   string `json:"pictureUrl"`
	DownloadCode string `json:"downloadCode"`
}

// ── Access Token ──────────────────────────────────────────────────────────────

func (d *DingTalkChannel) getAccessToken(ctx context.Context) (string, error) {
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()

	if d.cachedToken != "" && time.Now().Before(d.tokenExpiry.Add(-60*time.Second)) {
		return d.cachedToken, nil
	}

	body := mustMarshal(map[string]string{
		"appKey":    d.clientID,
		"appSecret": d.clientSecret,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.tokenURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.httpClient.Do(req)
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
	body := mustMarshal(map[string]any{
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

	resp, err := d.httpClient.Do(req)
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

// postMarkdownReply sends a markdown-formatted reply via the session webhook.
func (d *DingTalkChannel) postMarkdownReply(ctx context.Context, webhookURL, token, text string) error {
	// Use first line (up to 20 chars) as title.
	title := text
	if idx := strings.Index(title, "\n"); idx != -1 {
		title = title[:idx]
	}
	if len([]rune(title)) > 20 {
		title = string([]rune(title)[:20])
	}
	body := mustMarshal(map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]string{"title": title, "text": text},
		"at":       map[string]bool{"isAtAll": false},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post markdown reply: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("markdown reply status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// looksLikeMarkdown reports whether text contains markdown formatting.
func looksLikeMarkdown(text string) bool {
	return strings.HasPrefix(text, "#") ||
		strings.Contains(text, "**") ||
		strings.Contains(text, "```") ||
		strings.Contains(text, "\n- ") ||
		strings.Contains(text, "\n* ") ||
		strings.Contains(text, "\n1. ")
}

// downloadImageToTemp downloads an image from url into a temp file.
// Returns the absolute path of the temp file on success.
func (d *DingTalkChannel) downloadImageToTemp(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := d.mediaHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download image: status %d", resp.StatusCode)
	}

	// Detect extension from Content-Type.
	ext := ".jpg"
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		mt, _, _ := mime.ParseMediaType(ct)
		switch mt {
		case "image/png":
			ext = ".png"
		case "image/gif":
			ext = ".gif"
		case "image/webp":
			ext = ".webp"
		}
	}

	f, err := os.CreateTemp("", "claw-dt-*"+ext)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 20*1024*1024)); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("write temp file: %w", err)
	}
	return f.Name(), nil
}

// resolveDownloadCode exchanges a DingTalk downloadCode for a direct download URL.
// Calls POST /v1.0/robot/messageFiles/download with the bot's access token.
func (d *DingTalkChannel) resolveDownloadCode(ctx context.Context, downloadCode string) (string, error) {
	token, err := d.getAccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("get access token: %w", err)
	}
	robotCode := d.clientID
	body := mustMarshal(map[string]string{
		"downloadCode": downloadCode,
		"robotCode":    robotCode,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.messageFilesDownloadURL,
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("messageFiles/download: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("messageFiles/download status %d: %s", resp.StatusCode, respBody)
	}
	var result struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse download response: %w", err)
	}
	if result.DownloadURL == "" {
		return "", fmt.Errorf("empty downloadUrl in response: %s", respBody)
	}
	return result.DownloadURL, nil
}

// downloadDingTalkImage downloads a DingTalk image to a local temp file.
// It tries pictureUrl first; if empty, resolves downloadCode to a URL then downloads.
// Returns ("", nil) if both are empty (no image to download).
func (d *DingTalkChannel) downloadDingTalkImage(ctx context.Context, pictureURL, downloadCode string) (string, error) {
	url := pictureURL
	if url == "" && downloadCode != "" {
		var err error
		url, err = d.resolveDownloadCode(ctx, downloadCode)
		if err != nil {
			return "", fmt.Errorf("resolve downloadCode: %w", err)
		}
	}
	if url == "" {
		return "", nil
	}
	return d.downloadImageToTemp(ctx, url)
}
