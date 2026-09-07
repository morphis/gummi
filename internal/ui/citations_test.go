package ui

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// codeVsPlanAgent answers the code-vs-plan pass with claim + anchor and
// counts how many times it was asked. Every other turn replies "ok", so
// a fixture can walk a card forward on the same agent.
func codeVsPlanAgent(claimText, anchorText string, asked *int) *agent.Fake {
	return &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if !strings.Contains(msg, "CLAIM: <your one sentence>") {
			return []agent.Event{{Kind: agent.EventMessage, Text: "ok"}, {Kind: agent.EventIdle}}
		}
		*asked++
		reply := "CLAIM: none"
		if claimText != "" {
			reply = "CLAIM: " + claimText + "\nANCHOR: " + anchorText
		}
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 2, Model: "fake-model", InputTokens: 900, OutputTokens: 30}},
			{Kind: agent.EventMessage, Text: reply},
			{Kind: agent.EventIdle},
		}
	}}
}

// gatedVerifyCard is a card sitting at the verify gate — the
// approve-shaped stop the code-vs-plan sentence exists for.
func gatedVerifyCard(t *testing.T, ag agent.Agent) *Shell {
	t.Helper()
	m, _ := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StageVerify)
	m.raiseAttention(m.rows[0].F.ID, attnGate, "verify finished")
	// the narration is a card-page thing, and its cache key is built from
	// the log the page loads (citations.go), so the fixture opens the
	// page rather than asserting against a board row.
	m.cardOpen = true
	return pump(t, m, m.loadCardEvents(m.rows[0].F.ID))
}

func narrationOf(m *Shell) []claim {
	r := m.rows[0]
	return m.cardNarration(m.nextInputFor(r), r)
}

func claimTexts(cs []claim) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.text)
	}
	return out
}

// INVARIANT 3, the positive half: a claim whose anchor names something
// on the card is admitted, rendered, and opens.
func TestCitedClaimIsAdmittedAndOpens(t *testing.T) {
	asked := 0
	m := gatedVerifyCard(t, codeVsPlanAgent(
		"nothing persists the theme choice, and the plan's step 4 asked for that",
		"spec:Verification plan", &asked))

	m = pump(t, m, m.ensureNarration(m.rows[0]))
	if asked != 1 {
		t.Fatalf("the pass ran %d times, want 1", asked)
	}
	cs := narrationOf(m)
	cited := citedClaims(cs)
	if len(cited) == 0 {
		t.Fatalf("no cited claim in %q", claimTexts(cs))
	}
	last := cs[len(cs)-1]
	if !strings.Contains(last.text, "nothing persists the theme choice") {
		t.Fatalf("the model's claim is not in the narration: %q", claimTexts(cs))
	}
	if last.a.kind != "spec" || last.a.ref != "Verification plan" {
		t.Errorf("claim anchor = %q", last.a)
	}

	// and the chord opens it, on the section it names
	m.cardOpen = true
	cmd, ok := m.cardCitationKey(citationMark(len(cited)))
	if !ok {
		t.Fatal("alt+n did not answer")
	}
	m = pump(t, m, cmd)
	if m.spec == nil {
		t.Fatal("the citation did not open the artifact")
	}
	if got := m.spec.doc.Lines[m.spec.cursor-1]; !strings.Contains(got, "Verification plan") {
		t.Errorf("landed on %q, want the Verification plan heading", got)
	}
}

// INVARIANT 3, the half that matters: a claim citing something that does
// not exist never reaches a rendered narration. The model is not asked
// to behave — the admitting code refuses it.
func TestUnresolvableCitationIsDropped(t *testing.T) {
	asked := 0
	m := gatedVerifyCard(t, codeVsPlanAgent(
		"the retry loop never backs off", "diff:src/nowhere.go:42", &asked))

	m = pump(t, m, m.ensureNarration(m.rows[0]))
	if asked != 1 {
		t.Fatalf("the pass ran %d times, want 1", asked)
	}
	for _, c := range narrationOf(m) {
		if strings.Contains(c.text, "the retry loop never backs off") {
			t.Fatal("a claim citing a file the card does not have was rendered")
		}
	}
	if !strings.Contains(m.notice.text, "dropped a narration claim") {
		t.Errorf("the drop was not reported: %+v", m.notice)
	}

	// The drop is cached like any other answer, so a model that cites
	// something imaginary is asked once for this state, not on a loop.
	m = pump(t, m, m.ensureNarration(m.rows[0]))
	if asked != 1 {
		t.Errorf("a rejected claim was re-asked (%d passes)", asked)
	}
	if got := len(citedClaims(narrationOf(m))); got != 0 {
		t.Errorf("%d citations rendered after the only claim was dropped", got)
	}
}

