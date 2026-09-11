package ui

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
)

// The code-vs-plan sentence, its cache, and the contract that lets it on
// screen.
//
// It is the only thing on the card page a model writes, and the only
// part of the narration that costs money, so all three of the rules the
// design puts around it live here rather than being spread over the
// render path:
//
//   - GENERATED ONLY AT REAL STOPS. narrationStop says whether the card
//     is parked for a person at all; approveShaped narrows that to the
//     stops where the question is worth asking. A running, queued, done,
//     failed, out-of-budget or foreign card never reaches a model.
//
//   - NEVER REGENERATED FOR THE SAME STATE. The cache key is the card's
//     newest event seq plus the gate's blocking counts. The log is
//     append-only, so the newest seq is a natural version stamp; the
//     counts are there because resolving a comment changes what the
//     stop MEANS without appending anything.
//
//   - NEVER RENDERED WITHOUT RESOLVABLE EVIDENCE. The pass returns a
//     sentence and an anchor; the anchor is resolved against the real
//     card — its checks, its artifact, its diff, its log — before the
//     sentence is admitted to the cache. A claim that cites something
//     that does not exist is discarded, and it is the discard, not the
//     model's good behaviour, that keeps hallucinated evidence off the
//     screen.
//
// Everything expensive happens inside the dispatched command: the model
// turn, the artifact read and the git diff all run there, so rendering
// stays free and the Update goroutine never blocks on any of them.

// narrationEntry is one card's cached model claim. An entry with an
// empty claim is a real answer and is cached like any other — "the diff
// does what the plan asked" is exactly the case that must not be
// re-asked on every frame.
type narrationEntry struct {
	key string
	c   claim
}

// narrationDoneMsg carries a finished pass back to the Update loop. The
// claim is already resolved: an unresolvable one arrives here as a zero
// claim plus the anchor that failed, so the reader can be told once
// rather than on every frame.
type narrationDoneMsg struct {
	id       domain.FeatureID
	key      string
	c        claim
	rejected anchor
	err      error
}

// narrationKey stamps the card state a cached claim belongs to.
//
// The newest event seq is the version stamp — the log is append-only, so
// anything that happened to the card since the claim was written moved
// it. The three blocking counts are not derivable from that: resolving
// a spec thread or a diff comment changes what the stop means (it is
// the difference between "you cannot cross yet" and "decide") while
// appending nothing to the log, and a claim cached across that change
// would describe a card the reader is no longer looking at.
func (m *Shell) narrationKey(in nextInput, id domain.FeatureID) string {
	var newest int64
	for _, ev := range m.cardEvents[id] {
		if ev.Seq > newest {
			newest = ev.Seq
		}
	}
	return strings.Join([]string{
		strconv.FormatInt(newest, 10),
		strconv.Itoa(in.openSpecQs),
		strconv.Itoa(in.openDiffComments),
		strconv.Itoa(len(in.undrafted)),
		string(in.stage),
	}, "|")
}

// cachedCodeVsPlan returns the card's model claim when one is cached for
// exactly this state and this stop wants one.
//
// It reads the cache and never fills it, which is what keeps rendering
// free. A card with no entry — including every card in a process that
// does not drive it — simply has two sentences instead of three.
func (m *Shell) cachedCodeVsPlan(in nextInput, r featureRow) (claim, bool) {
	if !approveShaped(in) {
		return claim{}, false
	}
	e, ok := m.narration[r.F.ID]
	if !ok || e.key != m.narrationKey(in, r.F.ID) || e.c.text == "" {
		return claim{}, false
	}
	return e.c, true
}

