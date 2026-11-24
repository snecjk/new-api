package controller

import (
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestTracingScopeUsesOperationNotJustWireFormat(t *testing.T) {
	for _, tc := range []struct {
		format types.RelayFormat
		path   string
		want   bool
	}{
		{types.RelayFormatOpenAI, "/v1/chat/completions", true},
		{types.RelayFormatOpenAI, "/v1/images/generations", false},
		{types.RelayFormatOpenAI, "/v1/embeddings", false},
		{types.RelayFormatOpenAI, "/v1/audio/speech", false},
		{types.RelayFormatOpenAI, "/v1/rerank", false},
		{types.RelayFormatOpenAIRealtime, "/v1/realtime", false},
		{types.RelayFormatOpenAIAlphaSearch, "/v1/alpha/search", false},
		{types.RelayFormatOpenAIResponses, "/v1/responses", true},
		{types.RelayFormatOpenAIResponsesCompaction, "/v1/responses/compact", true},
		{types.RelayFormatClaude, "/v1/messages", true},
		{types.RelayFormatGemini, "/v1beta/models/gemini:generateContent", true},
		{types.RelayFormatGemini, "/v1beta/models/gemini:streamGenerateContent", true},
		{types.RelayFormatGemini, "/v1beta/models/gemini:embedContent", false},
		{types.RelayFormatGemini, "/v1beta/models/gemini:batchEmbedContents", false},
	} {
		t.Run(tc.path, func(t *testing.T) { assert.Equal(t, tc.want, isSupportedTraceOperation(tc.format, tc.path)) })
	}
}
