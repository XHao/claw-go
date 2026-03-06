// Package client — theme / colour-palette system.
//
// All UI code references the package-level variable S (the active Theme) rather
// than hard-coded ANSI escape sequences.  This makes colour completely optional:
// S.Dim("text") returns plain "text" when colour is disabled.
//
// Colour is automatically disabled when:
//   - The NO_COLOR environment variable is set (https://no-color.org/).
//   - TERM is set to "dumb".
//   - stdout is not a real TTY (e.g. redirected to a file or pipe).
//
// Users can customise the palette via config.yaml:
//
//	theme:
//	  preset: "default"   # default | dark | minimal | none
//	  colors:
//	    assistant: "1;35"  # override one slot with a custom ANSI SGR code
package client

import (
	"fmt"
	"os"
	"strings"

	"github.com/XHao/claw-go/config"
	"golang.org/x/term"
)

// colorEnabled is resolved once at package init from the environment.
var colorEnabled bool

func init() {
	colorEnabled = detectColor()
}

func detectColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if strings.ToLower(os.Getenv("TERM")) == "dumb" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// ─── Palette ──────────────────────────────────────────────────────────────────

// Palette holds raw ANSI SGR parameter strings (e.g. "1;36") for every
// semantic UI slot.  An empty string disables styling for that slot.
type Palette struct {
	Assistant string // assistant reply label
	User      string // user-prompt highlight (future use)
	Dim       string // secondary / muted text
	Success   string // tool success ✓
	Warn      string // warnings, [busy] indicator
	Err       string // error messages
	Bold      string // generic emphasis
	Border    string // box-drawing & dividers
	Timestamp string // time display
	ToolName  string // tool name in progress list
}

// Built-in presets ─────────────────────────────────────────────────────────────

var (
	// paletteDefault is the standard colour scheme (cool-tone).
	paletteDefault = Palette{
		Assistant: "1;36", // bold cyan
		User:      "1;32", // bold green
		Dim:       "90",   // dark gray
		Success:   "32",   // green
		Warn:      "33",   // yellow
		Err:       "31",   // red
		Bold:      "1",    // bold
		Border:    "36",   // cyan
		Timestamp: "90",   // dark gray
		ToolName:  "35",   // magenta
	}

	// paletteDark suits dark terminals that prefer a warmer accent colour.
	paletteDark = Palette{
		Assistant: "1;34", // bold blue
		User:      "1;33", // bold yellow
		Dim:       "2",    // faint
		Success:   "32",
		Warn:      "33",
		Err:       "31",
		Bold:      "1",
		Border:    "34", // blue
		Timestamp: "2",  // faint
		ToolName:  "33", // yellow
	}

	// paletteMinimal keeps only bold/reset — no colour, still structured.
	paletteMinimal = Palette{Bold: "1"}

	// paletteNone disables all styling.
	paletteNone = Palette{}
)

// ─── Theme ────────────────────────────────────────────────────────────────────

// Theme wraps a Palette and provides named helper methods used throughout
// the client UI.  Use the package-level S variable rather than creating your
// own Theme.
type Theme struct {
	Name    string
	palette Palette
}

// S is the active theme.  Initialised to the default palette; call
// ApplyTheme() once before any UI output to apply config-driven overrides.
var S = &Theme{Name: "default", palette: paletteDefault}

// ApplyTheme initialises S from a ThemeConfig.
// Must be called before any terminal output.
func ApplyTheme(cfg config.ThemeConfig) {
	if !colorEnabled {
		S = &Theme{Name: "none", palette: paletteNone}
		return
	}

	var base Palette
	switch strings.ToLower(cfg.Preset) {
	case "none", "off":
		S = &Theme{Name: "none", palette: paletteNone}
		return
	case "minimal":
		base = paletteMinimal
	case "dark":
		base = paletteDark
	default: // "default" or ""
		base = paletteDefault
	}

	// Overlay any per-slot user overrides on top of the chosen preset.
	applyColorOverrides(&base, cfg.Colors)
	S = &Theme{Name: cfg.Preset, palette: base}
}

func applyColorOverrides(p *Palette, c config.ThemeColors) {
	if c.Assistant != "" {
		p.Assistant = c.Assistant
	}
	if c.User != "" {
		p.User = c.User
	}
	if c.Dim != "" {
		p.Dim = c.Dim
	}
	if c.Success != "" {
		p.Success = c.Success
	}
	if c.Warn != "" {
		p.Warn = c.Warn
	}
	if c.Error != "" {
		p.Err = c.Error
	}
	if c.Bold != "" {
		p.Bold = c.Bold
	}
	if c.Border != "" {
		p.Border = c.Border
	}
	if c.Timestamp != "" {
		p.Timestamp = c.Timestamp
	}
	if c.ToolName != "" {
		p.ToolName = c.ToolName
	}
}

// ─── Styling helpers ──────────────────────────────────────────────────────────

// c applies a SGR code to text.  Returns text unchanged when colour is
// disabled or the code is empty.
func (t *Theme) c(code, text string) string {
	if !colorEnabled || code == "" || text == "" {
		return text
	}
	return fmt.Sprintf("\033[%sm%s\033[0m", code, text)
}

// Semantic styling methods — use these instead of raw ANSI codes.
func (t *Theme) Assistant(s string) string { return t.c(t.palette.Assistant, s) }
func (t *Theme) User(s string) string      { return t.c(t.palette.User, s) }
func (t *Theme) Dim(s string) string       { return t.c(t.palette.Dim, s) }
func (t *Theme) Success(s string) string   { return t.c(t.palette.Success, s) }
func (t *Theme) Warn(s string) string      { return t.c(t.palette.Warn, s) }
func (t *Theme) Err(s string) string       { return t.c(t.palette.Err, s) }
func (t *Theme) Bold(s string) string      { return t.c(t.palette.Bold, s) }
func (t *Theme) Border(s string) string    { return t.c(t.palette.Border, s) }
func (t *Theme) Timestamp(s string) string { return t.c(t.palette.Timestamp, s) }
func (t *Theme) ToolName(s string) string  { return t.c(t.palette.ToolName, s) }
