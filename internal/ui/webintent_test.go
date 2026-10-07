package ui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/reentry"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/webapi"
)

// The quit-resume question a headless board holds is the board's to
// show and the page's to answer: "not now" settles it and restarts
// nothing, and picking cards restarts those the quit stopped — and only
// those.
func TestHeadlessBoardHoldsTheResumeQuestion(t *testing.T) {
	b, _, eng, f, _ := headlessBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	quit := time.Date(2026, 9, 1, 8, 58, 0, 0, time.UTC)
	offer := func() {
		t.Helper()
		pausedByQuit(t, b, eng, f)
		if err := b.Do(ctx, func(m *Shell) tea.Cmd {
			m.resumeOffer = &quitResumeOffer{cards: []engine.QuitStoppedCard{{Feature: f, ParkedAt: time.Now()}}, since: "2m ago", at: quit}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	offer()
	bd := waitBoard(t, b, func(bd webapi.Board) bool { return bd.Resume != nil })
	if len(bd.Resume.Cards) != 1 || bd.Resume.Cards[0].ID != "FD-001" || bd.Resume.Since != "2m ago" || !bd.Resume.At.Equal(quit) {
		t.Fatalf("resume offer = %+v", bd.Resume)
	}
	if err := b.Resume(ctx, webapi.ResumeRequest{None: true}, "Simon"); err != nil {
		t.Fatal(err)
	}
	waitBoard(t, b, func(bd webapi.Board) bool { return bd.Resume == nil })
	if err := b.Resume(ctx, webapi.ResumeRequest{None: true}, "Simon"); err == nil {
		t.Error("answering a question nobody asked went through")
	}

	offer()
	if err := b.Resume(ctx, webapi.ResumeRequest{Cards: []string{"FD-009"}}, "Simon"); err == nil {
		t.Error("resuming a card the quit did not stop went through")
	}
	if err := b.Resume(ctx, webapi.ResumeRequest{Cards: []string{"FD-001"}}, "Simon"); err != nil {
		t.Fatal(err)
	}
	waitBoard(t, b, func(bd webapi.Board) bool { return bd.Resume == nil })
}

// pausedByQuit leaves f's session as a quit leaves it and a reopen
// restores it: paused, mid-stage.
func pausedByQuit(t *testing.T, b *Bridge, eng *engine.Engine, f domain.Feature) {
	t.Helper()
	ctx := context.Background()
	var saveErr error
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		saveErr = m.store.SaveSession(ctx, state.SessionSnapshot{
			Feature: f.ID, Stage: f.Stage, Role: "architect", State: "paused",
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if saveErr != nil {
		t.Fatal(saveErr)
	}
	if err := eng.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if s := eng.Get(f.ID); s == nil || s.State() != engine.StatePaused {
		t.Fatalf("precondition: %s was not restored paused", f.ID)
	}
}

// Starting an offered card from its own page answers the question for
// that card: the banner must not go on offering to resume a card that is
// already running.
func TestStartingAnOfferedCardByHandSettlesTheResumeQuestion(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ag := &agent.Fake{Responder: func(agent.SessionOpts, string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	b, _, eng, f, _ := headlessBoard(t, ag)
	ctx := context.Background()
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	pausedByQuit(t, b, eng, f)
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		m.quitCut = map[domain.FeatureID]bool{f.ID: true}
		m.resumeOffer = &quitResumeOffer{cards: []engine.QuitStoppedCard{{Feature: f, ParkedAt: time.Now()}}, since: "27m ago"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	waitBoard(t, b, func(bd webapi.Board) bool { return bd.Resume != nil })

	if err := b.Do(ctx, func(m *Shell) tea.Cmd { return m.runStage(f) }); err != nil {
		t.Fatal(err)
	}
	waitBoard(t, b, func(bd webapi.Board) bool { return bd.Resume == nil })
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		if m.quitCut[f.ID] {
			t.Errorf("%s is running and still marked as cut by the quit", f.ID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.Resume(ctx, webapi.ResumeRequest{None: true}, "Simon"); err == nil {
		t.Error("the question was settled, and answering it again went through")
	}
}

// Nothing on a headless board can see a dialog, so one no intent is
// answering is closed rather than left to sit on the stack.
func TestHeadlessBoardClosesDialogsNobodyAnswers(t *testing.T) {
	b, _, _, _, _ := headlessBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		m.Overlay.Push(&confirmDialog{id: "stray", question: "?", onConfirm: func() tea.Cmd { return nil }})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var open int
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { open = m.Overlay.Len(); return nil }); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Errorf("%d dialogs left open on a headless board", open)
	}
}

// An intent follows what its command started — a message that produces
// another command, and so on — and reports the error it ended on, the
// way the status bar would have said it.
func TestAnIntentFollowsItsCommandsToTheEnd(t *testing.T) {
	b, _, _, _, _ := headlessBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	out, err := b.intent(ctx, "FD-001", webInput{}, webWait, func(m *Shell, r featureRow) (tea.Cmd, error) {
		return tea.Sequence(
			func() tea.Msg { return noticeMsg{text: "first"} },
			tea.Batch(func() tea.Msg {
				time.Sleep(50 * time.Millisecond)
				return noticeMsg{text: "second went wrong", isErr: true}
			}),
		), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.settled || out.refused != "second went wrong" || len(out.notices) != 2 {
		t.Errorf("outcome = %+v", out)
	}
	if e := out.err(); e == nil || e.Error() != "second went wrong" {
		t.Errorf("err = %v", e)
	}
}

// The re-entry chip — a reading of a line the person sent, waiting to be
// confirmed — is the card's decision on the web face, as it stands where
// the picker stood in the TUI. "keep it here" is the chip's esc: nothing
// moves, and the line goes as a message.
func TestTheReentryChipIsAConfirmDecision(t *testing.T) {
	b, _, eng, f, _ := headlessBoard(t, agent.NewFake("noted"))
	ctx := context.Background()
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		m.setChip(&reentryReading{
			id: f.ID, line: "the flag was never in the spec",
			out: reentry.Outcome{Action: reentry.Rewind, Target: domain.StagePlan, Path: []domain.Stage{domain.StagePlan}, Reason: "requirement_missing", Note: "the flag was never in the spec"},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c, err := b.Card(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	d := c.Decision
	if d == nil || d.Kind != webapi.DecisionConfirm || len(d.Options) != 2 || d.Options[0].ID != webOptionGo || d.Options[1].ID != webOptionKeep {
		t.Fatalf("decision = %+v", d)
	}
	if _, err := b.Answer(ctx, "FD-001", webapi.AnswerRequest{Ref: d.Ref, Option: webOptionKeep, Against: d.Against.Token}, "Simon"); err != nil {
		t.Fatal(err)
	}
	c, err = b.Card(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if c.Decision != nil && c.Decision.Kind == webapi.DecisionConfirm {
		t.Error("the chip is still up after keep")
	}
	if c.Stage != string(domain.StagePlan) {
		t.Errorf("keep moved the card to %s", c.Stage)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if cs := eng.Consult(f.ID); cs != nil {
			for _, msg := range cs.Snapshot().Transcript {
				if msg.Content == "the flag was never in the spec" {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the kept line never reached the card's consult session")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A web write to a card's documents is made off the loop, so nothing the
// loop does would recount the card's gate blockers; RefreshBlockers does,
// and tells the pages the card changed.
func TestRefreshBlockersRecountsAWebWrite(t *testing.T) {
	// at implement, which owns diff comments: before it they hold no gate
	// and the row does not count them (engine.DiffCommentsHold)
	b, log, _, f, _ := headlessBoardFor(t, agent.NewFake("ok"), domain.Feature{ID: "FD-001", Num: 1, Title: "Dark mode", Slug: "dark-mode", Stage: domain.StageImplement})
	ctx := context.Background()
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	var store *state.Store
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { store = m.store; return nil }); err != nil {
		t.Fatal(err)
	}
	a := domain.DiffAnnotation{Feature: f.ID, File: "a.go", Anchor: "+x", Excerpt: "+x", Comment: "name it"}
	if _, err := store.AddDiffAnnotation(ctx, a, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { return m.RefreshBlockers(string(f.ID)) }); err != nil {
		t.Fatal(err)
	}
	log.waitFor(t, "a change for the card", func(c webapi.Change) bool { return c.Kind == webapi.ChangeCard && c.ID == string(f.ID) })
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := b.Do(ctx, func(m *Shell) tea.Cmd {
			if r, ok := m.rowByID(f.ID); ok {
				n = r.OpenDiffComments
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the row counts %d open diff comments, want 1", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A landing dialog opened downstream of a request that was not a landing
// — a goal that verified while an approval of its plan was being followed
// — is a person's to confirm. The request closes it unanswered, the draft
// in it notwithstanding; a landing request answers the same dialog.
func TestOnlyALandingRequestAnswersTheLandingDialog(t *testing.T) {
	b, _, _, _, _ := headlessBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	for _, tc := range []struct {
		name string
		in   webInput
		land bool
	}{
		{"an approval's flow", webInput{}, false},
		{"a confirmed approval's flow", webInput{confirm: webConfirmToken("confirm-design-gate", "FD-001", "advance FD-001?")}, false},
		// a landing lands the message the person read and sent back; one
		// with no message stops to have the draft read (the TUI's second
		// ctrl+s on an unreviewed draft)
		{"a landing with no message", webInput{land: true}, false},
		{"a landing", webInput{land: true, message: "feat: the drafted landing"}, true},
	} {
		landed := ""
		out, err := b.intent(ctx, "FD-001", tc.in, webWait, func(m *Shell, r featureRow) (tea.Cmd, error) {
			d := newCommitMsgDialog(r.F, func(msg string, _ domain.LandMethod) tea.Cmd { landed = msg; return nil }, nil)
			d.input.SetValue("feat: the drafted landing")
			m.Overlay.Push(d)
			return nil, nil
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := landed != ""; got != tc.land {
			t.Errorf("%s: landed = %v (%q), want %v", tc.name, got, landed, tc.land)
		}
		if tc.in.land && !tc.land {
			if out.needs != webapi.ActionNeedsMessage || out.draft == nil || *out.draft != "feat: the drafted landing" {
				t.Errorf("%s: needs %q draft %v, want the draft put back to be read", tc.name, out.needs, out.draft)
			}
		} else if !tc.land && out.err() != nil {
			t.Errorf("%s: leaving the landing to a person failed the request: %v", tc.name, out.err())
		}
		var open int
		if err := b.Do(ctx, func(m *Shell) tea.Cmd { open = m.Overlay.Len(); return nil }); err != nil {
			t.Fatal(err)
		}
		if open != 0 {
			t.Errorf("%s: %d dialogs left open", tc.name, open)
		}
	}
}

// Linking a pull request from the page answers the link dialog with the
// URL or number it carried, or with what the dialog opened on; picking a
// goal decision to reverse is sent to the goal's page, which lists them.
func TestWebAnswersTheLinkAndReverseDialogs(t *testing.T) {
	f := domain.Feature{ID: "FD-007"}
	var got []string
	d := newPRLinkDialog(f, "12", "", func(spec string) tea.Cmd { got = append(got, spec); return nil })
	d.webAnswer(nil, &webInput{message: " 7 "})
	d = newPRLinkDialog(f, "12", "", func(spec string) tea.Cmd { got = append(got, spec); return nil })
	d.webAnswer(nil, &webInput{})
	if len(got) != 2 || got[0] != "7" || got[1] != "12" {
		t.Fatalf("link submitted %q, want [7 12]", got)
	}
	res := (&reverseDialog{f: domain.Feature{ID: "GL-001"}}).webAnswer(nil, &webInput{})
	if res.needs != webapi.ActionNeedsDecision || res.cmd != nil {
		t.Fatalf("reverse answered %+v, want it to ask for the decision", res)
	}
}
