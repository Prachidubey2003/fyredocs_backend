package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type stubHub struct{ n int }

func (s *stubHub) RoomCount() int { return s.n }

func TestRoomsTotal_BindUpdatesGauge(t *testing.T) {
	// promauto registers the gauge against the default registry
	// at package-load time, so this test just exercises the
	// Bind → GaugeFunc → scrape path end-to-end via the same
	// scrape endpoint we ship in production.
	srv := httptest.NewServer(promhttp.Handler())
	defer srv.Close()

	hub := &stubHub{n: 0}
	Bind(hub)
	if got := scrapeMetric(t, srv.URL, "collab_rooms_total"); got != "0" {
		t.Errorf("rooms_total = %q after Bind(0), want 0", got)
	}

	hub.n = 7
	if got := scrapeMetric(t, srv.URL, "collab_rooms_total"); got != "7" {
		t.Errorf("rooms_total = %q after hub.n=7, want 7", got)
	}
}

func TestConnections_IncDec(t *testing.T) {
	srv := httptest.NewServer(promhttp.Handler())
	defer srv.Close()

	start := readGauge(t, srv.URL, "collab_connections_total")
	Connections.Inc()
	Connections.Inc()
	Connections.Dec()
	if got := readGauge(t, srv.URL, "collab_connections_total"); got != start+1 {
		t.Errorf("connections_total = %f, want %f", got, start+1)
	}
	// Reset for downstream tests that read the same counter.
	Connections.Dec()
}

func TestBroadcastBytes_AccumulatesAcrossCalls(t *testing.T) {
	srv := httptest.NewServer(promhttp.Handler())
	defer srv.Close()

	start := readCounter(t, srv.URL, "collab_broadcast_bytes_total")
	BroadcastBytes.Add(100)
	BroadcastBytes.Add(250)
	if got := readCounter(t, srv.URL, "collab_broadcast_bytes_total"); got != start+350 {
		t.Errorf("broadcast_bytes = %f, want %f", got, start+350)
	}
}

// scrapeMetric returns the value of the first sample of `name` as
// a string (preserving how Prometheus serialises it). Used when
// we want exact decimal text — gauges from RoomCount() are
// integers so this is the clearest assertion.
func scrapeMetric(t *testing.T, url, name string) string {
	t.Helper()
	body := scrape(t, url)
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name+" ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			return fields[1]
		}
	}
	t.Fatalf("metric %q not found in scrape", name)
	return ""
}

func readGauge(t *testing.T, url, name string) float64   { return readFloat(t, url, name) }
func readCounter(t *testing.T, url, name string) float64 { return readFloat(t, url, name) }

func readFloat(t *testing.T, url, name string) float64 {
	t.Helper()
	s := scrapeMetric(t, url, name)
	// Prometheus emits floats like "0" or "1.5e+01" — strconv handles both.
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("ParseFloat(%q): %v", s, err)
	}
	return v
}

func scrape(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read scrape body: %v", err)
	}
	return string(body)
}
