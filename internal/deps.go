//go:build pin

// Package internal pins gummi's UI and storage stack to the exact
// versions Crush uses (design decision: adopt Crush's stack wholesale).
// The blank imports keep the pins in go.mod until real code imports each
// package; remove an import here the moment a real one lands.
package internal

import (
	_ "charm.land/bubbles/v2/textarea"
	_ "charm.land/bubbletea/v2"
	_ "charm.land/glamour/v2"
	_ "charm.land/lipgloss/v2"
	_ "github.com/charmbracelet/colorprofile"
	_ "github.com/charmbracelet/ultraviolet"
	_ "github.com/charmbracelet/x/ansi"
	_ "github.com/charmbracelet/x/exp/charmtone"
	_ "github.com/charmbracelet/x/exp/golden"
	_ "modernc.org/sqlite"
)
