package ui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/verifydoc"
	"github.com/morphis/gummi/internal/worktree"
)

// researchWorkspace builds a shell wired to a real workspace and a
// Fake-backed engine, with one research card (RS-001) created directly at
// stage — mirroring chatWorkspace's setup, but for a research card, which
// gummi mints through its own creation form rather than the feature "n"
// flow this package's other tests drive.
func researchWorkspace(t *testing.T, ag agent.Agent, stage domain.Stage) (*Shell, *engine.Engine) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	git := func(a ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")

	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root, root, store)
	if err != nil {
		t.Fatal(err)
	}
	pool := worktree.WrapSingle(wt)
	eng := engine.New(engine.Config{Agents: singleAgent(ag), Store: store, Pool: pool, Workspace: ws, Model: "fake-model"})
	t.Cleanup(func() { eng.Close() })

	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return fixedTime }
	m.Attach(store, pool, ws)
	m.AttachEngine(eng)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = model.(*Shell)

	id, err := domain.NewID(domain.KindResearch, 1)
	if err != nil {
		t.Fatal(err)
	}
	slug, _ := domain.Slugify("research card")
	f := domain.Feature{
		ID: id, Num: 1, Kind: domain.KindResearch, Title: "research card", Slug: slug,
		Stage: stage, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	return m, eng
}

// An RS work-leg completion (Investigate finishing) with a review round
// already burned — the review→investigate bounce — auto-continues to
// Shape: onAutonomousDone's investigate case mirrors the Implement/Fix
// case, so the RS review loop turns in the TUI exactly like the feature
// loop, instead of parking as a generic gate item.
func TestRSInvestigateAutoStepsToShape(t *testing.T) {
	ag := verdictAgent(func(agent.SessionOpts) string { return "shaped" })
	// research stages run read-only in the main checkout; only a backend
	// that can structurally enforce that is allowed to drive them.
	ag.Caps.ReadOnlyEnforce = true
	m, eng := researchWorkspace(t, ag, domain.StageInvestigate)
	m.setRound("RS-001", domain.RoundKindReview, 1)

	handled, cmd := m.onAutonomousDone("RS-001", domain.StageInvestigate)
	if !handled {
		t.Fatal("onAutonomousDone(investigate, round>0) not handled — the RS work leg should auto-continue")
	}
	if cmd == nil {
		t.Fatal("onAutonomousDone(investigate, round>0) returned a nil command")
	}
	m = pump(t, m, cmd)

	f, err := m.store.GetFeature(context.Background(), "RS-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.Stage != domain.StageShape {
		t.Fatalf("feature at %s, want Shape (investigate auto-continued)", f.Stage)
	}
	// shape is interactive: the loop only clears the way to it, it never
	// auto-runs an agent turn there — that happens on the human's own
	// attach/Enter, so no session should have started.
	if s := eng.Get("RS-001"); s != nil && (s.State() == engine.StateRunning || s.State() == engine.StateQueued) {
		t.Error("shape session auto-started; want it to wait for the human's attach")
	}
}

// A fresh, loop-free Investigate completion (no review round burned) is
// NOT auto-continued — it raises the generic gate instead, matching
// Implement/Fix's behavior for a first-time work-stage completion.
func TestRSInvestigateNoLoopNotAutoStepped(t *testing.T) {
	m, _ := researchWorkspace(t, verdictAgent(func(agent.SessionOpts) string { return "shaped" }), domain.StageInvestigate)

	handled, cmd := m.onAutonomousDone("RS-001", domain.StageInvestigate)
	if handled {
		t.Fatal("onAutonomousDone(investigate, round==0) was handled — want the generic gate instead")
	}
	if cmd != nil {
		t.Fatal("onAutonomousDone(investigate, round==0) returned a non-nil command")
	}
}

const boardKeyDecomposeTwoRows = "# RS-001: research card\n\n## Findings\n\nNothing cited.\n\n## Slices\n\n" +
	"```yaml\n" +
	"- title: Row one\n  one-liner: first\n  depends-on: []\n  requirements: []\n  id: \"\"\n" +
	"- title: Row two\n  one-liner: second\n  depends-on: []\n  requirements: []\n  id: \"\"\n" +
	"```\n"

