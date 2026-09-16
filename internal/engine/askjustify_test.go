package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// A design-stage question has to say what it changes. One that names no
// section is a confirmation — the model already wrote the recommendation
// it is asking about — and a confirmation costs a full round trip: a
// process, a fresh session, and a person's attention.
func TestAskWithoutAChangedSectionIsBounced(t *testing.T) {
	ctx := context.Background()
	f := feature(1, "justify the ask", domain.StagePlan)
	e := newEngine(t, agent.NewFake("ack"))
	s, err := e.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}

	e.handleAsk(s, &agent.ToolCall{
		ID:   "c1",
		Name: askToolName,
		Args: json.RawMessage(`{"question":"Looks right?","options":[{"label":"yes"},{"label":"no"}]}`),
	})
	if s.Snapshot().PendingAsk != nil {
		t.Fatal("a question naming no section reached the user")
	}
	var bounced string
	for _, line := range s.Snapshot().Activity {
		if strings.HasPrefix(line, AskBouncedNote) {
			bounced = line
		}
	}
	if bounced == "" {
		t.Fatal("the bounce left no trace on the card")
	}
	if !strings.Contains(bounced, "changes_section") {
		t.Errorf("the bounce does not say what to do about it: %s", bounced)
	}
}

// A section that is not in this spec is the same failure with an extra
// step, and the bounce names the sections that do exist.
func TestAskWithAnUnknownSectionIsBounced(t *testing.T) {
	ctx := context.Background()
	f := feature(1, "justify the ask", domain.StagePlan)
	e := newEngine(t, agent.NewFake("ack"))
	s, err := e.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	e.handleAsk(s, &agent.ToolCall{
		ID:   "c1",
		Name: askToolName,
		Args: json.RawMessage(`{"changes_section":"Marketing","question":"Which seam?","options":[{"label":"a"},{"label":"b"}]}`),
	})
	if s.Snapshot().PendingAsk != nil {
		t.Fatal("a question naming a section this spec does not have reached the user")
	}
}

// A question that names a real section is what the toll is for: it goes
// through untouched.
func TestAskNamingASectionGoesThrough(t *testing.T) {
	ctx := context.Background()
	f := feature(1, "justify the ask", domain.StagePlan)
	e := newEngine(t, agent.NewFake("ack"))
	s, err := e.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	e.handleAsk(s, &agent.ToolCall{
		ID:   "c1",
		Name: askToolName,
		Args: json.RawMessage(`{"changes_section":"Chosen approach","question":"Which seam owns it?","options":[{"label":"a"},{"label":"b"}]}`),
	})
	ask := s.Snapshot().PendingAsk
	if ask == nil {
		t.Fatal("a question that names the section it changes was bounced")
	}
	if ask.ChangesSection != "Chosen approach" {
		t.Errorf("ChangesSection = %q, want the section the model named", ask.ChangesSection)
	}
}

// A gate ask IS the crossing, not a decision inside the stage, so it
// carries no section and is never bounced for the lack of one.
func TestGateAskNeedsNoChangedSection(t *testing.T) {
	ctx := context.Background()
	f := feature(1, "justify the ask", domain.StagePlan)
	e := newEngine(t, agent.NewFake("ack"))
	s, err := e.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	e.handleAsk(s, &agent.ToolCall{
		ID:   "c1",
		Name: askToolName,
		Args: json.RawMessage(`{"question":"Ready to move on?","gate":true,"options":[{"label":"yes"}]}`),
	})
	if s.Snapshot().PendingAsk == nil {
		t.Fatal("a gate ask was bounced for naming no section")
	}
}

// The toll is about the work, not about the prose. A testing question
// truthfully changes the Verification plan — and the Verification plan is
// rewritten by check discovery and by the stage that proves the work, so
// an answer that only lands there changes nothing about what gets built.
// This is the question the toll was added for, and naming a section was
// not enough to stop it.
func TestAskNamingAProcessOwnedSectionIsBounced(t *testing.T) {
	ctx := context.Background()
	for _, section := range []string{"Verification plan", "verification plan", "Progress", "Review"} {
		t.Run(section, func(t *testing.T) {
			f := feature(1, "justify the ask", domain.StagePlan)
			e := newEngine(t, agent.NewFake("ack"))
			s, err := e.Attach(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			args := `{"changes_section":"` + section + `","question":"Which interfaces should the tests exercise?","options":[{"label":"unit"},{"label":"unit + integration"}]}`
			e.handleAsk(s, &agent.ToolCall{ID: "c1", Name: askToolName, Args: json.RawMessage(args)})
			if s.Snapshot().PendingAsk != nil {
				t.Fatalf("a question whose answer only changes %q reached the user", section)
			}
			var bounced string
			for _, line := range s.Snapshot().Activity {
				if strings.HasPrefix(line, AskBouncedNote) {
					bounced = line
				}
			}
			if !strings.Contains(bounced, "rewritten by the stage that does the work") {
				t.Errorf("the bounce does not say why that section cannot justify a question: %s", bounced)
			}
			if !strings.Contains(bounced, "Chosen approach") {
				t.Errorf("the bounce does not name a section that would justify asking: %s", bounced)
			}
		})
	}
}
