package ui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/attachment"
	"github.com/morphis/gummi/internal/cardrun"
	"github.com/morphis/gummi/internal/diffannot"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/fleetrun"
	"github.com/morphis/gummi/internal/pr"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/threadfold"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// The card's documents for the web face: its thread, spec, diff, pull
// request and run. Unlike the board projections these read the disk, the
// store, git and gh, so they cannot run inside Update. WebDocs takes what
// they need from the model inside Update — through the Bridge — and the
// reads then run on the web request's own goroutine, the way the TUI's
// own loaders run as commands off the loop. Every read and every write
// here is the function the TUI's own surface uses, so a document read in
// a browser is the document the terminal shows.

// Errors the document readers and writers report, for the server to map
// onto a status.
var (
	// ErrNoCard: the board has no such card, or none of that kind of
	// thing (an annotation) on it.
	ErrNoCard = errors.New("not found")
	// ErrMoved: what the request was made against is not there any more
	// — the line a note was on, the diff line a comment named.
	ErrMoved = errors.New("moved")
	// ErrDetached: the board has no workspace to read from.
	ErrDetached = errors.New("the board has no workspace")
)

// InvalidError is a request the readers refuse as malformed.
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &InvalidError{Msg: fmt.Sprintf(format, a...)} }

// WebDocs is one card's documents, as captured from the model.
type WebDocs struct {
	f       domain.Feature
	base    string
	store   *state.Store
	pool    *worktree.Pool
	ws      state.Workspace
	now     func() time.Time
	threads func(ctx context.Context, ref domain.PullRequestRef) ([]pr.ReviewThread, []pr.TopLevelComment, string, error)
	// liveStart is the start of the local session still writing the
	// segment the card's log ends in (liveSessionActive), zero when none.
	liveStart time.Time
	live      *webapi.Live
	// locks is the board's per-card lock registry (nil in a scaffold), and
	// busy whether an agent had the card mid-turn when it was captured: the
	// two things a history rewrite asks of the model.
	locks *state.CardLocks
	busy  bool
	// attachments is the workspace's image store, for resolving a spec
	// note's attachment ids into links; nil on a board with no engine.
	attachments *attachment.Store
}

// WebDocs captures card id's documents for reading off the loop. It
// fails with ErrNoCard for a card the board does not have and
// ErrDetached for a board with no workspace.
func (m *Shell) WebDocs(id string) (*WebDocs, error) {
	r, ok := m.rowByID(domain.FeatureID(id))
	if !ok {
		return nil, ErrNoCard
	}
	if !m.attached() || m.store == nil || m.wt == nil {
		return nil, ErrDetached
	}
	d := &WebDocs{
		f: r.F, base: m.baseBranch(r.F), store: m.store, pool: m.wt, ws: m.ws,
		now: m.now, threads: m.fetchPRReviewThreads,
		locks: m.locks, busy: m.webCardBusy(r.F.ID) || m.cardBusy(r),
	}
	if m.engine != nil {
		d.attachments = m.engine.Attachments()
	}
	return d, nil
}

// WebThreadDocs is WebDocs for reading the thread, which also needs the
// live block and the session still writing the log's last segment.
func (m *Shell) WebThreadDocs(id string) (*WebDocs, error) {
	d, err := m.WebDocs(id)
	if err != nil {
		return nil, err
	}
	r, _ := m.rowByID(domain.FeatureID(id))
	if sess := m.sessionFor(r.F.ID); sess != nil && !r.DrivenAbroad {
		if snap := sess.Snapshot(); liveSessionActive(snap) {
			d.liveStart = snap.StartedAt
		}
	}
	if live, ok := m.WebLive(id); ok && liveWorthSending(live) {
		d.live = &live
	}
	return d, nil
}

func liveWorthSending(l webapi.Live) bool {
	return l.Busy || l.State != "" || l.Streaming != "" || len(l.Turns) > 0 ||
		l.Consult != nil || l.Freeform != nil || l.Elsewhere != nil
}

// fresh re-reads the card: the board row is a snapshot of the last
// refresh, and a route that reports what the card is linked to or what
// it cost must not answer from one.
func (d *WebDocs) fresh(ctx context.Context) domain.Feature {
	if f, err := d.store.GetFeature(ctx, d.f.ID); err == nil {
		return f
	}
	return d.f
}

// ---------------------------------------------------------------- thread

