package domain

// PlannedFile is one entry of a plan's file manifest: a path the plan
// expects the implementation to touch, and one line on what it is for.
//
// The manifest exists because gummi's implementer was measured making its
// first edit at turn 32 of 97, while a bare agent given no spec at all
// first edits at turn 20-31 of 62-72 — it explored MORE than an agent
// working blind, despite a spec that had already located every file. The
// architect knew the answer and buried it in prose the implementer then
// re-derived from the repo.
//
// It is deliberately a starting point and not a closed set. A manifest
// that is wrong or incomplete is worse than none if the implementer
// treats it as exhaustive, so the contract every consumer states is the
// same: open these first, and change whatever else the work actually
// needs.
type PlannedFile struct {
	// Path is repo-relative, as the plan wrote it.
	Path string `yaml:"path"`
	// Role is one line on why this file is in the manifest — what the
	// change to it is for. Optional; a bare path is still useful.
	Role string `yaml:"role,omitempty"`
	// New marks a file the plan expects to create rather than edit, so a
	// reader does not go looking for something that is not there yet.
	New bool `yaml:"new,omitempty"`
}
