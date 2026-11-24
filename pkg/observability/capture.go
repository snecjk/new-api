package observability

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// captureText walks only known text blocks. It never descends into tool arguments,
// URLs, reasoning, metadata or arbitrary nested objects.
func (a *Attempt) captureText(dst *TextCapture, content any) {
	if dst == nil {
		return
	}
	switch v := content.(type) {
	case string:
		dst.Append(v)
	case *string:
		if v != nil {
			dst.Append(*v)
		}
	case []any:
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				a.skipped = true
				continue
			}
			kind, _ := block["type"].(string)
			if kind == "text" || kind == "input_text" || kind == "output_text" {
				if text, ok := block["text"].(string); ok {
					dst.Append(text)
				}
			} else {
				a.skipped = true
			}
		}
	case []dto.MediaContent:
		for _, block := range v {
			if block.Type == dto.ContentTypeText {
				dst.Append(block.Text)
			} else {
				a.skipped = true
			}
		}
	case []dto.ClaudeMediaMessage:
		for _, block := range v {
			if block.Type == dto.ContentTypeText {
				dst.Append(block.GetText())
			} else {
				a.skipped = true
			}
		}
	case nil:
	default:
		a.skipped = true
	}
}

func (a *Attempt) captureRequest(request any) {
	switch r := request.(type) {
	case *dto.GeneralOpenAIRequest:
		for _, m := range r.Messages {
			if m.Role == "user" || m.Role == "system" || m.Role == "developer" || m.Role == "assistant" {
				a.captureText(a.input, m.Content)
				a.input.Append("\n")
			} else {
				a.skipped = true
			}
		}
	case *dto.ClaudeRequest:
		a.captureText(a.input, r.System)
		for _, m := range r.Messages {
			if m.Role == "user" || m.Role == "assistant" {
				a.captureText(a.input, m.Content)
				a.input.Append("\n")
			} else {
				a.skipped = true
			}
		}
	case *dto.GeminiChatRequest:
		if r.SystemInstructions != nil {
			a.captureGeminiParts(a.input, r.SystemInstructions.Parts)
		}
		for _, m := range r.Contents {
			a.captureGeminiParts(a.input, m.Parts)
		}
	case *dto.OpenAIResponsesCompactionRequest:
		a.input.appendJSONText(r.Instructions)
		a.captureResponsesInput(r.Input)
	case *dto.OpenAIResponsesRequest:
		a.input.appendJSONText(r.Instructions)
		a.captureResponsesInput(r.Input)

	default:
		a.skipped = true
	}
}

func (a *Attempt) captureGeminiParts(dst *TextCapture, parts []dto.GeminiPart) {
	if dst == nil {
		return
	}
	for _, part := range parts {
		if !part.Thought && part.InlineData == nil && part.FileData == nil && part.FunctionCall == nil && part.FunctionResponse == nil && part.ExecutableCode == nil && part.CodeExecutionResult == nil {
			dst.Append(part.Text)
		} else {
			a.skipped = true
		}
	}
}

