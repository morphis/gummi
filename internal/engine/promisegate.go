package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// The promises floor: a card does not finish while a commitment its own
// plan made is unanswered or unmet.
//
// gummi's quality floor has always asked whether the stages RAN — plan
// wrote a plan, a critique read the diff, the checks exited zero. It never
// asked whether what the plan PROMISED is in the branch. Two drives of the
// same feature against canonical/lxd showed both halves of the gap:
//
//   - one plan pinned the golden `TestParse_Error["name eq c1)"] =
//     "unbalanced parentheses"`; the shipped test table did not contain
//     that input, the parser accepted it, and three critique rounds plus
//     verify all passed;
//   - the other plan carried the card's own stated invariant — every
//     filter valid today keeps working — and shipped a branch that
//     rejects three such filters, because the critique labelled the
//     contradiction "non-blocking" and verify recorded it as a caveat.
//
// Both are mechanically checkable, and this is where they are checked. A
// golden names an input; that input must appear somewhere on the branch.
// An invariant gets an id; verify must answer it by id. Neither asks a
// model to judge anything at gate time — the floor reads the artifact and
// greps the worktree.
const (
	// promiseGrepTimeout bounds the one search per golden. A repository
	// too large or too slow to answer in this window yields no verdict for
	// that golden, and an unanswerable search never blocks a card.
	promiseGrepTimeout = 10 * time.Second
	// maxReportedPromises caps how many unmet promises a single block
	// message names, so a plan with forty goldens produces a message a
	// person can act on rather than a wall.
	maxReportedPromises = 5
)

// promiseReport is what the floor found, for the gate and for the message.
type promiseReport struct {
	// Unanswered are invariants verify never gave a verdict for.
	Unanswered []spec.Invariant
	// Failed are invariants verify answered fail/blocked.
	Failed []spec.Invariant
	// Unpinned are goldens whose input appears nowhere on the branch.
	Unpinned []spec.Golden
}

// blocks reports whether anything here holds the gate shut.
func (r promiseReport) blocks() bool {
	return len(r.Unanswered) > 0 || len(r.Failed) > 0 || len(r.Unpinned) > 0
}

