package sla

import (
	"testing"
	"time"
)

func TestEvaluate_NoSLOReturnsEmpty(t *testing.T) {
	obs := Observation{APIName: "x", LatencyP95Ms: 9999, SuccessCount: 1, TotalCount: 100}
	if b := Evaluate(SLO{}, obs); len(b) != 0 {
		t.Errorf("empty SLO should produce no breaches: %+v", b)
	}
}

func TestEvaluate_LatencyBreach(t *testing.T) {
	got := Evaluate(SLO{LatencyP95Ms: 200}, Observation{
		APIName: "billing", LatencyP95Ms: 350, SuccessCount: 100, TotalCount: 100,
	})
	if len(got) != 1 {
		t.Fatalf("want 1 breach, got %d: %+v", len(got), got)
	}
	if got[0].Dimension != "latency_p95" {
		t.Errorf("dim: %s", got[0].Dimension)
	}
}

func TestEvaluate_LatencyEqualNotBreach(t *testing.T) {
	got := Evaluate(SLO{LatencyP95Ms: 200}, Observation{
		APIName: "x", LatencyP95Ms: 200, SuccessCount: 1, TotalCount: 1,
	})
	if len(got) != 0 {
		t.Errorf("equal-to-threshold should NOT breach: %+v", got)
	}
}

func TestEvaluate_AvailabilityBreach(t *testing.T) {
	got := Evaluate(SLO{AvailabilityPercent: 99.9}, Observation{
		APIName: "x", SuccessCount: 980, TotalCount: 1000, // 98%
	})
	if len(got) != 1 || got[0].Dimension != "availability" {
		t.Errorf("expected availability breach: %+v", got)
	}
}

func TestEvaluate_AvailabilityZeroTraffic(t *testing.T) {
	// No traffic — convention: count as 100% available.
	got := Evaluate(SLO{AvailabilityPercent: 99.9}, Observation{
		APIName: "x", SuccessCount: 0, TotalCount: 0,
	})
	if len(got) != 0 {
		t.Errorf("zero traffic should not breach availability: %+v", got)
	}
}

func TestEvaluate_BothDimensionsBreach(t *testing.T) {
	got := Evaluate(SLO{LatencyP95Ms: 100, AvailabilityPercent: 99}, Observation{
		APIName: "x", LatencyP95Ms: 500, SuccessCount: 90, TotalCount: 100,
	})
	if len(got) != 2 {
		t.Fatalf("want 2 breaches, got %d: %+v", len(got), got)
	}
}

func TestObservation_AvailabilityPercent(t *testing.T) {
	cases := []struct {
		s, tot int64
		want   float64
	}{
		{100, 100, 100},
		{99, 100, 99},
		{0, 0, 100},
		{1, 1000, 0.1},
	}
	for _, c := range cases {
		got := Observation{SuccessCount: c.s, TotalCount: c.tot}.AvailabilityPercent()
		if got != c.want {
			t.Errorf("(%d/%d) = %v, want %v", c.s, c.tot, got, c.want)
		}
	}
}

func TestPeriodDefault(t *testing.T) {
	if Period(SLO{}) != 1*time.Hour {
		t.Errorf("zero PeriodHours should default to 1h")
	}
	if Period(SLO{PeriodHours: 24}) != 24*time.Hour {
		t.Errorf("24h period")
	}
}
