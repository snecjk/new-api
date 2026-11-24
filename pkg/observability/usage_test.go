package observability

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestUsageSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage *dto.Usage
		want  map[string]int
	}{
		{"missing", nil, map[string]int{}},
		{"openai inclusive cache", &dto.Usage{PromptTokens: 100, CompletionTokens: 50, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 20, CacheWriteTokens: 10}, CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 10}}, map[string]int{"input": 70, "input_cache_read": 20, "input_cache_write": 10, "output": 40, "output_reasoning": 10}},
		{"anthropic exclusive cache", &dto.Usage{PromptTokens: 100, CompletionTokens: 50, UsageSemantic: "anthropic", PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 20, CachedCreationTokens: 10}}, map[string]int{"input": 100, "input_cache_read": 20, "input_cache_write": 10, "output": 50}},
		{"gemini", &dto.Usage{PromptTokens: 100, CompletionTokens: 20, UsageSource: "upstream", UsageSemantic: "gemini"}, map[string]int{"input": 100, "output": 20}},
		{"responses aliases", &dto.Usage{InputTokens: 10, OutputTokens: 4, InputTokensDetails: &dto.InputTokenDetails{CachedTokens: 3}}, map[string]int{"input": 7, "input_cache_read": 3, "output": 4}},
		{"estimated", &dto.Usage{PromptTokens: 12, CompletionTokens: 3, UsageSource: "estimated"}, map[string]int{"input": 12, "output": 3}},
		{"zero unknown", &dto.Usage{}, map[string]int{}},
		{"invalid detail bounds", &dto.Usage{PromptTokens: 10, CompletionTokens: 4, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 999}, CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 8}}, map[string]int{"input_cache_read": 10, "output_reasoning": 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := common.Marshal(tc.usage)
			require.NoError(t, err)
			snapshot := FromUsage(tc.usage)
			js, err := snapshot.ToLangfuseJSON()
			require.NoError(t, err)
			var got map[string]int
			require.NoError(t, common.UnmarshalJsonStr(js, &got))
			assert.Equal(t, tc.want, got)
			after, err := common.Marshal(tc.usage)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			if tc.usage != nil {
				assert.Equal(t, tc.usage.UsageSource, snapshot.UsageSource)
			}
		})
	}
}
