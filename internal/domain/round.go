package domain

// RoundKind distinguishes which automatic loop a persisted round count
// belongs to. It keys the rounds store, the rounds row, and the
// driver/TUI fast-path maps — the one seam every round-counter consumer
// shares.
type RoundKind string

const (
	// RoundKindPlan is the plan→critique→replan loop's round kind. It
	// keeps its OWN separate budget (verdict.MaxRounds) — plan rounds are
	// never counted against RoundKindCorrective.
	RoundKindPlan RoundKind = "plan"
	// RoundKindReview is the review→fix→review loop's round kind. The
	// research slice's review loop reuses this kind verbatim — it is not
	// a distinct research round kind.
	RoundKindReview RoundKind = "review"
	// RoundKindCorrective is the unified budget for everything that
	// bounces a card back for another automatic pass once it is past the
	// design gates: review→fix rounds, verify bounces, and conflict
	// handoffs. It is deliberately one shared counter rather than three —
	// they are all "the loop tried again" — and it is distinct from
	// RoundKindPlan's own cap, which governs the plan→critique loop and
	// is never folded into this one.
	RoundKindCorrective RoundKind = "corrective"
)

// Valid reports whether k is a recognized round kind.
func (k RoundKind) Valid() bool {
	return k == RoundKindPlan || k == RoundKindReview || k == RoundKindCorrective
}
