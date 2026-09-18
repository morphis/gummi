// Package notebook holds what a goal knows that no card owns.
//
// A card's durable context is its spec, on its branch. That is right for a
// card and wrong for a programme: a body of knowledge every card needs —
// the design a goal implements, the allocation scheme its parts must agree
// on, what was learned about how the system under it really behaves —
// belongs to none of them. Re-deriving it per card is expensive, and worse,
// two cards that derive it differently produce work that does not fit
// together.
//
// A goal is in no repository (DESIGN §17.2a), so its knowledge is in none:
// a notebook is a directory beside the goal doc, never on a branch. It has
// three parts, told apart by who may write them:
//
//	reference/  the owner's: the documents the goal was agreed against.
//	            Pinned by hash when the plan is approved, and never written
//	            by anything after.
//	registry    decided constants — names, numbers, schemes, priorities. One
//	            writer, the goal's lead, so two cards cannot disagree about
//	            one: neither of them may decide it.
//	findings    what turned out to be true, append-only: a claim, its
//	            evidence, and whether it still holds. A finding is never
//	            edited, only superseded, so a spec that cites F-7 cites
//	            something that will always say what it said.
//
// Cards read the rendered files (REGISTRY.md, FINDINGS.md, reference/*)
// from disk; their kickoffs carry only the Index, a line per entry, so the
// token window stays small however much a goal comes to know.
package notebook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/atomicfile"
)

// Finding statuses.
const (
	Holds      = "holds"
	Refuted    = "refuted"
	Superseded = "superseded"
)

// Entry is one decided constant.
type Entry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Why   string `json:"why,omitempty"`
	// Decision is the decision for review that set it (D-N).
	Decision string    `json:"decision,omitempty"`
	At       time.Time `json:"at"`
	// Was is the value it replaced, when it replaced one.
	Was string `json:"was,omitempty"`
}

// Finding is one thing that turned out to be true.
type Finding struct {
	N        int    `json:"n"`
	Claim    string `json:"claim"`
	Evidence string `json:"evidence"`
	// Card is the card whose work found it, when one did.
	Card   string `json:"card,omitempty"`
	Status string `json:"status"`
	// Supersedes is the finding this one replaces; SupersededBy is set on
	// the one replaced.
	Supersedes   int       `json:"supersedes,omitempty"`
	SupersededBy int       `json:"superseded_by,omitempty"`
	At           time.Time `json:"at"`
	// From names the goal a finding was carried over from.
	From string `json:"from,omitempty"`
}

// Ref names the finding the way every surface prints it.
func (f Finding) Ref() string { return fmt.Sprintf("F-%d", f.N) }

// RefFile is one pinned reference document.
type RefFile struct {
	Name string
	SHA  string
	// Changed reports that the file no longer hashes to what was pinned,
	// and Missing that it is gone.
	Changed, Missing bool
}

// Notebook is one goal's.
type Notebook struct {
	dir string
}

// Open returns the notebook at dir. Nothing is created until something is
// written.
func Open(dir string) *Notebook { return &Notebook{dir: dir} }

// Dir is where the notebook lives.
func (n *Notebook) Dir() string { return n.dir }

// ReferenceDir is where the owner's documents go.
func (n *Notebook) ReferenceDir() string { return filepath.Join(n.dir, "reference") }

func (n *Notebook) path(name string) string { return filepath.Join(n.dir, name) }

// --- reference ---------------------------------------------------------------

// AddReference copies src into the notebook's reference directory.
func (n *Notebook) AddReference(src string) error {
	in, err := os.Open(src) //nolint:gosec // a path the owner named
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(n.ReferenceDir(), 0o750); err != nil {
		return err
	}
	raw, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(n.ReferenceDir(), filepath.Base(src)), raw, 0o600)
}

// Pin records the hash of every reference document. It is what makes the
// reference the owner's: a document that changes afterwards is reported as
// changed wherever it is listed, rather than quietly becoming what the goal
// was agreed against.
func (n *Notebook) Pin() error {
	files, err := n.hashReference()
	if err != nil || len(files) == 0 {
		return err
	}
	raw, err := json.MarshalIndent(files, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(n.path("reference.lock"), raw, 0o600)
}

func (n *Notebook) hashReference() (map[string]string, error) {
	out := map[string]string{}
	root := n.ReferenceDir()
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return filepath.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, rerr := os.ReadFile(p) //nolint:gosec // inside the notebook
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, p)
		sum := sha256.Sum256(raw)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	return out, err
}

