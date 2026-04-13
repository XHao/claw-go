package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/XHao/claw-go/knowledge"
	"github.com/XHao/claw-go/memory"
	"github.com/XHao/claw-go/provider"
)

// RecallMemoryDef is the tool schema for recall_memory.
var RecallMemoryDef = provider.ToolDef{
	Name:        "recall_memory",
	Description: "Search past conversation history for turns related to a query. Use when you need to recall what was discussed or solved in previous sessions. Returns up to 5 relevant turns with user message, assistant reply, and tool actions.",
	Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "Natural language description of what you want to recall, e.g. 'docker network configuration' or 'golang goroutine leak'."
    }
  },
  "required": ["query"]
}`),
}

// RegisterRecallMemory registers the recall_memory tool with runner under the "core" group.
func RegisterRecallMemory(runner *LocalRunner, mgr *memory.Manager) {
	runner.RegisterGroup("core", RecallMemoryDef, func(ctx context.Context, argsJSON string, _ RunContext, _ func(string)) (string, error) {
		var p struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
			return "", fmt.Errorf("recall_memory: invalid args: %w", err)
		}
		if strings.TrimSpace(p.Query) == "" {
			return "", fmt.Errorf("recall_memory: query is required")
		}
		keywords := knowledge.ExtractKeywords(p.Query)
		turns, err := mgr.SearchTurns(keywords, 5)
		if err != nil {
			return "", fmt.Errorf("recall_memory: search failed: %w", err)
		}
		return formatRecallResult(p.Query, turns), nil
	})
}

type recallTurn struct {
	N       int             `json:"n"`
	At      string          `json:"at"`
	User    string          `json:"user"`
	Reply   string          `json:"reply,omitempty"`
	Actions []memory.Action `json:"actions,omitempty"`
	Files   []string        `json:"files,omitempty"`
}

type recallResult struct {
	Type  string       `json:"type"`
	Query string       `json:"query"`
	Turns []recallTurn `json:"turns"`
	Note  string       `json:"note,omitempty"`
}

func formatRecallResult(query string, turns []memory.TurnSummary) string {
	result := recallResult{
		Type:  "recall_memory",
		Query: query,
	}
	for _, t := range turns {
		result.Turns = append(result.Turns, recallTurn{
			N:       t.N,
			At:      humanizeAge(t.At),
			User:    t.User,
			Reply:   t.Reply,
			Actions: t.Actions,
			Files:   t.Files,
		})
	}
	if len(result.Turns) == 0 {
		result.Note = "no relevant history found"
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	return string(b)
}

// humanizeAge returns a human-readable age string like "3天前" or "2小时前".
func humanizeAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d小时前", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%d天前", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

// SaveMemoryDef is the tool schema for save_memory.
var SaveMemoryDef = provider.ToolDef{
	Name:        "save_memory",
	Description: "Save a piece of knowledge or user preference to long-term memory. Use when the user says 'remember this', or when you discover reusable knowledge worth keeping. Content is appended to the existing file — it will be merged and deduplicated later by the distiller.",
	Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "topic": {
      "type": "string",
      "description": "Short topic name, e.g. 'golang', 'docker', 'user-preferences'. Used as the filename."
    },
    "content": {
      "type": "string",
      "description": "The knowledge or preference to save, in plain text or Markdown bullet points."
    },
    "type": {
      "type": "string",
      "enum": ["knowledge", "preference", "procedure"],
      "description": "Type of memory: 'knowledge' for domain facts, 'preference' for user preferences, 'procedure' for task workflows and step-by-step processes. Defaults to 'knowledge'."
    }
  },
  "required": ["topic", "content"]
}`),
}

// RegisterSaveMemory registers the save_memory tool with runner under the "core" group.
func RegisterSaveMemory(runner *LocalRunner, store *knowledge.ExperienceStore, procStore *knowledge.ProcedureStore, onProcedureSaved func()) {
	runner.RegisterGroup("core", SaveMemoryDef, func(ctx context.Context, argsJSON string, _ RunContext, _ func(string)) (string, error) {
		var p struct {
			Topic   string `json:"topic"`
			Content string `json:"content"`
			Type    string `json:"type"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
			return "", fmt.Errorf("save_memory: invalid args: %w", err)
		}
		p.Topic = strings.TrimSpace(p.Topic)
		p.Content = strings.TrimSpace(p.Content)
		if p.Topic == "" {
			return "", fmt.Errorf("save_memory: topic is required")
		}
		if p.Content == "" {
			return "", fmt.Errorf("save_memory: content is required")
		}
		if p.Type == "" {
			p.Type = "knowledge"
		}

		// Route procedure type to ProcedureStore.
		if p.Type == "procedure" {
			if procStore == nil {
				return "", fmt.Errorf("save_memory: procedure store not configured")
			}
			if err := procStore.Append(procStore.SafeName(p.Topic), p.Content); err != nil {
				return "", fmt.Errorf("save_memory: procedure save failed: %w", err)
			}
			if onProcedureSaved != nil {
				go onProcedureSaved()
			}
			b, _ := json.Marshal(map[string]string{
				"type":   "save_memory_ok",
				"topic":  p.Topic,
				"action": "procedure_saved",
			})
			return string(b), nil
		}

		if store == nil {
			return "", fmt.Errorf("save_memory: experience store not configured")
		}
		existing, err := store.Load(p.Topic)
		if err != nil {
			return "", fmt.Errorf("save_memory: load existing: %w", err)
		}

		var newContent string
		if existing == "" {
			newContent = fmt.Sprintf("# %s\n\n%s\n", p.Topic, p.Content)
		} else {
			separator := fmt.Sprintf("\n\n---\n*%s 追加*\n\n", time.Now().Format("2006-01-02 15:04"))
			newContent = existing + separator + p.Content + "\n"
		}

		if err := store.Save(p.Topic, newContent); err != nil {
			return "", fmt.Errorf("save_memory: save failed: %w", err)
		}
		action := "appended"
		if existing == "" {
			action = "created"
		}
		b, _ := json.Marshal(map[string]string{
			"type":   "save_memory_ok",
			"topic":  p.Topic,
			"action": action,
		})
		return string(b), nil
	})
}
