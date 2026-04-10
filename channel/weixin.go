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
	"crypto/aes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
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

// errWeixinTokenExpired is returned by getUpdates when the server signals that
// the bot_token is no longer valid (ret=-14). The caller must delete the token
// file and re-run QR login.
var errWeixinTokenExpired = fmt.Errorf("weixin: token expired (ret=-14), re-login required")

// weixinTokenData is persisted to disk after a successful QR login.
type weixinTokenData struct {
	BotToken   string `json:"bot_token"`
	BaseURL    string `json:"baseurl"`
	BotID      string `json:"ilink_bot_id,omitempty"`
	UserID     string `json:"ilink_user_id,omitempty"`
}

// weixinSession holds the reply context for a single WeChat user.
type weixinSession struct {
	fromUserID   string
	contextToken string
}

// weixinTypingEntry caches the typing_ticket for one user.
type weixinTypingEntry struct {
	ticket    string
	fetchedAt time.Time
}

// typingFlight represents an in-flight typing ticket fetch for one user.
type typingFlight struct {
	done   chan struct{}
	ticket string
}

// WeixinChannel implements Channel using WeChat's iLink Bot API.
type WeixinChannel struct {
	id              string
	tokenFile       string
	log             *slog.Logger
	httpClient      *http.Client // used for QR login and outbound replies
	mediaHTTPClient *http.Client // used for CDN image downloads (longer timeout)

	running atomic.Bool

	// populated after login
	botToken string
	baseURL  string

	mu       sync.RWMutex
	sessions map[string]weixinSession // key = from_user_id

	// typingMu guards typingTickets, typingActive, and typingInFlight.
	typingMu        sync.Mutex
	typingTickets   map[string]*weixinTypingEntry // key = fromUserID, TTL 24h
	typingActive    map[string]string             // key = fromUserID, value = ticket
	typingInFlight  map[string]*typingFlight      // key = fromUserID, singleflight dedup
}

