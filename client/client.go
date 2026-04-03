// Package client - interactive readline CLI that connects to the claw daemon.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/XHao/claw-go/config"
	"github.com/XHao/claw-go/ipc"
	"github.com/XHao/claw-go/knowledge"
	"github.com/XHao/claw-go/memory"
	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/session"
	"github.com/XHao/claw-go/tool"
	"github.com/chzyer/readline"
)

// Runner can execute a shell command with local stdin/stdout/stderr.
type Runner interface {
	Run(ctx context.Context, command string) error
}

// Run connects to the daemon over socketPath and starts an interactive session.
func Run(
	ctx context.Context,
	socketPath, prompt, historyFile string,
	shellEnabled bool,
	shellTimeoutSeconds int,
	allowedCommands []string,
	theme config.ThemeConfig,
	llmProvider provider.Provider,
	memoryDir string,
	experiencesDir string,
) error {
	// Apply colour theme before any terminal output.
	ApplyTheme(theme)

	// Capture the client's working directory to send with each message
	// so the daemon can execute tools (e.g. bash) in the right directory.
	cwd, _ := os.Getwd()
	// Build knowledge components (distiller + experience store).
	var distiller *knowledge.Distiller
	var expStore *knowledge.ExperienceStore
	if llmProvider != nil && memoryDir != "" && experiencesDir != "" {
		memMgr := memory.NewManager(memoryDir)
		expStore = knowledge.NewExperienceStore(experiencesDir)
		distiller = knowledge.NewDistiller(llmProvider, memMgr, expStore)
	}

	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("无法连接到守护进程 %q：%w\n提示：请先运行  claw serve  启动守护进程", socketPath, err)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	scanner := ipc.NewScanner(conn)

	// ── Phase 1: read session list (or busy error) ────────────────────────────
	if !scanner.Scan() {
		return fmt.Errorf("connection closed before session list")
	}
	var first ipc.Msg
	if err := json.Unmarshal(scanner.Bytes(), &first); err != nil {
		return fmt.Errorf("invalid response from daemon")
	}
	if first.Error != "" {
		return fmt.Errorf("守护进程拒绝连接：%s", first.Error)
	}

	// ── Phase 2: session selection (simple stdin/stdout, before readline) ─────
	sessionName, recentHist, err := selectSession(enc, scanner, first.Sessions)
	if err != nil {
		if err == io.EOF {
			return nil // Ctrl-D during selection — clean exit
		}
		return err
	}

	// Show recent conversation turns so the user has context right away.
	DrawWelcomeBanner()
	DrawHistory(recentHist)

	// ── Phase 3: set up readline + optional shell executor ───────────────────
	// Wrap stdin with a bracketed-paste interceptor so that multi-line pastes
	// are buffered and confirmed before being sent, rather than each pasted
	// line firing a separate request.
	pasteStdin, pasteErr := newPasteInterceptStdin(os.Stdin)
	var rlStdin io.ReadCloser
	if pasteErr == nil {
		rlStdin = pasteStdin
		defer pasteStdin.Close()
	} else {
		rlStdin = os.Stdin
	}

	rlCfg := &readline.Config{
		Prompt:          prompt,
		HistoryFile:     historyFile,
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
		AutoComplete:    newCompleter(shellEnabled),
		Stdin:           rlStdin,
	}
	rl, err := readline.NewEx(rlCfg)
	if err != nil {
		return fmt.Errorf("readline 初始化失败：%w", err)
	}
	defer rl.Close()

	var runner Runner
	if shellEnabled {
		runner = tool.NewShellExecutor("", shellTimeoutSeconds, allowedCommands)
	}
	interruptCh := make(chan os.Signal, 1)
	signal.Notify(interruptCh, os.Interrupt)
	defer signal.Stop(interruptCh)

	fmt.Printf("「%s」对话  （输入 /help 查看命令）\n", S.Bold(displaySessionName(sessionName)))
	usageTracker := NewUsageTracker()


	// ── Phase 4: chat loop (synchronous: send → wait for reply → repeat) ─────
	// Multi-line paste handling is done transparently inside pasteInterceptStdin;
	// readline sees the pasted content as a single confirmed line.
	for {
		if ctx.Err() != nil {
			return nil
		}

		line, err := readLineWithContinuation(rl, prompt)
		if err != nil {
			if err == readline.ErrInterrupt {
				if strings.TrimSpace(line) == "" {
					return nil
				}
				continue
			}
			if err == io.EOF {
				return nil
			}
			return err
		}

		line = strings.TrimSpace(line)
		// If the line is empty, check whether a multi-line paste was confirmed.
		// pasteInterceptStdin stores the joined paste text in pendingPaste and
		// injects a bare '\r' so readline returns "". We retrieve the real
		// content here. The paste text bypasses readLineWithContinuation, so
		// trailing '\' characters in the pasted content are treated as literals
		// and do NOT trigger the line-continuation logic.
		if line == "" && pasteStdin != nil {
			if pasted := pasteStdin.TakePaste(); pasted != "" {
				line = pasted
			}
		}
		if line == "" {
			continue
		}

		switch {
		case line == "exit" || line == "quit":
			return nil
		case line == "/help":
			printHelp()
			continue
		case strings.HasPrefix(line, "/tokens"):
			handleTokensCommand(line, usageTracker)
			continue
		case line == "/reset":
			if err := enc.Encode(ipc.Msg{Cmd: "reset"}); err != nil {
				return fmt.Errorf("write: %w", err)
			}
			sp := NewSpinner("正在清空对话…")
			err := printNextReply(ctx, scanner, enc, sp, usageTracker, interruptCh)
			sp.Stop()
			if err != nil {
				return err
			}
		case line == "/ml":
			msg, ok, err := readMultilineInput(rl, prompt)
			if err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
			if !ok {
				continue
			}
			usageTracker.BeginTurn()
			if err := enc.Encode(ipc.Msg{Text: msg, Cwd: cwd}); err != nil {
				return fmt.Errorf("write: %w", err)
			}
			sp := NewSpinner("思考中…")
			err = printNextReply(ctx, scanner, enc, sp, usageTracker, interruptCh)
			sp.Stop()
			if err != nil {
				return err
			}
		case strings.HasPrefix(line, "/learn"):
			topic := strings.TrimSpace(strings.TrimPrefix(line, "/learn"))
			topic = strings.Trim(topic, `"'`)
			if topic == "" {
				fmt.Fprintln(os.Stderr, S.Warn("Usage: /learn \"<topic>\"  e.g. /learn \"Linux 开发\""))
				continue
			}
			if distiller == nil {
				fmt.Fprintln(os.Stderr, S.Err("[错误]")+" 知识提炼需要配置 LLM")
				continue
			}
			runLearn(ctx, distiller, topic)
			continue
		case strings.HasPrefix(line, "/exp"):
			arg := strings.TrimSpace(strings.TrimPrefix(line, "/exp"))
			if expStore == nil {
				fmt.Fprintln(os.Stderr, S.Err("[错误]")+" 经验库不可用（无经验目录）")
				continue
			}
			runExp(ctx, arg, expStore, enc, scanner)
			continue
		case strings.HasPrefix(line, "!"):
			if runner == nil {
				fmt.Fprintln(os.Stderr, S.Err("[错误]")+" Shell 权限已禁用")
			} else if err := runner.Run(ctx, strings.TrimPrefix(line, "!")); err != nil {
				fmt.Fprintf(os.Stderr, "%s %v\n", S.Err("[错误]"), err)
			}
		default:
			usageTracker.BeginTurn()
			if err := enc.Encode(ipc.Msg{Text: line, Cwd: cwd}); err != nil {
				return fmt.Errorf("write: %w", err)
			}
			// Show spinner while waiting; stop it before rendering the reply.
			sp := NewSpinner("思考中…")
			err := printNextReply(ctx, scanner, enc, sp, usageTracker, interruptCh)
			sp.Stop()
			if err != nil {
				return err
			}
		}
	}
}

