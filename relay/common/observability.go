package common

import (
	"context"

	"github.com/QuantumNous/new-api/pkg/observability"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// BeginTraceAttempt is called only after conversion, at the actual upstream boundary.
// It never changes billing/request timing or installs upstream propagation headers.
func (info *RelayInfo) BeginTraceAttempt(ctx context.Context) *observability.Attempt {
	info.TraceAttempt = observability.BeginAttempt(ctx, observability.GlobalTracer("new-api.relay"), observability.GenSpanStart{
		RetryIndex: info.RetryIndex, OriginModel: info.OriginModelName,
		UpstreamModel: info.GetUpstreamModelName(), Protocol: string(info.RelayFormat), ChannelID: info.GetChannelID(),
	}, info.Request)
	// Preserve business state; only observe a StreamStatus created by this attempt.
	info.traceInitialStreamStatus = info.StreamStatus
	info.TraceAttempt.SetStreaming(info.IsStream)
	return info.TraceAttempt
}

func (info *RelayInfo) FinishTraceAttempt(attempt *observability.Attempt, usage *dto.Usage, err error) {
	if attempt == nil {
		return
	}
	if usage != nil {
		attempt.RecordUsage(usage)
	}
	reason := ""
	softErrors := false
	if status := info.StreamStatus; status != nil && status != info.traceInitialStreamStatus {
		reason = string(status.EndReason)
		softErrors = status.HasErrors()
		if err == nil {
			err = status.EndError
		}
	}
	attempt.End(info.IsStream || (info.StreamStatus != nil && info.StreamStatus != info.traceInitialStreamStatus), reason, softErrors, err)
}

// ObserveResponse receives an already parsed protocol object, never raw headers/body.
func (info *RelayInfo) ObserveResponse(response any) { info.TraceAttempt.ObserveResponse(response) }
