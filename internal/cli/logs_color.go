package cli

import (
	"bytes"
	"io"
)

const (
	logColorReset  = "\x1b[0m"
	logColorBlue   = "\x1b[1;34m"
	logColorCyan   = "\x1b[36m"
	logColorGreen  = "\x1b[32m"
	logColorRed    = "\x1b[31m"
	logColorYellow = "\x1b[33m"
	logColorDim    = "\x1b[2m"
)

// coloredLogWriter colors complete status lines without changing their text.
// The parsers can write one line in several calls, so it buffers until '\n'.
type coloredLogWriter struct {
	out     io.Writer
	pending []byte
}

func newColoredLogWriter(out io.Writer) *coloredLogWriter {
	return &coloredLogWriter{out: out}
}

func (w *coloredLogWriter) Write(p []byte) (int, error) {
	written := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			w.pending = append(w.pending, p...)
			break
		}
		w.pending = append(w.pending, p[:end+1]...)
		if err := w.writeLine(); err != nil {
			return 0, err
		}
		p = p[end+1:]
	}
	return written, nil
}

func (w *coloredLogWriter) writeLine() error {
	line := bytes.TrimSuffix(w.pending, []byte{'\n'})
	color := logLineColor(line)
	if color != "" {
		if _, err := io.WriteString(w.out, color); err != nil {
			return err
		}
	}
	if _, err := w.out.Write(line); err != nil {
		return err
	}
	if color != "" {
		if _, err := io.WriteString(w.out, logColorReset); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w.out, "\n")
	w.pending = w.pending[:0]
	return err
}

func logLineColor(line []byte) string {
	switch {
	case bytes.HasPrefix(line, []byte("--- Turn ")):
		return logColorBlue
	case bytes.HasPrefix(line, []byte("[error]")), bytes.HasPrefix(line, []byte("[result] error")):
		return logColorRed
	case bytes.HasPrefix(line, []byte("[result] incomplete")):
		return logColorYellow
	case bytes.HasPrefix(line, []byte("[result] completed")):
		return logColorGreen
	case bytes.HasPrefix(line, []byte("[usage]")):
		return logColorDim
	case len(line) > 0 && line[0] == '[':
		return logColorCyan
	default:
		return ""
	}
}
