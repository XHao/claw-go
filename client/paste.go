package client

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Bracketed Paste Mode escape sequences (xterm standard).
// Reference: https://invisible-island.net/xterm/ctlseqs/ctlseqs.html
const (
	bpmEnable  = "\x1b[?2004h" // sent to terminal: enable bracketed paste mode
	bpmDisable = "\x1b[?2004l" // sent to terminal: disable bracketed paste mode
	bpmStart   = "\x1b[200~"   // terminal sends this before pasted content
	bpmEnd     = "\x1b[201~"   // terminal sends this after pasted content
)

// pasteInterceptStdin wraps an io.ReadCloser (typically os.Stdin) to enable
// Bracketed Paste Mode and intercept multi-line pastes.
//
// Behaviour:
//   - Single-line paste (no newlines): forwarded to readline unchanged.
//   - Multi-line paste:
//     1. All pasted lines are collected.
//     2. Terminal shows "↓ 粘贴了 N 行，按 Enter 确认发送..."
//     3. Read() blocks until the user presses Enter (or Ctrl-C to cancel).
//     4. On confirmation, the joined text is stored in pendingPaste, and a
//        bare '\r' is returned to readline — readline submits an empty line.
//     5. The chat loop calls TakePaste() after readLineWithContinuation
//        returns ""; if non-empty, uses that as the actual message.
//
// This design avoids sending '\n' bytes to readline (which would cause it to
// split the content into multiple submitted lines).
type pasteInterceptStdin struct {
	raw io.ReadCloser

	mu          sync.Mutex
	pendingPaste string  // set by handlePaste; consumed by TakePaste
	escBuf      []byte  // partial escape sequence carried across Read() calls
	outBuf      []byte  // overflow bytes to return on next Read()
	closed      bool
}

// newPasteInterceptStdin creates a pasteInterceptStdin, enables bracketed paste
// mode on the terminal, and returns the wrapper.  Call Close() to restore state.
func newPasteInterceptStdin(stdin io.ReadCloser) (*pasteInterceptStdin, error) {
	if _, err := os.Stdout.Write([]byte(bpmEnable)); err != nil {
		return nil, fmt.Errorf("enable bracketed paste: %w", err)
	}
	return &pasteInterceptStdin{raw: stdin}, nil
}

// TakePaste returns and clears the pending multi-line paste content, if any.
// The chat loop calls this when readline returns an empty line.
func (p *pasteInterceptStdin) TakePaste() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.pendingPaste
	p.pendingPaste = ""
	return s
}

// Read implements io.Reader.  Called by readline's internal bufio.Reader.
func (p *pasteInterceptStdin) Read(b []byte) (int, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return 0, io.EOF
	}
	// Drain overflow from a previous call.
	if len(p.outBuf) > 0 {
		n := copy(b, p.outBuf)
		p.outBuf = p.outBuf[n:]
		p.mu.Unlock()
		return n, nil
	}
	p.mu.Unlock()

	// Read from real stdin.
	tmp := make([]byte, len(b))
	n, err := p.raw.Read(tmp)
	if err != nil {
		return 0, err
	}
	data := tmp[:n]

	// Prepend any saved partial escape sequence.
	p.mu.Lock()
	if len(p.escBuf) > 0 {
		data = append(p.escBuf, data...)
		p.escBuf = nil
	}
	p.mu.Unlock()

	out, partial := p.process(data)

	p.mu.Lock()
	p.escBuf = partial
	p.mu.Unlock()

	if len(out) == 0 {
		// Paste was detected and handled; a bare '\r' (Enter) may have been
		// placed in outBuf by handlePaste — drain it now.
		p.mu.Lock()
		if len(p.outBuf) > 0 {
			n = copy(b, p.outBuf)
			p.outBuf = p.outBuf[n:]
			p.mu.Unlock()
			return n, nil
		}
		p.mu.Unlock()
		// outBuf is empty: paste was cancelled (Ctrl-C). Fall through to read
		// the next real input byte so readline can continue normally.
		return p.raw.Read(b)
	}

	cn := copy(b, out)
	p.mu.Lock()
	if len(out) > cn {
		p.outBuf = append(p.outBuf, out[cn:]...)
	}
	p.mu.Unlock()
	return cn, nil
}

