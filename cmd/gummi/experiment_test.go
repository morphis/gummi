package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/experiment"
)

// __experiment is the process that makes a run and holds the substrate's
// lease while it does. It is internal: hidden, and useless without a
// directory gummi prepared.
func TestExperimentCommandMakesAPreparedRun(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"__experiment"})
	if err != nil || cmd == nil || cmd.Name() != "__experiment" || !cmd.Hidden {
		t.Fatalf("__experiment must be registered and hidden: %v", err)
	}

	root := t.TempDir()
	job := experiment.Job{
		ID: experiment.NewID(time.Now()), Experiment: "matrix", Owner: "GL-001", Purpose: "verify",
		Heads: map[string]string{"": "abc"}, Trees: map[string]string{"": root},
		Def:       config.Experiment{Substrate: "rig", Run: `printf '{"id":"a","ok":true}\n' > "$GUMMI_EVIDENCE/results.ndjson"`},
		Substrate: config.Substrate{Probe: "true"},
		Root:      root, StateDir: filepath.Join(root, ".gummi", "state"),
	}
	job.Dir = filepath.Join(root, ".gummi", "evidence", "GL-001", job.ID)
	if err := experiment.Prepare(job); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"__experiment", "--dir", job.Dir})
	// rootCmd is shared by every test in the package: put back what this
	// one redirected, or a later test that reads cobra's output reads ours
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	res, err := experiment.Load(job.Dir)
	if err != nil || res.Outcome != experiment.Pass || res.State != experiment.StateDone {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.Contains(out.String(), "pass") {
		t.Fatalf("it says what it proved: %q", out.String())
	}
}
