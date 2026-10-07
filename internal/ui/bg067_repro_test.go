package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/verdict"
	"github.com/morphis/gummi/internal/webapi"
)

// BG-067: a verify pass the promises floor overruled reads as a failed
// verify, and correcting the promise it cited does not clear the
// overrule — only a fresh verify run does.
//
// The drive is the reported one: the verifier answers VERDICT: pass, the
// plan pins a golden nothing on the branch contains, and the floor
// stamps the verdict blocked. The card is then headed "verify failed"
// with the floor's reason nowhere on it, and striking the golden from
// the plan leaves the verdict reading blocked.

// bg067Artifact writes the card's artifact at its workspace home, with
// (golden != "") or without (golden == "") the plan claim the floor
// reads.
func bg067Artifact(t *testing.T, root string, f domain.Feature, golden string) string {
	t.Helper()
	p := filepath.Join(root, f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	body := "# " + string(f.ID) + "\n\n## Plan claims\n\n"
	if golden != "" {
		body += "- golden `" + golden + "` because the stack is at its root.\n"
	}
	body += "## Verification plan\n\nThe suite ran clean on the branch.\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// bg067FlooredVerify drives a verify stage whose agent answers pass
// while the plan pins a golden the branch does not contain, and returns
// once the stop is on the card.
func bg067FlooredVerify(t *testing.T) (b *Bridge, eng *engine.Engine, f domain.Feature, root, artifact string) {
	t.Helper()
	ag := verdictAgent(func(opts agent.SessionOpts) string {
		if isVerify(opts) {
			return "All checks green; the verification plan ran clean.\nVERDICT: pass"
		}
		return "done"
	})
	f = domain.Feature{ID: "FD-001", Num: 1, Title: "Dark mode", Slug: "dark-mode", Stage: domain.StageVerify}
	b, _, eng, f, _ = headlessBoardFor(t, ag, f)
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	withWorktree(t, b, f)
	if err := b.Do(context.Background(), func(m *Shell) tea.Cmd { root = m.ws.Root; return nil }); err != nil {
		t.Fatal(err)
	}
	artifact = bg067Artifact(t, root, f, `TestParse_Error["name eq c9)"] = "unbalanced parentheses"`)
	if err := b.Do(context.Background(), func(m *Shell) tea.Cmd { return m.runStage(f) }); err != nil {
		t.Fatal(err)
	}
	// The stop can be on the card a beat before the floor is stamped on the
	// session, so wait for the floor's own reason to be on it.
	waitCard(t, b, string(f.ID), "verify stop", func(c webapi.Card) bool {
		if c.Decision == nil || c.Decision.Kind != webapi.DecisionVerify {
			return false
		}
		surface := c.Decision.Question
		for _, o := range c.Decision.Options {
			surface += " " + o.Label + " " + o.Detail
		}
		return strings.Contains(surface, "promises are not met")
	})
	return b, eng, f, root, artifact
}

// A pass the floor overruled is not a failed verify: the card is headed
// with the overrule and carries the floor's reason, on the page and in
// the needs-attention row alike.
func TestBG067AFlooredVerifyPassIsNotReportedAsFailed(t *testing.T) {
	b, _, f, _, _ := bg067FlooredVerify(t)
	c := waitCard(t, b, string(f.ID), "verify stop", func(c webapi.Card) bool {
		return c.Decision != nil && c.Decision.Kind == webapi.DecisionVerify
	})
	if c.Decision.Word == "verify failed" {
		t.Errorf("a pass the floor overruled is headed %q (%s); verify ran and passed — the overrule is what the head must say", c.Decision.Word, c.Decision.Tone)
	}
	var surface strings.Builder
	surface.WriteString(c.Decision.Question)
	for _, o := range c.Decision.Options {
		surface.WriteString(" " + o.Label + " " + o.Detail)
	}
	if !strings.Contains(surface.String(), "promises are not met") {
		t.Errorf("the floor's reason is nowhere on the decision:\n%s", surface.String())
	}
	bd := waitBoard(t, b, func(bd webapi.Board) bool {
		return len(bd.Rows) == 1 && bd.Rows[0].Needs != nil
	})
	if bd.Rows[0].Needs.Word == "verify failed" {
		t.Errorf("the rail reads the overruled pass as %q", bd.Rows[0].Needs.Word)
	}
	var itemText string
	if err := b.Do(context.Background(), func(m *Shell) tea.Cmd {
		for _, it := range m.inbox.list() {
			if it.Feature == f.ID {
				itemText = it.Text
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(itemText, "environment can't run") {
		t.Errorf("the stop misreads the overruled pass as an environment block: %q", itemText)
	}
	if !strings.Contains(itemText, "promises are not met") {
		t.Errorf("the stop does not name the floor's reason: %q", itemText)
	}
}

// Striking the promise the floor cited clears the overrule at the next
// read — the verdict the card carries is the agent's own pass again, and
// no new verify session is needed.
func TestBG067CorrectingTheCitedArtifactClearsTheOverrule(t *testing.T) {
	_, eng, f, root, _ := bg067FlooredVerify(t)
	snap := eng.Get(f.ID).Snapshot()
	if v := verdict.SessionVerdict(snap); v != verdict.Blocked {
		t.Fatalf("precondition: the floored pass reads %v, want blocked", v)
	}
	bg067Artifact(t, root, f, "")
	if v := verdict.SessionVerdict(eng.Get(f.ID).Snapshot()); v != verdict.Pass {
		t.Errorf("after the golden was struck the verdict still reads %v — the stale floor survives the artifact it cited, and only a fresh verify run clears it", v)
	}
}
