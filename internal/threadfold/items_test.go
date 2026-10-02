package threadfold

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// cardLog builds a card's event log one row at a time, in the payload
// shapes the engine writes (persist.go's mirrorEvents, the ask tool, the
// gate and park writers), numbering rows the way Store.Events does.
type cardLog struct {
	evs []state.CardEvent
	at  time.Time
}

func newLog() *cardLog { return &cardLog{at: time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)} }

func (l *cardLog) add(stage domain.Stage, kind, status string, payload any, output string) *cardLog {
	l.at = l.at.Add(time.Minute)
	raw, _ := json.Marshal(payload)
	l.evs = append(l.evs, state.CardEvent{
		Seq: int64(len(l.evs) + 1), Feature: "FD-001", Stage: stage, Kind: kind,
		Status: status, At: l.at, Payload: string(raw), Output: output,
	})
	return l
}

func (l *cardLog) enter(stage domain.Stage, role, flavor string) *cardLog {
	return l.add(stage, state.EventStageEnter, "", StageEnterPayload{Role: role, Model: "m-1", Flavor: flavor}, "")
}

func (l *cardLog) exit(stage domain.Stage, verdict string, credits float64) *cardLog {
	return l.add(stage, state.EventStageExit, "", StageExitPayload{Verdict: verdict, Credits: credits}, "")
}

func (l *cardLog) say(stage domain.Stage, author, content string) *cardLog {
	return l.add(stage, state.EventMessage, "", MessagePayload{Author: author, Content: content}, "")
}

func (l *cardLog) tool(stage domain.Stage, label, status, output string) *cardLog {
	return l.add(stage, state.EventTool, status, state.ToolPayload{Label: label}, output)
}

func (l *cardLog) decision(stage domain.Stage, id, kind, q string) *cardLog {
	return l.add(stage, state.EventDecisionOpen, "", state.DecisionPayload{ID: id, Kind: kind, Question: q}, "")
}

func (l *cardLog) gate(stage domain.Stage, from, to domain.Stage, actor, id string) *cardLog {
	return l.add(stage, state.EventGate, "", state.GatePayload{From: string(from), To: string(to), Actor: actor, ID: id}, "")
}

func (l *cardLog) ask(stage domain.Stage, id, q, answer, by string) *cardLog {
	return l.add(stage, state.EventAsk, "", state.AskPayload{Question: q, Answer: answer, Actor: by, By: by, ID: id}, "")
}

func (l *cardLog) autopilot(stage domain.Stage, event, mode, reason string) *cardLog {
	return l.add(stage, state.EventAutopilot, "", state.AutopilotPayload{Event: event, Mode: mode, Reason: reason}, "")
}

func (l *cardLog) park(stage domain.Stage, reason, detail string) *cardLog {
	return l.add(stage, state.EventPark, "", state.ParkPayload{Reason: reason, Detail: detail}, "")
}

// brief says an item in one line, so a scenario's whole thread reads as
// a list a person can check against the TUI's own rendering of it.
func brief(it Item) string {
	switch it.T {
	case ItemStage:
		return fmt.Sprintf("stage %s · %s · %s · exited=%v verdict=%q credits=%g mark=%q",
			it.Stage, it.Role, it.Flavor, it.Exited, it.Verdict, it.Credits, it.Outcome)
	case ItemMessage:
		return "message " + it.Author + ": " + it.Text
	case ItemYou:
		return "you (" + it.Via + "): " + it.Text
	case ItemTools:
		var calls []string
		for _, c := range it.Tools {
			calls = append(calls, c.Tool+"|"+c.Detail+"|"+c.Status)
		}
		return "tools " + strings.Join(calls, ", ")
	case ItemReceipt:
		return fmt.Sprintf("receipt %s ok=%v by=%q: %s", it.Receipt.Kind, it.Receipt.OK, it.Receipt.By, it.Receipt.Text)
	case ItemVerify:
		var cs []string
		for _, c := range it.Checks {
			cs = append(cs, fmt.Sprintf("%s=%s ok=%v out=%q", c.Name, c.Status, c.OK, c.Output))
		}
		return fmt.Sprintf("verify verdict=%q %s", it.Verdict, strings.Join(cs, "; "))
	case ItemDecision:
		return "decision " + it.Decision.Kind + " " + it.Decision.ID + ": " + it.Decision.Question
	case ItemStretch:
		m := it.Stretch
		if m.Edge == "open" {
			return "── " + m.Label
		}
		return fmt.Sprintf("── %s · reason=%q tally=%q", m.Label, m.Reason, m.Tally)
	default:
		return string(it.T) + " " + it.Text
	}
}

