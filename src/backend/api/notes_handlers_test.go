package api

import (
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	html := renderMarkdown("# Title\n\nSome **bold** text and a [link](https://example.com).\n\n- a\n- b\n")
	for _, want := range []string{"<h1", "Title", "<strong>bold</strong>", `href="https://example.com"`, "<li>a</li>"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected %q in output: %s", want, html)
		}
	}
}

func TestRenderMarkdownSanitizes(t *testing.T) {
	html := renderMarkdown("[click](javascript:alert(1))\n\n<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>")
	if strings.Contains(strings.ToLower(html), "javascript:") {
		t.Fatalf("javascript: URI must be stripped: %s", html)
	}
	if strings.Contains(strings.ToLower(html), "<script") {
		t.Fatalf("<script> must be stripped: %s", html)
	}
	if strings.Contains(strings.ToLower(html), "onerror") {
		t.Fatalf("event handler must be stripped: %s", html)
	}
}

func TestRenderMarkdownBlank(t *testing.T) {
	if got := renderMarkdown("   \n  "); got != "" {
		t.Fatalf("blank input should render empty, got %q", got)
	}
}
