package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestClaudeModelCatalog covers the list_models answer: values verbatim and
// in the CLI's order, "default" dropped, other frames skipped, an error
// refused; and the child the probe spawns runs under the scrubbed session
// env, with auth passed through.
func TestClaudeModelCatalog(t *testing.T) {
	t.Run("parse keeps values verbatim minus default", func(t *testing.T) {
		line := `{"type":"control_response","response":{"subtype":"success","request_id":"gummi-list-models-1","response":{"models":[` +
			`{"value":"default","resolvedModel":"claude-opus-5-5"},` +
			`{"value":"sonnet","resolvedModel":"claude-sonnet-5-5"},` +
			`{"value":"claude-sonnet-5","resolvedModel":"claude-sonnet-5"},` +
			`{"value":"haiku","resolvedModel":"claude-haiku-4-5-20251001"}]}}}`
		ids, done, err := parseClaudeModelList([]byte(line))
		if err != nil || !done {
			t.Fatalf("done=%v err=%v, want the answer", done, err)
		}
		if want := []string{"sonnet", "claude-sonnet-5", "haiku"}; !slices.Equal(ids, want) {
			t.Errorf("ids = %v, want %v", ids, want)
		}
	})

	t.Run("other frames are not the answer", func(t *testing.T) {
		for _, line := range []string{
			`{"type":"system","subtype":"init"}`,
			`{"type":"control_response","response":{"subtype":"success","request_id":"someone-else","response":{"models":[{"value":"x"}]}}}`,
			`not json`,
			``,
		} {
			if _, done, err := parseClaudeModelList([]byte(line)); done || err != nil {
				t.Errorf("%q: done=%v err=%v, want skipped", line, done, err)
			}
		}
	})

	t.Run("an error response is refused", func(t *testing.T) {
		line := `{"type":"control_response","response":{"subtype":"error","request_id":"gummi-list-models-1","error":"unsupported"}}`
		if _, done, err := parseClaudeModelList([]byte(line)); !done || err == nil {
			t.Errorf("done=%v err=%v, want the refusal as an error", done, err)
		}
	})

	t.Run("the probe child runs scrubbed, auth preserved", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, "env.txt")
		bin := filepath.Join(dir, "claude")
		script := "#!/bin/sh\nenv > '" + envFile + "'\nread line\n" +
			`echo '{"type":"control_response","response":{"subtype":"success","request_id":"gummi-list-models-1","response":{"models":[{"value":"default"},{"value":"sonnet"}]}}}'` + "\n"
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CLAUDECODE", "1")
		t.Setenv("AI_AGENT", "1")
		t.Setenv("CLAUDE_CODE_SESSION_ID", "parent-session")
		t.Setenv("ANTHROPIC_API_KEY", "test-key")

		ids, err := claudeModelCatalog(context.Background(), bin)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if want := []string{"sonnet"}; !slices.Equal(ids, want) {
			t.Errorf("ids = %v, want %v", ids, want)
		}
		raw, err := os.ReadFile(envFile)
		if err != nil {
			t.Fatalf("the child wrote no env: %v", err)
		}
		env := string(raw)
		for _, marker := range []string{"CLAUDECODE=", "AI_AGENT=", "CLAUDE_CODE_"} {
			if strings.Contains(env, marker) {
				t.Errorf("the probe child inherited %s from the parent session", marker)
			}
		}
		if !strings.Contains(env, "ANTHROPIC_API_KEY=test-key") {
			t.Errorf("the probe child lost its auth env")
		}
	})
}
