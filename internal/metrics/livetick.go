package metrics

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/events"
)

// LiveTickInterval is how often metrics.tick events fire.
const LiveTickInterval = 1 * time.Second

// LiveTickWindow is the rolling window used for rps / percentile computation.
//
// 60s matches what Prometheus's rate() default scrape window converges on for
// short-term dashboards, so the SSE numbers and a Grafana panel using
// "$__rate_interval" land on the same magnitude even if they aren't
// bit-identical.
const LiveTickWindow = 60 * time.Second

// liveSample is one access-log row reduced to the fields we need.
//
// rtime is -1 when the log line didn't carry timing (combined format),
// so the percentile slice can skip it without confusing zero values.
type liveSample struct {
	ts     int64 // unix nanos
	status int
	rtime  float64
}

// LiveTick is the payload published on topic "metrics.tick" once per
// LiveTickInterval. Subscribers (dashboard UI, CLI follow) parse this JSON.
type LiveTick struct {
	Ts       int64   `json:"ts"`       // unix seconds when the tick was computed
	WindowS  int     `json:"window_s"` // rolling window in seconds
	Count    int     `json:"count"`    // samples in the window
	RPS      float64 `json:"rps"`      // requests / second over the window
	ErrRate  float64 `json:"err_rate"` // 5xx / total (0..1)
	P50Ms    float64 `json:"p50_ms"`
	P95Ms    float64 `json:"p95_ms"`
	P99Ms    float64 `json:"p99_ms"`
	TimedCnt int     `json:"timed_count"` // samples that had rtime (subset of Count)
}

// RunLiveTicker subscribes to "nginx.access", parses each line, keeps a
// rolling LiveTickWindow of samples, and every LiveTickInterval publishes
// aggregate {rps, err_rate, p50/p95/p99} on topic "metrics.tick".
//
// No-op stream of zero-count ticks when nginx isn't installed or the access
// log isn't being tailed — subscribers still see "all clear" instead of
// silent disconnects.
//
// Memory bound: ~60 × peak-RPS samples × 32B. At 1k RPS sustained that's
// ~2MB; at 10k RPS, ~20MB. Beyond that, scrape Prometheus instead — this
// channel is for human-facing dashboards, not high-cardinality SLO eval.
func RunLiveTicker(ctx context.Context, hub *events.Hub, logger *slog.Logger) {
	sub, cancel := hub.Subscribe([]string{"nginx.access"}, 0)
	defer cancel()

	var (
		mu      sync.Mutex
		samples = make([]liveSample, 0, 4096)
	)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-sub.Ch():
				if !ok {
					return
				}
				s, parsed := parseAccessEvent(e.Data)
				if !parsed {
					continue
				}
				mu.Lock()
				samples = append(samples, s)
				mu.Unlock()
			}
		}
	}()

	tick := time.NewTicker(LiveTickInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			cutoff := time.Now().UnixNano() - LiveTickWindow.Nanoseconds()

			mu.Lock()
			idx := 0
			for ; idx < len(samples); idx++ {
				if samples[idx].ts > cutoff {
					break
				}
			}
			if idx > 0 {
				samples = append(samples[:0], samples[idx:]...)
			}
			snap := make([]liveSample, len(samples))
			copy(snap, samples)
			mu.Unlock()

			payload := computeLive(snap)
			body, err := json.Marshal(payload)
			if err != nil {
				if logger != nil {
					logger.Warn("metrics tick marshal", slog.String("err", err.Error()))
				}
				continue
			}
			hub.Publish("metrics.tick", "metrics", body)
		}
	}
}

// computeLive turns a window of samples into the published LiveTick.
//
// Percentiles use linear interpolation (Type-7 in R's quantile()), matching
// what nginx-vts and prometheus histogram_quantile() converge on for large
// samples — so dashboard numbers line up with Grafana ones.
func computeLive(samples []liveSample) LiveTick {
	out := LiveTick{
		Ts:      time.Now().Unix(),
		WindowS: int(LiveTickWindow / time.Second),
		Count:   len(samples),
	}
	if len(samples) == 0 {
		return out
	}
	out.RPS = float64(len(samples)) / float64(LiveTickWindow/time.Second)

	rt := make([]float64, 0, len(samples))
	errs := 0
	for _, s := range samples {
		if s.status >= 500 {
			errs++
		}
		if s.rtime >= 0 {
			rt = append(rt, s.rtime)
		}
	}
	out.ErrRate = float64(errs) / float64(len(samples))
	out.TimedCnt = len(rt)
	if len(rt) > 0 {
		sort.Float64s(rt)
		out.P50Ms = percentile(rt, 0.50) * 1000
		out.P95Ms = percentile(rt, 0.95) * 1000
		out.P99Ms = percentile(rt, 0.99) * 1000
	}
	return out
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := p * float64(len(sorted)-1)
	lo := int(rank)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := rank - float64(lo)
	return sorted[lo] + (sorted[hi]-sorted[lo])*frac
}

// parseAccessEvent unwraps the envelope written by nginx.TailAccessLog
// ({"line": "...", "ts": <unix>}) and extracts status + request_time.
//
// Recognises two underlying access-log formats:
//   - apigw_json (Logging.Format == "json") — direct JSON; uses status, rtime
//   - nginx combined (the default) — status pulled by regex; rtime unknown,
//     left as -1 so the percentile slice skips it.
//
// Anything else (custom log_format the operator added by hand) returns
// (_, false) — the line is counted nowhere, which is honest.
func parseAccessEvent(data []byte) (liveSample, bool) {
	var outer struct {
		Line string `json:"line"`
		Ts   int64  `json:"ts"`
	}
	if err := json.Unmarshal(data, &outer); err != nil {
		return liveSample{}, false
	}
	sample := liveSample{rtime: -1}
	if outer.Ts > 0 {
		sample.ts = outer.Ts * int64(time.Second)
	} else {
		sample.ts = time.Now().UnixNano()
	}
	line := strings.TrimSpace(outer.Line)
	if line == "" {
		return liveSample{}, false
	}
	if line[0] == '{' {
		var inner struct {
			Status int     `json:"status"`
			Rtime  float64 `json:"rtime"`
		}
		if err := json.Unmarshal([]byte(line), &inner); err == nil && inner.Status > 0 {
			sample.status = inner.Status
			sample.rtime = inner.Rtime
			return sample, true
		}
		return liveSample{}, false
	}
	if st, ok := extractCombinedStatus(line); ok {
		sample.status = st
		return sample, true
	}
	return liveSample{}, false
}

// combinedStatusRE matches the status field in nginx's "combined" log
// format. The pattern requires the closing quote of the request line
// (`"GET /foo HTTP/1.1"`) followed by whitespace + a 3-digit 1xx-5xx code,
// which is unambiguous even when the URL or user-agent contains digits.
var combinedStatusRE = regexp.MustCompile(`"\s+([1-5][0-9]{2})\b`)

func extractCombinedStatus(line string) (int, bool) {
	m := combinedStatusRE.FindStringSubmatch(line)
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}
