package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// discoverFixture puts a feature at the plan stage with a worktree and a
// blank spec, plus an agent whose reply is fixed and whose sessions are
// counted.
func discoverFixture(t *testing.T, reply string) (*Engine, domain.Feature, string, *int32) {
	t.Helper()
	ws, store, wt := newRepo(t)
	var sessions int32
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, _ string) []agent.Event {
		atomic.AddInt32(&sessions, 1)
		return []agent.Event{{Kind: agent.EventMessage, Text: reply}, {Kind: agent.EventIdle}}
	}}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "discover me", domain.StagePlan)
	withWorktree(t, wt, f)
	p := filepath.Join(wt.Root(), f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(spec.Template(&f)), 0o600); err != nil {
		t.Fatal(err)
	}
	return e, f, p, &sessions
}

func TestDiscoverChecksWritesBlock(t *testing.T) {
	reply := "Here you go:\n```gummi-checks\n- name: test\n  cmd: go test ./...\n```"
	e, f, p, _ := discoverFixture(t, reply)

	checks, err := e.DiscoverChecks(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Cmd != "go test ./..." {
		t.Fatalf("checks = %+v", checks)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got, found, _ := spec.ParseChecks(string(raw))
	if !found || len(got) != 1 || got[0].Cmd != "go test ./..." {
		t.Fatalf("spec block = %+v (found=%v):\n%s", got, found, raw)
	}
	// the block sits in the Verification plan section
	if !strings.Contains(string(raw), "## Verification plan\n\n```gummi-checks") {
		t.Errorf("block not under the Verification section:\n%s", raw)
	}
}

func TestDiscoverChecksSkipsWhenBlockExists(t *testing.T) {
	reply := "```gummi-checks\n- name: test\n  cmd: go test ./...\n```"
	e, f, p, sessions := discoverFixture(t, reply)

	// hand-authored block: discovery must not spawn a session or clobber it
	raw, _ := os.ReadFile(p)
	out, err := spec.UpsertChecks(string(raw), []domain.Check{{Name: "mine", Cmd: "make check"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}

	checks, err := e.DiscoverChecks(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if checks != nil {
		t.Errorf("discovery on an existing block returned %+v", checks)
	}
	if n := atomic.LoadInt32(sessions); n != 0 {
		t.Errorf("discovery spawned %d session(s) despite an existing block", n)
	}
	got, _, _ := spec.ParseChecks(readFile(t, p))
	if len(got) != 1 || got[0].Name != "mine" {
		t.Errorf("hand-authored block clobbered: %+v", got)
	}
}

func TestDiscoverChecksUnusableReplyIsSoft(t *testing.T) {
	e, f, p, _ := discoverFixture(t, "I could not find anything conclusive.")
	checks, err := e.DiscoverChecks(context.Background(), f)
	if err != nil || checks != nil {
		t.Fatalf("unusable reply should be (nil, nil), got (%+v, %v)", checks, err)
	}
	if _, found, _ := spec.ParseChecks(readFile(t, p)); found {
		t.Error("unusable reply still wrote a block")
	}
}

func TestDiscoverChecksCarriesArtifactPath(t *testing.T) {
	for _, f := range []domain.Feature{
		feature(1, "discover spec", domain.StagePlan),
		bugFeature("discover bug"),
	} {
		t.Run(string(f.ID), func(t *testing.T) {
			ws, store, wt := newRepo(t)
			rec := recordingAgent()
			e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
			t.Cleanup(func() { e.Close() })

			withWorktree(t, wt, f)
			p := filepath.Join(wt.Root(), f.ArtifactPath())
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(spec.Template(&f)), 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := e.DiscoverChecks(context.Background(), f); err != nil {
				t.Fatal(err)
			}
			if rec.opts().ArtifactPath != p {
				t.Errorf("ArtifactPath = %s, want promoted artifact %s", rec.opts().ArtifactPath, p)
			}
			if rec.opts().WorkDir == rec.opts().ArtifactPath {
				t.Errorf("ArtifactPath %s must not be the worktree workdir", rec.opts().ArtifactPath)
			}
		})
	}
}

func TestDiscoverChecksPassesSpecPathAsExtraRead(t *testing.T) {
	for _, f := range []domain.Feature{
		feature(1, "discover spec", domain.StagePlan),
		bugFeature("discover bug"),
	} {
		t.Run(string(f.ID), func(t *testing.T) {
			ws, store, wt := newRepo(t)
			rec := recordingAgent()
			e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
			t.Cleanup(func() { e.Close() })

			withWorktree(t, wt, f)
			p := filepath.Join(wt.Root(), f.ArtifactPath())
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(spec.Template(&f)), 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := e.DiscoverChecks(context.Background(), f); err != nil {
				t.Fatal(err)
			}
			if got := rec.opts().ExtraReadAllows; !reflect.DeepEqual(got, []string{p}) {
				t.Errorf("ExtraReadAllows = %v, want [%s]", got, p)
			}
		})
	}
}

func TestDiscoverChecksCarriesEnvironmentCard(t *testing.T) {
	ws, store, wt := newRepo(t)
	writeEnvironmentCard(t, ws.Root, testCard)

	var gotMsg string
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		gotMsg = msg
		return []agent.Event{{Kind: agent.EventMessage, Text: "```gummi-checks\n- name: test\n  cmd: go test ./...\n```"}, {Kind: agent.EventIdle}}
	}}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "discover env", domain.StagePlan)
	withWorktree(t, wt, f)
	p := filepath.Join(wt.Root(), f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(spec.Template(&f)), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := e.DiscoverChecks(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotMsg, testCard) {
		t.Errorf("discovery prompt missing environment card:\n%s", gotMsg)
	}
	if !strings.Contains(gotMsg, containerLocalRule) {
		t.Errorf("discovery prompt missing container-local rule:\n%s", gotMsg)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