// process scans data for BPM start/end markers.
// Returns out (to pass to readline) and partial (trailing partial escape to save).
func (p *pasteInterceptStdin) process(data []byte) (out []byte, partial []byte) {
	for len(data) > 0 {
		idx := bytes.Index(data, []byte(bpmStart))
		if idx < 0 {
			// No paste-start in this chunk.  Save any trailing partial marker.
			partial = trailingEscapePrefix(data, bpmStart)
			if len(partial) > 0 {
				out = append(out, data[:len(data)-len(partial)]...)
			} else {
				out = append(out, data...)
			}
			return
		}

		// Forward bytes before the paste-start marker.
		out = append(out, data[:idx]...)
		data = data[idx+len(bpmStart):]

		// Collect pasted content until paste-end.
		var pasted []byte
		for {
			end := bytes.Index(data, []byte(bpmEnd))
			if end >= 0 {
				pasted = append(pasted, data[:end]...)
				data = data[end+len(bpmEnd):]
				break
			}
			// End marker not yet received; read more directly from stdin.
			pasted = append(pasted, data...)
			data = data[:0]
			more := make([]byte, 512)
			nn, readErr := p.raw.Read(more)
			if readErr != nil {
				break // treat as end of paste on error
			}
			data = more[:nn]
		}

		// Handle the paste: may block waiting for user confirmation.
		injected := p.handlePaste(pasted)
		if len(injected) > 0 {
			// Store in outBuf; we'll return it after process() finishes.
			p.mu.Lock()
			p.outBuf = append(p.outBuf, injected...)
			p.mu.Unlock()
		}
		// Return nothing in 'out' for this paste block; the injected bytes
		// are in outBuf and will be drained on the next Read() call or below.
	}
	return
}

// handlePaste displays a confirmation prompt for multi-line pastes, waits for
// the user to press Enter, then returns bytes to inject into readline's stream.
//
// Single-line paste: return content + '\r' directly (no prompt).
// Multi-line paste:  store joined content in pendingPaste, return '\r' so that
//                    readline submits an empty line; TakePaste() retrieves it.
// Ctrl-C:           return nil (paste cancelled).
func (p *pasteInterceptStdin) handlePaste(content []byte) []byte {
	raw := string(content)
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	lines := strings.Split(raw, "\n")
	// Drop trailing empty entry produced by a trailing newline.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}

	// Single-line paste: pass through as-is + Enter.
	if len(lines) == 1 {
		return []byte(lines[0] + "\r")
	}

	// Multi-line paste: show prompt and wait for confirmation.
	lineCount := len(lines)
	fmt.Fprintf(os.Stdout, "\r\033[K") // CR + clear to end of line
	fmt.Fprintf(os.Stdout, "%s 粘贴了 %s 行，按 %s 确认发送，%s 取消...",
		S.Dim("↓"),
		S.Bold(fmt.Sprintf("%d", lineCount)),
		S.Bold("Enter"),
		S.Dim("Ctrl-C"))

	// Wait for user confirmation by reading one byte from real stdin.
	// (We are inside readline's ioloop goroutine, so p.raw.Read is safe.)
	for {
		oneByte := make([]byte, 1)
		_, err := p.raw.Read(oneByte)
		if err != nil {
			break
		}
		ch := oneByte[0]
		switch ch {
		case '\r', '\n':
			// Confirmed.
			fmt.Fprintf(os.Stdout, "\r\033[K")
			joined := strings.Join(lines, "\n")
			p.mu.Lock()
			p.pendingPaste = joined
			p.mu.Unlock()
			// Return a bare '\r' so readline submits a (visually empty) line.
			// TakePaste() will be called by the chat loop to get the real text.
			return []byte("\r")
		case 3: // Ctrl-C
			fmt.Fprintf(os.Stdout, "\r\033[K%s 已取消粘贴。\n", S.Warn("✗"))
			return nil
		default:
			// Ignore other keys.
		}
	}
	return nil
}

// Close disables bracketed paste mode and closes the underlying stdin.
func (p *pasteInterceptStdin) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	_, _ = os.Stdout.Write([]byte(bpmDisable))
	return p.raw.Close()
}

// trailingEscapePrefix returns the longest suffix of data that is a proper
// prefix of target.  Used to detect partial escape sequences at the end of a
// Read() chunk so they can be saved and re-prepended on the next call.
func trailingEscapePrefix(data []byte, target string) []byte {
	tb := []byte(target)
	for i := len(tb) - 1; i >= 1; i-- {
		if bytes.HasSuffix(data, tb[:i]) {
			return tb[:i]
		}
	}
	return nil
}
