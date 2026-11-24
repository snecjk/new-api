package observability

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Langfuse 观测属性名（v4 约定，langfuse. 前缀）。
const (
	AttrType            = "langfuse.observation.type"
	AttrName            = "langfuse.observation.name"
	AttrModel           = "langfuse.observation.model.name"
	AttrInput           = "langfuse.observation.input"
	AttrOutput          = "langfuse.observation.output"
	AttrMetadata        = "langfuse.observation.metadata"
	AttrTraceName       = "langfuse.trace.name"
	AttrTraceMetadata   = "langfuse.trace.metadata"
	AttrEnvironment     = "langfuse.environment"
	AttrLevel           = "langfuse.observation.level"
	AttrStatusMessage   = "langfuse.observation.status_message"
	AttrCompletionStart = "langfuse.observation.completion_start_time"
	AttrCaptureStatus   = "langfuse.observation.capture_status"

	// 受控元数据子键（不含秘密或自由文本）
	MetaRequestID     = "request_id"
	MetaChannelID     = "channel_id"
	MetaRetryIndex    = "retry_index"
	MetaOriginModel   = "origin_model"
	MetaUpstreamModel = "upstream_model"
	MetaProtocol      = "protocol"
	MetaUsageSource   = "usage_source"
	MetaUsageSemantic = "usage_semantic"
	MetaEndReason     = "stream_end_reason"
	MetaCaptureStatus = "capture_status"
)

// 观测类型（Langfuse v4 observation type）。
const (
	TypeSpan = "span"
	TypeGen  = "generation"
)

// 稳定操作名（不动态拼接，避免注入自由文本）。
const (
	OpRoot       = "relay-request"
	OpGeneration = "generate-response"
)

// 结果/等级（受控分类，不放上游原始错误）。
const (
	LevelInfo    = "DEFAULT"
	LevelWarn    = "WARNING"
	LevelError   = "ERROR"
	OutcomeOk    = "ok"
	OutcomeError = "error"
)

// RootSpanStart 是创建根 span 所需的最小信息。
type RootSpanStart struct {
	RequestID       string
	Protocol        string
	IsStream        bool
	Environment     string
	CaptureContent  bool
	MaxContentBytes int
}

// GenSpanStart 是创建子 generation span 所需的最小信息。
type GenSpanStart struct {
	RetryIndex    int
	OriginModel   string
	UpstreamModel string
	Protocol      string
	ChannelID     int
}

// RootSpan 是一次被采样的 relay 请求的根 observation（type=span）。
type RootSpan struct {
	originModel string
	span        trace.Span
	ctx         context.Context
	start       RootSpanStart
	tracer      trace.Tracer
}

// StartRootSpan 在 relay 入口创建根 span，采样决策由 OTEL SDK 在根创建时一次性做出，
// 后代通过 context 继承同一 trace ID。
func StartRootSpan(tracer trace.Tracer, ctx context.Context, start RootSpanStart) (context.Context, *RootSpan) {
	if tracer == nil {
		return ctx, nil
	}
	attrs := []attribute.KeyValue{
		attribute.String(AttrType, TypeSpan),
		attribute.String(AttrName, OpRoot),
		attribute.String(AttrTraceName, OpRoot),
		attribute.String(AttrTraceMetadata+"."+MetaRequestID, start.RequestID),
		attribute.String(AttrEnvironment, start.Environment),
		attribute.String(AttrMetadata+"."+MetaRequestID, start.RequestID),
		attribute.String(AttrMetadata+"."+MetaProtocol, start.Protocol),
	}
	if start.IsStream {
		attrs = append(attrs, attribute.Bool(AttrMetadata+"."+"is_stream", true))
	}
	ctx, span := tracer.Start(ctx, OpRoot,
		trace.WithAttributes(attrs...),
		trace.WithNewRoot(),
	)
	root := &RootSpan{span: span, ctx: ctx, start: start, tracer: tracer}
	ctx = context.WithValue(ctx, rootContextKey{}, root)
	root.ctx = ctx
	return ctx, root
}

// Ctx 返回携带根 span 的 context（用于子 span 的父级）。
func (r *RootSpan) Ctx() context.Context {
	if r == nil {
		return nil
	}
	return r.ctx
}

// End 结束根 span，记录最终业务结果。
func (r *RootSpan) End(outcome string, err error) {
	if r == nil || r.span == nil {
		return
	}
	switch outcome {
	case OutcomeOk:
		r.span.SetAttributes(attribute.String(AttrLevel, LevelInfo))
		r.span.SetStatus(codes.Ok, "")
	default:
		r.span.SetAttributes(attribute.String(AttrLevel, LevelError))
		r.span.SetStatus(codes.Error, "")
		if err != nil {
			r.span.SetAttributes(attribute.String(AttrStatusMessage, classifyError(err)))
		}
	}
	r.span.End()
}

