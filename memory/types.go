// Package memory implements short-term conversation memory for claw-go.
//
// After every completed agent turn a compact TurnSummary is appended to a
// date-stamped JSONL file:
//
//	~/.claw/data/memory/{sessionKey}/YYYY-MM-DD.jsonl
//
// Design principles:
//   - No extra LLM calls: deterministic extraction from tool args + truncated text.
//   - Compact records: ~200-400 bytes/turn, 100 turns/day ≈ 40 KB.
//   - Append-only writes: O(1) per turn, no file rewriting or locking needed.
//   - Daily rolling: each UTC day gets its own file; old files are never touched.
package memory

import "time"

// TurnSummary is the compact record written for one completed conversation turn.
type TurnSummary struct {
	N       int       `json:"n"`
	At      time.Time `json:"at"`
	User    string    `json:"user"`
	Reply   string    `json:"reply"`
	Actions []Action  `json:"actions,omitempty"`
	Files   []string  `json:"files,omitempty"`
	Iters   int       `json:"iters,omitempty"`
	IsError bool      `json:"err,omitempty"`
}

// Action describes a single tool invocation in a compact, human-readable form.
type Action struct {
	Tool    string `json:"tool"`
	Summary string `json:"s"`
	Path    string `json:"path,omitempty"`
	IsError bool   `json:"err,omitempty"`
}
