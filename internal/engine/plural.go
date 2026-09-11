package engine

// cardPlural is the "s" a count needs. The engine emits a handful of
// progress notes the TUI and the driver both print verbatim, and they
// carried "(s)" long after the surfaces around them stopped.
func cardPlural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
