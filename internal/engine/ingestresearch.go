package engine

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// IngestResearch turns an approved research card's `## Slices` table
// straight into an IngestResult — no architect pass, no `propose_features`
// call, no tokens spent. The shape stage already converged the table into
// the exact shape a proposal needs, so this is a mechanical parse rather
// than a re-decomposition. Unlike Ingest, every failure here is loud: a
// missing/blank `## Slices` section, no fenced `yaml` block, unparseable
// YAML, or a row with a blank title each return a non-nil error and an
// empty result, never a partial one.
func (e *Engine) IngestResearch(ctx context.Context, rsCard domain.Feature) (domain.IngestResult, error) {
	path := e.artifactFile(&rsCard)
	if path == "" {
		return domain.IngestResult{}, fmt.Errorf("%s: no artifact found", rsCard.ID)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return domain.IngestResult{}, fmt.Errorf("reading %s: %w", path, err)
	}
	artifact := string(raw)

	sliceBody, ok := spec.ViewSection(artifact, "Slices")
	if !ok || strings.TrimSpace(sliceBody) == "" {
		return domain.IngestResult{}, fmt.Errorf("%s: `## Slices` section is missing or blank", rsCard.ID)
	}
	m := sliceYAMLFenceRe.FindStringSubmatch(sliceBody)
	if m == nil {
		return domain.IngestResult{}, fmt.Errorf("%s: `## Slices` has no fenced ```yaml block", rsCard.ID)
	}
	rows, err := parseSliceRows(m[1])
	if err != nil {
		return domain.IngestResult{}, fmt.Errorf("%s: `## Slices` yaml is unparseable: %w", rsCard.ID, err)
	}

	var proposals []domain.FeatureProposal
	for _, r := range rows {
		if isScaffoldRow(r) {
			continue
		}
		title := strings.TrimSpace(r.Title)
		if title == "" {
			return domain.IngestResult{}, fmt.Errorf("%s: a `## Slices` row has a blank title", rsCard.ID)
		}
		proposals = append(proposals, domain.FeatureProposal{
			Title:     title,
			OneLiner:  strings.TrimSpace(r.OneLiner),
			DependsOn: r.DependsOn,
		})
	}
	if len(proposals) == 0 {
		return domain.IngestResult{}, fmt.Errorf("%s: `## Slices` has no usable proposals", rsCard.ID)
	}

	coverage := coverageFromSlices(rows)
	oosBody, _ := spec.ViewSection(artifact, "Out of scope")
	for _, bullet := range parseOutOfScopeBullets(oosBody) {
		coverage = append(coverage, domain.CoverageEntry{
			Status:      domain.CoverageOutOfScope,
			Requirement: bullet.Key,
			Note:        bullet.Prose,
		})
	}

	return domain.IngestResult{
		SourcePath: string(rsCard.ID) + " " + rsCard.Slug,
		Proposals:  proposals,
		Coverage:   coverage,
	}, nil
}

// unsettledSliceRows reads rsCard's artifact and returns the `## Slices`
// rows still lacking a minted FD id, in document order, with the scaffold
// row dropped. It is the single source of "not yet decomposed" state
// FD-081's decompose gate reads — no store column, no session flag: a row
// settles the moment its `id:` is filled in (by this pass or by hand) and
// reopens the moment that field is cleared.
func unsettledSliceRows(artifact string) ([]sliceRow, error) {
	sliceBody, ok := spec.ViewSection(artifact, "Slices")
	if !ok || strings.TrimSpace(sliceBody) == "" {
		return nil, fmt.Errorf("`## Slices` section is missing or blank")
	}
	m := sliceYAMLFenceRe.FindStringSubmatch(sliceBody)
	if m == nil {
		return nil, fmt.Errorf("`## Slices` has no fenced ```yaml block")
	}
	rows, err := parseSliceRows(m[1])
	if err != nil {
		return nil, fmt.Errorf("`## Slices` yaml is unparseable: %w", err)
	}
	var out []sliceRow
	for _, r := range rows {
		if isScaffoldRow(r) || strings.TrimSpace(r.ID) != "" {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// sliceRow is one row of the `## Slices` fenced-YAML scaffold (FD-076's
// internal/spec/research.go). id is decoded but never read here — it is
// the back-annotation slot FD-081's decompose gate fills in.
type sliceRow struct {
	Title        string   `yaml:"title"`
	OneLiner     string   `yaml:"one-liner"`
	DependsOn    []string `yaml:"depends-on"`
	Requirements []string `yaml:"requirements"`
	ID           string   `yaml:"id"`
}

var sliceYAMLFenceRe = regexp.MustCompile("(?s)```ya?ml\\s*\\n(.*?)```")

// parseSliceRows decodes the `## Slices` fenced-YAML body into rows, in
// document order (yaml.v3 preserves sequence order on unmarshal).
func parseSliceRows(yamlBody string) ([]sliceRow, error) {
	var rows []sliceRow
	if err := yaml.Unmarshal([]byte(yamlBody), &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// isScaffoldRow reports whether r is the FD-076 template's unedited
// example row — the one every blank research document ships with. Only
// its four content fields are compared (byte-exact); id is ignored since
// it is never populated at this stage. A row the user renamed but left
// otherwise blank is a real proposal, not scaffold.
func isScaffoldRow(r sliceRow) bool {
	return r.Title == "example slice" &&
		r.OneLiner == "what it mints" &&
		len(r.DependsOn) == 0 &&
		len(r.Requirements) == 0
}

// coverageFromSlices synthesizes the Mapped half of coverage deterministically
// from the parsed rows: one entry per trimmed non-empty requirement key, in
// (row, key) order. It never emits CoverageUnmapped — FD-078's verify gate
// guarantees no unmapped requirement survives to reach this pass.
func coverageFromSlices(rows []sliceRow) []domain.CoverageEntry {
	var out []domain.CoverageEntry
	for _, r := range rows {
		for _, req := range r.Requirements {
			if req = strings.TrimSpace(req); req != "" {
				out = append(out, domain.CoverageEntry{
					Status:      domain.CoverageMapped,
					Requirement: req,
					Feature:     r.Title,
				})
			}
		}
	}
	return out
}

// oosBullet is one parsed `- key: prose` line from `## Out of scope`.
type oosBullet struct {
	Key   string
	Prose string
}

var oosBulletRe = regexp.MustCompile(`^-\s+(\S+):\s*(.*)$`)

// parseOutOfScopeBullets walks a research document's `## Out of scope`
// body line by line and extracts every `- key: prose` bullet, in document
// order. Blank lines and `%%` marker lines are skipped, as is any other
// line that doesn't match the bullet shape — a section whose only content
// is its `%% @gummi:` prompt yields nil.
func parseOutOfScopeBullets(sectionBody string) []oosBullet {
	var out []oosBullet
	for _, line := range strings.Split(sectionBody, "\n") {
		if strings.TrimSpace(line) == "" || spec.IsMarkerLine(line) {
			continue
		}
		m := oosBulletRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		out = append(out, oosBullet{Key: m[1], Prose: strings.TrimSpace(m[2])})
	}
	return out
}
