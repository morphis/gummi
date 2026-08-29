package layout

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/x/exp/golden"
)

// TestComputeTable golden-records the rects for representative sizes so
// layout changes show up in review (and the package shares the -update
// flag with the rest of internal/ui).
func TestComputeTable(t *testing.T) {
	var b []byte
	for _, dim := range [][2]int{{80, 24}, {120, 34}, {60, 20}, {64, 10}} {
		l := Compute(dim[0], dim[1])
		b = fmt.Appendf(b, "%dx%d: tabs=%v main=%v status=%v\n",
			dim[0], dim[1], l.Tabs, l.Main, l.Status)
	}
	golden.RequireEqual(t, b)
}

func TestComputeWide(t *testing.T) {
	l := Compute(120, 34)
	if l.Tabs.Dy() != 1 || l.Tabs.Dx() != 120 || l.Tabs.Min.Y != 0 {
		t.Errorf("tabs rect wrong: %+v", l.Tabs)
	}
	if l.Main.Min.Y != 1 || l.Main.Dy() != 32 || l.Main.Dx() != 120 || l.Main.Min.X != 0 {
		t.Errorf("main rect wrong: %+v", l.Main)
	}
	if l.Status.Min.Y != 33 || l.Status.Dy() != 1 || l.Status.Dx() != 120 {
		t.Errorf("status rect wrong: %+v", l.Status)
	}
}

func TestComputeStandard(t *testing.T) {
	l := Compute(80, 24)
	if l.Tabs.Dx() != 80 || l.Tabs.Dy() != 1 {
		t.Errorf("tabs rect wrong: %+v", l.Tabs)
	}
	if l.Main.Dx() != 80 || l.Main.Dy() != 22 {
		t.Errorf("main rect wrong: %+v", l.Main)
	}
	if l.Status.Dy() != 1 {
		t.Errorf("status rect wrong: %+v", l.Status)
	}
}

// TestComputeTakesTheFullWidth: the backlog is the only board shape, so
// the main pane always spans the terminal — there is no column to carve
// out of it at any size.
func TestComputeTakesTheFullWidth(t *testing.T) {
	for _, dim := range [][2]int{{120, 34}, {80, 24}, {60, 20}} {
		l := Compute(dim[0], dim[1])
		if l.Main.Min.X != 0 || l.Main.Dx() != dim[0] {
			t.Errorf("%v: main should take full width: %+v", dim, l.Main)
		}
	}
}

func TestComputeDegenerate(t *testing.T) {
	for _, dim := range [][2]int{{0, 0}, {1, 1}, {2, 2}, {-5, -5}, {200, 1}} {
		l := Compute(dim[0], dim[1])
		if l.Main.Dx() < 0 || l.Main.Dy() < 0 || l.Status.Dy() < 0 || l.Tabs.Dy() < 0 {
			t.Errorf("negative rect for %v: %+v", dim, l)
		}
	}
}
