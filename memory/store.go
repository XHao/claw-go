package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store handles append-only persistence of TurnSummary records for a single
// session.  Records are written to JSONL files named YYYY-MM-DD.jsonl (UTC)
// under baseDir, one file per day.
//
// Writes are serialised with a mutex; reads take a snapshot of the directory
// listing and read each file independently, so they do not block ongoing writes.
type Store struct {
	baseDir    string
	retainDays int // 0 = keep forever
	mu         sync.Mutex
}

// newStore returns a Store rooted at baseDir.
// The directory itself is not created until the first SaveTurn call.
func newStore(baseDir string, retainDays int) *Store {
	return &Store{baseDir: baseDir, retainDays: retainDays}
}

// SaveTurn appends t to the JSONL file for t's UTC date.
// The base directory is created automatically if it does not exist.
// On the first write of a new day, the previous day's file is compacted
// asynchronously in a goroutine.
func (s *Store) SaveTurn(t TurnSummary) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.baseDir, 0o700); err != nil {
		return fmt.Errorf("memory: mkdir %s: %w", s.baseDir, err)
	}

	today := t.At.UTC().Format("2006-01-02")
	path := filepath.Join(s.baseDir, today+".jsonl")

	// If this is the first write of a new day, compact yesterday's file and
	// prune files older than retainDays, both asynchronously.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		yesterday := t.At.UTC().AddDate(0, 0, -1).Format("2006-01-02")
		retainDays := s.retainDays
		cutoff := t.At.UTC()
		go func() {
			_ = s.CompactDay(yesterday)
			if retainDays > 0 {
				s.pruneOldFiles(cutoff, retainDays)
			}
		}()
	}

	line, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("memory: marshal: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("memory: open %s: %w", path, err)
	}
	defer f.Close()

	_, err = f.Write(line)
	return err
}

// UpdateSummary appends a patch record that sets LLMSummary for turn ts.N.
// LoadRecent merges records by N so the patch takes effect on the next read.
// The patch is written to the JSONL file for ts.At's UTC date.
func (s *Store) UpdateSummary(ts TurnSummary) error {
	if ts.LLMSummary == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.baseDir, 0o700); err != nil {
		return fmt.Errorf("memory: mkdir %s: %w", s.baseDir, err)
	}

	day := ts.At.UTC().Format("2006-01-02")
	path := filepath.Join(s.baseDir, day+".jsonl")

	patch := struct {
		N          int    `json:"n"`
		LLMSummary string `json:"llm_summary"`
	}{N: ts.N, LLMSummary: ts.LLMSummary}

	line, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("memory: marshal patch: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("memory: open %s: %w", path, err)
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

// LoadRecent returns up to maxTurns TurnSummary records from the most recent
// JSONL files, ordered oldest-first. Patch records (same N, later in file)
// are merged: non-zero fields in the patch overwrite the original.
// If maxTurns ≤ 0 all stored turns are returned.
func (s *Store) LoadRecent(maxTurns int) ([]TurnSummary, error) {
	entries, err := os.ReadDir(s.baseDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory: readdir %s: %w", s.baseDir, err)
	}

	// Collect .jsonl file paths; lexicographic sort = chronological for ISO dates.
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			files = append(files, filepath.Join(s.baseDir, e.Name()))
		}
	}
	sort.Strings(files)

	byN := make(map[int]*TurnSummary)
	var order []int

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, rawLine := range strings.Split(string(data), "\n") {
			rawLine = strings.TrimSpace(rawLine)
			if rawLine == "" {
				continue
			}
			var t TurnSummary
			if json.Unmarshal([]byte(rawLine), &t) != nil {
				continue
			}
			if existing, ok := byN[t.N]; ok {
				if t.LLMSummary != "" {
					existing.LLMSummary = t.LLMSummary
				}
				if t.User != "" {
					existing.User = t.User
				}
				if t.Reply != "" {
					existing.Reply = t.Reply
				}
			} else {
				cp := t
				byN[t.N] = &cp
				order = append(order, t.N)
			}
		}
	}

	turns := make([]TurnSummary, 0, len(order))
	for _, n := range order {
		turns = append(turns, *byN[n])
	}

	if maxTurns > 0 && len(turns) > maxTurns {
		turns = turns[len(turns)-maxTurns:]
	}
	return turns, nil
}

