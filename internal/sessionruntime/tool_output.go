package sessionruntime

import "unicode/utf8"

const (
	maxToolOutputBytes         = 512 * 1024
	toolOutputTruncationMarker = "\n… output truncated …\n"
)

// boundedToolOutput retains complete output up to maxBytes, then keeps a
// bounded prefix and suffix around an explicit truncation marker.
type boundedToolOutput struct {
	maxBytes  int
	head      []byte
	tail      []byte
	truncated bool
}

func newBoundedToolOutput(maxBytes int) *boundedToolOutput {
	return &boundedToolOutput{maxBytes: maxBytes}
}

func (o *boundedToolOutput) WriteString(text string) {
	if text == "" || o.maxBytes <= 0 {
		return
	}
	if o.truncated {
		o.appendTail([]byte(text))
		return
	}
	if len(o.head)+len(text) <= o.maxBytes {
		o.head = append(o.head, text...)
		return
	}

	headLimit, _ := o.limits()
	retained := o.head
	retainedHeadBytes := min(headLimit, len(retained))
	o.head = append([]byte(nil), retained[:retainedHeadBytes]...)
	textHeadBytes := min(headLimit-len(o.head), len(text))
	o.head = append(o.head, text[:textHeadBytes]...)
	o.truncated = true
	o.appendTail(retained[retainedHeadBytes:])
	o.appendTail([]byte(text[textHeadBytes:]))
}

func (o *boundedToolOutput) String() string {
	if !o.truncated {
		return string(o.head)
	}
	head := validUTF8Prefix(o.head)
	tail := validUTF8Suffix(o.tail)
	return string(head) + toolOutputTruncationMarker + string(tail)
}

func (o *boundedToolOutput) limits() (int, int) {
	available := max(0, o.maxBytes-len(toolOutputTruncationMarker))
	head := available / 2
	return head, available - head
}

func (o *boundedToolOutput) appendTail(data []byte) {
	_, tailLimit := o.limits()
	if tailLimit == 0 {
		return
	}
	if len(data) >= tailLimit {
		o.tail = append(o.tail[:0], data[len(data)-tailLimit:]...)
		return
	}
	overflow := len(o.tail) + len(data) - tailLimit
	if overflow > 0 {
		copy(o.tail, o.tail[overflow:])
		o.tail = o.tail[:len(o.tail)-overflow]
	}
	o.tail = append(o.tail, data...)
}

func truncateToolOutput(output string) string {
	if len(output) <= maxToolOutputBytes {
		return output
	}
	retained := newBoundedToolOutput(maxToolOutputBytes)
	retained.WriteString(output)
	return retained.String()
}

func validUTF8Prefix(data []byte) []byte {
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return data
}

func validUTF8Suffix(data []byte) []byte {
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[1:]
	}
	return data
}
