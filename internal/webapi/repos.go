package webapi

import "time"

// Repos is GET /api/repos: every repository the workspace manages, each
// with its local branches sorted by who holds them (internal/repoview).
type Repos struct {
	Repos []Repo `json:"repos"`
	// Discovered marks a set found by scanning the workspace rather than
	// fixed in config, so a clone made since shows up on the next read.
	Discovered bool `json:"discovered,omitempty"`
	// Ambiguous lists the folder names discovery handed to nobody
	// because several checkouts share them.
	Ambiguous []RepoClash `json:"ambiguous,omitempty"`
}

// RepoClash is one folder name several checkouts share.
type RepoClash struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
}

// Repo is one managed repository.
type Repo struct {
	// Name is the configured name; empty is the workspace default.
	Name string `json:"name"`
	// Path is its root, relative to the workspace ("." for the root).
	Path string `json:"path"`
	// Base is the branch its main checkout has out; empty when detached.
	Base string `json:"base,omitempty"`
	// Origin is its `origin` remote's URL, credentials masked.
	Origin string `json:"origin,omitempty"`
	// Remote reports it has a remote to fetch from; Remotes lists them.
	Remote  bool         `json:"remote,omitempty"`
	Remotes []RepoRemote `json:"remotes,omitempty"`
	// RemoteBranches are the remote branches as last fetched
	// ("origin/main"): what a local branch may be set to track.
	RemoteBranches []string `json:"remoteBranches,omitempty"`
	// Dirty reports uncommitted tracked changes in the main checkout.
	Dirty bool `json:"dirty,omitempty"`
	// Fetched is when it last fetched; absent when it never has.
	Fetched time.Time `json:"fetched,omitzero"`
	// Cards counts the cards working in it that are not done.
	Cards    int          `json:"cards"`
	Branches []RepoBranch `json:"branches"`
	// Error says why it could not be read, in place of its branches.
	Error string `json:"error,omitempty"`
}

// RepoRemote is one of a repository's remotes.
type RepoRemote struct {
	Name string `json:"name"`
	// URL is where it fetches from and PushURL where it pushes to, set
	// only when that differs. A credential in either is masked, and
	// Secret says so: the text shown is then not the URL configured.
	URL     string `json:"url"`
	PushURL string `json:"pushUrl,omitempty"`
	Secret  bool   `json:"secret,omitempty"`
	// Tracking counts the local branches whose upstream is on it.
	Tracking int `json:"tracking,omitempty"`
}

// RepoBranch is one local branch.
type RepoBranch struct {
	Name string `json:"name"`
	// Group is who holds it: "base", "cards", "held" (adopted), "goals"
	// or "unowned".
	Group   string    `json:"group"`
	SHA     string    `json:"sha"`
	Subject string    `json:"subject,omitempty"`
	At      time.Time `json:"at,omitzero"`
	// Upstream is the remote branch it tracks, on Remote; Gone marks one
	// a prune removed. Ahead and Behind count against a live upstream.
	Upstream string `json:"upstream,omitempty"`
	Remote   string `json:"remote,omitempty"`
	Gone     bool   `json:"gone,omitempty"`
	Ahead    int    `json:"ahead,omitempty"`
	Behind   int    `json:"behind,omitempty"`
	// AheadBase and BehindBase count against the repository's base.
	AheadBase  int `json:"aheadBase,omitempty"`
	BehindBase int `json:"behindBase,omitempty"`
	// Worktree is where it is checked out, relative to the workspace.
	Worktree string          `json:"worktree,omitempty"`
	Card     *RepoBranchCard `json:"card,omitempty"`
	// ForkedBy lists the cards that fork from it.
	ForkedBy []string `json:"forkedBy,omitempty"`
	// Delete is "ok" (the base has everything on it), "confirm" (it
	// holds commits the base lacks: the request must set Force) or
	// absent, when it is not this view's to delete; Why says why for an
	// unowned branch.
	Delete string `json:"delete,omitempty"`
	Why    string `json:"why,omitempty"`
	// Push is the command that publishes its unpushed commits. The view
	// shows it and never runs it.
	Push string `json:"push,omitempty"`
}

// RepoBranchCard is the card holding a branch.
type RepoBranchCard struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Stage  string `json:"stage"`
	Landed bool   `json:"landed,omitempty"`
	// Clean reports the card's own clean action is on offer
	// (POST /api/cards/{id}/actions/clean).
	Clean bool `json:"clean,omitempty"`
}

// RepoRequest is the body of the repository writes: POST
// /api/repos/fetch and /api/repos/fastforward name Repo (empty is the
// default; All fetches every one, Remote only that remote), POST
// /api/repos/branches/delete names Repo and Branch, and sets Force for a
// branch whose Delete is "confirm".
//
// The remote writes name Repo and Remote: POST /api/repos/remotes/add
// and /seturl carry URL, /rename carries NewName, /remove nothing more.
// POST /api/repos/branches/upstream names Branch and the Upstream it is
// to track ("origin/topic"); an empty Upstream makes it track nothing.
type RepoRequest struct {
	Repo     string `json:"repo,omitempty"`
	All      bool   `json:"all,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Force    bool   `json:"force,omitempty"`
	Remote   string `json:"remote,omitempty"`
	NewName  string `json:"newName,omitempty"`
	URL      string `json:"url,omitempty"`
	Upstream string `json:"upstream,omitempty"`
}
