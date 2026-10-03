package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/attachment"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// turner is the test-visible slice of *agent.Fake's session: the Turns it
// recorded, without naming the unexported fakeSession type.
type turner interface {
	Turns() []agent.Turn
}

func putTestImage(t *testing.T, e *Engine) AttachmentRef {
	t.Helper()
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
		0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
		0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
	ref, err := e.Attachments().Put(bytes.NewReader(png), "shot.png")
	if err != nil {
		t.Fatal(err)
	}
	return AttachmentRef{ID: ref.ID, Name: ref.Name, MediaType: ref.MediaType, Size: ref.Size}
}

func TestSendTurnDeliversImages(t *testing.T) {
	ctx := context.Background()
	f := feature(1, "Dark mode", domain.StagePlan)
	ag := agent.NewFake("ack")
	ag.Caps.Images = true
	unregister := agent.RegisterCapabilities("fake", ag.Caps)
	defer unregister()
	e := newEngine(t, ag)
	s, err := e.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventStarted)
	waitFor(t, e, EventIdle) // the kickoff turn (agent speaks first) completes

	ref := putTestImage(t, e)
	if err := e.SendTurn(ctx, f.ID, "look at this", []AttachmentRef{ref}); err != nil {
		t.Fatal(err)
	}

	tn, ok := s.agent().(turner)
	if !ok {
		t.Fatal("fake session does not expose Turns()")
	}
	turns := tn.Turns()
	last := turns[len(turns)-1]
	if len(last.Images) != 1 {
		t.Fatalf("Turns() = %+v, want the last turn to carry one image", turns)
	}
	if last.Images[0].MediaType != "image/png" {
		t.Errorf("image media type = %q, want image/png", last.Images[0].MediaType)
	}

	got := s.Snapshot().Transcript
	lastMsg := got[len(got)-1]
	if len(lastMsg.Images) != 1 || lastMsg.Images[0].ID != ref.ID {
		t.Fatalf("transcript's user message Images = %+v, want %+v", lastMsg.Images, ref)
	}
}

func TestSendTurnRefusesWithoutImageCapability(t *testing.T) {
	ctx := context.Background()
	f := feature(1, "Dark mode", domain.StagePlan)
	ag := agent.NewFake("ack") // Caps.Images left false
	unregister := agent.RegisterCapabilities("fake", ag.Caps)
	defer unregister()
	e := newEngine(t, ag)
	s, err := e.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventStarted)
	waitFor(t, e, EventIdle) // the kickoff turn (agent speaks first) completes

	ref := putTestImage(t, e)
	before := len(s.Snapshot().Transcript)
	err = e.SendTurn(ctx, f.ID, "look at this", []AttachmentRef{ref})
	if !errors.Is(err, agent.ErrImagesUnsupported) {
		t.Fatalf("err = %v, want ErrImagesUnsupported", err)
	}
	if after := len(s.Snapshot().Transcript); after != before {
		t.Fatalf("refused turn changed the transcript: %d -> %d entries", before, after)
	}
}

func TestSendTurnImagesSurviveRestore(t *testing.T) {
	ctx := context.Background()
	ws, store, wt := newRepo(t)
	f := feature(1, "Dark mode", domain.StagePlan)
	createFeature(t, store, f)
	ag := agent.NewFake("ack")
	ag.Caps.Images = true
	unregister := agent.RegisterCapabilities("fake", ag.Caps)
	defer unregister()
	e := persistEngine(t, ag, ws, store, wt)
	if _, err := e.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventStarted)
	waitFor(t, e, EventIdle) // the kickoff turn (agent speaks first) completes

	ref := putTestImage(t, e)
	if err := e.SendTurn(ctx, f.ID, "look at this", []AttachmentRef{ref}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventIdle)

	snaps, err := store.LoadSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 {
		t.Fatalf("LoadSessions returned %d snapshots, want 1", len(snaps))
	}
	var found bool
	for _, m := range snaps[0].Transcript {
		if m.Author == "user" && len(m.Images) == 1 && m.Images[0].ID == ref.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("restored transcript missing the image ref: %+v", snaps[0].Transcript)
	}
}

// TestConsultSendTurnRefusedImagesNotRecorded asserts that a consult
// turn's images, refused only once the send actually reaches the backend
// (a live per-model check checkImageCapable's structural gate cannot see
// — simulated via Fake.RefuseImages), are never durably recorded: not in
// the card's event log, and not as an echo in the consult session's own
// transcript.
func TestConsultSendTurnRefusedImagesNotRecorded(t *testing.T) {
	ctx := context.Background()
	ws, store, wt := newRepo(t)
	f := feature(1, "dark mode", domain.StageImplement)
	createFeature(t, store, f)
	ag := agent.NewFake("ack")
	ag.Caps.Images = true
	ag.RefuseImages = true
	unregister := agent.RegisterCapabilities("fake", ag.Caps)
	defer unregister()
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e.Close() })

	c, err := e.OpenConsult(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	ref := putTestImage(t, e)
	before := len(c.Snapshot().Transcript)
	err = c.SendTurn(ctx, "look at this", []AttachmentRef{ref})
	if !errors.Is(err, agent.ErrImagesUnsupported) {
		t.Fatalf("err = %v, want ErrImagesUnsupported", err)
	}
	if after := len(c.Snapshot().Transcript); after != before {
		t.Fatalf("refused turn changed the consult transcript: %d -> %d entries", before, after)
	}

	evs, err := store.Events(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Kind == state.EventConsult {
			t.Errorf("refused turn was recorded on the card's log: %+v", ev)
		}
	}
}