// decomposeProposer answers a decompose pass with a fixed two-feature
// proposal set, in the same propose_features shape a real architect
// backend would emit.
func decomposeProposer() *agent.Fake {
	wire, _ := json.Marshal(struct {
		Features []map[string]any `json:"features"`
	}{Features: []map[string]any{
		{"title": "Row one", "one_liner": "first"},
		{"title": "Row two", "one_liner": "second"},
	}})
	return &agent.Fake{
		Caps: agent.Capabilities{ClientTools: true},
		Responder: func(agent.SessionOpts, string) []agent.Event {
			return []agent.Event{
				{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "c1", Name: "propose_features", Args: json.RawMessage(wire)}},
			}
		},
	}
}

// TestBoardKeyGReDecomposesDoneRS proves the board-key re-run surface
// FD-081's Chosen approach promises alongside the headless verb: pressing
// g on a done RS card runs DecomposeForCard and opens the ingest-review
// pane tagged so its approve path dispatches to MintProposals (not
// Materialize) — the same two engine ops the auto-trigger and
// --request-changes already share.
func TestBoardKeyGReDecomposesDoneRS(t *testing.T) {
	m, _ := researchWorkspace(t, decomposeProposer(), domain.StageDone)
	path := filepath.Join(m.ws.Root, "RS-001.md")
	if f, err := m.store.GetFeature(context.Background(), "RS-001"); err == nil {
		path = filepath.Join(m.ws.Root, f.ArtifactPath())
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(boardKeyDecomposeTwoRows), 0o600); err != nil {
		t.Fatal(err)
	}

	m = pump(t, m, m.loadRows)
	if _, ok := m.selected(); !ok {
		t.Fatal("no row selected after loadRows — RS-001 should be on the board")
	}

	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	if m.ingest == nil {
		t.Fatal("g on a done RS card did not open the ingest-review pane")
	}
	if m.ingest.decomposeFor != "RS-001" {
		t.Fatalf("ingest.decomposeFor = %q, want RS-001", m.ingest.decomposeFor)
	}
	if m.ingest.keptCount() != 2 {
		t.Fatalf("keptCount = %d, want 2", m.ingest.keptCount())
	}

	m = press(t, m, tea.KeyPressMsg{Code: 'A', Text: "A"})
	if !m.Overlay.Contains("confirm-ingest") {
		t.Fatal("approve did not raise the confirmation dialog")
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.ingest != nil {
		t.Error("review surface should close after mint")
	}

	feats, err := m.store.ListFeatures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var minted int
	var mintedIDs []string
	for _, f := range feats {
		if f.Title == "Row one" || f.Title == "Row two" {
			minted++
			mintedIDs = append(mintedIDs, string(f.ID))
		}
	}
	if minted != 2 {
		t.Fatalf("minted %d FDs, want 2 (of %d total features)", minted, len(feats))
	}

	rs, err := m.store.GetFeature(context.Background(), "RS-001")
	if err != nil {
		t.Fatal(err)
	}
	if rs.Stage != domain.StageDone {
		t.Fatalf("RS-001 stage = %s, want done (mint never un-approves)", rs.Stage)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range mintedIDs {
		if !strings.Contains(string(raw), id) {
			t.Errorf("minted id %s not back-annotated into the doc:\n%s", id, raw)
		}
	}
}

// --- FD-083: the RS board surface (creation form, badge, key gating,
// verifydoc dispatch, next-step hints) ---

// writeRSArtifact writes body to f's draft location — the seed-then-persist
// slot m.artifactFile resolves before an item's spec is ever promoted.
func writeRSArtifact(t *testing.T, m *Shell, f domain.Feature, body string) {
	t.Helper()
	draft := filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(&f))
	if err := os.MkdirAll(filepath.Dir(draft), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(draft, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// rsCardShell builds a shell attached to a fresh workspace with one RS
// card (RS-001) at Investigate — every caller here exercises a key or
// dispatch that fires the same way at any RS stage (NeedsWorktree is
// false throughout, and verifydoc has no stage dependency either) — and
// the given repo name ("" = the workspace default).
func rsCardShell(t *testing.T, repo string) (*Shell, domain.Feature) {
	t.Helper()
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return fixedTime }
	m.Attach(store, wt, ws)
	id, err := domain.NewID(domain.KindResearch, 1)
	if err != nil {
		t.Fatal(err)
	}
	slug, _ := domain.Slugify("research card")
	f := domain.Feature{
		ID: id, Num: 1, Kind: domain.KindResearch, Title: "research card", Slug: slug,
		Stage: domain.StageInvestigate, Repo: repo, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	return m, f
}

// --- Step 1: the rsForm dialog ---

func TestRS_Form_SubmitCarriesBrief(t *testing.T) {
	var got rsFormResult
	form := newRSForm([]string{"thrifty"}, []string{"lxd"}, true, 1000, func(res rsFormResult) tea.Cmd {
		got = res
		return nil
	})
	form.brief.SetValue("Investigate the retry storm\n\nmore detail")
	if done, _ := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !done {
		t.Fatal("form did not submit")
	}
	if got.Brief != "Investigate the retry storm\n\nmore detail" {
		t.Errorf("Brief = %q", got.Brief)
	}
	if got.Profile != "thrifty" {
		t.Errorf("Profile = %q, want thrifty", got.Profile)
	}
	if got.Envelope == nil || *got.Envelope != 1000 {
		t.Errorf("Envelope = %v, want 1000", got.Envelope)
	}
}

func TestRS_Form_EnterEmptyBriefErrors(t *testing.T) {
	submitted := false
	form := newRSForm(nil, nil, false, 1000, func(rsFormResult) tea.Cmd {
		submitted = true
		return nil
	})
	if done, _ := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done {
		t.Fatal("empty brief submitted")
	}
	if submitted {
		t.Fatal("onSubmit ran with an empty brief")
	}
	if form.errText != "brief required" {
		t.Errorf("errText = %q, want %q", form.errText, "brief required")
	}
}

func TestRS_Form_EnterEmptyEnvelopeErrors(t *testing.T) {
	submitted := false
	form := newRSForm(nil, nil, false, 1000, func(rsFormResult) tea.Cmd {
		submitted = true
		return nil
	})
	form.brief.SetValue("investigate the retry storm")
	form.env.SetValue("")
	if done, _ := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done {
		t.Fatal("empty envelope submitted")
	}
	if submitted {
		t.Fatal("onSubmit ran with an empty envelope")
	}
	if form.errText != "envelope required" {
		t.Errorf("errText = %q, want %q", form.errText, "envelope required")
	}
}

func TestRS_Form_EnterPunctuationOnlyBriefErrors(t *testing.T) {
	submitted := false
	form := newRSForm(nil, nil, false, 1000, func(rsFormResult) tea.Cmd {
		submitted = true
		return nil
	})
	form.brief.SetValue("???")
	if done, _ := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done {
		t.Fatal("punctuation-only brief submitted")
	}
	if submitted {
		t.Fatal("onSubmit ran with a punctuation-only brief")
	}
	if form.errText != "brief must include a letter or digit" {
		t.Errorf("errText = %q, want %q", form.errText, "brief must include a letter or digit")
	}
}

func TestRS_Form_EscCancels(t *testing.T) {
	form := newRSForm(nil, nil, false, 1000, func(rsFormResult) tea.Cmd {
		t.Fatal("onSubmit ran on esc")
		return nil
	})
	done, cmd := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !done {
		t.Fatal("esc did not close the form")
	}
	if cmd != nil {
		t.Fatal("esc returned a non-nil command")
	}
}

func TestRS_Form_TabsFocusRing(t *testing.T) {
	form := newRSForm(nil, nil, false, 0, func(rsFormResult) tea.Cmd { return nil })
	for _, want := range []int{rsFieldBrief, rsFieldEnvelope, rsFieldProfile, rsFieldButtons, rsFieldBrief} {
		if form.focus != want {
			t.Fatalf("focus = %d, want %d", form.focus, want)
		}
		form.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	}
}

// --- Step 2: createResearch ---

func TestRS_CreateResearch_MintsAndSeedsDraft(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return fixedTime }
	m.Attach(store, wt, ws)

	envelope := 750
	brief := "Investigate the retry storm\n\nmore detail about the ask"
	msg := m.createResearch(rsFormResult{Brief: brief, Profile: "thrifty", Envelope: &envelope})()
	if nm, ok := msg.(noticeMsg); !ok || nm.isErr {
		t.Fatalf("createResearch failed: %#v", msg)
	}

	f, err := store.GetFeature(ctx, "RS-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.Kind != domain.KindResearch {
		t.Errorf("Kind = %s, want research", f.Kind)
	}
	if f.Stage != domain.StageTodo {
		t.Errorf("Stage = %s, want todo", f.Stage)
	}
	wantSlug, _ := domain.Slugify("Investigate the retry storm")
	if f.Slug != wantSlug {
		t.Errorf("Slug = %q, want %q", f.Slug, wantSlug)
	}
	if f.Title != "Investigate the retry storm" {
		t.Errorf("Title = %q", f.Title)
	}
	if f.Budget.Envelope != 750 {
		t.Errorf("Envelope = %d, want 750", f.Budget.Envelope)
	}

	raw, err := os.ReadFile(filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f)))
	if err != nil {
		t.Fatal(err)
	}
	body, ok := spec.ViewSection(string(raw), "Brief")
	if !ok {
		t.Fatal("no Brief section in the seeded draft")
	}
	if !strings.Contains(body, strings.TrimSpace(brief)) {
		t.Errorf("Brief section = %q, want it to contain the trimmed brief verbatim", body)
	}
}

// --- Step 3: the R key + empty-board hint ---

func TestRS_R_CreatesResearchCard(t *testing.T) {
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return fixedTime }
	m.Attach(store, wt, ws)
	m.SetEnvelope(500)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = model.(*Shell)

	m = press(t, m, tea.KeyPressMsg{Code: 'R', Text: "R"})
	form, ok := m.Overlay.Top().(*rsForm)
	if !ok {
		t.Fatal("R did not open the research form")
	}
	form.brief.SetValue("Scratch research brief")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	f, err := store.GetFeature(context.Background(), "RS-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.Kind != domain.KindResearch {
		t.Errorf("Kind = %s, want research", f.Kind)
	}
	if f.Stage != domain.StageTodo {
		t.Errorf("Stage = %s, want todo", f.Stage)
	}
	if f.Title != "Scratch research brief" {
		t.Errorf("Title = %q", f.Title)
	}
	found := false
	for _, r := range m.rows {
		if r.F.ID == "RS-001" {
			found = true
		}
	}
	if !found {
		t.Error("RS-001 not present on the board after R → submit")
	}
}