// printNextReply reads messages from the daemon until a final reply, info, or
// error is received. sp is the active spinner; it is stopped before any output.
func printNextReply(ctx context.Context, scanner *bufio.Scanner, enc *json.Encoder, sp *Spinner, usageTracker *UsageTracker, interruptCh <-chan os.Signal) error {
	var encMu sync.Mutex
	sendFrame := func(msg ipc.Msg) {
		encMu.Lock()
		defer encMu.Unlock()
		_ = enc.Encode(msg)
	}
	for {
		select {
		case <-interruptCh:
			// Drop stale Ctrl+C events fired outside the waiting phase.
		default:
			goto drained
		}
	}

drained:
	// cancelTurn is called on Ctrl-C to signal the interrupt handling goroutine.
	// The daemon is notified via a "cancel" IPC frame; no local context needed.
	cancelTurn := func() {}
	defer cancelTurn()

	var interrupted atomic.Bool
	var streamedAny atomic.Bool // true once at least one delta frame arrived
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		for {
			select {
			case <-stopWatch:
				return
			case <-interruptCh:
				if interrupted.CompareAndSwap(false, true) {
					cancelTurn()
					sp.Stop()
					sendFrame(ipc.Msg{Cmd: "cancel"})
					fmt.Printf("%s 已请求中断当前任务，等待服务端确认…\n", S.Warn("[中断]"))
				}
			}
		}
	}()

	for {
		// Respect context cancellation (e.g. Ctrl-C).
		if ctx.Err() != nil {
			sp.Stop()
			fmt.Fprintln(os.Stderr, S.Warn("[已取消]"))
			return nil
		}

		if !scanner.Scan() {
			sp.Stop()
			if err := scanner.Err(); err != nil {
				fmt.Fprintf(os.Stderr, "\n%s 守护进程连接中断：%v\n", S.Err("[错误]"), err)
				fmt.Fprintln(os.Stderr, S.Warn("提示：请运行  claw serve  重启守护进程"))
				return fmt.Errorf("daemon disconnected: %w", err)
			}
			fmt.Fprintln(os.Stderr, "\n"+S.Warn("[提示] 守护进程已关闭连接。"))
			return io.EOF
		}

		var msg ipc.Msg
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			// Malformed frame — don't hang, just warn and keep waiting.
			sp.Stop()
			fmt.Fprintf(os.Stderr, "%s 收到格式错误的消息，已跳过。\n", S.Warn("[警告]"))
			return nil
		}

		// ── Streaming deltas: print text incrementally ───────────────────────
		if msg.Delta != "" {
			if interrupted.Load() {
				continue
			}
			if !streamedAny.Load() {
				sp.Stop()
				streamedAny.Store(true)
			}
			fmt.Print(msg.Delta)
			continue
		}

		// ── Usage telemetry: update live throughput label and keep waiting ─────
		if msg.Usage != nil {
			if usageTracker != nil {
				usageTracker.Add(*msg.Usage)
				label := usageTracker.SpinnerLabel()
				if label != "" {
					sp.UpdateLabel("思考中… " + label)
				}
			}
			continue
		}

		// ── Terminal messages — always stop spinner first ──────────────────────
		sp.Stop()
		switch {
		case msg.Reply != "":
			if interrupted.Load() {
				fmt.Println(S.Dim("[已中断] 本轮回复已取消。"))
				return nil
			}
			if streamedAny.Load() {
				// Content was already printed via delta frames;
				// just close the line and show metrics.
				fmt.Println()
			} else {
				renderMarkdown(msg.Reply)
			}
			if usageTracker != nil {
				if line := usageTracker.TurnSummaryLine(); line != "" {
					style := S.Dim
					switch usageTracker.TurnLevel() {
					case "critical":
						style = S.Err
					case "warn":
						style = S.Warn
					}
					fmt.Printf("%s %s\n", style("[指标]"), style(line))
				}
			}
			return nil
		case msg.Info != "":
			fmt.Printf("%s %s\n", S.Dim("[提示]"), msg.Info)
			return nil
		case msg.Error != "":
			// Friendly boxed error — non-blocking.
			w := boxInnerWidth(44, 36, maxBoxWidth)
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, boxTopLine(w, " 错误 "))
			for _, ln := range strings.Split(msg.Error, "\n") {
				fmt.Fprintf(os.Stderr, "%s%s%s\n", S.Err("│"), padRight(truncatePlain(ln, w), w), S.Err("│"))
			}
			fmt.Fprintln(os.Stderr, boxBottomLine(w))
			return nil
		default:
			// Empty or unrecognised frame — do NOT loop forever; return cleanly.
			// This guards against models returning null/empty content.
			return nil
		}
	}
}

