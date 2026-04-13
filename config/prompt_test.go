package config

import (
	"strings"
	"testing"
)

func TestExpandStaticVarsPreservesTimeVars(t *testing.T) {
	prompt := "os={os} date={date} datetime={datetime}"
	result := ExpandStaticVars(prompt)

	if strings.Contains(result, "{os}") {
		t.Errorf("ExpandStaticVars should expand {os}, got: %q", result)
	}
	if !strings.Contains(result, "{date}") {
		t.Errorf("ExpandStaticVars should preserve {date}, got: %q", result)
	}
	if !strings.Contains(result, "{datetime}") {
		t.Errorf("ExpandStaticVars should preserve {datetime}, got: %q", result)
	}
}

func TestExpandStaticVarsExpandsAllNonTimeVars(t *testing.T) {
	prompt := "{cwd} {os} {arch} {shell} {home} {user} {hostname}"
	result := ExpandStaticVars(prompt)

	for _, v := range []string{"{cwd}", "{os}", "{arch}", "{shell}", "{home}", "{user}", "{hostname}"} {
		if strings.Contains(result, v) {
			t.Errorf("ExpandStaticVars should expand %s, got: %q", v, result)
		}
	}
}

func TestExpandTimeVarsExpandsDateAndDatetime(t *testing.T) {
	prompt := "date={date} datetime={datetime} os={os}"
	result := ExpandTimeVars(prompt)

	if strings.Contains(result, "{date}") {
		t.Errorf("ExpandTimeVars should expand {date}, got: %q", result)
	}
	if strings.Contains(result, "{datetime}") {
		t.Errorf("ExpandTimeVars should expand {datetime}, got: %q", result)
	}
	// {os} should remain untouched
	if !strings.Contains(result, "{os}") {
		t.Errorf("ExpandTimeVars should NOT expand {os}, got: %q", result)
	}
}
