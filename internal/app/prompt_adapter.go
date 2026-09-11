package app

import "strings"

// promptAdapter is the Provider-decided strategy for shaping the hidden
// (instructions) channel when the upstream model has no native system prompt or
// function calling.
//
// Phase 1 (P0-3): this replaces the previously dead prompt_guard path. The
// strategy only reframes the task (the model remains itself); it does not
// attempt to defeat any provider safety control. It deliberately writes to the
// instructions channel and never inserts a standalone assistant turn, which
// avoids the few-shot parrot failure mode.
type promptAdapter struct {
	name   string
	prefix string
}

func (p promptAdapter) Name() string { return p.name }

func (p promptAdapter) Apply(request PromptRunRequest) PromptRunRequest {
	if request.PromptAdapterApplied || strings.TrimSpace(p.prefix) == "" {
		return request
	}
	request.PromptAdapterApplied = true
	request.PromptAdapterName = p.name
	request.HiddenPrompt = joinPromptSections(p.prefix, request.HiddenPrompt)
	return request
}

func selectPromptAdapter(cfg AppConfig) promptAdapter {
	profile := normalizePromptProfile(cfg.Prompt.Profile, promptProfileCognitiveReframing)
	switch profile {
	case promptProfileNone:
		return promptAdapter{name: "native"}
	case promptProfileCustom:
		return promptAdapter{name: "custom", prefix: strings.TrimSpace(cfg.Prompt.CustomPrefix)}
	case promptProfileToolboxCapabilityExpand:
		return promptAdapter{name: "toolbox_capability_expansion", prefix: strings.TrimSpace(cfg.Prompt.ToolboxCapabilityExpansionPrefix)}
	default:
		return promptAdapter{name: "cognitive_reframing", prefix: strings.TrimSpace(cfg.Prompt.CognitiveReframingPrefix)}
	}
}

// applyPromptAdapterToRequest injects the configured strategy framing once.
func applyPromptAdapterToRequest(cfg AppConfig, request PromptRunRequest) PromptRunRequest {
	return selectPromptAdapter(cfg).Apply(request)
}
