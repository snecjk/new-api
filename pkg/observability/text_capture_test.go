package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