// writeSpecWithAttachment writes f's pre-promotion draft with a
// spec-anchored link to ref appended after the blank template, so the
// worktree copy newAgentSession promotes carries it into the kickoff read.
func writeSpecWithAttachment(t *testing.T, wt *worktree.Manager, f domain.Feature, ref AttachmentRef) {
	t.Helper()
	p := filepath.Join(wt.Root(), f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	link := attachment.Link(attachment.Ref{ID: ref.ID, Name: ref.Name, MediaType: ref.MediaType, Size: ref.Size})
	content := spec.Template(&f) + "\n\n" + link + "\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestKickoffImagesDelivered asserts that a fresh autonomous stage whose
// spec links an image sends its kickoff turn carrying that image, when the
// live backend can take it.
func TestKickoffImagesDelivered(t *testing.T) {
	ws, store, wt := newRepo(t)
	ag := agent.NewFake("ack")
	ag.Caps.Images = true
	unregister := agent.RegisterCapabilities("fake", ag.Caps)
	defer unregister()
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	ref := putTestImage(t, e)
	writeSpecWithAttachment(t, wt, f, ref)

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventStarted)
	tn, ok := e.Get(f.ID).agent().(turner)
	if !ok {
		t.Fatal("fake session does not expose Turns()")
	}
	waitFor(t, e, EventIdle)

	turns := tn.Turns()
	if len(turns) != 1 || len(turns[0].Images) != 1 {
		t.Fatalf("Turns() = %+v, want one kickoff turn carrying one image", turns)
	}
	if turns[0].Images[0].MediaType != "image/png" {
		t.Errorf("kickoff image media type = %q, want image/png", turns[0].Images[0].MediaType)
	}
}

// TestKickoffImagesNamedWhenBackendCannot asserts that the same spec-linked
// image, on a backend that cannot take images, still starts the stage:
// text-only, naming the image's absolute path rather than erroring.
func TestKickoffImagesNamedWhenBackendCannot(t *testing.T) {
	ws, store, wt := newRepo(t)
	ag := agent.NewFake("ack") // Caps.Images left false
	unregister := agent.RegisterCapabilities("fake", ag.Caps)
	defer unregister()
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	ref := putTestImage(t, e)
	writeSpecWithAttachment(t, wt, f, ref)

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventStarted)
	tn, ok := e.Get(f.ID).agent().(turner)
	if !ok {
		t.Fatal("fake session does not expose Turns()")
	}
	waitFor(t, e, EventIdle)

	turns := tn.Turns()
	if len(turns) != 1 || len(turns[0].Images) != 0 {
		t.Fatalf("Turns() = %+v, want one text-only kickoff turn", turns)
	}
	path, err := e.Attachments().Path(ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(turns[0].Text, path) {
		t.Errorf("kickoff text = %q, want it to name %q", turns[0].Text, path)
	}
}

// TestKickoffImagesFallsBackOnLateRefusal asserts that a backend whose
// structural capability says it can take images, but whose live per-model
// check refuses once the kickoff actually reaches it (copilot's vision
// flag, simulated here via Fake.RefuseImages — kickoffTurn's own
// checkImageCapable gate cannot see this ahead of time), still starts the
// stage text-only naming the image's path, rather than failing the run.
func TestKickoffImagesFallsBackOnLateRefusal(t *testing.T) {
	ws, store, wt := newRepo(t)
	ag := agent.NewFake("ack")
	ag.Caps.Images = true
	ag.RefuseImages = true
	unregister := agent.RegisterCapabilities("fake", ag.Caps)
	defer unregister()
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	ref := putTestImage(t, e)
	writeSpecWithAttachment(t, wt, f, ref)

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventStarted)
	tn, ok := e.Get(f.ID).agent().(turner)
	if !ok {
		t.Fatal("fake session does not expose Turns()")
	}
	waitFor(t, e, EventIdle)

	if s := e.Get(f.ID); s != nil {
		if err := s.Snapshot().Err; err != nil {
			t.Fatalf("stage failed on a late image refusal: %v", err)
		}
	}
	turns := tn.Turns()
	if len(turns) != 1 || len(turns[0].Images) != 0 {
		t.Fatalf("Turns() = %+v, want one text-only kickoff turn", turns)
	}
	path, err := e.Attachments().Path(ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(turns[0].Text, path) {
		t.Errorf("kickoff text = %q, want it to name %q", turns[0].Text, path)
	}
}

// TestKickoffImagesMissingFileIsNotFatal asserts that a spec link to an
// attachment id the store has never seen (its file went missing) is named
// as missing rather than failing the stage.
func TestKickoffImagesMissingFileIsNotFatal(t *testing.T) {
	ws, store, wt := newRepo(t)
	ag := agent.NewFake("ack")
	unregister := agent.RegisterCapabilities("fake", ag.Caps)
	defer unregister()
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	missing := AttachmentRef{ID: strings.Repeat("a", 64), Name: "gone.png", MediaType: "image/png"}
	writeSpecWithAttachment(t, wt, f, missing)

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventStarted)
	tn, ok := e.Get(f.ID).agent().(turner)
	if !ok {
		t.Fatal("fake session does not expose Turns()")
	}
	waitState(t, e, f.ID, StateDone)

	turns := tn.Turns()
	if len(turns) != 1 || len(turns[0].Images) != 0 {
		t.Fatalf("Turns() = %+v, want one text-only kickoff turn", turns)
	}
	if !strings.Contains(turns[0].Text, "Attached image missing: "+missing.ID) {
		t.Errorf("kickoff text = %q, want it to name the missing attachment", turns[0].Text)
	}
}
