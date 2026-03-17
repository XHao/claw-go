package client

import (
	"os/exec"
	"strings"

	"github.com/chzyer/readline"
)

// cmdEntry describes a single slash-command recognised by the CLI.
// The table is the single source of truth for both autocompletion and /help.
type cmdEntry struct {
	cmd  string // full command string (e.g. "/reset")
	desc string // one-line description shown in /help
	// args lists optional argument hints shown as sub-completions (may be nil).
	// Each string is a display hint such as "<topic>" or "ls".
	args []string
}

// commands is the canonical list of all built-in slash commands.
// Keep this table in sync with the switch cases in client.go.
var commands = []cmdEntry{
	{"/help", "显示帮助信息", nil},
	{"/ml", "进入多行输入模式（/send 发送，/abort 取消）", nil},
	{"/tokens", "显示 token 消耗（report/turn/clear）", []string{"report", "turn", "clear"}},
	{"/reset", "清空当前对话历史（主会话保留，子会话删除）", nil},
	{"/learn", "将记忆提炼为指定主题的经验库，如 /learn \"Linux\"", []string{"\"<主题>\""}},
	{"/exp", "管理经验库", []string{"ls", "use", "show", "rm"}},
	{"exit", "断开连接并退出", nil},
	{"quit", "断开连接并退出", nil},
}

// ── readline.AutoCompleter implementation ────────────────────────────────────

// clawCompleter implements readline.AutoCompleter.
type clawCompleter struct {
	shellEnabled bool
}

// newCompleter returns a completer wired to the current shell-enabled setting.
func newCompleter(shellEnabled bool) readline.AutoCompleter {
	return &clawCompleter{shellEnabled: shellEnabled}
}

// Do is called by readline on every Tab keypress.
// line is the entire input buffer; pos is the cursor position.
// It returns a list of candidate strings and the length of the word being
// completed (used by readline to replace that suffix).
func (c *clawCompleter) Do(line []rune, pos int) (newLine [][]rune, length int) {
	input := string(line[:pos])

	switch {
	// ── Slash commands ────────────────────────────────────────────────────
	case strings.HasPrefix(input, "/"):
		return c.completeSlash(input)

	// ── Shell passthrough (!cmd) ──────────────────────────────────────────
	case strings.HasPrefix(input, "!") && c.shellEnabled:
		return c.completeShell(input)
	}

	return nil, 0
}

// completeSlash handles Tab completion for "/" prefixed commands.
func (c *clawCompleter) completeSlash(input string) ([][]rune, int) {
	// Case 1: the user has typed a complete command + space → complete its args.
	//   e.g. "/exp " or "/exp l"
	for _, e := range commands {
		prefix := e.cmd + " "
		if strings.HasPrefix(input, prefix) && len(e.args) > 0 {
			typed := input[len(prefix):]
			var cands [][]rune
			for _, a := range e.args {
				if strings.HasPrefix(a, typed) {
					cands = append(cands, []rune(a[len(typed):]))
				}
			}
			return cands, len(typed)
		}
	}

	// Case 2: still typing the command name itself.
	typed := input // e.g. "/res"
	var cands [][]rune
	for _, e := range commands {
		if strings.HasPrefix(e.cmd, typed) {
			// Append suffix needed to complete the command.
			suffix := e.cmd[len(typed):]
			// If the command takes args, add a trailing space.
			if len(e.args) > 0 {
				suffix += " "
			}
			cands = append(cands, []rune(suffix))
		}
	}
	return cands, 0
}

// completeShell handles Tab completion for "!<cmd>" shell passthrough.
// It queries PATH for executables whose name has the typed prefix.
func (c *clawCompleter) completeShell(input string) ([][]rune, int) {
	// Strip the leading "!"
	typed := input[1:]

	// Only complete the first word (the binary name).
	if strings.ContainsRune(typed, ' ') {
		return nil, 0
	}

	found, err := exec.LookPath(typed)
	if err == nil && found != "" {
		// Exact match: nothing left to complete.
		return nil, 0
	}

	// Collect all executables in PATH whose base name starts with typed.
	// We keep this lightweight: at most 20 candidates.
	candidates := findExesByPrefix(typed, 20)
	var cands [][]rune
	for _, name := range candidates {
		if len(name) > len(typed) {
			cands = append(cands, []rune(name[len(typed):]))
		}
	}
	return cands, 0
}

// findExesByPrefix scans PATH entries for executables starting with prefix.
// Returns at most max matches.
func findExesByPrefix(prefix string, max int) []string {
	if prefix == "" {
		return nil
	}
	var out []string
	// Use shell "compgen -c <prefix>" if available for speed; fall back to
	// a simpler approach using LookPath on hardcoded common tools.
	// We use a lightweight approach: ask the shell to do the heavy lifting
	// only if `bash` is available, otherwise skip.
	cmd := exec.Command("bash", "-c", "compgen -c "+prefix+" 2>/dev/null | head -"+itoa(max))
	if b, err := cmd.Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				out = append(out, line)
			}
			if len(out) >= max {
				break
			}
		}
	}
	return out
}

// itoa converts an int to its decimal string representation without importing
// strconv (keeps import list minimal in this file).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
