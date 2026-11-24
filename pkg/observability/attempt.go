package observability

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const AttrUsageDetails = "langfuse.observation.usage_details"

// Attempt owns bounded state for one actual call. Scanner hooks may run in another goroutine.
type Attempt struct {
	reportedInput, reportedOutput               int
	streaming                                   bool
	start                                       GenSpanStart
	mu                                          sync.Mutex
	gen                                         *GenerationSpan
	root                                        *RootSpan
	started                                     time.Time
	first                                       time.Time
	input, output                               *TextCapture
	observed, completed, protocolError, skipped bool
	reportedUsage                               bool
	ended                                       bool
	outcome, reason                             string
	endErr                                      error
}

func BeginAttempt(ctx context.Context, tracer trace.Tracer, start GenSpanStart, request any) *Attempt {
	_, gen := StartGeneration(tracer, ctx, start)
	if gen == nil {
		return nil
	}
	root, _ := ctx.Value(rootContextKey{}).(*RootSpan)
	a := &Attempt{gen: gen, root: root, started: time.Now(), start: start}
	gen.SetAttribute(AttrMetadata+"."+MetaUsageSource, "missing")
	if root.start.CaptureContent {
		a.input = NewTextCapture(true, root.start.MaxContentBytes)
		a.output = NewTextCapture(true, root.start.MaxContentBytes)
		a.captureRequest(request)
	}
	return a
}

func (a *Attempt) MarkFirstResponse() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.first.IsZero() {
		a.first = time.Now()
	}
}

// RecordUsage snapshots before billing can normalize or mutate the returned DTO.
func (a *Attempt) RecordUsage(usage *dto.Usage) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ended {
		return
	}
	if usage == nil {
		a.gen.SetAttribute(AttrMetadata+"."+MetaUsageSource, "missing")
		return
	}
	snapshot := FromUsage(usage)
	if snapshot.UsageSource == "" {
		if a.reportedUsage {
			snapshot.UsageSource = "upstream"
			if (a.reportedInput == 0 && snapshot.PromptTokens > 0) || (a.reportedOutput == 0 && snapshot.CompletionTokens > 0) {
				snapshot.UsageSource = "mixed"
			}
		} else if !a.observed {
			snapshot.UsageSource = "unspecified"
		} else if usage.TotalTokens > 0 || usage.PromptTokens > 0 || usage.CompletionTokens > 0 {
			snapshot.UsageSource = "estimated"
		} else {
			snapshot.UsageSource = "missing"
		}
	}
	a.gen.SetAttribute(AttrMetadata+"."+MetaUsageSource, snapshot.UsageSource)
	a.gen.SetAttribute(AttrMetadata+"."+MetaUsageSemantic, snapshot.UsageSemantic)
	a.gen.SetUsage(snapshot.UsageSource, snapshot.UsageSemantic, snapshot.PromptTokens, snapshot.CompletionTokens, snapshot.TotalTokens)
	for key, value := range map[string]int{
		"cache_read_tokens":        max(snapshot.InputTokenDetails.CachedTokens, snapshot.PromptCacheHitTokens),
		"cache_write_tokens":       snapshot.InputTokenDetails.CacheCreationTokensTotal(),
		"cache_creation_5m_tokens": snapshot.CacheCreation5m, "cache_creation_1h_tokens": snapshot.CacheCreation1h,
		"input_text_tokens": snapshot.InputTokenDetails.TextTokens, "input_audio_tokens": snapshot.InputTokenDetails.AudioTokens, "input_image_tokens": snapshot.InputTokenDetails.ImageTokens,
		"output_text_tokens": snapshot.OutputTokenDetails.TextTokens, "output_audio_tokens": snapshot.OutputTokenDetails.AudioTokens, "output_image_tokens": snapshot.OutputTokenDetails.ImageTokens,
		"reasoning_tokens": snapshot.OutputTokenDetails.ReasoningTokens,
	} {
		if value > 0 {
			a.gen.span.SetAttributes(attribute.Int(AttrMetadata+"."+key, value))
		}
	}
	if js, err := snapshot.ToLangfuseJSON(); err == nil && js != "{}" {
		a.gen.SetAttribute(AttrUsageDetails, js)
	}
}

func (a *Attempt) End(stream bool, reason string, softErrors bool, err error) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ended {
		return
	}
	a.ended = true
	stream = stream || a.streaming
	if stream && reason == "" && a.completed {
		reason = "completed"
	}
	a.reason = reason
	if err == nil && (softErrors || a.protocolError || (stream && (!isNormalEndReason(reason) || !a.completed))) {
		err = errors.New("incomplete_or_failed_stream")
	}
	a.endErr = err
	a.outcome = OutcomeOk
	if err != nil {
		a.outcome = OutcomeError
	}
	a.gen.SetEndReason(reason, a.firstResponseMs())
	if a.input != nil {
		a.gen.SetInputOutput(a.input.Text(), a.output.Text())
		status := "captured"
		if !a.observed {
			status = "unsupported"
		} else if a.skipped {
			status = "partial"
		}
		a.gen.SetAttribute(AttrCaptureStatus, status)
		if a.input.Truncated() {
			a.gen.SetAttribute(AttrMetadata+".input_truncated", "true")
		}
		if a.output.Truncated() {
			a.gen.SetAttribute(AttrMetadata+".output_truncated", "true")
		}
	}
	a.gen.End(a.outcome, err)
}

func (a *Attempt) firstResponseMs() int64 {
	if a.first.IsZero() {
		return 0
	}
	return a.first.Sub(a.started).Milliseconds()
}

func (a *Attempt) EndRoot(root *RootSpan) {
	if root == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	root.SetModel(a.start.UpstreamModel)
	root.SetEndReason(a.reason, a.firstResponseMs())
	if a.input != nil {
		root.SetAttribute(AttrInput, a.input.Text())
		root.SetAttribute(AttrOutput, a.output.Text())
		if a.input.Truncated() {
			root.SetAttribute(AttrMetadata+".input_truncated", "true")
		}
		if a.output.Truncated() {
			root.SetAttribute(AttrMetadata+".output_truncated", "true")
		}
	}
	root.End(a.outcome, a.endErr)
}

func (a *Attempt) SetStreaming(stream bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.streaming = a.streaming || stream
}
