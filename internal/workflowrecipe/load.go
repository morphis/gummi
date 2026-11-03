package workflowrecipe

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/gummidir"
)

//go:embed bundled/*.yaml
var bundled embed.FS

// nameRe is a recipe's name as a file name: no path, no dot-file.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Source says where a recipe came from.
type Source string

const (
	// SourceWorkspace is .gummi/workflows/<name>.yaml in the workspace.
	SourceWorkspace Source = "workspace"
	// SourceBundled is one of gummi's own.
	SourceBundled Source = "bundled"
)

// Found is a recipe as Load and List find it: its name, where it came
// from, its text as read, and the recipe parsed from it.
type Found struct {
	Name   string
	Source Source
	YAML   []byte
	Recipe domain.Recipe
	// Err is why a listed recipe would not parse; Recipe is empty then.
	Err error
}

// ErrNoRecipe is a name neither the workspace nor gummi has a recipe of.
var ErrNoRecipe = errors.New("no such workflow recipe")

// WorkflowsDir is where a workspace keeps its own recipes.
func WorkflowsDir(workspaceDir string) string {
	return gummidir.Path(workspaceDir, "workflows")
}

// CheckName refuses, with a sentence, a name no recipe file may have.
func CheckName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%q is not a recipe name: letters, digits, - and _, starting with a letter or a digit", name)
	}
	return nil
}

// Taken says where a recipe called name already is — the workspace's own
// (a file there, whether it parses or not) or gummi's bundled one — and
// reports false when none is.
func Taken(name, workspaceDir string) (Source, bool) {
	if workspaceDir != "" {
		if _, err := os.Stat(filepath.Join(WorkflowsDir(workspaceDir), name+".yaml")); err == nil {
			return SourceWorkspace, true
		}
	}
	if _, err := bundled.ReadFile("bundled/" + name + ".yaml"); err == nil {
		return SourceBundled, true
	}
	return "", false
}

// Load finds the recipe called name: the workspace's own
// (.gummi/workflows/<name>.yaml under workspaceDir) first, then gummi's
// bundled one, so a workspace may override a bundled recipe by name.
func Load(name, workspaceDir string) (Found, error) {
	if err := CheckName(name); err != nil {
		return Found{}, err
	}
	if workspaceDir != "" {
		data, err := os.ReadFile(filepath.Join(WorkflowsDir(workspaceDir), name+".yaml")) // #nosec G304 -- name is checked against nameRe
		switch {
		case err == nil:
			return found(name, SourceWorkspace, data)
		case !errors.Is(err, fs.ErrNotExist):
			return Found{}, err
		}
	}
	data, err := bundled.ReadFile("bundled/" + name + ".yaml")
	if err != nil {
		return Found{}, fmt.Errorf("%w: %s", ErrNoRecipe, name)
	}
	return found(name, SourceBundled, data)
}

func found(name string, src Source, data []byte) (Found, error) {
	r, err := Parse(data)
	if err != nil {
		return Found{}, fmt.Errorf("recipe %s (%s): %w", name, src, err)
	}
	return Found{Name: name, Source: src, YAML: data, Recipe: r}, nil
}

// List names every recipe a session may run, by name: the workspace's own
// and the bundled ones, a workspace recipe standing in for a bundled one of
// its name. A workspace recipe that does not parse is listed with its Err.
func List(workspaceDir string) ([]Found, error) {
	byName := map[string]Found{}
	names, err := fs.Glob(bundled, "bundled/*.yaml")
	if err != nil {
		return nil, err
	}
	for _, p := range names {
		name := strings.TrimSuffix(filepath.Base(p), ".yaml")
		data, err := bundled.ReadFile(p)
		if err != nil {
			return nil, err
		}
		f, err := found(name, SourceBundled, data)
		if err != nil {
			return nil, err
		}
		byName[name] = f
	}
	if workspaceDir != "" {
		ents, err := os.ReadDir(WorkflowsDir(workspaceDir))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		for _, e := range ents {
			name, ok := strings.CutSuffix(e.Name(), ".yaml")
			if e.IsDir() || !ok || !nameRe.MatchString(name) {
				continue
			}
			f, err := Load(name, workspaceDir)
			if err != nil {
				f = Found{Name: name, Source: SourceWorkspace, Err: err}
			}
			byName[name] = f
		}
	}
	out := make([]Found, 0, len(byName))
	for _, f := range byName {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
