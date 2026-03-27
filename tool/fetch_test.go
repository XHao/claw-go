package tool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchURLExtractsTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
<nav><a href="/">Home</a><a href="/about">About</a></nav>
<article>
<h1>Hello World</h1>
<p>This is the main content paragraph.</p>
<p>Second paragraph with more details.</p>
</article>
<footer>Copyright 2026</footer>
<script>alert('should be removed')</script>
</body>
</html>`))
	}))
	defer srv.Close()

	out, err := runFetchURL(context.Background(), srv.URL, fetchDefaultMaxBytes, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output not valid JSON: %v\nraw: %s", err, out)
	}
	if result["type"] != "webpage" {
		t.Errorf("expected type=webpage, got %v", result["type"])
	}
	if result["title"] != "Test Page" {
		t.Errorf("expected title='Test Page', got %q", result["title"])
	}
	content, _ := result["content"].(string)
	if !strings.Contains(content, "Hello World") {
		t.Errorf("expected content to contain 'Hello World', got: %s", content)
	}
	if !strings.Contains(content, "main content paragraph") {
		t.Errorf("expected content to contain 'main content paragraph', got: %s", content)
	}
	// nav and footer should be stripped
	if strings.Contains(content, "Copyright 2026") {
		t.Errorf("footer text should be removed, got: %s", content)
	}
	if strings.Contains(content, "alert(") {
		t.Errorf("script content should be removed, got: %s", content)
	}
}

func TestFetchURLTruncatesLargeContent(t *testing.T) {
	bigText := strings.Repeat("word ", 10000) // ~50 KB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p>` + bigText + `</p></body></html>`))
	}))
	defer srv.Close()

	maxBytes := 1024
	out, err := runFetchURL(context.Background(), srv.URL, maxBytes, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if result["truncated"] != true {
		t.Errorf("expected truncated=true for large content")
	}
	content, _ := result["content"].(string)
	if len(content) > maxBytes+10 { // small tolerance for rune boundary
		t.Errorf("content exceeds maxBytes: got %d bytes", len(content))
	}
}

func TestFetchURLRejectsNonHTTP(t *testing.T) {
	_, err := runFetchURL(context.Background(), "ftp://example.com/file", fetchDefaultMaxBytes, nil)
	if err == nil {
		t.Error("expected error for non-http URL")
	}
}

func TestFetchURLRejects4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := runFetchURL(context.Background(), srv.URL, fetchDefaultMaxBytes, nil)
	if err == nil {
		t.Error("expected error for 404 response")
	}
}

func TestExtractHTMLContentSkipsScriptAndStyle(t *testing.T) {
	raw := []byte(`<html><head>
<title>My Title</title>
<style>body{color:red}</style>
</head><body>
<script>var x=1;</script>
<p>Visible text here.</p>
</body></html>`)

	title, text := extractHTMLContent(raw)
	if title != "My Title" {
		t.Errorf("expected title 'My Title', got %q", title)
	}
	if strings.Contains(text, "color:red") {
		t.Errorf("style content should be stripped")
	}
	if strings.Contains(text, "var x=1") {
		t.Errorf("script content should be stripped")
	}
	if !strings.Contains(text, "Visible text here") {
		t.Errorf("expected visible text, got: %s", text)
	}
}

func TestTruncateUTF8(t *testing.T) {
	// "你好世界" = 4 runes × 3 bytes each = 12 bytes
	s := "你好世界"
	got := truncateUTF8(s, 6) // exactly 2 runes
	if got != "你好" {
		t.Errorf("expected '你好', got %q", got)
	}
	// truncating in the middle of a multi-byte rune should back off to the previous boundary
	got = truncateUTF8(s, 5)
	if got != "你" {
		t.Errorf("expected '你' when cutting mid-rune at byte 5, got %q", got)
	}
}
