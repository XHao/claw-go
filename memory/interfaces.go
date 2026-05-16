package memory

import "context"

// Embedder is the interface for text embedding services.
// Implementations convert a text string into a fixed-length float32 vector
// suitable for cosine-similarity search.
// NewEmbedder returns an OllamaEmbedder that satisfies this interface.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// SessionStore is the per-session memory read/write interface.
// Each conversation's history is stored independently; use
// MemoryManager.ForSession to obtain a SessionStore for a given session key.
type SessionStore interface {
	// SaveTurn appends a completed conversation turn.
	SaveTurn(t TurnSummary) error

	// UpdateSummary patches the LLMSummary field of an existing turn.
	UpdateSummary(t TurnSummary) error

	// LoadRecent returns up to maxTurns turns, ordered oldest-first.
	// maxTurns ≤ 0 returns all stored turns.
	LoadRecent(maxTurns int) ([]TurnSummary, error)
}

// MemoryManager is the cross-session memory coordination interface.
// It vends per-session stores, runs cross-session search, and manages
// session lifecycle (clear, delete).
//
// The default implementation is *Manager (filesystem-backed JSONL).
// Alternative backends (e.g. in-memory, SQLite) can be plugged in by
// implementing this interface.
type MemoryManager interface {
	// ForSession returns the SessionStore for the given session key.
	ForSession(sessionKey string) SessionStore

	// AllSessions returns the keys of all sessions that have stored turns.
	AllSessions() ([]string, error)

	// SearchTurns returns up to maxResults turns across all sessions ranked
	// by relevance to keywords. Uses semantic search when an Embedder is
	// configured, otherwise falls back to keyword hit-count × recency decay.
	SearchTurns(keywords []string, maxResults int) ([]TurnSummary, error)

	// DeleteSession removes all memory files for sessionKey.
	DeleteSession(sessionKey string) error

	// ClearSession deletes all turn data for sessionKey without removing
	// the session directory. Used when a protected session is reset.
	ClearSession(sessionKey string) error
}
