package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/envprobe"
)

// writeBugSpec drops a bug report into the workspace home for the card with
// the given Verification section body.
func writeBugSpec(t *testing.T, wt worktreeProvider, f domain.Feature, verificationBody string) {
	t.Helper()
	p := filepath.Join(wt.Root(), f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	content := "# " + string(f.ID) + "\n\n## Verification\n\n" + verificationBody + "\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// worktreeProvider exposes the worktree root for test helpers.
type worktreeProvider interface {
	Root() string
}

func TestVerifyFinishOmissionGateArmsForBug(t *testing.T) {
	ws, store, wt := newRepo(t)
	if err := os.WriteFile(ws.ConfigFile(), []byte("env:\n  docker:\n    probe: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ag := toolCallFake("submit_verdict", json.RawMessage(`{"verdict":"pass"}`))
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })

	f := bugFeature("omission gate arms")
	f.Stage = domain.StageVerify
	withWorktree(t, wt, f)
	writeBugSpec(t, wt, f, "Run local unit tests only.")

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateDone)
	waitFor(t, e, EventIdle)

	snap := e.Get(f.ID).Snapshot()
	if snap.VerdictFloor != "blocked" {
		t.Errorf("VerdictFloor = %q, want blocked", snap.VerdictFloor)
	}
	if snap.VerdictFloorReason == "" {
		t.Error("VerdictFloorReason empty, want a reason")
	}
	if snap.Verdict != "pass" {
		t.Errorf("raw Verdict = %q, want pass", snap.Verdict)
	}
	found := false
	for _, line := range snap.Activity {
		if strings.Contains(line, "downgraded to blocked") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("missing downgrade activity line; activity = %v", snap.Activity)
	}
}

func TestVerifyFinishOmissionGateDoesNotArmForFeature(t *testing.T) {
	ws, store, wt := newRepo(t)
	if err := os.WriteFile(ws.ConfigFile(), []byte("env:\n  docker:\n    probe: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ag := toolCallFake("submit_verdict", json.RawMessage(`{"verdict":"pass"}`))
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "feature no gate", domain.StageVerify)
	withWorktree(t, wt, f)
	writeSpecChecks(t, wt, f, "- name: x\n  cmd: echo ok\n")

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateDone)
	waitFor(t, e, EventIdle)

	snap := e.Get(f.ID).Snapshot()
	if snap.VerdictFloor != "" {
		t.Errorf("VerdictFloor = %q, want empty for feature", snap.VerdictFloor)
	}
	if snap.Verdict != "pass" {
		t.Errorf("raw Verdict = %q, want pass", snap.Verdict)
	}
}

func TestVerifyFinishOmissionGateDisarmedByAbsentProbe(t *testing.T) {
	ws, store, wt := newRepo(t)
	if err := os.WriteFile(ws.ConfigFile(), []byte("env:\n  docker:\n    probe: \"false\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ag := toolCallFake("submit_verdict", json.RawMessage(`{"verdict":"pass"}`))
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })

	f := bugFeature("absent probe no gate")
	f.Stage = domain.StageVerify
	withWorktree(t, wt, f)
	writeBugSpec(t, wt, f, "Run local unit tests only.")

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateDone)
	waitFor(t, e, EventIdle)

	snap := e.Get(f.ID).Snapshot()
	if snap.VerdictFloor != "" {
		t.Errorf("VerdictFloor = %q, want empty when probe absent", snap.VerdictFloor)
	}
	if snap.Verdict != "pass" {
		t.Errorf("raw Verdict = %q, want pass", snap.Verdict)
	}
}

