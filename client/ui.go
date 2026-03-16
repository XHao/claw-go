// Package client — rich terminal UI components.
package client

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/XHao/claw-go/ipc"
	"github.com/XHao/claw-go/session"
	runewidth "github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

const (
	uiWidth      = 54 // preferred visual width of horizontal lines
	maxName      = 26 // preferred max visible chars for a session name
	minBoxWidth  = 40
	maxBoxWidth  = 88
	compactWidth = 68
)

// DrawWelcomeBanner prints a one-time greeting the first time the session
// chooser is shown.
func DrawWelcomeBanner() {
	w := boxInnerWidth(uiWidth+8, 48, maxBoxWidth)
	printBoxTop(w, "")
	printBoxStyledLine(w, S.Bold("🤖  OpenClaw AI 小助手"))
	printBoxStyledLine(w, S.Dim("与 AI 进行智能对话，随时保存知识与经验"))
	printBoxBottom(w)
	fmt.Println()
}

// hr returns n repetitions of "─" styled as a border.
func hr(n int) string {
	if n <= 0 {
		return ""
	}
	return S.Border(strings.Repeat("─", n))
}

// ansiRe matches ANSI escape sequences so visWidth can strip them.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// visWidth returns the visible terminal column width of s, stripping ANSI
// escape sequences and counting CJK / wide runes as 2 columns each.
func visWidth(s string) int {
	return runewidth.StringWidth(ansiRe.ReplaceAllString(s, ""))
}

