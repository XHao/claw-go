package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/XHao/claw-go/provider"
)

const (
	fetchDefaultMaxBytes    = 40 * 1024 // 40 KB of extracted text
	fetchDefaultTimeoutSecs = 15
	fetchMaxResponseBytes   = 5 * 1024 * 1024 // 5 MB raw HTML cap
)

// FetchURLDef is the tool schema for fetch_url.
var FetchURLDef = provider.ToolDef{
	Name:        "fetch_url",
	Description: "Fetch a web page and return its main text content. HTML is parsed and stripped; navigation, ads, scripts and styles are removed. Use web_search first to find relevant URLs, then fetch_url to read the full content of specific pages.",
	Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": {
      "type": "string",
      "description": "The URL to fetch."
    },
    "max_bytes": {
      "type": "integer",
      "description": "Maximum bytes of extracted text to return. Defaults to 40960 (40 KB)."
    }
  },
  "required": ["url"]
}`),
}

// RegisterFetchURL registers the fetch_url tool with runner under the "core" group.
func RegisterFetchURL(runner *LocalRunner) {
	runner.RegisterGroup("core", FetchURLDef, func(ctx context.Context, argsJSON string, _ RunContext, progress func(string)) (string, error) {
		var p struct {
			URL      string `json:"url"`
			MaxBytes int    `json:"max_bytes"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
			return "", fmt.Errorf("fetch_url: invalid args: %w", err)
		}
		if strings.TrimSpace(p.URL) == "" {
			return "", fmt.Errorf("fetch_url: url is required")
		}
		if p.MaxBytes <= 0 {
			p.MaxBytes = fetchDefaultMaxBytes
		}
		return runFetchURL(ctx, p.URL, p.MaxBytes, progress)
	})
}

func runFetchURL(ctx context.Context, rawURL string, maxBytes int, progress func(string)) (string, error) {
	// Validate URL.
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("fetch_url: invalid URL %q (must be http or https)", rawURL)
	}

	if progress != nil {
		progress(fmt.Sprintf("Fetching %s …", rawURL))
	}

	fetchCtx, cancel := context.WithTimeout(ctx, fetchDefaultTimeoutSecs*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("fetch_url: build request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; claw-bot/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch_url: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("fetch_url: HTTP %d for %s", resp.StatusCode, rawURL)
	}

	limited := io.LimitReader(resp.Body, fetchMaxResponseBytes)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("fetch_url: read body: %w", err)
	}

	title, text := extractHTMLContent(raw)

	truncated := false
	if utf8.RuneCountInString(text) > 0 && len(text) > maxBytes {
		// Truncate on a UTF-8 boundary.
		text = truncateUTF8(text, maxBytes)
		truncated = true
	}
	if text == "" {
		text = "(no extractable text content)"
	}

	result := map[string]any{
		"type":        "webpage",
		"url":         rawURL,
		"title":       title,
		"content":     text,
		"truncated":   truncated,
		"byte_length": len(text),
	}
	if truncated {
		result["note"] = fmt.Sprintf("Content truncated to %d bytes. Use search_file on a saved copy for targeted lookups.", maxBytes)
	}

	out, _ := json.Marshal(result)
	return string(out), nil
}

// extractHTMLContent parses raw HTML and returns (title, bodyText).
// It skips script, style, nav, header, footer, aside, and form elements to
// focus on main readable content.
func extractHTMLContent(raw []byte) (title, text string) {
	doc, err := html.Parse(strings.NewReader(string(raw)))
	if err != nil {
		// Fallback: strip tags naively.
		return "", stripTagsNaive(string(raw))
	}

	var sb strings.Builder
	var walk func(*html.Node)

	// Tags whose subtrees are entirely skipped.
	skipTags := map[string]bool{
		"script": true, "style": true, "noscript": true,
		"nav": true, "header": true, "footer": true,
		"aside": true, "form": true, "button": true,
		"svg": true, "canvas": true, "iframe": true,
	}
	// Tags that produce a line break.
	blockTags := map[string]bool{
		"p": true, "div": true, "section": true, "article": true,
		"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
		"li": true, "dt": true, "dd": true, "blockquote": true,
		"tr": true, "td": true, "th": true,
		"br": true, "hr": true,
		"pre": true, "code": true,
	}

	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			if tag == "title" {
				// Collect title text.
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.TextNode {
						title += strings.TrimSpace(c.Data)
					}
				}
				return
			}
			if skipTags[tag] {
				return
			}
			if blockTags[tag] {
				// Ensure block elements start on a new line.
				s := sb.String()
				if len(s) > 0 && s[len(s)-1] != '\n' {
					sb.WriteByte('\n')
				}
			}
		}
		if n.Type == html.TextNode {
			t := strings.TrimSpace(n.Data)
			if t != "" {
				sb.WriteString(t)
				sb.WriteByte(' ')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && blockTags[strings.ToLower(n.Data)] {
			s := sb.String()
			if len(s) > 0 && s[len(s)-1] != '\n' {
				sb.WriteByte('\n')
			}
		}
	}
	walk(doc)

	// Collapse excessive blank lines.
	text = collapseBlankLines(sb.String())
	return strings.TrimSpace(title), strings.TrimSpace(text)
}

// collapseBlankLines replaces 3+ consecutive newlines with 2.
func collapseBlankLines(s string) string {
	var sb strings.Builder
	blank := 0
	for _, r := range s {
		if r == '\n' {
			blank++
			if blank <= 2 {
				sb.WriteRune(r)
			}
		} else {
			blank = 0
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// stripTagsNaive removes all HTML tags, used as fallback when parsing fails.
func stripTagsNaive(s string) string {
	var sb strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			sb.WriteRune(r)
		}
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, sb.String())
}

// truncateUTF8 truncates s to at most maxBytes bytes on a valid UTF-8 boundary.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 {
		if utf8.RuneStart(s[maxBytes]) {
			return s[:maxBytes]
		}
		maxBytes--
	}
	return ""
}