func TestItems(t *testing.T) {
	plan, impl, verify := domain.StagePlan, domain.StageImplement, domain.StageVerify
	tests := []struct {
		name string
		log  *cardLog
		opt  Options
		want []string
	}{
		{
			name: "a plan, its critique pass and the gate a person crossed",
			log: newLog().
				enter(plan, "architect", "stage").
				say(plan, "system", "Design this card.").
				say(plan, "assistant", "The repro holds: `DocPath` is empty.").
				add(plan, state.EventTool, state.StatusOK, state.ToolPayload{Label: "Read  internal/domain/delete.go", Tool: "Read", Detail: "internal/domain/delete.go", MS: 12}, "").
				tool(plan, "Bash  go test ./internal/domain", state.StatusFail, "--- FAIL: TestDelete").
				exit(plan, "", 12.04).
				enter(plan, "reviewer", "critique").
				say(plan, "assistant", "Two findings, both addressed.").
				exit(plan, "pass", 3).
				decision(plan, "g1", state.DecisionKindGate, "Approve the design?").
				gate(plan, plan, impl, "user", "g1"),
			want: []string{
				`stage plan · architect · stage · exited=true verdict="" credits=12 mark="ok"`,
				"message gummi: Design this card.",
				"message architect: The repro holds: `DocPath` is empty.",
				`tools Read|internal/domain/delete.go|ok, Bash|go test ./internal/domain|fail`,
				`stage plan · reviewer · critique · exited=true verdict="pass" credits=3 mark="ok"`,
				"message reviewer: Two findings, both addressed.",
				`receipt gate ok=true by="you": you advanced plan → implement`,
			},
		},
		{
			name: "an ask answered, its echo, and a steer",
			log: newLog().
				enter(impl, "implementer", "stage").
				decision(impl, "a1", state.DecisionKindAsk, "Persist where?").
				ask(impl, "a1", "Persist where?", "per-device", state.ActorUser).
				say(impl, "user", "per-device").
				say(impl, "user", "also add a regression test").
				say(impl, "assistant", "Done."),
			want: []string{
				`stage implement · implementer · stage · exited=false verdict="" credits=0 mark=""`,
				`receipt ask ok=true by="you": you answered “Persist where?” — per-device`,
				"you (answer): per-device",
				"you (steered): also add a regression test",
				"message implementer: Done.",
			},
		},
		{
			name: "a failed verify with its output, waiting on a person",
			log: newLog().
				enter(verify, "reviewer", "stage").
				tool(verify, "check build: pass", state.StatusOK, "ok  all built").
				tool(verify, "check test: FAIL (exit 1)", state.StatusFail, "--- FAIL: TestX\n\x1b[31mboom\x1b[0m").
				say(verify, "assistant", "verdict: fail").
				exit(verify, "fail", 4).
				decision(verify, "v1", state.DecisionKindVerify, "Verify failed on 1 of 2 checks."),
			want: []string{
				`stage verify · reviewer · stage · exited=true verdict="fail" credits=4 mark="fail"`,
				`verify verdict="fail" build=pass ok=true out=""; test=FAIL (exit 1) ok=false out="--- FAIL: TestX\nboom"`,
				"message reviewer: verdict: fail",
				"decision verify v1: Verify failed on 1 of 2 checks.",
			},
		},
		{
			name: "an autopilot stretch that parked",
			log: newLog().
				enter(plan, "architect", "stage").
				autopilot(plan, state.AutopilotTookOver, "autopilot", "").
				say(plan, "assistant", "Plan written.").
				gate(plan, plan, impl, "autopilot", "").
				enter(impl, "implementer", "stage").
				ask(impl, "a2", "Which rig?", "rig-a", state.ActorAutopilot).
				park(impl, state.ParkReasonNeedsYou, "implement finished, review it"),
			opt: Options{Live: true},
			want: []string{
				`stage plan · architect · stage · exited=false verdict="" credits=0 mark=""`,
				"── autopilot took over",
				"message architect: Plan written.",
				`receipt gate ok=true by="autopilot": autopilot crossed plan → implement`,
				`stage implement · implementer · stage · exited=false verdict="" credits=0 mark=""`,
				`receipt ask ok=true by="autopilot": autopilot answered “Which rig?” — rig-a`,
				`── autopilot parked it · reason="implement finished, review it" tally="1 gate · 1 answer"`,
			},
		},
		{
			name: "a steer takes the card back from autopilot",
			log: newLog().
				enter(impl, "implementer", "stage").
				autopilot(impl, state.AutopilotTookOver, "autopilot", "").
				say(impl, "assistant", "Working.").
				say(impl, "user", "stop, use the other API").
				autopilot(impl, "", "attended", ""),
			opt: Options{Live: true},
			want: []string{
				`stage implement · implementer · stage · exited=false verdict="" credits=0 mark=""`,
				"── autopilot took over",
				"message implementer: Working.",
				`── you took back control · reason="" tally=""`,
				"you (steered): stop, use the other API",
				`receipt autopilot ok=true by="": autopilot set to off`,
			},
		},
		{
			name: "a period whose driver died without a word",
			log: newLog().
				enter(impl, "implementer", "stage").
				autopilot(impl, state.AutopilotTookOver, "autopilot", "").
				say(impl, "assistant", "Working."),
			opt: Options{Live: false},
			want: []string{
				`stage implement · implementer · stage · exited=false verdict="" credits=0 mark=""`,
				"── autopilot took over",
				"message implementer: Working.",
				`── autopilot stopped without saying so · reason="" tally=""`,
			},
		},
		{
			name: "a decision superseded before anyone answered it",
			log: newLog().
				enter(impl, "implementer", "stage").
				decision(impl, "b1", state.DecisionKindBudget, "Out of credits.").
				decision(impl, "g2", state.DecisionKindGate, "Review the implementation?"),
			want: []string{
				`stage implement · implementer · stage · exited=false verdict="" credits=0 mark=""`,
				`receipt decision ok=false by="": Out of credits. — unanswered, superseded`,
				"decision gate g2: Review the implementation?",
			},
		},
		{
			name: "an outstanding Monitor watch reads as watching, not unknown",
			log: newLog().
				enter(impl, "implementer", "stage").
				add(impl, state.EventTool, "", state.ToolPayload{Label: "Monitor  tail -f build.log", Tool: "Monitor", Detail: "tail -f build.log"}, "").
				add(impl, state.EventTool, state.StatusOK, state.ToolPayload{Label: "Read  internal/domain/delete.go", Tool: "Read", Detail: "internal/domain/delete.go"}, ""),
			want: []string{
				`stage implement · implementer · stage · exited=false verdict="" credits=0 mark=""`,
				`tools Monitor|tail -f build.log|watching, Read|internal/domain/delete.go|ok`,
			},
		},
		{
			name: "a stage that never opened a session still gets a divider",
			log: newLog().
				enter(plan, "architect", "stage").
				exit(plan, "", 5).
				park(impl, state.ParkReasonNeedsYou, "the backend cannot run implement"),
			want: []string{
				`stage plan · architect · stage · exited=true verdict="" credits=5 mark="ok"`,
				`stage implement ·  ·  · exited=false verdict="" credits=0 mark=""`,
				`receipt park ok=false by="": parked — the backend cannot run implement`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := Items(tt.log.evs, tt.opt)
			var got []string
			for _, it := range items {
				got = append(got, brief(it))
			}
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Fatalf("items:\n%s\n\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			keys := map[string]bool{}
			for _, it := range items {
				if keys[it.Key] {
					t.Errorf("key %q used twice", it.Key)
				}
				keys[it.Key] = true
			}
		})
	}
}

// TestItemsCreditsMatchTheFoldedReceipts pins the divider's credits to
// the TUI's resolution: a session that exited without a figure takes its
// stage's unclaimed remainder from stage_spend when it is the only one
// missing one, and an open last session claims nothing yet.
func TestItemsCreditsMatchTheFoldedReceipts(t *testing.T) {
	impl := domain.StageImplement
	log := newLog().
		enter(impl, "implementer", "stage").exit(impl, "", 0).
		enter(impl, "reviewer", "critique").exit(impl, "changes", 6).
		enter(impl, "implementer", "stage")
	items := Items(log.evs, Options{Spend: []state.StageSpend{
		{Stage: impl, Role: "implementer", Credits: 30},
		{Stage: impl, Role: "reviewer", Credits: 6},
	}})
	var credits []float64
	for _, it := range items {
		if it.T == ItemStage {
			credits = append(credits, it.Credits)
		}
	}
	if want := []float64{30, 6, 0}; fmt.Sprint(credits) != fmt.Sprint(want) {
		t.Fatalf("divider credits = %v, want %v", credits, want)
	}
}

// TestSincePagesAndRedeliversWhatGrew is the paging contract: a client
// that has drawn everything up to a seq gets the newer items, plus any it
// already drew that grew since — under the same Key.
func TestSincePagesAndRedeliversWhatGrew(t *testing.T) {
	impl := domain.StageImplement
	log := newLog().
		enter(impl, "implementer", "stage").
		tool(impl, "Read  a.go", state.StatusOK, "")
	first := Items(log.evs, Options{})
	after := first[len(first)-1].Seq
	toolsKey := first[len(first)-1].Key

	log.tool(impl, "Edit  a.go", state.StatusOK, "").exit(impl, "", 2)
	page := Since(Items(log.evs, Options{}), after)
	var got []string
	for _, it := range page {
		got = append(got, string(it.T)+" "+it.Key)
	}
	want := []string{"stage stage:1", "tools " + toolsKey}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("page after %d = %v, want %v (the settled divider and the grown group)", after, got, want)
	}
	if n := len(page[1].Tools); n != 2 {
		t.Errorf("the redelivered group holds %d calls, want 2", n)
	}
}

