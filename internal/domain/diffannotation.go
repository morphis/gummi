package domain

import "time"

// DiffAnnotation is a line comment on a feature's worktree diff. It is
// anchored to the surrounding diff content by hash (DESIGN §6.1), not a
// line number, so it survives minor rebases; File and Excerpt let it
// degrade to a file-level note when the anchor orphans. Resolved
// annotations no longer block the gate.
type DiffAnnotation struct {
	ID        int64
	Feature   FeatureID
	File      string // new-side path the line belongs to
	Anchor    string // content hash of the line + surrounding context
	Excerpt   string // the commented line's text, for display and degraded matching
	Comment   string
	Resolved  bool
	CreatedAt time.Time
}
