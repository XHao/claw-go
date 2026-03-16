package client

import (
	"github.com/charmbracelet/glamour"
)

func newMarkdownRenderer() *glamour.TermRenderer {
	wrap := boxInnerWidth(100, 40, maxBoxWidth) - 2
	if wrap < 20 {
		wrap = 20
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(wrap),
	)
	if err != nil {
		return nil
	}
	return r
}

// renderMarkdown prints the assistant reply with a decorative header.
// If the content looks like Markdown it is rendered via glamour;
// otherwise it is printed as plain text.
func renderMarkdown(content string) {
	PrintAssistantHeader()
	mdRenderer := newMarkdownRenderer()

	if mdRenderer == nil || !looksLikeMarkdown(content) {
		// Plain text — just print indented under the header.
		println(content)
		println()
		return
	}

	rendered, err := mdRenderer.Render(content)
	if err != nil {
		// Fallback to plain on render error.
		println(content)
		println()
		return
	}
	// glamour already adds its own padding; print as-is.
	print(rendered)
}

// looksLikeMarkdown performs a cheap heuristic to avoid running glamour
// on ordinary conversational replies.
func looksLikeMarkdown(s string) bool {
	for _, m := range []string{
		"# ", "## ", "### ",
		"**", "__", "```", "`",
		"- ", "* ", "+ ", "1. ",
		"[", "![", "> ", "---",
	} {
		var found bool
		for i := 0; i+len(m) <= len(s); i++ {
			if s[i:i+len(m)] == m {
				found = true
				break
			}
		}
		if found {
			return true
		}
	}
	return false
}
