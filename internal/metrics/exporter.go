package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

// ExporterConfig drives the periodic Prometheus→UDP push.
type ExporterConfig struct {
	DogStatsDAddr string
	StatsDAddr    string
	GraphiteAddr  string
	InfluxDBAddr  string // host:port for line-protocol UDP
	Prefix        string
	FlushSeconds  int
}

// Empty reports whether any export target is configured.
func (c ExporterConfig) Empty() bool {
	return c.DogStatsDAddr == "" && c.StatsDAddr == "" && c.GraphiteAddr == "" && c.InfluxDBAddr == ""
}

// RunExporter polls the metrics registry every Flush seconds and ships
// the gathered series to the configured destinations. UDP is fire-and-
// forget; failures are logged, never fatal, so a dead Datadog Agent
// can't take the dashboard down with it.
//
// Returns when ctx is cancelled. Caller starts it as a goroutine.
func RunExporter(ctx context.Context, m *Metrics, cfg ExporterConfig, logger *slog.Logger) {
	if m == nil || cfg.Empty() {
		return
	}
	flush := time.Duration(cfg.FlushSeconds) * time.Second
	if flush <= 0 {
		flush = 10 * time.Second
	}
	prefix := cfg.Prefix
	if prefix == "" {
		prefix = "apigw"
	}
	prefix = strings.TrimRight(prefix, ".") + "."

	dial := func(addr string) (net.Conn, error) {
		if addr == "" {
			return nil, nil
		}
		return net.Dial("udp", addr)
	}
	dog, _ := dial(cfg.DogStatsDAddr)
	std, _ := dial(cfg.StatsDAddr)
	gph, _ := dial(cfg.GraphiteAddr)
	influx, _ := dial(cfg.InfluxDBAddr)
	defer func() {
		if dog != nil {
			_ = dog.Close()
		}
		if std != nil {
			_ = std.Close()
		}
		if gph != nil {
			_ = gph.Close()
		}
		if influx != nil {
			_ = influx.Close()
		}
	}()

	t := time.NewTicker(flush)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			families, err := m.Gather()
			if err != nil {
				logger.Warn("metrics exporter: gather", "err", err)
				continue
			}
			ts := time.Now().Unix()
			for _, fam := range families {
				metric := fam.GetName()
				for _, mt := range fam.GetMetric() {
					value, labels := pickValue(mt)
					if dog != nil {
						emitDog(dog, prefix+metric, value, labels)
					}
					if std != nil {
						emitStatsD(std, prefix+metric, value)
					}
					if gph != nil {
						emitGraphite(gph, prefix+metric, value, ts)
					}
					if influx != nil {
						emitInflux(influx, prefix+metric, value, labels, ts)
					}
				}
			}
		}
	}
}

// Gather returns the Prometheus families currently registered. Wraps
// the registry so callers don't need to know its concrete type.
func (m *Metrics) Gather() ([]*dto.MetricFamily, error) {
	return m.registry.Gather()
}

// expfmt import is kept active even when no encoder is used directly
// in this file — exporter callers can switch to a text-format buffer
// for debugging without pulling new deps.
var _ = expfmt.NewFormat

func pickValue(m *dto.Metric) (float64, []*dto.LabelPair) {
	switch {
	case m.Counter != nil:
		return m.Counter.GetValue(), m.Label
	case m.Gauge != nil:
		return m.Gauge.GetValue(), m.Label
	case m.Summary != nil:
		return float64(m.Summary.GetSampleCount()), m.Label
	case m.Histogram != nil:
		return float64(m.Histogram.GetSampleCount()), m.Label
	case m.Untyped != nil:
		return m.Untyped.GetValue(), m.Label
	}
	return 0, m.Label
}

func emitDog(c net.Conn, name string, value float64, labels []*dto.LabelPair) {
	tags := make([]string, 0, len(labels))
	for _, l := range labels {
		tags = append(tags, l.GetName()+":"+l.GetValue())
	}
	payload := fmt.Sprintf("%s:%g|g", name, value)
	if len(tags) > 0 {
		payload += "|#" + strings.Join(tags, ",")
	}
	_, _ = c.Write([]byte(payload + "\n"))
}

func emitStatsD(c net.Conn, name string, value float64) {
	_, _ = fmt.Fprintf(c, "%s:%g|g\n", name, value)
}

func emitGraphite(c net.Conn, name string, value float64, ts int64) {
	_, _ = fmt.Fprintf(c, "%s %g %d\n", name, value, ts)
}

// emitInflux writes one InfluxDB line-protocol record:
//
//	<measurement>[,tag=value,...] value=<num> <timestamp_ns>
//
// Tag keys/values are escaped per InfluxDB rules (commas + spaces +
// equals signs in tag values would break the parser; we strip them).
func emitInflux(c net.Conn, name string, value float64, labels []*dto.LabelPair, tsSec int64) {
	var b strings.Builder
	b.WriteString(name)
	for _, l := range labels {
		k := influxEscape(l.GetName())
		v := influxEscape(l.GetValue())
		if k == "" || v == "" {
			continue
		}
		b.WriteByte(',')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
	}
	fmt.Fprintf(&b, " value=%g %d\n", value, tsSec*int64(time.Second))
	_, _ = c.Write([]byte(b.String()))
}

// influxEscape strips characters the InfluxDB line-protocol parser
// treats as delimiters. Replacing rather than backslash-escaping is
// fine for our use case (Prometheus label values).
func influxEscape(s string) string {
	r := strings.NewReplacer(",", "_", " ", "_", "=", "_")
	return r.Replace(s)
}
