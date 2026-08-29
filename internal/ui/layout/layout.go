// Package layout computes gummi's rectangle layout: a one-line tab bar
// at the top, the main pane owned by whichever tab is active, and a
// one-line status bar at the bottom. Pure geometry — no styling, no
// state.
package layout

import (
	uv "github.com/charmbracelet/ultraviolet"
)

const (
	tabsHeight   = 1
	statusHeight = 1
)

// Layout is the set of screen rectangles the shell paints into.
type Layout struct {
	Area   uv.Rectangle
	Tabs   uv.Rectangle
	Main   uv.Rectangle
	Status uv.Rectangle
}

// Compute lays out a w×h terminal: the tab bar on row 0, the status bar
// on the last row, and the main pane spending everything between. Both
// chrome rows clamp against a terminal too short to hold them, so a 1-
// or 2-row terminal degrades to a shrinking main pane rather than
// negative heights.
func Compute(w, h int) Layout {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	l := Layout{Area: uv.Rect(0, 0, w, h)}

	tabsH := min(tabsHeight, h)
	l.Tabs = uv.Rect(0, 0, w, tabsH)

	statusH := min(statusHeight, max(h-tabsH, 0))
	l.Status = uv.Rect(0, h-statusH, w, statusH)

	mainH := max(h-tabsH-statusH, 0)
	l.Main = uv.Rect(0, tabsH, w, mainH)
	return l
}
