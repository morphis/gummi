package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/morphis/gummi/internal/childproc"
)

// claudeModelsTimeout bounds one catalog probe: the transient child a probe
// spawns must answer the control request, and a hung child must not hang a
// picker open behind it.
const claudeModelsTimeout = 30 * time.Second

// claudeListModelsID tags the probe's control request, so its answer is
// told apart from any other frame the child might print first.
const claudeListModelsID = "gummi-list-models-1"

// ClaudeModelCatalog returns the model ids the claude CLI itself offers, as
// its own list_models control request reports them, minus the "default"
// entry (the picker already offers the default as its no-model row). Each
// value is forwarded verbatim — aliases like "sonnet" and versioned ids like
// "claude-sonnet-5". Each alias is followed by the full id it currently
// resolves to (its resolvedModel, e.g. "claude-sonnet-5-5"), so the newest
// model of a family is offered under an id the operator recognises.
//
// It needs only the binary on PATH, no adapter and no session: the probe
// spawns a transient stream-json child, asks it the control request, and
// kills the child as soon as it answers. The CLI answers the request locally,
// without a user turn, so the probe spends no turn. Callers that repeat the
// ask are expected to cache (engine.SessionModelCatalog does).
//
// It is a variable, the seam OpencodeModelCatalog is: the engine's no-adapter
// probe path rebinds it in tests instead of spawning anything.
var ClaudeModelCatalog = claudeModelCatalog

func claudeModelCatalog(ctx context.Context, bin string) ([]string, error) {
	if bin == "" {
		bin = "claude"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("claude binary %q not found: %w", bin, err)
	}
	ctx, cancel := context.WithTimeout(ctx, claudeModelsTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "gummi-claude-probe-*")
	if err != nil {
		return nil, fmt.Errorf("claude catalog: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	cmd := exec.CommandContext(ctx, resolved, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose")
	cmd.Dir = dir
	// The probe is the operator's own catalog, not one session's: scrub the
	// parent Claude Code session markers so the child is top-level, the same
	// hygiene every claude child gets (auth is preserved).
	cmd.Env = scrubClaudeSessionEnv(os.Environ())
	childproc.Group(cmd)
	cmd.WaitDelay = 3 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("claude catalog: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude catalog: %w", err)
	}
	if err := childproc.Start(cmd); err != nil {
		return nil, fmt.Errorf("claude catalog: starting claude: %w", err)
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()

	req, err := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": claudeListModelsID,
		"request":    map[string]string{"subtype": "list_models"},
	})
	if err != nil {
		return nil, fmt.Errorf("claude catalog: %w", err)
	}
	if _, err := stdin.Write(append(req, '\n')); err != nil {
		return nil, fmt.Errorf("claude catalog: asking claude: %w", err)
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		ids, done, err := parseClaudeModelList(sc.Bytes())
		if err != nil {
			return nil, fmt.Errorf("claude catalog: %w", err)
		}
		if !done {
			continue
		}
		if len(ids) == 0 {
			return nil, errors.New("claude catalog: the CLI answered with no models")
		}
		return ids, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("claude catalog: %w", err)
	}
	return nil, errors.New("claude catalog: the CLI closed its output without answering")
}

// parseClaudeModelList reads one stdout frame of the stream-json child. It
// reports done only for the control_response carrying claudeListModelsID. The
// ids are the list_models values in the order the CLI gave them, minus
// "default" and blanks; each value that resolves to a different full id
// (resolvedModel) is followed by that id, unless it was already listed. The
// result never repeats an id. A resolvedModel that is not a JSON string is
// ignored, so the entry's value still comes through. Any other frame is not
// an answer and is skipped (done false, nil error). An error response is
// returned as an error.
func parseClaudeModelList(line []byte) (ids []string, done bool, err error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, false, nil
	}
	var frame struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Error     string `json:"error"`
			Response  struct {
				Models []struct {
					Value string `json:"value"`
					// ResolvedModel stays raw: a wrong-typed field must not
					// reject the whole answer, only the id it would have named.
					ResolvedModel json.RawMessage `json:"resolvedModel"`
				} `json:"models"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(line, &frame); err != nil {
		return nil, false, nil
	}
	if frame.Type != "control_response" || frame.Response.RequestID != claudeListModelsID {
		return nil, false, nil
	}
	if frame.Response.Subtype != "success" {
		return nil, true, fmt.Errorf("the CLI refused list_models: %s", frame.Response.Error)
	}
	seen := make(map[string]bool)
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, m := range frame.Response.Response.Models {
		if m.Value == "" || m.Value == "default" {
			continue
		}
		add(m.Value)
		var resolved string
		// A null or non-string resolvedModel decodes to "" and is skipped.
		if json.Unmarshal(m.ResolvedModel, &resolved) != nil {
			continue
		}
		if resolved != m.Value && resolved != "default" {
			add(resolved)
		}
	}
	return ids, true, nil
}
