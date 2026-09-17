package ui

import (
	"context"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/spec"
)

// userOpenThreads returns the open annotation threads that carry a
// human (`@user`) comment. It delegates to spec.UserOpenThreads so the
// gate-blocking definition stays identical across the UI and the engine.
func userOpenThreads(doc spec.Doc) []spec.Thread { return doc.UserOpenThreads() }

// userMarker returns the last unresolved `@user` marker in a thread, or
// nil — the human's actual comment (which may thread under a template
// prompt, so Markers[0] is not it).
func userMarker(t spec.Thread) *spec.Marker { return spec.UnresolvedUserMarker(t) }

// compileOpenQuestions builds a structured turn from a spec's open
// user annotations for the responsible role (DESIGN §6.1). Returns ""
// when the human has no open comments. The engine owns the wording, so a
// live turn and a writer's kickoff say the same thing.
func compileOpenQuestions(doc spec.Doc) string { return engine.CompileSpecComments(doc) }

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
	// Every stage is autonomous now; a chat is a session the user opened
	// against one, not a stage state. Changes go to the running session.
	return m.sendChangesToAutonomous(f, turn, n)
}

// sendChangesToAutonomous delivers review comments to an autonomous
// stage: a running stage writer gets them as a live turn (in-context, no
// restart); a critique or rebase pass does not take them at all — they
// stay open in the spec, holding the gate, and the writer's next run
// reads them in its kickoff (see heldForWriter); a finished or paused
// one is re-run with them appended to its kickoff, so the stage re-gates when it completes. A queued run
// hasn't started yet and reads the artifact — with the comments already
// in it — when it does.
func (m *Shell) sendChangesToAutonomous(f domain.Feature, turn string, n int) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if s := m.engine.Get(f.ID); s != nil {
			switch s.State() {
			case engine.StateRunning, engine.StateInteractive:
				if held := heldForWriter(s.Snapshot(), f, n, "comment"); held != "" {
					return noticeMsg{text: held}
				}
				// a session the user attached takes the turn in-context too:
				// Send accepts both states, and re-running a stage the user
				// is sitting in front of would throw its context away.
				if err := m.engine.Send(ctx, f.ID, turn); err != nil {
					return noticeMsg{text: sanitize(err.Error()), isErr: true}
				}
				return noticeMsg{text: fmt.Sprintf("%s: sent %d review comment(s) to the running %s agent", f.ID, n, f.Stage), reload: true}
			case engine.StateQueued:
				return noticeMsg{text: fmt.Sprintf("%s: %s is queued — it will read the open comments when it starts", f.ID, f.Stage)}
			}
		}
		if err := m.engine.RunWith(f, turn); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s: re-running %s with %d review comment(s)", f.ID, f.Stage, n), reload: true}
	}
}

// heldForWriter returns the notice for comments that must not go to the
// running session because it is not the stage's writer — a critique
// judging what the writer produced, or a rebase resolving conflicts — or
// "" when it is the writer and the comments can go to it live. A comment
// delivered to a critique mid-pass is acted on in the wrong phase: the
// reviewer reads an instruction meant for the architect and judges a
// plan nobody has revised yet.
func heldForWriter(snap engine.Snapshot, f domain.Feature, n int, what string) string {
	var pass string
	switch {
	case snap.Critique:
		pass = "the " + string(f.Stage) + " critique"
	case snap.Rebase:
		pass = "a rebase"
	default:
		return ""
	}
	return fmt.Sprintf("%s: %s is running — %d %s%s held for the next %s run", f.ID, pass, n, what, plural(n), f.Stage)
}

// openQuestionsBlockingGate returns the number of open, USER-authored
// `%%` annotations in an item's artifact (a feature's spec or a bug's
// report). These block every stage gate (DESIGN §6.1: unresolved
// annotations block the gate). The template's own `@gummi` prompts and
// unattributed notes do not block — only the human's review comments do.
// It reads wherever the artifact lives right now (workspace home, draft,
// or legacy worktree copy); zero for a missing or unreadable artifact.
func (m *Shell) openQuestionsBlockingGate(f domain.Feature) int {
	path := m.artifactFile(&f)
	if path == "" {
		return 0
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return len(userOpenThreads(spec.Parse(string(raw))))
}

// openDiffCommentsBlockingGate returns the number of unresolved diff
// annotations on an item — the diff-backend half of DESIGN §6.1's
// "unresolved annotations block the gate" (openQuestionsBlockingGate is
// the artifact half). Zero on any store error: like an unreadable
// artifact, a failed read never wedges the gate shut.
func (m *Shell) openDiffCommentsBlockingGate(ctx context.Context, id domain.FeatureID) int {
	anns, err := m.store.ListDiffAnnotations(ctx, id)
	if err != nil {
		return 0
	}
	n := 0
	for _, a := range anns {
		if !a.Resolved {
			n++
		}
	}
	return n
}
