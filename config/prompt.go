package config

import (
	"os"
	"os/user"
	"runtime"
	"strings"
	"time"
)

// ExpandTimeVars replaces only time-related template variables in a prompt
// string with live values. Use this for per-call expansion so that {date}
// and {datetime} always reflect the current time without touching variables
// like {cwd} or {os} that are only meaningful at daemon startup.
//
// Supported variables:
//
//	{datetime} current date+time (2006-01-02 15:04:05)
//	{date}     current date only (2006-01-02)
func ExpandTimeVars(prompt string) string {
	if !strings.ContainsRune(prompt, '{') {
		return prompt
	}
	now := time.Now()
	r := strings.NewReplacer(
		"{datetime}", now.Format("2006-01-02 15:04:05"),
		"{date}", now.Format("2006-01-02"),
	)
	return r.Replace(prompt)
}

// ExpandStaticVars replaces all non-time template variables with live values
// from the current runtime environment, leaving {date} and {datetime}
// untouched so they can be expanded dynamically on each LLM call.
// Use this at daemon startup instead of ExpandPrompt.
//
// Supported variables (same as ExpandPrompt minus time vars):
//
//	{cwd}      current working directory
//	{os}       operating system  (darwin / linux / windows …)
//	{arch}     CPU architecture  (amd64 / arm64 …)
//	{shell}    value of $SHELL   (/bin/zsh, /bin/bash …)
//	{home}     user home directory
//	{user}     login username
//	{hostname} machine hostname
func ExpandStaticVars(prompt string) string {
	if !strings.ContainsRune(prompt, '{') {
		return prompt
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

	r := strings.NewReplacer(
		"{cwd}", cwd,
		"{os}", runtime.GOOS,
		"{arch}", runtime.GOARCH,
		"{shell}", shell,
		"{home}", home,
		"{user}", username,
		"{hostname}", hostname,
	)
	return r.Replace(prompt)
}

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