// selectSession presents the conversation list and returns the chosen name.
// Protocol: server sends sessions list → client sends cmd → server sends
// info (success) or error + new sessions list (retry).
func selectSession(enc *json.Encoder, scanner *bufio.Scanner, sessions []ipc.SessionInfo) (string, []ipc.HistoryEntry, error) {
	inputReader := bufio.NewReader(os.Stdin)
	for {
		// Draw the boxed session chooser; it also prints the input prompt.
		fmt.Println()
		DrawSessionList(sessions)

		line, readErr := inputReader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return "", nil, readErr
		}
		input := strings.TrimSpace(line)
		if readErr == io.EOF && input == "" {
			fmt.Println()
			return "", nil, io.EOF
		}
		if input == "" && len(sessions) > 0 {
			if chosen, ok := defaultSessionName(sessions); ok {
				if err := enc.Encode(ipc.Msg{Cmd: "select", Session: chosen}); err != nil {
					return "", nil, fmt.Errorf("write: %w", err)
				}
				resp, err := readMsg(scanner)
				if err != nil {
					return "", nil, err
				}
				if resp.Error != "" {
					fmt.Fprintf(os.Stderr, "%s %s\n", S.Err("错误："), resp.Error)
					sessions, err = readSessionList(scanner)
					if err != nil {
						return "", nil, err
					}
					continue
				}
				return chosen, resp.History, nil
			}
		}
		if input == "" {
			// Server hasn't moved; no list refresh needed.
			continue
		}

		// ── New conversation ──────────────────────────────────────────────────
		if input == "n" || len(sessions) == 0 {
			var name string
			if input != "n" {
				name = input
			} else {
				fmt.Print(S.Bold("对话名称") + "：")
				_, scanErr := fmt.Scanln(&name)
				if scanErr != nil {
					fmt.Println()
					return "", nil, io.EOF
				}
				name = strings.TrimSpace(name)
				if name == "" {
					fmt.Fprintln(os.Stderr, S.Warn("名称不能为空。"))
					continue // no server round-trip, no list refresh
				}
			}
			if err := enc.Encode(ipc.Msg{Cmd: "new", Session: name}); err != nil {
				return "", nil, fmt.Errorf("write: %w", err)
			}
			resp, err := readMsg(scanner)
			if err != nil {
				return "", nil, err
			}
			if resp.Error != "" {
				fmt.Fprintf(os.Stderr, "%s %s\n", S.Err("错误："), resp.Error)
				// Server re-sent the updated list; consume it for the next display.
				sessions, err = readSessionList(scanner)
				if err != nil {
					return "", nil, err
				}
				continue
			}
			return name, nil, nil
		}

		// ── Select existing by number ─────────────────────────────────────────
		n, err := strconv.Atoi(input)
		if err != nil || n < 1 || n > len(sessions) {
			fmt.Fprintf(os.Stderr, S.Warn("无效选择。")+"请输入 1～%d 之间的数字，或输入 'n'。\n", len(sessions))
			continue // no server round-trip, no list refresh
		}
		chosen := sessions[n-1].Name
		if err := enc.Encode(ipc.Msg{Cmd: "select", Session: chosen}); err != nil {
			return "", nil, fmt.Errorf("write: %w", err)
		}
		resp, err := readMsg(scanner)
		if err != nil {
			return "", nil, err
		}
		if resp.Error != "" {
			fmt.Fprintf(os.Stderr, "%s %s\n", S.Err("错误："), resp.Error)
			// Server re-sent the updated list; consume it for the next display.
			sessions, err = readSessionList(scanner)
			if err != nil {
				return "", nil, err
			}
			continue
		}
		return chosen, resp.History, nil
	}
}