// ObserveResponse is fed parsed objects before any response conversion. Protocol
// completion is tracked even when content capture is disabled.
func (a *Attempt) ObserveResponse(response any) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ended {
		return
	}
	switch r := response.(type) {
	case *dto.ClaudeResponse:
		if r.Type == "ping" {
			return
		}
	case *dto.ChatCompletionsStreamResponse:
		if len(r.Choices) == 0 && r.Usage == nil {
			return
		}
	case *dto.ResponsesStreamResponse:
		if r.Type == "ping" {
			return
		}
	case *dto.GeminiChatResponse:
		if len(r.Candidates) == 0 && !dto.HasGeminiUsageMetadataTokens(r.GetUsageMetadata()) {
			return
		}
	}
	if a.first.IsZero() {
		a.first = time.Now()
	}
	a.observed = true
	switch r := response.(type) {
	case *dto.OpenAITextResponse:
		a.reportedUsage = r.Usage.TotalTokens > 0 || r.Usage.PromptTokens > 0 || r.Usage.CompletionTokens > 0
		a.reportedInput = r.Usage.PromptTokens
		a.reportedOutput = r.Usage.CompletionTokens
		for _, c := range r.Choices {
			a.captureText(a.output, c.Message.Content)
		}
		a.completed = true
	case *dto.ChatCompletionsStreamResponse:
		if r.Usage != nil && (r.Usage.PromptTokens > 0 || r.Usage.CompletionTokens > 0 || r.Usage.TotalTokens > 0) {
			a.reportedUsage = true
			a.reportedInput = r.Usage.PromptTokens
			a.reportedOutput = r.Usage.CompletionTokens
		}
		for _, c := range r.Choices {
			a.captureText(a.output, c.Delta.Content)
			if c.FinishReason != nil && *c.FinishReason != "" {
				a.completed = true
			}
		}
	case *dto.OpenAIResponsesResponse:
		a.reportedUsage = r.Usage != nil
		if r.Usage != nil {
			a.reportedInput = r.Usage.InputTokens
			a.reportedOutput = r.Usage.OutputTokens
		}
		for _, item := range r.Output {
			if item.Type != "message" {
				a.skipped = true
				continue
			}
			for _, c := range item.Content {
				if c.Type == "output_text" {
					a.output.Append(c.Text)
				} else {
					a.skipped = true
				}
			}
		}
		var status string
		_ = common.Unmarshal(r.Status, &status)
		a.completed = status == "completed"
		a.protocolError = status == "failed" || status == "incomplete" || status == "cancelled" || status == "canceled"
	case *dto.ResponsesStreamResponse:
		if r.Response != nil && r.Response.Usage != nil {
			a.reportedUsage = true
			a.reportedInput = r.Response.Usage.InputTokens
			a.reportedOutput = r.Response.Usage.OutputTokens
		}
		switch r.Type {
		case "response.output_text.delta":
			a.output.Append(r.Delta)
		case "response.completed", "response.done":
			a.completed = true
			if r.Response != nil {
				var status string
				_ = common.Unmarshal(r.Response.Status, &status)
				if status == "failed" || status == "incomplete" || status == "cancelled" || status == "canceled" {
					a.protocolError = true
				}
			}
		case "error", "response.error", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
			a.protocolError = true
		}
	case *dto.ClaudeResponse:
		if r.Usage != nil {
			a.reportedUsage = true
			if r.Usage.InputTokens > 0 {
				a.reportedInput = r.Usage.InputTokens
			}
			if r.Usage.OutputTokens > 0 {
				a.reportedOutput = r.Usage.OutputTokens
			}
		}
		if r.Message != nil && r.Message.Usage != nil {
			a.reportedUsage = true
			a.reportedInput = r.Message.Usage.InputTokens
			a.reportedOutput = r.Message.Usage.OutputTokens
		}
		if r.Type == "message_stop" {
			a.completed = true
		}
		if r.Type == "error" {
			a.protocolError = true
		}
		if r.Type == "content_block_delta" && r.Delta != nil && r.Delta.Type == "text_delta" {
			a.output.Append(r.Delta.GetText())
		}
		if r.Type == "content_block_start" && r.ContentBlock != nil && r.ContentBlock.Type == "text" {
			a.output.Append(r.ContentBlock.GetText())
		}
		if r.Type == "message" || r.Type == "" {
			for _, block := range r.Content {
				if block.Type == "text" {
					a.output.Append(block.GetText())
				} else {
					a.skipped = true
				}
			}
			a.completed = true
		}
	case *dto.GeminiChatResponse:
		if metadata := r.GetUsageMetadata(); dto.HasGeminiUsageMetadataTokens(metadata) {
			a.reportedUsage = true
			a.reportedInput = metadata.PromptTokenCount
			a.reportedOutput = metadata.CandidatesTokenCount + metadata.ThoughtsTokenCount
		}
		for _, c := range r.Candidates {
			a.captureGeminiParts(a.output, c.Content.Parts)
			if c.FinishReason != nil && *c.FinishReason != "" {
				a.completed = true
			}
		}
	default:
		a.observed = false
		a.skipped = true
	}
}
