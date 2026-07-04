package ui

import (
	"bytes"
	"testing"

	"github.com/morphia/gummi/internal/notify"
	"github.com/morphia/gummi/internal/ui/theme"
)

func TestAttentionRingsOncePerFeature(t *testing.T) {
	var buf bytes.Buffer
	m := NewShell(theme.GummiDark(), "test")
	m.SetNotifier(notify.New(notify.Bell, &buf))

	// a new alert rings
	m.raiseAttention("FD-001", attnGate, "review & advance")
	if buf.String() != "\a" {
		t.Fatalf("new attention rang %q, want one BEL", buf.String())
	}
	// updating the same feature's item does not re-ring
	m.raiseAttention("FD-001", attnBudget, "hit its budget")
	if buf.String() != "\a" {
		t.Errorf("updating an existing item re-rang: %q", buf.String())
	}
	// a different feature rings again
	m.raiseAttention("FD-002", attnFailure, "boom")
	if buf.String() != "\a\a" {
		t.Errorf("new feature did not ring: %q", buf.String())
	}
	// the inbox still tracks both
	if m.inbox.len() != 2 {
		t.Errorf("inbox len = %d, want 2", m.inbox.len())
	}
}

func TestAttentionNoNotifierIsSafe(t *testing.T) {
	m := NewShell(theme.GummiDark(), "test")
	// no SetNotifier → nil notifier must not panic
	m.raiseAttention("FD-001", attnGate, "x")
	if m.inbox.len() != 1 {
		t.Errorf("attention not recorded without a notifier")
	}
}
