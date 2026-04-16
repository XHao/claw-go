package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// threatPatterns matches common prompt injection attempts in injected context files.
var threatPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(previous|all|prior)\s+instructions?`),
	regexp.MustCompile(`(?i)system\s+prompt\s+override`),
	regexp.MustCompile(`(?i)disregard\s+(your|all|previous)\s+(rules?|instructions?|constraints?)`),
	regexp.MustCompile(`(?i)act\s+as\s+if\s+you\s+have\s+no\s+restrictions?`),
	regexp.MustCompile(`(?i)you\s+are\s+now\s+in\s+(developer|jailbreak|unrestricted)\s+mode`),
	regexp.MustCompile(`(?i)<!--.*?(inject|override|prompt).*?-->`),
	regexp.MustCompile(`(?i)curl\s+.*(OPENAI_API_KEY|ANTHROPIC_API_KEY|\.env)`),
	regexp.MustCompile(`(?i)cat\s+\.env`),
	regexp.MustCompile(`(?i)print\s+(your\s+)?(system\s+prompt|instructions?|rules?)`),
	regexp.MustCompile(`(?i)new\s+instructions?:\s`),
}

// invisibleChars matches Unicode characters that are invisible or directional
// and commonly used to hide injected text from human reviewers.
var invisibleChars = regexp.MustCompile(
	"[\u200b\u200c\u200d\u200e\u200f" + // zero-width space, non-joiner, joiner, LRM, RLM
		"\u202a\u202b\u202c\u202d\u202e" + // LRE, RLE, PDF, LRO, RLO
		"\ufeff]", // BOM
)

// scanContent checks content for prompt injection patterns and invisible
// Unicode characters. If a threat is found, it returns a safe placeholder
// and blocked=true. The filename is used only for the placeholder message.
func scanContent(filename, content string) (safe string, blocked bool) {
	// Check for invisible Unicode characters.
	if invisibleChars.MatchString(content) {
		return blockedPlaceholder(filename, "invisible Unicode characters"), true
	}

	// Check for threat patterns.
	for _, re := range threatPatterns {
		if loc := re.FindStringIndex(content); loc != nil {
			snippet := content[loc[0]:loc[1]]
			if len(snippet) > 60 {
				snippet = snippet[:60] + "…"
			}
			return blockedPlaceholder(filename, fmt.Sprintf("%q", snippet)), true
		}
	}

	return content, false
}

func blockedPlaceholder(filename, reason string) string {
	return fmt.Sprintf("[BLOCKED: %s contained potential prompt injection (%s)]",
		filename, reason)
}

// sanitizeInjection runs scanContent and returns the safe content along with
// a boolean indicating whether the content was blocked. Callers should log
// the block event and use the returned string as the injection content.
func sanitizeInjection(filename, content string) (string, bool) {
	safe, blocked := scanContent(filename, content)
	if blocked {
		// Replace with a single-line placeholder so the model knows something
		// was present but blocked — silent omission could confuse reasoning.
		return strings.TrimSpace(safe), true
	}
	return content, false
}
