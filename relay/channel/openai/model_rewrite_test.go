package openai

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendStreamDataModelRewrite(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })

	chunk := `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1710000000,"model":"glm-5.2-fast-preview","request_id":"dash-123","choices":[{"index":0,"delta":{"content":"hi"}}]}`

	newStreamContext := func() (*gin.Context, *httptest.ResponseRecorder) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		return c, recorder
	}

	t.Run("mapped model is rewritten, other fields and SSE framing preserved", func(t *testing.T) {
		c, recorder := newStreamContext()
		info := &common.RelayInfo{
			ChannelMeta:     &common.ChannelMeta{IsModelMapped: true},
			OriginModelName: "claude-opus-4-8",
		}

		require.NoError(t, sendStreamData(c, info, chunk, false, false))

		body := recorder.Body.String()
		assert.Contains(t, body, "data: ")
		assert.Contains(t, body, `"model":"claude-opus-4-8"`)
		assert.NotContains(t, body, "glm-5.2-fast-preview")
		// map 回写必须保留上游附加字段
		assert.Contains(t, body, `"request_id":"dash-123"`)
	})

	t.Run("unmapped model passes through untouched", func(t *testing.T) {
		c, recorder := newStreamContext()
		info := &common.RelayInfo{}

		require.NoError(t, sendStreamData(c, info, chunk, false, false))

		body := recorder.Body.String()
		assert.Contains(t, body, `"model":"glm-5.2-fast-preview"`)
		// 未映射走原始字节透传
		assert.Contains(t, body, chunk)
	})
}

func TestRewriteClaudeResponseModel(t *testing.T) {
	t.Run("message_start nested model is rewritten when mapped", func(t *testing.T) {
		info := &common.RelayInfo{
			ChannelMeta:     &common.ChannelMeta{IsModelMapped: true},
			OriginModelName: "claude-opus-4-8",
		}
		resp := &dto.ClaudeResponse{Type: "message_start", Message: &dto.ClaudeMediaMessage{Model: "qwen3.8-max"}}

		rewriteClaudeResponseModel(resp, info)

		assert.Equal(t, "claude-opus-4-8", resp.Message.Model)
	})

	t.Run("non message_start events are untouched", func(t *testing.T) {
		info := &common.RelayInfo{
			ChannelMeta:     &common.ChannelMeta{IsModelMapped: true},
			OriginModelName: "claude-opus-4-8",
		}
		resp := &dto.ClaudeResponse{Type: "content_block_delta"}

		rewriteClaudeResponseModel(resp, info)

		assert.Nil(t, resp.Message)
	})

	t.Run("unmapped responses are untouched", func(t *testing.T) {
		resp := &dto.ClaudeResponse{Type: "message_start", Message: &dto.ClaudeMediaMessage{Model: "qwen3.8-max"}}

		rewriteClaudeResponseModel(resp, &common.RelayInfo{})

		assert.Equal(t, "qwen3.8-max", resp.Message.Model)
	})
}