func TestRS_EmptyBoard_HintR(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	out := m.boardView(80)
	if !strings.Contains(out, "new research") {
		t.Errorf("empty-board hint does not mention research: %s", out)
	}
}

// --- Step 5: the badge tint ---

func TestRS_CardLine_BadgeTint(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	fid, _ := domain.NewFeatureID(1)
	bid, _ := domain.NewID(domain.KindBug, 1)
	rid, _ := domain.NewID(domain.KindResearch, 1)
	m.rows = []featureRow{
		{F: domain.Feature{ID: fid, Num: 1, Title: "feature", Stage: domain.StageTodo, CreatedAt: fixedTime, UpdatedAt: fixedTime}},
		{F: domain.Feature{ID: bid, Num: 1, Kind: domain.KindBug, Title: "bug", Stage: domain.StageTodo, CreatedAt: fixedTime, UpdatedAt: fixedTime}},
		{F: domain.Feature{ID: rid, Num: 1, Kind: domain.KindResearch, Title: "research", Stage: domain.StageTodo, CreatedAt: fixedTime, UpdatedAt: fixedTime}},
	}
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = model.(*Shell)

	featureLine := m.cardLine(m.rows[0], 1, false, 100)
	bugLine := m.cardLine(m.rows[1], 2, false, 100)
	rsLine := m.cardLine(m.rows[2], 3, false, 100)

	wantID := m.styles.CardIDResearch.Render(string(rid))
	if !strings.Contains(rsLine, wantID) {
		t.Errorf("RS card line does not carry the CardIDResearch tint:\n%s", rsLine)
	}
	if strings.Contains(featureLine, wantID) {
		t.Error("feature card line unexpectedly carries the research tint")
	}
	if strings.Contains(bugLine, wantID) {
		t.Error("bug card line unexpectedly carries the research tint")
	}
}

