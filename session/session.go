// Package session provides a conversation session store with optional
// disk persistence.  Each named session is saved as a JSON file under
// the configured sessions directory so that conversation history survives
// daemon restarts.
package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/XHao/claw-go/provider"
)

// MainSessionKey is the name of the built-in session that the daemon
// creates automatically on startup.  It cannot be deleted, but its history
// can be cleared with /reset.
const MainSessionKey = "main"

const (
	historySummaryPrefix       = "[History summary]\n"
	defaultPerTurnTokenBudget  = 1200
	minTrimTokenBudget         = 2000
	defaultCharsPerToken       = 4.0
	defaultRouterBudgetScale   = 0.35
	defaultTaskBudgetScale     = 1.0
	defaultSummaryBudgetScale  = 0.85
	defaultThinkingBudgetScale = 1.5
	adaptiveCharsPerTokenAlpha = 0.2
	minAdaptiveCharsPerToken   = 1.5
	maxAdaptiveCharsPerToken   = 8.0
	maxSummaryLines            = 80
	maxSummaryChars            = 6000
)

// Session holds the conversation history for a single named conversation.
type Session struct {
	Key     string
	Name    string // human-readable name chosen by the user
	history []provider.Message
	mu      sync.Mutex
	store   *Store // back-reference for auto-save
}

type historyBlock struct {
	messages []provider.Message
}

type trimSettings struct {
	maxTurns      int
	budget        int
	keepRecent    int
	charsPerToken float64
}

// TurnCount returns the number of completed dialogue turns (user+assistant pairs).
func (s *Session) TurnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, m := range s.history {
		if m.Role == "user" {
			count++
		}
	}
	return count
}

// Summary contains the public metadata of a session.
type Summary struct {
	Name      string
	TurnCount int
}

// History returns a copy of the current message history.
func (s *Session) History() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]provider.Message, len(s.history))
	copy(out, s.history)
	return out
}

// HistoryForLLM returns a hint-aware, budget-trimmed view of the session
// history for a provider call. It never mutates the stored history on disk.
func (s *Session) HistoryForLLM(hint provider.ModelHint) []provider.Message {
	history := s.History()
	settings := trimSettings{maxTurns: 0}
	if s.store != nil {
		settings = s.store.resolveTrimSettings(hint)
	}
	return trimMessages(history, settings)
}

// Append adds a message to the session history, trims it if needed, then
// persists the session to disk (best-effort; errors are logged, not fatal).
func (s *Session) Append(msg provider.Message, maxTurns int) {
	s.mu.Lock()
	s.history = append(s.history, msg)
	settings := trimSettings{maxTurns: maxTurns}
	if s.store != nil {
		settings = s.store.resolveBaseTrimSettings(maxTurns)
	}
	s.history = trimMessages(s.history, settings)
	s.mu.Unlock()
	// Save outside the lock so readers are not blocked.
	if s.store != nil {
		s.store.save(s)
	}
}

func trimMessages(history []provider.Message, settings trimSettings) []provider.Message {
	if settings.maxTurns <= 0 {
		out := make([]provider.Message, len(history))
		copy(out, history)
		return out
	}
	var system []provider.Message
	var existingSummary []string
	var rest []provider.Message
	for _, m := range history {
		if m.Role == "system" {
			if lines, ok := parseSummaryLines(m.Content); ok {
				existingSummary = append(existingSummary, lines...)
			} else {
				system = append(system, m)
			}
		} else {
			rest = append(rest, m)
		}
	}

	turns := splitIntoTurnBlocks(rest)
	if len(turns) == 0 {
		return append(system, buildSummaryMessage(existingSummary)...)
	}

	var summaryLines []string
	summaryLines = append(summaryLines, existingSummary...)

	if len(turns) > settings.maxTurns {
		dropped := turns[:len(turns)-settings.maxTurns]
		turns = turns[len(turns)-settings.maxTurns:]
		for _, b := range dropped {
			summaryLines = append(summaryLines, summarizeBlock(b))
		}
	}

	budget := settings.budget
	if budget <= 0 {
		budget = minTrimTokenBudget
	}
	keepRecent := settings.keepRecent
	if keepRecent < 1 {
		keepRecent = 1
	}
	charsPerToken := settings.charsPerToken
	if charsPerToken <= 0 {
		charsPerToken = defaultCharsPerToken
	}

	for len(turns) > keepRecent {
		candidate := composeHistory(system, summaryLines, turns)
		if estimateMessagesTokens(candidate, charsPerToken) <= budget {
			break
		}
		dropIdx := selectTrimCandidate(turns, keepRecent)
		summaryLines = append(summaryLines, summarizeBlock(turns[dropIdx]))
		turns = removeBlockAt(turns, dropIdx)
	}

	return composeHistory(system, summaryLines, turns)
}