func TestVerifyFinishOmissionGateDisarmedByEnvTag(t *testing.T) {
	ws, store, wt := newRepo(t)
	if err := os.WriteFile(ws.ConfigFile(), []byte("env:\n  docker:\n    probe: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ag := toolCallFake("submit_verdict", json.RawMessage(`{"verdict":"pass"}`))
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })

	f := bugFeature("env tag disarms")
	f.Stage = domain.StageVerify
	withWorktree(t, wt, f)
	writeBugSpec(t, wt, f, "Run the docker check [env: docker].")

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateDone)
	waitFor(t, e, EventIdle)

	snap := e.Get(f.ID).Snapshot()
	if snap.VerdictFloor != "" {
		t.Errorf("VerdictFloor = %q, want empty when env tag present", snap.VerdictFloor)
	}
	if snap.Verdict != "pass" {
		t.Errorf("raw Verdict = %q, want pass", snap.Verdict)
	}
}

func TestVerifyFinishOmissionGateDisarmedByWaiver(t *testing.T) {
	ws, store, wt := newRepo(t)
	if err := os.WriteFile(ws.ConfigFile(), []byte("env:\n  docker:\n    probe: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ag := toolCallFake("submit_verdict", json.RawMessage(`{"verdict":"pass"}`))
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })

	f := bugFeature("waiver disarms")
	f.Stage = domain.StageVerify
	withWorktree(t, wt, f)
	writeBugSpec(t, wt, f, "%% @user: no-live-check docker unavailable in this sandbox\n\nRun local unit tests only.")

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateDone)
	waitFor(t, e, EventIdle)

	snap := e.Get(f.ID).Snapshot()
	if snap.VerdictFloor != "" {
		t.Errorf("VerdictFloor = %q, want empty when waiver present", snap.VerdictFloor)
	}
	if snap.Verdict != "pass" {
		t.Errorf("raw Verdict = %q, want pass", snap.Verdict)
	}
}

func TestVerifyFinishOmissionGateSkippedOnSpecReadError(t *testing.T) {
	ws, store, wt := newRepo(t)
	if err := os.WriteFile(ws.ConfigFile(), []byte("env:\n  docker:\n    probe: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ag := toolCallFake("submit_verdict", json.RawMessage(`{"verdict":"pass"}`))
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })

	f := bugFeature("spec unreadable no gate")
	f.Stage = domain.StageVerify
	withWorktree(t, wt, f)
	// Place a directory at the artifact path so os.ReadFile returns an
	// error, exercising the fail-closed skip path.
	p := filepath.Join(wt.Root(), f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateDone)
	waitFor(t, e, EventIdle)

	snap := e.Get(f.ID).Snapshot()
	if snap.VerdictFloor != "" {
		t.Errorf("VerdictFloor = %q, want empty when spec unreadable", snap.VerdictFloor)
	}
	if snap.Verdict != "pass" {
		t.Errorf("raw Verdict = %q, want pass", snap.Verdict)
	}
	found := false
	for _, line := range snap.Activity {
		if strings.Contains(line, "Omission gate skipped") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("missing skipped-gate activity line; activity = %v", snap.Activity)
	}
}

