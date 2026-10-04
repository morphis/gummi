package webapi

import "github.com/morphis/gummi/internal/agentplugins"

type AgentPlugins struct {
	Items     []agentplugins.Item   `json:"items"`
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
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

type AgentPluginUpdateRequest struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type AgentPluginImportRequest struct {
	Sources []agentplugins.Candidate `json:"sources"`
}

type AgentPluginDiscover struct {
	Candidates []agentplugins.Candidate `json:"candidates"`
}