// LoadRecentForAgent returns turns for a specific agentID only.
// If agentID is empty, behaves identically to LoadRecent.
func (s *Store) LoadRecentForAgent(maxTurns int, agentID string) ([]TurnSummary, error) {
	if agentID == "" {
		return s.LoadRecent(maxTurns)
	}
	all, err := s.LoadRecent(0)
	if err != nil {
		return nil, err
	}
	var filtered []TurnSummary
	for _, t := range all {
		if t.AgentID == agentID {
			filtered = append(filtered, t)
		}
	}
	if maxTurns > 0 && len(filtered) > maxTurns {
		filtered = filtered[len(filtered)-maxTurns:]
	}
	return filtered, nil
}

// CompactDay rewrites the JSONL file for the given day (format "2006-01-02")
// by merging patch records into their originals, producing one record per N.
// Uses atomic write (tmp → rename). No-op if the file does not exist.
func (s *Store) CompactDay(day string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.compactDayLocked(day)
}

// compactDayLocked does the actual work; caller must hold s.mu.
func (s *Store) compactDayLocked(day string) error {
	path := filepath.Join(s.baseDir, day+".jsonl")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("memory: compact read %s: %w", path, err)
	}

	byN := make(map[int]*TurnSummary)
	var order []int
	for _, rawLine := range strings.Split(string(data), "\n") {
		rawLine = strings.TrimSpace(rawLine)
		if rawLine == "" {
			continue
		}
		var t TurnSummary
		if json.Unmarshal([]byte(rawLine), &t) != nil {
			continue
		}
		if existing, ok := byN[t.N]; ok {
			if t.LLMSummary != "" {
				existing.LLMSummary = t.LLMSummary
			}
		} else {
			cp := t
			byN[t.N] = &cp
			order = append(order, t.N)
		}
	}

	var buf []byte
	for _, n := range order {
		line, err := json.Marshal(*byN[n])
		if err != nil {
			return fmt.Errorf("memory: compact marshal: %w", err)
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return fmt.Errorf("memory: compact write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("memory: compact rename: %w", err)
	}
	return nil
}

// pruneOldFiles deletes JSONL files whose date is more than retainDays before
// now. Called asynchronously from SaveTurn on the first write of a new day.
// The caller must not hold s.mu.
func (s *Store) pruneOldFiles(now time.Time, retainDays int) {
	if retainDays <= 0 {
		return
	}
	cutoff := now.AddDate(0, 0, -retainDays).Format("2006-01-02")
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(name, ".jsonl")
		if day < cutoff { // lexicographic comparison works for ISO dates
			_ = os.Remove(filepath.Join(s.baseDir, name))
		}
	}
}

// ListDays returns the UTC date strings ("YYYY-MM-DD") for which JSONL files
// exist, sorted ascending.
func (s *Store) ListDays() ([]string, error) {
	entries, err := os.ReadDir(s.baseDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var days []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".jsonl") {
			days = append(days, strings.TrimSuffix(name, ".jsonl"))
		}
	}
	sort.Strings(days)
	return days, nil
}

// TodayPath returns the full path to today's JSONL file (may not exist yet).
func (s *Store) TodayPath() string {
	day := time.Now().UTC().Format("2006-01-02")
	return filepath.Join(s.baseDir, day+".jsonl")
}

// ─── Manager ─────────────────────────────────────────────────────────────────

// Manager creates and caches per-session memory Stores under a shared base
// directory (~/.claw/data/memory by default).
type Manager struct {
	baseDir    string
	retainDays int // propagated to each Store; 0 = keep forever
}

// NewManager returns a Manager that will root all session stores under baseDir.
func NewManager(baseDir string) *Manager {
	return &Manager{baseDir: baseDir}
}

// SetRetainDays configures how many days of JSONL files to keep per session.
// 0 means keep forever (the default). Changes take effect on the next
// SaveTurn call that triggers daily compaction.
func (m *Manager) SetRetainDays(days int) {
	m.retainDays = days
}

