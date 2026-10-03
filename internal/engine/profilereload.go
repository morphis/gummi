package engine

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/morphis/gummi/internal/config"
)

// Profiles are read from .gummi/profiles.yaml once, when the process
// starts, and every session resolves its backend and model from them. A
// board is a long-lived process, and the file is exactly what a person
// edits while one runs — to fix a model id a backend refuses, say. The
// board used to keep the startup copy for its whole life, so an edit
// reached `gummi doctor` (which reads the file) and not the engine: the
// doctor said Ready while every scribe pass went on failing on the old,
// unservable model half an hour after it was fixed.
//
// So the engine re-reads the file when it changes, at the moment a new
// session resolves its role. A session already running keeps the model it
// was started with — a turn does not change backends under itself — and
// an edit that does not validate, or that names a backend this process
// never started, is refused: the engine keeps the profiles it has and
// says why, once per bad edit, rather than half-applying a broken file.

// profileStamp identifies one version of the profiles file on disk.
type profileStamp struct {
	exists  bool
	modTime time.Time
	size    int64
}

func statProfiles(path string) profileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return profileStamp{}
	}
	return profileStamp{exists: true, modTime: fi.ModTime(), size: fi.Size()}
}

// profilesPath is the file this engine follows, "" when it has no
// workspace to follow one in (tests that hand the engine profiles
// directly, and nothing else).
func (e *Engine) profilesPath() string {
	if e.cfg.Workspace.Root == "" {
		return ""
	}
	return e.cfg.Workspace.ProfilesFile()
}

// initProfiles records the profiles the engine was constructed with and
// the file version they correspond to. Called from New; an Engine built
// as a bare literal (a few unit tests) gets the same on first use.
func (e *Engine) initProfiles() {
	e.profMu.Lock()
	defer e.profMu.Unlock()
	e.initProfilesLocked()
}

func (e *Engine) initProfilesLocked() {
	if e.profInit {
		return
	}
	e.profInit = true
	e.profiles = e.cfg.Profiles
	if p := e.profilesPath(); p != "" {
		e.profStamp = statProfiles(p)
	}
}

// currentProfiles is the profiles a NEW session resolves against: the
// file as it now reads, when it changed since it was last read and the
// change validates, and the last good profiles otherwise.
func (e *Engine) currentProfiles() config.Profiles {
	e.profMu.Lock()
	defer e.profMu.Unlock()
	e.initProfilesLocked()
	path := e.profilesPath()
	if path == "" {
		return e.profiles
	}
	st := statProfiles(path)
	if st == e.profStamp {
		return e.profiles
	}
	e.profStamp = st
	next, err := config.LoadProfiles(path)
	if err == nil {
		err = e.startedBackendsCover(next)
	}
	if err != nil {
		e.profErr = err.Error()
		if e.envWarn != nil {
			e.envWarn("profiles.yaml changed but was not applied: " + err.Error() + " — the board keeps the profiles it had")
		}
		return e.profiles
	}
	e.profiles, e.profErr = next, ""
	e.profReloads++
	return e.profiles
}

// startedBackendsCover refuses a profiles edit that routes a role to a
// backend this process never started. Backends are started once, at
// launch, from the profiles as they read then; a role pointed at another
// one would silently fall back to the default backend (agentFor), which
// is a model and a backend that disagree — the failure resolveConsultRole's
// comment describes. Saying so and keeping the old profiles is honest;
// the fix is a restart.
func (e *Engine) startedBackendsCover(p config.Profiles) error {
	var missing []string
	seen := map[string]bool{}
	for _, prof := range p.Profiles {
		for _, rc := range prof {
			if rc.Backend == "" || seen[rc.Backend] {
				continue
			}
			seen[rc.Backend] = true
			if _, ok := e.cfg.Agents[rc.Backend]; !ok {
				missing = append(missing, rc.Backend)
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("it names backend %v, which this board did not start — restart gummi to use it", missing)
}

// ProfilesState reports how the engine's profiles relate to the file:
// Reloads counts the edits it has picked up since it started, and Refused
// is why the latest edit was not applied ("" when it was, or when there
// was none). A doctor served by a running board reads this, because the
// file saying one thing and the running engine another is exactly what a
// readiness check exists to catch.
type ProfilesState struct {
	Reloads int
	Refused string
}

// ProfilesState reads the file's current version first, so the answer
// is about the file as it stands rather than as the last session found it.
func (e *Engine) ProfilesState() ProfilesState {
	_ = e.currentProfiles()
	e.profMu.Lock()
	defer e.profMu.Unlock()
	return ProfilesState{Reloads: e.profReloads, Refused: e.profErr}
}
