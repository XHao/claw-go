package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// ── Official openclaw JSON schema – only the fields we actually use ───────────
//
// All other fields present in the file are silently ignored; this keeps the
// surface area minimal and avoids accidentally acting on settings that have no
// meaning in claw-go.

type openclawJSON struct {
	Models struct {
		Providers map[string]openclawProvider `json:"providers"`
	} `json:"models"`
	Agents struct {
		Defaults struct {
			Model struct {
				Primary string `json:"primary"` // "provider-id/model-id"
			} `json:"model"`
			SystemPrompt string `json:"systemPrompt"`
		} `json:"defaults"`
	} `json:"agents"`
}

type openclawProvider struct {
	BaseURL string          `json:"baseUrl"`
	APIKey  string          `json:"apiKey"` // may contain ${ENV_VAR}
	Models  []openclawModel `json:"models"`
}

// Only the two numeric fields we forward; name/contextWindow etc. are ignored.
type openclawModel struct {
	ID        string `json:"id"`
	MaxTokens int    `json:"maxTokens"`
}

// ── Loader ────────────────────────────────────────────────────────────────────

// loadOpenClawJSON parses an official openclaw.json config file and maps the
// provider / model settings into a Config. CLI and daemon settings are left at
// zero values so that applyDefaults() fills them in afterwards.
func loadOpenClawJSON(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}

	var raw openclawJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config: parse JSON %q: %w", path, err)
	}

	// Determine which provider/model to use from agents.defaults.model.primary.
	primary := raw.Agents.Defaults.Model.Primary // e.g. "qwen-portal/qwq-32b"
	if primary == "" {
		// Fallback: pick the first provider's first model.
		for pid, prov := range raw.Models.Providers {
			if len(prov.Models) > 0 {
				primary = pid + "/" + prov.Models[0].ID
				break
			}
		}
	}
	if primary == "" {
		return nil, fmt.Errorf("config: no model found in %q (set agents.defaults.model.primary)", path)
	}

	parts := strings.SplitN(primary, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf(
			"config: agents.defaults.model.primary %q must be \"<provider>/<model-id>\"",
			primary,
		)
	}
	providerID, modelID := parts[0], parts[1]

	// model-id must only contain safe characters (alphanumeric, dash, dot, colon).
	if !safeIDRe.MatchString(modelID) {
		return nil, fmt.Errorf("config: model id %q contains invalid characters", modelID)
	}

	if _, ok := raw.Models.Providers[providerID]; !ok {
		return nil, fmt.Errorf("config: provider %q (from agents.defaults.model.primary) not found", providerID)
	}

	var cfg Config
	cfg.Models = map[string]ProviderConfig{}

	for pid, p := range raw.Models.Providers {
		pidURL, err := expandAPIEnvVar(p.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("config: provider %q baseUrl: %w", pid, err)
		}
		pidKey, err := expandAPIEnvVar(p.APIKey)
		if err != nil {
			return nil, fmt.Errorf("config: provider %q apiKey: %w", pid, err)
		}
		if !strings.HasPrefix(pidURL, "http://") && !strings.HasPrefix(pidURL, "https://") {
			return nil, fmt.Errorf("config: provider %q baseUrl must start with http:// or https://", pid)
		}
		for _, m := range p.Models {
			if !safeIDRe.MatchString(m.ID) {
				return nil, fmt.Errorf("config: model id %q contains invalid characters", m.ID)
			}
			key := pid + "/" + m.ID
			cfg.Models[key] = ProviderConfig{
				Type:         "openai",
				BaseURL:      pidURL,
				APIKey:       pidKey,
				Model:        m.ID,
				SystemPrompt: raw.Agents.Defaults.SystemPrompt,
				MaxTokens:    m.MaxTokens,
			}
		}
	}

	modelKey := providerID + "/" + modelID
	if _, ok := cfg.Models[modelKey]; !ok {
		return nil, fmt.Errorf("config: model %q not found in provider %q", modelID, providerID)
	}
	cfg.PrimaryModel = modelKey

	// Keep Provider populated for non-serve code paths that still read it.
	cfg.Provider = cfg.Models[modelKey]
	return &cfg, nil
}

// safeIDRe matches model / provider identifiers that are safe to use as-is.
var safeIDRe = regexp.MustCompile(`^[a-zA-Z0-9][-a-zA-Z0-9_.:/]*$`)

// envVarNameRe matches only strict POSIX env var names: uppercase letters,
// digits, underscore, must start with a letter. This is intentionally narrow
// so that an attacker who controls openclaw.json cannot reference arbitrary
// variables (lowercase names, paths, etc.).
var envVarNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// envVarRefRe matches ${...} placeholders in a string.
var envVarRefRe = regexp.MustCompile(`\$\{([^}]*)\}`)

// expandAPIEnvVar expands ${VAR} references in s. Only uppercase POSIX-style
// variable names are permitted; any other placeholder is an error rather than
// silently leaking or being ignored.
func expandAPIEnvVar(s string) (string, error) {
	var expandErr error
	result := envVarRefRe.ReplaceAllStringFunc(s, func(match string) string {
		if expandErr != nil {
			return ""
		}
		name := match[2 : len(match)-1] // strip ${ and }
		if !envVarNameRe.MatchString(name) {
			expandErr = fmt.Errorf("env var name %q is not a valid POSIX uppercase identifier", name)
			return ""
		}
		return os.Getenv(name)
	})
	if expandErr != nil {
		return "", expandErr
	}
	return result, nil
}
