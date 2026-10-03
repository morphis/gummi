package ui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/attachment"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// waitSessionIdle polls a card's live session until its kickoff turn has
// settled, so a decision it pins has landed before a test reads it.
func waitSessionIdle(t *testing.T, eng *engine.Engine, id domain.FeatureID) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := eng.Get(id); s != nil && !s.Snapshot().Busy && len(s.Snapshot().Transcript) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s's session never settled", id)
}

// testPNG is the smallest valid PNG: a 1x1 image, used wherever a test
// needs bytes that sniff as image/png without caring what they show.
var testPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

// cardAgainst reads id's current pinned decision's Against.Token, if it
// has one — the design gate a fresh interactive attach pins as soon as
// its kickoff turn idles, here, which every send against that card must
// carry or be refused "moved".
func cardAgainst(t *testing.T, ctx context.Context, b *Bridge, id domain.FeatureID) string {
	t.Helper()
	c, err := b.Card(ctx, string(id))
	if err != nil {
		t.Fatal(err)
	}
	if c.Decision == nil {
		return ""
	}
	return c.Decision.Against.Token
}

// TestWebSendAttachmentsSteer asserts that a steer (a line typed against a
// live session) carrying an attachment id goes through, on a backend
// whose capability says it can take images.
func TestWebSendAttachmentsSteer(t *testing.T) {
	ag := agent.NewFake("ack")
	ag.Caps.Images = true
	unregister := agent.RegisterCapabilities("fake", ag.Caps)
	defer unregister()
	b, log, eng, f, _ := headlessBoard(t, ag)
	log.waitFor(t, "board", func(c webapi.Change) bool { return c.Kind == webapi.ChangeBoard })

	ctx := context.Background()
	if _, err := eng.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	waitSessionIdle(t, eng, f.ID)

	ref, err := b.PutAttachment(bytes.NewReader(testPNG), "shot.png")
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.Send(ctx, string(f.ID), webapi.SendRequest{
		Text: "look at this", Attachments: []string{ref.ID}, Against: cardAgainst(t, ctx, b, f.ID),
	}, "Simon")
	if err != nil {
		t.Fatal(err)
	}
	if res.Route != webapi.RouteSteer {
		t.Fatalf("route = %s, want steer", res.Route)
	}

	transcript := eng.Get(f.ID).Snapshot().Transcript
	var found *engine.Message
	for i := range transcript {
		if transcript[i].Content == "look at this" {
			found = &transcript[i]
		}
	}
	if found == nil {
		t.Fatalf("transcript has no message with the sent text: %+v", transcript)
	}
	if len(found.Images) != 1 || found.Images[0].Name != "shot.png" {
		t.Fatalf("transcript image = %+v, want name %q (what was uploaded, not its content hash)", found.Images, "shot.png")
	}
}

// TestWebSendAttachmentsRefusedOnVerb asserts that a route other than
// steer, consult or a freeform turn refuses a line carrying images with a
// 4xx, rather than silently dropping them or routing text-only — this
// caught the RouteMenu early-return skipping the check entirely.
func TestWebSendAttachmentsRefusedOnVerb(t *testing.T) {
	ag := agent.NewFake("ack")
	ag.Caps.Images = true
	unregister := agent.RegisterCapabilities("fake", ag.Caps)
	defer unregister()
	b, log, eng, f, _ := headlessBoard(t, ag)
	log.waitFor(t, "board", func(c webapi.Change) bool { return c.Kind == webapi.ChangeBoard })

	ctx := context.Background()
	if _, err := eng.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	waitSessionIdle(t, eng, f.ID)

	ref, err := b.PutAttachment(bytes.NewReader(testPNG), "shot.png")
	if err != nil {
		t.Fatal(err)
	}
	// "/pause" is a verb, not a turn to a running agent: the composer
	// classifies it as RouteVerb (or RouteMenu), never one of the three
	// routes images are allowed on.
	_, err = b.Send(ctx, string(f.ID), webapi.SendRequest{
		Text: "/pause", Attachments: []string{ref.ID}, Against: cardAgainst(t, ctx, b, f.ID),
	}, "Simon")
	var werr *WebError
	if !errors.As(err, &werr) || werr.Code != WebBadRequest {
		t.Fatalf("err = %v, want a WebBadRequest refusal", err)
	}
}

