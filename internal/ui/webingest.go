package ui

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/webapi"
)

// Spec ingest on the web face (DESIGN §11.4). The board holds one pass at
// a time — the live feed (m.ingestRun) while the architect decomposes,
// then the review surface (m.ingest) until it is approved or discarded —
// and the web face works that same state rather than a copy: starting a
// pass is startIngest, an edit is one of the review's own edits, and
// approving is the confirm's yes. What the web adds is a name for the
// pass, so a page can tell the one it started from the next.

// webIngestState is the board's pass as the web face names it. It is
// kept by syncWebIngest, which watches the two surfaces the TUI keeps.
type webIngestState struct {
	// id numbers the passes this process has seen; 0 is none yet.
	id     int
	run    *ingestRunView
	review *ingestView
	state  string
	steps  []webapi.IngestStep
	tail   string
	source string
	err    string
	// created is what an approval minted.
	created []webapi.CardRef
}

// syncWebIngest follows the TUI's ingest surfaces after each message and
// reports a change to the web's pass. It notices a pass however it began
// — the web's own start, or a research card's decompose re-run from its
// card page — because both open the same feed.
func (m *Shell) syncWebIngest(msg tea.Msg) {
	w := &m.webIngest
	changed := false
	if m.ingestRun != nil && m.ingestRun != w.run {
		*w = webIngestState{id: w.id + 1, run: m.ingestRun, state: webapi.IngestRunning, source: m.ingestRun.source}
		changed = true
	}
	switch msg := msg.(type) {
	case ingestStepMsg:
		if m.ingestRun != nil && m.ingestRun == w.run {
			w.steps, w.tail = webIngestSteps(m.ingestRun), m.ingestRun.tail
			changed = true
		}
	case ingestLoadedMsg:
		if msg.err != nil && w.state == webapi.IngestRunning {
			w.state, w.err = webapi.IngestFailed, "ingest: "+sanitize(msg.err.Error())
			changed = true
		}
		if msg.err == nil && m.ingest == nil && w.state == webapi.IngestRunning {
			// a decompose re-run that found nothing unsettled opens no
			// review: the pass is over with nothing to approve
			w.state = webapi.IngestMaterialized
			changed = true
		}
	}
	if m.ingest != nil && m.ingest != w.review {
		if w.id == 0 || w.state != webapi.IngestRunning {
			w.id++
			w.steps, w.tail, w.err, w.created = nil, "", "", nil
		}
		w.run, w.review, w.state, w.source = nil, m.ingest, webapi.IngestReview, m.ingest.source
		changed = true
	}
	if m.ingest == nil && w.review != nil && w.state == webapi.IngestReview {
		// closed by something other than the web's approve: the TUI's
		// discard (or a quit, which ends the process anyway)
		w.review, w.state = nil, webapi.IngestDiscarded
		changed = true
	}
	if changed {
		m.EmitChange(webapi.Change{Kind: webapi.ChangeIngest, ID: strconv.Itoa(w.id)})
	}
}

// webIngestSteps copies the live feed's milestones and tool calls.
func webIngestSteps(rv *ingestRunView) []webapi.IngestStep {
	out := make([]webapi.IngestStep, 0, len(rv.activity))
	for _, a := range rv.activity {
		kind := string(engine.IngestStepTool)
		if a.note {
			kind = string(engine.IngestStepNote)
		}
		out = append(out, webapi.IngestStep{Kind: kind, Text: sanitize(a.text)})
	}
	return out
}

