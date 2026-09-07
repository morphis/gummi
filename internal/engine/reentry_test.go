package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/reentry"
	"github.com/morphis/gummi/internal/spec"
)

func TestParseIntentReply(t *testing.T) {
	cases := map[string]reentry.Intent{
		"INTENT: requirement_missing":                   reentry.RequirementMissing,
		"thinking out loud\nINTENT: plan_wrong":         reentry.PlanWrong,
		"intent:  check_missing  ":                      reentry.CheckMissing,
		"INTENT: `separate_card`":                       reentry.SeparateCard,
		"INTENT: plan_wrong\nno wait\nINTENT: question": reentry.Question, // last wins
		"INTENT: implementation-wrong":                  reentry.ImplementationWrong,
	}
	for in, want := range cases {
		got, ok := parseIntentReply(in)
		if !ok || got != want {
			t.Errorf("parseIntentReply(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	// Nothing in the vocabulary, and — the point of the fenced sentence —
	// the sentence quoting the word must not be able to answer for it.
	for _, in := range []string{
		"", "no idea", "INTENT: none", "INTENT: rewrite_everything",
		"the user said INTENT: plan_wrong in passing",
	} {
		if got, ok := parseIntentReply(in); ok {
			t.Errorf("parseIntentReply(%q) = %q, want no classification", in, got)
		}
	}
}

// The prompt must offer exactly the words the router switches on. A
// vocabulary added to reentry and not to the prompt is a route nothing
// can ever reach; one added here and not there is a word that routes to
// a turn while reading as if it did something.
func TestClassifyPromptOffersTheWholeVocabulary(t *testing.T) {
	p := classifyPrompt(feature(1, "dark mode", domain.StageVerify), "the toggle never persists")
	for _, i := range reentry.Vocabulary() {
		if !strings.Contains(p, string(i)) {
			t.Errorf("prompt does not offer %q", i)
		}
		if !strings.Contains(p, reentry.Describe(i)) {
			t.Errorf("prompt does not gloss %q", i)
		}
	}
	if !strings.Contains(p, "the toggle never persists") {
		t.Error("prompt does not carry the sentence")
	}
	if !strings.Contains(p, "verify") {
		t.Error("prompt does not say which stage the card is at")
	}
}

func TestClassifyReentry(t *testing.T) {
	var asked string
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		asked = msg
		return []agent.Event{
			{Kind: agent.EventReasoningDelta, Text: "could be INTENT: plan_wrong"},
			{Kind: agent.EventMessage, Text: "INTENT: requirement_missing"},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "dark mode", domain.StageVerify)
	withWorktree(t, wt, f)

	got, err := e.ClassifyReentry(context.Background(), f, "the persistence step was never in the spec")
	if err != nil {
		t.Fatal(err)
	}
	if got != reentry.RequirementMissing {
		t.Errorf("ClassifyReentry = %q, want %q (reasoning must not be parsed)", got, reentry.RequirementMissing)
	}
	if !strings.Contains(asked, "the persistence step was never in the spec") {
		t.Errorf("the sentence never reached the model: %q", asked)
	}

	// A reply that names nothing classifies nothing — and says so with a
	// nil error, because the sentence is what was unreadable, not the
	// classifier.
	ag.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{{Kind: agent.EventMessage, Text: "INTENT: none"}, {Kind: agent.EventIdle}}
	}
	got, err = e.ClassifyReentry(context.Background(), f, "something is off")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("ClassifyReentry = %q, want no classification", got)
	}

	// An empty sentence never spends a turn.
	called := false
	ag.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		called = true
		return []agent.Event{{Kind: agent.EventIdle}}
	}
	if _, err := e.ClassifyReentry(context.Background(), f, "   "); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("an empty sentence spent a classification turn")
	}
}

// No backend is not a statement about the sentence, and the caller has
// to be able to tell: it falls back to its row's own fixed route rather
// than turning an offered answer into a chat message.
func TestClassifyReentryWithNoBackend(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: map[string]agent.Agent{}, Store: store, Worktrees: wt, Workspace: ws, MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "dark mode", domain.StageVerify)
	if _, err := e.ClassifyReentry(context.Background(), f, "the toggle never persists"); err == nil {
		t.Fatal("want ErrNoScribe, got nil")
	}
}

