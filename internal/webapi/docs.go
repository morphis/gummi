package webapi

import "time"

// Spec is GET /api/cards/{id}/spec: the card's document as it stands, with
// its review notes and checks. A card with no document yet answers with
// None set and Why saying so, rather than a 404 the page cannot explain.
type Spec struct {
	None bool   `json:"none,omitempty"`
	Why  string `json:"why,omitempty"`
	// Path is the document's path, relative to the repository.
	Path string `json:"path,omitempty"`
	// Draft marks a document that has not reached its home yet: the
	// pre-worktree draft under .gummi/state/drafts, which the next stage
	// run (or the TUI opening it) promotes.
	Draft bool `json:"draft,omitempty"`
	// Rev is the document's revision: the blob hash the page names in a
	// decision's Against label.
	Rev      string        `json:"rev,omitempty"`
	Title    string        `json:"title,omitempty"`
	Markdown string        `json:"markdown,omitempty"`
	Sections []SpecSection `json:"sections,omitempty"`
	Notes    []SpecNote    `json:"notes,omitempty"`
	Checks   []SpecCheck   `json:"checks,omitempty"`
	// OpenComments counts the notes that hold the gate — the unresolved
	// threads a person started: what "request changes" sends.
	OpenComments int `json:"openComments,omitempty"`
}

// SpecSection is one heading and the line it starts on (1-based).
type SpecSection struct {
	Name string `json:"name"`
	Line int    `json:"line"`
}

// SpecNote is one review note anchored in the document. Line is the
// marker's own line; Anchor is the line it comments on (0: the whole
// document). Author is the marker's author as the grammar reads it —
// "user" for every person, whichever device — and By is the person's
// name when the note carries one.
type SpecNote struct {
	Line     int    `json:"line"`
	Anchor   int    `json:"anchor"`
	Author   string `json:"author"`
	By       string `json:"by,omitempty"`
	Date     string `json:"date"`
	Text     string `json:"text"`
	Resolved bool   `json:"resolved"`
}

// SpecCheck is one gummi-checks entry and its last result on this branch.
type SpecCheck struct {
	Name string        `json:"name"`
	Cmd  string        `json:"cmd"`
	Last *CheckOutcome `json:"last,omitempty"`
	// Excused marks a check that was already failing when the branch was
	// cut: verify writes it off rather than holding the card to it.
	Excused bool `json:"excused,omitempty"`
	// ExcusedOn is the commit an excused check was measured failing on:
	// the excusal is a claim about that commit, re-measured when the
	// card's base moves. Empty when unrecorded.
	ExcusedOn string `json:"excusedOn,omitempty"`
}

// CheckOutcome is a check's last result.
type CheckOutcome struct {
	OK bool      `json:"ok"`
	At time.Time `json:"at"`
}

// SpecNoteRequest is POST /api/cards/{id}/spec/notes: a note on one line.
// The server writes it as the person's (author "user", By their name).
// Answers Spec.
type SpecNoteRequest struct {
	Line int    `json:"line"`
	Text string `json:"text"`
	// Attachments are the ids of images (already uploaded via POST
	// /api/attachments) to link into the note — stored by reference, so
	// every later stage that reads the spec sees them too.
	Attachments []string `json:"attachments,omitempty"`
}

// SpecResolveRequest is POST /api/cards/{id}/spec/notes/resolve: close
// the note at Line, which must still be the note the page showed (Author
// and Date as served), or the answer is 409 "moved". Answers Spec.
type SpecResolveRequest struct {
	Line   int    `json:"line"`
	Author string `json:"author"`
	Date   string `json:"date"`
	Reason string `json:"reason,omitempty"`
}

// Diff is GET /api/cards/{id}/diff: the card's branch against its base,
// with the review comments anchored in it.
type Diff struct {
	// Base is the branch the card is measured against; BaseRev is the
	// commit the diff starts from (its merge-base with the branch).
	Base    string `json:"base"`
	BaseRev string `json:"baseRev,omitempty"`
	// Why says why there is no diff to show: no worktree yet, or no
	// change on the branch.
	Why string `json:"why,omitempty"`
	// Since echoes ?since=<rev>: files and lines changed after that
	// commit are marked, for "since your review".
	Since string `json:"since,omitempty"`
	// Rev is the branch head the diff was taken at. A page holding an
	// older Rev shows "new commit" rather than swapping the diff under a
	// reader (§20.1).
	Rev   string     `json:"rev"`
	Files []DiffFile `json:"files"`
	// Annotations are the diff comments, anchored or orphaned.
	Annotations []Annotation `json:"annotations"`
	// PendingComments counts the unresolved ones: what a send-back carries.
	PendingComments int `json:"pendingComments"`
}

// DiffFile is one file's change.
type DiffFile struct {
	Path string `json:"path"`
	// OldPath is set for a rename or copy.
	OldPath string `json:"oldPath,omitempty"`
	// Status is "modified", "added", "deleted", "renamed" or "copied".
	Status string `json:"status"`
	// Binary files have no hunks.
	Binary bool   `json:"binary,omitempty"`
	Add    int    `json:"add"`
	Del    int    `json:"del"`
	Hunks  []Hunk `json:"hunks"`
	// Since marks a file changed after the ?since= commit.
	Since bool `json:"since,omitempty"`
}