// NewWeixinChannel creates a WeixinChannel.
// tokenFile is the path to persist the bot_token; use dirs.WeixinTokenFile() for the default.
func NewWeixinChannel(id, tokenFile string, log *slog.Logger) *WeixinChannel {
	if log == nil {
		log = slog.Default()
	}
	return &WeixinChannel{
		id:              id,
		tokenFile:       tokenFile,
		log:             log,
		httpClient:      &http.Client{Timeout: 15 * time.Second},
		mediaHTTPClient: &http.Client{Timeout: 60 * time.Second}, // CDN image downloads can be large
		sessions:       make(map[string]weixinSession),
		typingTickets:  make(map[string]*weixinTypingEntry),
		typingActive:   make(map[string]string),
		typingInFlight: make(map[string]*typingFlight),
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
			if err == errWeixinTokenExpired {
				w.log.Warn("weixin: token expired, re-running QR login")
				if loginErr := w.ensureToken(ctx); loginErr != nil {
					return fmt.Errorf("weixin: re-login failed: %w", loginErr)
				}
				backoff = time.Second
				continue
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
// After sending, cancels any active typing indicator for this chat.
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

	if err := w.sendMessage(ctx, sess.fromUserID, sess.contextToken, stripMarkdown(msg.Text)); err != nil {
		return err
	}

	// Cancel typing indicator now that the reply has been sent.
	w.typingMu.Lock()
	ticket := w.typingActive[msg.ChatID]
	delete(w.typingActive, msg.ChatID)
	w.typingMu.Unlock()
	if ticket != "" {
		go w.sendTyping(ctx, msg.ChatID, ticket, 2)
	}

	return nil
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

// fetchQRCode requests a fresh QR code from the iLink server.
func (w *WeixinChannel) fetchQRCode(ctx context.Context) (qrcode string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, weixinQRCodeURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := w.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("get QR code: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	var qrResp struct {
		QRCode string `json:"qrcode"`
	}
	if err := json.Unmarshal(body, &qrResp); err != nil {
		return "", fmt.Errorf("parse QR response: %w (body: %s)", err, body)
	}
	if qrResp.QRCode == "" {
		return "", fmt.Errorf("parse QR response: empty qrcode (body: %s)", body)
	}
	return qrResp.QRCode, nil
}

// qrLogin fetches a QR code, displays it, and polls until the user scans it.
// Handles qrcode expiry by fetching a fresh one (up to 3 times).
func (w *WeixinChannel) qrLogin(ctx context.Context) error {
	w.log.Info("weixin: no token found, starting QR login")

	const maxRefresh = 3
	refreshes := 0

	qrcode, err := w.fetchQRCode(ctx)
	if err != nil {
		return err
	}
	w.printQRCode(qrcode)

	// Poll for scan confirmation.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}

		statusURL := weixinQRStatusURL + "?qrcode=" + url.QueryEscape(qrcode)
		sreq, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
		if err != nil {
			continue
		}
		sresp, err := w.httpClient.Do(sreq)
		if err != nil {
			continue
		}
		sbody, _ := io.ReadAll(io.LimitReader(sresp.Body, 4096))
		sresp.Body.Close()

		var statusResp struct {
			Status     string `json:"status"`
			BotToken   string `json:"bot_token"`
			BaseURL    string `json:"baseurl"`
			ILinkBotID string `json:"ilink_bot_id"`
			ILinkUserID string `json:"ilink_user_id"`
		}
		if err := json.Unmarshal(sbody, &statusResp); err != nil {
			continue
		}

		switch statusResp.Status {
		case "wait":
			// still waiting — do nothing
		case "scaned":
			fmt.Println("已扫码，请在微信端确认...")
		case "expired":
			refreshes++
			if refreshes > maxRefresh {
				return fmt.Errorf("weixin: QR code expired %d times, giving up", maxRefresh)
			}
			w.log.Info("weixin: QR code expired, fetching new one", "attempt", refreshes)
			qrcode, err = w.fetchQRCode(ctx)
			if err != nil {
				return err
			}
			w.printQRCode(qrcode)
		case "confirmed":
			// Confirmed — save token.
			w.botToken = statusResp.BotToken
			w.baseURL = statusResp.BaseURL
			if w.baseURL == "" {
				w.baseURL = weixinDefaultBase
			}

			td := weixinTokenData{
				BotToken: w.botToken,
				BaseURL:  w.baseURL,
				BotID:    statusResp.ILinkBotID,
				UserID:   statusResp.ILinkUserID,
			}
			tdJSON, _ := json.Marshal(td)
			if err := os.WriteFile(w.tokenFile, tdJSON, 0o600); err != nil {
				w.log.Warn("weixin: could not save token", "err", err)
			} else {
				w.log.Info("weixin: token saved", "file", w.tokenFile,
					"bot_id", statusResp.ILinkBotID)
			}

			fmt.Println("✓ 微信登录成功！")
			return nil
		}
	}
}

// printQRCode renders the QR code string in the terminal.
func (w *WeixinChannel) printQRCode(qrcode string) {
	fmt.Println("\n请用微信扫描以下二维码登录 WeChat Bot:")
	qrterminal.GenerateWithConfig(qrcode, qrterminal.Config{
		Level:     qrterminal.L,
		Writer:    os.Stdout,
		BlackChar: qrterminal.BLACK,
		WhiteChar: qrterminal.WHITE,
		QuietZone: 1,
	})
	fmt.Println()
}

const weixinTypingTicketTTL = 24 * time.Hour

// getTypingTicket returns the cached typing_ticket for a user, fetching it
// from /ilink/bot/getconfig if the cache is empty or expired.
// Returns ("", nil) on any error — callers must treat empty ticket as "skip typing".
// Uses singleflight deduplication so concurrent callers for the same user share
// one HTTP request instead of each issuing their own.
func (w *WeixinChannel) getTypingTicket(ctx context.Context, fromUserID, contextToken string) (string, error) {
	w.typingMu.Lock()
	// Fast path: valid cached entry.
	if entry := w.typingTickets[fromUserID]; entry != nil && time.Since(entry.fetchedAt) < weixinTypingTicketTTL {
		ticket := entry.ticket
		w.typingMu.Unlock()
		return ticket, nil
	}
	// Singleflight: if a fetch is already in progress for this user, wait for it.
	if flight, ok := w.typingInFlight[fromUserID]; ok {
		w.typingMu.Unlock()
		<-flight.done
		return flight.ticket, nil
	}
	// Start a new fetch; register the in-flight entry so other goroutines wait.
	flight := &typingFlight{done: make(chan struct{})}
	w.typingInFlight[fromUserID] = flight
	w.typingMu.Unlock()

	// Fetch outside the lock so concurrent users are not serialized.
	var ticket string
	func() {
		body := mustMarshal(map[string]any{
			"ilink_user_id": fromUserID,
			"context_token": contextToken,
			"base_info":     map[string]string{"channel_version": "1.0.2"},
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.baseURL+"/ilink/bot/getconfig", bytes.NewReader(body))
		if err != nil {
			return
		}
		w.setHeaders(req)
		resp, err := w.httpClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var result struct {
			Ret          int    `json:"ret"`
			TypingTicket string `json:"typing_ticket"`
		}
		if err := json.Unmarshal(respBody, &result); err != nil || result.Ret != 0 {
			return
		}
		ticket = result.TypingTicket
	}()

	// Store result and notify waiters.
	w.typingMu.Lock()
	if ticket != "" {
		w.typingTickets[fromUserID] = &weixinTypingEntry{
			ticket:    ticket,
			fetchedAt: time.Now(),
		}
	}
	flight.ticket = ticket
	delete(w.typingInFlight, fromUserID)
	close(flight.done)
	w.typingMu.Unlock()

	return ticket, nil
}

// sendTyping sends a typing indicator to a user.
// status: 1 = start typing, 2 = cancel typing.
// Errors are non-fatal and only logged.
func (w *WeixinChannel) sendTyping(ctx context.Context, fromUserID, ticket string, status int) {
	if ticket == "" {
		return
	}
	body := mustMarshal(map[string]any{
		"ilink_user_id": fromUserID,
		"typing_ticket": ticket,
		"status":        status,
		"base_info":     map[string]string{"channel_version": "1.0.2"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.baseURL+"/ilink/bot/sendtyping", bytes.NewReader(body))
	if err != nil {
		w.log.Warn("weixin: sendtyping request build failed", "err", err)
		return
	}
	w.setHeaders(req)
	resp, err := w.httpClient.Do(req)
	if err != nil {
		w.log.Warn("weixin: sendtyping failed", "err", err)
		return
	}
	resp.Body.Close()
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
	FromUserID   string              `json:"from_user_id"`
	ToUserID     string              `json:"to_user_id"`
	MessageType  int                 `json:"message_type"`
	ContextToken string              `json:"context_token"`
	ItemList     []weixinMessageItem `json:"item_list"`
}

// weixinRefMessage is a quoted/referenced message in a WeChat item.
type weixinRefMessage struct {
	Title       string             `json:"title"`        // summary/title of the quoted message
	MessageItem *weixinMessageItem `json:"message_item"` // the quoted message item (recursive)
}

// weixinMessageItem is a single item in the item_list of a WeChat message.
type weixinMessageItem struct {
	Type     int `json:"type"` // 1=text, 2=image, 4=voice, 6=file
	TextItem struct {
		Text string `json:"text"`
	} `json:"text_item"`
	ImageItem *weixinImageItem  `json:"image_item"`
	RefMsg    *weixinRefMessage `json:"ref_msg"` // quoted message, if present
}

// weixinImageItem holds CDN reference and AES key for a WeChat image.
type weixinImageItem struct {
	Media *weixinCDNMedia `json:"media"`
	// AESKey is the raw AES-128 key as a hex string (32 chars = 16 bytes).
	// Preferred over Media.AESKey for inbound decryption when present.
	AESKey string `json:"aeskey"`
}

// weixinCDNMedia is a CDN media reference from WeChat.
type weixinCDNMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param"`
	AESKey            string `json:"aes_key"` // base64-encoded; see parseWeixinAESKey
}

// parseWeixinAESKey parses a WeChat CDN AES key from two possible encodings:
//   - base64(raw 16 bytes)         → images (aes_key field)
//   - hex string (32 chars)        → from image_item.aeskey field
//   - base64(hex string 32 chars)  → voice/file/video
func parseWeixinAESKey(s string) ([]byte, error) {
	// Try hex first (image_item.aeskey is a raw hex string).
	if len(s) == 32 {
		if b, err := hex.DecodeString(s); err == nil && len(b) == 16 {
			return b, nil
		}
	}
	// Try base64.
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("parseWeixinAESKey: base64 decode: %w", err)
	}
	if len(decoded) == 16 {
		return decoded, nil // base64(raw 16 bytes)
	}
	// base64(hex string of 16 bytes) → 32 ASCII hex chars.
	if len(decoded) == 32 {
		b, err := hex.DecodeString(string(decoded))
		if err == nil && len(b) == 16 {
			return b, nil
		}
	}
	return nil, fmt.Errorf("parseWeixinAESKey: unexpected decoded length %d", len(decoded))
}

// decryptAES128ECB decrypts AES-128-ECB ciphertext and removes PKCS7 padding.
func decryptAES128ECB(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}
	bs := block.BlockSize() // 16
	if len(ciphertext) == 0 || len(ciphertext)%bs != 0 {
		return nil, fmt.Errorf("ciphertext length %d is not a multiple of block size", len(ciphertext))
	}
	plaintext := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += bs {
		block.Decrypt(plaintext[i:i+bs], ciphertext[i:i+bs])
	}
	// PKCS7 unpad.
	padLen := int(plaintext[len(plaintext)-1])
	if padLen == 0 || padLen > bs {
		return nil, fmt.Errorf("invalid PKCS7 padding byte %d", padLen)
	}
	for i := len(plaintext) - padLen; i < len(plaintext); i++ {
		if plaintext[i] != byte(padLen) {
			return nil, fmt.Errorf("invalid PKCS7 padding content at byte %d", i)
		}
	}
	return plaintext[:len(plaintext)-padLen], nil
}

// downloadWeixinCDNImage downloads and decrypts a WeChat CDN image to a temp file.
func (w *WeixinChannel) downloadWeixinCDNImage(ctx context.Context, item *weixinImageItem) (string, error) {
	if item == nil || item.Media == nil || item.Media.EncryptQueryParam == "" {
		return "", nil
	}

	// Resolve AES key: prefer item.AESKey (hex), fall back to media.AESKey (base64).
	aesKeyStr := item.AESKey
	if aesKeyStr == "" {
		aesKeyStr = item.Media.AESKey
	}
	if aesKeyStr == "" {
		return "", fmt.Errorf("no AES key for image")
	}
	key, err := parseWeixinAESKey(aesKeyStr)
	if err != nil {
		return "", fmt.Errorf("parse AES key: %w", err)
	}

	// Build CDN download URL.
	cdnURL := w.baseURL + "/cgi-bin/mmbiz_getcdndata?encrypt_fileid=" + item.Media.EncryptQueryParam

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cdnURL, nil)
	if err != nil {
		return "", err
	}
	w.setHeaders(req)
	resp, err := w.mediaHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("cdn download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cdn download status %d", resp.StatusCode)
	}
	encrypted, err := io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read cdn body: %w", err)
	}

	plaintext, err := decryptAES128ECB(encrypted, key)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	// Detect image format from magic bytes to use the correct extension.
	ext := ".jpg"
	if len(plaintext) >= 8 {
		switch {
		case len(plaintext) >= 8 &&
			plaintext[0] == 0x89 && plaintext[1] == 0x50 && plaintext[2] == 0x4e && plaintext[3] == 0x47:
			ext = ".png"
		case len(plaintext) >= 6 &&
			plaintext[0] == 0x47 && plaintext[1] == 0x49 && plaintext[2] == 0x46:
			ext = ".gif"
		case len(plaintext) >= 12 &&
			plaintext[0] == 0x52 && plaintext[1] == 0x49 && plaintext[2] == 0x46 && plaintext[3] == 0x46 &&
			plaintext[8] == 0x57 && plaintext[9] == 0x45 && plaintext[10] == 0x42 && plaintext[11] == 0x50:
			ext = ".webp"
		}
	}

	f, err := os.CreateTemp("", "claw-wx-*"+ext)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(plaintext); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("write temp file: %w", err)
	}
	return f.Name(), nil
}