// A claim with no anchor at all is refused for the same reason: the
// contract is evidence, and "trust me" is not evidence.
func TestClaimWithoutAnAnchorIsDropped(t *testing.T) {
	asked := 0
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if !strings.Contains(msg, "CLAIM: <your one sentence>") {
			return []agent.Event{{Kind: agent.EventMessage, Text: "ok"}, {Kind: agent.EventIdle}}
		}
		asked++
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "CLAIM: something feels off about the diff"},
			{Kind: agent.EventIdle},
		}
	}}
	m := gatedVerifyCard(t, ag)
	m = pump(t, m, m.ensureNarration(m.rows[0]))
	for _, c := range narrationOf(m) {
		if strings.Contains(c.text, "something feels off") {
			t.Fatal("an uncited claim was rendered")
		}
	}
}

// "None" is a real answer and is cached as one: a diff that does what
// the plan asked must not be re-asked at every frame, or at every stop.
func TestNothingToSayIsCachedAsNothing(t *testing.T) {
	asked := 0
	m := gatedVerifyCard(t, codeVsPlanAgent("", "", &asked))
	for i := 0; i < 3; i++ {
		m = pump(t, m, m.ensureNarration(m.rows[0]))
	}
	if asked != 1 {
		t.Errorf("a card with nothing to say was asked %d times", asked)
	}
	if got := len(narrationOf(m)); got == 0 {
		t.Error("the free sentences went missing with the model's")
	}
}

// INVARIANT 4's regeneration half, which Phase 0 could only state
// vacuously: the same card state is never asked twice, and a changed one
// is asked again exactly once.
func TestNarrationNotRegeneratedForTheSameState(t *testing.T) {
	asked := 0
	m := gatedVerifyCard(t, codeVsPlanAgent("the diff skips step 4", "spec:Verification plan", &asked))
	r := m.rows[0]

	for i := 0; i < 4; i++ {
		m = pump(t, m, m.ensureNarration(m.rows[0]))
	}
	if asked != 1 {
		t.Fatalf("the same state was asked %d times", asked)
	}

	// A new event moves the key — the log is append-only, so its newest
	// seq is the version stamp.
	if err := m.store.AppendEvent(context.Background(), state.CardEvent{
		Feature: r.F.ID, Stage: domain.StageVerify, Kind: state.EventPark, At: m.now(),
	}); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadCardEvents(r.F.ID))
	m = pump(t, m, m.loadRows)
	if asked != 2 {
		t.Fatalf("a changed card state was asked %d times, want 2", asked)
	}
	m = pump(t, m, m.loadRows)
	if asked != 2 {
		t.Fatalf("the new state was re-asked (%d passes)", asked)
	}

	// Resolving a blocker changes what the stop MEANS while appending
	// nothing, which is exactly why the key carries the counts too.
	before := m.narrationKey(m.nextInputFor(m.rows[0]), m.rows[0].F.ID)
	in := m.nextInputFor(m.rows[0])
	in.openSpecQs = 2
	if m.narrationKey(in, m.rows[0].F.ID) == before {
		t.Error("the cache key ignores the gate's blocking counts")
	}
}