// WebIngest is GET /api/ingest/{run}; an empty id asks for the board's
// current pass (GET /api/ingest).
func (m *Shell) WebIngest(id string) (webapi.IngestRun, error) {
	w := &m.webIngest
	if id == "" && w.id == 0 {
		// no pass yet: the current one is none, which is an answer, not a
		// missing page (the view reads an empty id as "no run")
		return webapi.IngestRun{}, nil
	}
	if w.id == 0 || (id != "" && id != strconv.Itoa(w.id)) {
		return webapi.IngestRun{}, webErr(WebNotFound, "no ingest %s on this board", id)
	}
	out := webapi.IngestRun{
		ID: strconv.Itoa(w.id), State: w.state, Source: w.source, Error: w.err,
		Steps: w.steps, Commentary: strings.TrimSpace(sanitize(w.tail)), Created: w.created,
	}
	if w.run != nil && w.run == m.ingestRun {
		out.Steps, out.Commentary = webIngestSteps(w.run), strings.TrimSpace(sanitize(w.run.tail))
	}
	iv := w.review
	if iv == nil || iv != m.ingest {
		return out, nil
	}
	out.Profile, out.Repo, out.Envelope = iv.profile, iv.repo, iv.envelope
	out.Proposals = make([]webapi.IngestProposal, 0, len(iv.props))
	for i, ip := range iv.props {
		p := ip.p
		kind := p.Kind
		if kind == "" {
			kind = domain.KindFeature
		}
		out.Proposals = append(out.Proposals, webapi.IngestProposal{
			Index: i, Kind: string(kind), Title: p.Title, OneLiner: p.OneLiner,
			SourceRefs: p.SourceRefs, DependsOn: p.DependsOn,
			Problem: p.Draft.Problem, OpenQuestions: p.Draft.OpenQuestions,
			Dropped: ip.dropped,
		})
	}
	if len(iv.coverage) > 0 {
		cov := &webapi.IngestCoverage{}
		for _, c := range iv.coverage {
			switch c.Status {
			case domain.CoverageMapped:
				cov.Mapped++
			case domain.CoverageOutOfScope:
				cov.OutOfScope++
			case domain.CoverageUnmapped:
				cov.Unmapped++
				text := c.Requirement
				if c.Note != "" {
					text += " — " + c.Note
				}
				out.Unmapped = append(out.Unmapped, text)
			}
		}
		out.Coverage = cov
	}
	return out, nil
}

// StartIngest is POST /api/ingest: the ingest form's submit. A document
// pasted rather than named is saved under .gummi/ingest first, where the
// pass would have stashed it anyway. The pass runs on the board, not the
// request; its steps arrive as ingest changes.
func (b *Bridge) StartIngest(ctx context.Context, req webapi.IngestRequest) (webapi.IngestRun, error) {
	var (
		eng      *engine.Engine
		path     string
		profile  string
		envelope int
		perr     error
	)
	pasted := strings.TrimSpace(req.Markdown) != ""
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		eng, perr = m.engine, m.webIngestFree()
		if perr != nil {
			return nil
		}
		switch {
		case pasted && req.Path != "":
			perr = webErr(WebBadRequest, "send a path or a document, not both")
			return nil
		case !pasted:
			if path, perr = m.webWorkspaceFile(req.Path); perr != nil {
				return nil
			}
		}
		if err := m.requireRepo(req.Repo); err != nil {
			perr = webErr(WebBadRequest, "%s", err.Error())
			return nil
		}
		profile = req.Profile
		if profile == "" {
			profiles := m.profileNames
			if len(profiles) == 0 {
				profiles = defaultProfilePresets
			}
			profile = profiles[0]
		}
		envelope = m.envelope
		if req.Envelope != nil {
			// refused before the pass runs: minting would refuse it only at
			// approve, after the review's edits
			if *req.Envelope < 0 {
				perr = webErr(WebBadRequest, "the envelope per card must be a whole, non-negative number of credits (0 is uncapped)")
				return nil
			}
			envelope = *req.Envelope
		}
		return nil
	}); err != nil {
		return webapi.IngestRun{}, err
	}
	if perr != nil {
		return webapi.IngestRun{}, perr
	}
	if pasted {
		_, abs, err := eng.StashIngestDocument(webIngestName(req.Name), []byte(req.Markdown))
		if err != nil {
			return webapi.IngestRun{}, err
		}
		path = abs
	}
	var (
		run webapi.IngestRun
		err error
	)
	derr := b.Do(ctx, func(m *Shell) tea.Cmd {
		// asked again: another pass may have started while the document
		// was being saved
		if err = m.webIngestFree(); err != nil {
			return nil
		}
		cmd := m.startIngestWith(path, profile, req.Repo, envelope)
		m.syncWebIngest(nil)
		run, err = m.WebIngest("")
		return cmd
	})
	if derr != nil {
		return webapi.IngestRun{}, derr
	}
	return run, err
}

// webIngestFree refuses a second pass while one is decomposing or waiting
// for review: the board holds one at a time.
func (m *Shell) webIngestFree() error {
	switch {
	case m.engine == nil:
		return webErr(WebUnavailable, "%s", m.noAgent(" — ingestion needs one"))
	case m.ingestRun != nil:
		return webErr(WebConflict, "an ingest is already decomposing — wait for it")
	case m.ingest != nil:
		return webErr(WebConflict, "an ingest is waiting for review — approve or discard it first")
	}
	return nil
}

// webIngestName is the file a pasted document is saved as: its name,
// made safe for a path, as markdown.
func webIngestName(name string) string {
	stem := strings.TrimSuffix(filepath.Base(strings.TrimSpace(name)), filepath.Ext(name))
	slug, err := domain.Slugify(stem)
	if err != nil {
		slug = "pasted"
	}
	return slug + ".md"
}

