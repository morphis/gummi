package web

import (
	"net/http"
	"os"
	"testing"

	"github.com/morphis/gummi/internal/domain"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/webapi"
)

// TestMemoryDocumentsServeLikeTheSpec: a freeform card's project memory
// reads over HTTP the way a workflow card's spec does — the workspace's
// global memory and the card's own memory and dead-ends, as the files
// stand — and a workflow card answers none with why, the way the spec
// answers a freeform card. A card whose session memory still sits in
// plan.md migrates it on the GET: the content serves under the renamed
// key at the renamed path and the old file is gone.
func TestMemoryDocumentsServeLikeTheSpec(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("on it"))
	c := h.create(webapi.CreateCardRequest{Kind: "freeform", Title: "Poke at the pty leak", Description: "find where it leaks"})
	h.waitCard(c.ID, "the row", func(webapi.Card) bool { return true })

	// nothing written yet: the documents read as empty, not missing
	var m webapi.Memory
	if st := h.call(http.MethodGet, "/api/cards/"+c.ID+"/memory", nil, &m); st != http.StatusOK {
		t.Fatalf("GET memory: %d", st)
	}
	if m.None || m.Global.Text != "" || m.Memory.Text != "" || m.DeadEnds.Text != "" {
		t.Fatalf("empty memory = %+v", m)
	}
	if m.Dir != ".gummi/memory" || m.Global.Path != ".gummi/memory/global.md" {
		t.Errorf("paths = dir %q global %q, want relative to the workspace root", m.Dir, m.Global.Path)
	}

	// what the session wrote — or the person, by hand — reads back
	writeFile(t, h.ws.GlobalMemoryFile(), "the repo's checks are make ci\n")
	writeFile(t, h.ws.SessionMemoryDir(domain.FeatureID(c.ID))+"/memory.md", "# Plan\n- split the parser\n")
	writeFile(t, h.ws.SessionMemoryDir(domain.FeatureID(c.ID))+"/dead-ends.md", "attempt 1 failed\n")
	if st := h.call(http.MethodGet, "/api/cards/"+c.ID+"/memory", nil, &m); st != http.StatusOK {
		t.Fatalf("GET memory after writes: %d", st)
	}
	for _, want := range []string{m.Global.Text, m.Memory.Text, m.DeadEnds.Text} {
		if want == "" {
			t.Fatalf("a written document read back empty: %+v", m)
		}
	}
	if m.Global.Text != "the repo's checks are make ci" || m.Memory.Text != "# Plan\n- split the parser" || m.DeadEnds.Text != "attempt 1 failed" {
		t.Errorf("texts = global %q memory %q dead %q", m.Global.Text, m.Memory.Text, m.DeadEnds.Text)
	}
	if m.Memory.Path != ".gummi/memory/"+c.ID+"/memory.md" {
		t.Errorf("memory path = %q", m.Memory.Path)
	}

	// a workflow card's documents are its stages'
	fc := h.create(webapi.CreateCardRequest{Kind: "feature", Title: "Row cache"})
	var w webapi.Memory
	if st := h.call(http.MethodGet, "/api/cards/"+fc.ID+"/memory", nil, &w); st != http.StatusOK {
		t.Fatalf("GET memory on a workflow card: %d", st)
	}
	if !w.None || w.Why == "" {
		t.Errorf("workflow card memory = %+v, want none with why", w)
	}
}

// TestMemoryMigratesPlanOnGET: a card whose session memory still sits in
// plan.md — hand-written by a previous run — migrates it on the web
// read: the content serves under the memory key at the memory.md path
// and plan.md is gone, the same migration the engine's tools run.
func TestMemoryMigratesPlanOnGET(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("on it"))
	c := h.create(webapi.CreateCardRequest{Kind: "freeform", Title: "Notes from before", Description: "carry them over"})
	h.waitCard(c.ID, "the row", func(webapi.Card) bool { return true })
	dir := h.ws.SessionMemoryDir(domain.FeatureID(c.ID))
	writeFile(t, dir+"/plan.md", "# Plan\n- notes a previous session kept\n")

	var m webapi.Memory
	if st := h.call(http.MethodGet, "/api/cards/"+c.ID+"/memory", nil, &m); st != http.StatusOK {
		t.Fatalf("GET memory: %d", st)
	}
	if m.Memory.Text != "# Plan\n- notes a previous session kept" {
		t.Errorf("migrated text = %q", m.Memory.Text)
	}
	if m.Memory.Path != ".gummi/memory/"+c.ID+"/memory.md" {
		t.Errorf("migrated path = %q", m.Memory.Path)
	}
	if _, err := os.Stat(dir + "/plan.md"); !os.IsNotExist(err) {
		t.Errorf("plan.md survived the GET (stat err %v)", err)
	}
	if b, err := os.ReadFile(dir + "/memory.md"); err != nil || b == nil {
		t.Errorf("memory.md after the GET (%v): %q", err, b)
	}
}
