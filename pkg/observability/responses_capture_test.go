package observability

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestResponsesTextProjection(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
		truncated       bool
	}{
		{"simple", "\"hello\"", "hello", false},
		{"unicode escapes", "\"你好\\n\\u4f60\\ud83d\\ude00\"", "你好\n你😀", false},
		{"message blocks", "[{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"hello\"},{\"type\":\"input_image\",\"image_url\":\"SECRET_MEDIA\"}]},{\"type\":\"function_call\",\"arguments\":\"SECRET_ARGS\"},{\"type\":\"compaction\",\"encrypted_content\":\"SECRET_OPAQUE\"}]", "hello", false},
		{"large text", "\"" + strings.Repeat("你", 10000) + "\"", strings.Repeat("你", 84), true},
		{"large skipped media", "[{\"role\":\"user\",\"content\":[{\"type\":\"input_image\",\"image_url\":\"" + strings.Repeat("SECRET_MEDIA", 1000) + "\"},{\"type\":\"input_text\",\"text\":\"safe\"}]}]", "safe", false},
		{"escaped bounds", "\"" + strings.Repeat("\\n", 1000) + "\"", strings.Repeat("\n", 127), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Attempt{input: NewTextCapture(true, 256)}
			a.captureResponsesInput([]byte(tc.raw))
			assert.Equal(t, tc.want, a.input.Text())
			assert.Equal(t, tc.truncated, a.input.Truncated())
			assert.True(t, utf8.ValidString(a.input.Text()))
			encoded, err := common.Marshal(a.input.Text())
			require.NoError(t, err)
			assert.LessOrEqual(t, len(encoded), 256)
			assert.NotContains(t, a.input.Text(), "SECRET_")
		})
	}
}