// reason renders the block into the sentence a person (or a driver's
// NDJSON consumer) reads. It names the promise, not the machinery.
func (r promiseReport) reason() string {
	var parts []string
	if n := len(r.Failed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d invariant%s the plan promised %s reported FAIL by verify (%s)",
			n, plural(n), wasWere(n), promiseIDs(r.Failed)))
	}
	if n := len(r.Unanswered); n > 0 {
		parts = append(parts, fmt.Sprintf("%d invariant%s %s never answered (%s) — verify must write one "+
			"`INV-n: pass` or `INV-n: fail` line per invariant",
			n, plural(n), wasWere(n), promiseIDs(r.Unanswered)))
	}
	if n := len(r.Unpinned); n > 0 {
		var quoted []string
		for i, g := range r.Unpinned {
			if i == maxReportedPromises {
				quoted = append(quoted, fmt.Sprintf("and %d more", len(r.Unpinned)-i))
				break
			}
			quoted = append(quoted, fmt.Sprintf("%q", g.Literal))
		}
		parts = append(parts, fmt.Sprintf("%d golden%s the plan promised %s pinned by nothing on the branch "+
			"(%s) — add the case, or strike the golden from the plan if it was wrong",
			n, plural(n), isAre(n), strings.Join(quoted, ", ")))
	}
	return "the plan's own promises are not met: " + strings.Join(parts, "; ") + "."
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func wasWere(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func promiseIDs(in []spec.Invariant) string {
	ids := make([]string, 0, len(in))
	for _, i := range in {
		ids = append(ids, i.ID)
	}
	return strings.Join(ids, ", ")
}

// checkPromises reads the artifact at specPath and reports which of its
// promises the branch in workDir does not keep.
//
// Everything here is best-effort in one direction only: an artifact that
// cannot be read, or a search that cannot run, yields an EMPTY report
// rather than a block. A floor that fires because a grep timed out would
// be worse than no floor — it would teach its way around itself.
func checkPromises(ctx context.Context, specPath, workDir string) promiseReport {
	raw, err := os.ReadFile(specPath)
	if err != nil {
		return promiseReport{}
	}
	content := string(raw)

	var rep promiseReport
	verdicts := spec.InvariantVerdicts(content)
	for _, inv := range spec.Invariants(content) {
		switch verdicts[inv.ID] {
		case "pass":
		case "fail", "blocked":
			rep.Failed = append(rep.Failed, inv)
		default:
			rep.Unanswered = append(rep.Unanswered, inv)
		}
	}
	for _, g := range spec.Goldens(content) {
		if g.Literal == "" {
			// A golden that quotes nothing is prose. The plan rubric asks
			// for an input and an expected value; a line that names
			// neither cannot be checked and is not held against the card.
			continue
		}
		if pinned, known := goldenIsPinned(ctx, workDir, g.Literal); known && !pinned {
			rep.Unpinned = append(rep.Unpinned, g)
		}
	}
	return rep
}

// goldenIsPinned reports whether literal appears in any tracked file of
// the branch in workDir. known is false when the search could not be run
// at all, which the caller reads as "no opinion".
//
// Tracked files, not test files: a golden pinned by a doc example or a
// fixture is still pinned, and guessing at every ecosystem's test-file
// naming would turn a floor into a lottery. The search is for the golden's
// INPUT, which is the part a test table actually contains.
func goldenIsPinned(ctx context.Context, workDir, literal string) (pinned, known bool) {
	if workDir == "" || strings.TrimSpace(literal) == "" {
		return false, false
	}
	ctx, cancel := context.WithTimeout(ctx, promiseGrepTimeout)
	defer cancel()
	// --fixed-strings: a golden is a literal, and a golden containing a
	// regexp metacharacter — "(a eq 1)" — is the common case, not the odd
	// one. -I skips binaries.
	cmd := exec.CommandContext(ctx, "git", "grep", "--fixed-strings", "-I", "-l", "-e", literal)
	cmd.Dir = workDir
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return false, false
	}
	if err != nil {
		// git grep exits 1 for "no match", which is an answer; any other
		// exit is a search that did not happen.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return false, true
		}
		return false, false
	}
	return strings.TrimSpace(string(out)) != "", true
}

// promiseGateBlocksAdvance is the verify→done half of the floor: the last
// gate before a card is finished re-reads its promises against the branch
// as it now stands.
//
// It mirrors omissionGateBlocksAdvance's shape, including its
// skip-on-error behaviour, and like that gate it runs before any
// merge/landing work so a blocked card has changed nothing.
func (e *Engine) promiseGateBlocksAdvance(ctx context.Context, f domain.Feature) (string, bool) {
	if f.Kind == domain.KindResearch || f.IsGoal() {
		// Research writes a document and has its own citation floor; a
		// goal is judged by its done-when list, which is this same idea
		// with its own machinery.
		return "", false
	}
	workDir, specPath, err := e.locate(ctx, f)
	if err != nil {
		return "", false
	}
	rep := checkPromises(ctx, specPath, workDir)
	if !rep.blocks() {
		return "", false
	}
	return rep.reason(), true
}

// gatePromiseVerdict stamps the live verify session's verdict floor when
// the card's promises are unmet, so an autonomous run is told at the
// verdict rather than at the gate behind it.
//
// Same one-way shape as gateVerifyVerdict: it only ever downgrades a raw
// pass, and a card with no promises in its artifact is untouched.
func (e *Engine) gatePromiseVerdict(s *Session) {
	if s == nil || s.Feature.Stage != domain.StageVerify {
		return
	}
	if s.Feature.Kind == domain.KindResearch || s.Feature.IsGoal() {
		return
	}
	ctx := context.Background()
	workDir, specPath, err := e.locate(ctx, s.Feature)
	if err != nil {
		// No worktree to search is no opinion, not a block.
		return
	}
	if p := s.SpecPath(); p != "" {
		specPath = p
	}
	rep := checkPromises(ctx, specPath, workDir)
	if !rep.blocks() {
		return
	}
	s.setVerdictFloor("blocked", rep.reason())
	s.appendActivity("Pass downgraded to blocked: " + rep.reason())
}

