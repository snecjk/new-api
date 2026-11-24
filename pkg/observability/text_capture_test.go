package observability

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTextCaptureDisabled(t *testing.T) {
	c := NewTextCapture(false, 8192)
	assert.Nil(t, c)
}

func TestTextCaptureZeroMax(t *testing.T) {
	c := NewTextCapture(true, 0)
	assert.Nil(t, c)
}

func TestTextCaptureAppend(t *testing.T) {
	c := NewTextCapture(true, 1024)
	c.Append("hello")
	c.Append(" world")
	assert.Equal(t, "hello world", c.Text())
	assert.False(t, c.Truncated())
}

func TestTextCaptureTruncation(t *testing.T) {
	c := NewTextCapture(true, 10)
	c.Append("hello")
	c.Append(" world")
	assert.Equal(t, "hello wo", c.Text())
	assert.True(t, c.Truncated())
	// 截断后不再追加
	c.Append("!!")
	assert.Equal(t, "hello wo", c.Text())
}

func TestTextCaptureUTF8Boundary(t *testing.T) {
	c := NewTextCapture(true, 5)
	// "你好" 是 6 字节，限制 5 字节应截断到第一个字符
	c.Append("你好世界")
	assert.Equal(t, "你", c.Text())
	assert.True(t, c.Truncated())
}

func TestTextCaptureEscapedBudget(t *testing.T) {
	c := NewTextCapture(true, 14)
	c.Append("<>&你好")
	assert.Equal(t, "<>", c.Text())
	assert.True(t, c.Truncated())
}

func TestTextCaptureOneMiB(t *testing.T) {
	for _, tc := range []struct {
		name, text string
	}{
		{"ascii", strings.Repeat("x", 1048574)},
		{"utf8", strings.Repeat("你", 349524)},
		{"escaped", strings.Repeat("<", 174762)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewTextCapture(true, 1048576)
			c.Append(tc.text)
			assert.False(t, c.Truncated())
			assert.Equal(t, tc.text, c.Text())
			c.Append("你好")
			assert.True(t, c.Truncated())
			assert.Equal(t, tc.text, c.Text())
			assert.True(t, utf8.ValidString(c.Text()))
			encoded, err := common.Marshal(c.Text())
			require.NoError(t, err)
			assert.LessOrEqual(t, len(encoded), 1048576)
		})
	}
}

func TestTextCaptureLongContextBudget(t *testing.T) {
	const limit = 32 << 20
	// Each pair is four UTF-8 bytes, but nine bytes after JSON escaping.
	text := strings.Repeat("<你", (limit-2)/9)
	c := NewTextCapture(true, limit)
	c.Append(text)
	require.False(t, c.Truncated())
	require.True(t, c.Text() == text)
	c.Append("你好")
	assert.True(t, c.Truncated())
	assert.True(t, c.Text() == text)
	assert.True(t, utf8.ValidString(c.Text()))
	encoded, err := common.Marshal(c.Text())
	require.NoError(t, err)
	assert.Len(t, encoded, limit)
}
