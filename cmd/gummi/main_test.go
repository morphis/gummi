package main

import "testing"

func TestRunVersion(t *testing.T) {
	for _, args := range [][]string{nil, {"version"}, {"--version"}} {
		if err := run(args); err != nil {
			t.Errorf("run(%v) = %v, want nil", args, err)
		}
	}
}

func TestRunUnknownCommand(t *testing.T) {
	if err := run([]string{"frobnicate"}); err == nil {
		t.Fatal("run with unknown command: want error, got nil")
	}
}

func TestVersionNonEmpty(t *testing.T) {
	if version() == "" {
		t.Fatal("version() returned empty string")
	}
	old := Version
	defer func() { Version = old }()
	Version = "v1.2.3"
	if got := version(); got != "v1.2.3" {
		t.Fatalf("version() = %q, want ldflags value to win", got)
	}
}
