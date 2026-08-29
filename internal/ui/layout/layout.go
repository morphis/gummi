// Package layout computes gummi's rectangle layout: optionally a kanban
// column on the left, the main pane beside (or instead of) it, and a
// one-line status bar at the bottom. Pure geometry — no styling, no
// state.
package layout

import (
	uv "github.com/charmbracelet/ultraviolet"
)

const (
	// kanban column bounds; between them the column takes a third of
	// the terminal.
	minKanbanWidth = 24
	maxKanbanWidth = 36
	// below this total width the kanban column collapses entirely.
	collapseBelow = 64
	statusHeight  = 1
)

// Layout is the set of screen rectangles the shell paints into.
type Layout struct {
	Area   uv.Rectangle
	Kanban uv.Rectangle // zero when collapsed
	Main   uv.Rectangle
	Status uv.Rectangle

	// KanbanVisible reports whether the kanban column fits.
	KanbanVisible bool
}

// Compute lays out a w×h terminal. kanban asks for the left column;
// the backlog layout passes false and takes the whole width for the
// main pane, the same geometry a too-narrow terminal already falls
// back to.
func Compute(w, h int, kanban bool) Layout {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	l := Layout{Area: uv.Rect(0, 0, w, h)}

	contentH := max(h-statusHeight, 0)
	l.Status = uv.Rect(0, contentH, w, min(statusHeight, h))

	if kanban && w >= collapseBelow {
		kw := min(max(w/3, minKanbanWidth), maxKanbanWidth)
		l.Kanban = uv.Rect(0, 0, kw, contentH)
		l.Main = uv.Rect(kw, 0, w-kw, contentH)
		l.KanbanVisible = true
		return l
	}
	l.Main = uv.Rect(0, 0, w, contentH)
	return l
}
