package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/events"
)

func TestExtractCombinedStatus(t *testing.T) {
	cases := []struct {
		name string
		line string
		want int
		ok   bool
	}{
		{
			"basic GET 200",
			`127.0.0.1 - - [08/Jun/2026:10:00:00 +0000] "GET /api/hello HTTP/1.1" 200 12 "-" "curl/8.0"`,
			200, true,
		},
		{
			"500 with longer UA",
			`10.0.0.1 - - [08/Jun/2026:10:00:00 +0000] "POST /x HTTP/1.1" 503 0 "https://x" "Mozilla/5.0 (rv:130) Gecko"`,
			503, true,
		},
		{
			"empty line",
			``,
			0, false,
		},
		{
			"missing status",
			`127.0.0.1 - - [date] "GET / HTTP/1.1"`,
			0, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractCombinedStatus(tc.line)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (got=%d)", ok, tc.ok, got)
			}
			if got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestParseAccessEvent_JSONInner(t *testing.T) {
	inner := `{"ts":"2026-06-08T10:00:00Z","status":204,"rtime":0.012}`
	outer, err := json.Marshal(map[string]any{"line": inner, "ts": int64(1717840000)})
	if err != nil {
		t.Fatal(err)
	}
	s, ok := parseAccessEvent(outer)
	if !ok {
		t.Fatal("parse failed")
	}
	if s.status != 204 {
		t.Fatalf("status = %d, want 204", s.status)
	}
	if math.Abs(s.rtime-0.012) > 1e-9 {
		t.Fatalf("rtime = %v, want 0.012", s.rtime)
	}
}

func TestParseAccessEvent_CombinedInner(t *testing.T) {
	combined := `127.0.0.1 - - [08/Jun/2026:10:00:00 +0000] "GET /api/hello HTTP/1.1" 200 12 "-" "curl/8.0"`
	outer, err := json.Marshal(map[string]any{"line": combined, "ts": int64(1717840000)})
	if err != nil {
		t.Fatal(err)
	}
	s, ok := parseAccessEvent(outer)
	if !ok {
		t.Fatal("parse failed")
	}
	if s.status != 200 {
		t.Fatalf("status = %d, want 200", s.status)
	}
	if s.rtime >= 0 {
		t.Fatalf("rtime should be -1 (unknown) for combined format, got %v", s.rtime)
	}
}

func TestParseAccessEvent_JunkLine(t *testing.T) {
	outer, _ := json.Marshal(map[string]any{"line": "this is not a log line", "ts": 0})
	if _, ok := parseAccessEvent(outer); ok {
		t.Fatal("expected parse to fail on junk line")
	}
}

func TestComputeLive_EmptyWindow(t *testing.T) {
	out := computeLive(nil)
	if out.Count != 0 || out.RPS != 0 || out.ErrRate != 0 {
		t.Fatalf("expected all zero for empty window, got %+v", out)
	}
	if out.WindowS != int(LiveTickWindow/time.Second) {
		t.Fatalf("WindowS = %d, want %d", out.WindowS, int(LiveTickWindow/time.Second))
	}
}

func TestComputeLive_Percentiles(t *testing.T) {
	now := time.Now().UnixNano()
	s := make([]liveSample, 0, 100)
	for i := 1; i <= 100; i++ {
		s = append(s, liveSample{
			ts:     now,
			status: 200,
			rtime:  float64(i) / 1000.0, // 1ms ... 100ms
		})
	}
	// Add 5 hard errors so err_rate = 5/105
	for i := 0; i < 5; i++ {
		s = append(s, liveSample{ts: now, status: 502, rtime: -1})
	}
	out := computeLive(s)
	if out.Count != 105 {
		t.Fatalf("count = %d, want 105", out.Count)
	}
	if out.TimedCnt != 100 {
		t.Fatalf("timed_count = %d, want 100", out.TimedCnt)
	}
	wantErr := 5.0 / 105.0
	if math.Abs(out.ErrRate-wantErr) > 1e-9 {
		t.Fatalf("err_rate = %v, want %v", out.ErrRate, wantErr)
	}
	// 100 samples at 1..100ms; p95 ≈ 95ms, p99 ≈ 99ms with linear interpolation.
	if out.P50Ms < 49 || out.P50Ms > 51 {
		t.Fatalf("p50 = %v, want ~50ms", out.P50Ms)
	}
	if out.P95Ms < 94 || out.P95Ms > 96 {
		t.Fatalf("p95 = %v, want ~95ms", out.P95Ms)
	}
	if out.P99Ms < 98 || out.P99Ms > 100 {
		t.Fatalf("p99 = %v, want ~99ms", out.P99Ms)
	}
}

func TestComputeLive_RPS(t *testing.T) {
	now := time.Now().UnixNano()
	s := make([]liveSample, 600) // 600 samples in 60s = 10 RPS
	for i := range s {
		s[i] = liveSample{ts: now, status: 200, rtime: 0.005}
	}
	out := computeLive(s)
	if math.Abs(out.RPS-10) > 1e-9 {
		t.Fatalf("rps = %v, want 10", out.RPS)
	}
}

// TestRunLiveTicker_EndToEnd publishes a few access events and waits for a
// metrics.tick. Time-sensitive but the ticker fires every 1s, so 2.5s is
// generous on any CI runner.
func TestRunLiveTicker_EndToEnd(t *testing.T) {
	hub := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tickSub, tickCancel := hub.Subscribe([]string{"metrics.tick"}, 0)
	defer tickCancel()

	go RunLiveTicker(ctx, hub, nil)

	// Give the inner subscriber time to attach before publishing.
	time.Sleep(50 * time.Millisecond)

	for i := 0; i < 3; i++ {
		inner := fmt.Sprintf(`{"ts":"2026-06-08T10:00:0%dZ","status":200,"rtime":0.010}`, i)
		outer, _ := json.Marshal(map[string]any{"line": inner, "ts": time.Now().Unix()})
		hub.Publish("nginx.access", "access", outer)
	}

	deadline := time.NewTimer(2500 * time.Millisecond)
	defer deadline.Stop()
	for {
		select {
		case e, ok := <-tickSub.Ch():
			if !ok {
				t.Fatal("tick channel closed early")
			}
			if e.Topic != "metrics.tick" {
				continue
			}
			var tick LiveTick
			if err := json.Unmarshal(e.Data, &tick); err != nil {
				t.Fatalf("unmarshal tick: %v", err)
			}
			if tick.Count < 3 {
				// Sub might have attached after the first publish raced; wait
				// for the next tick that has all three samples.
				continue
			}
			if tick.P50Ms < 1 {
				t.Fatalf("p50 should be ~10ms, got %v", tick.P50Ms)
			}
			return
		case <-deadline.C:
			t.Fatal("no metrics.tick within deadline")
		}
	}
}
