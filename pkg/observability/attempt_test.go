package observability

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestAttemptRetrySnapshots(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	tracer := provider.Tracer("test")
	ctx, root := StartRootSpan(tracer, context.Background(), RootSpanStart{RequestID: "req-retry", Environment: "test", Protocol: "openai", CaptureContent: true, MaxContentBytes: 256})
	request := &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: "hello"}}}
	first := BeginAttempt(ctx, tracer, GenSpanStart{RetryIndex: 0, OriginModel: "client-model", UpstreamModel: "upstream-a", ChannelID: 1}, request)
	require.NotNil(t, first)
	first.MarkFirstResponse()
	first.output.Append("failed partial")
	first.End(false, "", false, errors.New("SECRET_RAW_ERROR"))
	second := BeginAttempt(ctx, tracer, GenSpanStart{RetryIndex: 1, OriginModel: "client-model", UpstreamModel: "upstream-b", ChannelID: 2}, request)
	require.NotNil(t, second)
	second.output.Append("final")
	usage := &dto.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, UsageSource: "upstream"}
	second.RecordUsage(usage)
	usage.PromptTokens = 999 // simulate later billing mutation
	second.End(false, "", false, nil)
	second.EndRoot(root)
	spans := recorder.Ended()
	require.Len(t, spans, 3)
	assert.Equal(t, spans[0].Parent(), spans[1].Parent())
	assert.Equal(t, spans[2].SpanContext().SpanID(), spans[0].Parent().SpanID())
	assert.Equal(t, codes.Error, spans[0].Status().Code)
	assert.Equal(t, codes.Ok, spans[1].Status().Code)
	assert.Equal(t, codes.Ok, spans[2].Status().Code)
	for _, span := range spans {
		attrs := attribute.NewSet(span.Attributes()...)
		id, ok := attrs.Value(attribute.Key(AttrMetadata + "." + MetaRequestID))
		require.True(t, ok)
		assert.Equal(t, "req-retry", id.AsString())
		env, _ := attrs.Value(attribute.Key(AttrEnvironment))
		assert.Equal(t, "test", env.AsString())
		for _, attr := range span.Attributes() {
			assert.NotContains(t, attr.Value.Emit(), "SECRET_RAW_ERROR")
		}
	}
	attrs := attribute.NewSet(spans[1].Attributes()...)
	details, ok := attrs.Value(attribute.Key(AttrUsageDetails))
	require.True(t, ok)
	assert.JSONEq(t, "{\"input\":10,\"output\":2}", details.AsString())
	_, ok = attrs.Value(attribute.Key(AttrMetadata + ".first_response_ms"))
	assert.False(t, ok, "retry without response cannot inherit first response timing")
	output, _ := attrs.Value(attribute.Key(AttrOutput))
	assert.Equal(t, "final", output.AsString())
	firstAttrs := attribute.NewSet(spans[0].Attributes()...)
	_, ok = firstAttrs.Value(attribute.Key(AttrUsageDetails))
	assert.False(t, ok)
}

func TestStreamOutcomes(t *testing.T) {
	for _, tc := range []struct {
		reason          string
		completed, soft bool
		err             error
		want            codes.Code
	}{
		{"done", true, false, nil, codes.Ok}, {"eof", true, false, nil, codes.Ok},
		{"eof", false, false, nil, codes.Error}, {"done", false, false, nil, codes.Error},
		{"handler_stop", true, false, errors.New("SECRET"), codes.Error},
		{"done", true, true, nil, codes.Error}, {"timeout", true, false, nil, codes.Error},
		{"client_gone", true, false, context.Canceled, codes.Error}, {"scanner_error", true, false, nil, codes.Error},
	} {
		t.Run(tc.reason+tc.want.String(), func(t *testing.T) {
			rec := tracetest.NewSpanRecorder()
			p := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
			defer p.Shutdown(context.Background())
			tr := p.Tracer("test")
			ctx, root := StartRootSpan(tr, context.Background(), RootSpanStart{})
			a := BeginAttempt(ctx, tr, GenSpanStart{}, nil)
			require.NotNil(t, a)
			a.completed = tc.completed
			a.End(true, tc.reason, tc.soft, tc.err)
			a.EndRoot(root)
			ended := rec.Ended()
			require.Len(t, ended, 2)
			assert.Equal(t, tc.want, ended[0].Status().Code)
			assert.Equal(t, tc.want, ended[1].Status().Code)
		})
	}
}

