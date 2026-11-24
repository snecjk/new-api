package observability

import (
	"strings"
	"unicode/utf8"
)

// TextCapture 是受限的文本输入/输出收集器，仅在 enabled+sampled+capture_content 时启用。
// 收集器对 UTF-8 做安全截断，达到上限后停止收集并保持截断标记。
type TextCapture struct {
	enabled      bool
	buf          strings.Builder
	maxBytes     int
	truncated    bool
	encodedBytes int
}

// NewTextCapture 创建收集器。若未启用内容捕获（maxBytes <= 0），返回 nil。
func NewTextCapture(enabled bool, maxBytes int) *TextCapture {
	if !enabled || maxBytes <= 0 {
		return nil
	}
	return &TextCapture{enabled: true, maxBytes: maxBytes}
}

// Append 追加一段文本。达到上限后停止收集并保持截断标记。
func (c *TextCapture) Append(s string) {
	if c == nil || !c.enabled || c.truncated {
		return
	}

	for _, r := range s {
		size := utf8.RuneLen(r)
		encoded := size
		switch r {
		case '"', '\\', '\n', '\r', '\t', '\b', '\f':
			encoded = 2
		case '<', '>', '&', '\u2028', '\u2029':
			encoded = 6
		default:
			if r < 32 {
				encoded = 6
			}
		}
		if c.buf.Len()+size > c.maxBytes || c.encodedBytes+encoded+2 > c.maxBytes {
			c.truncated = true
			return
		}
		c.buf.WriteRune(r)
		c.encodedBytes += encoded
	}

}

// Text 返回已收集的文本（UTF-8 有效）。
func (c *TextCapture) Text() string {
	if c == nil || !c.enabled {
		return ""
	}
	return c.buf.String()
}

// Truncated 返回是否发生过截断。
func (c *TextCapture) Truncated() bool {
	if c == nil || !c.enabled {
		return false
	}
	return c.truncated
}
