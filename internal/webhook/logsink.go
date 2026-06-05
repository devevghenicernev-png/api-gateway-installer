package webhook

import (
	"bytes"
	"encoding/json"
	"io"
	"time"
)

// publishingSink is an io.Writer that splits incoming bytes on '\n' and
// publishes each line as an event on `deploy.<name>.stdout`.
//
// Build commands (npm/pip/go) write line-by-line; piping their stdout
// through this sink turns "deploy build" into a live log stream visible in
// the dashboard with sub-100 ms latency.
//
// Lines longer than 64 KiB are split — saves us from a runaway build
// (linker dumping a backtrace) OOMing the dashboard's DOM.
type publishingSink struct {
	publish func(topic, evType string, data []byte)
	deploy  string
	buf     bytes.Buffer
}

func newPublishingSink(publish func(topic, evType string, data []byte), deploy string) io.Writer {
	if publish == nil {
		return io.Discard
	}
	return &publishingSink{publish: publish, deploy: deploy}
}

// Write splits p on newlines and emits one event per line. Trailing partial
// line is held in buf until the next Write (or Close — we don't bother).
//
// Returns n == len(p) always (the sink never short-writes); the publish
// step is best-effort, so a slow subscriber doesn't block the build.
func (s *publishingSink) Write(p []byte) (int, error) {
	const maxLine = 64 * 1024
	s.buf.Write(p)
	for {
		idx := bytes.IndexByte(s.buf.Bytes(), '\n')
		if idx < 0 {
			if s.buf.Len() > maxLine {
				// Flush an oversized "line" to keep memory bounded.
				line := make([]byte, maxLine)
				_, _ = s.buf.Read(line)
				s.emit(line)
			}
			break
		}
		line := make([]byte, idx)
		_, _ = s.buf.Read(line)
		_, _ = s.buf.ReadByte() // consume the newline
		s.emit(line)
	}
	return len(p), nil
}

func (s *publishingSink) emit(line []byte) {
	payload, _ := json.Marshal(map[string]any{
		"deploy": s.deploy,
		"line":   string(line),
		"stream": "stdout",
		"ts":     time.Now().Unix(),
	})
	s.publish("deploy."+s.deploy+".stdout", "stdout", payload)
}
