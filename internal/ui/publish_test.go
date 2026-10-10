package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/webapi"
)

func publishIDs(acts []cardAction) map[string]bool {
	out := map[string]bool{}
	for _, a := range acts {
		if _, ok := publishActs[a.id]; ok {
			out[a.id] = true
		}
	}
	return out
}

func TestPublishRowsAreOfferedOnlyWhereACardCouldBePublished(t *testing.T) {
	on := func(r featureRow) nextInput {
		return nextInput{stage: r.F.Stage, kind: r.F.Kind, landed: r.Landed, publish: true}
	}
	plain := cardRow(domain.KindFeature, domain.StageImplement, false, true)

	// a board that does not publish offers none of it
	off := on(plain)
	off.publish = false
	if got := publishIDs(cardActionsFor(off, plain)); len(got) != 0 {
		t.Fatalf("a board without gh offers %v", got)
	}

	got := publishIDs(cardActionsFor(on(plain), plain))
	if !got["prcreate"] || !got["push"] || got["prready"] || got["prdraft"] {
		t.Fatalf("an unlinked card offers %v, want open-PR and push only", got)
	}

	linked := plain
	linked.F.PullRequest = domain.PullRequestRef{Repo: "me/widget", Number: 7}
	got = publishIDs(cardActionsFor(on(linked), linked))
	if got["prcreate"] || !got["push"] || !got["prready"] || !got["prdraft"] {
		t.Fatalf("a linked card offers %v, want push, ready and draft", got)
	}

	stacked := plain
	stacked.F.StackID = "ST-001"
	research := cardRow(domain.KindResearch, domain.StageImplement, false, true)
	todo := cardRow(domain.KindFeature, domain.StageTodo, false, true)
	landed := cardRow(domain.KindFeature, domain.StageVerify, true, true)
	noTree := cardRow(domain.KindFeature, domain.StageImplement, false, false)
	for name, r := range map[string]featureRow{"stacked": stacked, "research": research, "todo": todo, "landed": landed, "no worktree": noTree} {
		if got := publishIDs(cardActionsFor(on(r), r)); len(got) != 0 {
			t.Errorf("a %s card offers %v", name, got)
		}
	}

	// an agent holding the card holds publishing off too
	busy := on(plain)
	busy.busy = true
	if got := publishIDs(cardActionsFor(busy, plain)); len(got) != 0 {
		t.Errorf("a busy card offers %v", got)
	}
}

func pressPublish(d *publishDialog, keys ...string) (closed bool) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "ctrl+d":
			msg = tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
		case "ctrl+k":
			msg = tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}
		}
		closed, _ = d.HandleKey(msg)
	}
	return closed
}

func TestThePublishConfirmSendsWhatThePersonReadAndNothingUnacknowledged(t *testing.T) {
	var sent []webapi.PublishRequest
	submit := func(r webapi.PublishRequest) tea.Cmd { sent = append(sent, r); return nil }
	f := domain.Feature{ID: "FD-001"}

	facts := webapi.PublishFacts{Act: "create", Fingerprint: "abc", Summary: "Push x and open a PR.", Title: "feat: x", Body: "why", Hook: ".git/hooks/pre-push"}
	d := newPublishDialog(f, facts, submit)
	// a pre-push hook runs with the person's credential: enter alone does
	// not start it
	if pressPublish(d, "enter") || len(sent) != 0 {
		t.Fatal("the confirm ran with an unacknowledged pre-push hook")
	}
	if !strings.Contains(d.View(m0Styles(), 80, 24), "pre-push") {
		t.Fatal("the confirm does not show the hook")
	}
	if !pressPublish(d, "ctrl+k", "ctrl+d", "enter") || len(sent) != 1 {
		t.Fatalf("acknowledged confirm sent %v", sent)
	}
	if got := sent[0]; got.Act != "create" || got.Fingerprint != "abc" || got.Title != "feat: x" || got.Body != "why" || !got.Draft {
		t.Fatalf("sent %+v", got)
	}

	// the floor's draft is not the person's to untick
	locked := webapi.PublishFacts{Act: "create", Fingerprint: "abc", Title: "t", Draft: true, DraftLocked: true, DraftWhy: "not verified"}
	d = newPublishDialog(f, locked, submit)
	pressPublish(d, "ctrl+d", "enter")
	if got := sent[len(sent)-1]; !got.Draft {
		t.Fatal("a locked draft was unticked")
	}

	// a push that returns a ready PR to draft says so in what it sends
	back := webapi.PublishFacts{Act: "push", Fingerprint: "abc", ToDraft: true}
	d = newPublishDialog(f, back, submit)
	pressPublish(d, "enter")
	if got := sent[len(sent)-1]; got.Act != "push" || !got.Draft || got.Title != "" {
		t.Fatalf("sent %+v", got)
	}

	n := len(sent)
	d = newPublishDialog(f, facts, submit)
	if !pressPublish(d, "esc") || len(sent) != n {
		t.Fatal("esc must close without publishing")
	}
}