// Reference lists the reference documents against what was pinned.
func (n *Notebook) Reference() []RefFile {
	now, _ := n.hashReference()
	pinned := map[string]string{}
	if raw, err := os.ReadFile(n.path("reference.lock")); err == nil {
		_ = json.Unmarshal(raw, &pinned)
	}
	names := map[string]bool{}
	for k := range now {
		names[k] = true
	}
	for k := range pinned {
		names[k] = true
	}
	var out []RefFile
	for name := range names {
		f := RefFile{Name: name, SHA: now[name]}
		want, wasPinned := pinned[name]
		switch {
		case f.SHA == "":
			f.Missing, f.SHA = true, want
		case wasPinned && want != f.SHA:
			f.Changed = true
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// --- registry ----------------------------------------------------------------

// Registry lists the decided constants, by key.
func (n *Notebook) Registry() []Entry {
	var out []Entry
	if raw, err := os.ReadFile(n.path("registry.json")); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Set decides a constant, returning the entry it replaced, if any.
func (n *Notebook) Set(e Entry) (*Entry, error) {
	e.Key, e.Value = strings.TrimSpace(e.Key), strings.TrimSpace(e.Value)
	if e.Key == "" || e.Value == "" {
		return nil, errors.New("a registry entry needs a key and a value")
	}
	if strings.ContainsAny(e.Key, " \t\n") {
		return nil, fmt.Errorf("a registry key is one word a spec can cite, got %q", e.Key)
	}
	entries := n.Registry()
	var prev *Entry
	kept := entries[:0]
	for _, old := range entries {
		if old.Key == e.Key {
			o := old
			prev = &o
			continue
		}
		kept = append(kept, old)
	}
	if prev != nil {
		e.Was = prev.Value
	}
	kept = append(kept, e)
	sort.Slice(kept, func(i, j int) bool { return kept[i].Key < kept[j].Key })
	if err := n.writeJSON("registry.json", kept); err != nil {
		return nil, err
	}
	return prev, n.render()
}

// --- findings ----------------------------------------------------------------

// Findings lists every finding, oldest first.
func (n *Notebook) Findings() []Finding {
	var out []Finding
	if raw, err := os.ReadFile(n.path("findings.json")); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	return out
}

// Record appends a finding and numbers it. A finding that supersedes
// another marks that one superseded; nothing else about an old finding
// ever changes.
func (n *Notebook) Record(f Finding) (Finding, error) {
	f.Claim, f.Evidence = strings.TrimSpace(f.Claim), strings.TrimSpace(f.Evidence)
	if f.Claim == "" {
		return f, errors.New("a finding says something: give it a claim")
	}
	if f.Evidence == "" {
		return f, errors.New("a finding without evidence is an opinion: say what showed it — a run, a card's document, a source line")
	}
	switch f.Status {
	case "":
		f.Status = Holds
	case Holds, Refuted:
	default:
		return f, fmt.Errorf("a new finding holds or is refuted, not %q", f.Status)
	}
	all := n.Findings()
	if f.Supersedes != 0 {
		found := false
		for i := range all {
			if all[i].N == f.Supersedes {
				if all[i].Status == Superseded {
					return f, fmt.Errorf("F-%d was already superseded by F-%d", all[i].N, all[i].SupersededBy)
				}
				found = true
			}
		}
		if !found {
			return f, fmt.Errorf("there is no F-%d to supersede", f.Supersedes)
		}
	}
	f.N = 1
	for _, old := range all {
		if old.N >= f.N {
			f.N = old.N + 1
		}
	}
	for i := range all {
		if all[i].N == f.Supersedes {
			all[i].Status, all[i].SupersededBy = Superseded, f.N
		}
	}
	all = append(all, f)
	if err := n.writeJSON("findings.json", all); err != nil {
		return f, err
	}
	return f, n.render()
}

// Import carries another notebook's reference, registry and standing
// findings into this one — for a goal that continues where another ended.
// Findings keep their words and are renumbered; what was superseded or
// refuted there stays there.
func (n *Notebook) Import(from *Notebook, goal string) error {
	for _, rf := range from.Reference() {
		if rf.Missing {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(from.ReferenceDir(), filepath.FromSlash(rf.Name))) //nolint:gosec // another notebook's own file
		if err != nil {
			return err
		}
		dst := filepath.Join(n.ReferenceDir(), filepath.FromSlash(rf.Name))
		if _, err := os.Stat(dst); err == nil {
			continue // this goal's own copy wins
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return err
		}
		if err := atomicfile.Write(dst, raw, 0o600); err != nil {
			return err
		}
	}
	have := map[string]bool{}
	for _, e := range n.Registry() {
		have[e.Key] = true
	}
	for _, e := range from.Registry() {
		if !have[e.Key] {
			e.Decision, e.Was = e.Decision+" in "+goal, ""
			if _, err := n.Set(e); err != nil {
				return err
			}
		}
	}
	for _, f := range from.Findings() {
		if f.Status != Holds {
			continue
		}
		origin := goal
		if f.From != "" {
			origin = f.From
		}
		if _, err := n.Record(Finding{Claim: f.Claim, Evidence: f.Evidence, Card: f.Card, At: f.At, From: origin}); err != nil {
			return err
		}
	}
	return nil
}

// --- rendering ---------------------------------------------------------------

func (n *Notebook) writeJSON(name string, v any) error {
	if err := os.MkdirAll(n.dir, 0o750); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(n.path(name), raw, 0o600)
}

// render writes the files cards read.
func (n *Notebook) render() error {
	var b strings.Builder
	b.WriteString("# Registry — the constants this goal has decided\n\n" +
		"One writer: the goal's lead. A card that needs a constant that is not here asks; it does not pick one.\n\n")
	for _, e := range n.Registry() {
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", e.Key, e.Value)
		if e.Why != "" {
			fmt.Fprintf(&b, "_Why:_ %s\n\n", e.Why)
		}
		if e.Was != "" {
			fmt.Fprintf(&b, "_Replaced:_ %s\n\n", e.Was)
		}
	}
	if err := atomicfile.Write(n.path("REGISTRY.md"), []byte(b.String()), 0o600); err != nil {
		return err
	}
	b.Reset()
	b.WriteString("# Findings — what turned out to be true\n\n" +
		"Append-only. A finding is never edited, only superseded; cite one by its F-number.\n\n")
	for _, f := range n.Findings() {
		fmt.Fprintf(&b, "## %s — %s\n\n%s\n\n_Evidence:_ %s\n\n", f.Ref(), f.Status, f.Claim, f.Evidence)
		if f.SupersededBy != 0 {
			fmt.Fprintf(&b, "_Superseded by F-%d._\n\n", f.SupersededBy)
		}
		if f.Supersedes != 0 {
			fmt.Fprintf(&b, "_Supersedes F-%d._\n\n", f.Supersedes)
		}
		if f.Card != "" || f.From != "" {
			fmt.Fprintf(&b, "_From:_ %s\n\n", strings.TrimSpace(f.Card+" "+f.From))
		}
	}
	return atomicfile.Write(n.path("FINDINGS.md"), []byte(b.String()), 0o600)
}

// Empty reports that the notebook holds nothing.
func (n *Notebook) Empty() bool {
	return len(n.Reference()) == 0 && len(n.Registry()) == 0 && len(n.Findings()) == 0
}

// Index is the notebook in a line per entry: what a card's kickoff carries.
// The bodies stay on disk, where a card that needs one reads it.
func (n *Notebook) Index() string {
	if n.Empty() {
		return ""
	}
	var b strings.Builder
	if refs := n.Reference(); len(refs) > 0 {
		fmt.Fprintf(&b, "Reference — the owner's documents, in %s:\n", n.ReferenceDir())
		for _, r := range refs {
			note := ""
			switch {
			case r.Missing:
				note = "  (MISSING since the plan was agreed)"
			case r.Changed:
				note = "  (CHANGED since the plan was agreed — do not rely on it without saying so)"
			}
			fmt.Fprintf(&b, "- %s%s\n", r.Name, note)
		}
	}
	if reg := n.Registry(); len(reg) > 0 {
		fmt.Fprintf(&b, "Registry — decided constants, in %s:\n", n.path("REGISTRY.md"))
		for _, e := range reg {
			fmt.Fprintf(&b, "- %s = %s\n", e.Key, oneLine(e.Value, 100))
		}
	}
	if fs := n.Findings(); len(fs) > 0 {
		fmt.Fprintf(&b, "Findings — what turned out to be true, in %s:\n", n.path("FINDINGS.md"))
		for _, f := range fs {
			if f.Status == Superseded {
				fmt.Fprintf(&b, "- %s superseded by F-%d\n", f.Ref(), f.SupersededBy)
				continue
			}
			fmt.Fprintf(&b, "- %s (%s) %s\n", f.Ref(), f.Status, oneLine(f.Claim, 140))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}