func selectTrimCandidate(turns []historyBlock, keepRecent int) int {
	limit := len(turns) - keepRecent
	if limit <= 0 {
		return 0
	}
	for i := 0; i < limit; i++ {
		if !isToolHeavyBlock(turns[i]) {
			return i
		}
	}
	return 0
}

func removeBlockAt(turns []historyBlock, idx int) []historyBlock {
	if idx < 0 || idx >= len(turns) {
		return turns
	}
	trimmed := make([]historyBlock, 0, len(turns)-1)
	trimmed = append(trimmed, turns[:idx]...)
	trimmed = append(trimmed, turns[idx+1:]...)
	return trimmed
}

func isToolHeavyBlock(b historyBlock) bool {
	for _, m := range b.messages {
		if m.Role == "tool" {
			return true
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

func splitIntoTurnBlocks(messages []provider.Message) []historyBlock {
	if len(messages) == 0 {
		return nil
	}
	var blocks []historyBlock
	var current []provider.Message

	flush := func() {
		if len(current) == 0 {
			return
		}
		copied := make([]provider.Message, len(current))
		copy(copied, current)
		blocks = append(blocks, historyBlock{messages: copied})
		current = nil
	}

	for _, m := range messages {
		if m.Role == "user" && len(current) > 0 {
			flush()
		}
		current = append(current, m)
	}
	flush()
	return blocks
}

func composeHistory(system []provider.Message, summaryLines []string, turns []historyBlock) []provider.Message {
	out := make([]provider.Message, 0, len(system)+1+len(turns)*3)
	out = append(out, system...)
	out = append(out, buildSummaryMessage(summaryLines)...)
	for _, b := range turns {
		out = append(out, b.messages...)
	}
	return out
}

func parseSummaryLines(content string) ([]string, bool) {
	if !strings.HasPrefix(content, historySummaryPrefix) {
		return nil, false
	}
	body := strings.TrimPrefix(content, historySummaryPrefix)
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, true
	}
	parts := strings.Split(body, "\n")
	lines := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			lines = append(lines, p)
		}
	}
	return lines, true
}

func buildSummaryMessage(lines []string) []provider.Message {
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > maxSummaryLines {
		lines = lines[len(lines)-maxSummaryLines:]
	}
	content := historySummaryPrefix + strings.Join(lines, "\n")
	if len(content) > maxSummaryChars {
		content = content[len(content)-maxSummaryChars:]
		content = historySummaryPrefix + strings.TrimLeft(content, "\n")
	}
	return []provider.Message{{Role: "system", Content: content}}
}

func summarizeBlock(b historyBlock) string {
	user := ""
	assistant := ""
	toolSet := map[string]struct{}{}

	for _, m := range b.messages {
		switch m.Role {
		case "user":
			if user == "" {
				user = compactText(m.Content, 80)
			}
		case "assistant":
			if m.Content != "" {
				assistant = compactText(m.Content, 80)
			}
			for _, tc := range m.ToolCalls {
				if tc.Function.Name != "" {
					toolSet[tc.Function.Name] = struct{}{}
				}
			}
		case "tool":
			if m.Name != "" {
				toolSet[m.Name] = struct{}{}
			}
		}
	}
	if user == "" {
		user = "(no-user)"
	}
	if assistant == "" {
		assistant = "(no-assistant)"
	}
	tools := make([]string, 0, len(toolSet))
	for t := range toolSet {
		tools = append(tools, t)
	}
	sort.Strings(tools)
	if len(tools) == 0 {
		return fmt.Sprintf("- U:%s | A:%s", user, assistant)
	}
	return fmt.Sprintf("- U:%s | A:%s | T:%s", user, assistant, strings.Join(tools, ","))
}