// webIngestReview returns the review the named pass is waiting in.
func (m *Shell) webIngestReview(id string) (*ingestView, error) {
	w := &m.webIngest
	if w.id == 0 || id != strconv.Itoa(w.id) {
		return nil, webErr(WebNotFound, "no ingest %s on this board", id)
	}
	if w.state != webapi.IngestReview || w.review == nil || w.review != m.ingest {
		return nil, webErr(WebConflict, "ingest %s is %s, not waiting for review", id, w.state)
	}
	return m.ingest, nil
}

// WebIngestEdit is POST /api/ingest/{run}/edit: one of the review
// surface's edits, on the proposal the request names instead of the one
// under the cursor.
func (m *Shell) WebIngestEdit(id string, req webapi.IngestEditRequest) (webapi.IngestRun, error) {
	iv, err := m.webIngestReview(id)
	if err != nil {
		return webapi.IngestRun{}, err
	}
	i := req.Index
	if i < 0 || i >= len(iv.props) {
		return webapi.IngestRun{}, webErr(WebBadRequest, "no proposal %d", i)
	}
	switch req.Op {
	case webapi.IngestEditRename:
		title := strings.TrimSpace(req.Title)
		if title == "" {
			return webapi.IngestRun{}, webErr(WebBadRequest, "a title is needed")
		}
		if err := iv.rename(i, title); err != nil {
			return webapi.IngestRun{}, webErr(WebBadRequest, "%s", err.Error())
		}
	case webapi.IngestEditOneLiner:
		iv.setOneLiner(i, strings.TrimSpace(req.OneLiner))
	case webapi.IngestEditDrop:
		iv.setDropped(i, true)
	case webapi.IngestEditUndrop:
		iv.setDropped(i, false)
	case webapi.IngestEditMerge:
		if !iv.mergeAt(i) {
			return webapi.IngestRun{}, webErr(WebConflict, "merge folds a proposal into the one above — the first has none")
		}
		iv.setCursor(iv.cursor)
	default:
		return webapi.IngestRun{}, webErr(WebBadRequest, "no review edit %q", req.Op)
	}
	m.EmitChange(webapi.Change{Kind: webapi.ChangeIngest, ID: id})
	return m.WebIngest(id)
}

// WebIngestDiscard is POST /api/ingest/{run}/discard: the review's esc,
// answered yes.
func (m *Shell) WebIngestDiscard(id string) (webapi.IngestRun, error) {
	if _, err := m.webIngestReview(id); err != nil {
		return webapi.IngestRun{}, err
	}
	m.discardIngest()
	m.syncWebIngest(nil)
	return m.WebIngest(id)
}

// ApproveIngest is POST /api/ingest/{run}/approve: the review's A,
// confirmed — the kept proposals minted into todo with the options the
// pass was started with. It answers once the cards exist.
func (b *Bridge) ApproveIngest(ctx context.Context, id string) (webapi.IngestRun, error) {
	var (
		mint func(context.Context) ([]domain.Feature, noticeMsg)
		perr error
	)
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		iv, err := m.webIngestReview(id)
		if err != nil {
			perr = err
			return nil
		}
		if iv.keptCount() == 0 {
			perr = webErr(WebConflict, "every proposal is dropped — nothing to create")
			return nil
		}
		if mint = m.takeIngest(); mint == nil {
			perr = webErr(WebUnavailable, "%s", m.noAgent(" — ingestion needs one"))
			return nil
		}
		m.webIngest.review, m.webIngest.state = nil, webapi.IngestMaterialized
		return nil
	}); err != nil {
		return webapi.IngestRun{}, err
	}
	if perr != nil {
		return webapi.IngestRun{}, perr
	}
	created, notice := mint(ctx)
	var (
		run webapi.IngestRun
		err error
	)
	derr := b.Do(ctx, func(m *Shell) tea.Cmd {
		w := &m.webIngest
		if strconv.Itoa(w.id) == id {
			w.created = make([]webapi.CardRef, 0, len(created))
			for _, f := range created {
				w.created = append(w.created, webapi.CardRef{ID: string(f.ID), Title: f.Title, Stage: string(f.Stage)})
			}
			if notice.isErr {
				w.state, w.err = webapi.IngestFailed, notice.text
			}
			m.EmitChange(webapi.Change{Kind: webapi.ChangeIngest, ID: id})
		}
		run, err = m.WebIngest(id)
		return func() tea.Msg { return notice }
	})
	if derr != nil {
		return webapi.IngestRun{}, derr
	}
	return run, err
}
