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

// compileOpenQuestions builds a structured turn from a spec's open user
// annotations for the writer of f's current stage (DESIGN §6.1): the ones
// that stage answers (engine.SpecCommentsAnswered). Returns "" when none
// are open for it. The engine owns the wording, so a live turn and a
// writer's kickoff say the same thing.
func compileOpenQuestions(f domain.Feature, doc spec.Doc) string {
	return engine.CompileSpecComments(f, doc)
}

// requestSpecChanges sends the artifact's open comments where they belong
// (DESIGN §6.1): to the running stage's writer when they are its to
// answer, or — asked first — back to the earlier stage that owns them
// (engine.RouteComments). The agent edits the artifact and resolves each;
// the user reloads to see the open-count burn down.
func (m *Shell) requestSpecChanges(sv *specView) tea.Cmd {
	cmd, refused, ask := m.specChanges(sv.f, sv.doc)
	if ask != nil {
		return m.confirmChanges(ask, nil)
	}
	if cmd == nil {
		m.notice = refused
	}
	return cmd
}

// changesAsk is a "request changes" that sends the card back to an
// earlier stage, and so asks before it does: every face shows question
// and runs do on a yes.
type changesAsk struct {
	card     domain.FeatureID
	question string
	do       func() tea.Cmd
}

// confirmChanges raises ask in the terminal. then, when set, runs on the
// yes before the send-back does (the diff surface closes itself).
func (m *Shell) confirmChanges(ask *changesAsk, then func()) tea.Cmd {
	m.Overlay.Push(&confirmDialog{
		id:           "confirm-request-changes",
		card:         ask.card,
		question:     ask.question,
		confirmLabel: "Send back",
		onConfirm: func() tea.Cmd {
			if then != nil {
				then()
			}
			return ask.do()
		},
	})
	return nil
}

// specChanges is requestSpecChanges for any face: the command that sends
// doc's open comments on; or the question to ask before a send-back that
// moves the card; or — both nil — the notice saying why nothing can be
// sent.
func (m *Shell) specChanges(f domain.Feature, doc spec.Doc) (tea.Cmd, noticeMsg, *changesAsk) {
	if m.engine == nil {
		return nil, noticeMsg{text: m.noAgent(""), isErr: true}, nil
	}
	if len(userOpenThreads(doc)) == 0 {
		return nil, noticeMsg{text: "no open review comments to send"}, nil
	}
	route := engine.RouteComments(domain.CardTypeOf(&f), f.Stage, doc, m.openDiffComments(f.ID))
	if route.Rewinds() {
		return nil, noticeMsg{}, m.commentRewind(f, route)
	}
	turn := compileOpenQuestions(f, doc)
	if turn == "" {
		// every open comment is on a section a later stage writes: it
		// is that stage's, and its writer's kickoff carries it
		return nil, noticeMsg{text: fmt.Sprintf("%s: the open comments are on sections a later stage writes — they go to it when it runs", f.ID)}, nil
	}
	n := len(engine.SpecCommentsAnswered(domain.CardTypeOf(&f), f.Stage, doc))
	// Every stage is autonomous now; a chat is a session the user opened
	// against one, not a stage state. Changes go to the running session.
	return m.sendChangesToAutonomous(f, turn, n), noticeMsg{}, nil
}

// commentRewind is the send-back a route that rewinds performs once asked:
// one store transition per rerun edge in route.Path, then the stage it
// lands on runs. Nothing rides its kickoff by hand — the comments are
// still open in the artifact and the store, and each writer's run reads
// the ones it owns (engine.openSpecComments, the implement hints' diff
// comments), which is also how the ones a later stage owns reach it on
// the way back.
//
// The transitions come before the stale session is dropped, so a refused
// edge leaves the card where it was, session and all.
func (m *Shell) commentRewind(f domain.Feature, route engine.CommentRoute) *changesAsk {
	actor := m.humanActor()
	return &changesAsk{card: f.ID, question: route.Question(f.ID), do: func() tea.Cmd {
		return func() tea.Msg {
			ctx := context.Background()
			nf := f
			for _, to := range route.Path {
				var err error
				if nf, err = m.store.Transition(ctx, f.ID, to, actor); err != nil {
					return noticeMsg{text: sanitize(err.Error()), isErr: true, id: f.ID}
				}
			}
			m.dropSession(f.ID)
			if err := m.engine.Run(nf); err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true, id: f.ID}
			}
			return noticeMsg{text: fmt.Sprintf("%s: sent back to %s with %s", f.ID, route.Target, route.Why()), reload: true, clearInbox: f.ID}
		}
	}}
}

// artifactDoc parses f's artifact wherever it lives right now; the empty
// document when there is none or it cannot be read.
func (m *Shell) artifactDoc(f domain.Feature) spec.Doc {
	path := m.artifactFile(&f)
	if path == "" {
		return spec.Doc{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return spec.Doc{}
	}
	return spec.Parse(string(raw))
}

// openDiffComments is the number of f's unresolved diff comments, whatever
// stage it is in; zero on a store error.
func (m *Shell) openDiffComments(id domain.FeatureID) int {
	anns, err := m.store.ListDiffAnnotations(context.Background(), id)
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
// report) that hold its current gate (DESIGN §6.1: unresolved annotations
// block the gate) — all of them but those a later stage owns
// (engine.SpecCommentsHolding). The template's own `@gummi` prompts and
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
	return len(engine.SpecCommentsHolding(domain.CardTypeOf(&f), f.Stage, spec.Parse(string(raw))))
}

// openDiffCommentsBlockingGate returns the number of unresolved diff
// annotations holding f's gate — the diff-backend half of DESIGN §6.1's
// "unresolved annotations block the gate" (openQuestionsBlockingGate is
// the artifact half): none before implement, which owns them
// (engine.DiffCommentsHold). Zero on any store error: like an unreadable
// artifact, a failed read never wedges the gate shut.
func (m *Shell) openDiffCommentsBlockingGate(f domain.Feature) int {
	if !engine.DiffCommentsHold(f.Stage) {
		return 0
	}
	return m.openDiffComments(f.ID)
}
