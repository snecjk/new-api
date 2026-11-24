package observability

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var (
	globalMu  sync.RWMutex
	globalExp *Exporter
	globalEnv string
)

func SetGlobalExporter(e *Exporter) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalExp = e
	if e != nil {
		globalEnv = e.config.Environment
	}
}
func SetGlobalEnvironment(env string) { globalMu.Lock(); defer globalMu.Unlock(); globalEnv = env }
func GlobalEnvironment() string       { globalMu.RLock(); defer globalMu.RUnlock(); return globalEnv }
func GlobalConfig() Config {
	globalMu.RLock()
	defer globalMu.RUnlock()
	if globalExp == nil {
		return Config{}
	}
	return globalExp.config
}
func GlobalTracer(name string) trace.Tracer {
	globalMu.RLock()
	defer globalMu.RUnlock()
	if globalExp == nil {
		return trace.NewNoopTracerProvider().Tracer(name)
	}
	return globalExp.Tracer(name)
}
func IsEnabled() bool {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalExp != nil && globalExp.enabled
}

type Exporter struct {
	provider    *sdktrace.TracerProvider
	config      Config
	enabled     bool
	closeOnce   sync.Once
	shutdownErr error
	processor   *boundedProcessor
}

func New(cfg Config) (*Exporter, error) {
	return newExporter(cfg, &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, MaxIdleConns: 2, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second},
		// Reject every redirect, including subdomains: credentials never leave this origin.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       time.Duration(cfg.ExportTimeoutSec) * time.Second,
	})
}

// The client injection is restricted to package tests; production has no insecure toggle.
func newExporter(cfg Config, client *http.Client) (*Exporter, error) {
	e := &Exporter{config: cfg}
	if !cfg.Enabled {
		return e, nil
	}
	if err := cfg.Validate(); err != nil {
		return e, err
	}
	endpoint, err := cfg.IngestionURL()
	if err != nil {
		return e, err
	}
	// Enforce the same redirect boundary for injected TLS test clients.
	p := &boundedProcessor{}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	transport := clientCopy.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clientCopy.Transport = &ingestionTransport{next: transport, processor: p}
	client = &clientCopy
	exp, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpointURL(endpoint), otlptracehttp.WithHTTPClient(client),
		otlptracehttp.WithHeaders(map[string]string{"Authorization": cfg.BasicAuth(), "x-langfuse-ingestion-version": IngestionVersion}),
		otlptracehttp.WithTimeout(time.Duration(cfg.ExportTimeoutSec)*time.Second),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: true, InitialInterval: 100 * time.Millisecond, MaxInterval: time.Second, MaxElapsedTime: time.Duration(cfg.ExportTimeoutSec) * time.Second}),
	)
	if err != nil {
		return e, errors.New("langfuse: exporter initialization failed")
	}
	guarded := &diagnosticExporter{SpanExporter: exp, processor: p}
	p.next = sdktrace.NewBatchSpanProcessor(guarded, sdktrace.WithMaxQueueSize(512), sdktrace.WithMaxExportBatchSize(64), sdktrace.WithBatchTimeout(time.Second), sdktrace.WithExportTimeout(time.Duration(cfg.ExportTimeoutSec)*time.Second))
	e.processor = p
	e.provider = sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRate))),
		sdktrace.WithSpanProcessor(p),
		sdktrace.WithResource(resource.NewWithAttributes("", attribute.String("service.name", "new-api"), attribute.String("deployment.environment", cfg.Environment))),
		sdktrace.WithRawSpanLimits(sdktrace.SpanLimits{AttributeValueLengthLimit: MaxContentBytes, AttributeCountLimit: 64, EventCountLimit: 0, LinkCountLimit: 0, AttributePerEventCountLimit: 0, AttributePerLinkCountLimit: 0}),
	)
	e.enabled = true
	return e, nil
}

