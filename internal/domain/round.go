package domain

// RoundKind distinguishes which automatic loop a persisted round count
// belongs to. It keys the rounds store, the rounds row, and the
// driver/TUI fast-path maps — the one seam every round-counter consumer
// shares.
type RoundKind string

const (
	// RoundKindPlan is the plan→critique→replan loop's round kind.
	RoundKindPlan RoundKind = "plan"
	// RoundKindReview is the review→fix→review loop's round kind. The
	// research slice's review loop reuses this kind verbatim — it is not
	// a distinct research round kind.
	RoundKindReview RoundKind = "review"
)

// Valid reports whether k is a recognized round kind.
func (k RoundKind) Valid() bool {
	return k == RoundKindPlan || k == RoundKindReview
}