// getUpdates calls /ilink/bot/getupdates and returns parsed messages and the new cursor.
func (w *WeixinChannel) getUpdates(ctx context.Context, client *http.Client, cursor string) ([]weixinInboundMsg, string, error) {
	body := mustMarshal(map[string]any{
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
		if result.Ret == -14 {
			// Token expired — delete the token file so ensureToken triggers QR login.
			if w.tokenFile != "" {
				_ = os.Remove(w.tokenFile)
			}
			return nil, "", errWeixinTokenExpired
		}
		return nil, "", fmt.Errorf("getupdates ret=%d body=%s", result.Ret, respBody)
	}

	return result.Msgs, result.GetUpdatesBuf, nil
}

// extractRefText extracts displayable text from a ref_msg for context.
// Returns empty string if there is nothing useful to show.
func extractRefText(ref *weixinRefMessage) string {
	if ref == nil {
		return ""
	}
	var parts []string
	if ref.Title != "" {
		parts = append(parts, ref.Title)
	}
	if ref.MessageItem != nil && ref.MessageItem.Type == 1 {
		if t := strings.TrimSpace(ref.MessageItem.TextItem.Text); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "[引用: " + strings.Join(parts, " | ") + "]"
}

// stripMarkdown converts common markdown to plain text suitable for WeChat.
// WeChat does not render markdown, so we strip formatting characters.
func stripMarkdown(text string) string {
	// Remove code fences.
	text = strings.ReplaceAll(text, "```", "")
	// Remove bold/italic markers.
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "__", "")
	// Remove inline code.
	text = strings.ReplaceAll(text, "`", "")
	// Convert ATX headings (# Foo → Foo).
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, "#")
		if len(trimmed) < len(line) && (len(trimmed) == 0 || trimmed[0] == ' ') {
			lines[i] = strings.TrimSpace(trimmed)
		}
	}
	return strings.Join(lines, "\n")
}

