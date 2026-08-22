package spec

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/morphis/gummi/internal/domain"
)

// sliceYAMLFenceRe matches the fenced-YAML block inside a `## Slices`
// section body — the same shape internal/engine's sliceYAMLFenceRe
// matches, duplicated here so this package never imports engine's
// unexported row type.
var sliceYAMLFenceRe = regexp.MustCompile("(?s)```ya?ml\\s*\\n(.*?)```")

// sliceRow mirrors internal/engine's sliceRow: the `## Slices` fenced-YAML
// row shape, decoded and re-encoded here so SetSliceIDsPositional never
// needs to import engine's unexported type.
type sliceRow struct {
	Title        string   `yaml:"title"`
	OneLiner     string   `yaml:"one-liner"`
	DependsOn    []string `yaml:"depends-on"`
	Requirements []string `yaml:"requirements"`
	ID           string   `yaml:"id"`
}

// isScaffoldSliceRow mirrors engine's isScaffoldRow predicate byte-for-byte
// so the two packages agree on which row is the unedited template example.
func isScaffoldSliceRow(r sliceRow) bool {
	return r.Title == "example slice" &&
		r.OneLiner == "what it mints" &&
		len(r.DependsOn) == 0 &&
		len(r.Requirements) == 0
}

// SetSliceIDsPositional back-annotates a research document's `## Slices`
// rows with minted FD ids, keyed by doc-order position among unsettled
// rows (non-scaffold, blank `id:` — the same predicate engine's
// unsettledSliceRows applies): ids[i] is assigned to the i-th such row.
// len(ids) may be less than the unsettled count (a partial-mint prefix);
// len(ids) greater than the unsettled count is a caller bug and returns an
// error before anything is rewritten, so a title the architect quietly
// rewrote — or a row an operator settled by hand between decompose and
// approve — can never silently misalign the assignment.
func SetSliceIDsPositional(artifact string, ids []domain.FeatureID) (string, error) {
	sliceBody, ok := ViewSection(artifact, "Slices")
	if !ok || strings.TrimSpace(sliceBody) == "" {
		return "", fmt.Errorf("`## Slices` section is missing or blank")
	}
	loc := sliceYAMLFenceRe.FindStringSubmatchIndex(sliceBody)
	if loc == nil {
		return "", fmt.Errorf("`## Slices` has no fenced ```yaml block")
	}
	fenceBody := sliceBody[loc[2]:loc[3]]
	var rows []sliceRow
	if err := yaml.Unmarshal([]byte(fenceBody), &rows); err != nil {
		return "", fmt.Errorf("`## Slices` yaml is unparseable: %w", err)
	}

	var unsettled []int
	for i, r := range rows {
		if isScaffoldSliceRow(r) || strings.TrimSpace(r.ID) != "" {
			continue
		}
		unsettled = append(unsettled, i)
	}
	if len(ids) > len(unsettled) {
		return "", fmt.Errorf("SetSliceIDsPositional: %d ids given but only %d unsettled rows", len(ids), len(unsettled))
	}
	for i, id := range ids {
		rows[unsettled[i]].ID = string(id)
	}

	// preserve any leading comment/blank lines verbatim (the scaffold's
	// field-guide comment) — yaml.Marshal only re-renders the list itself.
	var preamble strings.Builder
	for _, line := range strings.Split(fenceBody, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			preamble.WriteString(line)
			preamble.WriteString("\n")
			continue
		}
		break
	}

	out, err := yaml.Marshal(rows)
	if err != nil {
		return "", err
	}
	newFenceBody := preamble.String() + string(out)
	newSliceBody := sliceBody[:loc[2]] + newFenceBody + sliceBody[loc[3]:]
	newArtifact, _, err := ReplaceSection(artifact, "Slices", newSliceBody)
	if err != nil {
		return "", err
	}
	return newArtifact, nil
}
