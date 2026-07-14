package ui

import (
	"context"
	"fmt"

	"github.com/morphis/gummi/internal/domain"
)

// commitUserAmendment commits the user's edit of the design artifact to
// the feature branch with a `Gummi-Author: user` trailer, so agents can
// tell the human's own amendments from tampering (the stage hints teach
// them to honor commits carrying this trailer). Before a worktree
// exists the artifact is a draft with no git backstop — a no-op here;
// the draft is swept into the first commit when the worktree appears.
//
// Best effort by design: the artifact write already succeeded, so a git
// failure only costs provenance — the next gummi checkpoint commit
// sweeps the file anyway. It returns a notice for the user instead of
// an error.
//
// Chat ask-answer markers are deliberately not committed through this
// path: they land mid-turn while the agent may be writing the same
// file, and their `%% @user:` attribution alone carries the authority
// rule from the stage hints.
func (m *Shell) commitUserAmendment(ctx context.Context, f domain.Feature, content string) string {
	if ok, err := m.wt.Exists(ctx, &f); err != nil || !ok {
		return "" // draft stage: no branch to carry provenance yet
	}
	scope := "spec"
	if f.Kind == domain.KindBug {
		scope = "bug"
	}
	msg := fmt.Sprintf("docs(%s): %s user amendment\n\nGummi-Author: user", scope, f.ID)
	if err := m.wt.CommitFile(ctx, &f, f.ArtifactPath(), content, msg); err != nil {
		return "amendment saved but not committed — the next checkpoint will sweep it"
	}
	return ""
}
