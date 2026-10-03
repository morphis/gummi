package theme

import (
	"hash/fnv"
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/charmtone"
)

// RepoSlots is how many colors the repo palette has. The web page draws
// the same slots from --r0 … --r5 (internal/web/assets/app.css).
const RepoSlots = 6

// repoAccents is the palette a repository's chip is drawn from. A repo's
// color is a hash of its name onto these slots, never a setting, so one
// repo reads the same on every board and in every theme. None of them is
// a stage accent or the destructive red, so a chip never reads as a stage.
var repoAccents = [RepoSlots]color.Color{
	charmtone.Malibu,
	charmtone.Zest,
	charmtone.Guppy,
	charmtone.Pickle,
	charmtone.Damson,
	charmtone.Cumin,
}

// RepoSlot maps a repository name to its palette slot: FNV-1a over the
// name's bytes, modulo RepoSlots. The web rail computes the same value in
// assets/dom.js (repoSlot), so the two faces agree on a repo's color.
func RepoSlot(name string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return int(h.Sum32() % RepoSlots)
}

// Repo returns the chip for a named repository: a filled pill in the
// repo's palette slot. The default repo has no chip; callers check first.
func (s *Styles) Repo(name string) lipgloss.Style {
	return s.repo[RepoSlot(name)]
}
