package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestWebRowCarriesItsRepo: the web rail chips and filters a card by its
// repo, so the row must carry the repo's name as the page reads it ("repo"),
// and a default-repo card must say so with an empty value.
func TestWebRowCarriesItsRepo(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	named := row(51, "rate limits", domain.StageTodo, "thrifty", false)
	named.F.Repo = "lxd"
	def := row(52, "plain feature", domain.StageTodo, "thrifty", false)

	if got := m.webRow(named, nil).Repo; got != "lxd" {
		t.Errorf("named-repo row Repo = %q, want lxd", got)
	}
	defRow := m.webRow(def, nil)
	if defRow.Repo != "" {
		t.Errorf("default-repo row Repo = %q, want empty", defRow.Repo)
	}
	raw, err := json.Marshal(m.webRow(named, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"repo":"lxd"`) {
		t.Errorf("row JSON carries no repo key: %s", raw)
	}
}
