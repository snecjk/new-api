package openai

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/observability"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTracingPreservesConvertedSSEAndFlush(t *testing.T) {
	previous := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previous })
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fixture\",\"created_at\":1710000000,\"model\":\"gpt-test\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"SECRET_TOOL_ARGUMENTS\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":3,\"total_tokens\":5}}}\n\ndata: [DONE]\n\n"
	var baseline string
	var baselineUsage *dto.Usage
	for _, enabled := range []bool{false, true} {
		c, writer, response, info := newResponsesChatTestContext(t, body, true)
		var root *observability.RootSpan
		rec := tracetest.NewSpanRecorder()
		provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		tracer := provider.Tracer("test")
		if enabled {
			ctx, r := observability.StartRootSpan(tracer, c.Request.Context(), observability.RootSpanStart{RequestID: "sse", CaptureContent: true, MaxContentBytes: 256})
			root = r
			c.Request = c.Request.WithContext(ctx)
			info.TraceAttempt = observability.BeginAttempt(ctx, tracer, observability.GenSpanStart{}, nil)
		}
		usage, apiErr := OaiResponsesToChatStreamHandler(c, info, response)
		require.Nil(t, apiErr)
		require.NotNil(t, usage)
		if !enabled {
			baseline = writer.Body.String()
			baselineUsage = usage
		} else {
			info.FinishTraceAttempt(info.TraceAttempt, usage, nil)
			info.TraceAttempt.EndRoot(root)
			assert.Equal(t, baseline, writer.Body.String())
			assert.Equal(t, baselineUsage, usage)
			spans := rec.Ended()
			require.Len(t, spans, 2)
			assert.Equal(t, codes.Ok, spans[0].Status().Code)
			attrs := attribute.NewSet(spans[0].Attributes()...)
			out, _ := attrs.Value(attribute.Key(observability.AttrOutput))
			assert.Equal(t, "hello", out.AsString())
			for _, s := range spans {
				for _, a := range s.Attributes() {
					assert.NotContains(t, a.Value.Emit(), "SECRET_TOOL_ARGUMENTS")
				}
			}
		}
		assert.True(t, writer.Flushed, "stream must retain Flusher behavior")
		require.NoError(t, provider.Shutdown(context.Background()))
	}
}

func TestTracingMarksHTTP200TruncatedStreamAsFailure(t *testing.T) {
	service.InitTokenEncoders()
	old := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = old })
	for _, body := range []string{
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n",
		"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\ndata: [DONE]\n\n",
		"data: {\"type\":\"response.done\",\"response\":{\"status\":\"incomplete\"}}\n\ndata: [DONE]\n\n",
	} {
		c, _, response, info := newResponsesChatTestContext(t, body, true)
		rec := tracetest.NewSpanRecorder()
		p := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		tracer := p.Tracer("test")
		ctx, root := observability.StartRootSpan(tracer, c.Request.Context(), observability.RootSpanStart{})
		info.TraceAttempt = observability.BeginAttempt(ctx, tracer, observability.GenSpanStart{}, nil)
		usage, apiErr := OaiResponsesStreamHandler(c, info, response)
		require.Nil(t, apiErr, "telemetry must not replace the existing helper return value")
		info.FinishTraceAttempt(info.TraceAttempt, usage, nil)
		info.TraceAttempt.EndRoot(root)
		ended := rec.Ended()
		require.Len(t, ended, 2)
		assert.Equal(t, codes.Error, ended[0].Status().Code)
		assert.Equal(t, codes.Error, ended[1].Status().Code)
		require.NoError(t, p.Shutdown(context.Background()))
	}
}