func TestSamplingAndCapturePrivacy(t *testing.T) {
	for _, sample := range []bool{false, true} {
		for _, capture := range []bool{false, true} {
			name := "unsampled"
			if sample {
				name = "sampled"
			}
			if capture {
				name += "-capture"
			}
			t.Run(name, func(t *testing.T) {
				rec := tracetest.NewSpanRecorder()
				sampler := sdktrace.NeverSample()
				if sample {
					sampler = sdktrace.AlwaysSample()
				}
				p := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sdktrace.ParentBased(sampler)))
				defer p.Shutdown(context.Background())
				tr := p.Tracer("test")
				ctx, root := StartRootSpan(tr, context.Background(), RootSpanStart{CaptureContent: capture, MaxContentBytes: 256})
				req := &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Role: "user", Content: []any{map[string]any{"type": "text", "text": strings.Repeat("你好<", 100)}, map[string]any{"type": "image_url", "image_url": "SECRET_MEDIA"}}}, {Role: "tool", Content: "SECRET_TOOL"}}}
				a := BeginAttempt(ctx, tr, GenSpanStart{}, req)
				if !sample {
					assert.Nil(t, a)
					root.End(OutcomeOk, nil)
					for _, s := range rec.Ended() {
						assert.False(t, s.SpanContext().IsSampled())
					}
					return
				}
				require.NotNil(t, a)
				var response dto.ClaudeResponse
				require.NoError(t, common.UnmarshalJsonStr("{\"type\":\"message\",\"content\":[{\"type\":\"text\",\"text\":\"public text\"},{\"type\":\"thinking\",\"thinking\":\"SECRET_REASON\"},{\"type\":\"tool_use\",\"input\":\"SECRET_ARGS\"}]}", &response))
				a.ObserveResponse(&response)
				a.End(false, "", false, nil)
				a.EndRoot(root)
				for _, s := range rec.Ended() {
					for _, attr := range s.Attributes() {
						assert.NotContains(t, attr.Value.Emit(), "SECRET_")
						if !capture {
							assert.NotEqual(t, attribute.Key(AttrInput), attr.Key)
							assert.NotEqual(t, attribute.Key(AttrOutput), attr.Key)
						}
					}
				}
				if capture {
					assert.True(t, a.input.Truncated())
					assert.Equal(t, "public text", a.output.Text())
					encoded, err := common.Marshal(a.input.Text())
					require.NoError(t, err)
					assert.LessOrEqual(t, len(encoded), 256)
				} else {
					assert.Nil(t, a.input)
					assert.Nil(t, a.output)
				}
			})
		}
	}
}

func TestPingsDoNotCountAsFirstResponse(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	p := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	defer p.Shutdown(context.Background())
	tracer := p.Tracer("test")
	ctx, root := StartRootSpan(tracer, context.Background(), RootSpanStart{})
	a := BeginAttempt(ctx, tracer, GenSpanStart{}, nil)
	require.NotNil(t, a)
	a.ObserveResponse(&dto.ClaudeResponse{Type: "ping"})
	a.ObserveResponse(&dto.ChatCompletionsStreamResponse{})
	a.ObserveResponse(&dto.ResponsesStreamResponse{Type: "ping"})
	assert.True(t, a.first.IsZero())
	a.End(true, "timeout", false, context.DeadlineExceeded)
	a.EndRoot(root)
	for _, s := range rec.Ended() {
		attrs := attribute.NewSet(s.Attributes()...)
		_, ok := attrs.Value(attribute.Key(AttrMetadata + ".first_response_ms"))
		assert.False(t, ok)
	}
}

func TestUsageSourceReflectsReportedAndEstimatedPortions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response any
		usage    *dto.Usage
		want     string
	}{
		{"gemini reported", &dto.GeminiChatResponse{UsageMetadata: dto.GeminiUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 2, ThoughtsTokenCount: 1, TotalTokenCount: 13}}, &dto.Usage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13}, "upstream"},
		{"partial report with output estimate", &dto.ChatCompletionsStreamResponse{Usage: &dto.Usage{PromptTokens: 10}}, &dto.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}, "mixed"},
		{"recognized response without usage", &dto.OpenAITextResponse{}, &dto.Usage{PromptTokens: 10, CompletionTokens: 2}, "estimated"},
		{"unsupported provider", nil, &dto.Usage{PromptTokens: 10}, "unspecified"},
		{"explicit source", nil, &dto.Usage{PromptTokens: 10, UsageSource: "upstream"}, "upstream"},
		{"missing usage", &dto.OpenAITextResponse{}, nil, "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := tracetest.NewSpanRecorder()
			p := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
			t.Cleanup(func() { require.NoError(t, p.Shutdown(context.Background())) })
			ctx, root := StartRootSpan(p.Tracer("test"), context.Background(), RootSpanStart{})
			a := BeginAttempt(ctx, p.Tracer("test"), GenSpanStart{}, nil)
			if tc.response != nil {
				a.ObserveResponse(tc.response)
			}
			a.RecordUsage(tc.usage)
			a.End(false, "", false, nil)
			a.EndRoot(root)
			spans := rec.Ended()
			require.Len(t, spans, 2)
			attrs := attribute.NewSet(spans[0].Attributes()...)
			source, _ := attrs.Value(attribute.Key(AttrMetadata + "." + MetaUsageSource))
			assert.Equal(t, tc.want, source.AsString())
		})
	}
}

func TestParsedProtocolOutputs(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		response   any
	}{
		{"openai", "{\"choices\":[{\"message\":{\"role\":\"assistant\",\"content\":\"hello\",\"reasoning_content\":\"SECRET_REASON\"}}]}", &dto.OpenAITextResponse{}},
		{"responses", "{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]},{\"type\":\"function_call\",\"arguments\":\"SECRET_ARGS\"}]}", &dto.OpenAIResponsesResponse{}},
		{"claude", "{\"type\":\"message\",\"content\":[{\"type\":\"text\",\"text\":\"hello\"},{\"type\":\"thinking\",\"thinking\":\"SECRET_REASON\"}]}", &dto.ClaudeResponse{}},
		{"gemini", "{\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"text\":\"hello\"},{\"text\":\"SECRET_THOUGHT\",\"thought\":true},{\"functionCall\":{\"name\":\"tool\",\"args\":{\"secret\":\"SECRET_ARGS\"}}}]}}]}", &dto.GeminiChatResponse{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, common.UnmarshalJsonStr(tc.body, tc.response))
			a := &Attempt{output: NewTextCapture(true, 256)}
			a.ObserveResponse(tc.response)
			assert.Equal(t, "hello", a.output.Text())
			assert.True(t, a.completed)
			assert.NotContains(t, a.output.Text(), "SECRET_")
		})
	}
}