// --- Step 6: d/m/r/c refuse on RS ---

func TestRS_WorktreeKeys_Refuse(t *testing.T) {
	cases := []struct {
		key  rune
		verb string
	}{
		{'d', "diff"},
		{'m', "merge"},
		{'r', "rebase"},
		{'c', "cleanup"},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			m, _ := rsCardShell(t, "")
			model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m = model.(*Shell)
			m = pump(t, m, m.loadRows)
			if _, ok := m.selected(); !ok {
				t.Fatal("no row selected after loadRows")
			}

			m = press(t, m, tea.KeyPressMsg{Code: tc.key, Text: string(tc.key)})
			want := "RS-001: no " + tc.verb + " — research cards carry no branch"
			if m.notice.text != want {
				t.Errorf("notice = %q, want %q", m.notice.text, want)
			}
			if m.diff != nil {
				t.Error("diff view opened despite the refusal")
			}
			if m.Overlay.HasDialogs() {
				t.Error("a dialog was pushed despite the refusal")
			}
		})
	}
}

// TestRS_FDStillDispatches proves the new per-key guards are RS-scoped:
// an FD card at a worktree-bound stage still reaches each of the four
// handlers unchanged, none of them short-circuiting to the RS refusal.
func TestRS_FDStillDispatches(t *testing.T) {
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return fixedTime }
	m.Attach(store, wt, ws)
	id, _ := domain.NewFeatureID(1)
	slug, _ := domain.Slugify("feature card")
	f := domain.Feature{ID: id, Num: 1, Title: "feature card", Slug: slug, Stage: domain.StageImplement, CreatedAt: fixedTime, UpdatedAt: fixedTime}
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = model.(*Shell)
	m = pump(t, m, m.loadRows)

	for _, key := range []rune{'d', 'r', 'm', 'c'} {
		m.notice = noticeMsg{}
		m = press(t, m, tea.KeyPressMsg{Code: key, Text: string(key)})
		if strings.Contains(m.notice.text, "research cards carry no branch") {
			t.Errorf("key %q: the RS refusal fired for an FD card: %q", string(key), m.notice.text)
		}
	}
}