// TestWebSendAttachmentsUnknownID asserts that an unresolvable attachment
// id is refused with a 4xx and never reaches the engine.
func TestWebSendAttachmentsUnknownID(t *testing.T) {
	b, log, eng, f, _ := headlessBoard(t, agent.NewFake("ack"))
	log.waitFor(t, "board", func(c webapi.Change) bool { return c.Kind == webapi.ChangeBoard })

	ctx := context.Background()
	if _, err := eng.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	waitSessionIdle(t, eng, f.ID)
	_, err := b.Send(ctx, string(f.ID), webapi.SendRequest{
		Text: "hello", Attachments: []string{"not-a-real-id"}, Against: cardAgainst(t, ctx, b, f.ID),
	}, "Simon")
	var werr *WebError
	if !errors.As(err, &werr) || werr.Code != WebBadRequest {
		t.Fatalf("err = %v, want a WebBadRequest refusal", err)
	}
}

// TestComposerImagesFollowsBackend asserts that Composer.Images tracks
// the card's live session capability: true on a capable backend, false
// on one without it.
func TestComposerImagesFollowsBackend(t *testing.T) {
	ag := agent.NewFake("ack")
	ag.Caps.Images = true
	unregister := agent.RegisterCapabilities("fake", ag.Caps)
	defer unregister()
	b, log, eng, f, _ := headlessBoard(t, ag)
	log.waitFor(t, "board", func(c webapi.Change) bool { return c.Kind == webapi.ChangeBoard })

	ctx := context.Background()
	if _, err := eng.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	comp, err := b.Composer(ctx, string(f.ID), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !comp.Images {
		t.Errorf("composer.Images = false on a capable backend")
	}
	unregister()

	ag2 := agent.NewFake("ack") // Caps.Images left false
	unregister2 := agent.RegisterCapabilities("fake", ag2.Caps)
	defer unregister2()
	b2, log2, eng2, f2, _ := headlessBoard(t, ag2)
	log2.waitFor(t, "board", func(c webapi.Change) bool { return c.Kind == webapi.ChangeBoard })
	if _, err := eng2.Attach(ctx, f2); err != nil {
		t.Fatal(err)
	}
	comp2, err := b2.Composer(ctx, string(f2.ID), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if comp2.Images {
		t.Errorf("composer.Images = true on a backend without the capability")
	}
}

// TestCreateCardAttachmentsLinkInSpec asserts that a description
// attachment lands as a spec-anchored link in the card's seeded draft.
func TestCreateCardAttachmentsLinkInSpec(t *testing.T) {
	b, _, _, _, _ := headlessBoard(t, agent.NewFake("ack"))
	ctx := context.Background()
	var ws state.Workspace
	var store *state.Store
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { ws, store = m.ws, m.store; return nil }); err != nil {
		t.Fatal(err)
	}
	ref, err := b.PutAttachment(bytes.NewReader(testPNG), "shot.png")
	if err != nil {
		t.Fatal(err)
	}
	desc := "Screenshot of the broken layout.\n\nSee the attached image."
	c, err := b.CreateCard(ctx, webapi.CreateCardRequest{
		Kind: "feature", Title: "Fix the broken layout", Description: desc, Attachments: []string{ref.ID},
	}, "Simon")
	if err != nil {
		t.Fatal(err)
	}
	f, err := store.GetFeature(ctx, domain.FeatureID(c.ID))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f)))
	if err != nil {
		t.Fatalf("seeded draft missing: %v", err)
	}
	link := attachment.Link(attachment.Ref{ID: ref.ID, Name: "shot.png", MediaType: ref.MediaType})
	if !strings.Contains(string(raw), link) {
		t.Errorf("draft missing the attachment link %q:\n%s", link, raw)
	}
}

// TestCreateCardUnknownAttachmentMintsNothing asserts that an unresolvable
// attachment id refuses the create before any card is minted.
func TestCreateCardUnknownAttachmentMintsNothing(t *testing.T) {
	b, _, _, _, _ := headlessBoard(t, agent.NewFake("ack"))
	ctx := context.Background()
	var store *state.Store
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { store = m.store; return nil }); err != nil {
		t.Fatal(err)
	}
	before, err := store.ListFeatures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.CreateCard(ctx, webapi.CreateCardRequest{
		Kind: "feature", Title: "Fix the broken layout", Attachments: []string{"not-a-real-id"},
	}, "Simon")
	var werr *WebError
	if !errors.As(err, &werr) || werr.Code != WebBadRequest {
		t.Fatalf("err = %v, want a WebBadRequest refusal", err)
	}
	after, err := store.ListFeatures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("minted %d card(s) despite the unknown attachment", len(after)-len(before))
	}
}

