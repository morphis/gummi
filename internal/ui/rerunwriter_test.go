package ui

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// architectRecorder passes every critique and records each architect
// kickoff.
type architectRecorder struct {
	mu     sync.Mutex
	kicked []string
}

func (a *architectRecorder) agent() *agent.Fake {
	return &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleReviewer {
			return []agent.Event{{Kind: agent.EventMessage, Text: "Sound.\nVERDICT: pass"}, {Kind: agent.EventIdle}}
		}
		if opts.Role == agent.RoleArchitect {
			a.mu.Lock()
			a.kicked = append(a.kicked, msg)
			a.mu.Unlock()
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "written"}, {Kind: agent.EventIdle}}
	}}
}

func (a *architectRecorder) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.kicked)
}

func (a *architectRecorder) last() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.kicked) == 0 {
		return ""
	}
	return a.kicked[len(a.kicked)-1]
}

// cleanDesignGate is FD-001 attended at its design gate, its critique
// clean.
func cleanDesignGate(t *testing.T, rec *architectRecorder) *Shell {
	t.Helper()
	m, eng := chatWorkspace(t, rec.agent())
	if err := m.store.SetGateApproval(context.Background(), "FD-001", domain.GateAttended); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)
	m = openAndAttach(t, m)
	settleChat(t, eng)
	m = drainEngineLoop(t, m)
	if it, ok := m.inbox.get("FD-001"); !ok || it.Kind != attnGate {
		t.Fatalf("fixture: no clean design gate: %+v", it)
	}
	return m
}

// TestRunningTheWriterAtACleanDesignGateRunsIt: at a design gate whose
// critique came back clean, asking for the stage to run is asking for its
// writer. It used to re-judge the finished critique, which re-raised the
// very gate the person was looking at, and nothing ran.
func TestRunningTheWriterAtACleanDesignGateRunsIt(t *testing.T) {
	rec := &architectRecorder{}
	m := cleanDesignGate(t, rec)
	before := rec.count()

	m = pump(t, m, m.fixedSendBack(m.rows[0], "run", ""))
	_ = drainEngineLoop(t, m)

	if rec.count() == before {
		t.Fatal("start the architect at a clean design gate ran nothing")
	}
}

// TestALineForTheWriterAtACleanDesignGateReachesIt: the same ask carrying
// a line — the re-entry chip's "go", after a paid classification — runs
// the writer with the line in its kickoff rather than dropping it.
func TestALineForTheWriterAtACleanDesignGateReachesIt(t *testing.T) {
	rec := &architectRecorder{}
	m := cleanDesignGate(t, rec)
	before := rec.count()

	const line = "rename the function to Wave as the note says"
	m = pump(t, m, m.runStageWithNote(m.rows[0].F, line))
	_ = drainEngineLoop(t, m)

	if rec.count() == before {
		t.Fatal("the line's run never started the architect")
	}
	if !strings.Contains(rec.last(), line) && !strings.Contains(strings.Join(rec.kicked[before:], "\n"), line) {
		t.Errorf("the line did not ride the architect's kickoff: %q", rec.kicked[before:])
	}
}
