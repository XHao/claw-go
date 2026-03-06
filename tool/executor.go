// Package tool provides execution helpers for interactive shell commands.
package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ShellExecutor runs shell commands with stdin/stdout/stderr attached to the
// current terminal so fully-interactive programs (vim, htop, ssh, …) work.
type ShellExecutor struct {
	shell       string        // e.g. "/bin/bash"
	timeout     time.Duration // 0 → no timeout
	allowedCmds map[string]bool
}

// NewShellExecutor creates a ShellExecutor.
//
//   - shell           – shell binary path; "" ⇒ $SHELL ⇒ /bin/sh
//   - timeoutSeconds  – per-command timeout; 0 disables the timeout
//   - allowedCommands – if non-empty, only these command names are permitted
func NewShellExecutor(shell string, timeoutSeconds int, allowedCommands []string) *ShellExecutor {
	if shell == "" {
		shell = os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
	}

	allowed := make(map[string]bool, len(allowedCommands))
	for _, c := range allowedCommands {
		if t := strings.TrimSpace(c); t != "" {
			allowed[t] = true
		}
	}

	var d time.Duration
	if timeoutSeconds > 0 {
		d = time.Duration(timeoutSeconds) * time.Second
	}

	return &ShellExecutor{
		shell:       shell,
		timeout:     d,
		allowedCmds: allowed,
	}
}

// Run executes command (passed to the shell via -c) interactively.
// stdin, stdout, and stderr are the process's own file descriptors, so
// TUI programs receive a proper terminal.
func (e *ShellExecutor) Run(ctx context.Context, command string) error {
	if err := e.checkAllowed(command); err != nil {
		return err
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if e.timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, e.timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, e.shell, "-c", command)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// checkAllowed returns an error when the command's leading word is not on the
// allowlist. An empty allowlist means everything is permitted.
func (e *ShellExecutor) checkAllowed(command string) error {
	if len(e.allowedCmds) == 0 {
		return nil
	}
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
	}
	// Use only the basename so "allowed: git" also permits "/usr/bin/git".
	name := parts[0]
	if idx := strings.LastIndexByte(name, '/'); idx >= 0 {
		name = name[idx+1:]
	}
	if !e.allowedCmds[name] {
		return fmt.Errorf("command %q is not in the allowed list", name)
	}
	return nil
}
