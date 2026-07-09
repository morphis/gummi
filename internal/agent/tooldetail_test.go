package agent

import (
	"strings"
	"testing"
)

func TestToolDetailPicksSalientArg(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"command wins over path", map[string]any{"command": "go test ./...", "path": "x"}, "go test ./..."},
		{"file path", map[string]any{"file_path": "/wt/internal/ui/chat.go"}, "internal/ui/chat.go"},
		{"camel-case path", map[string]any{"filePath": "a.go"}, "a.go"},
		{"pattern", map[string]any{"pattern": "AuthorTool", "include": "*.go"}, "AuthorTool"},
		{"non-string values skipped", map[string]any{"command": 42, "path": "a.go"}, "a.go"},
		{"nothing displayable", map[string]any{"todos": []any{"a"}}, ""},
		{"nil args", nil, ""},
	}
	for _, c := range cases {
		if got := toolDetail("/wt", c.args); got != c.want {
			t.Errorf("%s: toolDetail = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCollapseDetail(t *testing.T) {
	// worktree paths become repo-relative, even inside a command
	if got := collapseDetail("/wt/", "go test /wt/internal/..."); got != "go test internal/..." {
		t.Errorf("workdir trim = %q", got)
	}
	// newlines and runs of spaces collapse to single spaces (the double
	// space is reserved as the name/detail separator downstream)
	if got := collapseDetail("", "a\n\n  b\tc"); got != "a b c" {
		t.Errorf("collapse = %q", got)
	}
	// long details are capped at the source
	got := collapseDetail("", strings.Repeat("x", detailCap+50))
	if want := strings.Repeat("x", detailCap) + "…"; got != want {
		t.Errorf("cap: len = %d, tail = %q", len([]rune(got)), got[len(got)-3:])
	}
}
