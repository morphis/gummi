package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/diffannot"
	"github.com/morphis/gummi/internal/domain"
)

// diffView is the diff surface: a feature's worktree diff in read mode
// (colorized unified diff) or line-addressed annotate mode, with line
// comments anchored by content hash (DESIGN §6.1). Annotations live in
// the store, not the diff, so they persist across reloads and rebases.
type diffView struct {
	f         domain.Feature
	lines     []string                // the unified diff, split
	anns      []domain.DiffAnnotation // this feature's annotations
	located   map[int][]int           // diff line index → annotation indices anchored there
	orphans   []int                   // annotation indices whose anchor no longer matches
	annotate  bool
	cursor    int // 1-based diff line (annotate mode)
	offset    int // scroll offset (both modes)
	maxOffset int // largest useful read-mode offset from the last render
}

// diffLoadedMsg delivers a (re)loaded diff plus its annotations.
type diffLoadedMsg struct {
	f     domain.Feature
	diff  string
	anns  []domain.DiffAnnotation
	err   error
	empty bool
}

// openDiff loads the feature's worktree diff and its stored annotations.
func (m *Shell) openDiff(f domain.Feature) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if ok, err := m.wt.Exists(ctx, &f); err != nil {
			return diffLoadedMsg{err: err}
		} else if !ok {
			return diffLoadedMsg{err: fmt.Errorf("%s has no worktree yet (created at spec approval)", f.ID)}
		}
		diff, err := m.wt.Diff(ctx, &f)
		if err != nil {
			return diffLoadedMsg{err: err}
		}
		if strings.TrimSpace(diff) == "" {
			return diffLoadedMsg{f: f, empty: true}
		}
		anns, err := m.store.ListDiffAnnotations(ctx, f.ID)
		if err != nil {
			return diffLoadedMsg{err: err}
		}
		return diffLoadedMsg{f: f, diff: diff, anns: anns}
	}
}

// reloadDiff re-reads the currently open diff and annotations.
func (m *Shell) reloadDiff() tea.Cmd {
	if m.diff == nil {
		return nil
	}
	return m.openDiff(m.diff.f)
}

// newDiffView builds a diff view, anchoring each annotation to a diff line
// (or the orphan list when its anchor no longer matches any line).
func newDiffView(f domain.Feature, diff string, anns []domain.DiffAnnotation) *diffView {
	dv := &diffView{
		f:       f,
		lines:   strings.Split(strings.TrimRight(diff, "\n"), "\n"),
		anns:    anns,
		located: map[int][]int{},
		cursor:  1,
	}
	anchors := make([]string, len(anns))
	for i, a := range anns {
		anchors[i] = a.Anchor
	}
	for i, idx := range diffannot.LocateAll(dv.lines, anchors) {
		if idx >= 0 {
			dv.located[idx] = append(dv.located[idx], i)
		} else {
			dv.orphans = append(dv.orphans, i)
		}
	}
	return dv
}

// openCount is the number of unresolved annotations (located or orphaned).
func (dv *diffView) openCount() int {
	n := 0
	for _, a := range dv.anns {
		if !a.Resolved {
			n++
		}
	}
	return n
}

// setCursor clamps the cursor to the addressable positions: the diff
// lines plus one slot per orphaned annotation in the footer (so x/D can
// reach a comment whose line changed).
func (dv *diffView) setCursor(n int) {
	dv.cursor = min(max(n, 1), len(dv.lines)+len(dv.orphans))
}

// scrollMax is the largest read-mode offset the last render allowed,
// falling back to a loose bound before the first render.
func (dv *diffView) scrollMax() int {
	if dv.maxOffset > 0 {
		return dv.maxOffset
	}
	return max(len(dv.lines)-1, 0)
}

// jumpAnn moves the cursor to the next/previous annotated position —
// anchored diff lines and the orphan footer slots.
func (dv *diffView) jumpAnn(dir int) {
	var ls []int
	for idx := range dv.located {
		ls = append(ls, idx+1) // to 1-based
	}
	for k := range dv.orphans {
		ls = append(ls, dv.orphanRowPos(k))
	}
	if len(ls) == 0 {
		return
	}
	// simple selection since the map is small
	target := -1
	if dir > 0 {
		for _, l := range ls {
			if l > dv.cursor && (target == -1 || l < target) {
				target = l
			}
		}
		if target == -1 { // wrap to the smallest
			for _, l := range ls {
				if target == -1 || l < target {
					target = l
				}
			}
		}
	} else {
		for _, l := range ls {
			if l < dv.cursor && (target == -1 || l > target) {
				target = l
			}
		}
		if target == -1 { // wrap to the largest
			for _, l := range ls {
				if l > target {
					target = l
				}
			}
		}
	}
	if target > 0 {
		dv.cursor = target
	}
}

// annAtCursor returns the annotation at the cursor — the first one
// anchored at the cursor line, or the orphan the cursor addresses in the
// footer — or -1. Used by `x` (toggle resolved) and `D` (delete).
func (dv *diffView) annAtCursor() int {
	if k := dv.cursor - len(dv.lines) - 1; k >= 0 && k < len(dv.orphans) {
		return dv.orphans[k]
	}
	if idxs := dv.located[dv.cursor-1]; len(idxs) > 0 {
		return idxs[0]
	}
	return -1
}