// GenerationSpan 是一次实际上游模型调用的子 observation（type=generation）。
type GenerationSpan struct {
	span trace.Span
}

// StartGeneration 在实际调用 DoRequest 前开始子 generation，保存本轮渠道/模型/起始时间。
func StartGeneration(tracer trace.Tracer, ctx context.Context, start GenSpanStart) (context.Context, *GenerationSpan) {
	if tracer == nil {
		return ctx, nil
	}
	root, _ := ctx.Value(rootContextKey{}).(*RootSpan)
	if root == nil || !root.span.IsRecording() {
		return ctx, nil
	}
	tracer = root.tracer
	if root.originModel != "" {
		start.OriginModel = root.originModel
	}

	attrs := []attribute.KeyValue{
		attribute.String(AttrEnvironment, root.start.Environment),
		attribute.String(AttrTraceName, OpRoot),
		attribute.String(AttrTraceMetadata+"."+MetaRequestID, root.start.RequestID),
		attribute.String(AttrMetadata+"."+MetaRequestID, root.start.RequestID),
		attribute.String(AttrType, TypeGen),
		attribute.String(AttrName, OpGeneration),
		attribute.Int(AttrMetadata+"."+MetaRetryIndex, start.RetryIndex),
		attribute.String(AttrMetadata+"."+MetaOriginModel, start.OriginModel),
		attribute.String(AttrMetadata+"."+MetaProtocol, start.Protocol),
	}
	if start.UpstreamModel != "" {
		attrs = append(attrs, attribute.String(AttrMetadata+"."+MetaUpstreamModel, start.UpstreamModel), attribute.String(AttrModel, start.UpstreamModel))
	}
	if start.ChannelID != 0 {
		attrs = append(attrs, attribute.Int(AttrMetadata+"."+MetaChannelID, start.ChannelID))
	}
	ctx, span := tracer.Start(ctx, OpGeneration,
		trace.WithAttributes(attrs...),
	)
	return ctx, &GenerationSpan{span: span}
}

// End 结束子 generation，记录模型/usage/结果。
func (g *GenerationSpan) End(outcome string, err error) {
	if g == nil || g.span == nil {
		return
	}
	switch outcome {
	case OutcomeOk:
		g.span.SetAttributes(attribute.String(AttrLevel, LevelInfo))
		g.span.SetStatus(codes.Ok, "")
	default:
		g.span.SetAttributes(attribute.String(AttrLevel, LevelError))
		g.span.SetStatus(codes.Error, "")
		if err != nil {
			g.span.SetAttributes(attribute.String(AttrStatusMessage, classifyError(err)))
		}
	}
	g.span.End()
}

// EndWithStream 基于流终止状态结束子 generation。IsNormalEnd 仅表示
// 协议层面的结束标记，还需结合 EndError 判断：handler_stop 附带错误、
// timeout/client_gone/scanner_error 均视为失败，不因 HTTP 200 而误判为成功。
func (g *GenerationSpan) EndWithStream(endReason string, firstResponseMs int64, endError error) {
	if g == nil || g.span == nil {
		return
	}
	g.SetEndReason(endReason, firstResponseMs)
	if endError != nil {
		g.span.SetAttributes(attribute.String(AttrLevel, LevelError))
		g.span.SetStatus(codes.Error, "")
		g.span.SetAttributes(attribute.String(AttrStatusMessage, classifyError(endError)))
	} else if isNormalEndReason(endReason) {
		g.span.SetAttributes(attribute.String(AttrLevel, LevelInfo))
		g.span.SetStatus(codes.Ok, "")
	} else {
		g.span.SetAttributes(attribute.String(AttrLevel, LevelError))
		g.span.SetStatus(codes.Error, "")
	}
	g.span.End()
}

// RootEndWithStream 同上，用于根 span。
func (r *RootSpan) EndWithStream(endReason string, firstResponseMs int64, endError error) {
	if r == nil || r.span == nil {
		return
	}
	r.SetEndReason(endReason, firstResponseMs)
	if endError != nil {
		r.span.SetAttributes(attribute.String(AttrLevel, LevelError))
		r.span.SetStatus(codes.Error, "")
		r.span.SetAttributes(attribute.String(AttrStatusMessage, classifyError(endError)))
	} else if isNormalEndReason(endReason) {
		r.span.SetAttributes(attribute.String(AttrLevel, LevelInfo))
		r.span.SetStatus(codes.Ok, "")
	} else {
		r.span.SetAttributes(attribute.String(AttrLevel, LevelError))
		r.span.SetStatus(codes.Error, "")
	}
	r.span.End()
}

