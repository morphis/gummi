package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/agentplugins"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/webapi"
)

func TestIsSkillLine(t *testing.T) {
	for text, want := range map[string]bool{
		"/skill":              true,
		"/skill review":       true,
		"  /SKILL review now": true,
		"/skill\treview":      true,
		"/skills":             false,
		"/skillful":           false,
		"skill review":        false,
		"use /skill review":   false,
		"":                    false,
	} {
		if got := isSkillLine(text); got != want {
			t.Errorf("isSkillLine(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestExpandSkillLine(t *testing.T) {
	lib := []agentplugins.Item{
		{ID: "skill-review-rules", Name: "Review rules", Kind: agentplugins.KindSkill},
		{ID: "skill-deploy", Name: "Deploy", Kind: agentplugins.KindSkill},
	}
	ws := "/ws"
	deployFile := filepath.Join(agentplugins.ItemDir(ws, "skill-deploy"), "SKILL.md")
	cases := []struct {
		name, text, wantID, wantOut, wantProblem string
		wantOK                                   bool
	}{
		{name: "not a skill line", text: "ship it", wantOut: "ship it"},
		{name: "plural is not the command", text: "/skills", wantOut: "/skills"},
		{name: "no name", text: "/skill ", wantOK: true, wantProblem: "name a skill"},
		{name: "unknown name", text: "/skill nope do it", wantOK: true, wantProblem: `no skill "nope"`},
		{
			name: "by id, no message", text: "/skill skill-deploy", wantOK: true, wantID: "skill-deploy",
			wantOut: `Use the "Deploy" skill for this (library id ` + "`skill-deploy`, instructions in `" + deployFile + "`).",
		},
		{
			name: "by name, any case, message leads", text: "/skill DEPLOY  roll out v2 ", wantOK: true, wantID: "skill-deploy",
			wantOut: "roll out v2\n\nUse the \"Deploy\" skill",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, id, ok, problem := expandSkillLine(c.text, ws, lib)
			if ok != c.wantOK || id != c.wantID {
				t.Fatalf("got id %q ok %v, want %q %v", id, ok, c.wantID, c.wantOK)
			}
			if !strings.Contains(problem, c.wantProblem) || (c.wantProblem == "" && problem != "") {
				t.Fatalf("problem = %q, want %q", problem, c.wantProblem)
			}
			if c.wantProblem != "" {
				if out != c.text {
					t.Errorf("a refused line was rewritten to %q", out)
				}
				return
			}
			if !strings.HasPrefix(out, c.wantOut) {
				t.Errorf("out = %q, want prefix %q", out, c.wantOut)
			}
		})
	}
	if _, _, ok, problem := expandSkillLine("/skill deploy", ws, nil); !ok || !strings.Contains(problem, "no skills") {
		t.Errorf("empty library: ok %v problem %q, want a refusal saying the library is empty", ok, problem)
	}
}

// skillBoard is a headless board whose library holds two skills.
func skillBoard(t *testing.T) (*Bridge, *state.Store, agentplugins.Item, agentplugins.Item) {
	t.Helper()
	b, _, _, _, _ := headlessBoard(t, agent.NewFake("ack"))
	var ws state.Workspace
	var store *state.Store
	if err := b.Do(context.Background(), func(m *Shell) tea.Cmd { ws, store = m.ws, m.store; return nil }); err != nil {
		t.Fatal(err)
	}
	lib, err := agentplugins.New(ws.Root, []agentplugins.Repo{{Name: "default", Root: ws.Root}})
	if err != nil {
		t.Fatal(err)
	}
	review, err := lib.Create(agentplugins.KindSkill, "Review rules", "---\nname: review-rules\ndescription: how we review\n---\n")
	if err != nil {
		t.Fatal(err)
	}
	deploy, err := lib.Create(agentplugins.KindSkill, "Deploy", "---\nname: deploy\ndescription: how we ship\n---\n")
	if err != nil {
		t.Fatal(err)
	}
	return b, store, review, deploy
}

func TestWebFormOffersLibrarySkills(t *testing.T) {
	b, _, review, deploy := skillBoard(t)
	var form webapi.Form
	if err := b.Do(context.Background(), func(m *Shell) tea.Cmd {
		var err error
		form, err = m.WebForm("")
		if err != nil {
			t.Error(err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(form.Skills) != 2 || form.Skills[0].Value != deploy.ID || form.Skills[1].Value != review.ID {
		t.Fatalf("form skills = %+v, want Deploy then Review rules", form.Skills)
	}
	if form.Skills[1].Label != "Review rules" || form.Skills[1].Detail != "how we review" {
		t.Errorf("skill choice = %+v, want its name and description", form.Skills[1])
	}
}

func TestCreateCardKeepsPickedSkills(t *testing.T) {
	b, store, review, _ := skillBoard(t)
	ctx := context.Background()
	c, err := b.CreateCard(ctx, webapi.CreateCardRequest{Kind: "feature", Title: "Tighten review", Skills: []string{review.ID}}, "Simon")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Skills) != 1 || c.Skills[0] != review.ID {
		t.Errorf("card skills = %q, want [%s]", c.Skills, review.ID)
	}
	f, err := store.GetFeature(ctx, domain.FeatureID(c.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Skills) != 1 || f.Skills[0] != review.ID {
		t.Errorf("stored skills = %q, want [%s]", f.Skills, review.ID)
	}
}

func TestCreateCardRefusesSkillsItCannotForward(t *testing.T) {
	b, store, review, _ := skillBoard(t)
	ctx := context.Background()
	before, err := store.ListFeatures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for name, req := range map[string]webapi.CreateCardRequest{
		"unknown skill":   {Kind: "feature", Title: "Ship", Skills: []string{"skill-gone"}},
		"goal with skill": {Kind: "goal", Title: "Ship it all", Description: "done when shipped", Skills: []string{review.ID}},
		"unknown /skill":  {Kind: "freeform", Title: "Chat", Description: "/skill nope help"},
	} {
		_, err := b.CreateCard(ctx, req, "Simon")
		var werr *WebError
		if !errors.As(err, &werr) || werr.Code != WebBadRequest {
			t.Errorf("%s: err = %v, want a WebBadRequest refusal", name, err)
		}
	}
	after, err := store.ListFeatures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("minted %d card(s) despite the refusals", len(after)-len(before))
	}
}

// A session started with a "/skill" line gets the instruction in place of
// the sigil, and the skill it names joins the ones picked beside it.
func TestCreateSessionExpandsASkillLine(t *testing.T) {
	b, store, review, deploy := skillBoard(t)
	ctx := context.Background()
	c, err := b.CreateCard(ctx, webapi.CreateCardRequest{
		Kind: "freeform", Description: "/skill deploy roll out v2", Skills: []string{review.ID},
	}, "Simon")
	if err != nil {
		t.Fatal(err)
	}
	f, err := store.GetFeature(ctx, domain.FeatureID(c.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Skills) != 2 || f.Skills[0] != review.ID || f.Skills[1] != deploy.ID {
		t.Errorf("stored skills = %q, want the picked one and the one named", f.Skills)
	}
	if strings.Contains(f.Title, "/skill") {
		t.Errorf("title %q kept the sigil", f.Title)
	}
	if !strings.Contains(f.Title+f.OneLiner, "roll out v2") {
		t.Errorf("card %+v lost the message", f)
	}
}

// A "/skill" line sent to a live session reaches the agent as the
// instruction, and one naming no library skill is refused unsent.
func TestSendExpandsASkillLine(t *testing.T) {
	b, log, eng, f, _ := headlessBoard(t, agent.NewFake("ack"))
	log.waitFor(t, "board", func(c webapi.Change) bool { return c.Kind == webapi.ChangeBoard })
	ctx := context.Background()
	var root string
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { root = m.ws.Root; return nil }); err != nil {
		t.Fatal(err)
	}
	lib, err := agentplugins.New(root, []agentplugins.Repo{{Name: "default", Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Create(agentplugins.KindSkill, "Deploy", "# Deploy\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	waitSessionIdle(t, eng, f.ID)

	_, err = b.Send(ctx, string(f.ID), webapi.SendRequest{Text: "/skill nope go", Against: cardAgainst(t, ctx, b, f.ID)}, "Simon")
	var werr *WebError
	if !errors.As(err, &werr) || werr.Code != WebBadRequest {
		t.Fatalf("unknown skill: err = %v, want a WebBadRequest refusal", err)
	}
	if _, err := b.Send(ctx, string(f.ID), webapi.SendRequest{Text: "/skill deploy roll out v2", Against: cardAgainst(t, ctx, b, f.ID)}, "Simon"); err != nil {
		t.Fatal(err)
	}
	for _, m := range eng.Get(f.ID).Snapshot().Transcript {
		if strings.HasPrefix(m.Content, "roll out v2\n\nUse the \"Deploy\" skill") {
			return
		}
		if strings.Contains(m.Content, "/skill") {
			t.Fatalf("the sigil reached the agent: %q", m.Content)
		}
	}
	t.Fatalf("transcript has no expanded skill line: %+v", eng.Get(f.ID).Snapshot().Transcript)
}