func TestVerifyKickoffAnnouncesArmedOmissionGate(t *testing.T) {
	cases := []struct {
		name          string
		kind          domain.Kind
		probePresent  bool
		probeErr      bool
		wantAnnounced bool
	}{
		{
			name:          "bug with clean present probe announces gate",
			kind:          domain.KindBug,
			probePresent:  true,
			wantAnnounced: true,
		},
		{
			name:          "feature with clean present probe does not announce",
			kind:          domain.KindFeature,
			probePresent:  true,
			wantAnnounced: false,
		},
		{
			name:          "bug with absent or errored probe does not announce",
			kind:          domain.KindBug,
			probePresent:  false,
			wantAnnounced: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := "true"
			if !tc.probePresent {
				probe = "false"
			}
			ws, store, wt := newRepo(t)
			if err := os.WriteFile(ws.ConfigFile(), []byte("env:\n  docker:\n    probe: \""+probe+"\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			var mu sync.Mutex
			var got string
			f := agent.NewFake("")
			f.Caps = agent.Capabilities{ClientTools: true, Interrupt: true}
			first := true
			f.Responder = func(_ agent.SessionOpts, msg string) []agent.Event {
				mu.Lock()
				got = msg
				mu.Unlock()
				if first {
					first = false
					return []agent.Event{
						{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "c1", Name: "submit_verdict", Args: json.RawMessage(`{"verdict":"pass"}`)}},
						{Kind: agent.EventIdle},
					}
				}
				return []agent.Event{{Kind: agent.EventIdle}}
			}

			e := New(Config{Agents: singleAgent(f), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll})
			t.Cleanup(func() { e.Close() })

			var feat domain.Feature
			if tc.kind == domain.KindBug {
				feat = bugFeature("kickoff gate kind probe")
			} else {
				feat = feature(1, "kickoff gate kind probe", domain.StageVerify)
				feat.Kind = domain.KindFeature
			}
			feat.Stage = domain.StageVerify
			withWorktree(t, wt, feat)
			writeBugSpec(t, wt, feat, "Run local unit tests only.")

			if err := e.Run(feat); err != nil {
				t.Fatal(err)
			}
			waitState(t, e, feat.ID, StateDone)

			mu.Lock()
			defer mu.Unlock()
			announced := strings.Contains(got, "Omission gate") && strings.Contains(got, "downgraded to blocked")
			if announced != tc.wantAnnounced {
				t.Errorf("announced = %v, want %v; kickoff=%q", announced, tc.wantAnnounced, got)
			}
		})
	}
}

func TestOmissionGateReason(t *testing.T) {
	cases := []struct {
		name              string
		kind              domain.Kind
		cleanPresentProbe bool
		content           string
		blocked           bool
	}{
		{
			name:              "bug clean present no tags no waiver",
			kind:              domain.KindBug,
			cleanPresentProbe: true,
			content:           "# BG-002\n\n## Verification\n\nRun local unit tests only.\n",
			blocked:           true,
		},
		{
			name:              "bug clean present with env tag",
			kind:              domain.KindBug,
			cleanPresentProbe: true,
			content:           "# BG-002\n\n## Verification\n\nRun the docker check [env: docker].\n",
			blocked:           false,
		},
		{
			name:              "bug clean present with waiver",
			kind:              domain.KindBug,
			cleanPresentProbe: true,
			content:           "# BG-002\n\n## Verification\n\n%% @user: no-live-check docker unavailable in this sandbox\n\nRun local unit tests only.\n",
			blocked:           false,
		},
		{
			name:              "feature clean present no tags no waiver",
			kind:              domain.KindFeature,
			cleanPresentProbe: true,
			content:           "# FD-001\n\n## Verification plan\n\nRun local unit tests only.\n",
			blocked:           false,
		},
		{
			name:              "bug no clean present probe",
			kind:              domain.KindBug,
			cleanPresentProbe: false,
			content:           "# BG-002\n\n## Verification\n\nRun local unit tests only.\n",
			blocked:           false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := omissionGateReason(tc.kind, tc.cleanPresentProbe, tc.content)
			if tc.blocked {
				if reason == "" {
					t.Fatal("expected blocked reason, got empty")
				}
			} else {
				if reason != "" {
					t.Fatalf("expected not blocked, got reason: %s", reason)
				}
			}
		})
	}
}

func TestGateVerifyVerdictDirectArming(t *testing.T) {
	ws, store, wt := newRepo(t)
	if err := os.WriteFile(ws.ConfigFile(), []byte("env:\n  docker:\n    probe: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	e := New(Config{Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := bugFeature("direct gate test")
	f.Stage = domain.StageVerify
	withWorktree(t, wt, f)
	writeBugSpec(t, wt, f, "Run local unit tests only.")

	s := &Session{Feature: f}
	s.setSpecPath(filepath.Join(wt.Root(), f.ArtifactPath()))
	s.envProbes = []envprobe.Result{{Name: "docker", Present: true}}
	e.gateVerifyVerdict(s)

	snap := s.Snapshot()
	if snap.VerdictFloor != "blocked" {
		t.Errorf("VerdictFloor = %q, want blocked", snap.VerdictFloor)
	}
}