func isNormalEndReason(reason string) bool {
	return reason == "completed" || reason == "done" || reason == "eof" || reason == "handler_stop"
}

// classifyError 把任意错误映射为受控分类，不泄露上游原始错误文本。
func classifyError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return OutcomeError
}

// SetRootModel 更新根 span 的上游模型名（在模型映射完成后调用）。
func (r *RootSpan) SetModel(upstreamModel string) {
	if r == nil || r.span == nil || upstreamModel == "" {
		return
	}
	r.span.SetAttributes(attribute.String(AttrModel, upstreamModel))
}

// SetRootUsage 将 usage 记录到根 span 的 metadata（供筛选，不重复累计）。
func (r *RootSpan) SetUsage(usageSource, usageSemantic string, promptTokens, completionTokens, totalTokens int) {
	if r == nil || r.span == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String(AttrMetadata+"."+MetaUsageSource, usageSource),
		attribute.String(AttrMetadata+"."+MetaUsageSemantic, usageSemantic),
		attribute.Int("langfuse.observation.metadata.prompt_tokens", promptTokens),
		attribute.Int("langfuse.observation.metadata.completion_tokens", completionTokens),
		attribute.Int("langfuse.observation.metadata.total_tokens", totalTokens),
	}
	r.span.SetAttributes(attrs...)
}

// SetGenUsage 将 usage 记录到子 generation。
func (g *GenerationSpan) SetUsage(usageSource, usageSemantic string, promptTokens, completionTokens, totalTokens int) {
	if g == nil || g.span == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String(AttrMetadata+"."+MetaUsageSource, usageSource),
		attribute.String(AttrMetadata+"."+MetaUsageSemantic, usageSemantic),
		attribute.Int("langfuse.observation.metadata.prompt_tokens", promptTokens),
		attribute.Int("langfuse.observation.metadata.completion_tokens", completionTokens),
		attribute.Int("langfuse.observation.metadata.total_tokens", totalTokens),
	}
	g.span.SetAttributes(attrs...)
}

// SetGenInputOutput 记录 generation 的输入/输出文本（仅在 capture_content 时调用）。
func (g *GenerationSpan) SetInputOutput(input, output string) {
	if g == nil || g.span == nil {
		return
	}
	if input != "" {
		g.span.SetAttributes(attribute.String(AttrInput, input))
	}
	if output != "" {
		g.span.SetAttributes(attribute.String(AttrOutput, output))
	}
}

// SetAttribute 设置单个观测属性（仅用于已结束前的属性补齐）。
func (g *GenerationSpan) SetAttribute(key, value string) {
	if g == nil || g.span == nil {
		return
	}
	g.span.SetAttributes(attribute.String(key, value))
}

// SetAttribute 设置单个观测属性（仅用于已结束前的属性补齐）。
func (r *RootSpan) SetAttribute(key, value string) {
	if r == nil || r.span == nil {
		return
	}
	r.span.SetAttributes(attribute.String(key, value))
}
func (g *GenerationSpan) SetEndReason(endReason string, firstResponseMs int64) {
	if g == nil || g.span == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String(AttrMetadata+"."+MetaEndReason, endReason),
	}
	if firstResponseMs > 0 {
		attrs = append(attrs, attribute.Int64("langfuse.observation.metadata.first_response_ms", firstResponseMs))
	}
	g.span.SetAttributes(attrs...)
}

// SetRootEndReason 记录根 span 的流终止信息。
func (r *RootSpan) SetEndReason(endReason string, firstResponseMs int64) {
	if r == nil || r.span == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String(AttrMetadata+"."+MetaEndReason, endReason),
	}
	if firstResponseMs > 0 {
		attrs = append(attrs, attribute.Int64("langfuse.observation.metadata.first_response_ms", firstResponseMs))
	}
	r.span.SetAttributes(attrs...)
}

type rootContextKey struct{}

// SetRequestMetadata runs after validation and before any channel mapping.
func (r *RootSpan) SetRequestMetadata(model string, stream bool) {
	if r == nil {
		return
	}
	r.originModel = model
	r.start.IsStream = stream
	r.span.SetAttributes(attribute.String(AttrMetadata+"."+MetaOriginModel, model), attribute.Bool(AttrMetadata+".is_stream", stream))
}
