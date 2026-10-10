// Package repoview is the pure read model behind the repositories view:
// one repository's local branches sorted by who holds each, and the one
// rule for which of them may be deleted from there. A branch is the
// base's, a card's, a goal's, held (adopted — never gummi's to delete),
// or nobody's; only the last kind is offered for deletion, and only when
// nothing has it checked out and no card forks from it.
//
// Nothing here runs git or reads the store: a caller gathers
// worktree.BranchInfo (Manager.Branches) and the cards' claims, and the
// answer is a value.
package repoview

import (
	"sort"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// Group is who holds a branch.
type Group string

const (
	// GroupBase is the branch the main checkout has out.
	GroupBase Group = "base"
	// GroupCards are branches gummi cut for a card.
	GroupCards Group = "cards"
	// GroupHeld are branches a card adopted: held, never owned.
	GroupHeld Group = "held"
	// GroupGoals are goals' own branches.
	GroupGoals Group = "goals"
	// GroupUnowned are branches no card on the board claims.
	GroupUnowned Group = "unowned"
)

// groupOrder is the order a view lists the groups in.
var groupOrder = map[Group]int{GroupBase: 0, GroupCards: 1, GroupHeld: 2, GroupGoals: 3, GroupUnowned: 4}

// Owner is a card's claim on a branch.
type Owner struct {
	Branch  string
	Card    domain.FeatureID
	Title   string
	Stage   domain.Stage
	Goal    bool
	Adopted bool
	// Landed reports the card's work is on its base.
	Landed bool
	// Cleanable reports the card's own clean action is on offer: the
	// view sends that, and never removes a card's branch itself.
	Cleanable bool
}

// Fork says a card names a branch as the base it forks from.
type Fork struct {
	Branch string
	Card   domain.FeatureID
}

// Delete is whether, and how readily, the view may delete a branch.
type Delete string

const (
	// DeleteNo: not from here. Row.Why says why when it is not obvious.
	DeleteNo Delete = ""
	// DeleteOK: the base has everything on it.
	DeleteOK Delete = "ok"
	// DeleteConfirm: it holds commits the base lacks, so the person
	// names it to delete it.
	DeleteConfirm Delete = "confirm"
)

// Row is one branch as the view draws it.
type Row struct {
	worktree.BranchInfo
	Group Group
	// Owner is the card holding it; nil for the base and for an unowned
	// branch.
	Owner *Owner
	// ForkedBy lists the cards that fork from it.
	ForkedBy []domain.FeatureID
	Delete   Delete
	// Why is the reason an unowned branch is not offered for deletion.
	Why string
	// Push is the command that publishes its unpushed commits, when a
	// plain push would. The view shows it and never runs it.
	Push string
}

// Rows sorts a repository's branches into groups, base first. base is
// the main checkout's branch, empty when it is detached.
func Rows(base string, branches []worktree.BranchInfo, owners []Owner, forks []Fork) []Row {
	byBranch := make(map[string]*Owner, len(owners))
	for i := range owners {
		// two cards on one branch name is a board error surfaced
		// elsewhere; the first claim stands here
		if _, taken := byBranch[owners[i].Branch]; !taken {
			byBranch[owners[i].Branch] = &owners[i]
		}
	}
	forkedBy := map[string][]domain.FeatureID{}
	for _, f := range forks {
		forkedBy[f.Branch] = append(forkedBy[f.Branch], f.Card)
	}
	rows := make([]Row, 0, len(branches))
	for _, b := range branches {
		r := Row{BranchInfo: b, Owner: byBranch[b.Name], ForkedBy: forkedBy[b.Name], Push: pushCommand(b)}
		switch {
		case base != "" && b.Name == base:
			r.Group, r.Owner = GroupBase, nil
		case r.Owner == nil:
			r.Group = GroupUnowned
			r.Delete, r.Why = deletable(r)
		case r.Owner.Goal:
			r.Group = GroupGoals
		case r.Owner.Adopted:
			r.Group = GroupHeld
		default:
			r.Group = GroupCards
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if gi, gj := groupOrder[rows[i].Group], groupOrder[rows[j].Group]; gi != gj {
			return gi < gj
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}

func deletable(r Row) (Delete, string) {
	switch {
	case r.Worktree != "":
		return DeleteNo, "checked out at " + r.Worktree
	case len(r.ForkedBy) > 0:
		ids := make([]string, len(r.ForkedBy))
		for i, id := range r.ForkedBy {
			ids[i] = string(id)
		}
		return DeleteNo, strings.Join(ids, ", ") + " forks from it"
	case r.AheadBase > 0:
		return DeleteConfirm, ""
	}
	return DeleteOK, ""
}

// pushCommand is the push a branch ahead of its live upstream needs.
// Nothing for a branch that is also behind — that push is refused, and
// forcing it is a decision this view does not make for anyone — or for
// one tracking nothing, which nobody has said should be published.
func pushCommand(b worktree.BranchInfo) string {
	if b.Upstream == "" || b.UpstreamGone || b.Ahead == 0 || b.Behind > 0 {
		return ""
	}
	// the remote's own name, when git gave it: one may hold a slash
	remote := b.UpstreamRemote
	theirs, ok := strings.CutPrefix(b.Upstream, remote+"/")
	if remote == "" || !ok {
		if remote, theirs, ok = strings.Cut(b.Upstream, "/"); !ok {
			return ""
		}
	}
	if theirs != b.Name {
		return "git push " + remote + " " + b.Name + ":" + theirs
	}
	return "git push " + remote + " " + b.Name
}
