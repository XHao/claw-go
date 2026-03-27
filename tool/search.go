package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/XHao/claw-go/config"
	"github.com/XHao/claw-go/provider"
)

const tavilyEndpoint = "https://api.tavily.com/search"

// RegisterWebSearch registers the web_search tool with runner using Tavily.
// Does nothing when cfg.TavilyAPIKey is empty.
func RegisterWebSearch(runner *LocalRunner, cfg config.SearchConfig) {
	if strings.TrimSpace(cfg.TavilyAPIKey) == "" {
		return
	}

	maxResults := cfg.MaxResults
	if maxResults <= 0 {
		maxResults = 5
	}
	timeoutSec := cfg.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 15
	}

	client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}

	def := provider.ToolDef{
		Name: "web_search",
		Description: "Search the web for up-to-date information using Tavily. " +
			"Call when user asks about current events, recent news, live data, " +
			"or any topic where real-time information is needed. " +
			"Returns a ranked list of relevant web pages with titles, URLs, and snippets.",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "The search query. Use concise, specific terms for best results."
    }
  },
  "required": ["query"]
}`),
	}

	runner.Register(def, func(ctx context.Context, argsJSON string, _ RunContext, progress func(string)) (string, error) {
		var p struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
			return "", fmt.Errorf("web_search: invalid args: %w", err)
		}
		query := strings.TrimSpace(p.Query)
		if query == "" {
			return "", fmt.Errorf("web_search: query is required")
		}

		if progress != nil {
			progress(fmt.Sprintf("正在搜索 %q …", query))
		}

		results, err := tavilySearch(ctx, client, cfg.TavilyAPIKey, query, maxResults)
		if err != nil {
			return "", fmt.Errorf("web_search: %w", err)
		}
		if len(results) == 0 {
			return fmt.Sprintf("未找到 %q 的相关网页。", query), nil
		}
		return formatSearchResults(query, results), nil
	})
}

// tavilyResult mirrors the fields we use from Tavily's response.
type tavilyResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

func tavilySearch(ctx context.Context, client *http.Client, apiKey, query string, maxResults int) ([]tavilyResult, error) {
	body, err := json.Marshal(map[string]any{
		"api_key":        apiKey,
		"query":          query,
		"max_results":    maxResults,
		"include_answer": false,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilyEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return nil, fmt.Errorf("Tavily HTTP %d: %s", resp.StatusCode, msg)
	}

	var payload struct {
		Results []tavilyResult `json:"results"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	return payload.Results, nil
}

func formatSearchResults(query string, results []tavilyResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "搜索 %q 返回 %d 条结果：\n\n", query, len(results))
	for i, r := range results {
		snippet := strings.TrimSpace(r.Content)
		if len(snippet) > 400 {
			snippet = snippet[:400] + "…"
		}
		fmt.Fprintf(&sb, "**%d. %s**\n%s\n%s\n\n", i+1, r.Title, r.URL, snippet)
	}
	return strings.TrimRight(sb.String(), "\n")
}
