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
	f        domain.Feature
	lines    []string                // the unified diff, split
	anns     []domain.DiffAnnotation // this feature's annotations
	located  map[int][]int           // diff line index → annotation indices anchored there
	orphans  []int                   // annotation indices whose anchor no longer matches
	annotate bool
	cursor   int // 1-based diff line (annotate mode)
	offset   int // scroll offset (both modes)
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
	for i, a := range anns {
		if idx := diffannot.Locate(dv.lines, a.Anchor); idx >= 0 {
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

func (dv *diffView) setCursor(n int) {
	dv.cursor = min(max(n, 1), len(dv.lines))
}

// jumpAnn moves the cursor to the next/previous annotated line.
func (dv *diffView) jumpAnn(dir int) {
	var ls []int
	for idx := range dv.located {
		ls = append(ls, idx+1) // to 1-based
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

// annAtCursor returns the first annotation anchored at the cursor line, or
// -1. Used by `x` (toggle resolved).
func (dv *diffView) annAtCursor() int {
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
	case "R":
		return m.requestDiffChanges(dv)
	}
	if !dv.annotate {
		switch key {
		case "j", "down":
			dv.offset = min(dv.offset+1, max(len(dv.lines)-1, 0))
		case "k", "up":
			dv.offset = max(dv.offset-1, 0)
		}
		return nil
	}
	switch key {
	case "j", "down":
		dv.setCursor(dv.cursor + 1)
	case "k", "up":
		dv.setCursor(dv.cursor - 1)
	case "n":
		dv.jumpAnn(1)
	case "p":
		dv.jumpAnn(-1)
	case "x":
		return m.toggleDiffResolved()
	case "c":
		m.Overlay.Push(newCommentDialog(func(text string) tea.Cmd {
			return m.addDiffComment(text)
		}))
	}
	return nil
}