func defaultSessionName(sessions []ipc.SessionInfo) (string, bool) {
	if len(sessions) == 0 {
		return "", false
	}
	for _, s := range sessions {
		if s.Name == session.MainSessionKey {
			return s.Name, true
		}
	}
	return sessions[0].Name, true
}

// readSessionList reads one message from the server and returns its Sessions
// field. Used to consume the refreshed session list the server sends after an
// error in the selection phase.
func readSessionList(scanner *bufio.Scanner) ([]ipc.SessionInfo, error) {
	msg, err := readMsg(scanner)
	if err != nil {
		return nil, err
	}
	if msg.Error != "" {
		return nil, fmt.Errorf("守护进程错误：%s", msg.Error)
	}
	return msg.Sessions, nil
}

func readMsg(scanner *bufio.Scanner) (ipc.Msg, error) {
	if !scanner.Scan() {
		return ipc.Msg{}, fmt.Errorf("连接已断开")
	}
	var msg ipc.Msg
	if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
		return ipc.Msg{}, fmt.Errorf("无效响应：%w", err)
	}
	return msg, nil
}

func readLineWithContinuation(rl *readline.Instance, basePrompt string) (string, error) {
	line, err := rl.Readline()
	if err != nil {
		return line, err
	}

	parts := []string{}
	current := line
	promptChanged := false
	for {
		r := strings.TrimRight(current, " \t")
		if !strings.HasSuffix(r, "\\") {
			parts = append(parts, current)
			break
		}
		parts = append(parts, strings.TrimSuffix(r, "\\"))
		if !promptChanged {
			rl.SetPrompt("... ")
			promptChanged = true
		}
		next, readErr := rl.Readline()
		if readErr != nil {
			if promptChanged {
				rl.SetPrompt(basePrompt)
			}
			return strings.Join(parts, "\n"), readErr
		}
		current = next
	}
	if promptChanged {
		rl.SetPrompt(basePrompt)
	}
	return strings.Join(parts, "\n"), nil
}