// The edit is the reason a rewind is worth taking, so it is asserted on
// the artifact's own bytes: the section it landed under, that it is a
// USER marker (only those hold a gate shut), and that spec's own
// gate-blocking predicate now sees it.
func TestApplyReentryEditWritesTheArtifact(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("hi")), Store: store, Worktrees: wt, Workspace: ws, MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "dark mode", domain.StageVerify)
	withWorktree(t, wt, f)
	// the artifact where a card at verify actually carries it: on its own
	// branch, in the worktree.
	path := filepath.Join(ws.Root, f.WorktreePath(), f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(spec.Template(&f)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := e.artifactFile(&f); got != path {
		t.Fatalf("artifactFile = %q, want %q", got, path)
	}

	const miss = "nothing persists the theme choice across restarts"
	if err := e.ApplyReentryEdit(f, reentry.Edit{Section: "Problem", Text: miss}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, "%% @user") || !strings.Contains(body, miss) {
		t.Fatalf("the miss was not recorded as a user marker:\n%s", body)
	}
	doc := spec.Parse(body)
	open := doc.UserOpenThreads()
	if len(open) != 1 {
		t.Fatalf("UserOpenThreads = %d, want 1 — the miss must hold the gate shut", len(open))
	}
	head, ok := spec.HeadingLine(body, "Problem")
	if !ok {
		t.Fatal("Problem heading vanished")
	}
	if open[0].Anchor != head {
		t.Errorf("marker anchored at line %d, want the Problem heading at %d", open[0].Anchor, head)
	}

	// A section this artifact does not carry fails rather than writing
	// the miss somewhere nobody asked for.
	before := body
	if err := e.ApplyReentryEdit(f, reentry.Edit{Section: "Root cause", Text: miss}); err == nil {
		t.Error("writing to a section a feature spec has no heading for should fail")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Error("a failed edit still touched the artifact")
	}

	// An empty edit is a no-op, not an error: the routes that carry no
	// edit call this unconditionally.
	if err := e.ApplyReentryEdit(f, reentry.Edit{}); err != nil {
		t.Errorf("empty edit: %v", err)
	}
}

// A one-shot pass costs real credits, and until now none of them were
// booked: Estimate and DiscoverChecks each spend and neither records, so
// a card's masthead reported less than the card cost. Every pass through
// oneShot books its usage against the card's stage under the scribe
// role, which is what makes the envelope on screen the money spent.
func TestOneShotSpendIsMeteredAgainstTheStage(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 3, Model: "fake-model", InputTokens: 1200, OutputTokens: 40}},
			{Kind: agent.EventMessage, Text: "INTENT: plan_wrong"},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, Persist: true,
	})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "dark mode", domain.StageVerify)
	withWorktree(t, wt, f)
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	if _, err := e.ClassifyReentry(context.Background(), f, "the approach cannot work offline"); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spend.Credits != 3 {
		t.Errorf("card spend = %v credits, want 3", got.Spend.Credits)
	}
	rows, err := store.StageBreakdown(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, row := range rows {
		if row.Stage == domain.StageVerify && row.Role == string(agent.RoleScribe) && row.Credits == 3 {
			found = true
			// Booked as REAL, not estimated. The masthead prefixes "~"
			// and labels a figure "est." off this field, so a one-shot
			// must label an adapter's own credit figure exactly the way
			// a stage session does — otherwise the same card reads as
			// estimated or not depending on which path spent the money.
			if row.EstimatedCredits != 0 {
				t.Errorf("metered credits booked as an estimate: %+v", row)
			}
		}
	}
	if !found {
		t.Errorf("no verify/scribe row carrying the pass's credits: %+v", rows)
	}

	// A token-only backend reports no credits, and that figure IS an
	// estimate — the other half of the same rule.
	e.recordOneShotUsage(f.ID, domain.StageImplement, agent.Usage{Model: "fake-model", InputTokens: 2000, OutputTokens: 100})
	rows, err = store.StageBreakdown(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Stage == domain.StageImplement && row.EstimatedCredits != row.Credits {
			t.Errorf("token-derived credits not labelled as an estimate: %+v", row)
		}
	}
}

func TestParseCodeVsPlan(t *testing.T) {
	claim, anchor := parseCodeVsPlan("CLAIM: nothing persists the choice\nANCHOR: spec:Verification plan")
	if claim != "nothing persists the choice" || anchor != "spec:Verification plan" {
		t.Errorf("parse = %q / %q", claim, anchor)
	}
	// An anchor with a space in it is the common case, not the corner:
	// section headings and check names both have them.
	if _, a := parseCodeVsPlan("CLAIM: x\nANCHOR: check:go vet"); a != "check:go vet" {
		t.Errorf("anchor = %q, want the whole name", a)
	}
	// Both lines or neither.
	for _, in := range []string{
		"CLAIM: none",
		"CLAIM: NONE\nANCHOR: spec:Problem",
		"CLAIM: something is off",
		"ANCHOR: spec:Problem",
		"the diff looks fine to me",
		"",
	} {
		if c, a := parseCodeVsPlan(in); c != "" || a != "" {
			t.Errorf("parseCodeVsPlan(%q) = %q / %q, want nothing", in, c, a)
		}
	}
}

// The prompt must tell the model the contract it is judged by: the
// anchor kinds it may use, and that citing something imaginary throws
// the sentence away. A prompt that asked for evidence without saying it
// is checked would be asking politely.
func TestCodeVsPlanPromptStatesTheContract(t *testing.T) {
	p := codeVsPlanPrompt(feature(1, "dark mode", domain.StageVerify))
	for _, want := range []string{"diff:", "spec:", "check:", "DISCARDS", "CLAIM: none"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt does not carry %q", want)
		}
	}
}
