package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/agentplugins"
	"github.com/morphis/gummi/internal/webapi"
)

var pluginProviders = []string{"claude", "codex", "copilot", "headless", "opencode", "pi"}

func (s *Server) agentPluginRoutes() {
	s.api("GET /api/plugins", s.handleAgentPlugins)
	s.api("POST /api/plugins/discover", s.handleAgentPluginDiscover)
	s.api("POST /api/plugins", s.handleAgentPluginCreate)
	s.api("POST /api/plugins/import", s.handleAgentPluginImport)
	s.api("GET /api/plugins/{id}", s.handleAgentPluginGet)
	s.api("PUT /api/plugins/{id}", s.handleAgentPluginUpdate)
	s.api("DELETE /api/plugins/{id}", s.handleAgentPluginDelete)
	s.api("GET /api/plugins/{id}/export", s.handleAgentPluginExport)
}

func (s *Server) handleAgentPlugins(w http.ResponseWriter, r *http.Request) {
	store, ok := s.pluginStore(w)
	if !ok {
		return
	}
	items, err := store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	providers := make([]webapi.AgentPluginProvider, 0, len(pluginProviders))
	for _, name := range pluginProviders {
		caps, found := agent.CapabilitiesFor(name)
		detail := "This backend cannot load workspace-forwarded skills."
		if found && caps.SkillDirs {
			detail = "Enabled skills are available to new sessions."
		}
		providers = append(providers, webapi.AgentPluginProvider{
			Name: name, SkillDirs: found && caps.SkillDirs, SkillDetail: detail,
		})
	}
	writeJSON(w, http.StatusOK, webapi.AgentPlugins{
		Items: items, Providers: providers,
	})
}

func (s *Server) handleAgentPluginDiscover(w http.ResponseWriter, r *http.Request) {
	store, ok := s.pluginStore(w)
	if !ok {
		return
	}
	// accept an optional JSON body that narrows discovery to a specific
	// repo/path and/or a single kind, so a scan started from the skills or
	// agents tab only returns candidates relevant to that tab.
	var req struct {
		Repo string `json:"repo"`
		Path string `json:"path"`
		Kind string `json:"kind"`
	}
	_ = readJSON(w, r, &req) // ignore error — empty body is fine
	if req.Path != "" {
		cands, err := store.DiscoverAt(req.Path, req.Repo)
		if err != nil {
			if errors.Is(err, agentplugins.ErrInvalid) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, webapi.AgentPluginDiscover{Candidates: filterCandidatesByKind(cands, req.Kind)})
		return
	}
	candidates, err := store.Discover()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, webapi.AgentPluginDiscover{Candidates: filterCandidatesByKind(candidates, req.Kind)})
}

// filterCandidatesByKind narrows candidates to the requested kind. An empty
// kind returns every candidate unchanged.
func filterCandidatesByKind(candidates []agentplugins.Candidate, kind string) []agentplugins.Candidate {
	if kind == "" {
		return candidates
	}
	out := make([]agentplugins.Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

func (s *Server) handleAgentPluginCreate(w http.ResponseWriter, r *http.Request) {
	store, ok := s.pluginStore(w)
	if !ok {
		return
	}
	var req webapi.AgentPluginCreateRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	item, err := store.Create(req.Kind, req.Name, req.Content)
	if err != nil {
		writeAgentPluginError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) handleAgentPluginImport(w http.ResponseWriter, r *http.Request) {
	store, ok := s.pluginStore(w)
	if !ok {
		return
	}
	var req webapi.AgentPluginImportRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	items, err := store.ImportMany(req.Sources)
	if err != nil {
		writeAgentPluginError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Items []agentplugins.Item `json:"items"`
	}{Items: items})
}

func (s *Server) handleAgentPluginGet(w http.ResponseWriter, r *http.Request) {
	store, ok := s.pluginStore(w)
	if !ok {
		return
	}
	detail, err := store.Get(r.PathValue("id"))
	if err != nil {
		writeAgentPluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, webapi.AgentPluginDetail{Item: detail.Item, Content: detail.Content})
}

func (s *Server) handleAgentPluginUpdate(w http.ResponseWriter, r *http.Request) {
	store, ok := s.pluginStore(w)
	if !ok {
		return
	}
	var req webapi.AgentPluginUpdateRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	item, err := store.Update(r.PathValue("id"), req.Name, req.Content)
	if err != nil {
		writeAgentPluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleAgentPluginDelete(w http.ResponseWriter, r *http.Request) {
	store, ok := s.pluginStore(w)
	if !ok {
		return
	}
	if err := store.Delete(r.PathValue("id")); err != nil {
		writeAgentPluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, webapi.OK{OK: true})
}

func (s *Server) handleAgentPluginExport(w http.ResponseWriter, r *http.Request) {
	store, ok := s.pluginStore(w)
	if !ok {
		return
	}
	content, name, err := store.Export(r.PathValue("id"))
	if err != nil {
		writeAgentPluginError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", strconv.Quote(name)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (s *Server) pluginStore(w http.ResponseWriter) (*agentplugins.Store, bool) {
	if s.opt.Plugins == nil {
		writeError(w, http.StatusServiceUnavailable, "agent plugin management is not configured for this workspace")
		return nil, false
	}
	return s.opt.Plugins, true
}

func writeAgentPluginError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agentplugins.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, agentplugins.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, agentplugins.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