// ensureNarration dispatches the code-vs-plan pass for a card that wants
// one and has no fresh cached answer. It returns nil — the common case
// by far — whenever there is nothing to do.
//
// A CARD ANOTHER PROCESS DRIVES NEVER REACHES A MODEL HERE. Its
// envelope belongs to the process that owns it, and spending from a
// surface that is only watching would charge someone else's budget for
// this screen's convenience. Such a card renders whatever is already
// cached and nothing more, which in a process that never drove it means
// the two free sentences.
func (m *Shell) ensureNarration(r featureRow) tea.Cmd {
	if m.engine == nil || r.DrivenAbroad {
		return nil
	}
	// Only for the card whose page is open, and that is a correctness
	// rule as much as a thrift one. featureRow.Events is loaded for the
	// open card and left nil for every other row (msgs.go, deliberately,
	// so drawing the board costs no read per row) — and the cache key is
	// built from the newest event seq, so keying a card whose log was
	// never read would stamp every state of it with the same degenerate
	// key and pin the first claim there forever. The narration is only
	// ever SHOWN on this page too, so nothing is lost by not buying one
	// for a card nobody is looking at.
	if !m.cardOpen || m.selectedID() != r.F.ID {
		return nil
	}
	in := m.nextInputFor(r)
	if !approveShaped(in) {
		return nil
	}
	key := m.narrationKey(in, r.F.ID)
	if e, ok := m.narration[r.F.ID]; ok && e.key == key {
		return nil
	}
	if m.narrating == nil {
		m.narrating = map[domain.FeatureID]string{}
	}
	if m.narrating[r.F.ID] == key {
		return nil
	}
	m.narrating[r.F.ID] = key
	f := r.F
	eng := m.engine
	// The evidence a claim may cite is gathered from the same card, in
	// the same command, right after the pass returns — never from state
	// carried in from render time, which by then can be a frame old.
	checks := make([]string, 0, 4)
	for _, res := range m.checksFor(f) {
		checks = append(checks, res.Name)
	}
	events := m.cardEvents[r.F.ID]
	artifactPath := m.artifactFile(&f)
	// Assigned through the nil check rather than straight across: a nil
	// *Manager placed in an interface is a non-nil interface value, and
	// gatherEvidence's own guard would wave it through into a call on
	// nothing.
	var wt worktreeDiffer
	if m.wt != nil {
		wt = m.wt
	}
	return func() tea.Msg {
		ctx := context.Background()
		text, raw, err := eng.CodeVsPlan(ctx, f)
		if err != nil {
			return narrationDoneMsg{id: f.ID, key: key, err: err}
		}
		if text == "" {
			// Nothing to say, cached as nothing to say. This is the
			// answer that makes the cache worth having: a clean card
			// would otherwise be re-asked at every stop it reaches.
			return narrationDoneMsg{id: f.ID, key: key}
		}
		a, ok := parseAnchor(raw)
		if !ok {
			return narrationDoneMsg{id: f.ID, key: key, rejected: anchor{kind: "?", ref: raw}}
		}
		ev := gatherEvidence(ctx, wt, f, artifactPath, checks, events)
		if !ev.resolves(a) {
			return narrationDoneMsg{id: f.ID, key: key, rejected: a}
		}
		return narrationDoneMsg{id: f.ID, key: key, c: claim{text: text, a: a}}
	}
}

// applyNarration folds a finished pass into the cache.
func (m *Shell) applyNarration(msg narrationDoneMsg) {
	if m.narrating != nil {
		delete(m.narrating, msg.id)
	}
	if msg.err != nil {
		// No sentence, and no notice: the card still has its two free
		// ones, and a stop is not the place to report that an optional
		// extra could not be bought.
		return
	}
	if m.narration == nil {
		m.narration = map[domain.FeatureID]narrationEntry{}
	}
	// Cached either way — including the rejected case, so a model that
	// cites something imaginary is asked once for this state rather than
	// on a loop.
	m.narration[msg.id] = narrationEntry{key: msg.key, c: msg.c}
	if !msg.rejected.empty() {
		// Said once, here, where a generation happened — never from the
		// renderer, which runs every frame. A citation that resolves to
		// nothing is the one failure mode the contract exists to catch,
		// so it is reported rather than swallowed.
		m.notice = noticeMsg{text: string(msg.id) + ": dropped a narration claim citing " +
			sanitize(msg.rejected.String()) + " — nothing on this card matches it"}
	}
}

// evidence is everything an anchor can resolve against, gathered once
// per pass. Resolution is a pure function of it, which is what makes
// invariant 3 testable without a repository.
type evidence struct {
	checks   map[string]bool
	sections map[string]bool
	files    map[string]bool
	events   map[int64]bool
}

// resolves reports whether a names something that exists on this card.
//
// Matching is deliberately forgiving about SPELLING and strict about
// EXISTENCE: a section is matched case-insensitively because that is how
// spec.HeadingLine matches it, and a diff path is matched by suffix
// because a model reading a diff sees "b/lxc/list.go" where the tree
// holds "lxc/list.go". What it never does is accept a name nothing on
// the card carries.
func (e evidence) resolves(a anchor) bool {
	switch a.kind {
	case "check":
		return e.checks[strings.ToLower(strings.TrimSpace(a.ref))]
	case "spec":
		return e.sections[strings.ToLower(strings.TrimSpace(a.ref))]
	case "event":
		n, err := strconv.ParseInt(strings.TrimSpace(a.ref), 10, 64)
		return err == nil && e.events[n]
	case "diff":
		path, _, ok := cutDiffRef(a.ref)
		if !ok {
			return false
		}
		for f := range e.files {
			if f == path || strings.HasSuffix(f, "/"+path) || strings.HasSuffix(path, "/"+f) {
				return true
			}
		}
		return false
	}
	return false
}

