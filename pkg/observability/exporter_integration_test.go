package observability

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestOTLPWireContractAndCancelledRequest(t *testing.T) {
	cfg := validConfig()
	type captured struct {
		path                   string
		auth, version, content bool
		body                   []byte
	}
	requests := make(chan captured, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		requests <- captured{r.URL.Path, r.Header.Get("Authorization") == cfg.BasicAuth(), r.Header.Get("x-langfuse-ingestion-version") == "4", r.Header.Get("Content-Type") == "application/x-protobuf", body}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer server.Close()
	cfg.BaseURL = server.URL
	original := otel.GetTracerProvider()
	exp, err := newExporter(cfg, server.Client())
	require.NoError(t, err)
	assert.Equal(t, original, otel.GetTracerProvider(), "must not replace global provider")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	ctx, root := StartRootSpan(exp.Tracer("test"), cancelled, RootSpanStart{RequestID: "wire-request", Environment: "test"})
	a := BeginAttempt(ctx, exp.Tracer("test"), GenSpanStart{UpstreamModel: "model"}, nil)
	require.NotNil(t, a)
	a.End(false, "", false, nil)
	a.EndRoot(root)
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	require.NoError(t, exp.Shutdown(shutdown))
	require.NoError(t, exp.Shutdown(shutdown))
	var got captured
	select {
	case got = <-requests:
	case <-shutdown.Done():
		t.Fatal("OTLP request missing")
	}
	assert.Equal(t, IngestionPath, got.path)
	assert.True(t, got.auth, "Basic auth mismatch")
	assert.True(t, got.version)
	assert.True(t, got.content)
	var payload collector.ExportTraceServiceRequest
	require.NoError(t, proto.Unmarshal(got.body, &payload))
	require.Len(t, payload.ResourceSpans, 1)
	spans := payload.ResourceSpans[0].ScopeSpans[0].Spans
	require.Len(t, spans, 2)
	assert.Len(t, spans[0].TraceId, 16)
	assert.Len(t, spans[0].SpanId, 8)
	assert.Equal(t, spans[0].TraceId, spans[1].TraceId)
	assert.Equal(t, spans[0].ParentSpanId, spans[1].SpanId)
}

func TestOTLPRejectsCredentialRedirect(t *testing.T) {
	var destinationCalls atomic.Int64
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", destination.URL+"/stolen")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	cfg := validConfig()
	cfg.BaseURL = source.URL
	// Use the same redirect policy as the production client (even a same-host
	// redirect is rejected, so Basic credentials cannot reach another endpoint).
	client := source.Client()

	exp, err := newExporter(cfg, client)
	require.NoError(t, err)
	_, root := StartRootSpan(exp.Tracer("test"), context.Background(), RootSpanStart{})
	root.End(OutcomeOk, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, exp.Shutdown(ctx))
	assert.Zero(t, destinationCalls.Load())
}

func TestOTLPFailuresBoundedAndSanitized(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = w.Write([]byte("SECRET_ERROR_BODY"))
			}))
			defer server.Close()
			cfg := validConfig()
			cfg.BaseURL = server.URL
			cfg.ExportTimeoutSec = 1
			exp, err := newExporter(cfg, server.Client())
			require.NoError(t, err)
			_, root := StartRootSpan(exp.Tracer("test"), context.Background(), RootSpanStart{})
			root.End(OutcomeOk, nil)
			// Shutdown has an independent deadline even while ingestion is failing.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err = exp.Shutdown(ctx)
			if err != nil {
				assert.ErrorIs(t, err, context.DeadlineExceeded)
			}
			assert.GreaterOrEqual(t, exp.FailureCount(), int64(1))
			if status == 401 || status == 403 {
				assert.Equal(t, "authentication_failed", exp.LastFailureClass())
			}
			if status == 401 || status == 403 {
				assert.EqualValues(t, 1, calls.Load(), "authentication errors cannot retry")
			} else {
				assert.GreaterOrEqual(t, calls.Load(), int64(1))
			}
		})
	}
}

type blockedExporter struct {
	entered chan struct{}
	release chan struct{}
	once    atomic.Bool
}

func (e *blockedExporter) ExportSpans(ctx context.Context, _ []sdktrace.ReadOnlySpan) error {
	if e.once.CompareAndSwap(false, true) {
		close(e.entered)
	}
	select {
	case <-e.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (*blockedExporter) Shutdown(context.Context) error { return nil }

func TestQueueAdmissionDropsWithoutBlocking(t *testing.T) {
	sink := &blockedExporter{entered: make(chan struct{}), release: make(chan struct{})}
	p := &boundedProcessor{}
	p.next = sdktrace.NewBatchSpanProcessor(&diagnosticExporter{SpanExporter: sink, processor: p}, sdktrace.WithMaxQueueSize(512), sdktrace.WithMaxExportBatchSize(1))
	span := tracetest.SpanStub{SpanContext: trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled}), Attributes: []attribute.KeyValue{attribute.String("test", "bounded")}}.Snapshot()
	p.OnEnd(span)
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("export did not start")
	}
	for i := 1; i < 513; i++ {
		p.OnEnd(span)
	}
	assert.EqualValues(t, 1, p.dropped.Load())
	assert.EqualValues(t, 512, p.pending.Load())
	close(sink.release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, p.Shutdown(ctx))
	assert.Zero(t, p.pending.Load())
}

