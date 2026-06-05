package events

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a minimal SSE consumer for CLI commands. The hub's SSE handler
// is the server; this is the matching client. <50 LoC because EventSource's
// wire format is dead simple — we only need the `id:`/`event:`/`data:` lines.
//
// Usage:
//
//	c := events.NewClient("http://127.0.0.1:9080/events?topic=deploy.foo.stdout")
//	c.OnEvent = func(e events.Event) { fmt.Println(string(e.Data)) }
//	c.Run(ctx) // blocks until ctx cancelled or unrecoverable error
type Client struct {
	URL     string
	OnEvent func(Event)

	// LastEventID is updated as messages arrive. On reconnect we send it
	// back via the Last-Event-ID header so the hub replays the gap.
	LastEventID string

	HTTP *http.Client
}

// NewClient constructs a Client with a sensible default HTTP client (no
// timeout — SSE connections are intentionally long-lived).
func NewClient(url string) *Client {
	return &Client{
		URL:  url,
		HTTP: &http.Client{Timeout: 0},
	}
}

// Run blocks until ctx is cancelled. Reconnects on transient errors with
// exponential backoff capped at 30 s. Returns a non-nil error only on
// permanent failures (HTTP 4xx other than 404).
func (c *Client) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := c.connectOnce(ctx)
		if err != nil && !isTransient(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func (c *Client) connectOnce(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.LastEventID != "" {
		req.Header.Set("Last-Event-ID", c.LastEventID)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 408 {
		return fmt.Errorf("sse %s: %s", c.URL, resp.Status)
	}
	return c.parse(resp.Body)
}

// parse reads SSE wire format: groups of `field: value` lines terminated by
// a blank line. We honour the four field names the hub emits: id, event,
// data, retry. Comments (`:` prefix) are heartbeats we silently drop.
func (c *Client) parse(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var (
		curID   string
		curType string
		curData strings.Builder
	)
	flush := func() {
		if curType == "__reconnect" {
			curID, curType, curData = "", "", strings.Builder{}
			return
		}
		if c.OnEvent != nil && (curData.Len() > 0 || curType != "") {
			c.OnEvent(Event{
				Type: curType,
				Data: []byte(curData.String()),
			})
		}
		if curID != "" {
			c.LastEventID = curID
		}
		curID, curType, curData = "", "", strings.Builder{}
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / heartbeat
		}
		field, value, ok := splitColon(line)
		if !ok {
			continue
		}
		switch field {
		case "id":
			curID = value
		case "event":
			curType = value
		case "data":
			if curData.Len() > 0 {
				curData.WriteByte('\n')
			}
			curData.WriteString(value)
		case "retry":
			// Ignored — we use a fixed backoff.
		}
	}
	return scanner.Err()
}

func splitColon(s string) (string, string, bool) {
	idx := strings.IndexByte(s, ':')
	if idx < 0 {
		return s, "", true
	}
	v := strings.TrimPrefix(s[idx+1:], " ")
	return s[:idx], v, true
}

func isTransient(err error) bool {
	if err == nil {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "EOF") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "reset by peer") ||
		strings.Contains(s, "i/o timeout")
}
