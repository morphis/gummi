//go:build zz_integration

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestZZIntegration drives one real zz turn against a stub OpenAI-compatible
// endpoint. This card does not add a --provider flag (see Chosen approach),
// so the stub is wired in via zz's own default_provider config under an
// isolated $HOME rather than adapter argv. It skips when zz is absent from
// PATH. Only compiled with -tags zz_integration so the default suite never
// spawns a real agent or reaches a model provider.
func TestZZIntegration(t *testing.T) {
	if _, err := exec.LookPath("zz"); err != nil {
		t.Skip("zz not installed")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "PONG"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12},
		})
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".config", "zz")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("default_provider = \"stub\"\n\n[providers.stub]\nbase_url = %q\napi_key = \"test\"\n", srv.URL+"/v1")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	z, err := NewZZ("zz")
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()

	sess, err := z.NewSession(context.Background(), SessionOpts{
		WorkDir: t.TempDir(), Model: "stub-model", Permission: PermissionAllowAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.Send(context.Background(), "Reply with exactly PONG"); err != nil {
		t.Fatal(err)
	}

	var sawText, sawUsage, sawIdle bool
	deadline := time.After(60 * time.Second)
	for !sawIdle {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("events channel closed before idle")
			}
			switch ev.Kind {
			case EventTextDelta:
				sawText = true
			case EventUsage:
				sawUsage = true
			case EventIdle:
				sawIdle = true
			case EventError:
				t.Fatalf("zz turn failed: %v", ev.Err)
			}
		case <-deadline:
			t.Fatal("timed out waiting for zz turn to finish")
		}
	}
	if !sawText {
		t.Error("no text delta observed")
	}
	if !sawUsage {
		t.Error("no usage event observed")
	}
}
