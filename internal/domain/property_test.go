package domain

// Property tests of money and of the stored tree set, driven by hegel
// (https://hegel.dev). A budget is what a person typed and what every
// envelope is held to; a run's trees are what its revert reads back.

import (
	"fmt"
	"maps"
	"math"
	"strings"
	"testing"

	"hegel.dev/go/hegel"
)

const propCases = 2000

func props(t *testing.T, fn func(*hegel.T)) {
	t.Helper()
	hegel.Test(t, fn, hegel.WithTestCases(propCases), hegel.WithDatabase(""))
}

// typed writes cents the ways a person types a dollar figure: with or
// without "$", thousands commas, cents or a trailing zero, spaces around.
func typed(ht *hegel.T, cents int) string {
	whole, frac := cents/100, cents%100
	w := fmt.Sprint(whole)
	if hegel.Draw(ht, hegel.Booleans()) {
		var groups []string
		for len(w) > 3 {
			groups = append([]string{w[len(w)-3:]}, groups...)
			w = w[:len(w)-3]
		}
		w = strings.Join(append([]string{w}, groups...), ",")
	}
	s := w
	switch {
	case frac != 0 && frac%10 == 0 && hegel.Draw(ht, hegel.Booleans()):
		s += fmt.Sprintf(".%d", frac/10)
	case frac != 0 || hegel.Draw(ht, hegel.Booleans()):
		s += fmt.Sprintf(".%02d", frac)
	}
	if hegel.Draw(ht, hegel.Booleans()) {
		s = "$" + s
	}
	pad := hegel.Draw(ht, hegel.SampledFrom([]string{"", " ", "  "}))
	return pad + s + pad
}

// TestPropertyABudgetIsStoredAsTyped: a dollar figure typed in any of the
// forms a person uses is stored as exactly its cents, up to the largest
// envelope there is.
func TestPropertyABudgetIsStoredAsTyped(t *testing.T) {
	props(t, func(ht *hegel.T) {
		cents := hegel.Draw(ht, hegel.Integers(0, math.MaxInt32))
		s := typed(ht, cents)
		got, err := ParseDollars(s)
		if err != nil || got != cents {
			ht.Fatalf("ParseDollars(%q) = %d, %v; want %d", s, got, err, cents)
		}
	})
}

// TestPropertyWhatIsNotADollarFigureIsRefused: a figure that is not a
// plain dollar amount — a decimal comma, a misplaced thousands comma, an
// exponent, a hex figure, a fraction of a cent, a sign — is refused, not
// read as some other amount.
func TestPropertyWhatIsNotADollarFigureIsRefused(t *testing.T) {
	props(t, func(ht *hegel.T) {
		d := hegel.Draw(ht, hegel.Integers(1, 99999))
		s := hegel.Draw(ht, hegel.SampledFrom([]string{
			"%d,50", "%d,5", "1,23", "1,2345", "%d,1234", "%d,", ",%d", "%de2", "%dE0", "0x%d", "%d.001", "%d.505",
			"-%d", "%d$", "$$%d", "%d..5", "%d.5.5", "%d dollars", "1_%d", "0b1%d", "%d,00,0",
		}))
		in := strings.ReplaceAll(s, "%d", fmt.Sprint(d))
		if got, err := ParseDollars(in); err == nil {
			ht.Fatalf("ParseDollars(%q) = %d, accepted", in, got)
		}
	})
}

// TestPropertyAFormattedFigureReadsBack: a whole number of cents, shown
// as dollars, reads back as those cents.
func TestPropertyAFormattedFigureReadsBack(t *testing.T) {
	props(t, func(ht *hegel.T) {
		cents := hegel.Draw(ht, hegel.Integers(0, math.MaxInt32))
		shown := FormatDollars(float64(cents))
		if got, err := ParseDollars(shown); err != nil || got != cents {
			ht.Fatalf("FormatDollars(%d) = %q, which reads back as %d, %v", cents, shown, got, err)
		}
	})
}

// TestPropertyARealCostNeverReadsAsFree: any spend above nothing reads as
// something.
func TestPropertyARealCostNeverReadsAsFree(t *testing.T) {
	props(t, func(ht *hegel.T) {
		exp := hegel.Draw(ht, hegel.Integers(-300, 9))
		mant := hegel.Draw(ht, hegel.Integers(1, 999))
		credits := float64(mant) * math.Pow(10, float64(exp))
		if credits <= 0 {
			return
		}
		if shown := FormatDollars(credits); shown == "$0.00" || strings.HasPrefix(shown, "$0.000") || shown == "0 credits" {
			ht.Fatalf("FormatDollars(%g) = %q", credits, shown)
		}
	})
}

// TestPropertyATreeSetReadsBackAsStored: a run's primary tree and its
// attached ones decode as they were encoded, whatever the attached
// repositories are called.
func TestPropertyATreeSetReadsBackAsStored(t *testing.T) {
	props(t, func(ht *hegel.T) {
		hash := func() string {
			n := hegel.Draw(ht, hegel.Integers(0, 1<<30))
			return fmt.Sprintf("%040x", n)
		}
		primary := hash()
		n := hegel.Draw(ht, hegel.Integers(0, 4))
		attached := map[string]string{}
		for range n {
			name := hegel.Draw(ht, hegel.SampledFrom([]string{"api", "web", "a=b", "x y", "é", "-r", "a.b", "=", "k8s"}))
			attached[name] = hash()
		}
		p, a := DecodeTrees(EncodeTrees(primary, attached))
		if len(attached) == 0 {
			attached = nil
		}
		if p != primary || !maps.Equal(a, attached) {
			ht.Fatalf("EncodeTrees(%q, %q) read back %q, %q", primary, attached, p, a)
		}
	})
}
