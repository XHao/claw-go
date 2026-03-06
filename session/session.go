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
	"strings"
	"sync"

	"github.com/XHao/claw-go/provider"
)

// MainSessionKey is the name of the built-in session that the daemon
// creates automatically on startup.  It cannot be deleted, but its history
// can be cleared with /reset.
const MainSessionKey = "main"

// Session holds the conversation history for a single named conversation.
type Session struct {
	Key     string
	Name    string // human-readable name chosen by the user
	history []provider.Message
	mu      sync.Mutex
	store   *Store // back-reference for auto-save
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

// Append adds a message to the session history, trims it if needed, then
// persists the session to disk (best-effort; errors are logged, not fatal).
func (s *Session) Append(msg provider.Message, maxTurns int) {
	s.mu.Lock()
	s.history = append(s.history, msg)
	s.trim(maxTurns)
	s.mu.Unlock()
	// Save outside the lock so readers are not blocked.
	if s.store != nil {
		s.store.save(s)
	}
}

func (s *Session) trim(maxTurns int) {
	if maxTurns <= 0 {
		return
	}
	var sys []provider.Message
	var rest []provider.Message
	for _, m := range s.history {
		if m.Role == "system" {
			sys = append(sys, m)
		} else {
			rest = append(rest, m)
		}
	}
	maxMsgs := maxTurns * 2
	if len(rest) > maxMsgs {
		rest = rest[len(rest)-maxMsgs:]
	}
	s.history = append(sys, rest...)
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
	sessions     sync.Map
	maxTurns     int
	systemPrompt string
	dir          string       // sessions directory; "" = no persistence
	log          *slog.Logger // may be nil
}

// NewStore creates a Store with the given history limit, system prompt, and
// optional sessions directory.  When dir is non-empty, existing session files
// are loaded immediately and new writes are persisted automatically.
func NewStore(maxTurns int, systemPrompt, dir string) *Store {
	st := &Store{
		maxTurns:     maxTurns,
		systemPrompt: systemPrompt,
		dir:          dir,
		log:          slog.Default(),
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
