package engine

import (
	"context"
	"regexp"
	"strings"

	"github.com/morphis/gummi/internal/domain"
)

// The code-vs-plan sentence is the one claim in a card's narration that
// is NOT derivable from the event log, because nothing in gummi compares
// a diff to a plan's steps. Everything else the narration says — the
// verdict, the failing check, the blocker, the gates autopilot crossed —
// is already recorded somewhere and is folded out for free (ui/
// narration.go). This one costs a model turn, which is why it runs only
// at approve-shaped stops and only from cache.
//
// IT SHIPS UNDER A CONTRACT, and the contract is the reason it is safe
// to put a model's prose above a decision. The reply must carry an
// anchor naming something openable — a check, a diff hunk, an artifact
// section, a log event — and the caller resolves that anchor against the
// card before the sentence is ever rendered. An anchor that resolves to
// nothing takes the sentence with it. Hallucinated evidence therefore
// cannot reach a screen: not because the model is asked nicely, but
// because a claim with no resolvable evidence is discarded by the same
// code that admits one with it.

// codeVsPlanIntro states the question. It is deliberately narrow: not
// "review this diff" — the critique pass already did that, and paying a
// second model to disagree with it is exactly the "new way to spend
// money to be told what gummi already knows" DESIGN §6.3 warns off — but
// the single comparison no stage makes, between what the plan promised
// and what the branch contains.
const codeVsPlanIntro = `Compare what this card's branch actually does against what its artifact
asked for. Read the artifact's plan sections and the branch's own diff
(git diff against the base branch).

Say ONE sentence about the gap, and only if there is a real one worth a
reader's attention at a decision: something the plan asked for that the
diff does not do, or something the diff does that the plan never asked
for. Do not summarise the diff, do not review its quality, and do not
repeat what the checks already report.`

// codeVsPlanClose is the reply contract. Both lines or neither: a claim
// with no anchor is discarded exactly like an anchor that resolves to
// nothing, so there is no reward for answering half of it.
const codeVsPlanClose = "Reply with exactly these two lines and nothing else:\n\n" +
	"CLAIM: <your one sentence>\n" +
	"ANCHOR: <one anchor, from the list below>\n\n" +
	"An anchor must be ONE of:\n\n" +
	"  diff:<path>:<line>   a line in the diff your sentence is about\n" +
	"  spec:<section>       an artifact section heading, spelled exactly\n" +
	"  check:<name>         a named check from the artifact's checks block\n\n" +
	"The anchor is checked against the real card before your sentence is\n" +
	"shown. An anchor naming something that does not exist DISCARDS the\n" +
	"sentence, so cite something you actually read.\n\n" +
	"If the diff does what the plan asked, or you cannot cite a specific\n" +
	"place, reply with the single line CLAIM: none — saying nothing is a\n" +
	"good answer and is much better than a vague one."

// claimRe and anchorRe read the two lines. The anchor's capture runs to
// the end of the line rather than to the first space, because a section
// heading and a check name both legitimately contain spaces
// ("Verification plan", "go vet") — an anchor pattern that stopped at
// the space would silently reject exactly the two kinds a reader is most
// likely to be cited to.
var (
	claimRe  = regexp.MustCompile(`(?im)^\s*CLAIM:\s*(.+?)\s*$`)
	anchorRe = regexp.MustCompile(`(?im)^\s*ANCHOR:\s*(.+?)\s*$`)
)

// codeVsPlanPrompt renders the turn for one card.
func codeVsPlanPrompt(f domain.Feature) string {
	var b strings.Builder
	b.WriteString(codeVsPlanIntro + "\n\n")
	b.WriteString("The card is a " + kindNoun(f.Kind) + ", currently at the " + string(f.Stage) + " stage.\n\n")
	b.WriteString(codeVsPlanClose)
	return b.String()
}

// parseCodeVsPlan pulls the claim and its anchor out of a reply. Both or
// nothing: a claim with no anchor, an anchor with no claim, and the
// explicit "none" are all ("", "").
func parseCodeVsPlan(text string) (claim, anchor string) {
	cm := claimRe.FindAllStringSubmatch(text, -1)
	am := anchorRe.FindAllStringSubmatch(text, -1)
	if len(cm) == 0 || len(am) == 0 {
		return "", ""
	}
	claim = strings.TrimSpace(cm[len(cm)-1][1])
	anchor = strings.TrimSpace(am[len(am)-1][1])
	if claim == "" || strings.EqualFold(claim, "none") || anchor == "" {
		return "", ""
	}
	return claim, anchor
}

// CodeVsPlan runs one scribe pass comparing the card's branch to its
// artifact and returns a single claim with the anchor that backs it.
//
// ("", "", nil) is the ordinary answer and is not a failure: a diff that
// does what the plan asked has nothing to add above a decision, and a
// sentence saying so would be a row of prose telling a reader what the
// green checks already told them. The caller MUST still resolve the
// anchor before rendering — this returns what the model said, not what
// is true.
func (e *Engine) CodeVsPlan(ctx context.Context, f domain.Feature) (claim, anchor string, err error) {
	text, err := e.oneShot(ctx, f, codeVsPlanPrompt(f),
		"Answer in the two-line format the prompt specifies; nothing else is read.")
	if err != nil {
		return "", "", err
	}
	claim, anchor = parseCodeVsPlan(text)
	return claim, anchor, nil
}