// Thread is the card's folded log (threadfold.Items) with the items
// newer than after, and the live block. d must come from WebThreadDocs.
//
// While a local session is still writing the log's last segment, its
// turns reach the log only once per turn; the TUI draws that segment
// from the session's snapshot instead, and so does the page. The items
// after that segment's divider are therefore left out, and LastSeq stops
// short of them, so the page picks them up — as upserts — once the
// session hands the segment to the log.
func (d *WebDocs) Thread(ctx context.Context, after int64) (webapi.Thread, error) {
	evs, err := d.store.Events(ctx, d.f.ID)
	if err != nil {
		return webapi.Thread{}, err
	}
	spend, err := d.store.StageBreakdown(ctx, d.f.ID)
	if err != nil {
		spend = nil
	}
	items := threadfold.Items(evs, threadfold.Options{Spend: spend, Live: state.CardIsLive(d.ws, d.f.ID)})
	var held []threadfold.Item
	if !d.liveStart.IsZero() {
		for k := len(items) - 1; k >= 0; k-- {
			if items[k].T == threadfold.ItemStage && items[k].At.Equal(d.liveStart) {
				items, held = items[:k+1], items[k+1:]
				break
			}
		}
		// A consult turn is no turn of the stage session: the live block
		// does not draw it, so it is never held back for the session to
		// hand over.
		kept := held[:0:0]
		for _, it := range held {
			if it.Via == threadfold.ViaConsult {
				items = append(items, it)
				continue
			}
			kept = append(kept, it)
		}
		held = kept
	}
	var last int64
	for _, it := range items {
		last = max(last, it.Seq)
	}
	for _, it := range held {
		last = min(last, it.Seq-1)
	}
	cmds := d.checkCommands(ctx)
	converted := make([]webapi.Item, 0, len(items))
	for _, it := range items {
		converted = append(converted, webItem(it, cmds))
	}
	out := webapi.Thread{Items: []webapi.Item{}, Live: d.live, LastSeq: max(last, 0)}
	for _, it := range groupActivity(converted) {
		if it.Seq > after {
			out.Items = append(out.Items, it)
		}
	}
	return out, nil
}

// checkCommands maps each gummi-check's name to its command: the spec's
// block first, the baseline's record of it second.
func (d *WebDocs) checkCommands(ctx context.Context) map[string]string {
	cmds := map[string]string{}
	if base, err := d.store.CheckBaseline(ctx, d.f.ID); err == nil {
		for _, c := range base {
			cmds[c.Name] = c.Cmd
		}
	}
	if path := d.artifact(); path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			if checks, _, err := spec.ParseChecks(string(raw)); err == nil {
				for _, c := range checks {
					cmds[c.Name] = c.Cmd
				}
			}
		}
	}
	return cmds
}

// webItem maps one folded item onto the wire.
func webItem(it threadfold.Item, cmds map[string]string) webapi.Item {
	out := webapi.Item{
		Key: it.Key, Seq: it.Seq, T: webapi.ItemType(it.T), Time: it.At, Stage: string(it.Stage),
		Role: it.Role, Model: it.Model, Flavor: it.Flavor, Author: it.Author, Text: it.Text, Via: it.Via,
		Exited: it.Exited, Verdict: it.Verdict, Credits: it.Credits, Outcome: it.Outcome, By: it.By,
	}
	for _, a := range it.Attachments {
		out.Attachments = append(out.Attachments, webapi.AttachmentRef{ID: a.ID, Name: a.Name, MediaType: a.MediaType, Size: a.Size})
	}
	for _, c := range it.Tools {
		out.Tools = append(out.Tools, webapi.ToolCall{
			Tool: c.Tool, Label: c.Label, Detail: c.Detail, Status: c.Status, Ms: c.MS, Output: c.Output,
		})
	}
	if it.Receipt != nil {
		out.Receipt = &webapi.Receipt{Kind: it.Receipt.Kind, OK: it.Receipt.OK, Text: it.Receipt.Text, By: it.Receipt.By}
		out.By = it.Receipt.By
	}
	for _, c := range it.Checks {
		out.Checks = append(out.Checks, webapi.CheckRun{
			Name: c.Name, Cmd: cmds[c.Name], OK: c.OK, Ms: c.MS, Output: c.Output, Status: c.Status,
		})
	}
	if it.Decision != nil {
		out.Decision = &webapi.ThreadDecision{
			ID: it.Decision.ID, Kind: webapi.DecisionKind(it.Decision.Kind), Question: it.Decision.Question,
		}
	}
	if st := it.Stretch; st != nil {
		out.Edge, out.Label, out.How, out.Reason, out.Tally, out.Mode =
			st.Edge, st.Label, string(st.How), st.Reason, st.Tally, st.Mode
	}
	return out
}

// ------------------------------------------------------------------ spec

// artifact is where the card's document lives right now, "" when it has
// none yet (artifactFile's rule, read-only: nothing is promoted or
// created by looking).
func (d *WebDocs) artifact() string {
	if d.f.IsFreeform() {
		return ""
	}
	return artifactFileIn(d.pool.Root(), d.ws.DraftsDir(), &d.f)
}

