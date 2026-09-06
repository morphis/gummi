package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/spec"
)

func TestCompileOpenQuestions(t *testing.T) {
	doc := spec.Parse("Title\n%% @user(2026-07-04): per-device or synced?\n\nBody\n%% @user: what about webviews?\n%% @architect: resolved — covered\n")
	turn := compileOpenQuestions(doc)
	if !strings.Contains(turn, "per-device or synced?") {
		t.Errorf("compiled turn missing the open question:\n%s", turn)
	}
	if strings.Contains(turn, "webviews") {
		t.Errorf("compiled turn included a resolved thread:\n%s", turn)
	}
	if strings.Contains(turn, "L2") == false {
		t.Errorf("compiled turn missing the line reference:\n%s", turn)
	}
	// nothing open → empty
	if compileOpenQuestions(spec.Parse("Body\n%% @user: q\n%% @a: resolved — y\n")) != "" {
		t.Error("resolved-only doc should compile to empty")
	}
}

func TestUserAnnotationBlocksSpecApproval(t *testing.T) {
	m := specWorkspace(t)
	m = pressAdvance(t, m) // todo → plan (the design stage)
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("setup: stage = %s, want plan", m.rows[0].F.Stage)
	}
	// open the spec and add a user annotation
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "is this the right approach?")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	// approving is blocked while the annotation is open
	m = pressAdvance(t, m)
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("open user annotation did not block approval (stage=%s)", m.rows[0].F.Stage)
	}
	if !strings.Contains(m.notice.text, "open question") {
		t.Errorf("notice = %q, want a blocking message", m.notice.text)
	}

	// resolve it, then approval proceeds
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "resolved — yes, going with it")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = pressAdvance(t, m)
	if m.rows[0].F.Stage != domain.StageImplement {
		t.Fatalf("resolving the annotation did not unblock approval (stage=%s)", m.rows[0].F.Stage)
	}
}

// TestTemplatePromptsDoNotBlockAsQuestions: gummi's own `%% @gummi:`
// prompts are not user threads, so they never raise the open-question
// blocker. They do leave their sections undrafted, which is a different
// gate with a different message — asserted below — so this walks a card
// whose required section has been written and checks it crosses cleanly.
func TestTemplatePromptsDoNotBlockAsQuestions(t *testing.T) {
	m := specWorkspace(t)
	m = pressAdvance(t, m) // todo → plan (the design stage)
	m = openSpecFor(t, m)  // creates the draft with @gummi prompts
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = pressAdvance(t, m) // cross the design gate
	if m.rows[0].F.Stage != domain.StageImplement {
		t.Fatalf("template @gummi prompts blocked approval (stage=%s)", m.rows[0].F.Stage)
	}
}

// TestUndraftedSectionBlocksSpecApproval is the floor a blank spec used to
// walk straight through: a spec stage that wrote nothing leaves `Chosen
// approach` holding only its `%% @gummi:` prompt, and that must hold the
// gate shut even though no user thread is open. Measured before this gate
// existed: the crossing recorded `gate spec→implement auto-approved`,
// implement started from a stub, and the run still finished verified.
func TestUndraftedSectionBlocksSpecApproval(t *testing.T) {
	m := specWorkspace(t)
	m = pressAdvance(t, m) // todo → plan (the design stage)
	m = openSpecFor(t, m)  // creates the draft, every section undrafted
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	// press g WITHOUT drafting: the stage produced nothing
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("an undrafted spec crossed its gate (stage=%s)", m.rows[0].F.Stage)
	}
	if !strings.Contains(m.notice.text, "Chosen approach") {
		t.Errorf("notice = %q, want it to name the undrafted section", m.notice.text)
	}
}

func TestUserAnnotationBlocksWorkStageGate(t *testing.T) {
	// the gate out of a work stage blocks on open user annotations just
	// like the design gate — and past the design gate the artifact has
	// been promoted into the worktree, so this is the case where the
	// check must read the worktree copy, not the retired draft.
	m := specWorkspace(t)
	m = advanceTo(t, m, domain.StageImplement) // the worktree exists from here
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "the plan misses the migration step")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	m = pressAdvance(t, m)
	if m.rows[0].F.Stage != domain.StageImplement {
		t.Fatalf("open user annotation did not block the work gate (stage=%s)", m.rows[0].F.Stage)
	}
	if !strings.Contains(m.notice.text, "open question") {
		t.Errorf("notice = %q, want a blocking message", m.notice.text)
	}

	// resolve it, then the gate opens
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "resolved — added below")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = pressAdvance(t, m)
	if m.rows[0].F.Stage != domain.StageVerify {
		t.Fatalf("resolving the annotation did not unblock the gate (stage=%s)", m.rows[0].F.Stage)
	}
}

func TestRequestChangesRerunsAutonomousStage(t *testing.T) {
	// R with no session running has no chat to send to: it re-runs the
	// stage with the compiled comments appended to the kickoff.
	m, eng := chatWorkspace(t, agent.NewFake("Tightened the plan."))
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("setup: stage = %s, want plan", m.rows[0].F.Stage)
	}
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "the plan misses the migration step")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	m = press(t, m, tea.KeyPressMsg{Code: 'R', Text: "R"})
	settleChat(t, eng)
	if !strings.Contains(m.notice.text, "re-running plan") {
		t.Errorf("notice = %q, want a re-run confirmation", m.notice.text)
	}
	s := m.engine.Get("FD-001")
	if s == nil {
		t.Fatal("request-changes did not re-run the plan stage")
	}
	snap := s.Snapshot()
	if snap.Feature.Stage != domain.StagePlan || snap.Interactive {
		t.Fatalf("wrong session: stage=%s interactive=%v", snap.Feature.Stage, snap.Interactive)
	}
	// the comments ride in the kickoff (gummi's own first turn)
	if len(snap.Transcript) == 0 || !strings.Contains(snap.Transcript[0].Content, "misses the migration step") {
		t.Fatalf("kickoff missing the review comments: %+v", snap.Transcript)
	}
}

func TestRequestChangesSendsToAgent(t *testing.T) {
	// with a session live on the card, R hands the compiled comments to
	// it as a turn rather than re-running the stage under it — the user
	// is sitting in front of that context and it must not be thrown away.
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleScribe {
			return []agent.Event{{Kind: agent.EventIdle}}
		}
		// no idle event: the session stays running, which is the state
		// that makes R a live turn instead of a re-run.
		return []agent.Event{{Kind: agent.EventMessage, Text: "I'll address those."}}
	}}
	m, eng := chatWorkspace(t, ag)
	m = openAndAttach(t, m)
	waitLive(t, eng, "FD-001")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // back to the board

	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "please reconsider the storage choice")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	press(t, m, tea.KeyPressMsg{Code: 'R', Text: "R"})
	deadline := time.After(testWaitTimeout)
	for {
		s := eng.Get("FD-001")
		if s != nil {
			for _, msg := range s.Snapshot().Transcript {
				if msg.Author == engine.AuthorUser && strings.Contains(msg.Content, "reconsider the storage choice") {
					return
				}
			}
		}
		select {
		case <-deadline:
			t.Fatalf("the comments never reached the live session as a user turn: %+v", eng.Get("FD-001").Snapshot().Transcript)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