// cutDiffRef splits "<path>:<line>" — the line is required, since an
// anchor pointing at a whole file is not pointing at a hunk and the
// contract is that the citation opens somewhere specific.
func cutDiffRef(ref string) (path string, line int, ok bool) {
	i := strings.LastIndex(ref, ":")
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(ref[i+1:]))
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return strings.TrimSpace(ref[:i]), n, true
}

// diffFileRe pulls the post-image path out of a unified diff's file
// headers. "b/" is stripped because that is git's own prefix, not part
// of the path.
var diffFileRe = regexp.MustCompile(`(?m)^\+\+\+ (?:b/)?(.+)$`)

// gatherEvidence reads the card for everything an anchor could name.
// Every failure is silent and empty rather than fatal: a card whose
// diff cannot be read has no diff anchors to resolve, which is the right
// answer, and refusing the whole narration over it would trade a missing
// sentence for a missing sentence plus an error.
func gatherEvidence(ctx context.Context, wt worktreeDiffer, f domain.Feature, artifactPath string, checks []string, events []state.CardEvent) evidence {
	ev := evidence{
		checks:   map[string]bool{},
		sections: map[string]bool{},
		files:    map[string]bool{},
		events:   map[int64]bool{},
	}
	for _, name := range checks {
		ev.checks[strings.ToLower(strings.TrimSpace(name))] = true
	}
	for _, e := range events {
		ev.events[e.Seq] = true
	}
	if artifactPath != "" {
		if raw, err := os.ReadFile(artifactPath); err == nil {
			for _, name := range spec.Headings(string(raw)) {
				ev.sections[strings.ToLower(name)] = true
			}
			// The artifact's own checks block is the other place a check
			// name is real: a check declared but not yet run is still a
			// check this card has.
			if cs, found, _ := spec.ParseChecks(string(raw)); found {
				for _, c := range cs {
					ev.checks[strings.ToLower(strings.TrimSpace(c.Name))] = true
				}
			}
		}
	}
	if wt != nil {
		if diff, err := wt.Diff(ctx, &f); err == nil {
			for _, m := range diffFileRe.FindAllStringSubmatch(diff, -1) {
				ev.files[strings.TrimSpace(m[1])] = true
			}
		}
	}
	return ev
}

// worktreeDiffer is the one method gatherEvidence needs from the
// worktree manager, named so the gathering can be tested against a stub
// diff without a git repository.
type worktreeDiffer interface {
	Diff(ctx context.Context, f *domain.Feature) (string, error)
}

// openCitation opens the nth citation in the card's current narration —
// what alt+<n> does.
//
// The number is the one printed in the sentence, so it counts only
// claims that carry an anchor: a paragraph whose first sentence cites
// nothing still has its cited sentence marked [1].
func (m *Shell) openCitation(r featureRow, n int) tea.Cmd {
	cites := citedClaims(m.cardNarration(m.nextInputFor(r), r))
	if n < 1 || n > len(cites) {
		return nil
	}
	return m.openAnchor(r, cites[n-1].a)
}

// citedClaims is the numbered subset of a narration: the claims that
// carry an anchor, in order. One function so the marks the renderer
// prints and the targets the key opens are counted the same way.
func citedClaims(claims []claim) []claim {
	out := make([]claim, 0, len(claims))
	for _, c := range claims {
		if !c.a.empty() {
			out = append(out, c)
		}
	}
	return out
}

// openAnchor mounts the surface an anchor names, positioned where it
// points.
//
// Each kind lands on a surface that already exists rather than inventing
// a viewer of its own: a check and a section both open the artifact at
// the heading that carries them, a hunk opens the diff tab, and an event
// is a row in the thread — the surface already under the reader — so it
// scrolls there rather than mounting anything.
func (m *Shell) openAnchor(r featureRow, a anchor) tea.Cmd {
	switch a.kind {
	case "spec":
		m.diff = nil
		m.specJump = a.ref
		return m.openSpec(r.F)
	case "check":
		// A check lives in the artifact's checks block, which is inside
		// the verification section — so the citation opens the artifact
		// where the check is declared.
		m.diff = nil
		m.specJump = verificationSection(r.F.Kind)
		return m.openSpec(r.F)
	case "diff":
		if !cardHasDiff(r) {
			m.notice = noticeMsg{text: string(r.F.ID) + ": no diff — " + noDiffReason(r)}
			return nil
		}
		path, line, ok := cutDiffRef(a.ref)
		if !ok {
			return nil
		}
		m.spec = nil
		m.diffJump = diffTarget{path: path, line: line}
		return m.openDiff(r.F)
	case "event":
		n, err := strconv.ParseInt(a.ref, 10, 64)
		if err != nil {
			return nil
		}
		m.spec, m.diff = nil, nil
		if !m.scrollThreadToEvent(r, n) {
			// The claim named a real event — gatherEvidence already checked
			// that — but not one any stretch opens, which is the only shape
			// an event citation is ever generated for. Without this the key
			// would arm an anchor the render can never place, clear it on
			// the very next frame, and answer alt+a with nothing: the same
			// silent drop this whole fix exists to remove, just moved one
			// step earlier. Diff citations already refuse to swallow the
			// key this way (the cardHasDiff branch above); this is that
			// same refusal for the event kind.
			m.notice = noticeMsg{text: string(r.F.ID) + ": nothing on this page opens at that citation"}
		}
		return nil
	}
	return nil
}