// Spec is the card's document as it stands.
//
// The TUI's spec surface creates the draft from its template on first
// open, and promotes a draft to its workspace home once the worktree
// exists (openSpec). A read over HTTP does neither: it reports what is
// on disk, marks a draft as one, and says so when there is nothing yet.
func (d *WebDocs) Spec(ctx context.Context) (webapi.Spec, error) {
	if d.f.IsFreeform() {
		return webapi.Spec{None: true, Why: "a freeform card has no document: its record is its thread and its branch"}, nil
	}
	path := d.artifact()
	if path == "" {
		return webapi.Spec{None: true, Why: "no " + artifactNoun(d.f.Kind) + " yet: it is written when the card's first stage runs"}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return webapi.Spec{}, err
	}
	content := string(raw)
	out := webapi.Spec{
		Path:     d.rel(path),
		Draft:    strings.HasPrefix(path, d.ws.DraftsDir()+string(filepath.Separator)),
		Rev:      spec.Rev(raw),
		Title:    docTitle(content, d.f.Title),
		Markdown: content,
		Sections: []webapi.SpecSection{},
		Notes:    []webapi.SpecNote{},
		Checks:   []webapi.SpecCheck{},
	}
	for _, h := range spec.HeadingLines(content) {
		out.Sections = append(out.Sections, webapi.SpecSection{Name: h.Title, Line: h.Line})
	}
	doc := spec.Parse(content)
	// counted as the gate counts them, so the button's number is the one
	// holding it
	f := d.fresh(ctx)
	out.OpenComments = len(engine.SpecCommentsHolding(domain.CardTypeOf(&f), f.Stage, doc))
	var notes []spec.Marker
	for _, t := range doc.Threads() {
		notes = append(notes, t.Markers...)
	}
	sortMarkers(notes)
	for _, mk := range notes {
		date, by := spec.SplitStamp(mk.Date)
		out.Notes = append(out.Notes, webapi.SpecNote{
			Line: mk.Line, Anchor: mk.Anchor, Author: mk.Author, By: by, Date: date,
			Text: mk.Text, Resolved: mk.Resolved,
		})
	}
	if checks, _, err := spec.ParseChecks(content); err == nil && len(checks) > 0 {
		last := d.lastChecks(ctx)
		excused := map[string]bool{}
		excusedOn := ""
		if base, err := d.store.CheckBaseline(ctx, d.f.ID); err == nil {
			for _, n := range state.ExcusedChecks(base) {
				excused[n] = true
			}
			excusedOn = state.ExcusedOn(base)
		}
		for _, c := range checks {
			sc := webapi.SpecCheck{Name: c.Name, Cmd: c.Cmd, Excused: excused[c.Name]}
			if sc.Excused {
				sc.ExcusedOn = excusedOn
			}
			if o, ok := last[c.Name]; ok {
				sc.Last = &o
			}
			out.Checks = append(out.Checks, sc)
		}
	}
	return out, nil
}

// lastChecks is each check's newest result in the card's log: the verify
// rows the thread draws.
func (d *WebDocs) lastChecks(ctx context.Context) map[string]webapi.CheckOutcome {
	out := map[string]webapi.CheckOutcome{}
	evs, err := d.store.Events(ctx, d.f.ID)
	if err != nil {
		return out
	}
	for _, it := range threadfold.Items(evs, threadfold.Options{}) {
		for _, c := range it.Checks {
			out[c.Name] = webapi.CheckOutcome{OK: c.OK, At: it.At}
		}
	}
	return out
}

// AddSpecNote writes a person's note under line, the way the TUI's
// comment dialog does (writeSpecNote), with the person's name in the
// marker's stamp. It writes only to a document that exists.
func (d *WebDocs) AddSpecNote(ctx context.Context, line int, text, person string, attachments []string) (webapi.Spec, error) {
	path := d.artifact()
	if path == "" {
		return webapi.Spec{}, ErrNoCard
	}
	if strings.TrimSpace(text) == "" {
		return webapi.Spec{}, invalid("a note needs some text")
	}
	if len(attachments) > 0 {
		if d.attachments == nil {
			return webapi.Spec{}, invalid("no agent configured — attachments need the workspace's store")
		}
		refs, err := d.attachments.Resolve(attachments)
		if err != nil {
			return webapi.Spec{}, invalid("%s", err.Error())
		}
		links := make([]string, len(refs))
		for i, ref := range refs {
			links[i] = attachment.Link(ref)
		}
		// A spec note stays one line, so its attachments ride the same
		// line as the text rather than a paragraph of their own.
		text = strings.TrimSpace(text) + " " + strings.Join(links, " ")
	}
	if err := writeSpecNote(path, line, spec.Stamp(d.now().Format("2006-01-02"), person), text); err != nil {
		return webapi.Spec{}, invalid("%s", err.Error())
	}
	return d.Spec(ctx)
}

// ResolveSpecNote closes the note at req.Line through the TUI's own
// resolve path (writeSpecResolution), provided the line still holds the
// note the page showed.
func (d *WebDocs) ResolveSpecNote(ctx context.Context, req webapi.SpecResolveRequest, person string) (webapi.Spec, error) {
	path := d.artifact()
	if path == "" {
		return webapi.Spec{}, ErrNoCard
	}
	check := func(mk spec.Marker) error {
		date, _ := spec.SplitStamp(mk.Date)
		if mk.Line == 0 || mk.Author != req.Author || date != req.Date {
			return ErrMoved
		}
		return nil
	}
	err := writeSpecResolution(path, req.Line, spec.Stamp(d.now().Format("2006-01-02"), person), req.Reason, check)
	switch {
	case errors.Is(err, ErrMoved):
		return webapi.Spec{}, err
	case err != nil:
		return webapi.Spec{}, invalid("%s", err.Error())
	}
	return d.Spec(ctx)
}

func (d *WebDocs) rel(path string) string {
	if r, err := filepath.Rel(d.pool.Root(), path); err == nil {
		return filepath.ToSlash(r)
	}
	return path
}

// docTitle is the document's own `# ` title, or fallback.
func docTitle(content, fallback string) string {
	for _, l := range strings.Split(content, "\n") {
		if t, ok := strings.CutPrefix(l, "# "); ok {
			return strings.TrimSpace(t)
		}
	}
	return fallback
}

func sortMarkers(ms []spec.Marker) {
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0 && ms[j].Line < ms[j-1].Line; j-- {
			ms[j], ms[j-1] = ms[j-1], ms[j]
		}
	}
}

