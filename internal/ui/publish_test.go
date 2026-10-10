package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/publish"
	"github.com/morphis/gummi/internal/state"
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
		case "ctrl+t":
			msg = tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
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

const publishFlowGH = `#!/bin/sh
echo "$*" >> "$GH_FAKE/log"
test -f "$GH_FAKE/fail-$1-$2" && { cat "$GH_FAKE/fail-$1-$2" >&2; exit 1; }
case "$1 $2" in
"auth status") exit 0 ;;
"repo view") echo '{"nameWithOwner":"me/widget","viewerPermission":"WRITE","isFork":false}' ;;
"pr list") echo '[]' ;;
"pr view") cat "$GH_FAKE/view.json" ;;
"pr create") cat > "$GH_FAKE/body"; echo "https://github.com/me/widget/pull/512" ;;
*) exit 0 ;;
esac
`

// publishWorkspace is a board that publishes: one card two commits ahead of
// main on its own worktree, a fake gh that is signed in, and a local bare
// repository standing in for github.com/me/widget.
func publishWorkspace(t *testing.T) (m *Shell, fake, bare string) {
	t.Helper()
	m, root := newWorkspace(t)
	ctx := context.Background()
	fake = t.TempDir()
	bare = filepath.Join(fake, "remote.git")
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(fake, "init", "-q", "--bare", bare)
	git(root, "remote", "add", "origin", "git@github.com:me/widget.git")
	git(root, "config", "url."+bare+".insteadOf", "git@github.com:me/widget.git")
	f := &domain.Feature{
		ID: "FD-001", Num: 1, Title: "Dark mode", Slug: "dark-mode",
		Stage: domain.StageImplement, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if _, err := m.wt.Create(ctx, f); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(root, f.WorktreePath())
	git(tree, "commit", "-q", "--allow-empty", "-m", "feat: a dark palette")
	git(tree, "commit", "-q", "--allow-empty", "-m", "feat: a toggle for it")
	gh := filepath.Join(fake, "gh")
	if err := os.WriteFile(gh, []byte(publishFlowGH), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_FAKE", fake)
	publish.RewriteAllowed = func(string) bool { return true }
	t.Cleanup(func() { publish.RewriteAllowed = func(string) bool { return false } })
	m.EnablePublishing(gh)
	m.AttachCardLocks(state.NewCardLocks(m.ws))
	if m.publishOffered() {
		t.Fatal("publishing is offered before gh was looked for")
	}
	m = pump(t, m, m.Init())
	m = update(m, m.detectPublish())
	if !m.publishOffered() {
		t.Fatalf("a signed-in gh was not detected: %v", m.publishWhy)
	}
	return m, fake, bare
}

func publishDialogOn(t *testing.T, m *Shell) *publishDialog {
	t.Helper()
	d, ok := m.Overlay.Top().(*publishDialog)
	if !ok {
		t.Fatalf("no publish confirm is open (notice: %q)", m.notice.text)
	}
	return d
}

// The board's own path, end to end: the act reads the facts, the confirm
// shows them, and enter runs exactly that — push, PR, link, and a line in
// the card's thread.
// On a fork the PR can open in two repositories: the confirm does not run
// until the person has said which, and the yes carries their choice.
func TestThePublishConfirmAsksWhereAForksPROpens(t *testing.T) {
	var sent []webapi.PublishRequest
	var asked []string
	f := domain.Feature{ID: "FD-001"}
	open := func(facts webapi.PublishFacts, chosen string) *publishDialog {
		d := newPublishDialog(f, facts, func(r webapi.PublishRequest) tea.Cmd { sent = append(sent, r); return nil })
		d.baseRepo = chosen
		d.onRetarget = func(repo string) tea.Cmd { asked = append(asked, repo); return nil }
		return d
	}
	repos := []string{"me/widget", "acme/widget"}

	d := open(webapi.PublishFacts{Act: "create", Fingerprint: "abc", Title: "t", BaseRepos: repos}, "")
	if view := d.View(m0Styles(), 80, 24); !strings.Contains(view, "nowhere yet") || !strings.Contains(view, "me/widget or acme/widget") {
		t.Fatalf("the confirm does not ask where the PR opens:\n%s", view)
	}
	if pressPublish(d, "enter") || len(sent) != 0 {
		t.Fatal("the confirm ran with no target chosen")
	}
	// choosing reads the facts again for that repository
	if !pressPublish(d, "ctrl+t") || len(asked) != 1 || asked[0] != "me/widget" {
		t.Fatalf("ctrl+t asked for %v", asked)
	}

	d = open(webapi.PublishFacts{Act: "create", Fingerprint: "def", Title: "t", BaseRepo: "me/widget", BaseRepos: repos}, "me/widget")
	if view := d.View(m0Styles(), 80, 24); !strings.Contains(view, "opens in me/widget") || !strings.Contains(view, "ctrl+t for acme/widget") {
		t.Fatalf("the confirm does not show the target and the other one:\n%s", view)
	}
	if !pressPublish(d, "ctrl+t") || asked[len(asked)-1] != "acme/widget" {
		t.Fatalf("ctrl+t asked for %v", asked)
	}
	d = open(webapi.PublishFacts{Act: "create", Fingerprint: "def", Title: "t", BaseRepo: "me/widget", BaseRepos: repos}, "me/widget")
	if !pressPublish(d, "enter") || len(sent) != 1 || sent[0].BaseRepo != "me/widget" || sent[0].Fingerprint != "def" {
		t.Fatalf("sent %+v", sent)
	}
}

func TestTheBoardOpensAPullRequestFromItsConfirm(t *testing.T) {
	m, fake, bare := publishWorkspace(t)
	ctx := context.Background()
	r, ok := m.rowByID("FD-001")
	if !ok {
		t.Fatal("no row")
	}
	cmd := m.openPublish(r, publish.ActCreate, "")
	// the read is GitHub's to answer: the status bar spins on it until
	// the confirm opens
	if pill := m.ghWorkPill(); !strings.Contains(pill, "FD-001: reading where the branch goes") || !m.spinnerActive() {
		t.Fatalf("while the facts are read the bar says %q (spinning %v)", pill, m.spinnerActive())
	}
	m = update(m, cmd())
	if len(m.ghWork) != 0 {
		t.Fatalf("the wait outlived the read: %v", m.ghWork)
	}
	d := publishDialogOn(t, m)
	if d.ID() != "publish" {
		t.Fatalf("dialog id = %q", d.ID())
	}
	view := d.View(m0Styles(), 100, 40)
	// the card is mid-implement: the draft is the floor's, not a choice
	for _, want := range []string{"open pull request · FD-001", "and open a draft", "- feat: a dark palette", "[x] open as draft", "Dark mode"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the confirm does not show %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "git push --porcelain") {
		t.Fatal("the details are shown before tab")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if view = publishDialogOn(t, m).View(m0Styles(), 100, 40); !strings.Contains(view, "git push --porcelain origin") || !strings.Contains(view, "me:gummi/FD-001-dark-mode") {
		t.Fatalf("tab does not unfold the details:\n%s", view)
	}

	// a title pasted over an emptied field is what is sent; an empty one
	// is not sent at all
	d = publishDialogOn(t, m)
	d.title.SetValue("")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if d = publishDialogOn(t, m); d.problem != "a pull request needs a title" {
		t.Fatalf("an empty title: problem %q", d.problem)
	}
	if b, _ := os.ReadFile(filepath.Join(fake, "log")); strings.Contains(string(b), "pr create") {
		t.Fatal("a PR was opened with no title")
	}
	model, _ := m.Update(tea.PasteMsg{Content: "feat: dark mode"})
	m = model.(*Shell)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Overlay.HasDialogs() {
		t.Fatal("the confirm stayed open after enter")
	}
	if m.notice.isErr || !strings.Contains(m.notice.text, "opened PR #512 (draft) https://github.com/me/widget/pull/512") || !strings.Contains(m.notice.text, "FD-001: pushed ") {
		t.Fatalf("notice = %+v", m.notice)
	}
	if len(m.ghWork) != 0 {
		t.Fatalf("the wait outlived the act: %v", m.ghWork)
	}
	log, _ := os.ReadFile(filepath.Join(fake, "log"))
	if !strings.Contains(string(log), "--title=feat: dark mode --body-file - --draft") {
		t.Fatalf("gh was called as:\n%s", log)
	}
	if out, err := exec.Command("git", "--git-dir", bare, "rev-parse", "gummi/FD-001-dark-mode").CombinedOutput(); err != nil {
		t.Fatalf("the branch was not pushed: %s", out)
	}
	f, err := m.store.GetFeature(ctx, "FD-001")
	if err != nil || f.PullRequest.Number != 512 {
		t.Fatalf("the card links %+v (%v)", f.PullRequest, err)
	}
	evs, _ := m.store.Events(ctx, "FD-001")
	recorded := false
	for _, ev := range evs {
		recorded = recorded || ev.Kind == state.EventPublish
	}
	if !recorded {
		t.Fatal("the thread has no line for the publish")
	}

	// the reloaded row carries the link, so the menu now offers the PR's
	// own acts; readying a draft the floor holds is refused in words
	r, _ = m.rowByID("FD-001")
	if r.F.PullRequest.Number != 512 {
		t.Fatalf("the board's row was not reloaded: %+v", r.F.PullRequest)
	}
	tip, _ := exec.Command("git", "--git-dir", bare, "rev-parse", "gummi/FD-001-dark-mode").Output()
	view512 := `{"number":512,"url":"https://github.com/me/widget/pull/512","state":"OPEN","isDraft":true,"headRefOid":"` + strings.TrimSpace(string(tip)) + `","headRefName":"gummi/FD-001-dark-mode","headRepositoryOwner":{"login":"me"}}`
	if err := os.WriteFile(filepath.Join(fake, "view.json"), []byte(view512), 0o600); err != nil {
		t.Fatal(err)
	}
	m = update(m, m.openPublish(r, publish.ActReady, "")())
	if m.Overlay.HasDialogs() || !m.notice.isErr || !strings.Contains(m.notice.text, "FD-001: ") || !strings.Contains(m.notice.text, "verif") {
		t.Fatalf("ready on an unverified card: dialogs %v, notice %+v", m.Overlay.HasDialogs(), m.notice)
	}
	// nothing new to push either
	m = update(m, m.openPublish(r, publish.ActPush, "")())
	if m.Overlay.HasDialogs() || !m.notice.isErr || !strings.Contains(m.notice.text, "already has") {
		t.Fatalf("a push with nothing new: %+v", m.notice)
	}
}

// An act that fails after the confirm says so on the board, with what
// already reached GitHub, and leaves the card unlinked.
func TestTheBoardSaysWhatAFailedPublishAlreadyDid(t *testing.T) {
	m, fake, bare := publishWorkspace(t)
	r, _ := m.rowByID("FD-001")
	if err := os.WriteFile(filepath.Join(fake, "fail-pr-create"), []byte("HTTP 502: Bad Gateway"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = update(m, m.openPublish(r, publish.ActCreate, "")())
	publishDialogOn(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.notice.isErr || !strings.Contains(m.notice.text, "HTTP 502") || !strings.Contains(m.notice.text, "was pushed to me/widget") {
		t.Fatalf("notice = %+v", m.notice)
	}
	if out, err := exec.Command("git", "--git-dir", bare, "rev-parse", "gummi/FD-001-dark-mode").CombinedOutput(); err != nil {
		t.Fatalf("the branch was not pushed: %s", out)
	}
	if f, _ := m.store.GetFeature(context.Background(), "FD-001"); !f.PullRequest.Empty() {
		t.Fatalf("a PR that was not opened is linked: %+v", f.PullRequest)
	}

	// a card another process holds is not published from here
	if err := os.Remove(filepath.Join(fake, "fail-pr-create")); err != nil {
		t.Fatal(err)
	}
	r, _ = m.rowByID("FD-001")
	m = update(m, m.openPublish(r, publish.ActCreate, "")())
	publishDialogOn(t, m)
	release, err := state.AcquireLock(m.ws.CardLockFile("FD-001"))
	if err != nil {
		t.Fatal(err)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	release()
	if !m.notice.isErr {
		t.Fatalf("a held card was published: %+v", m.notice)
	}
	if b, _ := os.ReadFile(filepath.Join(fake, "log")); strings.Count(string(b), "pr create") != 1 {
		t.Fatalf("gh pr create ran on a held card:\n%s", b)
	}
}

func TestPublishNoticesReadTheSameForEveryAct(t *testing.T) {
	for want, r := range map[string]webapi.PublishResult{
		"FD-001: pushed abc1234":                                        {Act: "push", Pushed: "abc1234"},
		"FD-001: pushed abc1234 · PR #7 returned to draft":              {Act: "push", Pushed: "abc1234", Number: 7, ToDraft: true},
		"FD-001: opened PR #7 (ready) https://x/7":                      {Act: "create", Number: 7, URL: "https://x/7"},
		"FD-001: pushed abc1234 · updated PR #7":                        {Act: "update", Pushed: "abc1234", Number: 7},
		"FD-001: PR #7 is ready for review":                             {Act: "ready", Number: 7},
		"FD-001: PR #7 is a draft again":                                {Act: "draft", Number: 7},
		"FD-001: opened PR #7 (draft) https://x/7\nwarning: not linked": {Act: "create", Number: 7, URL: "https://x/7", Draft: true, LinkError: "not linked"},
	} {
		if got := publishResultText("FD-001", r); got != want {
			t.Errorf("%+v reads %q, want %q", r, got, want)
		}
	}
	if got := publishErrorText("FD-001", &webapi.PublishError{Text: "uncommitted work", Fix: "commit it"}); got != "FD-001: uncommitted work — commit it" {
		t.Errorf("an error reads %q", got)
	}
	if _, ok := webPublishAct("merge"); ok {
		t.Error("an act nobody defined was read off the wire")
	}
}

// The bar names the step an act is on and how far through its plan that
// is; a step read after the act's last word is not drawn over the outcome.
func TestTheBarNamesThePublishStepInFlight(t *testing.T) {
	m, _, _ := publishWorkspace(t)
	said := make(chan publishStepMsg, 1)
	step := publishStepMsg{id: "FD-001", step: "push", text: "Push 0123abc to me/widget", n: 2, of: 3, next: said}

	m.markGHWork("FD-001", "publishing", "")
	if cmd := m.handlePublishStep(step); cmd == nil {
		t.Fatal("the next step is not waited for")
	}
	if pill := m.ghWorkPill(); !strings.Contains(pill, "FD-001: Push 0123abc to me/widget (2/3)") {
		t.Fatalf("the bar says %q", pill)
	}

	model, _ := m.Update(ghDoneMsg{id: "FD-001", inner: noticeMsg{text: "FD-001: pushed 0123abc"}})
	m = model.(*Shell)
	if len(m.ghWork) != 0 || m.notice.text != "FD-001: pushed 0123abc" {
		t.Fatalf("after the act: work %v, notice %q", m.ghWork, m.notice.text)
	}
	m.handlePublishStep(step)
	if len(m.ghWork) != 0 {
		t.Fatalf("a late step reopened the wait: %v", m.ghWork)
	}
}
