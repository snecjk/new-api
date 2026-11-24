package observability

import (
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"net/http"
)

func NewTestHTTPExporter(client *http.Client) (*Exporter, error) {
	cfg := validConfig()
	cfg.ExportTimeoutSec = 1
	return newExporter(cfg, client)
}

// NewTestExporter exists only in the test binary for cross-package integration
// tests. Production has no injectable provider or insecure transport setting.
func NewTestExporter(cfg Config, sink sdktrace.SpanExporter) *Exporter {
	return &Exporter{config: cfg, enabled: cfg.Enabled, provider: sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRate))), sdktrace.WithSyncer(sink))}
}
