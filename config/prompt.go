package config

import (
	"os"
	"os/user"
	"runtime"
	"strings"
	"time"
)

// ExpandPrompt replaces template variables in a system-prompt string with
// live values from the current runtime environment.
//
// Supported variables:
//
//	{cwd}      current working directory
//	{os}       operating system  (darwin / linux / windows …)
//	{arch}     CPU architecture  (amd64 / arm64 …)
//	{shell}    value of $SHELL   (/bin/zsh, /bin/bash …)
//	{home}     user home directory
//	{user}     login username
//	{hostname} machine hostname
//	{datetime} current date+time (2006-01-02 15:04:05)
//	{date}     current date only (2006-01-02)
func ExpandPrompt(prompt string) string {
	if !strings.ContainsRune(prompt, '{') {
		return prompt // fast path: nothing to expand
	}

	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	hostname, _ := os.Hostname()
	shell := os.Getenv("SHELL")
	if shell == "" {
		if runtime.GOOS == "windows" {
			shell = "cmd.exe"
		} else {
			shell = "/bin/sh"
		}
	}
	username := os.Getenv("USER")
	if username == "" {
		if u, err := user.Current(); err == nil {
			username = u.Username
		}
	}
	now := time.Now()

	r := strings.NewReplacer(
		"{cwd}", cwd,
		"{os}", runtime.GOOS,
		"{arch}", runtime.GOARCH,
		"{shell}", shell,
		"{home}", home,
		"{user}", username,
		"{hostname}", hostname,
		"{datetime}", now.Format("2006-01-02 15:04:05"),
		"{date}", now.Format("2006-01-02"),
	)
	return r.Replace(prompt)
}
