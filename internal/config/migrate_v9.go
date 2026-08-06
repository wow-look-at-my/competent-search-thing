package config

import (
	"encoding/json"
	"fmt"
)

// The v9 migration step: the three AI provider sections (openai,
// anthropic, custom, chosen with preview.aiProvider) collapse into ONE
// user-described endpoint, preview.ai. The app ships no provider list
// and no default endpoint any more -- there is nowhere for a query to
// go until the user names a server -- and the wire shape is the
// universal OpenAI-compatible chat-completions API rather than the
// OpenAI Responses / Anthropic Messages pair.
//
// This step carries the SELECTED provider's settings across (a user
// who configured OpenAI keeps answering with OpenAI) and says so, per
// the no-silent-scope-change rule the other steps follow. It reads
// the RAW bytes: the old keys no longer exist on the struct (the v6
// and v7 steps' precedent).

// The endpoints the two retired built-in providers used, filled in
// for a config that never had to name one. Both are the API BASE
// including the version segment -- what the new baseUrl means.
const (
	openAICompatBase    = "https://api.openai.com/v1"
	anthropicCompatBase = "https://api.anthropic.com/v1"
)

// aiV9Section is one retired provider section as it appeared on disk.
type aiV9Section struct {
	APIKey          string `json:"apiKey"`
	BaseURL         string `json:"baseUrl"`
	Model           string `json:"model"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
}

// aiV9Raw is the minimal raw-document shape the v9 step reads.
type aiV9Raw struct {
	Preview struct {
		AIProvider string       `json:"aiProvider"`
		OpenAI     *aiV9Section `json:"openai"`
		Anthropic  *aiV9Section `json:"anthropic"`
		Custom     *aiV9Section `json:"custom"`
		AI         *aiV9Section `json:"ai"`
	} `json:"preview"`
}

// migrateAIProvider is the v9 step. A document carrying the new
// preview.ai section keeps it untouched (a hand-edited config that is
// already there); otherwise the selected old section is carried over
// and announced. A config that never configured any AI provider
// migrates nothing and says nothing -- there is nothing to carry.
func (c *Config) migrateAIProvider(raw []byte) {
	var doc aiV9Raw
	if err := json.Unmarshal(raw, &doc); err != nil {
		return // unreadable raw bytes: the parsed config still stands
	}
	p := doc.Preview
	if p.AI != nil {
		return // already on the new shape
	}
	var (
		from    string
		section *aiV9Section
		base    string
	)
	switch p.AIProvider {
	case "anthropic":
		from, section, base = "anthropic", p.Anthropic, anthropicCompatBase
	case "custom":
		from, section, base = "custom", p.Custom, ""
	default: // "openai" and the pre-selector empty value
		from, section, base = "openai", p.OpenAI, openAICompatBase
	}
	if section == nil {
		// The selected provider was never configured. Announce the
		// collapse only if SOME provider section existed.
		if p.OpenAI != nil || p.Anthropic != nil || p.Custom != nil {
			c.MigrationNotes = append(c.MigrationNotes,
				"the preview AI providers collapsed into one endpoint you configure yourself (preview.ai: baseUrl, model, apiKey); "+
					"the old preview.openai/anthropic/custom sections are gone and nothing is sent anywhere until preview.ai.baseUrl names a server")
		}
		return
	}
	if section.BaseURL != "" {
		base = section.BaseURL
	}
	c.Preview.AI = PreviewAIConfig{
		APIKey:          section.APIKey,
		BaseURL:         base,
		Model:           section.Model,
		MaxOutputTokens: section.MaxOutputTokens,
	}
	c.MigrationNotes = append(c.MigrationNotes, fmt.Sprintf(
		"the preview AI providers collapsed into one endpoint you configure yourself: your %s settings moved to preview.ai (baseUrl %q, model %q)",
		from, base, section.Model))
	// The wire shape changed with the collapse, so a hand-typed base
	// URL may now point at the wrong path. Say so instead of letting
	// the first Ctrl+I fail mysteriously.
	if from == "custom" || section.BaseURL != "" {
		c.MigrationNotes = append(c.MigrationNotes,
			"check preview.ai.baseUrl: requests now go to <baseUrl>/chat/completions (the OpenAI-compatible shape every server implements), "+
				"so the value must be the API base INCLUDING its version segment, e.g. http://localhost:11434/v1")
	}
	// Keys used to be readable from OPENAI_API_KEY / ANTHROPIC_API_KEY;
	// there is one endpoint now, so there is one variable.
	if section.APIKey == "" && from != "custom" {
		c.MigrationNotes = append(c.MigrationNotes,
			"the AI API key now comes from preview.ai.apiKey or the COMPETENT_SEARCH_AI_API_KEY environment variable "+
				"(the per-provider OPENAI_API_KEY / ANTHROPIC_API_KEY fallbacks are gone)")
	}
}