// ---------------------------------------------------------------- memory

// Memory is a freeform card's project memory as it stands: the
// workspace's global memory and the card's own session memory — the
// documents its session reads at spawn (engine.memoryCard) and fills as
// it works, shown the way a workflow card's spec is. A workflow card
// answers None with Why, the way Spec answers a freeform card: its
// document is its spec, and memory belongs to no stage of it.
//
// A read, like Spec's: it reports what is on disk and says nothing about
// the session — the files are plain markdown, editable by hand. The
// card's session memory is migrated off its former file name first, so a
// card whose notes still sit in plan.md reads them under the current
// one; the migration never replaces an existing file, so it needs no
// lock.
func (d *WebDocs) Memory() (webapi.Memory, error) {
	if !d.f.IsFreeform() {
		return webapi.Memory{None: true, Why: "memory is a freeform session's; this card's documents are its stages'"}, nil
	}
	global, err := d.memoryDoc(d.ws.GlobalMemoryFile())
	if err != nil {
		return webapi.Memory{}, err
	}
	if _, err := d.ws.LegacyPlanRename(d.f.ID); err != nil {
		return webapi.Memory{}, err
	}
	mem, err := d.memoryDoc(d.ws.SessionMemoryFile(d.f.ID))
	if err != nil {
		return webapi.Memory{}, err
	}
	dead, err := d.memoryDoc(filepath.Join(d.ws.SessionMemoryDir(d.f.ID), "dead-ends.md"))
	if err != nil {
		return webapi.Memory{}, err
	}
	return webapi.Memory{
		Dir:      d.rel(d.ws.MemoryDir()),
		Global:   global,
		Memory:   mem,
		DeadEnds: dead,
	}, nil
}

// memoryDoc reads one memory file for the wire. A file nothing has
// written yet reads as empty text — the page's placeholder — not an
// error; any other read failure propagates.
func (d *WebDocs) memoryDoc(path string) (webapi.MemoryDoc, error) {
	rel := d.rel(path)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return webapi.MemoryDoc{Path: rel}, nil
	}
	if err != nil {
		return webapi.MemoryDoc{}, err
	}
	return webapi.MemoryDoc{Path: rel, Text: strings.TrimRight(string(raw), "\n")}, nil
}

// ----------------------------------------------------------------- files

// FilesDir is the card's worktree, the one directory the page may open
// files from (webapi.Files); ok is false while it has none.
func (d *WebDocs) FilesDir(ctx context.Context) (string, bool) {
	return filesDir(ctx, d.pool, d.f)
}

// filesDir is a card's branch worktree when it exists. A research card's
// scratch tree is left out: it is a throwaway checkout, not the card's.
func filesDir(ctx context.Context, pool *worktree.Pool, f domain.Feature) (string, bool) {
	if pool == nil || f.Kind == domain.KindResearch {
		return "", false
	}
	if ok, err := pool.Exists(ctx, &f); err != nil || !ok {
		return "", false
	}
	dir, err := pool.Path(&f)
	return dir, err == nil
}

// ------------------------------------------------------------------ diff

// diffLines is the card's diff as the diff surface reads it, split in
// the coordinates annotations use. why is set when there is none.
func (d *WebDocs) diffLines(ctx context.Context) (lines []string, why string, err error) {
	ok, err := d.pool.Exists(ctx, &d.f)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, noWorktreeYet(d.f), nil
	}
	raw, err := d.pool.Diff(ctx, &d.f)
	if err != nil {
		// fork drift and its kin are sentences a reader can act on
		return nil, err.Error(), nil
	}
	if strings.TrimSpace(raw) == "" {
		return nil, string(d.f.ID) + " has no change on its branch yet", nil
	}
	return diffannot.Lines(raw), "", nil
}

// Diff is the card's branch against its base, with the review comments
// anchored in it. since, when set, is a commit whose later changes are
// marked (DiffSince).
func (d *WebDocs) Diff(ctx context.Context, since string) (webapi.Diff, error) {
	out := webapi.Diff{Base: d.base, Files: []webapi.DiffFile{}, Annotations: []webapi.Annotation{}}
	lines, why, err := d.diffLines(ctx)
	if err != nil {
		return out, err
	}
	out.Why = why
	if lines != nil {
		out.Rev, _ = d.pool.Head(ctx, &d.f)
		out.BaseRev, _ = d.pool.DiffBase(ctx, &d.f)
	}
	var fresh map[string]map[int]bool
	if since != "" && lines != nil {
		raw, err := d.pool.DiffSince(ctx, &d.f, since)
		if err != nil {
			return out, invalid("%s", err.Error())
		}
		out.Since = since
		fresh = map[string]map[int]bool{}
		for _, f := range diffannot.Parse(diffannot.Lines(raw)) {
			added := map[int]bool{}
			for _, h := range f.Hunks {
				for _, l := range h.Lines {
					if l.Op == '+' {
						added[l.New] = true
					}
				}
			}
			fresh[f.Path] = added
		}
	}
	for _, f := range diffannot.Parse(lines) {
		added, touched := fresh[f.Path]
		out.Files = append(out.Files, webDiffFile(f, added, touched))
	}
	anns, err := d.store.ListDiffAnnotations(ctx, d.f.ID)
	if err != nil {
		return out, err
	}
	anchors := make([]string, len(anns))
	for i, a := range anns {
		anchors[i] = a.Anchor
	}
	located := diffannot.LocateAll(lines, anchors)
	for i, a := range anns {
		out.Annotations = append(out.Annotations, webAnnotation(a, located[i]))
		if !a.Resolved {
			out.PendingComments++
		}
	}
	return out, nil
}

