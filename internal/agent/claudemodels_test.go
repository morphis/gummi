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
// in the CLI's order, each alias followed by its resolved full id, "default"
// dropped, other frames skipped, an error refused; and the child the probe
// spawns runs under the scrubbed session env, with auth passed through.
func TestClaudeModelCatalog(t *testing.T) {
	t.Run("parse keeps values verbatim minus default, resolved ids after their alias", func(t *testing.T) {
		line := `{"type":"control_response","response":{"subtype":"success","request_id":"gummi-list-models-1","response":{"models":[` +
			`{"value":"default","resolvedModel":"claude-opus-5-5"},` +
			`{"value":"sonnet","resolvedModel":"claude-sonnet-5-5"},` +
			`{"value":"claude-sonnet-5","resolvedModel":"claude-sonnet-5"},` +
			`{"value":"haiku","resolvedModel":"claude-haiku-4-5-20251001"}]}}}`
		ids, done, err := parseClaudeModelList([]byte(line))
		if err != nil || !done {
			t.Fatalf("done=%v err=%v, want the answer", done, err)
		}
		if want := []string{"sonnet", "claude-sonnet-5-5", "claude-sonnet-5", "haiku", "claude-haiku-4-5-20251001"}; !slices.Equal(ids, want) {
			t.Errorf("ids = %v, want %v", ids, want)
		}
	})

	t.Run("the full 2.1.289 answer lists each alias's resolved id after it", func(t *testing.T) {
		line := `{"type":"control_response","response":{"subtype":"success","request_id":"gummi-list-models-1","response":{"models":[` +
			`{"value":"default","resolvedModel":"claude-opus-5-5"},` +
			`{"value":"opus","resolvedModel":"claude-opus-5-5"},` +
			`{"value":"fable","resolvedModel":"claude-fable-5-1"},` +
			`{"value":"sonnet","resolvedModel":"claude-sonnet-5-5"},` +
			`{"value":"haiku","resolvedModel":"claude-haiku-4-5-20251001"},` +
			`{"value":"claude-sonnet-5","resolvedModel":"claude-sonnet-5"},` +
			`{"value":"claude-opus-5","resolvedModel":"claude-opus-5"},` +
			`{"value":"claude-fable-5","resolvedModel":"claude-fable-5"},` +
			`{"value":"claude-opus-4-8","resolvedModel":"claude-opus-4-8"},` +
			`{"value":"claude-opus-4-7","resolvedModel":"claude-opus-4-7"},` +
			`{"value":"claude-opus-4-6","resolvedModel":"claude-opus-4-6"},` +
			`{"value":"claude-sonnet-4-6","resolvedModel":"claude-sonnet-4-6"}]}}}`
		ids, done, err := parseClaudeModelList([]byte(line))
		if err != nil || !done {
			t.Fatalf("done=%v err=%v, want the answer", done, err)
		}
		want := []string{
			"opus", "claude-opus-5-5",
			"fable", "claude-fable-5-1",
			"sonnet", "claude-sonnet-5-5",
			"haiku", "claude-haiku-4-5-20251001",
			"claude-sonnet-5", "claude-opus-5", "claude-fable-5",
			"claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-4-6",
		}
		if !slices.Equal(ids, want) {
			t.Errorf("ids = %v, want %v", ids, want)
		}
		if slices.Contains(ids, "default") {
			t.Errorf("the catalog offers default: %v", ids)
		}
		seen := make(map[string]bool)
		for _, id := range ids {
			if seen[id] {
				t.Errorf("the catalog repeats %q", id)
			}
			seen[id] = true
		}
	})

	t.Run("a resolvedModel of default is never offered", func(t *testing.T) {
		line := `{"type":"control_response","response":{"subtype":"success","request_id":"gummi-list-models-1","response":{"models":[` +
			`{"value":"sonnet","resolvedModel":"default"},{"value":"haiku","resolvedModel":""}]}}}`
		ids, done, err := parseClaudeModelList([]byte(line))
		if err != nil || !done {
			t.Fatalf("done=%v err=%v, want the answer", done, err)
		}
		if want := []string{"sonnet", "haiku"}; !slices.Equal(ids, want) {
			t.Errorf("ids = %v, want %v", ids, want)
		}
		if slices.Contains(ids, "default") {
			t.Errorf("the catalog offers default: %v", ids)
		}
	})

	t.Run("an answer without resolvedModel fields keeps today's list", func(t *testing.T) {
		line := `{"type":"control_response","response":{"subtype":"success","request_id":"gummi-list-models-1","response":{"models":[` +
			`{"value":"default"},{"value":"sonnet"},{"value":"claude-sonnet-5"},{"value":""}]}}}`
		ids, done, err := parseClaudeModelList([]byte(line))
		if err != nil || !done {
			t.Fatalf("done=%v err=%v, want the answer", done, err)
		}
		if want := []string{"sonnet", "claude-sonnet-5"}; !slices.Equal(ids, want) {
			t.Errorf("ids = %v, want %v", ids, want)
		}
	})

	t.Run("a resolvedModel that is not a string is ignored, the value still comes through", func(t *testing.T) {
		line := `{"type":"control_response","response":{"subtype":"success","request_id":"gummi-list-models-1","response":{"models":[` +
			`{"value":"sonnet","resolvedModel":7},` +
			`{"value":"haiku","resolvedModel":true},` +
			`{"value":"opus","resolvedModel":{}},` +
			`{"value":"fable","resolvedModel":[]},` +
			`{"value":"claude-sonnet-5","resolvedModel":null}]}}}`
		ids, done, err := parseClaudeModelList([]byte(line))
		if err != nil || !done {
			t.Fatalf("done=%v err=%v, want the answer", done, err)
		}
		if want := []string{"sonnet", "haiku", "opus", "fable", "claude-sonnet-5"}; !slices.Equal(ids, want) {
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
			`echo '{"type":"control_response","response":{"subtype":"success","request_id":"gummi-list-models-1","response":{"models":[{"value":"default"},{"value":"sonnet","resolvedModel":"claude-sonnet-5-5"}]}}}'` + "\n"
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
		if want := []string{"sonnet", "claude-sonnet-5-5"}; !slices.Equal(ids, want) {
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
