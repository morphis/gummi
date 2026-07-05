package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/spec"
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

// requestSpecChanges compiles the spec's open questions into a turn and
// sends it to the feature's architect session (DESIGN §6.1). The agent
// edits the spec and resolves each; the user reloads to see the
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

// openQuestionsBlockingGate returns the number of open, USER-authored
// `%%` annotations in an item's draft artifact (a feature's spec or a
// bug's report). These block approval (DESIGN §6.1: unresolved
// annotations block the gate). The template's own `@gummi` prompts and
// unattributed notes do not block — only the human's review comments do.
// Called at the approval moment (before a worktree exists), so it reads
// the draft; zero for an unreadable draft.
func (m *Shell) openQuestionsBlockingGate(_ context.Context, f domain.Feature) int {
	path := filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(&f))
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return len(userOpenThreads(spec.Parse(string(raw))))
}