func readMultilineInput(rl *readline.Instance, basePrompt string) (string, bool, error) {
	fmt.Println(S.Dim("多行输入模式：输入 /send 发送，/abort 取消。"))
	rl.SetPrompt("... ")
	defer rl.SetPrompt(basePrompt)

	var lines []string
	for {
		line, err := rl.Readline()
		if err != nil {
			if err == readline.ErrInterrupt {
				fmt.Println(S.Dim("已取消多行输入。"))
				return "", false, nil
			}
			if err == io.EOF {
				return "", false, io.EOF
			}
			return "", false, err
		}

		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "/abort":
			fmt.Println(S.Dim("已取消多行输入。"))
			return "", false, nil
		case "/send":
			msg := strings.TrimSpace(strings.Join(lines, "\n"))
			if msg == "" {
				fmt.Fprintln(os.Stderr, S.Warn("多行内容为空，已取消。"))
				return "", false, nil
			}
			return msg, true, nil
		default:
			lines = append(lines, line)
		}
	}
}

// runLearn runs the knowledge distillation pipeline for topic and prints
// live progress to stdout.
func runLearn(ctx context.Context, d *knowledge.Distiller, topic string) {
	fmt.Printf("%s 开始提炼主题 %s 的经验知识…\n", S.Bold("→"), S.Bold(topic))
	sp := NewSpinner("读取记忆…")
	progress := func(msg string) {
		sp.UpdateLabel(msg)
	}
	content, err := d.Distill(ctx, topic, progress)
	sp.Stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s %v\n", S.Err("[error]"), err)
		return
	}
	lines := strings.Count(content, "\n")
	fmt.Printf("%s 经验库已更新！共 %d 行。使用 /exp show %q 查看。\n",
		S.Success("✓"), lines, topic)
}

// runExp handles /exp subcommands: ls, show, rm, use.
func runExp(ctx context.Context, arg string, store *knowledge.ExperienceStore,
	enc *json.Encoder, scanner *bufio.Scanner) {
	parts := strings.SplitN(arg, " ", 2)
	subcmd := strings.ToLower(strings.TrimSpace(parts[0]))
	param := ""
	if len(parts) > 1 {
		param = strings.Trim(strings.TrimSpace(parts[1]), `"'`)
	}

	switch subcmd {
	case "", "ls":
		list, err := store.List()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s %v\n", S.Err("[error]"), err)
			return
		}
		if len(list) == 0 {
			fmt.Println(S.Dim("暂无经验库。使用 /learn \"<主题>\" 开始提炼。"))
			return
		}
		w := boxInnerWidth(50, 46, maxBoxWidth)
		printBoxTop(w, "Experience Library")
		contentW := boxContentWidth(w)
		for i, m := range list {
			header := fmt.Sprintf("%d. %s", i+1, m.Topic)
			for _, l := range wrapPlain(header, contentW) {
				printBoxStyledLine(w, S.Bold(l))
			}
			size := fmt.Sprintf("%d KB", (m.Size+512)/1024)
			updated := m.UpdatedAt.Format("01-02 15:04")
			meta := fmt.Sprintf("   size: %s   updated: %s", size, updated)
			for _, l := range wrapPlain(meta, contentW) {
				printBoxStyledLine(w, S.Dim(l))
			}
			if i < len(list)-1 {
				printBoxDivider(w)
			}
		}
		printBoxBottom(w)
		fmt.Println(S.Dim("提示: /exp show <主题>  /exp use <主题>  /exp rm <主题>"))

	case "show":
		if param == "" {
			fmt.Fprintln(os.Stderr, S.Warn("Usage: /exp show <topic>"))
			return
		}
		content, err := store.Load(param)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s %v\n", S.Err("[error]"), err)
			return
		}
		if content == "" {
			fmt.Fprintf(os.Stderr, S.Warn("找不到主题 %q 的经验库\n"), param)
			return
		}
		renderMarkdown(content)

	case "rm":
		if param == "" {
			fmt.Fprintln(os.Stderr, S.Warn("Usage: /exp rm <topic>"))
			return
		}
		if err := store.Delete(param); err != nil {
			fmt.Fprintf(os.Stderr, "%s %v\n", S.Err("[error]"), err)
			return
		}
		fmt.Printf("%s 已删除主题 %q 的经验库\n", S.Success("✓"), param)

	case "use":
		if param == "" {
			fmt.Fprintln(os.Stderr, S.Warn("Usage: /exp use <topic>"))
			return
		}
		content, err := store.Load(param)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s %v\n", S.Err("[error]"), err)
			return
		}
		if content == "" {
			fmt.Fprintf(os.Stderr, S.Warn("找不到主题 %q 的经验库，请先运行 /learn %q\n"), param, param)
			return
		}
		// Send inject_ctx command to daemon so it injects into the active session.
		if err := enc.Encode(ipc.Msg{Cmd: "inject_ctx", Text: content}); err != nil {
			fmt.Fprintf(os.Stderr, "%s %v\n", S.Err("[error]"), err)
			return
		}
		// Wait for ack.
		resp, err := readMsg(scanner)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s %v\n", S.Err("[error]"), err)
			return
		}
		if resp.Error != "" {
			fmt.Fprintf(os.Stderr, "%s %s\n", S.Err("[error]"), resp.Error)
			return
		}
		fmt.Printf("%s 经验库 %q 已挂载到当前会话上下文\n", S.Success("✓"), param)

	default:
		fmt.Fprintf(os.Stderr, S.Warn("未知子命令 %q。可用: ls, show, rm, use\n"), subcmd)
	}
}