// webAnnotation projects one stored diff comment located at idx.
func webAnnotation(a domain.DiffAnnotation, idx int) webapi.Annotation {
	w := webapi.Annotation{
		ID: a.ID, File: a.File, Idx: idx, Excerpt: a.Excerpt, Comment: a.Comment,
		Source: "gummi", Resolved: a.Resolved, At: a.CreatedAt, By: a.Author,
	}
	if a.SourceRef != "" {
		w.Source = "pr"
		w.By = prCommentAuthor(a.Comment)
	}
	return w
}

// prCommentAuthor reads the reviewer off a pulled thread's body, which
// pr.Ingest writes as "@login: body" blocks (an outdated one behind an
// "[outdated]" line).
func prCommentAuthor(body string) string {
	body = strings.TrimPrefix(body, "[outdated]\n\n")
	rest, ok := strings.CutPrefix(body, "@")
	if !ok {
		return ""
	}
	login, _, ok := strings.Cut(rest, ":")
	if !ok || strings.ContainsAny(login, " \n") {
		return ""
	}
	return login
}

// AddAnnotation comments on raw diff line idx, the way the diff surface's
// comment dialog does (newDiffAnnotation). text, when set, is the line
// as the page showed it; a diff that has moved under the page since is
// refused as ErrMoved rather than commenting on whatever line idx names
// now.
//
// person is who wrote it, recorded with it and shown beside it.
func (d *WebDocs) AddAnnotation(ctx context.Context, idx int, comment, text, person string) (webapi.Diff, error) {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return webapi.Diff{}, invalid("a comment needs some text")
	}
	lines, why, err := d.diffLines(ctx)
	if err != nil {
		return webapi.Diff{}, err
	}
	if lines == nil {
		return webapi.Diff{}, invalid("%s", why)
	}
	if idx < 0 || idx >= len(lines) {
		return webapi.Diff{}, ErrMoved
	}
	if text != "" && diffPayload(lines[idx]) != text {
		return webapi.Diff{}, ErrMoved
	}
	ann := newDiffAnnotation(d.f.ID, lines, idx, comment)
	ann.Author = strings.TrimSpace(person)
	if _, err := d.store.AddDiffAnnotation(ctx, ann, d.now()); err != nil {
		return webapi.Diff{}, err
	}
	return d.Diff(ctx, "")
}

// diffPayload is a diff line's text without its +/-/space marker.
func diffPayload(l string) string {
	if l != "" && (l[0] == '+' || l[0] == '-' || l[0] == ' ') {
		return l[1:]
	}
	return l
}

// annotation finds one of this card's annotations.
func (d *WebDocs) annotation(ctx context.Context, aid int64) (domain.DiffAnnotation, error) {
	anns, err := d.store.ListDiffAnnotations(ctx, d.f.ID)
	if err != nil {
		return domain.DiffAnnotation{}, err
	}
	for _, a := range anns {
		if a.ID == aid {
			return a, nil
		}
	}
	return domain.DiffAnnotation{}, ErrNoCard
}

// DeleteAnnotation removes one of the card's comments (the diff
// surface's D).
func (d *WebDocs) DeleteAnnotation(ctx context.Context, aid int64) (webapi.Diff, error) {
	if _, err := d.annotation(ctx, aid); err != nil {
		return webapi.Diff{}, err
	}
	if err := d.store.DeleteDiffAnnotation(ctx, aid); err != nil {
		return webapi.Diff{}, err
	}
	return d.Diff(ctx, "")
}

// ResolveAnnotation marks one of the card's comments resolved, or open
// again (the diff surface's x, which toggles; a page says which it
// means, so two viewers pressing it at once agree on the outcome).
func (d *WebDocs) ResolveAnnotation(ctx context.Context, aid int64, resolved bool) (webapi.Diff, error) {
	if _, err := d.annotation(ctx, aid); err != nil {
		return webapi.Diff{}, err
	}
	if err := d.store.SetDiffAnnotationResolved(ctx, aid, resolved); err != nil {
		return webapi.Diff{}, err
	}
	return d.Diff(ctx, "")
}

// -------------------------------------------------------------------- pr

// PRLink names the card's linked pull request as PR.Ref does ("" when
// none is linked), read from the store without asking GitHub.
func (d *WebDocs) PRLink(ctx context.Context) string {
	ref := d.fresh(ctx).PullRequest
	if ref.Empty() {
		return ""
	}
	return ref.Repo + "#" + strconv.Itoa(ref.Number)
}