// safeName sanitises sessionKey into a filesystem-safe directory name.
// Only alphanumeric characters, dashes, and underscores are kept; everything
// else (including path separators and dots) is replaced with '_'.
// This prevents path traversal attacks from user-chosen session names.
func (m *Manager) safeName(sessionKey string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, sessionKey)
}

// ForSession returns the Store for sessionKey.
// Memory files live directly under {MemoryDir}/{sessionKey}/ as daily JSONL files.
// The returned Store is lightweight (no I/O performed here).
func (m *Manager) ForSession(sessionKey string) *Store {
	return newStore(filepath.Join(m.baseDir, m.safeName(sessionKey)), m.retainDays)
}

// DeleteSession removes all memory files and the directory for sessionKey.
// Called when a session is permanently deleted.
func (m *Manager) DeleteSession(sessionKey string) error {
	dir := filepath.Join(m.baseDir, m.safeName(sessionKey))
	err := os.RemoveAll(dir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("memory: delete session %q: %w", sessionKey, err)
	}
	return nil
}

// ClearSession deletes all JSONL files for sessionKey without removing the
// directory.  Used when a protected session (e.g. "main") is reset but not
// deleted; the directory is recreated automatically on the next SaveTurn.
func (m *Manager) ClearSession(sessionKey string) error {
	return m.DeleteSession(sessionKey)
}

// AllSessions returns the names of all session keys that have memory files
// stored under the manager's base directory.  Used by the knowledge distiller
// to aggregate memory across all conversations.
func (m *Manager) AllSessions() ([]string, error) {
	entries, err := os.ReadDir(m.baseDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory: list sessions: %w", err)
	}
	var sessions []string
	for _, e := range entries {
		if e.IsDir() {
			sessions = append(sessions, e.Name())
		}
	}
	return sessions, nil
}

// SearchTurns returns up to maxResults TurnSummary records across all sessions
// that contain at least one of the given keywords, sorted by descending composite
// score (keyword hit count × recency decay factor). If maxResults <= 0, all
// matching turns are returned. If keywords is empty, nil is returned.
// Per-session load errors are silently skipped so a single corrupted session
// does not abort the search.
func (m *Manager) SearchTurns(keywords []string, maxResults int) ([]TurnSummary, error) {
	sessions, err := m.AllSessions()
	if err != nil {
		return nil, err
	}
	var all []TurnSummary
	for _, key := range sessions {
		turns, err := m.ForSession(key).LoadRecent(0)
		if err != nil {
			continue
		}
		all = append(all, turns...)
	}
	if len(all) == 0 || len(keywords) == 0 {
		return nil, nil
	}

	type scored struct {
		turn  TurnSummary
		score float64
	}
	var hits []scored
	for _, t := range all {
		kwScore := scoreTurnByKeywords(t, keywords)
		if kwScore > 0 {
			composite := float64(kwScore) * recencyFactor(t.At)
			hits = append(hits, scored{t, composite})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if maxResults > 0 && len(hits) > maxResults {
		hits = hits[:maxResults]
	}
	out := make([]TurnSummary, len(hits))
	for i, h := range hits {
		out[i] = h.turn
	}
	return out, nil
}

// recencyFactor returns a time-decay multiplier in (0, 1] based on how many
// days ago t occurred. Uses the formula 1 / (1 + daysSince/30), so:
//
//	today     → 1.0
//	30 days   → 0.5
//	90 days   → 0.25
func recencyFactor(t time.Time) float64 {
	daysSince := time.Since(t).Hours() / 24
	if daysSince < 0 {
		daysSince = 0
	}
	return 1.0 / (1.0 + daysSince/30.0)
}

// scoreTurnByKeywords counts keyword occurrences in a TurnSummary's text fields.
func scoreTurnByKeywords(t TurnSummary, keywords []string) int {
	parts := []string{t.User, t.Reply}
	for _, a := range t.Actions {
		parts = append(parts, a.Tool, a.Summary)
	}
	haystack := strings.ToLower(strings.Join(parts, " "))
	score := 0
	for _, kw := range keywords {
		score += strings.Count(haystack, strings.ToLower(kw))
	}
	return score
}
