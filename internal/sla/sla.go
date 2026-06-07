// Package sla evaluates per-API service-level objectives against
// observed metrics. Pure-function — the caller supplies the
// observation snapshot (typically pulled from the Prometheus
// registry); this package decides "in bounds" vs "breach" and
// returns the list of breaches for alert/audit dispatch.
package sla

import (
	"fmt"
	"time"
)

// SLO mirrors config.SLO. Kept here so internal/sla doesn't import
// internal/config (the package is a re-usable evaluator the dashboard
// + a future CLI both call).
type SLO struct {
	LatencyP95Ms        int
	AvailabilityPercent float64
	PeriodHours         int
}

// Observation is one snapshot of measured behaviour for a single API
// at a moment in time. The caller is responsible for keeping the
// snapshot fresh (typically a Prometheus rangeQuery result rolled
// into these fields).
type Observation struct {
	APIName         string
	LatencyP95Ms    int
	SuccessCount    int64 // 2xx + 3xx
	TotalCount      int64 // all responses
	SampleStartedAt time.Time
	SampleEndedAt   time.Time
}

// AvailabilityPercent returns 100 × success/total, or 100 when total
// is zero (no traffic = no breach, by convention).
func (o Observation) AvailabilityPercent() float64 {
	if o.TotalCount == 0 {
		return 100
	}
	return 100 * float64(o.SuccessCount) / float64(o.TotalCount)
}

// Breach describes one SLO violation. Dimension is "latency_p95" or
// "availability" so alert receivers can route. Reason is human-
// readable.
type Breach struct {
	APIName   string
	Dimension string
	Observed  string // pretty-printed observation
	Threshold string // pretty-printed SLO bound
	Reason    string
}

// Evaluate compares one observation against the configured SLO and
// returns the breaches. Empty slice = in-bounds.
//
// Period mismatch is not validated here — the caller is expected to
// give an observation whose window matches the SLO's PeriodHours.
func Evaluate(slo SLO, obs Observation) []Breach {
	var out []Breach
	if slo.LatencyP95Ms > 0 && obs.LatencyP95Ms > slo.LatencyP95Ms {
		out = append(out, Breach{
			APIName:   obs.APIName,
			Dimension: "latency_p95",
			Observed:  fmt.Sprintf("%dms", obs.LatencyP95Ms),
			Threshold: fmt.Sprintf("≤%dms", slo.LatencyP95Ms),
			Reason: fmt.Sprintf("p95 %dms over threshold %dms",
				obs.LatencyP95Ms, slo.LatencyP95Ms),
		})
	}
	if slo.AvailabilityPercent > 0 {
		got := obs.AvailabilityPercent()
		if got < slo.AvailabilityPercent {
			out = append(out, Breach{
				APIName:   obs.APIName,
				Dimension: "availability",
				Observed:  fmt.Sprintf("%.2f%%", got),
				Threshold: fmt.Sprintf("≥%.2f%%", slo.AvailabilityPercent),
				Reason: fmt.Sprintf("availability %.2f%% under threshold %.2f%% (success=%d total=%d)",
					got, slo.AvailabilityPercent, obs.SuccessCount, obs.TotalCount),
			})
		}
	}
	return out
}

// Period returns the SLO's window as a duration, defaulting to 1h
// when PeriodHours is unset.
func Period(slo SLO) time.Duration {
	if slo.PeriodHours <= 0 {
		return 1 * time.Hour
	}
	return time.Duration(slo.PeriodHours) * time.Hour
}