// changedPaths lists the paths f's branch changes, or nil when the
// worktree cannot answer. Deleted paths are included: "no check exercised
// this deletion" is a real thing to say about a removal.
func (e *Engine) changedPaths(f domain.Feature) []string {
	ctx, cancel := context.WithTimeout(context.Background(), promiseGrepTimeout)
	defer cancel()
	mgr, err := e.mgr(ctx, &f)
	if err != nil || mgr == nil {
		return nil
	}
	files, err := mgr.ChangedFiles(ctx, &f)
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(files))
	for _, cf := range files {
		paths = append(paths, cf.Path)
	}
	return paths
}

// maxInventoryPaths bounds the file list the verify kickoff carries. A
// branch past this size is one whose coverage is a question about
// packages rather than files, and the list would crowd the prompt.
const maxInventoryPaths = 60

// changedFileInventory renders the branch's changed paths for the verify
// kickoff, with the instruction that makes the list actionable. Empty
// when there is nothing to list.
func changedFileInventory(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nThis branch changes these files:\n")
	for i, p := range paths {
		if i == maxInventoryPaths {
			fmt.Fprintf(&b, "  …and %d more\n", len(paths)-i)
			break
		}
		fmt.Fprintf(&b, "  %s\n", p)
	}
	b.WriteString("For each one, know whether a check that ran actually exercised it. " +
		"Any file no check exercised — a tree this container's toolchain cannot " +
		"build, a suite only CI runs — gets a line reading `UNPROVEN: <path> — <why>` " +
		"in the Verification plan. It does not block the card; it is how the person " +
		"merging this branch learns which parts nothing ran.\n")
	return b.String()
}

// grammarSeamWords are the marks of a change whose surface is "which
// inputs are legal" — a parser, a tokenizer, a validator, a matcher.
//
// The list is deliberately short and matched against the artifact's own
// prose and the branch's filenames, because that is where the words
// appear: the lxd drive's two branches changed clause.go and match.go,
// whose names say nothing, while both plans said "parser" and "tokenize"
// in their first paragraph.
var grammarSeamWords = []string{
	"parser", "parse", "tokeniz", "tokenis", "lexer", "grammar",
	"syntax", "validator", "validate input", "expression",
}

// looksLikeGrammarSeam reports whether this card changes what inputs a
// program accepts. False positives cost one paragraph of prompt; false
// negatives cost what both lxd branches cost — a malformed input silently
// accepted, past three critique rounds and a verify.
func looksLikeGrammarSeam(content string, paths []string) bool {
	hay := strings.ToLower(content + "\n" + strings.Join(paths, "\n"))
	for _, w := range grammarSeamWords {
		if strings.Contains(hay, w) {
			return true
		}
	}
	return false
}

// grammarSweepHint asks verify for the one thing neither a plan's goldens
// nor a critique's re-derivation produced on either lxd drive: an input
// the card had not already thought of.
//
// Every stage on those cards read the same artifact and re-derived the
// same case list from it, so three critique rounds bought three
// re-derivations and zero new inputs. Both branches shipped accepting a
// malformation — a dangling operator inside a group; a stray closing
// paren — that neither plan had listed. The sweep is mechanical, it needs
// no model judgement, and both trees it compares are already on disk.
func grammarSweepHint(content string, paths []string) string {
	if !looksLikeGrammarSeam(content, paths) {
		return ""
	}
	return "\nThis change decides which inputs are legal, so the tests it shipped are " +
		"the cases its own author thought of. Before your verdict, spend a few minutes " +
		"on the ones nobody wrote down: take the alphabet this grammar is built from — " +
		"its operators, delimiters, quotes, and a valid term — and enumerate short " +
		"combinations of them, including the malformed ones (an operator with nothing " +
		"after it, an unmatched delimiter, an empty group, a delimiter inside a quoted " +
		"value). Two properties, both cheap to assert and neither requiring you to " +
		"decide what the right answer is: nothing panics, and every input that was " +
		"accepted by the pre-change code is still accepted with the same result, " +
		"unless one of the plan's invariants says it changes. The pre-change tree is " +
		"one `git worktree add` away from this branch's merge-base — use it rather " +
		"than reasoning about what the old code did. Record what you found; a " +
		"malformation the shipped code accepts is a fail, not a note.\n"
}