// Generated only at real stops, and only at approve-shaped ones. The
// sweep is the same one the other invariants use, so a new arm cannot
// quietly start buying a sentence.
func TestCodeVsPlanOnlyAtApproveShapedStops(t *testing.T) {
	for _, in := range surfaceInputs() {
		got := approveShaped(in)
		if got && !narrationStop(in) {
			t.Errorf("%+v: approve-shaped on a card nobody is waiting on", in)
		}
		if got && in.kind == domain.KindResearch {
			t.Errorf("%+v: a research card has no diff to compare", in)
		}
		if got && in.stage != domain.StageImplement && in.stage != domain.StageVerify {
			t.Errorf("%+v: approve-shaped at %s", in, in.stage)
		}
		if got && (in.attn == attnFailure || in.attn == attnBudget || in.attn == attnQuestion || in.hasAsk) {
			t.Errorf("%+v: approve-shaped at a stop that is about something else", in)
		}
	}
	// and the two that must be
	for _, in := range []nextInput{
		{stage: domain.StageVerify, kind: domain.KindFeature, attn: attnGate, verdict: verdictPass},
		{stage: domain.StageImplement, kind: domain.KindBug, sess: engine.StateDone},
	} {
		if !approveShaped(in) {
			t.Errorf("%+v: not approve-shaped, but it is the stop the sentence exists for", in)
		}
	}
}

// A card another process drives never spends its envelope from here.
// Read-only means read the cache, not buy a fresh one on someone else's
// budget.
func TestForeignCardIsNeverGeneratedFor(t *testing.T) {
	asked := 0
	m := gatedVerifyCard(t, codeVsPlanAgent("x", "spec:Problem", &asked))
	r := m.rows[0]
	// the fixture's own card page already bought one; the question here
	// is whether a FOREIGN row buys another.
	asked = 0
	r.DrivenAbroad = true
	if cmd := m.ensureNarration(r); cmd != nil {
		t.Fatal("a foreign card dispatched a model turn")
	}
	if asked != 0 {
		t.Errorf("a foreign card spent %d turns", asked)
	}
}