// verificationSection names the artifact heading a kind keeps its checks
// under — the same two names requiredSections uses for the landing gate,
// so a citation opens the section the gate actually reads.
func verificationSection(kind domain.Kind) string {
	if kind == domain.KindBug {
		return "Verification"
	}
	return "Verification plan"
}

// diffTarget is a pending diff citation: the post-image path and line a
// claim pointed at, consumed by the diffLoadedMsg handler once the diff
// is actually in hand.
type diffTarget struct {
	path string
	line int
}

func (t diffTarget) empty() bool { return t.path == "" }

// hunkRe reads a unified diff hunk header's post-image start line.
var hunkRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// diffLineFor walks a unified diff and returns the 1-based DIFF line
// index showing the given post-image line of the given file, or 0 when
// the diff does not show it.
//
// The two numberings are different things and conflating them is the
// whole reason this exists: a citation names a line of the FILE, which
// is what a reader of the diff sees in the margin, while diffView
// scrolls by line of the DIFF. Walking is the only honest conversion —
// the offset between them changes at every hunk header and at every
// removed line.
func diffLineFor(diff string, target diffTarget) int {
	if target.empty() || target.line <= 0 {
		return 0
	}
	file, newLine, header := "", 0, 0
	for i, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+++ "):
			file = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l[4:]), "b/"))
			newLine = 0
			if diffPathMatches(file, target.path) {
				header = i + 1
			}
			continue
		case hunkRe.MatchString(l):
			m := hunkRe.FindStringSubmatch(l)
			n, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			newLine = n
			continue
		}
		if newLine == 0 || !diffPathMatches(file, target.path) {
			continue
		}
		// "-" lines exist only in the pre-image and consume no post-image
		// number; every other body line does.
		if strings.HasPrefix(l, "-") {
			continue
		}
		if newLine == target.line {
			return i + 1
		}
		if strings.HasPrefix(l, "+") || strings.HasPrefix(l, " ") || l == "" {
			newLine++
		}
	}
	// The file is in the diff but the line is not in a hunk: land on the
	// file's header rather than nowhere, which is still the right file.
	return header
}

// diffPathMatches compares a diff's own path with a cited one, allowing
// either to be the other's suffix — a model reading a diff may cite
// "b/lxc/list.go", "lxc/list.go" or "list.go" for the same file.
func diffPathMatches(have, want string) bool {
	if have == "" || want == "" {
		return false
	}
	return have == want || strings.HasSuffix(have, "/"+want) || strings.HasSuffix(want, "/"+have)
}

// scrollThreadToEvent points the thread at the event a claim cited,
// reporting whether it found somewhere to point it.
//
// It reuses the anchor the unread-period jump already has (thread.go's
// anchorTo/anchorFrom): that machinery lands a stretch's opening rule at
// the top of the window, which is exactly where an event citation wants
// the reader — an event anchor is only ever generated for an autopilot
// stretch's own opening event (the code-vs-plan pass cites "before that,
// autopilot crossed N gates…" at the seq that opened the stretch it is
// describing), never an arbitrary transcript line. The seq is mapped
// back to its index in the card's log because that is what the render
// compares against, and confirmed against the card's own stretches here
// rather than left for the render to discover: the render's anchorIdx
// stays -1 for an index that opens nothing, and openAnchor needs the
// false back before it decides whether to arm the anchor or say so.
func (m *Shell) scrollThreadToEvent(r featureRow, seq int64) bool {
	for i, ev := range r.Events {
		if ev.Seq != seq {
			continue
		}
		for _, st := range liveStretches(r.F, r.Events, m.ws) {
			if st.from == i {
				m.anchorTo, m.anchorFrom = r.F.ID, i
				return true
			}
		}
		return false
	}
	return false
}
