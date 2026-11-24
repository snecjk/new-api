package observability

import (
	"bytes"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// Responses stores input as RawMessage. Project known textual fields directly
// from slices so large media, tool arguments and long strings are never decoded
// into another unbounded object just for tracing. The enclosing request has
// already been JSON-validated by the relay.
func (a *Attempt) captureResponsesInput(raw []byte) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return
	}
	if raw[0] == '"' {
		a.input.appendJSONText(raw)
		return
	}
	if raw[0] != '[' {
		a.skipped = true
		return
	}
	for item := range rawValues(raw) {
		if a.input.Truncated() {
			return
		}
		kind := rawField(item, "type")
		role := rawField(item, "role")
		if (len(kind) == 0 || bytes.Equal(kind, []byte("\"message\""))) && (bytes.Equal(role, []byte("\"user\"")) || bytes.Equal(role, []byte("\"assistant\"")) || bytes.Equal(role, []byte("\"system\"")) || bytes.Equal(role, []byte("\"developer\""))) {
			content := rawField(item, "content")
			if len(content) == 0 {
				continue
			}
			if content[0] == '"' {
				a.input.appendJSONText(content)
				continue
			}
			if content[0] != '[' {
				a.skipped = true
				continue
			}
			for block := range rawValues(content) {
				kind := rawField(block, "type")
				if bytes.Equal(kind, []byte("\"input_text\"")) || bytes.Equal(kind, []byte("\"output_text\"")) || bytes.Equal(kind, []byte("\"text\"")) {
					a.input.appendJSONText(rawField(block, "text"))
				} else {
					a.skipped = true
				}
				if a.input.Truncated() {
					return
				}
			}
		} else {
			a.skipped = true
		}
	}
}

// rawValues yields one array value at a time without retaining all elements.
func rawValues(raw []byte) func(func([]byte) bool) {
	return func(yield func([]byte) bool) {
		if len(raw) == 0 || raw[0] != '[' {
			return
		}
		for pos := 1; pos < len(raw); {
			for pos < len(raw) && (raw[pos] == ' ' || raw[pos] == '\n' || raw[pos] == '\r' || raw[pos] == '\t' || raw[pos] == ',') {
				pos++
			}
			if pos >= len(raw) || raw[pos] == ']' {
				return
			}
			end := rawValueEnd(raw, pos)
			if end <= pos {
				return
			}
			if !yield(raw[pos:end]) {
				return
			}
			pos = end
		}
	}
}

func rawField(raw []byte, key string) []byte {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	for pos := 1; pos < len(raw); {
		for pos < len(raw) && (raw[pos] == ' ' || raw[pos] == '\n' || raw[pos] == '\r' || raw[pos] == '\t' || raw[pos] == ',') {
			pos++
		}
		if pos >= len(raw) || raw[pos] != '"' {
			return nil
		}
		keyEnd := rawValueEnd(raw, pos)
		if keyEnd <= pos {
			return nil
		}
		match := bytes.Equal(raw[pos:keyEnd], []byte("\""+key+"\""))
		pos = keyEnd
		for pos < len(raw) && (raw[pos] == ' ' || raw[pos] == '\n' || raw[pos] == '\r' || raw[pos] == '\t' || raw[pos] == ':') {
			pos++
		}
		end := rawValueEnd(raw, pos)
		if end <= pos {
			return nil
		}
		if match {
			return raw[pos:end]
		}
		pos = end
	}
	return nil
}

// rawValueEnd skips nested arrays/objects and escaped strings in constant space.
func rawValueEnd(raw []byte, pos int) int {
	if pos >= len(raw) {
		return pos
	}
	depth := 0
	quoted := false
	for i := pos; i < len(raw); i++ {
		b := raw[i]
		if quoted {
			if b == '\\' {
				i++
				continue
			}
			if b == '"' {
				quoted = false
				if depth == 0 {
					return i + 1
				}
			}
			continue
		}
		switch b {
		case '"':
			quoted = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth <= 0 {
				return i + 1
			}
		case ',', ':', ' ', '\n', '\r', '\t':
			if depth == 0 {
				return i
			}
		}
	}
	return len(raw)
}

func (c *TextCapture) appendJSONText(raw []byte) {
	if c == nil || len(raw) < 2 || raw[0] != '"' {
		return
	}
	for i := 1; i < len(raw)-1 && !c.Truncated(); {
		if raw[i] != '\\' {
			r, size := utf8.DecodeRune(raw[i:])
			c.Append(string(r))
			i += size
			continue
		}
		i++
		if i >= len(raw)-1 {
			return
		}
		var r rune
		switch raw[i] {
		case '"', '\\', '/':
			r = rune(raw[i])
			i++
		case 'b':
			r = '\b'
			i++
		case 'f':
			r = '\f'
			i++
		case 'n':
			r = '\n'
			i++
		case 'r':
			r = '\r'
			i++
		case 't':
			r = '\t'
			i++
		case 'u':
			if i+5 > len(raw) {
				return
			}
			value, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
			if err != nil {
				return
			}
			r = rune(value)
			i += 5
			if utf16.IsSurrogate(r) && i+6 <= len(raw) && raw[i] == '\\' && raw[i+1] == 'u' {
				low, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
				if err == nil {
					r = utf16.DecodeRune(r, rune(low))
					i += 6
				}
			}
			if utf16.IsSurrogate(r) {
				r = utf8.RuneError
			}
		default:
			return
		}
		c.Append(string(r))
	}
}