// handleMessage processes a single inbound message.
func (w *WeixinChannel) handleMessage(ctx context.Context, msg weixinInboundMsg, dispatch DispatchFunc) error {
	// message_type=1: user message (text or mixed with images)
	// message_type=2: bot-sent message — ignore
	if msg.MessageType != 1 {
		return nil
	}

	var text string
	var attachments []Attachment

	for _, item := range msg.ItemList {
		switch item.Type {
		case 1: // text
			if item.TextItem.Text != "" && text == "" {
				userText := strings.TrimSpace(item.TextItem.Text)
				if refText := extractRefText(item.RefMsg); refText != "" {
					text = refText + "\n" + userText
				} else {
					text = userText
				}
			}
		case 2: // image
			path, err := w.downloadWeixinCDNImage(ctx, item.ImageItem)
			if err != nil {
				w.log.Warn("weixin: image download/decrypt failed", "err", err)
			} else if path != "" {
				attachments = append(attachments, Attachment{
					Path:     path,
					MimeType: mimeFromPath(path),
					AltText:  "[图片]",
				})
			}
		}
	}

	if text == "" && len(attachments) == 0 {
		return nil
	}

	// Store session for reply.
	w.mu.Lock()
	if len(w.sessions) >= maxSessions {
		n := len(w.sessions) / 2
		toDelete := make([]string, 0, n)
		for k := range w.sessions {
			toDelete = append(toDelete, k)
			if len(toDelete) == n {
				break
			}
		}
		for _, k := range toDelete {
			delete(w.sessions, k)
		}
	}
	w.sessions[msg.FromUserID] = weixinSession{
		fromUserID:   msg.FromUserID,
		contextToken: msg.ContextToken,
	}
	w.mu.Unlock()

	w.log.Info("weixin: message received",
		"from", msg.FromUserID,
		"text", text,
		"images", len(attachments),
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
		Attachments: attachments,
	}

	ticket, _ := w.getTypingTicket(ctx, msg.FromUserID, msg.ContextToken)
	if ticket != "" {
		w.typingMu.Lock()
		w.typingActive[msg.FromUserID] = ticket
		w.typingMu.Unlock()
		w.sendTyping(ctx, msg.FromUserID, ticket, 1)
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				w.log.Error("weixin: dispatch panic recovered", "panic", r)
			}
		}()
		dispatch(ctx, inbound)
	}()
	return nil
}