// --- Step 7-8: v → verifydoc dispatch + the report dialog ---

const rsCleanDoc = "# RS-001: research card\n\n## Brief\n\nthe ask\n\n## Findings\n\nNothing cited.\n\n## Questions\n\n## Slices\n\n## Out of scope\n"

const rsBrokenCitationDoc = "# RS-001: research card\n\n## Findings\n\nSee `README.md:99` for details.\n"

const rsUnmappedQuestionDoc = "# RS-001: research card\n\n## Questions\n\n- what should we investigate?\n\n## Slices\n\n## Out of scope\n"

func TestRS_V_RunsDocVerify(t *testing.T) {
	t.Run("broken_citation", func(t *testing.T) {
		m, f := rsCardShell(t, "")
		writeRSArtifact(t, m, f, rsBrokenCitationDoc)
		m.runDocVerify(f)
		d, ok := m.Overlay.Top().(*docVerifyDialog)
		if !ok {
			t.Fatal("no doc-verify dialog pushed")
		}
		if len(d.report.Citations) != 1 {
			t.Fatalf("Citations = %d, want 1: %#v", len(d.report.Citations), d.report.Citations)
		}
	})

	t.Run("unmapped_question", func(t *testing.T) {
		m, f := rsCardShell(t, "")
		writeRSArtifact(t, m, f, rsUnmappedQuestionDoc)
		m.runDocVerify(f)
		d, ok := m.Overlay.Top().(*docVerifyDialog)
		if !ok {
			t.Fatal("no doc-verify dialog pushed")
		}
		if len(d.report.Coverage) != 1 {
			t.Fatalf("Coverage = %d, want 1: %#v", len(d.report.Coverage), d.report.Coverage)
		}
	})

	t.Run("clean", func(t *testing.T) {
		m, f := rsCardShell(t, "")
		writeRSArtifact(t, m, f, rsCleanDoc)
		m.runDocVerify(f)
		want := "RS-001: document verify — clean"
		if m.notice.text != want {
			t.Errorf("notice = %q, want %q", m.notice.text, want)
		}
		if m.Overlay.HasDialogs() {
			t.Error("a dialog was pushed on a clean report")
		}
	})

	t.Run("no_doc", func(t *testing.T) {
		m, f := rsCardShell(t, "")
		m.runDocVerify(f)
		want := "RS-001: no doc yet — verifydoc runs on the artifact"
		if m.notice.text != want {
			t.Errorf("notice = %q, want %q", m.notice.text, want)
		}
	})

	t.Run("repo_unresolved", func(t *testing.T) {
		m, f := rsCardShell(t, "no-such-repo")
		writeRSArtifact(t, m, f, rsCleanDoc)
		m.runDocVerify(f)
		wantPrefix := "RS-001: repo unresolved — "
		if !strings.HasPrefix(m.notice.text, wantPrefix) {
			t.Errorf("notice = %q, want prefix %q", m.notice.text, wantPrefix)
		}
	})
}