// PR is the card's linked pull request, read from GitHub now: its state,
// its unresolved review threads and its conversation. A gh failure is
// reported in Error, never as a failed read — the push command a person
// needs does not depend on GitHub answering.
func (d *WebDocs) PR(ctx context.Context) webapi.PR {
	f := d.fresh(ctx)
	out := webapi.PR{PushCommand: d.pushCommand(ctx, f), Fetched: d.now()}
	ref := f.PullRequest
	if ref.Empty() {
		return out
	}
	out.Linked = true
	out.Ref = ref.Repo + "#" + strconv.Itoa(ref.Number)
	out.URL = ref.URL
	out.HeadSHA = ref.HeadSHA
	var errs []string
	st, n, head, err := pr.LiveStatus(ctx, pr.GHBinary(), ref)
	if err != nil {
		errs = append(errs, err.Error())
	} else {
		out.State, out.CommentCount = st, n
		if head != "" {
			out.HeadSHA = head
		}
	}
	fetch := d.threads
	if fetch == nil {
		fetch = func(ctx context.Context, ref domain.PullRequestRef) ([]pr.ReviewThread, []pr.TopLevelComment, string, error) {
			return pr.FetchReviewThreads(ctx, pr.GHBinary(), ref)
		}
	}
	threads, top, _, err := fetch(ctx, ref)
	if err != nil {
		errs = append(errs, err.Error())
	}
	for _, t := range threads {
		wt := webapi.PRThread{Path: t.Path, Line: hunkTailLine(t.DiffHunk), Resolved: t.IsResolved, Outdated: t.IsOutdated, Notes: []webapi.PRNote{}}
		for _, c := range t.Comments {
			wt.Notes = append(wt.Notes, webapi.PRNote{Author: c.AuthorLogin, Body: c.Body})
		}
		out.Threads = append(out.Threads, wt)
	}
	for _, c := range top {
		out.Comments = append(out.Comments, webapi.PRNote{Author: c.AuthorLogin, Body: c.Body})
	}
	if len(errs) > 0 {
		out.Error = threadfold.Sanitize(strings.Join(errs, "; "))
	}
	return out
}

// pushCommand is the command that publishes the card's branch: to the
// remote branch it already tracks (an adopted branch's own upstream),
// otherwise to origin under its own name. gummi never runs it (§20.5).
func (d *WebDocs) pushCommand(ctx context.Context, f domain.Feature) string {
	if f.IsFreeform() && f.Stage == domain.StageTodo {
		return ""
	}
	// a research card has a branch name and never a branch: there is
	// nothing to push
	if f.Kind == domain.KindResearch {
		return ""
	}
	branch := f.BranchName()
	if branch == "" {
		return ""
	}
	if remote, rb, ok := d.pool.Upstream(ctx, &f); ok {
		if rb == branch {
			return "git push " + remote + " " + branch
		}
		return "git push " + remote + " " + branch + ":" + rb
	}
	return "git push -u origin " + branch
}

// hunkTailLine is the new-side line a review thread's diff hunk ends on:
// the line GitHub anchors the thread to.
func hunkTailLine(hunk string) int {
	lines := strings.Split(hunk, "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "@@") {
		return 0
	}
	parsed := diffannot.Parse(append([]string{"diff --git a/x b/x"}, lines...))
	if len(parsed) == 0 || len(parsed[0].Hunks) == 0 {
		return 0
	}
	hl := parsed[0].Hunks[0].Lines
	if len(hl) == 0 {
		return 0
	}
	last := hl[len(hl)-1]
	if last.New > 0 {
		return last.New
	}
	return last.Old
}

// WebPullPR starts the card's "pull PR review" action — the PR's review
// threads read back onto the diff as comments — as the board's own
// command (pullPRReview), under the card's lock like the terminal's. Its
// outcome arrives as a toast and a card change.
func (m *Shell) WebPullPR(id string) (tea.Cmd, error) {
	r, ok := m.rowByID(domain.FeatureID(id))
	if !ok {
		return nil, ErrNoCard
	}
	// whether a pull request is linked is the command's to check: it
	// re-reads the card, where the row here may predate the link
	return m.pullPRReview(r.F), nil
}

// WebRequestSpecChanges is the spec surface's R: the card's open spec
// comments go to its writer, the way the terminal sends them
// (specChanges). POST /api/cards/{id}/spec/changes.
func (m *Shell) WebRequestSpecChanges(id, person, confirm string) (tea.Cmd, error) {
	r, err := m.webRowFor(id)
	if err != nil {
		return nil, err
	}
	f := r.F
	path := ""
	if !f.IsFreeform() {
		path = m.artifactFile(&f)
	}
	if path == "" {
		return nil, webErr(WebConflict, "%s has no %s to send comments from", f.ID, artifactNoun(f.Kind))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, webErr(WebUnavailable, "reading the %s: %v", artifactNoun(f.Kind), err)
	}
	m.webActor = state.PersonActor(person)
	defer func() { m.webActor = "" }()
	cmd, refused, ask := m.specChanges(f, spec.Parse(string(raw)))
	return m.webChanges(cmd, refused, ask, confirm)
}

// WebRequestDiffChanges is the diff surface's R: the card's open diff
// comments go to the implementer, or to a freeform card's session, the
// way the terminal sends them (diffChanges).
// POST /api/cards/{id}/diff/changes.
func (m *Shell) WebRequestDiffChanges(id, person, confirm string) (tea.Cmd, error) {
	r, err := m.webRowFor(id)
	if err != nil {
		return nil, err
	}
	anns, err := m.store.ListDiffAnnotations(context.Background(), r.F.ID)
	if err != nil {
		return nil, webErr(WebUnavailable, "reading the diff comments: %v", err)
	}
	// who asked, as a bounce to implement records it: diffChanges reads
	// it through humanActor before it returns
	m.webActor = state.PersonActor(person)
	defer func() { m.webActor = "" }()
	cmd, refused, ask := m.diffChanges(r.F, anns)
	return m.webChanges(cmd, refused, ask, confirm)
}

