package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/workflow"
)

// userOpenThreads returns the open annotation threads that carry a
// human (`@user`) comment — the review feedback that blocks the gate and
// that "request changes" sends to the agent. The template's own `@gummi`
// prompts are scaffolding and are not included.
func userOpenThreads(doc spec.Doc) []spec.Thread {
	var out []spec.Thread
	for _, t := range doc.OpenQuestions() {
		if userMarker(t) != nil {
			out = append(out, t)
		}
	}
	return out
}

// userMarker returns the last unresolved `@user` marker in a thread, or
// nil — the human's actual comment (which may thread under a template
// prompt, so Markers[0] is not it).
func userMarker(t spec.Thread) *spec.Marker {
	var found *spec.Marker
	for i := range t.Markers {
		if t.Markers[i].Author == "user" && !t.Markers[i].Resolved {
			found = &t.Markers[i]
		}
	}
	return found
}

// compileOpenQuestions builds a structured turn from a spec's open
// user annotations for the responsible role (DESIGN §6.1). Returns ""
// when the human has no open comments.
func compileOpenQuestions(doc spec.Doc) string {
	threads := userOpenThreads(doc)
	if len(threads) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Please address these review comments in the spec. ")
	b.WriteString("For each, edit the relevant section and mark it resolved with a line like ")
	b.WriteString("`%% @architect: resolved — <how>`:\n\n")
	for _, t := range threads {
		mk := userMarker(t)
		q := mk.Text
		if q == "" {
			q = "(see the marker)"
		}
		fmt.Fprintf(&b, "- L%d: %s\n", mk.Line, q)
	}
	return b.String()
}

// requestSpecChanges compiles the artifact's open questions into a turn
// and sends it to the responsible agent (DESIGN §6.1). Interactive
// stages get it as a chat turn to the attached architect; autonomous
// stages (plan, implement, …) have no chat, so the comments go to the
// session directly (see sendChangesToAutonomous). Either way the agent
// edits the artifact and resolves each; the user reloads to see the
// open-count burn down.
func (m *Shell) requestSpecChanges(sv *specView) tea.Cmd {
	if m.engine == nil {
		m.notice = noticeMsg{text: "no agent configured", isErr: true}
		return nil
	}
	turn := compileOpenQuestions(sv.doc)
	if turn == "" {
		m.notice = noticeMsg{text: "no open review comments to send"}
		return nil
	}
	f := sv.f
	n := len(userOpenThreads(sv.doc))
	if !workflow.Interactive(f.Stage) {
		return m.sendChangesToAutonomous(f, turn, n)
	}
	return func() tea.Msg {
		ctx := context.Background()
		if _, err := m.engine.Attach(ctx, f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		if err := m.engine.Send(ctx, f.ID, turn); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: string(f.ID) + ": sent " + strconv.Itoa(n) + " review comment(s) to the architect"}
	}
}

// sendChangesToAutonomous delivers review comments to an autonomous
// stage: a running session gets them as a live turn (in-context, no
// restart); a finished or paused one is re-run with them appended to
// its kickoff, so the stage re-gates when it completes. A queued run
// hasn't started yet and reads the artifact — with the comments already
// in it — when it does.
func (m *Shell) sendChangesToAutonomous(f domain.Feature, turn string, n int) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if s := m.engine.Get(f.ID); s != nil {
			switch s.State() {
			case engine.StateRunning:
				if err := m.engine.Send(ctx, f.ID, turn); err != nil {
					return noticeMsg{text: sanitize(err.Error()), isErr: true}
				}
				return noticeMsg{text: fmt.Sprintf("%s: sent %d review comment(s) to the running %s agent", f.ID, n, f.Stage)}
			case engine.StateQueued:
				return noticeMsg{text: fmt.Sprintf("%s: %s is queued — it will read the open comments when it starts", f.ID, f.Stage)}
			}
		}
		if err := m.engine.RunWith(f, turn); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s: re-running %s with %d review comment(s)", f.ID, f.Stage, n)}
	}
}

// openQuestionsBlockingGate returns the number of open, USER-authored
// `%%` annotations in an item's artifact (a feature's spec or a bug's
// report). These block every stage gate (DESIGN §6.1: unresolved
// annotations block the gate). The template's own `@gummi` prompts and
// unattributed notes do not block — only the human's review comments do.
// It reads the worktree copy once one exists (the artifact migrates
// there at approval), the draft before then; zero for an unreadable
// artifact.
func (m *Shell) openQuestionsBlockingGate(ctx context.Context, f domain.Feature) int {
	path := filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(&f))
	if ok, err := m.wt.Exists(ctx, &f); err == nil && ok {
		path = filepath.Join(m.wt.Root(), f.WorktreePath(), f.ArtifactPath())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return len(userOpenThreads(spec.Parse(string(raw))))
}