// TestSpecNoteWithAttachmentIsOneLine asserts that a spec note posted
// with an attachment stays one line, with the attachment's link on it.
func TestSpecNoteWithAttachmentIsOneLine(t *testing.T) {
	f := domain.Feature{ID: "FD-001", Num: 1, Title: "Dark mode", Slug: "dark-mode", Stage: domain.StageImplement}
	b, log, _, f, _ := headlessBoardFor(t, agent.NewFake("ack"), f)
	log.waitFor(t, "board", func(c webapi.Change) bool { return c.Kind == webapi.ChangeBoard })
	ctx := context.Background()

	ref, err := b.PutAttachment(bytes.NewReader(testPNG), "shot.png")
	if err != nil {
		t.Fatal(err)
	}

	// The worktree and its commit are set up on the test's own goroutine,
	// not inside a Do closure: t.Fatal (which commitWork and other test
	// helpers call on failure) runs on whatever goroutine calls it, and
	// calling it from inside a closure Do runs on its own goroutine would
	// unwind that goroutine without ever answering Do — hanging Do, and
	// the whole test binary, rather than failing the test.
	var root string
	var wt *worktree.Pool
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { root, wt = m.ws.RepoRoot, m.wt; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Create(ctx, &f); err != nil {
		t.Fatal(err)
	}
	commitWork(t, root, string(f.ID))
	path := filepath.Join(root, f.WorktreePath(), f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	body := "# Dark mode\n\n## Goal\n\nMake it dark.\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var sp webapi.Spec
	var addErr error
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		d, err := m.WebDocs(string(f.ID))
		if err != nil {
			addErr = err
			return nil
		}
		sp, addErr = d.AddSpecNote(context.Background(), 3, "see the mock", "sam", []string{ref.ID})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if addErr != nil {
		t.Fatal(addErr)
	}
	if len(sp.Notes) != 1 {
		t.Fatalf("notes = %+v, want 1", sp.Notes)
	}
	note := sp.Notes[0]
	link := attachment.Link(attachment.Ref{ID: ref.ID, Name: "shot.png", MediaType: ref.MediaType})
	if !strings.Contains(note.Text, link) {
		t.Errorf("note text %q missing the attachment link %q", note.Text, link)
	}
	if strings.Contains(note.Text, "\n") {
		t.Errorf("note text %q is not one line", note.Text)
	}
}

// TestCreateCardAttachmentsRideTheSessionFirstTurn asserts that a session
// (a freeform card) started with a first message that carries attachment
// ids delivers those images to the session's first turn itself, the way a
// capable backend takes them — not only as links in the text.
func TestCreateCardAttachmentsRideTheSessionFirstTurn(t *testing.T) {
	ag := agent.NewFake("ack")
	ag.Caps.Images = true
	unregister := agent.RegisterCapabilities("fake", ag.Caps)
	defer unregister()
	b, _, eng, _, _ := headlessBoard(t, ag)
	ctx := context.Background()

	ref, err := b.PutAttachment(bytes.NewReader(testPNG), "shot.png")
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.CreateCard(ctx, webapi.CreateCardRequest{
		Kind:        "freeform",
		Description: "Fix the header: the screenshot shows it wrapping.\n\nSee the attached image.",
		Attachments: []string{ref.ID},
	}, "Simon")
	if err != nil {
		t.Fatal(err)
	}
	id := domain.FeatureID(c.ID)

	deadline := time.Now().Add(10 * time.Second)
	var snap engine.Snapshot
	for time.Now().Before(deadline) {
		if ff := eng.Freeform(id); ff != nil {
			snap = ff.Snapshot()
			if !snap.Busy && len(snap.Transcript) > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		if time.Now().After(deadline) {
			t.Fatalf("%s's first turn never settled: %+v", id, snap)
		}
	}
	var found *engine.Message
	for i := range snap.Transcript {
		if snap.Transcript[i].Author == "user" {
			found = &snap.Transcript[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("transcript has no user turn: %+v", snap.Transcript)
	}
	if len(found.Images) != 1 || found.Images[0].Name != "shot.png" {
		t.Fatalf("first turn images = %+v, want shot.png", found.Images)
	}
	if strings.Contains(found.Content, "Attached image:") {
		t.Errorf("a capable backend got the image by path line: %q", found.Content)
	}
}
