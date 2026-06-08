package metrics

import (
	"net"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

func TestEmitInflux_Basic(t *testing.T) {
	// fake UDP listener
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer conn.Close()
	addr := conn.LocalAddr().String()

	out, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer out.Close()

	labels := []*dto.LabelPair{
		strPair("env", "prod"),
		strPair("region", "eu"),
	}
	emitInflux(out, "apigw_requests_total", 42, labels, 1700000000)

	buf := make([]byte, 1024)
	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	line := strings.TrimSpace(string(buf[:n]))
	for _, want := range []string{
		"apigw_requests_total",
		"env=prod",
		"region=eu",
		"value=42",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in %q", want, line)
		}
	}
	// Timestamp at the end (ns precision).
	if !strings.HasSuffix(line, " 1700000000000000000") {
		t.Errorf("expected ns timestamp suffix; got: %q", line)
	}
}

func TestInfluxEscape(t *testing.T) {
	cases := map[string]string{
		"clean":              "clean",
		"with space":         "with_space",
		"with,comma":         "with_comma",
		"with=equals":        "with_equals",
		"all,broken stuff=x": "all_broken_stuff_x",
	}
	for in, want := range cases {
		if got := influxEscape(in); got != want {
			t.Errorf("escape(%q) = %q, want %q", in, got, want)
		}
	}
}

func strPair(k, v string) *dto.LabelPair {
	return &dto.LabelPair{Name: &k, Value: &v}
}