// ── Outbound reply ────────────────────────────────────────────────────────────

// randomClientID returns a short random hex string used as client_id in sendmessage.
func randomClientID() string {
	b := make([]byte, 8)
	rand.Read(b) //nolint:errcheck // crypto/rand.Read never returns an error on supported platforms
	return hex.EncodeToString(b)
}

// sendMessage posts a text reply to a WeChat user.
func (w *WeixinChannel) sendMessage(ctx context.Context, toUserID, contextToken, text string) error {
	body := mustMarshal(map[string]any{
		"msg": map[string]any{
			"from_user_id":  "",
			"to_user_id":    toUserID,
			"client_id":     randomClientID(),
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

	resp, err := w.httpClient.Do(req)
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
// X-WECHAT-UIN: random uint32 → decimal string → base64, fresh per request.
func (w *WeixinChannel) setHeaders(req *http.Request) {
	var b [4]byte
	rand.Read(b[:]) //nolint:errcheck
	uin := binary.BigEndian.Uint32(b[:])
	uinStr := fmt.Sprintf("%d", uin)
	uinB64 := base64.StdEncoding.EncodeToString([]byte(uinStr))

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("X-WECHAT-UIN", uinB64)
	req.Header.Set("Authorization", "Bearer "+w.botToken)
}