// Hunk is one @@ block.
type Hunk struct {
	Header string     `json:"header"`
	Lines  []DiffLine `json:"lines"`
}

// DiffLine is one line of a hunk. Idx is its index into the raw diff's
// lines: the coordinate an annotation is made against.
type DiffLine struct {
	// T is " ", "+" or "-".
	T    string `json:"t"`
	Old  int    `json:"old,omitempty"`
	New  int    `json:"new,omitempty"`
	Text string `json:"text"`
	Idx  int    `json:"idx"`
	// Since marks an added line written after the ?since= commit.
	Since bool `json:"since,omitempty"`
}

// Annotation is one diff comment.
type Annotation struct {
	ID   int64  `json:"id"`
	File string `json:"file"`
	// Idx is the raw diff line it anchors to, -1 when the content it was
	// made against is gone (an orphan, still listed).
	Idx     int    `json:"idx"`
	Excerpt string `json:"excerpt"`
	Comment string `json:"comment"`
	// By is who wrote it when that is known: a pull request reviewer's
	// login, or the named person who wrote it at the web face. A comment
	// made at the terminal carries no author.
	By string `json:"by"`
	// Source is "gummi" for a comment made at the board, "pr" for a
	// review thread pulled from the linked pull request.
	Source   string    `json:"source"`
	Resolved bool      `json:"resolved"`
	At       time.Time `json:"at,omitzero"`
}

// AnnotationRequest is POST /api/cards/{id}/diff/annotations. Answers Diff.
// Text, when sent, is the line's text as the page showed it (without its
// +/-/space marker): a diff that moved under the page since is refused
// with 409 "moved" instead of commenting on whatever Idx names now.
type AnnotationRequest struct {
	Idx     int    `json:"idx"`
	Comment string `json:"comment"`
	Text    string `json:"text,omitempty"`
}

// ChangesRequest is the optional body of POST /api/cards/{id}/spec/changes
// and /diff/changes. A request that would send the card back to an earlier
// stage is answered with a "confirm" question first; Confirm is the token
// that question came with, sent back once the person has said yes.
type ChangesRequest struct {
	Confirm string `json:"confirm,omitempty"`
}

// AnnotationEditRequest is PATCH /api/cards/{id}/diff/annotations/{aid}:
// the comment's new words. Where it is anchored does not change, and a
// comment pulled from a pull request is GitHub's words, so it is refused.
// Answers Diff.
type AnnotationEditRequest struct {
	Comment string `json:"comment"`
}

// AnnotationResolveRequest is the optional body of POST
// /api/cards/{id}/diff/annotations/{aid}/resolve: Resolved false opens
// the comment again. With no body the comment is resolved. Answers Diff,
// as does DELETE /api/cards/{id}/diff/annotations/{aid}.
type AnnotationResolveRequest struct {
	Resolved *bool `json:"resolved,omitempty"`
}

// PR is GET /api/cards/{id}/pr: the linked pull request as GitHub has it.
// The read never writes to GitHub; PushCommand is the command a person can
// run themselves, and Publish the acts a person may start instead (§22).
// POST /api/cards/{id}/pr/pull reads the PR's review threads onto the diff
// as comments; it runs on the board and reports back as a toast and a card
// change.
type PR struct {
	Linked   bool       `json:"linked"`
	Ref      string     `json:"ref,omitempty"`
	URL      string     `json:"url,omitempty"`
	State    string     `json:"state,omitempty"`
	Threads  []PRThread `json:"threads,omitempty"`
	Comments []PRNote   `json:"comments,omitempty"`
	// Checks are the PR's checks on HeadSHA, in GitHub's order. Read only:
	// the card's "prchecks" action is what hands the failing ones to its
	// session.
	Checks      []PRCheck `json:"checks,omitempty"`
	PushCommand string    `json:"pushCommand,omitempty"`
	// Comments counts the PR's conversation comments, as gh reports them.
	CommentCount int    `json:"commentCount,omitempty"`
	HeadSHA      string `json:"headSha,omitempty"`
	// Fetched is when GitHub was last asked; the server answers from a
	// short cache in between.
	Fetched time.Time `json:"fetched,omitzero"`
	Error   string    `json:"error,omitempty"`
	// Publish is the publish strip; nil where the card has no branch to
	// publish at all.
	Publish *PublishOffer `json:"publish,omitempty"`
}

// PRThread is one unresolved review thread (GitHub's resolved ones are
// not fetched). Line is the new-side line the thread is on, read from its
// diff hunk; 0 when the hunk does not say.
type PRThread struct {
	Path     string   `json:"path,omitempty"`
	Line     int      `json:"line,omitempty"`
	Resolved bool     `json:"resolved"`
	Outdated bool     `json:"outdated,omitempty"`
	Notes    []PRNote `json:"notes"`
}

// PRCheck is one of the PR's checks. Bucket is gh's word for where it
// stands: pass, fail, pending, skipping or cancel.
type PRCheck struct {
	Name     string `json:"name"`
	Workflow string `json:"workflow,omitempty"`
	Bucket   string `json:"bucket"`
	URL      string `json:"url,omitempty"`
}

// PRNote is one comment in a thread, or a top-level PR comment.
type PRNote struct {
	Author string    `json:"author"`
	Body   string    `json:"body"`
	At     time.Time `json:"at,omitzero"`
}
