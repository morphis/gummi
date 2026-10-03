package webapi

// Memory is GET /api/cards/{id}/memory: a freeform card's project memory
// as it stands — the workspace's global memory and the card's own session
// memory, the documents its session reads at spawn (the card's own two
// filled as it works), shown the way a workflow card's spec is. A
// workflow card answers None
// with Why: its document is its spec, and memory belongs to no stage of
// it.
type Memory struct {
	None bool   `json:"none,omitempty"`
	Why  string `json:"why,omitempty"`
	// Dir is the memory directory, relative to the workspace root —
	// where the files live for hand edits.
	Dir string `json:"dir,omitempty"`
	// Global is the workspace's global memory: every freeform session
	// here reads it at spawn; it is filled by hand, and sessions record
	// what should move up to it in their own memory.
	Global MemoryDoc `json:"global"`
	// Plan and DeadEnds are the card's own session memory: the working
	// plan, kept current, and what it tried that failed.
	Plan     MemoryDoc `json:"plan"`
	DeadEnds MemoryDoc `json:"deadEnds"`
}

// MemoryDoc is one memory document: its path, relative to the workspace
// root, and its content. Empty text is a document nothing has written yet.
type MemoryDoc struct {
	Path string `json:"path,omitempty"`
	Text string `json:"text,omitempty"`
}
