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
		b = fmt.Appendf(b, "%dx%d: kanban=%v main=%v status=%v visible=%v\n",
			dim[0], dim[1], l.Kanban, l.Main, l.Status, l.KanbanVisible)
	}
	golden.RequireEqual(t, b)
}

func TestComputeWide(t *testing.T) {
	l := Compute(120, 34)
	if !l.KanbanVisible {
		t.Fatal("kanban should be visible at 120 cols")
	}
	if l.Kanban.Dx() != 36 { // 120/3=40 clamped to max 36
		t.Errorf("kanban width = %d, want 36", l.Kanban.Dx())
	}
	if l.Main.Min.X != l.Kanban.Dx() || l.Main.Dx() != 120-36 {
		t.Errorf("main rect wrong: %+v", l.Main)
	}
	if l.Kanban.Dy() != 33 || l.Main.Dy() != 33 {
		t.Errorf("content height should be 33 (34 - status), got kanban %d main %d", l.Kanban.Dy(), l.Main.Dy())
	}
	if l.Status.Min.Y != 33 || l.Status.Dy() != 1 || l.Status.Dx() != 120 {
		t.Errorf("status rect wrong: %+v", l.Status)
	}
}

func TestComputeStandard(t *testing.T) {
	l := Compute(80, 24)
	if !l.KanbanVisible {
		t.Fatal("kanban should be visible at 80 cols")
	}
	if l.Kanban.Dx() != 26 { // 80/3 = 26
		t.Errorf("kanban width = %d, want 26", l.Kanban.Dx())
	}
	if l.Main.Dx() != 54 {
		t.Errorf("main width = %d, want 54", l.Main.Dx())
	}
}

func TestComputeNarrowCollapsesKanban(t *testing.T) {
	l := Compute(60, 20)
	if l.KanbanVisible {
		t.Fatal("kanban should collapse below 64 cols")
	}
	if l.Main.Dx() != 60 || l.Main.Min.X != 0 {
		t.Errorf("main should take full width: %+v", l.Main)
	}
}

func TestComputeDegenerate(t *testing.T) {
	for _, dim := range [][2]int{{0, 0}, {1, 1}, {-5, -5}, {200, 1}} {
		l := Compute(dim[0], dim[1])
		if l.Main.Dx() < 0 || l.Main.Dy() < 0 || l.Status.Dy() < 0 {
			t.Errorf("negative rect for %v: %+v", dim, l)
		}
	}
}
