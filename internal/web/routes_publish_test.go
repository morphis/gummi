package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/publish"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/webapi"
)

const publishGHShim = `#!/bin/sh
echo "$*" >> "$GH_FAKE/log"
case "$1 $2" in
"auth status") exit 0 ;;
"repo view") cat "$GH_FAKE/repo.json" ;;
"pr list") echo '[]' ;;
"pr view") cat "$GH_FAKE/view.json" ;;
"pr create") cat > "$GH_FAKE/body"; echo "https://github.com/me/widget/pull/512" ;;
*) exit 0 ;;
esac
`

// publishBoard is a docs board that publishes: a fake gh that is signed in
// and a local bare repository standing in for github.com/me/widget.
func publishBoard(t *testing.T) (b *docsBoard, fake, bare string) {
	t.Helper()
	tmp := t.TempDir()
	fake, bare = filepath.Join(tmp, "gh"), filepath.Join(tmp, "remote.git")
	gh := filepath.Join(fake, "gh")
	writeFile(t, gh, publishGHShim)
	if err := os.Chmod(gh, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(fake, "repo.json"), `{"nameWithOwner":"me/widget","viewerPermission":"WRITE","isFork":false}`)
	t.Setenv("GH_FAKE", fake)
	t.Setenv("GUMMI_GH_CMD", gh)
	publish.RewriteAllowed = func(string) bool { return true }
	docsShellSetup = func(s *ui.Shell) { s.EnablePublishing(gh) }
	t.Cleanup(func() {
		publish.RewriteAllowed = func(string) bool { return false }
		docsShellSetup = nil
	})
	b = newDocsBoard(t, agent.NewFake("ok"))
	gitIn(t, tmp, "init", "-q", "--bare", bare)
	gitIn(t, b.root, "remote", "add", "origin", "git@github.com:me/widget.git")
	gitIn(t, b.root, "config", "url."+bare+".insteadOf", "git@github.com:me/widget.git")
	gitIn(t, b.root, "push", "-q", "origin", "main")

	// detection runs off the loop once the board starts
	deadline := time.Now().Add(10 * time.Second)
	for {
		var fx webapi.PublishFacts
		b.get("/api/cards/FD-001/publish?act=create", &fx)
		if fx.Error == nil || !strings.Contains(fx.Error.Text, "still checking") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("publishing was never detected")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return b, fake, bare
}

func TestPublishOpensADraftPRForAnUnverifiedCardAndRecordsIt(t *testing.T) {
	b, fake, bare := publishBoard(t)
	ctx := context.Background()
	tip := gitIn(t, b.wt, "rev-parse", "HEAD")

	var p webapi.PR
	b.get("/api/cards/FD-001/pr?refresh=1", &p)
	if p.Publish == nil || !p.Publish.Available || len(p.Publish.Acts) != 1 || p.Publish.Acts[0] != "create" {
		t.Fatalf("the PR tab offers %+v, want create", p.Publish)
	}

	var fx webapi.PublishFacts
	if st := b.get("/api/cards/FD-001/publish?act=create", &fx); st != http.StatusOK || fx.Error != nil {
		t.Fatalf("facts = %d %+v", st, fx.Error)
	}
	if fx.Tip != tip || fx.Head == "" || fx.BaseRepo != "me/widget" || fx.Fingerprint == "" || fx.Title == "" {
		t.Fatalf("facts = %+v", fx)
	}
	// the card is mid-implement: the floor, not the person, makes it a draft
	if !fx.Draft || !fx.DraftLocked || fx.DraftWhy == "" {
		t.Fatalf("an unverified card's PR must open as a locked draft: %+v", fx)
	}

	// a confirm that names no facts runs nothing
	var refused webapi.PublishResult
	b.send(http.MethodPost, "/api/cards/FD-001/publish", `{"act":"create","title":"x"}`, &refused)
	if refused.Error == nil || refused.Error.Code != string(publish.CodeConfirmationNeeded) {
		t.Fatalf("no fingerprint = %+v", refused)
	}

	body, _ := json.Marshal(webapi.PublishRequest{Act: "create", Fingerprint: fx.Fingerprint, Title: "feat: dark mode", Body: "why\n"})
	var res webapi.PublishResult
	if st := b.send(http.MethodPost, "/api/cards/FD-001/publish", string(body), &res); st != http.StatusOK {
		t.Fatalf("publish = %d", st)
	}
	if res.Number != 512 || !res.Draft || res.Pushed != tip || res.LinkError != "" {
		t.Fatalf("result = %+v", res)
	}
	if got := gitIn(t, b.root, "--git-dir", bare, "rev-parse", b.f.BranchName()); got != tip {
		t.Fatalf("the remote has %s, want %s", got, tip)
	}
	log, _ := os.ReadFile(filepath.Join(fake, "log"))
	if !strings.Contains(string(log), "pr create --repo me/widget") || !strings.Contains(string(log), "--draft") {
		t.Fatalf("gh was called as:\n%s", log)
	}
	f, err := b.store.GetFeature(ctx, "FD-001")
	if err != nil || f.PullRequest.Number != 512 || f.PullRequest.HeadSHA != tip {
		t.Fatalf("the card links %+v (%v)", f.PullRequest, err)
	}
	evs, err := b.store.Events(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	var rec *state.PublishPayload
	for _, ev := range evs {
		if ev.Kind == state.EventPublish {
			rec = &state.PublishPayload{}
			_ = json.Unmarshal([]byte(ev.Payload), rec)
		}
	}
	if rec == nil || rec.Act != "create" || rec.Number != 512 || !rec.Draft || rec.Pushed != tip || rec.By == "" {
		t.Fatalf("the thread records %+v", rec)
	}

	// the PR exists now, so the facts that were confirmed are stale: the
	// same confirm sent again publishes nothing
	writeFile(t, filepath.Join(fake, "view.json"), `{"number":512,"url":"https://github.com/me/widget/pull/512","state":"OPEN","isDraft":true,"headRefOid":"`+tip+`","headRefName":"`+b.f.BranchName()+`","headRepositoryOwner":{"login":"me"}}`)
	refused = webapi.PublishResult{}
	b.send(http.MethodPost, "/api/cards/FD-001/publish", string(body), &refused)
	if refused.Error == nil || refused.Error.Code != string(publish.CodeFactsChanged) {
		t.Fatalf("a stale confirm = %+v", refused)
	}
	// and readying it is refused by the floor, not left to the person
	var ready webapi.PublishFacts
	b.get("/api/cards/FD-001/publish?act=ready", &ready)
	if ready.Error == nil || ready.Error.Code != string(publish.CodeNotVerified) {
		t.Fatalf("ready on an unverified card = %+v", ready.Error)
	}
}

func TestPublishIsRefusedOnABoardThatDoesNotPublish(t *testing.T) {
	b := newDocsBoard(t, agent.NewFake("ok"))
	var fx webapi.PublishFacts
	if st := b.get("/api/cards/FD-001/publish?act=create", &fx); st != http.StatusOK || fx.Error == nil {
		t.Fatalf("facts = %d %+v", st, fx)
	}
	var refused webapi.PublishResult
	b.send(http.MethodPost, "/api/cards/FD-001/publish", `{"act":"create","fingerprint":"x","title":"t"}`, &refused)
	if refused.Error == nil || refused.Error.Code == "" {
		t.Fatalf("publish = %+v", refused)
	}
	var p webapi.PR
	b.get("/api/cards/FD-001/pr", &p)
	if p.Publish != nil {
		t.Fatalf("the PR tab offers publishing on a board without it: %+v", p.Publish)
	}
	var c webapi.Card
	b.get("/api/cards/FD-001", &c)
	for _, a := range c.Actions {
		if a.Needs == webapi.ActionNeedsPublish {
			t.Fatalf("the card offers %q on a board that does not publish", a.ID)
		}
	}
}
