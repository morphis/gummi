package driver

import (
	"testing"

	"github.com/morphis/gummi/internal/engine"
)

// TestAMergedGoalNamesEveryRepositoryItLandsIn: a goal across repositories
// lands once in each — git has no merge that spans them — so the one
// commit the event carries is the home repository's. A caller that
// recorded only it has no way to reach the rest, and no way to tell a
// whole landing from one that stopped part way.
func TestAMergedGoalNamesEveryRepositoryItLandsIn(t *testing.T) {
	got := mergedRepos([]engine.GoalReportRepo{
		{Name: "fabricd", Landed: true, Order: 1},
		{Name: "netd", Home: true, Landed: false, Order: 2},
	})
	if len(got) != 2 {
		t.Fatalf("repos = %+v, want both", got)
	}
	if got[0].Name != "fabricd" || !got[0].Landed || got[0].Order != 1 {
		t.Errorf("the repository that landed first is not reported as it landed: %+v", got[0])
	}
	if got[1].Name != "netd" || !got[1].Home || got[1].Landed {
		t.Errorf("a repository the goal has not reached yet reads as landed: %+v", got[1])
	}
	if len(mergedRepos(nil)) != 0 {
		t.Error("a single-repository goal invents a repository list")
	}
}
