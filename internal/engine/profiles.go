package engine

import (
	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
)

// resolveRole picks the role config and backend name for a feature's
// profile and role. It falls back to the engine's single-model config
// (M1/M2 behavior) when profiles are absent or don't cover the
// profile/role, so a repo without profiles.yaml still works. An empty
// backend means "use the engine's default backend" — agentFor resolves
// that.
func (e *Engine) resolveRole(profileName string, role agent.Role) (config.RoleConfig, string) {
	if rc, ok := e.lookupRole(profileName, role); ok {
		return rc, rc.Backend
	}
	return config.RoleConfig{Model: e.cfg.Model}, ""
}

// lookupRole reports whether profileName's profile (or the default one)
// declares role, and its config if so. It is resolveRole's first half,
// split out because "the role is declared" and "the role resolved to
// something" are different questions and only the caller knows which one
// it is asking. resolveRole conflates them deliberately — an undeclared
// role there means the single-model fallback, which is the right answer
// for a stage role that every profile is expected to cover. A role no
// profile is expected to declare at all needs to tell the two apart.
func (e *Engine) lookupRole(profileName string, role agent.Role) (config.RoleConfig, bool) {
	profiles := e.currentProfiles()
	prof, ok := profiles.Profiles[profileName]
	if !ok {
		if def := profiles.Default; def != "" {
			prof, ok = profiles.Profiles[def]
		}
	}
	if !ok {
		return config.RoleConfig{}, false
	}
	rc, ok := prof[string(role)]
	return rc, ok
}

// resolveConsultRole picks a consult session's model and backend as ONE
// decision, which is the whole point of it not being resolveRole.
//
// resolveRole's fallback returns the engine's single default model with
// an EMPTY backend, on the reasonable assumption that a profile covers
// every stage role and so the fallback is only ever reached by a repo
// with no profiles.yaml at all. RoleConsult breaks that assumption: no
// profile declares it (it did not exist when profiles.yaml was written,
// and requiring one would make every consult fail on existing
// workspaces), so the fallback is the NORMAL path here, not the edge —
// and it would hand back a model from one source and, via agentFor(""),
// a backend from another. A workspace whose default model is gpt-5 and
// whose default agent is claude would then get a claude session told to
// drive gpt-5, which that adapter refuses outright at session start.
// Model and backend have to travel together or they disagree.
//
// So: the consult role if a profile has bothered to declare one, else
// the architect's — the closest analogue, being the role that reasons
// about a card's work rather than editing it, and paired by
// construction. Failing both, nothing at all: an empty model lets the
// card's own profile-resolved backend pick whatever it normally would,
// which is always something that backend can actually drive. That is
// strictly better than naming a model chosen with no idea of who would
// run it.
func (e *Engine) resolveConsultRole(profileName string) (config.RoleConfig, string) {
	for _, role := range []agent.Role{agent.RoleConsult, agent.RoleArchitect} {
		if rc, ok := e.lookupRole(profileName, role); ok {
			return rc, rc.Backend
		}
	}
	return config.RoleConfig{}, ""
}

// agentFor returns the Agent for the given backend name. An empty name,
// or an unknown backend, resolves to the engine's default agent (the
// entry stored under the "" key in cfg.Agents). Returns nil when the
// engine has no agents at all — a construction-time misconfiguration the
// callers already guard against with their own nil checks.
func (e *Engine) agentFor(backend string) agent.Agent {
	if backend != "" {
		if a, ok := e.cfg.Agents[backend]; ok {
			return a
		}
	}
	return e.cfg.Agents[""]
}

// defaultAgent returns the engine's default backend, or nil when none is
// configured. It exists because a handful of engine-owned sessions
// (discovery, ingest, estimate) don't run under a profile role and use
// the default directly.
func (e *Engine) defaultAgent() agent.Agent { return e.agentFor("") }

// ProfileChoice is one profile entry in a profile picker: the name a
// user picks, and the backend/model the engine actually resolves that
// profile to for the picker's role — not the raw role config, since a
// profile that never declared the role at all resolves through a
// fallback, and the picker needs to show what will really run when it's
// picked, not what the yaml literally says under a role it doesn't name.
type ProfileChoice struct {
	Name    string
	Backend string
	Model   string
}

// CardProfiles lists every declared profile for a card-scoped profile
// picker, in config.Profiles.Names order (the declared default first, the
// rest sorted). It reuses Names rather than re-deriving that ordering
// here — a duplicate sort is a second place for the new-feature form's
// ordering rule and a card picker's to quietly disagree the next time
// one of them changes. Each entry's Backend/Model comes from resolveRole
// for the role a card at stage actually runs, not the profile map
// directly, for the reason ProfileChoice's own comment gives. An empty
// Backend coming back from resolveRole is reported empty, not papered
// over with a placeholder string — wording "use the engine's default" is
// a UI decision, not this package's to make.
//
// stage, not agent.Role, is deliberate: roleForStage stays unexported
// and package-engine-only, so callers (internal/ui) never need to know
// agent.Role exists. A stage with no agent action (e.g. domain.StageDone)
// leaves role at its zero value "", which resolveRole's own undeclared-
// role fallback already handles — the same fallback every other
// undeclared-role lookup gets, not a new one invented for this picker.
//
// Nil-safe: an engine with no profiles.yaml has an empty
// cfg.Profiles.Profiles; Names() then returns nil and so does this.
func (e *Engine) CardProfiles(stage domain.Stage) []ProfileChoice {
	names := e.currentProfiles().Names()
	if len(names) == 0 {
		return nil
	}
	role, _ := roleForStage(domain.Feature{Stage: stage})
	out := make([]ProfileChoice, 0, len(names))
	for _, name := range names {
		rc, backend := e.resolveRole(name, role)
		out = append(out, ProfileChoice{Name: name, Backend: backend, Model: rc.Model})
	}
	return out
}