func compactText(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if s == "" {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

func estimateMessagesTokens(messages []provider.Message, charsPerToken float64) int {
	total := 0
	for _, m := range messages {
		total += estimateMessageTokens(m, charsPerToken)
	}
	return total
}

func estimateMessageTokens(m provider.Message, charsPerToken float64) int {
	if charsPerToken <= 0 {
		charsPerToken = defaultCharsPerToken
	}
	chars := len(m.Content)
	for _, tc := range m.ToolCalls {
		chars += len(tc.ID) + len(tc.Type) + len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	chars += len(m.ToolCallID) + len(m.Name) + len(m.Role)
	// Cheap token estimate: configurable chars/token plus small per-message overhead.
	return int(float64(chars)/charsPerToken) + 8
}

// ── Persistence helpers ───────────────────────────────────────────────────────

// sessionFile is the on-disk representation of a Session.
type sessionFile struct {
	Key     string             `json:"key"`
	Name    string             `json:"name"`
	History []provider.Message `json:"history"`
}

// Store is a concurrency-safe map from session key to *Session.
type Store struct {
	sessions       sync.Map
	settingsMu     sync.RWMutex
	maxTurns       int
	tokenBudget    int
	recentRaw      int
	charsPerToken  float64
	fixedEstimator bool
	routerScale    float64
	taskScale      float64
	summaryScale   float64
	thinkingScale  float64
	systemPrompt   string
	dir            string       // sessions directory; "" = no persistence
	log            *slog.Logger // may be nil
}

// NewStore creates a Store with the given history limit, system prompt, and
// optional sessions directory.  When dir is non-empty, existing session files
// are loaded immediately and new writes are persisted automatically.
func NewStore(maxTurns int, systemPrompt, dir string) *Store {
	st := &Store{
		maxTurns:      maxTurns,
		tokenBudget:   maxTurns * defaultPerTurnTokenBudget,
		recentRaw:     defaultRecentRawTurns(maxTurns),
		charsPerToken: defaultCharsPerToken,
		routerScale:   defaultRouterBudgetScale,
		taskScale:     defaultTaskBudgetScale,
		summaryScale:  defaultSummaryBudgetScale,
		thinkingScale: defaultThinkingBudgetScale,
		systemPrompt:  systemPrompt,
		dir:           dir,
		log:           slog.Default(),
	}
	if dir != "" {
		st.loadAll()
	}
	return st
}

// Get returns the session for the given key, creating it if needed.
func (st *Store) Get(key string) *Session {
	sess := &Session{Key: key, Name: key, store: st}
	v, loaded := st.sessions.LoadOrStore(key, sess)
	sess = v.(*Session)
	if !loaded {
		// Brand-new session: inject system prompt and persist.
		sess.mu.Lock()
		if st.systemPrompt != "" {
			sess.history = []provider.Message{
				{Role: "system", Content: st.systemPrompt},
			}
		}
		sess.mu.Unlock()
		st.save(sess)
	}
	return sess
}

// Has reports whether a session with the given name already exists.
func (st *Store) Has(name string) bool {
	_, ok := st.sessions.Load(name)
	return ok
}

// Create creates a new named session. Returns an error if the name is already taken.
func (st *Store) Create(name string) (*Session, error) {
	sess := &Session{Key: name, Name: name, store: st}
	if st.systemPrompt != "" {
		sess.history = []provider.Message{
			{Role: "system", Content: st.systemPrompt},
		}
	}
	_, loaded := st.sessions.LoadOrStore(name, sess)
	if loaded {
		return nil, fmt.Errorf("conversation %q already exists", name)
	}
	st.save(sess)
	return sess, nil
}

// List returns a summary of all stored sessions.
func (st *Store) List() []Summary {
	var out []Summary
	st.sessions.Range(func(_, v any) bool {
		sess := v.(*Session)
		out = append(out, Summary{Name: sess.Name, TurnCount: sess.TurnCount()})
		return true
	})
	return out
}

// Delete removes a session from memory and deletes its file (if any).
// The main session (MainSessionKey) is protected and cannot be deleted;
// use ClearHistory to reset its content instead.
func (st *Store) Delete(key string) {
	if key == MainSessionKey {
		return // main session is permanent; caller should use ClearHistory
	}
	st.sessions.Delete(key)
	if st.dir != "" {
		_ = os.Remove(st.filePath(key))
	}
}

// ClearHistory resets a session's message history to the initial system
// prompt (if any) without removing the session itself.  Used to reset the
// main session which cannot be deleted.
func (st *Store) ClearHistory(key string) {
	v, ok := st.sessions.Load(key)
	if !ok {
		return
	}
	sess := v.(*Session)
	sess.mu.Lock()
	if st.systemPrompt != "" {
		sess.history = []provider.Message{{Role: "system", Content: st.systemPrompt}}
	} else {
		sess.history = nil
	}
	sess.mu.Unlock()
	st.save(sess)
}

// InjectContext appends an extra system-role message carrying the given
// content into the session history.  This is used by the /exp use command
// to mount an experience document as additional context for the LLM.
// The injected message is marked with a special prefix so it can be
// identified (but not to be removed automatically — it stays for the
// session lifetime unless /reset is called).
func (st *Store) InjectContext(key, content string) {
	sess := st.Get(key) // creates if missing
	sess.Append(provider.Message{
		Role:    "system",
		Content: "[Injected experience context]\n" + content,
	}, 0) // maxTurns=0 → never trim system messages
}

// Keys returns all active session keys.
func (st *Store) Keys() []string {
	var keys []string
	st.sessions.Range(func(k, _ any) bool {
		keys = append(keys, k.(string))
		return true
	})
	return keys
}

// MaxTurns returns the configured history turn limit.
func (st *Store) MaxTurns() int { return st.maxTurns }

// SetTokenBudget sets an approximate prompt token budget used by history trim.
// Values <= 0 disable explicit override and fall back to maxTurns-derived budget.
func (st *Store) SetTokenBudget(tokens int) {
	st.settingsMu.Lock()
	defer st.settingsMu.Unlock()
	st.tokenBudget = tokens
}

// SetRecentRawTurns sets how many newest turns are always kept verbatim.
// Values <= 0 use a default derived from max_history_turns.
func (st *Store) SetRecentRawTurns(turns int) {
	st.settingsMu.Lock()
	defer st.settingsMu.Unlock()
	st.recentRaw = turns
}

// SetCharsPerToken sets the token estimator scale used by history trimming.
// Values <= 0 reset to the default heuristic.
func (st *Store) SetCharsPerToken(chars float64) {
	st.settingsMu.Lock()
	defer st.settingsMu.Unlock()
	st.charsPerToken = chars
	st.fixedEstimator = chars > 0
}

// SetHintBudgetScale overrides the per-hint LLM history budget multiplier.
// Values <= 0 restore the default scale for that hint.
func (st *Store) SetHintBudgetScale(hint provider.ModelHint, scale float64) {
	st.settingsMu.Lock()
	defer st.settingsMu.Unlock()
	if scale <= 0 {
		scale = defaultBudgetScaleForHint(hint)
	}
	switch hint {
	case provider.ModelHintRouter:
		st.routerScale = scale
	case provider.ModelHintSummary:
		st.summaryScale = scale
	case provider.ModelHintThinking:
		st.thinkingScale = scale
	default:
		st.taskScale = scale
	}
}

// ObservePromptUsage updates the automatic chars/token estimator using
// observed prompt token usage from actual provider calls. It is ignored when
// the estimator has been explicitly fixed via SetCharsPerToken.
func (st *Store) ObservePromptUsage(messages []provider.Message, promptTokens int) {
	if promptTokens <= 0 {
		return
	}
	chars := measuredMessageChars(messages)
	if chars <= 0 {
		return
	}
	observed := float64(chars) / float64(promptTokens)
	if observed < minAdaptiveCharsPerToken {
		observed = minAdaptiveCharsPerToken
	}
	if observed > maxAdaptiveCharsPerToken {
		observed = maxAdaptiveCharsPerToken
	}

	st.settingsMu.Lock()
	defer st.settingsMu.Unlock()
	if st.fixedEstimator {
		return
	}
	current := st.charsPerToken
	if current <= 0 {
		current = defaultCharsPerToken
	}
	st.charsPerToken = current*(1-adaptiveCharsPerTokenAlpha) + observed*adaptiveCharsPerTokenAlpha
}

func (st *Store) resolveBaseTrimSettings(maxTurns int) trimSettings {
	return trimSettings{
		maxTurns:      maxTurns,
		budget:        st.trimTokenBudget(maxTurns),
		keepRecent:    st.keepRecentRawTurns(maxTurns),
		charsPerToken: st.charsPerTokenValue(),
	}
}

func (st *Store) resolveTrimSettings(hint provider.ModelHint) trimSettings {
	base := st.resolveBaseTrimSettings(st.MaxTurns())
	scale := st.hintBudgetScale(hint)
	base.budget = int(float64(base.budget) * scale)
	if base.budget < minTrimTokenBudget/2 {
		base.budget = minTrimTokenBudget / 2
	}
	return base
}

func (st *Store) trimTokenBudget(maxTurns int) int {
	st.settingsMu.RLock()
	defer st.settingsMu.RUnlock()
	if st.tokenBudget > 0 {
		if st.tokenBudget < minTrimTokenBudget {
			return minTrimTokenBudget
		}
		return st.tokenBudget
	}
	b := maxTurns * defaultPerTurnTokenBudget
	if b < minTrimTokenBudget {
		return minTrimTokenBudget
	}
	return b
}

func (st *Store) charsPerTokenValue() float64 {
	st.settingsMu.RLock()
	defer st.settingsMu.RUnlock()
	if st.charsPerToken <= 0 {
		return defaultCharsPerToken
	}
	if st.charsPerToken < 1 {
		return 1
	}
	return st.charsPerToken
}

func (st *Store) hintBudgetScale(hint provider.ModelHint) float64 {
	st.settingsMu.RLock()
	defer st.settingsMu.RUnlock()
	switch hint {
	case provider.ModelHintRouter:
		return normalizedScale(st.routerScale, defaultRouterBudgetScale)
	case provider.ModelHintSummary:
		return normalizedScale(st.summaryScale, defaultSummaryBudgetScale)
	case provider.ModelHintThinking:
		return normalizedScale(st.thinkingScale, defaultThinkingBudgetScale)
	default:
		return normalizedScale(st.taskScale, defaultTaskBudgetScale)
	}
}

func (st *Store) keepRecentRawTurns(maxTurns int) int {
	st.settingsMu.RLock()
	defer st.settingsMu.RUnlock()
	r := st.recentRaw
	if r <= 0 {
		r = defaultRecentRawTurns(maxTurns)
	}
	if r < 1 {
		r = 1
	}
	if r > maxTurns {
		r = maxTurns
	}
	if r < 1 {
		return 1
	}
	return r
}

func measuredMessageChars(messages []provider.Message) int {
	total := 0
	for _, m := range messages {
		total += len(m.Content) + len(m.ToolCallID) + len(m.Name) + len(m.Role)
		for _, tc := range m.ToolCalls {
			total += len(tc.ID) + len(tc.Type) + len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	return total
}

func defaultBudgetScaleForHint(hint provider.ModelHint) float64 {
	switch hint {
	case provider.ModelHintRouter:
		return defaultRouterBudgetScale
	case provider.ModelHintSummary:
		return defaultSummaryBudgetScale
	case provider.ModelHintThinking:
		return defaultThinkingBudgetScale
	default:
		return defaultTaskBudgetScale
	}
}

func normalizedScale(v, fallback float64) float64 {
	if v <= 0 {
		return fallback
	}
	if v < 0.1 {
		return 0.1
	}
	if v > 4 {
		return 4
	}
	return v
}

func defaultRecentRawTurns(maxTurns int) int {
	if maxTurns <= 1 {
		return 1
	}
	if maxTurns < 4 {
		return maxTurns
	}
	return 4
}

// ── Disk I/O ──────────────────────────────────────────────────────────────────

func (st *Store) filePath(key string) string {
	// Sanitise name so it is safe as a filename: keep alphanum, dash, underscore, dot.
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, key)
	return filepath.Join(st.dir, safe+".json")
}

// save writes sess to its JSON file. Errors are logged, never returned.
func (st *Store) save(sess *Session) {
	if st.dir == "" {
		return
	}
	sess.mu.Lock()
	sf := sessionFile{
		Key:     sess.Key,
		Name:    sess.Name,
		History: make([]provider.Message, len(sess.history)),
	}
	copy(sf.History, sess.history)
	sess.mu.Unlock()

	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		st.logErr("session save marshal", sess.Key, err)
		return
	}
	// Write to a temp file then rename for atomicity.
	tmp := st.filePath(sess.Key) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		st.logErr("session save write", sess.Key, err)
		return
	}
	if err := os.Rename(tmp, st.filePath(sess.Key)); err != nil {
		st.logErr("session save rename", sess.Key, err)
		_ = os.Remove(tmp)
	}
}

// loadAll reads every *.json file in st.dir and populates the in-memory store.
func (st *Store) loadAll() {
	entries, err := os.ReadDir(st.dir)
	if err != nil {
		if !os.IsNotExist(err) {
			st.logErr("session loadAll readdir", st.dir, err)
		}
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(st.dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			st.logErr("session load read", path, err)
			continue
		}
		var sf sessionFile
		if err := json.Unmarshal(data, &sf); err != nil {
			st.logErr("session load parse", path, err)
			continue
		}
		if sf.Key == "" {
			continue
		}
		sess := &Session{
			Key:     sf.Key,
			Name:    sf.Name,
			history: sf.History,
			store:   st,
		}
		st.sessions.Store(sf.Key, sess)
	}
}

func (st *Store) logErr(op, key string, err error) {
	if st.log != nil {
		st.log.Warn("session persistence error", "op", op, "key", key, "err", err)
	}
}
