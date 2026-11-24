package observability_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/pkg/observability"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("fixture panic") }
func (panicReader) Close() error             { return nil }

func TestControllerValidationFailureAndPanicEndRoot(t *testing.T) {
	for _, panics := range []bool{false, true} {
		baselineStatus := 0
		baselineBody := ""
		for _, enabled := range []bool{false, true} {
			sink := tracetest.NewInMemoryExporter()
			exp := observability.NewTestExporter(observability.Config{Enabled: enabled, SampleRate: 1, Environment: "test"}, sink)
			observability.SetGlobalExporter(exp)
			writer := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("invalid-json"))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(common.RequestIdKey, "validation-fixture")
			if panics {
				c.Request.Body = panicReader{}
				require.Panics(t, func() { controller.Relay(c, types.RelayFormatOpenAI) })
			} else {
				controller.Relay(c, types.RelayFormatOpenAI)
			}
			if !enabled {
				baselineStatus = writer.Code
				baselineBody = writer.Body.String()
			} else {
				assert.Equal(t, baselineStatus, writer.Code)
				assert.Equal(t, baselineBody, writer.Body.String())
				spans := sink.GetSpans()
				require.Len(t, spans, 1, "validation/panic must not invent an upstream call")
				assert.Equal(t, observability.OpRoot, spans[0].Name)
				assert.Equal(t, codes.Error, spans[0].Status.Code)
			}
			require.NoError(t, exp.Shutdown(context.Background()))
			observability.SetGlobalExporter(nil)
		}
	}
}
