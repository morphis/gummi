package webapi

import "time"

// Log is GET /api/cards/{id}/log: the card's own commits, oldest first,
// as branchlog folds them — the same rows the terminal's log tab and
// `gummi log` draw.
type Log struct {
	Base string `json:"base"`
	// Head is the branch tip the rows end at. A plan sent back carries it
	// so a branch that moved in between is refused, not rewritten.
	Head    string      `json:"head,omitempty"`
	Commits []LogCommit `json:"commits"`
	// Why is the reason there is nothing to read or nothing to rewrite;
	// Rewritable false with Commits present is a read-only log.
	Why        string `json:"why,omitempty"`
	Rewritable bool   `json:"rewritable"`
	// PushCommand is what publishes a rewrite of pushed commits. gummi
	// never runs it.
	PushCommand string `json:"pushCommand,omitempty"`
	// Signable: commits are signed here and some of these are not, so a
	// plan may ask for them signed.
	Signable bool `json:"signable,omitempty"`
}

// LogCommit is one commit of a Log.
type LogCommit struct {
	SHA     string    `json:"sha"`
	Short   string    `json:"short"`
	Subject string    `json:"subject"`
	Body    string    `json:"body,omitempty"`
	Author  string    `json:"author"`
	At      time.Time `json:"at"`
	Files   int       `json:"files"`
	Add     int       `json:"add"`
	Del     int       `json:"del"`
	// Checkpoint: gummi made this commit itself between turns or stages.
	Checkpoint bool `json:"checkpoint,omitempty"`
	// Pushed: the branch's upstream already has it.
	Pushed bool `json:"pushed,omitempty"`
	// Signed: the commit carries a signature.
	Signed bool `json:"signed,omitempty"`
	// Warning names agent-authorship metadata in the message.
	Warning string `json:"warning,omitempty"`
}

// RewriteRequest is POST /api/cards/{id}/log/plan (a dry run) and
// POST /api/cards/{id}/log/rewrite: the branch as it should read
// afterwards. Groups are contiguous runs of the current commits, oldest
// first, each becoming one commit; every commit is in exactly one.
type RewriteRequest struct {
	Head   string         `json:"head"`
	Groups []RewriteGroup `json:"groups"`
	// AcknowledgePushed is the yes to a rewrite that replaces commits the
	// remote already has, which will need a force push.
	AcknowledgePushed bool `json:"acknowledgePushed,omitempty"`
	// Sign makes again every commit from the first unsigned one up,
	// signed. Refused where commits are not signed.
	Sign bool `json:"sign,omitempty"`
}

// RewriteGroup is one commit of the result: the commits it replaces and
// the message it carries. A one-commit group without a message keeps the
// commit as it is.
type RewriteGroup struct {
	Commits []string `json:"commits"`
	Message string   `json:"message,omitempty"`
}

// RewritePreview is the answer to a plan: what the branch would read
// like, without anything having moved.
type RewritePreview struct {
	Commits []LogCommit `json:"commits"`
	Changed int         `json:"changed"`
	// Pushed: a commit the remote has would be replaced, so the request
	// needs acknowledgePushed and the person a force push afterwards.
	Pushed      bool   `json:"pushed"`
	Noop        bool   `json:"noop"`
	PushCommand string `json:"pushCommand,omitempty"`
}

// RewriteResult is the answer to a rewrite that ran.
type RewriteResult struct {
	Head string `json:"head"`
	// PushCommand is set when pushed commits were replaced.
	PushCommand string `json:"pushCommand,omitempty"`
	Log         Log    `json:"log"`
}

// CommitDiff is GET /api/cards/{id}/log/{sha}: one commit's patch, in the
// shapes the diff tab draws (without comments: those are on the branch's
// diff, not on a commit).
type CommitDiff struct {
	SHA   string     `json:"sha"`
	Files []DiffFile `json:"files"`
}
