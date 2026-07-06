package ui

import (
	"strings"
	"testing"

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
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // todo → brainstorm
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // brainstorm → spec
	if m.rows[0].F.Stage != domain.StageSpec {
		t.Fatalf("setup: stage = %s, want spec", m.rows[0].F.Stage)
	}
	// open the spec and add a user annotation
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "is this the right approach?")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	// approving is blocked while the annotation is open
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StageSpec {
		t.Fatalf("open user annotation did not block approval (stage=%s)", m.rows[0].F.Stage)
	}
	if !strings.Contains(m.notice.text, "open question") {
		t.Errorf("notice = %q, want a blocking message", m.notice.text)
	}

	// resolve it, then approval proceeds
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "resolved — yes, going with it")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("resolving the annotation did not unblock approval (stage=%s)", m.rows[0].F.Stage)
	}
}

func TestTemplatePromptsDoNotBlock(t *testing.T) {
	// a fresh spec (template @gummi prompts only) must not block approval
	m := specWorkspace(t)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // todo → brainstorm
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // brainstorm → spec
	m = openSpecFor(t, m)                                  // creates the draft with @gummi prompts
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // approve spec → plan
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Fatalf("template @gummi prompts blocked approval (stage=%s)", m.rows[0].F.Stage)
	}
}

func TestRequestChangesSendsToAgent(t *testing.T) {
	// chatWorkspace wires an engine; its FD-001 is at brainstorm
	m, eng := chatWorkspace(t, agent.NewFake("I'll address those."))
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // → spec (interactive)
	m = openSpecFor(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeString(t, m, "please reconsider the storage choice")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	// R compiles the open questions and sends them to the architect
	m = press(t, m, tea.KeyPressMsg{Code: 'R', Text: "R"})
	settleChat(t, eng)
	s := m.engine.Get("FD-001")
	if s == nil {
		t.Fatal("request-changes did not start an architect session")
	}
	// the compiled turn is the first user-authored message (a fresh
	// session opens with gummi's system kickoff before it)
	snap := s.Snapshot()
	var compiled string
	for _, msg := range snap.Transcript {
		if msg.Author == engine.AuthorUser {
			compiled = msg.Content
			break
		}
	}
	if compiled == "" {
		t.Fatalf("no compiled turn sent: %+v", snap.Transcript)
	}
	if !strings.Contains(compiled, "reconsider the storage choice") {
		t.Errorf("compiled turn missing the annotation:\n%s", compiled)
	}
}
