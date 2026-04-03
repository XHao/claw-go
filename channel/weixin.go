// Package channel - WeChat iLink Bot channel.
//
// Uses WeChat's iLink Bot API (HTTP long-polling) — no public HTTP server needed.
// On first start, displays a QR code for the user to scan with WeChat.
// After scanning, the bot_token is saved to disk and reused on subsequent starts.
//
// Protocol:
//  1. GET /ilink/bot/get_bot_qrcode?bot_type=3  → {qrcode, qrcode_img_content}
//  2. Display QR code in terminal
//  3. Poll GET /ilink/bot/get_qrcode_status?qrcode=<qrcode> until status="confirmed"
//  4. Save {bot_token, baseurl} to token file (0600)
//  5. POST {baseurl}/ilink/bot/getupdates (long-poll, 38s timeout) in a loop
//  6. Reply via POST {baseurl}/ilink/bot/sendmessage with context_token echoed back
//
// Credentials: obtained via QR-code scan. No manual configuration needed.
package channel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mdp/qrterminal/v3"
)

const (
	weixinDefaultBase = "https://ilinkai.weixin.qq.com"
	weixinQRCodeURL   = weixinDefaultBase + "/ilink/bot/get_bot_qrcode?bot_type=3"
	weixinQRStatusURL = weixinDefaultBase + "/ilink/bot/get_qrcode_status"
)

// weixinTokenData is persisted to disk after a successful QR login.
type weixinTokenData struct {
	BotToken string `json:"bot_token"`
	BaseURL  string `json:"baseurl"`
}

// weixinSession holds the reply context for a single WeChat user.
type weixinSession struct {
	fromUserID   string
	contextToken string
}

// WeixinChannel implements Channel using WeChat's iLink Bot API.
type WeixinChannel struct {
	id        string
	tokenFile string
	log       *slog.Logger

	running atomic.Bool

	// populated after login
	botToken string
	baseURL  string

	mu       sync.RWMutex
	sessions map[string]weixinSession // key = from_user_id
}

// NewWeixinChannel creates a WeixinChannel.
// tokenFile is the path to persist the bot_token; use dirs.WeixinTokenFile() for the default.
func NewWeixinChannel(id, tokenFile string, log *slog.Logger) *WeixinChannel {
	if log == nil {
		log = slog.Default()
	}
	return &WeixinChannel{
		id:        id,
		tokenFile: tokenFile,
		log:       log,
		sessions:  make(map[string]weixinSession),
	}
}

// ID returns the unique channel identifier.
func (w *WeixinChannel) ID() string { return "weixin:" + w.id }

// Status returns the current health of the channel.
func (w *WeixinChannel) Status() Status {
	return Status{
		ID:      w.ID(),
		Type:    "weixin",
		Name:    w.id,
		Running: w.running.Load(),
	}
}