func TestDisabledExporterMakesNoNetworkRequests(t *testing.T) {
	var calls atomic.Int64
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("SECRET_NETWORK_ERROR")
	})
	cfg := validConfig()
	cfg.Enabled = false
	exp, err := newExporter(cfg, &http.Client{Transport: transport})
	require.NoError(t, err)
	_, root := StartRootSpan(exp.Tracer("test"), context.Background(), RootSpanStart{})
	root.End(OutcomeOk, nil)
	require.NoError(t, exp.Shutdown(context.Background()))
	assert.Zero(t, calls.Load())
}

func TestShutdownHonorsIndependentDeadline(t *testing.T) {
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if first.CompareAndSwap(false, true) {
			close(entered)
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		select {
		case <-returned:
		default:
			close(returned)
		}
		return nil, errors.New("fixture interrupted")
	})}
	cfg := validConfig()
	cfg.ExportTimeoutSec = 30
	exp, err := newExporter(cfg, client)
	require.NoError(t, err)
	for range 64 {
		_, root := StartRootSpan(exp.Tracer("test"), context.Background(), RootSpanStart{})
		root.End(OutcomeOk, nil)
	}
	wait, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWait()
	select {
	case <-entered:
	case <-wait.Done():
		t.Fatal("export did not start")
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	assert.ErrorIs(t, exp.Shutdown(ctx), context.DeadlineExceeded)
	close(release)
	select {
	case <-returned:
	case <-wait.Done():
		t.Fatal("transport did not exit")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExporterTransportTimeout(t *testing.T) {
	cfg := validConfig()
	cfg.ExportTimeoutSec = 1
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}
	exp, err := newExporter(cfg, client)
	require.NoError(t, err)
	_, root := StartRootSpan(exp.Tracer("test"), context.Background(), RootSpanStart{})
	root.End(OutcomeOk, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, exp.Shutdown(ctx))
	assert.EqualValues(t, 1, exp.FailureCount())
	assert.Equal(t, "timeout", exp.LastFailureClass())
}

func TestSpanAttributeLimits(t *testing.T) {
	cfg := validConfig()
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	exp, err := newExporter(cfg, client)
	require.NoError(t, err)
	recorder := tracetest.NewSpanRecorder()
	exp.provider.RegisterSpanProcessor(recorder)
	_, span := exp.Tracer("test").Start(context.Background(), "limits")
	span.SetAttributes(attribute.String("oversize", strings.Repeat("x", MaxContentBytes+1)))
	for i := 0; i < 80; i++ {
		span.SetAttributes(attribute.Int(strconv.Itoa(i), i))
	}
	span.End()
	ended := recorder.Ended()
	require.Len(t, ended, 1)
	assert.LessOrEqual(t, len(ended[0].Attributes()), 64)
	for _, a := range ended[0].Attributes() {
		if a.Key == "oversize" {
			assert.Len(t, a.Value.AsString(), MaxContentBytes)
		}
	}
	require.NoError(t, exp.Shutdown(context.Background()))
}

func TestDiagnosticsAreSanitizedAndRateLimited(t *testing.T) {
	var logs bytes.Buffer
	common.LogWriterMu.Lock()
	original := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logs
	common.LogWriterMu.Unlock()
	defer func() { common.LogWriterMu.Lock(); gin.DefaultErrorWriter = original; common.LogWriterMu.Unlock() }()
	cfg := validConfig()
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("SECRET_ERROR_BODY"))}, nil
	})}
	exp, err := newExporter(cfg, client)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, root := StartRootSpan(exp.Tracer("test"), context.Background(), RootSpanStart{})
		root.End(OutcomeOk, nil)
		require.NoError(t, exp.provider.ForceFlush(context.Background()))
	}
	require.NoError(t, exp.Shutdown(context.Background()))
	common.LogWriterMu.Lock()
	output := logs.String()
	common.LogWriterMu.Unlock()
	assert.Equal(t, 1, strings.Count(output, "langfuse telemetry:"))
	assert.Contains(t, output, "authentication_failed")
	assert.NotContains(t, output, "SECRET_ERROR_BODY")
	assert.NotContains(t, output, cfg.SecretKey)
	assert.NotContains(t, output, cfg.BasicAuth())
	assert.EqualValues(t, 2, exp.FailureCount())
}

func TestOTLPRetryReusesCompletedSpanIdentity(t *testing.T) {
	for _, status := range []int{429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int64
			payloads := make(chan []byte, 2)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				payloads <- body
				if calls.Add(1) == 1 {
					w.WriteHeader(status)
					return
				}
				w.Header().Set("Content-Type", "application/x-protobuf")
			}))
			defer server.Close()
			cfg := validConfig()
			cfg.BaseURL = server.URL
			exp, err := newExporter(cfg, server.Client())
			require.NoError(t, err)
			_, root := StartRootSpan(exp.Tracer("test"), context.Background(), RootSpanStart{})
			root.End(OutcomeOk, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			require.NoError(t, exp.Shutdown(ctx))
			assert.EqualValues(t, 2, calls.Load())
			assert.Equal(t, <-payloads, <-payloads)
			assert.Zero(t, exp.FailureCount())
		})
	}
}