// webChanges turns a surface's answer into the web's: a send-back that
// moves the card is asked first, as the terminal's confirm asks it, and
// goes only with the yes to that question's token (webConfirmToken); a
// refusal is the terminal's status-band sentence, classed as the HTTP
// answer will be.
func (m *Shell) webChanges(cmd tea.Cmd, refused noticeMsg, ask *changesAsk, confirm string) (tea.Cmd, error) {
	if ask != nil {
		tok := webConfirmToken("confirm-request-changes", ask.card, ask.question)
		in := webInput{confirm: confirm}
		if !in.takeConfirm(tok) {
			return nil, &WebError{Code: WebConflict, Reason: webapi.ConflictConfirm, Text: ask.question, Confirm: tok}
		}
		return ask.do(), nil
	}
	if cmd != nil {
		return cmd, nil
	}
	code := WebConflict
	if m.engine == nil {
		code = WebUnavailable
	}
	return nil, webErr(code, "%s", refused.text)
}

// ----------------------------------------------------------------- stats

// Stats is the card's run: cardrun's report over the inputs every
// surface gathers (cardrun.Gather).
func (d *WebDocs) Stats(ctx context.Context) (webapi.CardStats, error) {
	f := d.fresh(ctx)
	in, err := cardrun.Gather(ctx, d.store, f)
	if err != nil {
		return webapi.CardStats{}, err
	}
	return WebCardStats(cardrun.Report(in)), nil
}

// WebCardStats projects a card's run onto the wire.
func WebCardStats(r cardrun.Run) webapi.CardStats {
	out := webapi.CardStats{
		ID: string(r.ID), Title: r.Title, Kind: string(r.Kind), Stage: string(r.Stage),
		Sessions: make([]webapi.StatSession, 0, len(r.Sessions)),
		Money: webapi.StatMoney{
			Credits: r.Money.Credits, Estimated: r.Money.Estimated,
			FirstPass: r.Money.FirstPass, Rework: r.Money.Rework,
			Corrected: r.Money.Corrected, Reproved: r.Money.Reproved,
			Elsewhere: r.Money.Elsewhere, ElsewhereBy: webBuckets(r.Money.ElsewhereBy),
			ByStage: webBuckets(r.Money.ByStage), ByRole: webBuckets(r.Money.ByRole), ByModel: webBuckets(r.Money.ByModel),
		},
		Clock: webapi.StatClock{
			AgentMs: r.Clock.Agent.Milliseconds(), OnYouMs: r.Clock.OnYou.Milliseconds(),
			IdleMs: r.Clock.Idle.Milliseconds(), ElapsedMs: r.Clock.Elapsed.Milliseconds(),
		},
		Envelope: webapi.StatEnvelope{Credits: r.Envelope.Granted, Left: float64(r.Envelope.Granted) - r.Envelope.Spent},
	}
	for _, s := range r.Sessions {
		ws := webapi.StatSession{
			Stage: string(s.Stage), Role: s.Role, Flavor: s.Flavor, Model: s.Model, Started: s.Started,
			EndInferred: s.EndInferred, Turns: s.Turns, Tools: s.Tools, ToolFails: s.ToolFails,
			Credits: s.Credits, Estimated: s.Estimated, Verdict: s.Verdict,
			Redo: s.Redo, RedoReason: s.RedoReason, Reconstructed: s.Reconstructed,
		}
		if s.Closed {
			ws.Ended = s.Ended
		}
		out.Sessions = append(out.Sessions, ws)
	}
	return out
}

func webTokens(t fleetrun.Tokens) webapi.Tokens {
	return webapi.Tokens{Input: t.Input, Cached: t.Cached, Output: t.Output}
}

func webBuckets(bs []cardrun.Bucket) []webapi.Bucket {
	out := make([]webapi.Bucket, 0, len(bs))
	for _, b := range bs {
		out = append(out, webapi.Bucket{Name: b.Name, Credits: b.Credits})
	}
	return out
}

// ----------------------------------------------------------------- fleet

// WebFleet is the board's stats tab over any window, captured from the
// model for reading off the loop.
type WebFleet struct {
	store *state.Store
	rows  []featureRow
	// busy is the board's running set at capture (Shell.boardBusy), so
	// the page's "running now" is the header pill's count.
	busy map[domain.FeatureID]bool
	now  func() time.Time
}

// WebFleet captures the board's rows for the fleet report.
func (m *Shell) WebFleet() (*WebFleet, error) {
	if !m.attached() || m.store == nil {
		return nil, ErrDetached
	}
	return &WebFleet{store: m.store, rows: append([]featureRow(nil), m.rows...), busy: m.boardBusy(), now: m.now}, nil
}

// FleetDefaultWindow is the window the fleet opens on when the page asks
// for none: the TUI stats tab's own default.
func FleetDefaultWindow() time.Duration { return wsWindows[wsDefaultPreset] }

