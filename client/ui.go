// Package client — rich terminal UI components.
package client

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	runewidth "github.com/mattn/go-runewidth"
	"github.com/XHao/claw-go/ipc"
)

const (
	uiWidth = 54 // visual width of horizontal lines
	maxName = 26 // max visible chars for a session name
)

// DrawWelcomeBanner prints a one-time greeting the first time the session
// chooser is shown.
func DrawWelcomeBanner() {
	w := uiWidth + 8
	bdr := S.Border("│")
	fmt.Println(S.Border("╭") + S.Border(strings.Repeat("─", w)) + S.Border("╮"))
	fmt.Printf("%s%s%s\n", bdr, padRight("  "+S.Bold("🤖  OpenClaw AI 小助手"), w), bdr)
	fmt.Printf("%s%s%s\n", bdr, padRight("  "+S.Dim("与 AI 进行智能对话，随时保存知识与经验"), w), bdr)
	fmt.Println(S.Border("╰") + S.Border(strings.Repeat("─", w)) + S.Border("╯"))
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
	w := uiWidth
	pipe := S.Border("│")

	// Top border + title.
	// visWidth counts CJK chars as 2 columns, so border dashes are exact.
	titleText := " 我的对话 "
	titleDashes := w - 4 - visWidth(titleText)
	if titleDashes < 0 {
		titleDashes = 0
	}
	fmt.Println(S.Border("╭─") + S.Bold(titleText) + hr(titleDashes) + S.Border("─╮"))

	if len(sessions) == 0 {
		fmt.Printf("%s%s%s\n", pipe, padRight("  "+S.Dim("还没有对话，输入 n 新建一个吧！"), w-2), pipe)
	} else {
		for i, s := range sessions {
			name := s.Name
			// Trim to maxName visual columns.
			if visWidth(name) > maxName {
				runes := []rune(name)
				for visWidth(string(runes)) > maxName-1 {
					runes = runes[:len(runes)-1]
				}
				name = string(runes) + "…"
			}
			busy := ""
			if s.Active {
				busy = " " + S.Warn("[忙碌]")
			}
			// Build row with exact-width columns, then pad to fill inner width.
			row := "  " + padRight(S.Bold(fmt.Sprintf("%2d", i+1)), 2) +
				"  " + padRight(name, maxName) +
				"  " + padRight(S.Dim(formatTurns(s.TurnCount)), 9) + busy
			fmt.Printf("%s%s%s\n", pipe, padRight(row, w-2), pipe)
		}
	}

	// Divider before "new" option.
	fmt.Println(S.Border("├") + hr(w-2) + S.Border("┤"))
	newRow := "  " + padRight(S.Bold(" n"), 4) + "＋ 新建对话"
	fmt.Printf("%s%s%s\n", pipe, padRight(newRow, w-2), pipe)

	// Bottom border.
	fmt.Printf("%s%s%s\n", S.Border("╰"), hr(w-2), S.Border("╯"))

	// Prompt.
	if len(sessions) == 0 {
		fmt.Printf("%s：", S.Bold("请输入对话名称"))
	} else {
		fmt.Printf("%s [1～%d 选择 / n 新建 / Ctrl-D 退出]： ", S.Bold("请选择"), len(sessions))
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
	w := uiWidth + 8 // slightly wider for content

	// Top border.
	titleText := " 最近消息 "
	titleDashes := w - 4 - visWidth(titleText)
	if titleDashes < 1 {
		titleDashes = 1
	}
	fmt.Println(S.Border("╭─") + S.Dim(titleText) + hr(titleDashes) + S.Border("─╮"))

	const maxContent = 58 // content column width

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

		fmt.Printf("%s %s %s\n", pipe, roleLabel, S.Dim(content))
	}

	// Bottom border.
	fmt.Printf("%s%s%s\n", S.Border("╰"), hr(w-2), S.Border("╯"))
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

	// ╭─ <label> <dashes> <ts>
	used := 2 + visWidth(label) + len(ts)
	dashes := uiWidth - used
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
