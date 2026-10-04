// Package agentplugins manages workspace-local agent and skill definitions.
package agentplugins

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/morphis/gummi/internal/atomicfile"
)

const (
	KindAgent = "agent"
	KindSkill = "skill"
)

var (
	ErrNotFound = errors.New("agent plugin item not found")
	ErrConflict = errors.New("agent plugin item already exists")
	ErrInvalid  = errors.New("invalid agent plugin request")
)

type Item struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Entry       string   `json:"entry"`
	SourcePath  string   `json:"sourcePath,omitempty"`
	Linked      bool     `json:"linked"`
	Global      bool     `json:"global"`
	Description string   `json:"description,omitempty"`
	Repos       []string `json:"repos,omitempty"`
}

func validateSourceTree(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: source contains a symbolic link at %s", ErrInvalid, filepath.Base(path))
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return fmt.Errorf("%w: source contains a non-regular file at %s", ErrInvalid, filepath.Base(path))
		}
		return nil
	})
}

type Detail struct {
	Item
	Content string `json:"content"`
}

type Candidate struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Repo        string `json:"repo"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

type Repo struct {
	Name string
	Root string
}

// parseDescriptionFromText scans the leading YAML frontmatter in text and
// returns the value of the `description:` key if present, otherwise "".
func parseDescriptionFromText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.HasPrefix(text, "---") {
		// find the closing frontmatter marker
		rest := text[3:]
		if i := strings.Index(rest, "---"); i >= 0 {
			block := rest[:i]
			for _, line := range strings.Split(block, "\n") {
				lower := strings.ToLower(strings.TrimSpace(line))
				if strings.HasPrefix(lower, "description:") {
					// preserve the original trimmed suffix after the key
					val := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "description:"))
					return strings.Trim(val, " \t\"')(")
				}
			}
		}
	}
	return ""
}

// readDescription reads a file and returns the frontmatter description, if any.
func readDescription(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return parseDescriptionFromText(string(raw))
}

type manifest struct {
	Version int    `json:"version"`
	Items   []Item `json:"items"`
}

// Store keeps managed content under <workspace>/.gummi/agent-plugins. Imported
// items are symlinks into a managed repository until an edit makes a private
// copy, preserving any sibling references without changing their source.
type Store struct {
	workspace string
	dir       string
	itemsDir  string
	repos     map[string]string
	mu        sync.Mutex
}

func New(workspace string, repos []Repo) (*Store, error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace directory: %w", err)
	}
	gummiDir := filepath.Join(root, ".gummi")
	info, err := os.Lstat(gummiDir)
	if err != nil {
		return nil, fmt.Errorf("inspect .gummi directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New(".gummi is not a real directory")
	}
	s := &Store{
		workspace: root,
		dir:       filepath.Join(gummiDir, "agent-plugins"),
		itemsDir:  filepath.Join(gummiDir, "agent-plugins", "items"),
		repos:     make(map[string]string, len(repos)),
	}
	for _, repo := range repos {
		name := strings.TrimSpace(repo.Name)
		if name == "" || strings.ContainsAny(name, `/\`) {
			return nil, fmt.Errorf("invalid managed repository name %q", repo.Name)
		}
		path, err := filepath.Abs(repo.Root)
		if err != nil {
			return nil, fmt.Errorf("resolve repository %q: %w", name, err)
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("resolve repository %q directory: %w", name, err)
		}
		if !within(root, path) {
			return nil, fmt.Errorf("repository %q is outside the workspace", name)
		}
		s.repos[name] = filepath.Clean(path)
	}
	return s, nil
}

func (s *Store) Repos() []string {
	out := make([]string, 0, len(s.repos))
	for name := range s.repos {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (s *Store) List() ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readManifest()
	if err != nil {
		return nil, err
	}
	out := append([]Item(nil), m.Items...)
	for i := range out {
		out[i].Repos = append([]string(nil), out[i].Repos...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (s *Store) Get(id string) (Detail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readManifest()
	if err != nil {
		return Detail{}, err
	}
	for _, item := range m.Items {
		if item.ID != id {
			continue
		}
		root, err := s.itemRoot(item.ID)
		if err != nil {
			return Detail{}, err
		}
		entry := filepath.Join(root, item.Entry)
		info, err := os.Lstat(entry)
		if err != nil {
			return Detail{}, fmt.Errorf("inspect %s content: %w", item.Name, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return Detail{}, fmt.Errorf("%s content is not a regular file", item.Name)
		}
		content, err := os.ReadFile(entry)
		if err != nil {
			return Detail{}, fmt.Errorf("read %s content: %w", item.Name, err)
		}
		return Detail{Item: item, Content: string(content)}, nil
	}
	return Detail{}, ErrNotFound
}

func (s *Store) Create(kind, name, content string, global bool, repos []string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.create(kind, name, content, global, repos)
}

func (s *Store) create(kind, name, content string, global bool, repos []string) (Item, error) {
	name = strings.TrimSpace(name)
	if err := validateName(name); err != nil {
		return Item{}, err
	}
	if err := validateKind(kind); err != nil {
		return Item{}, err
	}
	if len(content) > 1<<20 {
		return Item{}, fmt.Errorf("%w: markdown content exceeds 1 MiB", ErrInvalid)
	}
	repos, err := s.validateScope(global, repos)
	if err != nil {
		return Item{}, err
	}
	m, err := s.readManifest()
	if err != nil {
		return Item{}, err
	}
	id := uniqueID(kind+"-"+slug(name), m.Items)
	entry := entryName(kind, name, id)
	if err := s.ensureDirs(); err != nil {
		return Item{}, err
	}
	itemDir := s.itemPath(id)
	if err := os.Mkdir(itemDir, 0o700); err != nil {
		return Item{}, fmt.Errorf("create item directory: %w", err)
	}
	if err := atomicfile.Write(filepath.Join(itemDir, entry), []byte(content), 0o600); err != nil {
		_ = os.RemoveAll(itemDir)
		return Item{}, fmt.Errorf("write item: %w", err)
	}
	item := Item{ID: id, Kind: kind, Name: name, Entry: entry, Global: global, Repos: repos, Description: parseDescriptionFromText(content)}
	m.Items = append(m.Items, item)
	if err := s.writeManifest(m); err != nil {
		_ = os.RemoveAll(itemDir)
		return Item{}, err
	}
	return item, nil
}

func (s *Store) Update(id, name, content string, global bool, repos []string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = strings.TrimSpace(name)
	if err := validateName(name); err != nil {
		return Item{}, err
	}
	if len(content) > 1<<20 {
		return Item{}, fmt.Errorf("%w: markdown content exceeds 1 MiB", ErrInvalid)
	}
	repos, err := s.validateScope(global, repos)
	if err != nil {
		return Item{}, err
	}
	m, err := s.readManifest()
	if err != nil {
		return Item{}, err
	}
	for i, item := range m.Items {
		if item.ID != id {
			continue
		}
		if err := s.makePrivate(item); err != nil {
			return Item{}, err
		}
		if err := atomicfile.Write(filepath.Join(s.itemPath(id), item.Entry), []byte(content), 0o600); err != nil {
			return Item{}, fmt.Errorf("write item: %w", err)
		}
		item.Name, item.Global, item.Repos, item.Linked = name, global, repos, false
		item.Description = parseDescriptionFromText(content)
		m.Items[i] = item
		if err := s.writeManifest(m); err != nil {
			return Item{}, err
		}
		return item, nil
	}
	return Item{}, ErrNotFound
}

func (s *Store) Import(kind, path, repo string, global bool, repos []string) (Item, error) {
	items, err := s.ImportMany([]Candidate{{Kind: kind, Path: path, Repo: repo}}, global, repos)
	if err != nil {
		return Item{}, err
	}
	return items[0], nil
}

func (s *Store) ImportMany(sources []Candidate, global bool, repos []string) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(sources) == 0 {
		return nil, fmt.Errorf("%w: select at least one item to import", ErrInvalid)
	}
	targetRepos, err := s.validateScope(global, repos)
	if err != nil {
		return nil, err
	}
	m, err := s.readManifest()
	if err != nil {
		return nil, err
	}
	if err := s.ensureDirs(); err != nil {
		return nil, err
	}
	created := make([]string, 0, len(sources))
	items := make([]Item, 0, len(sources))
	rollback := func() {
		for _, p := range created {
			_ = os.RemoveAll(p)
		}
	}
	for _, source := range sources {
		resolved, entry, name, sourcePath, err := s.resolveSource(source)
		if err != nil {
			rollback()
			return nil, err
		}
		for _, old := range append(m.Items, items...) {
			if old.Kind == source.Kind && old.SourcePath == sourcePath {
				rollback()
				return nil, fmt.Errorf("%w: %s", ErrConflict, sourcePath)
			}
		}
		if err := validateName(name); err != nil {
			rollback()
			return nil, fmt.Errorf("imported item name: %w", err)
		}
		id := uniqueID(source.Kind+"-"+slug(name), append(m.Items, items...))
		dst := s.itemPath(id)
		linked := source.Kind == KindSkill
		if linked {
			if err := os.Symlink(resolved, dst); err != nil {
				rollback()
				return nil, fmt.Errorf("link %s into the workspace: %w", sourcePath, err)
			}
			created = append(created, dst)
		} else {
			if err := os.Mkdir(dst, 0o700); err != nil {
				rollback()
				return nil, fmt.Errorf("create imported agent directory: %w", err)
			}
			created = append(created, dst)
			content, err := os.ReadFile(filepath.Join(resolved, entry))
			if err != nil {
				rollback()
				return nil, fmt.Errorf("read imported agent %s: %w", sourcePath, err)
			}
			if err := os.WriteFile(filepath.Join(dst, entry), content, 0o600); err != nil {
				rollback()
				return nil, fmt.Errorf("write imported agent %s: %w", sourcePath, err)
			}
		}
		item := Item{
			ID: id, Kind: source.Kind, Name: name, Entry: entry,
			SourcePath: sourcePath, Linked: linked, Global: global,
			Repos: append([]string(nil), targetRepos...),
		}
		// populate description from the source/copy's entry file
		if linked {
			item.Description = readDescription(filepath.Join(resolved, entry))
		} else {
			item.Description = readDescription(filepath.Join(dst, entry))
		}
		items = append(items, item)
	}
	m.Items = append(m.Items, items...)
	if err := s.writeManifest(m); err != nil {
		rollback()
		return nil, err
	}
	return items, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readManifest()
	if err != nil {
		return err
	}
	for i, item := range m.Items {
		if item.ID != id {
			continue
		}
		if err := s.verifyItemDirectories(); err != nil {
			return err
		}
		if err := os.RemoveAll(s.itemPath(item.ID)); err != nil {
			return fmt.Errorf("remove item: %w", err)
		}
		m.Items = append(m.Items[:i], m.Items[i+1:]...)
		return s.writeManifest(m)
	}
	return ErrNotFound
}

func (s *Store) Discover() ([]Candidate, error) {
	var found []Candidate
	repoNames := s.Repos()
	for _, repoName := range repoNames {
		root := s.repos[repoName]
		cands, err := s.scanRoot(root, repoName)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", repoName, err)
		}
		found = append(found, cands...)
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].Repo != found[j].Repo {
			return found[i].Repo < found[j].Repo
		}
		if found[i].Kind != found[j].Kind {
			return found[i].Kind < found[j].Kind
		}
		return found[i].Path < found[j].Path
	})
	return found, nil
}

// scanRoot walks known candidate locations under a repository root and
// returns discovered candidates using the same matching rules as Discover.
func (s *Store) scanRoot(root, repoName string) ([]Candidate, error) {
	var found []Candidate
	seen := make(map[string]bool)
	locations := []struct{ kind, path string }{
		{KindSkill, filepath.Join(root, ".claude", "skills")},
		{KindSkill, filepath.Join(root, ".agents", "skills")},
		{KindSkill, filepath.Join(root, ".github", "skills")},
		{KindAgent, filepath.Join(root, ".claude", "agents")},
		{KindAgent, filepath.Join(root, ".agents", "agents")},
		{KindAgent, filepath.Join(root, ".github", "agents")},
		{"any", filepath.Join(root, "plugins")},
	}
	for _, location := range locations {
		resolved, err := filepath.EvalSymlinks(location.path)
		if err == nil && !within(root, resolved) {
			continue
		}
		if err := walkCandidates(location.path, func(path string, d fs.DirEntry) {
			if d.IsDir() {
				return
			}
			name := d.Name()
			kind := location.kind
			switch {
			case strings.EqualFold(name, "SKILL.md"):
				kind = KindSkill
			case strings.HasSuffix(strings.ToLower(name), ".agent.md"), strings.EqualFold(name, "AGENTS.md"):
				kind = KindAgent
			default:
				return
			}
			if location.kind != "any" && kind != location.kind {
				return
			}
			rel, err := filepath.Rel(root, path)
			if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return
			}
			key := kind + ":" + filepath.Clean(path)
			if seen[key] {
				return
			}
			seen[key] = true
			base := filepath.Base(path)
			switch {
			case kind == KindSkill:
				base = filepath.Base(filepath.Dir(path))
			case strings.EqualFold(base, "AGENTS.md"):
				base = filepath.Base(filepath.Dir(path))
			default:
				base = strings.TrimSuffix(base, filepath.Ext(base))
				base = strings.TrimSuffix(base, ".agent")
			}
			desc := readDescription(path)
			found = append(found, Candidate{Kind: kind, Name: base, Repo: repoName, Path: filepath.ToSlash(rel), Description: desc})
		}); err != nil {
			return nil, fmt.Errorf("scan %s: %w", repoName, err)
		}
	}
	// also consider AGENTS.md at repo root
	agentsFile := filepath.Join(root, "AGENTS.md")
	if info, err := os.Lstat(agentsFile); err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		rel, err := filepath.Rel(root, agentsFile)
		if err == nil {
			found = append(found, Candidate{Kind: KindAgent, Name: filepath.Base(root), Repo: repoName, Path: filepath.ToSlash(rel), Description: readDescription(agentsFile)})
		}
	}
	return found, nil
}

// DiscoverAt scans the provided path (file or directory) within the given
// repository (or workspace if repo is empty) and returns candidates found
// under that root.
func (s *Store) DiscoverAt(path, repo string) ([]Candidate, error) {
	base := s.workspace
	if repo != "" {
		var ok bool
		base, ok = s.repos[repo]
		if !ok {
			return nil, fmt.Errorf("%w: unknown repository %q", ErrInvalid, repo)
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, filepath.FromSlash(path))
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: discover path does not exist", ErrInvalid)
		}
		return nil, fmt.Errorf("resolve discover path: %w", err)
	}
	if !within(base, path) {
		return nil, fmt.Errorf("%w: discover path must stay within the selected repository or workspace", ErrInvalid)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect discover path: %w", err)
	}
	var found []Candidate
	if info.IsDir() {
		err := filepath.WalkDir(path, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() {
				// skip common vendor/build folders
				switch d.Name() {
				case ".git", ".gummi", "node_modules", "vendor", "target", "dist", ".venv":
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			var kind string
			switch {
			case strings.EqualFold(name, "SKILL.md"):
				kind = KindSkill
			case strings.HasSuffix(strings.ToLower(name), ".agent.md"), strings.EqualFold(name, "AGENTS.md"):
				kind = KindAgent
			default:
				return nil
			}
			rel, err := filepath.Rel(base, p)
			if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil
			}
			nameBase := filepath.Base(p)
			switch {
			case kind == KindSkill:
				nameBase = filepath.Base(filepath.Dir(p))
			case strings.EqualFold(nameBase, "AGENTS.md"):
				nameBase = filepath.Base(filepath.Dir(p))
			default:
				nameBase = strings.TrimSuffix(nameBase, filepath.Ext(nameBase))
				nameBase = strings.TrimSuffix(nameBase, ".agent")
			}
			found = append(found, Candidate{Kind: kind, Name: nameBase, Repo: repoOrDefault(repo), Path: filepath.ToSlash(rel), Description: readDescription(p)})
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		name := filepath.Base(path)
		var kind string
		switch {
		case strings.EqualFold(name, "SKILL.md"):
			kind = KindSkill
		case strings.HasSuffix(strings.ToLower(name), ".agent.md"), strings.EqualFold(name, "AGENTS.md"):
			kind = KindAgent
		default:
			return nil, nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, nil
		}
		nameBase := filepath.Base(path)
		switch {
		case kind == KindSkill:
			nameBase = filepath.Base(filepath.Dir(path))
		case strings.EqualFold(nameBase, "AGENTS.md"):
			nameBase = filepath.Base(filepath.Dir(path))
		default:
			nameBase = strings.TrimSuffix(nameBase, filepath.Ext(nameBase))
			nameBase = strings.TrimSuffix(nameBase, ".agent")
		}
		found = append(found, Candidate{Kind: kind, Name: nameBase, Repo: repoOrDefault(repo), Path: filepath.ToSlash(rel), Description: readDescription(path)})
	}
	return found, nil
}

func repoOrDefault(r string) string {
	if r == "" {
		return "default"
	}
	return r
}
func (s *Store) Export(id string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readManifest()
	if err != nil {
		return nil, "", err
	}
	for _, item := range m.Items {
		if item.ID != id {
			continue
		}
		root, err := s.itemRoot(item.ID)
		if err != nil {
			return nil, "", fmt.Errorf("resolve item contents: %w", err)
		}
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("cannot export symlink inside item: %s", rel)
			}
			name := filepath.ToSlash(filepath.Join(item.ID, rel))
			if d.IsDir() {
				_, err = zw.Create(name + "/")
				return err
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("cannot export non-regular item file: %s", rel)
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			w, err := zw.Create(name)
			if err != nil {
				return err
			}
			_, err = io.Copy(w, f)
			return err
		})
		if err != nil {
			_ = zw.Close()
			return nil, "", fmt.Errorf("export item: %w", err)
		}
		if err := zw.Close(); err != nil {
			return nil, "", fmt.Errorf("finish export: %w", err)
		}
		return buf.Bytes(), item.ID + ".zip", nil
	}
	return nil, "", ErrNotFound
}

// SkillDirs returns managed skill directories enabled for repo. "default" is
// the scope id used by a single-repository workspace.
func SkillDirs(workspace, repo string) ([]string, error) {
	if _, err := os.Lstat(filepath.Join(workspace, ".gummi")); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("inspect workspace .gummi directory: %w", err)
	}
	s, err := New(workspace, []Repo{{Name: "default", Root: workspace}})
	if err != nil {
		return nil, err
	}
	m, err := s.readManifest()
	if err != nil {
		return nil, err
	}
	if repo == "" {
		repo = "default"
	}
	var out []string
	for _, item := range m.Items {
		if item.Kind != KindSkill || (!item.Global && !contains(item.Repos, repo)) {
			continue
		}
		root, err := s.itemRoot(item.ID)
		if err != nil {
			return nil, fmt.Errorf("managed skill %q is unavailable: %w", item.Name, err)
		}
		info, err := os.Lstat(filepath.Join(root, "SKILL.md"))
		if err != nil {
			return nil, fmt.Errorf("managed skill %q is unavailable: %w", item.Name, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("managed skill %q has no regular SKILL.md", item.Name)
		}
		out = append(out, s.itemPath(item.ID))
	}
	return out, nil
}

func (s *Store) resolveSource(source Candidate) (root, entry, name, sourcePath string, err error) {
	if err = validateKind(source.Kind); err != nil {
		return
	}
	base := s.workspace
	if source.Repo != "" {
		var ok bool
		base, ok = s.repos[source.Repo]
		if !ok {
			err = fmt.Errorf("%w: unknown source repository %q", ErrInvalid, source.Repo)
			return
		}
	}
	path := source.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, filepath.FromSlash(path))
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = fmt.Errorf("%w: import path does not exist", ErrInvalid)
		} else {
			err = fmt.Errorf("resolve import path: %w", err)
		}
		return
	}
	if !within(base, path) {
		err = fmt.Errorf("%w: import path must stay within the workspace or selected repository", ErrInvalid)
		return
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			err = fmt.Errorf("%w: import path does not exist", ErrInvalid)
		} else {
			err = fmt.Errorf("inspect import path: %w", statErr)
		}
		return
	}
	switch source.Kind {
	case KindSkill:
		if info.IsDir() {
			root = path
			entry = "SKILL.md"
		} else if strings.EqualFold(filepath.Base(path), "SKILL.md") {
			root = filepath.Dir(path)
			entry = filepath.Base(path)
		} else {
			err = fmt.Errorf("%w: a skill import must be its directory or a SKILL.md file", ErrInvalid)
			return
		}
		if !regularFile(filepath.Join(root, entry)) {
			err = fmt.Errorf("%w: skill directory does not contain a regular SKILL.md", ErrInvalid)
			return
		}
		name = filepath.Base(root)
	case KindAgent:
		if info.IsDir() {
			err = fmt.Errorf("%w: an agent import must be an AGENTS.md or *.agent.md file", ErrInvalid)
			return
		}
		if !info.Mode().IsRegular() {
			err = fmt.Errorf("%w: an agent import must be a regular Markdown file", ErrInvalid)
			return
		}
		name = filepath.Base(path)
		if strings.EqualFold(name, "AGENTS.md") {
			name = filepath.Base(filepath.Dir(path))
		} else if strings.HasSuffix(strings.ToLower(name), ".agent.md") {
			name = name[:len(name)-len(".agent.md")]
		} else {
			err = fmt.Errorf("%w: an agent import must be an AGENTS.md or *.agent.md file", ErrInvalid)
			return
		}
		root, entry = filepath.Dir(path), filepath.Base(path)
	}
	if !within(base, root) {
		err = fmt.Errorf("%w: import directory must stay within the workspace or selected repository", ErrInvalid)
		return
	}
	if source.Kind == KindSkill {
		if err = validateSourceTree(root); err != nil {
			if !errors.Is(err, ErrInvalid) {
				return
			}
			return
		}
	}
	rel, relErr := filepath.Rel(s.workspace, filepath.Join(root, entry))
	if relErr != nil {
		err = relErr
		return
	}
	sourcePath = filepath.ToSlash(rel)
	return
}

func (s *Store) validateScope(global bool, repos []string) ([]string, error) {
	if !global && len(repos) == 0 {
		return nil, fmt.Errorf("%w: select at least one repository or enable globally", ErrInvalid)
	}
	seen := make(map[string]bool)
	var out []string
	for _, repo := range repos {
		if _, ok := s.repos[repo]; !ok {
			return nil, fmt.Errorf("%w: unknown managed repository %q", ErrInvalid, repo)
		}
		if !seen[repo] {
			seen[repo] = true
			out = append(out, repo)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *Store) makePrivate(item Item) error {
	root := s.itemPath(item.ID)
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect item storage: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	source, err := s.itemRoot(item.ID)
	if err != nil {
		return fmt.Errorf("resolve linked item before editing: %w", err)
	}
	tmp, err := os.MkdirTemp(s.itemsDir, ".copy-*")
	if err != nil {
		return fmt.Errorf("prepare private item copy: %w", err)
	}
	defer os.RemoveAll(tmp)
	if err := copyTree(source, tmp); err != nil {
		return fmt.Errorf("copy linked item before editing: %w", err)
	}
	backupFile, err := os.CreateTemp(s.itemsDir, ".link-*")
	if err != nil {
		return fmt.Errorf("prepare link replacement: %w", err)
	}
	backup := backupFile.Name()
	if err := backupFile.Close(); err != nil {
		_ = os.Remove(backup)
		return fmt.Errorf("prepare link replacement: %w", err)
	}
	if err := os.Remove(backup); err != nil {
		return fmt.Errorf("prepare link replacement: %w", err)
	}
	if err := os.Rename(root, backup); err != nil {
		return fmt.Errorf("detach linked item: %w", err)
	}
	if err := os.Rename(tmp, root); err != nil {
		_ = os.Rename(backup, root)
		return fmt.Errorf("install private item copy: %w", err)
	}
	if err := os.Remove(backup); err != nil {
		return fmt.Errorf("remove imported link after copying: %w", err)
	}
	return nil
}

func (s *Store) ensureDirs() error {
	if err := mkdirReal(s.dir); err != nil {
		return err
	}
	return mkdirReal(s.itemsDir)
}

func mkdirReal(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(path), err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", filepath.Base(path), err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a real directory", filepath.Base(path))
	}
	return nil
}

func (s *Store) readManifest() (manifest, error) {
	if err := verifyRealDirectory(filepath.Join(s.workspace, ".gummi")); err != nil {
		return manifest{}, fmt.Errorf("inspect workspace .gummi directory: %w", err)
	}
	if err := verifyRealDirectory(s.dir); errors.Is(err, fs.ErrNotExist) {
		return manifest{Version: 1}, nil
	} else if err != nil {
		return manifest{}, fmt.Errorf("inspect agent plugins directory: %w", err)
	}
	path := filepath.Join(s.dir, "index.json")
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return manifest{}, errors.New("agent plugins index is not a regular file")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return manifest{}, fmt.Errorf("inspect agent plugins index: %w", err)
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return manifest{Version: 1}, nil
	}
	if err != nil {
		return manifest{}, fmt.Errorf("read agent plugins index: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return manifest{}, fmt.Errorf("decode agent plugins index: %w", err)
	}
	if m.Version != 1 {
		return manifest{}, fmt.Errorf("unsupported agent plugins index version %d", m.Version)
	}
	seen := make(map[string]bool)
	for _, item := range m.Items {
		if !validID(item.ID) || validateKind(item.Kind) != nil || seen[item.ID] ||
			(filepath.Base(item.Entry) != item.Entry) || item.Entry == "." || item.Entry == ".." {
			return manifest{}, errors.New("agent plugins index contains an invalid item")
		}
		seen[item.ID] = true
	}
	return m, nil
}

func (s *Store) writeManifest(m manifest) error {
	if err := s.ensureDirs(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent plugins index: %w", err)
	}
	if err := atomicfile.Write(filepath.Join(s.dir, "index.json"), raw, 0o600); err != nil {
		return fmt.Errorf("write agent plugins index: %w", err)
	}
	return nil
}

func (s *Store) itemPath(id string) string { return filepath.Join(s.itemsDir, id) }

func (s *Store) itemRoot(id string) (string, error) {
	if err := s.verifyItemDirectories(); err != nil {
		return "", err
	}
	path := s.itemPath(id)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	root := path
	if info.Mode()&os.ModeSymlink != 0 {
		root, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		if !within(s.workspace, root) {
			return "", errors.New("item link points outside the workspace")
		}
		info, err = os.Stat(root)
		if err != nil {
			return "", err
		}
	}
	if !info.IsDir() {
		return "", errors.New("item storage is not a directory")
	}
	return root, nil
}

func (s *Store) verifyItemDirectories() error {
	for _, path := range []string{
		filepath.Join(s.workspace, ".gummi"),
		s.dir,
		s.itemsDir,
	} {
		if err := verifyRealDirectory(path); err != nil {
			return fmt.Errorf("inspect agent plugin storage: %w", err)
		}
	}
	return nil
}

func verifyRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a real directory", filepath.Base(path))
	}
	return nil
}

func walkCandidates(root string, add func(string, fs.DirEntry)) error {
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() && path != root {
			switch d.Name() {
			case ".git", ".gummi", "node_modules", "vendor", "target", "dist", ".venv":
				return filepath.SkipDir
			}
		}
		add(path, d)
		return nil
	})
}

func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("supporting resource %s is a symlink; refusing to copy outside the managed item", rel)
		}
		dst := filepath.Join(target, rel)
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			return os.MkdirAll(dst, 0o700)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("supporting resource %s is not a regular file", rel)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o600)
	})
}

func entryName(kind, name, id string) string {
	if kind == KindSkill {
		return "SKILL.md"
	}
	return slug(name) + ".agent.md"
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,79}$`)

func validateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%w: name must start with a letter or number and contain only letters, numbers, spaces, dots, underscores, or hyphens (max 80 characters)", ErrInvalid)
	}
	return nil
}

func validateKind(kind string) error {
	if kind != KindAgent && kind != KindSkill {
		return fmt.Errorf("%w: kind must be %q or %q", ErrInvalid, KindAgent, KindSkill)
	}
	return nil
}

func slug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	dash := false
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func uniqueID(base string, items []Item) string {
	used := make(map[string]bool, len(items))
	for _, item := range items {
		used[item.ID] = true
	}
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		id := fmt.Sprintf("%s-%d", base, i)
		if !used[id] {
			return id
		}
	}
}

func validID(id string) bool {
	return idPattern.MatchString(id)
}

var idPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