func (e *Exporter) Shutdown(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.closeOnce.Do(func() {
		if e.enabled {
			e.shutdownErr = e.provider.Shutdown(ctx)
		}
	})
	return e.shutdownErr
}
func (e *Exporter) ShutdownTimeout() time.Duration {
	return time.Duration(e.config.FlushTimeoutSec) * time.Second
}
func (e *Exporter) DropCount() int64 {
	if e == nil || e.processor == nil {
		return 0
	}
	return e.processor.dropped.Load()
}
func (e *Exporter) IsEnabled() bool { return e != nil && e.enabled }
func (e *Exporter) Tracer(name string) trace.Tracer {
	if !e.IsEnabled() {
		return trace.NewNoopTracerProvider().Tracer(name)
	}
	return e.provider.Tracer(name)
}

// Admission counts exporting and queued spans together. Accepted spans cannot
// overflow the SDK queue; excess work is dropped synchronously without blocking.
type boundedProcessor struct {
	next           sdktrace.SpanProcessor
	pending        atomic.Int64
	dropped        atomic.Int64
	failed         atomic.Int64
	failureClass   atomic.Value
	stopped        atomic.Bool
	lastDiagnostic atomic.Int64
}

func (p *boundedProcessor) OnStart(ctx context.Context, s sdktrace.ReadWriteSpan) {
	p.next.OnStart(ctx, s)
}
func (p *boundedProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	if !s.SpanContext().IsSampled() || p.stopped.Load() {
		return
	}
	for {
		n := p.pending.Load()
		if n >= 512 {
			p.dropped.Add(1)
			p.diagnose("queue_full")
			return
		}
		if p.pending.CompareAndSwap(n, n+1) {
			break
		}
	}
	p.next.OnEnd(s)
}
func (p *boundedProcessor) Shutdown(ctx context.Context) error {
	p.stopped.Store(true)
	return p.next.Shutdown(ctx)
}
func (p *boundedProcessor) ForceFlush(ctx context.Context) error { return p.next.ForceFlush(ctx) }
func (p *boundedProcessor) diagnose(category string) {
	now := time.Now().Unix()
	prev := p.lastDiagnostic.Load()
	if now-prev >= 60 && p.lastDiagnostic.CompareAndSwap(prev, now) {
		common.SysError("langfuse telemetry: " + category)
	}
}

type diagnosticExporter struct {
	sdktrace.SpanExporter
	processor *boundedProcessor
}

func (e *diagnosticExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	defer e.processor.pending.Add(-int64(len(spans)))
	if err := e.SpanExporter.ExportSpans(ctx, spans); err != nil {
		e.processor.failed.Add(int64(len(spans)))
		// Raw SDK errors may contain the endpoint or remote response body.
		e.processor.diagnose("export_failed")
	}
	// Do not send raw exporter errors to the process-global OTel error handler.
	return nil
}

// FailureCount counts spans whose bounded export failed; it is not proof of Cloud ingestion.
func (e *Exporter) FailureCount() int64 {
	if e == nil || e.processor == nil {
		return 0
	}
	return e.processor.failed.Load()
}
func (e *Exporter) LastFailureClass() string {
	if e == nil || e.processor == nil {
		return ""
	}
	value := e.processor.failureClass.Load()
	if value == nil {
		return ""
	}
	return value.(string)
}

type ingestionTransport struct {
	next      http.RoundTripper
	processor *boundedProcessor
}

func (t *ingestionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(r)
	category := ""
	if err != nil {
		category = "transport_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			category = "timeout"
		}
	} else if response != nil {
		switch {
		case response.StatusCode == 401 || response.StatusCode == 403:
			category = "authentication_failed"
		case response.StatusCode == 429:
			category = "rate_limited"
		case response.StatusCode >= 500:
			category = "server_error"
		case response.StatusCode >= 300:
			category = "rejected"
		}
	}
	if category != "" {
		t.processor.failureClass.Store(category)
		t.processor.diagnose(category)
	}
	return response, err
}