// padRight pads s with spaces on the right until its visible width equals col.
// If s is already at or wider than col it is returned unchanged.
func padRight(s string, col int) string {
	if d := col - visWidth(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func truncatePlain(s string, col int) string {
	if col <= 0 || visWidth(s) <= col {
		return s
	}
	if col == 1 {
		return "…"
	}
	width := 0
	var out []rune
	for _, r := range []rune(s) {
		rw := runewidth.RuneWidth(r)
		if rw <= 0 {
			rw = 1
		}
		if width+rw > col-1 {
			break
		}
		out = append(out, r)
		width += rw
	}
	return string(out) + "…"
}

func wrapPlain(s string, col int) []string {
	if col <= 0 {
		return []string{s}
	}
	var lines []string
	remaining := strings.TrimSpace(s)
	for remaining != "" {
		if visWidth(remaining) <= col {
			lines = append(lines, remaining)
			break
		}
		width := 0
		split := 0
		lastSpace := -1
		for i, r := range []rune(remaining) {
			rw := runewidth.RuneWidth(r)
			if rw <= 0 {
				rw = 1
			}
			if width+rw > col {
				break
			}
			width += rw
			split = i + 1
			if r == ' ' {
				lastSpace = split
			}
		}
		if split == 0 {
			break
		}
		if lastSpace > 0 {
			split = lastSpace
		}
		chunk := strings.TrimSpace(string([]rune(remaining)[:split]))
		if chunk == "" {
			chunk = truncatePlain(remaining, col)
			lines = append(lines, chunk)
			break
		}
		lines = append(lines, chunk)
		remaining = strings.TrimSpace(string([]rune(remaining)[split:]))
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func terminalColumns() int {
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		return width
	}
	return 80
}

func boxInnerWidth(preferred, minWidth, maxWidth int) int {
	available := terminalColumns() - 2
	if available < minWidth {
		available = minWidth
	}
	w := preferred
	if w < minWidth {
		w = minWidth
	}
	if maxWidth > 0 && w > maxWidth {
		w = maxWidth
	}
	if w > available {
		w = available
	}
	return w
}

func boxContentWidth(innerWidth int) int {
	cw := innerWidth - 2
	if cw < 1 {
		return 1
	}
	return cw
}

func printBoxTop(innerWidth int, title string) {
	fmt.Println(boxTopLine(innerWidth, title))
}

func boxTopLine(innerWidth int, title string) string {
	if title == "" {
		return S.Border("╭") + hr(innerWidth) + S.Border("╮")
	}
	label := " " + strings.TrimSpace(title) + " "
	remain := innerWidth - visWidth(label)
	if remain < 0 {
		remain = 0
	}
	left := remain / 2
	right := remain - left
	return S.Border("╭") + hr(left) + S.Bold(label) + hr(right) + S.Border("╮")
}

func printBoxDivider(innerWidth int) {
	fmt.Println(S.Border("├") + hr(innerWidth) + S.Border("┤"))
}

func printBoxBottom(innerWidth int) {
	fmt.Println(boxBottomLine(innerWidth))
}

func boxBottomLine(innerWidth int) string {
	return S.Border("╰") + hr(innerWidth) + S.Border("╯")
}

func printBoxStyledLine(innerWidth int, content string) {
	pipe := S.Border("│")
	contentWidth := boxContentWidth(innerWidth)
	fmt.Printf("%s %s %s\n", pipe, padRight(content, contentWidth), pipe)
}

func printBoxTextLine(innerWidth int, text string, styler func(string) string) {
	if styler == nil {
		styler = func(s string) string { return s }
	}
	trimmed := truncatePlain(text, innerWidth)
	printBoxStyledLine(innerWidth, styler(trimmed))
}

func printBoxWrappedTextLines(innerWidth int, text string, styler func(string) string) {
	if styler == nil {
		styler = func(s string) string { return s }
	}
	for _, line := range wrapPlain(text, innerWidth) {
		printBoxStyledLine(innerWidth, styler(line))
	}
}

func printBoxKeyValueLines(
	innerWidth int,
	key, value string,
	keyWidth int,
	keyStyler func(string) string,
	valueStyler func(string) string,
) {
	if keyStyler == nil {
		keyStyler = func(s string) string { return s }
	}
	if valueStyler == nil {
		valueStyler = func(s string) string { return s }
	}
	contentW := boxContentWidth(innerWidth)
	if keyWidth < 6 {
		keyWidth = 6
	}
	if keyWidth > contentW-4 {
		keyWidth = contentW - 4
	}
	valueW := contentW - keyWidth - 1
	if valueW < 8 {
		valueW = 8
	}
	wrapped := wrapPlain(value, valueW)
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}
	first := keyStyler(padRight(truncatePlain(key, keyWidth), keyWidth)) + " " + valueStyler(wrapped[0])
	printBoxStyledLine(innerWidth, first)
	for _, line := range wrapped[1:] {
		cont := strings.Repeat(" ", keyWidth) + " " + valueStyler(line)
		printBoxStyledLine(innerWidth, cont)
	}
}

func isCompactWidth(innerWidth int) bool {
	return innerWidth <= compactWidth
}

// ─── Session chooser ─────────────────────────────────────────────────────────

// DrawSessionList renders the bordered conversation chooser and prints
// the input prompt at the end.  The caller should call fmt.Scanln() next.
//
//	╭─ Conversations ──────────────────────────────────╮
//	│   1  my-project               12 turns           │
//	│   2  research           [busy]  3 turns           │
//	├──────────────────────────────────────────────────┤
//	│   n  New conversation                            │
//	╰──────────────────────────────────────────────────╯
//	Select [1-2 / n]:
func DrawSessionList(sessions []ipc.SessionInfo) {
	w := boxInnerWidth(uiWidth, 48, maxBoxWidth)
	pipe := S.Border("│")
	contentW := boxContentWidth(w)
	turnWidth := 10
	indexWidth := 4
	nameWidth := contentW - indexWidth - turnWidth - 6
	if nameWidth > maxName {
		nameWidth = maxName
	}
	if nameWidth < 12 {
		nameWidth = 12
	}

	printBoxTop(w, "我的对话")

	if len(sessions) == 0 {
		printBoxStyledLine(w, S.Dim("还没有对话，输入 n 新建一个吧！"))
	} else {
		for i, s := range sessions {
			name := displaySessionName(s.Name)
			if s.Name == session.MainSessionKey {
				name += " " + S.Dim("(默认)")
			}
			// Trim to maxName visual columns.
			if visWidth(name) > nameWidth {
				runes := []rune(name)
				for visWidth(string(runes)) > nameWidth-1 {
					runes = runes[:len(runes)-1]
				}
				name = string(runes) + "…"
			}
			busy := ""
			if s.Active {
				busy = " " + S.Warn("[忙碌]")
			}
			// Build row with exact-width columns, then pad to fill inner width.
			row := padRight(S.Bold(fmt.Sprintf("%2d", i+1)), 2) +
				"  " + padRight(name, nameWidth) +
				"  " + padRight(S.Dim(formatTurns(s.TurnCount)), turnWidth) + busy
			fmt.Printf("%s %s %s\n", pipe, padRight(row, contentW), pipe)
		}
	}

	// Divider before "new" option.
	printBoxDivider(w)
	newRow := padRight(S.Bold("n"), 2) + "  ＋ 新建对话"
	fmt.Printf("%s %s %s\n", pipe, padRight(newRow, contentW), pipe)

	// Bottom border.
	printBoxBottom(w)

	// Prompt.
	if len(sessions) == 0 {
		fmt.Printf("%s：", S.Bold("请输入对话名称"))
	} else {
		fmt.Printf("%s [回车=主会话 / 1～%d 选择 / n 新建 / Ctrl-D 退出]： ", S.Bold("请选择"), len(sessions))
	}
}

// ─── Recent history preview ──────────────────────────────────────────────────

// DrawHistory renders the last few conversation turns immediately after a
// session is resumed, so the user can see context at a glance.
//
//	╭─ Recent conversation ────────────────────────────────╮
//	│ You       what files are in this directory?           │
//	│ Assistant Here are the files: main.go, go.mod, ...    │
//	╰──────────────────────────────────────────────────────╯
func DrawHistory(entries []ipc.HistoryEntry) {
	if len(entries) == 0 {
		return
	}

	pipe := S.Border("│")
	w := boxInnerWidth(uiWidth+8, 56, maxBoxWidth)
	contentW := boxContentWidth(w)

	printBoxTop(w, "最近消息")

	maxContent := contentW - 8
	if maxContent < 20 {
		maxContent = 20
	}

	for _, e := range entries {
		var roleLabel string
		var content string

		switch e.Role {
		case "user":
			roleLabel = S.Dim("你     ")
		case "assistant":
			roleLabel = S.Dim("助手   ")
		default:
			continue
		}

		// Truncate long content.
		content = strings.ReplaceAll(e.Content, "\n", " ")
		runes := []rune(content)
		if len(runes) > maxContent {
			content = string(runes[:maxContent-1]) + "…"
		}

		row := roleLabel + " " + S.Dim(truncatePlain(content, maxContent))
		fmt.Printf("%s %s %s\n", pipe, padRight(row, contentW), pipe)
	}

	// Bottom border.
	printBoxBottom(w)
	fmt.Println()
}

// ─── Assistant reply header ───────────────────────────────────────────────────

// PrintAssistantHeader prints a decorative header line immediately before
// the assistant reply body.
//
//	╭─ 助手 ───────────────────────────── 19:47:14 ─
func PrintAssistantHeader() {
	now := time.Now().Format("15:04:05")
	label := " 助手 "
	ts := " " + now + " ─"
	w := boxInnerWidth(uiWidth, 48, maxBoxWidth)

	// ╭─ <label> <dashes> <ts>
	used := 2 + visWidth(label) + len(ts)
	dashes := w - used
	if dashes < 2 {
		dashes = 2
	}
	fmt.Println(S.Border("╭─") + S.Bold(label) + hr(dashes) + S.Timestamp(ts))
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func formatTurns(n int) string {
	switch n {
	case 0:
		return "暂无记录 "
	case 1:
		return "1 轮对话 "
	default:
		return fmt.Sprintf("%d 轮对话", n)
	}
}

func displaySessionName(name string) string {
	if name == session.MainSessionKey {
		return "主会话"
	}
	return name
}