// TestItemsReadTheStoresOwnLog drives the fold through a real store, so a
// tool call and its separately-appended result arrive as one call with
// its outcome and duration, the way a page reads them.
func TestItemsReadTheStoresOwnLog(t *testing.T) {
	ctx := context.Background()
	store, err := state.OpenStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	id, _ := domain.NewFeatureID(7)
	now := time.Now().UTC()
	f := &domain.Feature{ID: id, Num: 7, Title: "Thread fold", Slug: "thread-fold", Stage: domain.StageImplement, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	impl := domain.StageImplement
	pl := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	if err := store.AppendEvents(ctx, []state.CardEvent{
		{Feature: id, Stage: impl, Kind: state.EventStageEnter, At: now, Payload: pl(StageEnterPayload{Role: "implementer", Model: "m-1", Flavor: "stage"})},
		{Feature: id, Stage: impl, Kind: state.EventTool, At: now, Payload: pl(state.ToolPayload{Label: "Bash  make test", Tool: "Bash", Detail: "make test", Call: "c1"})},
		{Feature: id, Stage: impl, Kind: state.EventToolResult, Status: state.StatusFail, At: now, Output: "exit 2", Payload: pl(state.ToolPayload{Call: "c1", MS: 950})},
	}); err != nil {
		t.Fatal(err)
	}
	evs, err := store.Events(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	items := Items(evs, Options{})
	if len(items) != 2 || items[1].T != ItemTools {
		t.Fatalf("items = %+v, want a divider and one tool group", items)
	}
	call := items[1].Tools[0]
	if call.Tool != "Bash" || call.Detail != "make test" || call.Status != state.StatusFail || call.MS != 950 || call.Output != "exit 2" {
		t.Errorf("call = %+v, want Bash make test, failed in 950ms with its output", call)
	}
}

// A line a named person typed is headed with their name, and the
// terminal's own lines with none, so a shared board never shows one
// person's words under another's name (or under the viewer's own).
func TestAYouLineCarriesWhoTypedIt(t *testing.T) {
	plan := domain.StagePlan
	log := newLog().enter(plan, "architect", "stage").
		add(plan, state.EventMessage, "", MessagePayload{Author: "user", Content: "cover the settings page", By: state.PersonActor("Ana")}, "").
		say(plan, "user", "and the footer")
	var got []string
	for _, it := range Items(log.evs, Options{}) {
		if it.T == ItemYou {
			got = append(got, it.By+"|"+it.Text)
		}
	}
	if want := []string{"Ana|cover the settings page", "|and the footer"}; strings.Join(got, ";") != strings.Join(want, ";") {
		t.Errorf("you lines = %q, want %q", got, want)
	}
}

// TestItemsCarryAttachments asserts that a you line's attachments (a
// steer's images, mirrored onto the card log) reach the folded item.
func TestItemsCarryAttachments(t *testing.T) {
	plan := domain.StagePlan
	ref := state.AttachmentRef{ID: strings.Repeat("a", 64), Name: "shot.png", MediaType: "image/png", Size: 4096}
	log := newLog().enter(plan, "architect", "stage").
		add(plan, state.EventMessage, "", MessagePayload{Author: "user", Content: "look at this", Images: []state.AttachmentRef{ref}}, "")
	var got []engine.AttachmentRef
	for _, it := range Items(log.evs, Options{}) {
		if it.T == ItemYou {
			got = it.Attachments
		}
	}
	want := []engine.AttachmentRef{{ID: ref.ID, Name: ref.Name, MediaType: ref.MediaType, Size: ref.Size}}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("you line attachments = %+v, want %+v", got, want)
	}
}

// A verify that had no gummi-checks to run is a verify item with no
// checks and the reason as its text. Before, it drew nothing — and a page
// counting failures in a verify item reads zero checks as "all passed".
func TestAVerifyWithNoChecksIsSaidAsOne(t *testing.T) {
	verify := domain.StageVerify
	log := newLog().enter(verify, "reviewer", "stage").
		tool(verify, engine.NoChecksRow+" — this card has no gummi-checks block, so "+engine.NoChecksConsequence, "", "").
		say(verify, "assistant", "ran go test myself").
		exit(verify, "pass", 2)
	items := Items(log.evs, Options{})
	var v *Item
	for i := range items {
		if items[i].T == ItemVerify {
			v = &items[i]
		}
	}
	if v == nil {
		t.Fatalf("no verify item: %+v", items)
	}
	if len(v.Checks) != 0 || !strings.Contains(v.Text, engine.NoChecksConsequence) {
		t.Errorf("verify item = %+v, want no checks and the no-checks sentence", *v)
	}
	if !v.Exited || v.Verdict != "pass" {
		t.Errorf("verify item = %+v, want it to carry the stage's verdict", *v)
	}
}
