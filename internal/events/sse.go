package events

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// HeartbeatInterval is the cadence of the `:keepalive` comment we emit so
// idle proxies don't time out the connection.
//
// Defaults beat the harshest envelope: nginx (60s), Cloudflare (100s).
// 15s gives 4-6× margin; lower has measurable CPU cost on Pi.
const HeartbeatInterval = 15 * time.Second

// SSEHandler returns an http.HandlerFunc that subscribes to `topics` (from
// query params) and streams events to the client per WHATWG SSE.
//
// Query params:
//   - topic=foo&topic=bar — repeated, OR
//   - topics=foo,bar      — comma-separated
//
// Headers:
//   - Last-Event-ID: <n> — replay everything with ID > n from the ring buffer
//
// Wire format (one event):
//
//	id: 4218\n
//	event: stdout\n
//	data: {"line":"npm install"}\n\n
//
// Comments (heartbeats) are `: keepalive\n\n` — ignored by EventSource.
func (h *Hub) SSEHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		// SSE headers per spec. X-Accel-Buffering=no disables nginx response
		// buffering (the classic gotcha — see ARCHITECTURE.md §"Gotchas" #1).
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		topics := extractTopics(r)
		if len(topics) == 0 {
			// No topics → subscribe to nothing, just heartbeat. Useful to
			// keep the connection warm before subscribing.
			topics = []string{}
		}

		var sinceID uint64
		if v := r.Header.Get("Last-Event-ID"); v != "" {
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				sinceID = n
			}
		}

		sub, cancel := h.Subscribe(topics, sinceID)
		defer cancel()

		// Tell EventSource: reconnect after 3s on errors. Spec default is 3s
		// already, but stating it explicitly avoids ambiguity and is a clear
		// signal to operators inspecting the wire with curl.
		if _, err := io.WriteString(w, "retry: 3000\n\n"); err != nil {
			return
		}
		flusher.Flush()

		ticker := time.NewTicker(HeartbeatInterval)
		defer ticker.Stop()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return

			case <-ticker.C:
				if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
					return
				}
				flusher.Flush()

			case e, ok := <-sub.Ch():
				if !ok {
					// Hub disconnected us (drop threshold or shutdown).
					// Send a synthetic event so the client knows to reconnect
					// with its Last-Event-ID rather than silently stalling.
					_, _ = io.WriteString(w, "event: __reconnect\ndata: {\"reason\":\"drops_exceeded\"}\n\n")
					flusher.Flush()
					return
				}
				if err := writeEvent(w, e); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

// writeEvent emits one SSE message. We do this manually instead of pulling
// in github.com/r3labs/sse — the encoding is 6 lines and the dep is heavy.
func writeEvent(w io.Writer, e Event) error {
	// Type is optional; default would be "message". We always emit it so
	// clients can `es.addEventListener("stdout", ...)` cleanly.
	typ := e.Type
	if typ == "" {
		typ = "message"
	}
	_, err := fmt.Fprintf(w,
		"id: %d\nevent: %s\ndata: %s\n\n",
		e.ID, typ, e.Data,
	)
	return err
}

// extractTopics collects "topic" repeated params and "topics" comma-list.
func extractTopics(r *http.Request) []string {
	q := r.URL.Query()
	out := append([]string{}, q["topic"]...)
	if v := q.Get("topics"); v != "" {
		for _, s := range splitComma(v) {
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func splitComma(s string) []string {
	out := []string{}
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[last:i])
			last = i + 1
		}
	}
	out = append(out, s[last:])
	return out
}
