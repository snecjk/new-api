package observability

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// UsageSnapshot is copied before billing. No pointer to BillingUsage or cost is retained.
type UsageSnapshot struct {
	CacheCreation5m, CacheCreation1h                                  int
	Present                                                           bool
	PromptTokens, CompletionTokens, TotalTokens, PromptCacheHitTokens int
	UsageSource, UsageSemantic                                        string
	InputTokens, OutputTokens                                         int
	InputTokenDetails                                                 dto.InputTokenDetails
	OutputTokenDetails                                                dto.OutputTokenDetails
}

func FromUsage(u *dto.Usage) UsageSnapshot {
	if u == nil {
		return UsageSnapshot{}
	}
	inputDetails := u.PromptTokensDetails
	if u.InputTokensDetails != nil && inputDetails == (dto.InputTokenDetails{}) {
		inputDetails = *u.InputTokensDetails
	}
	return UsageSnapshot{CacheCreation5m: u.ClaudeCacheCreation5mTokens, CacheCreation1h: u.ClaudeCacheCreation1hTokens, Present: true, PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
		TotalTokens: u.TotalTokens, PromptCacheHitTokens: u.PromptCacheHitTokens, UsageSource: u.UsageSource,
		UsageSemantic: u.UsageSemantic, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		InputTokenDetails: inputDetails, OutputTokenDetails: u.CompletionTokenDetails}
}

// Langfuse accepts a flat map of numeric counts. Categories are disjoint: cached
// and reasoning tokens must not be added on top of inclusive OpenAI totals.
func (s UsageSnapshot) ToLangfuseJSON() (string, error) {
	counts := map[string]int{}
	if s.Present {
		input, output := max(0, s.PromptTokens), max(0, s.CompletionTokens)
		if input == 0 {
			input = max(0, s.InputTokens)
		}
		if output == 0 {
			output = max(0, s.OutputTokens)
		}
		read := max(0, max(s.InputTokenDetails.CachedTokens, s.PromptCacheHitTokens))
		write := s.InputTokenDetails.CacheCreationTokensTotal()
		if s.UsageSemantic != "anthropic" {
			read = min(read, input)
			write = min(write, input-read)
			input -= read + write
		}
		reasoning := min(max(0, s.OutputTokenDetails.ReasoningTokens), output)
		if input > 0 {
			counts["input"] = input
		}
		if output-reasoning > 0 {
			counts["output"] = output - reasoning
		}
		if read > 0 {
			counts["input_cache_read"] = read
		}
		if write > 0 {
			counts["input_cache_write"] = write
		}
		if reasoning > 0 {
			counts["output_reasoning"] = reasoning
		}
	}
	b, err := common.Marshal(counts)
	return string(b), err
}
