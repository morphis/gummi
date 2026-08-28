package overlay

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/ui/theme"
)

// stubDialog is a minimal Dialog for stack + drawing tests.
type stubDialog struct {
	id    string
	title string
	body  string
}

func (d *stubDialog) ID() string { return d.id }

func (d *stubDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	return key.String() == "esc", nil
}

func (d *stubDialog) View(s *theme.Styles, w, h int) string {
	return s.DialogFrame.Render(s.DialogTitle.Render(d.title) + "\n\n" + s.Base.Render(d.body))
}

func TestStackOps(t *testing.T) {
	var st Stack
	if st.HasDialogs() || st.Top() != nil || st.Pop() != nil {
		t.Fatal("empty stack misbehaves")
	}
	a := &stubDialog{id: "a"}
	b := &stubDialog{id: "b"}
	st.Push(a)
	st.Push(b)
	if st.Len() != 2 || st.Top() != b || !st.Contains("a") || st.Contains("zzz") {
		t.Fatalf("stack state wrong: len=%d", st.Len())
	}
	if got := st.Pop(); got != b {
		t.Fatal("Pop returned wrong dialog")
	}
	if st.Top() != a {
		t.Fatal("top after pop wrong")
	}
}

func TestStackHandleKey(t *testing.T) {
	var st Stack
	// no dialog: not consumed
	if consumed, _ := st.HandleKey(tea.KeyPressMsg{Code: 'x', Text: "x"}); consumed {
		t.Fatal("empty stack consumed a key")
	}
	st.Push(&stubDialog{id: "a"})
	// any key is consumed while a dialog is open
	if consumed, _ := st.HandleKey(tea.KeyPressMsg{Code: 'x', Text: "x"}); !consumed {
		t.Fatal("open dialog did not consume key")
	}
	if st.Len() != 1 {
		t.Fatal("non-esc key closed the dialog")
	}
	if consumed, _ := st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape}); !consumed {
		t.Fatal("esc not consumed")
	}
	if st.Len() != 0 {
		t.Fatal("esc did not close the dialog")
	}
}

// drawScene paints checkerboard content, then the stack, and returns
// the rendered screen.
func drawScene(t *testing.T, st *Stack, w, h int) string {
	t.Helper()
	s := theme.New(theme.GummiDark())
	buf := uv.NewScreenBuffer(w, h)
	var bg strings.Builder
	for y := 0; y < h; y++ {
		bg.WriteString(s.Subtle.Render(strings.Repeat("backdrop ", w/9+1)[:w]))
		if y < h-1 {
			bg.WriteString("\n")
		}
	}
	uv.NewStyledString(bg.String()).Draw(&buf, buf.Bounds())
	st.Draw(&buf, buf.Bounds(), s)
	return buf.Render()
}

func TestDrawDimsBackdropAndCentersDialog80(t *testing.T) {
	var st Stack
	st.Push(&stubDialog{id: "gate", title: "Approve plan?", body: "FD-042 · dark mode\n[a]pprove · [r]equest changes"})
	golden.RequireEqual(t, []byte(drawScene(t, &st, 80, 24)))
}

func TestDrawDimsBackdropAndCentersDialog120(t *testing.T) {
	var st Stack
	st.Push(&stubDialog{id: "gate", title: "Approve plan?", body: "FD-042 · dark mode\n[a]pprove · [r]equest changes"})
	golden.RequireEqual(t, []byte(drawScene(t, &st, 120, 34)))
}

func TestDrawEmptyStackLeavesContent(t *testing.T) {
	var st Stack
	out := drawScene(t, &st, 40, 6)
	if !strings.Contains(out, "backdrop") {
		t.Error("content vanished with no dialogs open")
	}
}

// valueDialog is a value-type Dialog holding an uncomparable field, the
// shape helpDialog has. Removing it from the stack must not compare
// interfaces: == on two such values panics with "comparing uncomparable
// type", which crashed gummi on every esc out of the help overlay.
type valueDialog struct {
	rows [][2]string
}

func (valueDialog) ID() string { return "value" }
func (valueDialog) HandleKey(tea.KeyPressMsg) (bool, tea.Cmd) {
	return true, nil
}
func (valueDialog) View(*theme.Styles, int, int) string { return "" }

func TestHandleKeyRemovesValueTypeDialog(t *testing.T) {
	var st Stack
	st.Push(valueDialog{rows: [][2]string{{"?", "help"}}})
	if consumed, _ := st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape}); !consumed {
		t.Fatal("key was not consumed")
	}
	if st.Len() != 0 {
		t.Fatalf("stack len = %d, want 0", st.Len())
	}
}

// pushingDialog opens another dialog from its own handler, then reports
// done — the command menu running "New feature". The stack must close
// the pusher, not the pushed.
type pushingDialog struct{ st *Stack }

func (pushingDialog) ID() string { return "pusher" }
func (d pushingDialog) HandleKey(tea.KeyPressMsg) (bool, tea.Cmd) {
	d.st.Push(valueDialog{})
	return true, nil
}
func (pushingDialog) View(*theme.Styles, int, int) string { return "" }

func TestHandleKeyClosesThePusherNotThePushed(t *testing.T) {
	var st Stack
	st.Push(pushingDialog{st: &st})
	st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if st.Len() != 1 {
		t.Fatalf("stack len = %d, want 1 (the pushed dialog)", st.Len())
	}
	if got := st.Top().ID(); got != "value" {
		t.Fatalf("top = %q, want the pushed dialog", got)
	}
}