func TestRS_DocVerifyDialog_RendersReport(t *testing.T) {
	id, _ := domain.NewID(domain.KindResearch, 1)
	f := domain.Feature{ID: id}
	report := verifydoc.Report{
		OpenThreads: 2,
		Citations:   []verifydoc.CitationIssue{{Citation: "foo.go:10", Reason: "line out of range"}},
		Coverage:    []verifydoc.CoverageIssue{{Item: "what about x?", Reason: "no slice or out-of-scope line answers it"}},
	}
	d := newDocVerifyDialog(f, report)
	out := d.View(theme.New(theme.GummiDark()), 80, 24)
	for _, want := range []string{"foo.go:10", "line out of range", "what about x?", "no slice or out-of-scope line answers it", "open threads: 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
}

// --- Step 9: bar bindings + next-step hints for RS ---

func TestRS_BoardBindings_OmitsWorktreeKeys(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	rid, _ := domain.NewID(domain.KindResearch, 1)
	fid, _ := domain.NewFeatureID(2)
	m.rows = []featureRow{
		{F: domain.Feature{ID: rid, Num: 1, Kind: domain.KindResearch, Title: "research", Stage: domain.StageInvestigate, CreatedAt: fixedTime, UpdatedAt: fixedTime}},
		{F: domain.Feature{ID: fid, Num: 2, Title: "feature", Stage: domain.StageImplement, CreatedAt: fixedTime, UpdatedAt: fixedTime}},
	}

	m.sel = 0
	for _, b := range m.boardBindings() {
		switch b.key {
		case "d", "r", "m", "c", "z":
			t.Errorf("RS-selected board bindings still include %q", b.key)
		}
	}

	m.sel = 1
	found := map[string]bool{}
	for _, b := range m.boardBindings() {
		found[b.key] = true
	}
	for _, k := range []string{"d", "r", "m", "c", "z"} {
		if !found[k] {
			t.Errorf("FD-selected board bindings missing %q", k)
		}
	}
}

func TestRS_HelpOverlay_OmitsWorktreeKeysOnRS(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	rid, _ := domain.NewID(domain.KindResearch, 1)
	m.rows = []featureRow{
		{F: domain.Feature{ID: rid, Num: 1, Kind: domain.KindResearch, Title: "research", Stage: domain.StageInvestigate, CreatedAt: fixedTime, UpdatedAt: fixedTime}},
	}
	m.sel = 0
	dlg := m.helpOverlay()
	for _, row := range dlg.rows {
		switch row[0] {
		case "d", "r", "m", "c":
			t.Errorf("? help still lists %q for a selected RS card", row[0])
		}
	}
}

func TestRS_NextSteps_HintsMatchKind(t *testing.T) {
	cases := []struct {
		name string
		in   nextInput
	}{
		{"todo", nextInput{stage: domain.StageTodo, kind: domain.KindResearch}},
		{"investigate", nextInput{stage: domain.StageInvestigate, kind: domain.KindResearch}},
		{"shape", nextInput{stage: domain.StageShape, kind: domain.KindResearch}},
		{"review", nextInput{stage: domain.StageReview, kind: domain.KindResearch, attn: attnGate}},
		{"verify_pass", nextInput{stage: domain.StageVerify, kind: domain.KindResearch, attn: attnGate, verdict: verdictPass}},
		{"verify_fail", nextInput{stage: domain.StageVerify, kind: domain.KindResearch, attn: attnGate, verdict: verdictFail}},
		{"verify_blocked_gate", nextInput{stage: domain.StageVerify, kind: domain.KindResearch, attn: attnGate, openSpecQs: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, a := range nextActions(tc.in) {
				switch a.key {
				case "d", "r", "m", "c":
					t.Errorf("%s: nextActions includes worktree key %q", tc.name, a.key)
				}
			}
		})
	}
}

func TestRS_NextSteps_VerifyPass_TwoRows(t *testing.T) {
	in := nextInput{stage: domain.StageVerify, kind: domain.KindResearch, attn: attnGate, verdict: verdictPass}
	got := nextActions(in)
	want := []nextAction{
		{"g", "mark done", "verify passed — advance to done"},
		{"b", "bounce to investigate", "not convinced — send it back with comments"},
	}
	if len(got) != len(want) {
		t.Fatalf("nextActions = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("action %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
