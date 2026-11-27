package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionRPMTaskRejectionDoesNotRetryAnotherChannel(t *testing.T) {
	apiErr := types.NewErrorWithStatusCode(errors.New("RPM exhausted"), types.ErrorCode("subscription_rpm_limit"), http.StatusTooManyRequests, types.ErrOptionWithSkipRetry())
	taskErr := service.TaskErrorFromAPIError(apiErr)
	require.NotNil(t, taskErr)
	assert.True(t, taskErr.LocalError, "user rate limits must not count as channel failures")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	assert.False(t, shouldRetryTaskRelay(c, 1, taskErr, 3))
	assert.True(t, shouldRetryTaskRelay(c, 1, &taskdto.TaskError{StatusCode: http.StatusTooManyRequests}, 3), "upstream 429 retains normal retry behavior")
}
