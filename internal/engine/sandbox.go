package engine

import (
	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/sandbox"
)

// resolveSandbox computes the effective confinement mode and coverage gaps
// for a feature's run: precedence profile → workspace → built-in warn,
// using the live adapters for the backends the feature's profile names. It
// is the engine's single call site for the sandbox.Resolve resolver, so a
// coverage decision the engine makes can never diverge from the one doctor
// reports.
func (e *Engine) resolveSandbox(f domain.Feature) sandbox.Resolution {
	profile := e.resolvedProfile(f.Profile)
	return sandbox.Resolve(
		sandbox.Mode(e.cfg.Sandbox),
		sandbox.Mode(e.cfg.Profiles.Sandboxes[f.Profile]),
		profile,
		e.capsForProfile(profile),
	)
}

// resolvedProfile returns the feature's profile (or the workspace default
// when the named one is absent) with every role's empty `backend:` expanded
// to the default adapter's concrete name, so the resolver can look each
// backend up in a capabilities map by name.
func (e *Engine) resolvedProfile(profileName string) config.Profile {
	prof, ok := e.cfg.Profiles.Profiles[profileName]
	if !ok {
		if def := e.cfg.Profiles.Default; def != "" {
			if d, has := e.cfg.Profiles.Profiles[def]; has {
				prof = d
			}
		}
	}
	defName := ""
	if a := e.defaultAgent(); a != nil {
		defName = a.Name()
	}
	resolved := make(config.Profile, len(prof))
	for role, rc := range prof {
		r := rc
		if r.Backend == "" {
			r.Backend = defName
		}
		resolved[role] = r
	}
	return resolved
}

// capsForProfile builds the capabilities map the resolver needs from the
// live adapters for the distinct backends a profile names. A backend with
// no registered adapter is given zero capabilities and so reads as a
// coverage gap — fail-closed.
func (e *Engine) capsForProfile(profile config.Profile) map[string]agent.Capabilities {
	caps := map[string]agent.Capabilities{}
	seen := map[string]struct{}{}
	for _, rc := range profile {
		if rc.Backend == "" {
			continue
		}
		if _, ok := seen[rc.Backend]; ok {
			continue
		}
		seen[rc.Backend] = struct{}{}
		if a := e.agentFor(rc.Backend); a != nil {
			caps[rc.Backend] = a.Capabilities()
		} else {
			caps[rc.Backend] = agent.Capabilities{}
		}
	}
	return caps
}