func printHelp() {
	w := boxInnerWidth(58, 54, maxBoxWidth)
	printBoxTop(w, "帮助")
	cmdWidth := 22
	if w < 58 {
		cmdWidth = 18
	}
	// Extra non-slash entries shown at the bottom.
	extras := [][2]string{
		{"粘贴多行", "自动缓冲为一条消息，按 Enter 确认发送"},
		{"!<命令>", "直接运行本地 Shell 命令（需开启 shell_enabled）"},
		{"<消息>", "发送消息给 AI 助手"},
	}
	for _, e := range commands {
		argHint := ""
		if len(e.args) > 0 {
			argHint = " " + strings.Join(e.args, "|")
		}
		printBoxKeyValueLines(w, e.cmd+argHint, e.desc, cmdWidth, S.Bold, S.Dim)
	}
	printBoxDivider(w)
	for _, item := range extras {
		printBoxKeyValueLines(w, item[0], item[1], cmdWidth, S.Bold, S.Dim)
	}
	printBoxBottom(w)
}

func printTokenReport(usageTracker *UsageTracker) {
	if usageTracker == nil {
		fmt.Println(S.Dim("暂无 token 统计。"))
		return
	}
	w := boxInnerWidth(86, 48, maxBoxWidth)
	lines := usageTracker.PrettyReportLines()
	if isCompactWidth(w) {
		lines = usageTracker.CompactReportLines()
	}
	title := " Token 报表 "
	printBoxTop(w, title)
	keyWidth := 12
	if isCompactWidth(w) {
		keyWidth = 10
	}
	first := true
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			printBoxWrappedTextLines(w, "  "+line, S.Dim)
			first = false
			continue
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			printBoxWrappedTextLines(w, line, S.Dim)
			first = false
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if value == "" {
			if !first {
				printBoxDivider(w)
			}
			printBoxWrappedTextLines(w, key, S.Bold)
			first = false
			continue
		}
		printBoxKeyValueLines(w, key, value, keyWidth, S.Bold, S.Dim)
		first = false
	}
	printBoxBottom(w)
}

func handleTokensCommand(line string, usageTracker *UsageTracker) {
	if usageTracker == nil {
		fmt.Println(S.Dim("暂无 token 统计。"))
		return
	}
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/tokens"))
	if arg == "" || arg == "report" || arg == "r" {
		printTokenReport(usageTracker)
		return
	}
	switch arg {
	case "turn", "t":
		if s := usageTracker.TurnSummaryLine(); s != "" {
			fmt.Printf("%s %s\n", S.Bold("[本轮]"), S.Dim(s))
		} else {
			fmt.Println(S.Dim("本轮暂无 token 调用。"))
		}
	case "clear", "c", "reset":
		usageTracker.Reset()
		fmt.Println(S.Success("[tokens] 已清空本次 CLI 的 token 统计。"))
	default:
		fmt.Println(S.Warn("用法: /tokens [report|turn|clear]"))
	}
}
