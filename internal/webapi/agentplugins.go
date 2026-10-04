package webapi

import "github.com/morphis/gummi/internal/agentplugins"

type AgentPlugins struct {
	Items     []agentplugins.Item   `json:"items"`
	Repos     []string              `json:"repos"`
	Providers []AgentPluginProvider `json:"providers"`
}

type AgentPluginProvider struct {
	Name        string `json:"name"`
	SkillDirs   bool   `json:"skillDirs"`
	SkillDetail string `json:"skillDetail"`
}

type AgentPluginDetail struct {
	Item    agentplugins.Item `json:"item"`
	Content string            `json:"content"`
}

type AgentPluginCreateRequest struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	Content string   `json:"content"`
	Global  bool     `json:"global"`
	Repos   []string `json:"repos"`
}

type AgentPluginUpdateRequest struct {
	Name    string   `json:"name"`
	Content string   `json:"content"`
	Global  bool     `json:"global"`
	Repos   []string `json:"repos"`
}

type AgentPluginImportRequest struct {
	Sources []agentplugins.Candidate `json:"sources"`
	Global  bool                     `json:"global"`
	Repos   []string                 `json:"repos"`
}

type AgentPluginDiscover struct {
	Candidates []agentplugins.Candidate `json:"candidates"`
}
