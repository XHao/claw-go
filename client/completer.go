package client

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	{"/reload", "重新加载 daemon 配置（prompt 文件、动态 profile）", nil},
	{"/learn", "将记忆提炼为指定主题的经验库，如 /learn \"Linux\"", []string{"\"<主题>\""}},
	{"/exp", "管理经验库", []string{"ls", "use", "show", "rm"}},
	{"exit", "断开连接并退出", nil},
	{"quit", "断开连接并退出", nil},
}

// ── readline.AutoCompleter implementation ────────────────────────────────────

// clawCompleter implements readline.AutoCompleter.
type clawCompleter struct {
	shellEnabled bool
	cwd          string
}

// newCompleter returns a completer wired to the current shell-enabled setting.
func newCompleter(shellEnabled bool, cwd string) readline.AutoCompleter {
	return &clawCompleter{shellEnabled: shellEnabled, cwd: cwd}
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

	// ── @ file path completion ────────────────────────────────────────────
	case strings.ContainsRune(input, '@'):
		return c.completeAt(input)
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

// atMaxDisplayChars caps the total rune count of all candidate names shown in
// the completion popup. This prevents the list from flooding the terminal when
// a directory contains many files with long names. Once the running total would
// exceed the threshold, remaining entries are collapsed into a single hint.
const atMaxDisplayChars = 200

// completeAt handles Tab completion for "@<path>" file references.
// It finds the last '@' in input and completes the path that follows it.
func (c *clawCompleter) completeAt(input string) ([][]rune, int) {
	atIdx := strings.LastIndex(input, "@")
	if atIdx < 0 {
		return nil, 0
	}
	pathPrefix := input[atIdx+1:]

	// Determine the directory to scan and the base name prefix to match.
	var scanDir, basePrefix string
	if strings.HasSuffix(pathPrefix, "/") {
		// e.g. "@src/" — list everything inside src/
		scanDir = pathPrefix
		basePrefix = ""
	} else {
		scanDir = filepath.Dir(pathPrefix)
		if scanDir == "." {
			scanDir = ""
		}
		basePrefix = filepath.Base(pathPrefix)
		if basePrefix == "." {
			basePrefix = ""
		}
	}

	// Resolve to an absolute directory.
	var absDir string
	if filepath.IsAbs(pathPrefix) {
		absDir = scanDir
		if absDir == "" {
			absDir = "/"
		}
	} else {
		base := c.cwd
		if base == "" {
			base = "."
		}
		if scanDir == "" {
			absDir = base
		} else {
			absDir = filepath.Join(base, scanDir)
		}
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, 0
	}

	// Filter entries by basePrefix.
	var matched []os.DirEntry
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), basePrefix) {
			matched = append(matched, e)
		}
	}

	total := len(matched)

	// Accumulate candidates until the total display width would exceed the
	// threshold. Using rune count (not bytes) gives a better screen-width
	// estimate for both ASCII and CJK filenames.
	var shown []os.DirEntry
	charCount := 0
	for _, e := range matched {
		w := len([]rune(e.Name())) + 1 // +1 for trailing "/" or " "
		if charCount > 0 && charCount+w > atMaxDisplayChars {
			break
		}
		shown = append(shown, e)
		charCount += w
	}

	truncated := len(shown) < total

	cands := make([][]rune, 0, len(shown)+1)
	for _, e := range shown {
		suffix := e.Name()[len(basePrefix):]
		if e.IsDir() {
			suffix += "/"
		} else {
			suffix += " "
		}
		cands = append(cands, []rune(suffix))
	}
	if truncated {
		hint := fmt.Sprintf("… (还有 %d 项，请继续输入过滤)", total-len(shown))
		cands = append(cands, []rune(hint))
	}

	return cands, len(pathPrefix)
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
