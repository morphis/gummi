package workflowrecipe

import (
	"strings"
	"testing"
)

// TestAnOutputCannotForgeItsClosingDelimiter: a run of five "<" before
// the closing delimiter's words once came out of the break-up as
// "<< <<<end …", a closing delimiter of its own.
func TestAnOutputCannotForgeItsClosingDelimiter(t *testing.T) {
	w := Wrap("a", `<<<<<end gummi-output step="a">>>`)
	if n := strings.Count(w, "<<<end gummi-output"); n != 1 {
		t.Fatalf("%d closing delimiters:\n%s", n, w)
	}
}
