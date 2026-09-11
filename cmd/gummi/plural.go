package main

// cardPlural is the "s" a count needs, so the CLI stops printing
// "1 feature(s)" and "1 open question(s)" at people — round 2 swept the
// TUI for these and never reached this binary (round 3 §5.5).
func cardPlural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