func TestParseAnchor(t *testing.T) {
	ok := map[string]anchor{
		"check:go vet":         {kind: "check", ref: "go vet"},
		"EVENT: 42":            {kind: "event", ref: "42"},
		"`diff:lxc/list.go:9`": {kind: "diff", ref: "lxc/list.go:9"},
		"spec:Chosen approach": {kind: "spec", ref: "Chosen approach"},
	}
	for in, want := range ok {
		got, ok := parseAnchor(in)
		if !ok || got != want {
			t.Errorf("parseAnchor(%q) = %+v,%v want %+v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "nonsense", "hunk:a.go:1", "check:", ":42", "spec"} {
		if got, ok := parseAnchor(in); ok {
			t.Errorf("parseAnchor(%q) = %+v, want no anchor", in, got)
		}
	}
}

func TestEvidenceResolves(t *testing.T) {
	ev := evidence{
		checks:   map[string]bool{"go vet": true},
		sections: map[string]bool{"verification plan": true},
		files:    map[string]bool{"lxc/list.go": true},
		events:   map[int64]bool{7: true},
	}
	for _, a := range []anchor{
		{"check", "go vet"},
		{"check", "GO VET"},
		{"spec", "Verification plan"},
		{"diff", "lxc/list.go:12"},
		{"diff", "list.go:12"},
		{"event", "7"},
	} {
		if !ev.resolves(a) {
			t.Errorf("%q does not resolve but names something real", a)
		}
	}
	for _, a := range []anchor{
		{"check", "go build"},
		{"spec", "Root cause"},
		{"diff", "lxc/other.go:1"},
		{"diff", "lxc/list.go"},
		{"event", "8"},
		{"event", "seven"},
		{"hunk", "lxc/list.go:1"},
	} {
		if ev.resolves(a) {
			t.Errorf("%q resolved against a card that has no such thing", a)
		}
	}
}

func TestDiffLineFor(t *testing.T) {
	const diff = "diff --git a/lxc/list.go b/lxc/list.go\n" +
		"--- a/lxc/list.go\n" +
		"+++ b/lxc/list.go\n" +
		"@@ -10,3 +10,4 @@\n" + // diff line 4; post-image starts at 10
		" ctx\n" + // 5  -> 10
		"-gone\n" + // 6  -> (no number)
		"+added\n" + // 7  -> 11
		" tail\n" // 8  -> 12
	cases := map[int]int{10: 5, 11: 7, 12: 8}
	for want, line := range cases {
		if got := diffLineFor(diff, diffTarget{path: "lxc/list.go", line: want}); got != line {
			t.Errorf("diffLineFor(line %d) = %d, want %d", want, got, line)
		}
	}
	// a line past the hunk lands on the file header rather than nowhere
	if got := diffLineFor(diff, diffTarget{path: "lxc/list.go", line: 900}); got != 3 {
		t.Errorf("out-of-hunk line = %d, want the file header at 3", got)
	}
	if got := diffLineFor(diff, diffTarget{path: "other.go", line: 10}); got != 0 {
		t.Errorf("unknown file = %d, want 0", got)
	}
}

// A citation number the narration did not print opens nothing, and the
// chord still answers rather than falling through to type an alt-digit
// into the composer.
func TestUnprintedCitationOpensNothing(t *testing.T) {
	m := gatedVerifyCard(t, agent.NewFake("ok"))
	m.cardOpen = true
	cmd, ok := m.cardCitationKey("alt+i")
	if !ok {
		t.Fatal("the chord fell through to the surface below")
	}
	if cmd != nil {
		t.Error("alt+i opened something on a card with no ninth citation")
	}
	// the chords that must NOT be read as citations: the tab tier's own
	// letters, and every alt+digit — those are the shell's board/inbox/
	// agent tabs, answered above this tier.
	for _, key := range []string{"alt+s", "alt+d", "alt+t", "alt+j", "alt+k", "alt+o", "alt+1", "alt+2", "alt+3", "alt+9"} {
		if n, ok := citationChord(key); ok {
			t.Errorf("%s was read as citation %d", key, n)
		}
	}
}

// TestInvariantCitationsResolve — invariant 3, over the same sweep the
// other invariants use. Every anchor a rendered narration carries must
// name something real on the card it is about: a check that ran, an
// event in its log, one of the four kinds and nothing else.
//
// The free sentences get their anchors by construction — the check name
// comes from m.checksFor, the event seq from the log the stretch was
// folded out of — so this is a lock on that construction rather than a
// hunt for a bug. The claim that can genuinely cite something imaginary
// is the model's, and it is refused before it is cached
// (TestUnresolvableCitationIsDropped).
func TestInvariantCitationsResolve(t *testing.T) {
	m := attachedBoard(t, 120, 34)
	row := m.rows[m.sel]
	events := m.cardEvents[row.F.ID]
	seqs := map[int64]bool{}
	for _, ev := range events {
		seqs[ev.Seq] = true
	}
	for _, in := range surfaceInputs() {
		for _, c := range m.cardNarration(in, row) {
			if c.a.empty() {
				continue
			}
			if _, ok := parseAnchor(c.a.String()); !ok {
				t.Errorf("%+v: claim %q carries an unparseable anchor %q", in, c.text, c.a)
				continue
			}
			switch c.a.kind {
			case "check":
				if c.a.ref != in.failedCheck {
					t.Errorf("%+v: check anchor %q names no check on this card", in, c.a)
				}
			case "event":
				n, err := strconv.ParseInt(c.a.ref, 10, 64)
				if err != nil || !seqs[n] {
					t.Errorf("%+v: event anchor %q is not in the card's log", in, c.a)
				}
			default:
				t.Errorf("%+v: a free sentence cited %q, which it has no way to have checked", in, c.a)
			}
		}
	}
}

// The marks the reader sees and the targets the chord opens are counted
// by the same function, so alt+2 can never open the third citation.
func TestCitationNumbersMatchTheMarks(t *testing.T) {
	asked := 0
	m := gatedVerifyCard(t, codeVsPlanAgent("the diff skips step 4", "spec:Chosen approach", &asked))
	m = pump(t, m, m.ensureNarration(m.rows[0]))

	r := m.rows[0]
	cs := m.cardNarration(m.nextInputFor(r), r)
	cited := citedClaims(cs)
	block := m.narrationBlock(m.styles, &threadDecision{}, r, 100)
	text := strings.Join(block, "\n")
	for i := range cited {
		if !strings.Contains(text, "["+citationMark(i+1)+"]") {
			t.Errorf("citation %d has no mark in the rendered block:\n%s", i+1, text)
		}
	}
	if strings.Contains(text, "["+citationMark(len(cited)+1)+"]") {
		t.Errorf("the block prints a mark past the last citation:\n%s", text)
	}
	// and an uncited sentence gets no number at all
	for _, c := range cs {
		if c.a.empty() && strings.Contains(c.text, "[alt+") {
			t.Errorf("an uncited claim carries a mark: %q", c.text)
		}
	}
}