// addDiffComment stores a comment anchored to the cursor line.
func (m *Shell) addDiffComment(text string) tea.Cmd {
	dv := m.diff
	if dv == nil {
		return nil
	}
	idx := dv.cursor - 1
	if idx < 0 || idx >= len(dv.lines) {
		// the orphan footer: nothing to anchor a new comment to
		m.notice = noticeMsg{text: "move to a diff line to comment"}
		return nil
	}
	ann := domain.DiffAnnotation{
		Feature: dv.f.ID,
		File:    diffannot.FileAt(dv.lines, idx),
		Anchor:  diffannot.Anchor(dv.lines, idx),
		Excerpt: strings.TrimSpace(dv.lines[idx]),
		Comment: text,
	}
	reload := m.reloadDiff()
	return func() tea.Msg {
		if _, err := m.store.AddDiffAnnotation(context.Background(), ann, m.now()); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return reload()
	}
}

// toggleDiffResolved flips the resolved flag of the annotation at the
// cursor and reloads.
func (m *Shell) toggleDiffResolved() tea.Cmd {
	dv := m.diff
	if dv == nil {
		return nil
	}
	i := dv.annAtCursor()
	if i < 0 {
		m.notice = noticeMsg{text: "no annotation on this line"}
		return nil
	}
	ann := dv.anns[i]
	reload := m.reloadDiff()
	return func() tea.Msg {
		if err := m.store.SetDiffAnnotationResolved(context.Background(), ann.ID, !ann.Resolved); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return reload()
	}
}

// deleteDiffAnnotation removes the annotation at the cursor from the
// store and reloads — the escape hatch for a mistyped comment (`x` only
// resolves, which keeps it visible).
func (m *Shell) deleteDiffAnnotation() tea.Cmd {
	dv := m.diff
	if dv == nil {
		return nil
	}
	i := dv.annAtCursor()
	if i < 0 {
		m.notice = noticeMsg{text: "no annotation on this line"}
		return nil
	}
	ann := dv.anns[i]
	reload := m.reloadDiff()
	return func() tea.Msg {
		if err := m.store.DeleteDiffAnnotation(context.Background(), ann.ID); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return reload()
	}
}

// bindings is the diff surface's key table (see keymap.go), split by
// mode like handleDiffKey routes.
func (dv *diffView) bindings() []binding {
	if dv.annotate {
		return []binding{
			{key: "tab", label: "read", help: "switch to read mode", bar: true},
			{key: "j/k", label: "line", help: "move the line cursor"},
			{key: "pgup/pgdn", label: "page", help: "move the line cursor by a page"},
			{key: "c", label: "comment", help: "comment on the cursor line", bar: true},
			{key: "x", label: "resolve", help: "toggle the annotation resolved", bar: true},
			{key: "D", label: "delete", help: "delete the annotation at the cursor"},
			{key: "A", label: "approve", help: "approve the gate", bar: true},
			{key: "n/p", label: "annotations", help: "jump between annotated lines", bar: true},
			{key: "R", label: "request changes", help: "send the open comments to the implementer", bar: true},
			{key: "esc", label: "back", help: "back to the board (also q)", bar: true},
			{key: "?", label: "help", bar: true},
		}
	}
	return []binding{
		{key: "tab", label: "annotate", help: "switch to annotate mode", bar: true},
		{key: "j/k", label: "scroll", bar: true},
		{key: "pgup/pgdn", label: "page", help: "scroll by a page"},
		{key: "R", label: "request changes", help: "send the open comments to the implementer", bar: true},
		{key: "A", label: "approve", help: "approve the gate", bar: true},
		{key: "esc", label: "back", help: "back to the board (also q)", bar: true},
		{key: "?", label: "help", bar: true},
	}
}

// handleDiffKey processes keys while the diff surface is open.
func (m *Shell) handleDiffKey(key string) tea.Cmd {
	dv := m.diff
	switch key {
	case "esc", "q":
		m.diff = nil
		return nil
	case "tab":
		dv.annotate = !dv.annotate
		return nil
	case "?":
		m.Overlay.Push(m.helpOverlay())
		return nil
	case "R":
		return m.requestDiffChanges(dv)
	case "A":
		// approve the gate: leave the surface and run the board's g
		return m.approveSurface(dv.f)
	}
	if !dv.annotate {
		switch key {
		case "j", "down":
			dv.offset = min(dv.offset+1, dv.scrollMax())
		case "k", "up":
			dv.offset = max(dv.offset-1, 0)
		case "pgdown":
			dv.offset = min(dv.offset+m.mainPage(), dv.scrollMax())
		case "pgup":
			dv.offset = max(dv.offset-m.mainPage(), 0)
		}
		return nil
	}
	switch key {
	case "j", "down":
		dv.setCursor(dv.cursor + 1)
	case "k", "up":
		dv.setCursor(dv.cursor - 1)
	case "pgdown":
		dv.setCursor(dv.cursor + m.mainPage())
	case "pgup":
		dv.setCursor(dv.cursor - m.mainPage())
	case "n":
		dv.jumpAnn(1)
	case "p":
		dv.jumpAnn(-1)
	case "x":
		return m.toggleDiffResolved()
	case "D":
		return m.deleteDiffAnnotation()
	case "c":
		m.Overlay.Push(newCommentDialog(func(text string) tea.Cmd {
			return m.addDiffComment(text)
		}))
	}
	return nil
}