// Start logs in (via QR if needed) then polls for messages until ctx is cancelled.
// Reconnects automatically with exponential back-off on error.
func (w *WeixinChannel) Start(ctx context.Context, dispatch DispatchFunc) error {
	// Step 1: load or acquire token.
	if err := w.ensureToken(ctx); err != nil {
		return fmt.Errorf("weixin: login: %w", err)
	}

	w.running.Store(true)
	defer w.running.Store(false)

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := w.pollLoop(ctx, dispatch); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.log.Warn("weixin: poll error, reconnecting", "err", err, "backoff", backoff)
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

// Send posts a text reply to the WeChat user.
// Streaming deltas are ignored; only the final reply is sent.
func (w *WeixinChannel) Send(ctx context.Context, msg OutboundMessage) error {
	if msg.Delta != "" {
		return nil
	}
	if msg.Text == "" {
		return nil
	}

	w.mu.RLock()
	sess, ok := w.sessions[msg.ChatID]
	w.mu.RUnlock()
	if !ok {
		return fmt.Errorf("weixin: no session for chat %q", msg.ChatID)
	}

	return w.sendMessage(ctx, sess.fromUserID, sess.contextToken, msg.Text)
}

// ── Login ─────────────────────────────────────────────────────────────────────

// ensureToken loads the token from disk, or runs the QR login flow.
func (w *WeixinChannel) ensureToken(ctx context.Context) error {
	// Try loading persisted token.
	data, err := os.ReadFile(w.tokenFile)
	if err == nil {
		var td weixinTokenData
		if json.Unmarshal(data, &td) == nil && td.BotToken != "" {
			w.botToken = td.BotToken
			w.baseURL = td.BaseURL
			if w.baseURL == "" {
				w.baseURL = weixinDefaultBase
			}
			w.log.Info("weixin: loaded token from file", "file", w.tokenFile)
			return nil
		}
	}

	// No valid token — run interactive QR login.
	return w.qrLogin(ctx)
}

// qrLogin fetches a QR code, displays it, and polls until the user scans it.
func (w *WeixinChannel) qrLogin(ctx context.Context) error {
	w.log.Info("weixin: no token found, starting QR login")

	// Fetch QR code.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, weixinQRCodeURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("get QR code: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	var qrResp struct {
		QRCode           string `json:"qrcode"`
		QRCodeImgContent string `json:"qrcode_img_content"` // base64 PNG, unused
	}
	if err := json.Unmarshal(body, &qrResp); err != nil || qrResp.QRCode == "" {
		return fmt.Errorf("parse QR response: %w (body: %s)", err, body)
	}

	// Display QR code in terminal.
	fmt.Println("\n请用微信扫描以下二维码登录 WeChat Bot:")
	qrterminal.GenerateWithConfig(qrResp.QRCode, qrterminal.Config{
		Level:     qrterminal.L,
		Writer:    os.Stdout,
		BlackChar: qrterminal.BLACK,
		WhiteChar: qrterminal.WHITE,
		QuietZone: 1,
	})
	fmt.Println()

	// Poll for scan confirmation.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}

		statusURL := weixinQRStatusURL + "?qrcode=" + qrResp.QRCode
		sreq, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
		if err != nil {
			continue
		}
		sresp, err := http.DefaultClient.Do(sreq)
		if err != nil {
			continue
		}
		sbody, _ := io.ReadAll(io.LimitReader(sresp.Body, 4096))
		sresp.Body.Close()

		var statusResp struct {
			Status   string `json:"status"`
			BotToken string `json:"bot_token"`
			BaseURL  string `json:"baseurl"`
		}
		if err := json.Unmarshal(sbody, &statusResp); err != nil {
			continue
		}
		if statusResp.Status != "confirmed" {
			continue
		}

		// Confirmed — save token.
		w.botToken = statusResp.BotToken
		w.baseURL = statusResp.BaseURL
		if w.baseURL == "" {
			w.baseURL = weixinDefaultBase
		}

		td := weixinTokenData{BotToken: w.botToken, BaseURL: w.baseURL}
		tdJSON, _ := json.Marshal(td)
		if err := os.WriteFile(w.tokenFile, tdJSON, 0o600); err != nil {
			w.log.Warn("weixin: could not save token", "err", err)
		} else {
			w.log.Info("weixin: token saved", "file", w.tokenFile)
		}

		fmt.Println("✓ 微信登录成功！")
		return nil
	}
}

// ── Long-poll loop ─────────────────────────────────────────────────────────────

// pollLoop runs a single continuous long-poll session until error or ctx cancellation.
func (w *WeixinChannel) pollLoop(ctx context.Context, dispatch DispatchFunc) error {
	cursor := ""
	client := &http.Client{Timeout: 38 * time.Second}

	for {
		if ctx.Err() != nil {
			return nil
		}

		msgs, newCursor, err := w.getUpdates(ctx, client, cursor)
		if err != nil {
			return err
		}
		if newCursor != "" {
			cursor = newCursor
		}

		for _, msg := range msgs {
			if err := w.handleMessage(ctx, msg, dispatch); err != nil {
				w.log.Warn("weixin: handle message", "err", err)
			}
		}
	}
}

