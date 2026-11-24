package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/observability"
	openaichannel "github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestActualCallBoundaries(t *testing.T) {
	service.InitHttpClient()
	for _, tc := range []struct {
		name, path    string
		format        types.RelayFormat
		channel, mode int
		req           dto.Request
		helper        func(*gin.Context, *relaycommon.RelayInfo) *types.NewAPIError
	}{
		{"chat", "/v1/chat/completions", types.RelayFormatOpenAI, constant.ChannelTypeOpenAI, relayconstant.RelayModeChatCompletions, &dto.GeneralOpenAIRequest{Model: "client-model", Messages: []dto.Message{{Role: "user", Content: "hello"}}}, TextHelper},
		{"responses", "/v1/responses", types.RelayFormatOpenAIResponses, constant.ChannelTypeOpenAI, relayconstant.RelayModeResponses, &dto.OpenAIResponsesRequest{Model: "client-model", Input: []byte("\"hello\"")}, ResponsesHelper},
		{"compact", "/v1/responses/compact", types.RelayFormatOpenAIResponsesCompaction, constant.ChannelTypeOpenAI, relayconstant.RelayModeResponsesCompact, &dto.OpenAIResponsesCompactionRequest{Model: "client-model", Input: []byte("[]")}, ResponsesHelper},
		{"claude", "/v1/messages", types.RelayFormatClaude, constant.ChannelTypeAnthropic, relayconstant.RelayModeChatCompletions, &dto.ClaudeRequest{Model: "client-model", Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}}}, ClaudeHelper},
		{"gemini", "/v1beta/models/client-model:generateContent", types.RelayFormatGemini, constant.ChannelTypeGemini, relayconstant.RelayModeGemini, &dto.GeminiChatRequest{Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{{Text: "hello"}}}}}, GeminiHelper},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				// Normal channel credentials are allowed upstream; observability keys and baggage are not.
				assert.NotContains(t, r.Header.Get("Authorization"), "pk-lf-")
				assert.Empty(t, r.Header.Get("Baggage"))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(500)
				_, _ = io.WriteString(w, "{\"error\":{\"type\":\"server_error\",\"message\":\"SECRET_UPSTREAM_ERROR\"}}")
			}))
			defer server.Close()
			rec := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
			defer provider.Shutdown(context.Background())
			ctx, root := observability.StartRootSpan(provider.Tracer("test"), context.Background(), observability.RootSpanStart{RequestID: "boundary", Environment: "test"})
			root.SetRequestMetadata("client-model", false)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", tc.path, strings.NewReader("{}")).WithContext(ctx)
			common.SetContextKey(c, constant.ContextKeyChannelType, tc.channel)
			common.SetContextKey(c, constant.ContextKeyChannelId, 12)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, server.URL)
			common.SetContextKey(c, constant.ContextKeyOriginalModel, "client-model")
			common.SetContextKey(c, constant.ContextKeyChannelKey, "SECRET_CHANNEL_KEY")
			c.Set("model_mapping", "{\"client-model\":\"upstream-model\"}")
			info := &relaycommon.RelayInfo{Request: tc.req, RelayFormat: tc.format, RelayMode: tc.mode, OriginModelName: "client-model", RequestURLPath: tc.path, StartTime: time.Now()}
			apiErr := tc.helper(c, info)
			require.NotNil(t, apiErr)
			root.End(observability.OutcomeError, apiErr)
			require.EqualValues(t, 1, calls.Load())
			spans := rec.Ended()
			require.Len(t, spans, 2, "exactly one generation per actual call")
			gen := spans[0]
			assert.Equal(t, spans[1].SpanContext().SpanID(), gen.Parent().SpanID())
			assert.Equal(t, codes.Error, gen.Status().Code)
			attrs := attribute.NewSet(gen.Attributes()...)
			model, _ := attrs.Value(attribute.Key(observability.AttrModel))
			assert.Equal(t, "upstream-model", model.AsString())
			origin, _ := attrs.Value(attribute.Key(observability.AttrMetadata + ".origin_model"))
			assert.Equal(t, "client-model", origin.AsString())
			channel, _ := attrs.Value(attribute.Key(observability.AttrMetadata + ".channel_id"))
			assert.EqualValues(t, 12, channel.AsInt64())
			environment, _ := attrs.Value(attribute.Key(observability.AttrEnvironment))
			assert.Equal(t, "test", environment.AsString())
			for _, s := range spans {
				for _, attr := range s.Attributes() {
					assert.NotContains(t, attr.Value.Emit(), "SECRET_")
				}
			}
			// A conversion failure is before the actual call and cannot fabricate a generation.
			before := len(spans)
			c.Set("model_mapping", "not-json")
			require.NotNil(t, tc.helper(c, info))
			assert.Len(t, rec.Ended(), before)
			assert.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestChatViaResponsesSuccessRecordsUsage(t *testing.T) {
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/responses", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{\"id\":\"resp_fixture\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"upstream-model\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":2,\"total_tokens\":12}}")
	}))
	defer server.Close()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	ctx, root := observability.StartRootSpan(provider.Tracer("test"), context.Background(), observability.RootSpanStart{RequestID: "conversion", CaptureContent: true, MaxContentBytes: 256})
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
	request := &dto.GeneralOpenAIRequest{Model: "upstream-model", Messages: []dto.Message{{Role: "user", Content: "test"}}}
	info := &relaycommon.RelayInfo{Request: request, RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions, OriginModelName: "client-model", RequestURLPath: "/v1/chat/completions", ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, ChannelBaseUrl: server.URL, UpstreamModelName: "upstream-model", ApiKey: "fixture"}}
	adaptor := &openaichannel.Adaptor{}
	adaptor.Init(info)
	usage, apiErr := chatCompletionsViaResponses(c, info, adaptor, request)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	info.TraceAttempt.EndRoot(root)
	spans := recorder.Ended()
	require.Len(t, spans, 2)
	attrs := attribute.NewSet(spans[0].Attributes()...)
	usageAttr, ok := attrs.Value(attribute.Key(observability.AttrUsageDetails))
	require.True(t, ok)
	assert.JSONEq(t, "{\"input\":10,\"output\":2}", usageAttr.AsString())
	output, _ := attrs.Value(attribute.Key(observability.AttrOutput))
	assert.Equal(t, "hello", output.AsString())
	assert.Equal(t, codes.Ok, spans[0].Status().Code)
	assert.Contains(t, writer.Body.String(), "hello")
	assert.Equal(t, relayconstant.RelayModeChatCompletions, info.RelayMode)
}
