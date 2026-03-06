package knowledge

import (
	"fmt"
	"sort"
	"strings"

	"github.com/XHao/claw-go/memory"
)

// synonyms maps common topic keywords to related terms for broader matching.
var synonyms = map[string][]string{
	"linux":   {"kernel", "bash", "shell", "unix", "posix"},
	"docker":  {"container", "compose", "image", "dockerfile"},
	"git":     {"commit", "branch", "merge", "rebase", "push", "pull"},
	"golang":  {"go", "goroutine", "channel", "interface", "struct"},
	"python":  {"pip", "venv", "django", "flask", "pytest"},
	"sql":     {"database", "query", "table", "index", "postgres", "mysql"},
	"nginx":   {"proxy", "upstream", "config", "server", "location"},
	"k8s":     {"kubernetes", "pod", "deployment", "service", "ingress"},
	"network": {"tcp", "udp", "http", "dns", "ip", "port"},
	"c++":     {"cpp", "template", "stl", "pointer", "class", "object", "编译"},
	"cpp":     {"c++", "template", "stl", "pointer", "class"},
	"rust":    {"cargo", "borrow", "lifetime", "trait", "unsafe"},
	"java":    {"jvm", "spring", "maven", "gradle", "class"},
	"开发":      {"程序", "代码", "编程", "实现", "项目"},
	"编程":      {"开发", "代码", "程序", "实现"},
}

// tokenize splits s into tokens at whitespace AND at ASCII↔CJK boundaries,
// so mixed strings like "C++开发" become ["c++", "开发"] rather than one token.
func tokenize(s string) []string {
	s = strings.ToLower(s)
	var tokens []string
	var cur []rune
	var lastCJK bool

	flush := func() {
		if t := strings.TrimSpace(string(cur)); t != "" {
			tokens = append(tokens, t)
		}
		cur = cur[:0]
	}

	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '　':
			flush()
			lastCJK = false
		default:
			// CJK Unified Ideographs and common CJK blocks.
			isCJK := (r >= 0x4E00 && r <= 0x9FFF) ||
				(r >= 0x3400 && r <= 0x4DBF) ||
				(r >= 0xF900 && r <= 0xFAFF) ||
				(r >= 0x3000 && r <= 0x303F) // CJK punctuation
			if len(cur) > 0 && isCJK != lastCJK {
				flush()
			}
			cur = append(cur, r)
			lastCJK = isCJK
		}
	}
	flush()
	return tokens
}

// TopicTokens returns the raw tokens for a topic WITHOUT synonym expansion,
// filtering out single-character ASCII tokens that are too noisy for matching
// (e.g. the "c" in "c开发" should not match every URL containing the letter c).
// Used by auto-inject to decide whether an experience library is relevant to a
// user message; all returned tokens must appear in the text (AND semantics).
func TopicTokens(topic string) []string {
	raw := tokenize(topic)
	var out []string
	for _, w := range raw {
		// Skip single ASCII characters – they match spuriously in any sentence.
		if len([]rune(w)) == 1 && w[0] < 0x80 {
			continue
		}
		out = append(out, w)
	}
	return out
}

// ExtractKeywords returns a deduplicated slice of lowercase keyword tokens
// for the given topic, including any synonym expansions.
func ExtractKeywords(topic string) []string {
	raw := tokenize(topic)
	seen := make(map[string]bool)
	var out []string
	for _, w := range raw {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
		for _, syn := range synonyms[w] {
			if !seen[syn] {
				seen[syn] = true
				out = append(out, syn)
			}
		}
	}
	return out
}

// scoredTurn pairs a turn with its relevance score.
type scoredTurn struct {
	turn  memory.TurnSummary
	score int
}

// scoreTurn counts keyword occurrences across all text fields of t.
func scoreTurn(t memory.TurnSummary, keywords []string) int {
	var parts []string
	parts = append(parts, t.User, t.Reply)
	for _, a := range t.Actions {
		parts = append(parts, a.Tool, a.Summary)
	}
	haystack := strings.ToLower(strings.Join(parts, " "))
	score := 0
	for _, kw := range keywords {
		score += strings.Count(haystack, kw)
	}
	return score
}

// FilterRelevant returns up to maxResults TurnSummary entries that score at
// least one keyword hit, sorted by descending relevance.
// If maxResults <= 0, all matching turns are returned.
func FilterRelevant(turns []memory.TurnSummary, keywords []string, maxResults int) []memory.TurnSummary {
	var scored []scoredTurn
	for _, t := range turns {
		if s := scoreTurn(t, keywords); s > 0 {
			scored = append(scored, scoredTurn{turn: t, score: s})
		}
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	if maxResults > 0 && len(scored) > maxResults {
		scored = scored[:maxResults]
	}
	out := make([]memory.TurnSummary, len(scored))
	for i, s := range scored {
		out[i] = s.turn
	}
	return out
}

// FormatBatchForLLM renders a slice of TurnSummary into compact plain text
// suitable for an LLM Map prompt batch.
func FormatBatchForLLM(turns []memory.TurnSummary) string {
	var sb strings.Builder
	for i, t := range turns {
		fmt.Fprintf(&sb, "\n--- Turn %03d ---\n", i+1)
		fmt.Fprintf(&sb, "User: %s\n", t.User)
		if t.Reply != "" {
			fmt.Fprintf(&sb, "Assistant: %s\n", t.Reply)
		}
		for _, a := range t.Actions {
			fmt.Fprintf(&sb, "  Tool[%s]: %s\n", a.Tool, a.Summary)
		}
	}
	return sb.String()
}