// weixinInboundMsg is the JSON structure of a message from getupdates.
type weixinInboundMsg struct {
	FromUserID   string `json:"from_user_id"`
	ToUserID     string `json:"to_user_id"`
	MessageType  int    `json:"message_type"`
	ContextToken string `json:"context_token"`
	ItemList     []struct {
		Type     int `json:"type"`
		TextItem struct {
			Text string `json:"text"`
		} `json:"text_item"`
	} `json:"item_list"`
}

// getUpdates calls /ilink/bot/getupdates and returns parsed messages and the new cursor.
func (w *WeixinChannel) getUpdates(ctx context.Context, client *http.Client, cursor string) ([]weixinInboundMsg, string, error) {
	body, _ := json.Marshal(map[string]any{
		"get_updates_buf": cursor,
		"base_info":       map[string]string{"channel_version": "1.0.2"},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.baseURL+"/ilink/bot/getupdates", bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	w.setHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", nil // cancelled
		}
		return nil, "", fmt.Errorf("getupdates: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var result struct {
		Ret           int                `json:"ret"`
		Msgs          []weixinInboundMsg `json:"msgs"`
		GetUpdatesBuf string             `json:"get_updates_buf"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, "", fmt.Errorf("parse getupdates response: %w", err)
	}
	if result.Ret != 0 {
		return nil, "", fmt.Errorf("getupdates ret=%d body=%s", result.Ret, respBody)
	}

	return result.Msgs, result.GetUpdatesBuf, nil
}

// handleMessage processes a single inbound message.
func (w *WeixinChannel) handleMessage(ctx context.Context, msg weixinInboundMsg, dispatch DispatchFunc) error {
	// Only handle user messages (message_type=1).
	if msg.MessageType != 1 {
		return nil
	}

	// Extract text from item_list.
	var text string
	for _, item := range msg.ItemList {
		if item.Type == 1 && item.TextItem.Text != "" {
			text = item.TextItem.Text
			break
		}
	}
	if text == "" {
		return nil
	}

	// Store session for reply.
	w.mu.Lock()
	w.sessions[msg.FromUserID] = weixinSession{
		fromUserID:   msg.FromUserID,
		contextToken: msg.ContextToken,
	}
	w.mu.Unlock()

	w.log.Info("weixin: message received",
		"from", msg.FromUserID,
		"text", text,
	)

	inbound := InboundMessage{
		ChannelName: w.id,
		ChannelType: "weixin",
		SessionKey:  msg.FromUserID,
		ChatID:      msg.FromUserID,
		UserID:      msg.FromUserID,
		Username:    msg.FromUserID,
		Text:        text,
		Timestamp:   time.Now(),
	}

	go dispatch(ctx, inbound)
	return nil
}

// ── Outbound reply ────────────────────────────────────────────────────────────

// sendMessage posts a text reply to a WeChat user.
func (w *WeixinChannel) sendMessage(ctx context.Context, toUserID, contextToken, text string) error {
	body, _ := json.Marshal(map[string]any{
		"msg": map[string]any{
			"to_user_id":    toUserID,
			"message_type":  2,
			"message_state": 2,
			"context_token": contextToken,
			"item_list": []map[string]any{
				{
					"type":      1,
					"text_item": map[string]string{"text": text},
				},
			},
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.baseURL+"/ilink/bot/sendmessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	w.setHeaders(req)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sendmessage: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("sendmessage status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// setHeaders adds the required WeChat iLink Bot headers to a request.
// X-WECHAT-UIN must be a fresh random value per request.
func (w *WeixinChannel) setHeaders(req *http.Request) {
	uin := rand.Uint32()
	uinStr := fmt.Sprintf("%d", uin)
	uinB64 := base64.StdEncoding.EncodeToString([]byte(uinStr))

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("X-WECHAT-UIN", uinB64)
	req.Header.Set("Authorization", "Bearer "+w.botToken)
}
