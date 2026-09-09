package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/worktree"
)

// The shell's side of the new-card door (cardform.go): building the
// dialog from board state, the GitHub round trips it asks for, and what
// happens once a card exists.

// openCardForm builds the door for kind with everything the board knows:
// profiles, repositories, the repo chosen last time, the cards the after
// row may name, and the seams for GitHub. It does not push it — presets
// decide whether the form opens or goes straight to browse.
func (m *Shell) openCardForm(kind domain.Kind) *cardForm {
	d := newCardForm(kind, m.profileNames, m.repoNames, m.repoHasDefault(), m.lastRepo, m.afterCands(), m.envelopePrefill(), m.createCard)
	if m.wt != nil {
		d.originFor = m.repoOriginFor
	}
	d.onImport = func(ref domain.IssueRef, repo string) tea.Cmd { return m.fetchIssue(d, ref, repo) }
	d.onBrowse = func(repo string) tea.Cmd { return m.browseIssues(d, repo) }
	return d
}

// afterCands is every card not yet done, as the after row offers them.
func (m *Shell) afterCands() []afterCand {
	var out []afterCand
	for _, r := range m.rows {
		if r.F.Stage == domain.StageDone {
			continue
		}
		out = append(out, afterCand{ID: r.F.ID, Title: r.F.Title, Repo: r.F.Repo, Stage: r.F.Stage})
	}
	sortAfterCands(out)
	return out
}

// repoOriginFor reads a repository's origin remote — a local git read,
// no network — and parses it for the repo row's readout. remoteOrigin
// is the seam tests stub.
func (m *Shell) repoOriginFor(repo string) repoOrigin {
	dir, ok := m.repoRoot(repo)
	if !ok {
		return repoOrigin{}
	}
	read := m.remoteOrigin
	if read == nil {
		read = func(dir string) string { return worktree.RemoteOrigin(context.Background(), dir) }
	}
	host, ownerRepo, ok := worktree.ParseRemote(read(dir))
	if !ok {
		return repoOrigin{}
	}
	return repoOrigin{host: host, ownerRepo: ownerRepo}
}

// repoRoot resolves a configured name ("" = default) to its checkout.
func (m *Shell) repoRoot(repo string) (string, bool) {
	if m.wt == nil {
		return "", false
	}
	return m.wt.RootForName(repo)
}

// cardIssueMsg delivers an alt+g import to the form that asked for it.
type cardIssueMsg struct {
	form *cardForm
	prop domain.BugProposal
	ref  domain.IssueRef
	err  error
}

// fetchIssue runs `gh issue view` for ref in repo's checkout, off the
// main loop. ref is never bare here: the form resolved it against the
// repo's origin before asking.
func (m *Shell) fetchIssue(d *cardForm, ref domain.IssueRef, repo string) tea.Cmd {
	dir, _ := m.repoRoot(repo)
	fetch := m.ghIssue
	if fetch == nil {
		fetch = func(ctx context.Context, dir, ownerRepo string, n int) (domain.BugProposal, error) {
			return engine.GitHubSource{Repo: ownerRepo, Dir: dir}.FetchIssue(ctx, n)
		}
	}
	return func() tea.Msg {
		p, err := fetch(context.Background(), dir, ref.OwnerRepo(), ref.Number)
		return cardIssueMsg{form: d, prop: p, ref: ref, err: err}
	}
}

// browseIssues opens the issue picker for repo. The form is popped by
// its own HandleKey (done=true) and parked here until the picker fills
// it or is left.
func (m *Shell) browseIssues(d *cardForm, repo string) tea.Cmd {
	m.pendingCard = d
	o := repoOrigin{}
	if m.wt != nil {
		o = m.repoOriginFor(repo)
	}
	params := bugIngestParams{repo: repo, ownerRepo: o.ownerRepo, label: "bug", state: "open"}
	if !o.github() {
		// no origin to list from: ask for one, then browse
		m.Overlay.Push(newTextPrompt("browse issues of", "", "owner/repo", nil, func(s string) tea.Cmd {
			params.ownerRepo = strings.TrimSpace(s)
			return m.startBugIngest(params)
		}))
		return nil
	}
	return m.startBugIngest(params)
}

// restorePendingCard pushes a form parked for browsing back onto the
// stack, if there is one.
func (m *Shell) restorePendingCard() {
	if d := m.pendingCard; d != nil {
		m.pendingCard = nil
		m.Overlay.Push(d)
	}
}

// cardCreated is what the shell does once a card exists.
func (m *Shell) cardCreated(msg cardCreatedMsg) tea.Cmd {
	m.lastRepo = msg.f.Repo
	text := fmt.Sprintf("%s created", msg.f.ID)
	if msg.warn != "" {
		text += " — dependency not recorded: " + msg.warn
	}
	m.notice = noticeMsg{text: text, isErr: msg.warn != ""}
	if msg.fromPicker && m.bugIngest != nil {
		m.bugIngest.markOnBoard(msg.f.ExternalRef, msg.f.ID)
	}
	cmds := []tea.Cmd{m.loadRows}
	if msg.start {
		cmds = append(cmds, m.openAutopilot(msg.f))
	}
	return tea.Batch(cmds...)
}