// Report folds the fleet over [from, to) — the stats tab's own builder
// (buildFleetReport), so the page and the tab cannot disagree. A zero
// from is the workspace's whole history; a zero to is now.
func (w *WebFleet) Report(ctx context.Context, from, to time.Time) (webapi.Fleet, error) {
	now := w.now()
	if to.IsZero() || to.After(now) {
		to = now
	}
	if !from.IsZero() && !from.Before(to) {
		return webapi.Fleet{}, invalid("the window must start before it ends")
	}
	rep, err := buildFleetReport(ctx, w.store, w.rows, fleetrun.Window{From: from, To: to}, now, w.busy)
	if err != nil {
		return webapi.Fleet{}, err
	}
	return WebFleetReport(*rep), nil
}

// WebFleetReport projects the fleet fold onto the wire.
func WebFleetReport(r fleetrun.Report) webapi.Fleet {
	out := webapi.Fleet{
		From: r.Window.From, To: r.Window.To,
		Credits: r.Credits, Estimated: r.Estimated, Rework: r.Rework, Corrected: r.Corrected, Reproved: r.Reproved,
		ByStage: webBuckets(r.ByStage), ByModel: webBuckets(r.ByModel),
		AgentMs: r.Agent.Milliseconds(), OnYouMs: r.OnYou.Milliseconds(), IdleMs: r.Idle.Milliseconds(),
		ElapsedMs: r.Elapsed.Milliseconds(),
		Running:   r.Running, PeakLanes: r.PeakLanes,
		Tokens:         webTokens(r.Tokens),
		Lanes:          make([]webapi.Lane, 0, len(r.Lanes)),
		AllTimeCredits: r.AllTime.Credits, AllTimeCards: r.AllTime.Cards,
	}
	// An all-history window starts at the zero time in the fold; on the
	// wire it starts where the history does — the first activity the
	// fold measured its rate from — or, when nothing ever ran, at its
	// own end: an empty window, never the year 1.
	if out.From.IsZero() {
		out.From = r.Window.To
		if r.RateSpan > 0 {
			out.From = r.Window.To.Add(-r.RateSpan)
		}
	}
	if r.BusiestAgent > 0 {
		out.Busiest = &webapi.Busiest{From: r.Busiest, LenMs: r.BusiestLen.Milliseconds(), AgentMs: r.BusiestAgent.Milliseconds()}
	}
	for _, l := range r.Lanes {
		wl := webapi.Lane{
			ID: string(l.ID), Title: l.Title, Kind: string(l.Kind), Ending: string(l.Ending),
			Credits: l.Credits, Redo: l.Redo, Tokens: webTokens(l.Tokens), Note: l.Note,
			Running: l.Running, OpenWaitFrom: l.OpenWaitFrom,
			Blocks: make([]webapi.Span, 0, len(l.Blocks)), Gates: l.Gates, LandedAt: l.LandedAt,
		}
		for _, b := range l.Blocks {
			sp := webapi.Span{From: b.From, To: b.To, Stage: string(b.Stage)}
			if b.Open {
				sp.To = time.Time{}
			}
			wl.Blocks = append(wl.Blocks, sp)
		}
		for _, sp := range l.Waits {
			wl.Waits = append(wl.Waits, webapi.Span{From: sp.From, To: sp.To})
		}
		out.Lanes = append(out.Lanes, wl)
	}
	return out
}

// webDiffFile projects one parsed file onto the wire. added, when the
// diff is read against a commit, is the new-side lines changed since it,
// and touched whether the file was.
func webDiffFile(f diffannot.File, added map[int]bool, touched bool) webapi.DiffFile {
	wf := webapi.DiffFile{
		Path: f.Path, OldPath: f.OldPath, Status: f.Status, Binary: f.Binary,
		Add: f.Add, Del: f.Del, Since: touched, Hunks: make([]webapi.Hunk, 0, len(f.Hunks)),
	}
	for _, h := range f.Hunks {
		wh := webapi.Hunk{Header: h.Header, Lines: make([]webapi.DiffLine, 0, len(h.Lines))}
		for _, l := range h.Lines {
			wh.Lines = append(wh.Lines, webapi.DiffLine{
				T: string(l.Op), Old: l.Old, New: l.New, Text: l.Text, Idx: l.Idx,
				Since: l.Op == '+' && added[l.New],
			})
		}
		wf.Hunks = append(wf.Hunks, wh)
	}
	return wf
}

// groupActivity folds each run of tool calls and thoughts between two other
// items into one activity item. A run keeps its first item's key, so it
// stays the same row as it grows.
func groupActivity(items []webapi.Item) []webapi.Item {
	out := make([]webapi.Item, 0, len(items))
	for _, it := range items {
		if !isActivity(it) {
			out = append(out, it)
			continue
		}
		if n := len(out); n > 0 && out[n-1].T == webapi.ItemActivity {
			a := &out[n-1]
			a.Items = append(a.Items, it)
			a.Seq = max(a.Seq, it.Seq)
			continue
		}
		out = append(out, webapi.Item{Key: "act:" + it.Key, Seq: it.Seq, T: webapi.ItemActivity, Time: it.Time, Stage: it.Stage, Items: []webapi.Item{it}})
	}
	return out
}

func isActivity(it webapi.Item) bool {
	return it.T == webapi.ItemTools || it.T == webapi.ItemMessage && it.Author == string(engine.AuthorThinking)
}
