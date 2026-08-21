package output

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Allan-Nava/segcheck/internal/finding"
)

func metricsResult() finding.Result {
	return finding.Result{
		Source:   "https://cdn.example/master.m3u8",
		Started:  time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC),
		Duration: 4200 * time.Millisecond,
		Segments: 18,
		Bytes:    25_690_112,
		Findings: []finding.Finding{
			{Check: "continuity", Target: "720p seg 38", Status: finding.BAD,
				Message: "gap of 1.240s", Value: finding.Num(1.24), Unit: "s"},
			{Check: "continuity", Target: "720p seg 41", Status: finding.BAD,
				Message: "gap of 0.900s", Value: finding.Num(0.9), Unit: "s"},
			{Check: "bitrate", Target: "1080p 4800kbps seg 12", Status: finding.WARN,
				Message: "segment peaks above BANDWIDTH", Value: finding.Num(29), Unit: "%"},
			{Check: "container", Target: "720p", Status: finding.OK,
				Message: "ts, 1 tracks"},
		},
	}
}

// A Prometheus endpoint whose series count grows with every run is a memory
// leak in the operator's Prometheus, not a feature. Every segment sampled has
// its own target — "720p seg 38" — so a target label would mint new series on
// every cron tick and never retire them. The aggregate is what gets exposed,
// and this test is the guarantee: no finding's target reaches the output.
func TestPrometheus_NoPerTargetSeries(t *testing.T) {
	got := Prometheus(metricsResult())

	for _, forbidden := range []string{"720p seg 38", "720p seg 41", "1080p 4800kbps seg 12", "target="} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the exposition carries %q, which mints a new series per segment per run:\n%s",
				forbidden, got)
		}
	}
}

// Every check present in the run states all four statuses, zeros included, so
// an alert can be written as `> 0` rather than having to reason about a series
// that does not exist yet.
func TestPrometheus_EveryCheckStatesAllFourStatuses(t *testing.T) {
	got := Prometheus(metricsResult())

	for _, want := range []string{
		`segcheck_findings{check="continuity",status="OK",stream="https://cdn.example/master.m3u8"} 0`,
		`segcheck_findings{check="continuity",status="WARN",stream="https://cdn.example/master.m3u8"} 0`,
		`segcheck_findings{check="continuity",status="BAD",stream="https://cdn.example/master.m3u8"} 2`,
		`segcheck_findings{check="continuity",status="ERROR",stream="https://cdn.example/master.m3u8"} 0`,
		`segcheck_findings{check="bitrate",status="WARN",stream="https://cdn.example/master.m3u8"} 1`,
		`segcheck_findings{check="container",status="OK",stream="https://cdn.example/master.m3u8"} 1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing series:\n  %s\ngot:\n%s", want, got)
		}
	}
}

// The per-check severity is what an alert rule keys on: one series per check,
// carrying the worst thing that check found.
func TestPrometheus_PerCheckSeverityIsTheWorstThatCheckFound(t *testing.T) {
	got := Prometheus(metricsResult())

	for _, want := range []string{
		`segcheck_check_severity{check="continuity",stream="https://cdn.example/master.m3u8"} 2`,
		`segcheck_check_severity{check="bitrate",stream="https://cdn.example/master.m3u8"} 1`,
		`segcheck_check_severity{check="container",stream="https://cdn.example/master.m3u8"} 0`,
		`segcheck_worst_severity{stream="https://cdn.example/master.m3u8"} 2`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing series:\n  %s\ngot:\n%s", want, got)
		}
	}
}

// ERROR outranks BAD, because a check that could not run is a hole in the
// coverage and an operator has to know. The metric has to carry that order or
// an alert on the worst severity would rank a hole below a defect.
func TestPrometheus_ERROROutranksBAD(t *testing.T) {
	res := metricsResult()
	res.Findings = append(res.Findings, finding.Finding{
		Check: "init", Target: "1080p", Status: finding.ERROR, Message: "init.mp4: 404",
	})

	got := Prometheus(res)
	if !strings.Contains(got, `segcheck_worst_severity{stream="https://cdn.example/master.m3u8"} 3`) {
		t.Errorf("an ERROR did not raise the worst severity above BAD:\n%s", got)
	}
}

// The run's own facts, which is what says a cron job is still working at all.
// A dashboard with no segcheck_up cannot tell a healthy stream from a checker
// that stopped running.
func TestPrometheus_CarriesTheRunItself(t *testing.T) {
	got := Prometheus(metricsResult())

	for _, want := range []string{
		`segcheck_up{stream="https://cdn.example/master.m3u8"} 1`,
		`segcheck_segments_sampled{stream="https://cdn.example/master.m3u8"} 18`,
		`segcheck_bytes_downloaded{stream="https://cdn.example/master.m3u8"} 25690112`,
		`segcheck_run_duration_seconds{stream="https://cdn.example/master.m3u8"} 4.2`,
		`segcheck_run_timestamp_seconds{stream="https://cdn.example/master.m3u8"} 1787308200`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing series:\n  %s\ngot:\n%s", want, got)
		}
	}
}

// Every metric needs its HELP and TYPE, or a scraper shows an operator a bare
// number with no idea which direction is bad.
func TestPrometheus_EveryMetricIsDocumented(t *testing.T) {
	got := Prometheus(metricsResult())

	names := map[string]bool{}
	for _, line := range strings.Split(got, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, _, _ := strings.Cut(line, "{")
		name, _, _ = strings.Cut(name, " ")
		names[name] = true
	}
	if len(names) == 0 {
		t.Fatalf("no samples at all:\n%s", got)
	}
	for name := range names {
		if !strings.Contains(got, "# HELP "+name+" ") {
			t.Errorf("%s has no HELP line", name)
		}
		if !strings.Contains(got, "# TYPE "+name+" gauge") {
			t.Errorf("%s has no TYPE line", name)
		}
	}
}

// A label value is not free text: a quote or a backslash in a stream URL that
// reaches the output unescaped makes the whole exposition unparseable, and the
// scrape fails on every metric rather than on the one that was odd.
func TestPrometheus_EscapesLabelValues(t *testing.T) {
	res := metricsResult()
	res.Source = `https://cdn.example/a"b\c` + "\n" + `d?x=1`

	got := Prometheus(res)
	if strings.Contains(got, `a"b`) {
		t.Errorf("an unescaped quote reached the output:\n%s", got)
	}
	if !strings.Contains(got, `a\"b\\c\nd?x=1`) {
		t.Errorf("quote, backslash and newline were not all escaped:\n%s", got)
	}
}

// A clean run still has to report, and report zero. Emitting nothing would make
// a healthy stream indistinguishable from a checker that never ran.
func TestPrometheus_ACleanRunStillReports(t *testing.T) {
	got := Prometheus(finding.Result{Source: "https://cdn.example/m.m3u8"})

	if !strings.Contains(got, `segcheck_up{stream="https://cdn.example/m.m3u8"} 1`) {
		t.Errorf("a run with no findings did not report itself:\n%s", got)
	}
	if !strings.Contains(got, `segcheck_worst_severity{stream="https://cdn.example/m.m3u8"} 0`) {
		t.Errorf("a run with no findings did not report a worst severity of 0:\n%s", got)
	}
}

// ---------- OTLP ----------

// The OTLP payload is the same metrics in the shape an OTLP/HTTP collector
// accepts, so it has to be valid JSON carrying valid OTLP structure — not JSON
// that merely looks about right.
func TestOTLP_IsOTLPShapedJSON(t *testing.T) {
	s := OTLP(metricsResult())

	var payload struct {
		ResourceMetrics []struct {
			Resource struct {
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						StringValue string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
			ScopeMetrics []struct {
				Scope struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"scope"`
				Metrics []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
					Unit        string `json:"unit"`
					Gauge       struct {
						DataPoints []struct {
							TimeUnixNano string  `json:"timeUnixNano"`
							AsDouble     float64 `json:"asDouble"`
							Attributes   []struct {
								Key   string `json:"key"`
								Value struct {
									StringValue string `json:"stringValue"`
								} `json:"value"`
							} `json:"attributes"`
						} `json:"dataPoints"`
					} `json:"gauge"`
				} `json:"metrics"`
			} `json:"scopeMetrics"`
		} `json:"resourceMetrics"`
	}
	if err := json.Unmarshal([]byte(s), &payload); err != nil {
		t.Fatalf("the payload is not JSON: %v\n%s", err, s)
	}
	if len(payload.ResourceMetrics) != 1 {
		t.Fatalf("want one resourceMetrics, got %d", len(payload.ResourceMetrics))
	}
	rm := payload.ResourceMetrics[0]
	if len(rm.ScopeMetrics) != 1 {
		t.Fatalf("want one scopeMetrics, got %d", len(rm.ScopeMetrics))
	}

	// The stream belongs on the resource, not repeated on every data point:
	// every metric in this payload is about one stream.
	var stream, service string
	for _, a := range rm.Resource.Attributes {
		switch a.Key {
		case "segcheck.stream":
			stream = a.Value.StringValue
		case "service.name":
			service = a.Value.StringValue
		}
	}
	if service != "segcheck" {
		t.Errorf("service.name = %q, want segcheck", service)
	}
	if stream != "https://cdn.example/master.m3u8" {
		t.Errorf("segcheck.stream = %q", stream)
	}
	if rm.ScopeMetrics[0].Scope.Name != "segcheck" || rm.ScopeMetrics[0].Scope.Version == "" {
		t.Errorf("scope = %+v, want a named and versioned scope", rm.ScopeMetrics[0].Scope)
	}

	byName := map[string]bool{}
	for _, m := range rm.ScopeMetrics[0].Metrics {
		byName[m.Name] = true
		if len(m.Gauge.DataPoints) == 0 {
			t.Errorf("%s carries no data points", m.Name)
		}
		if m.Description == "" {
			t.Errorf("%s carries no description", m.Name)
		}
		for _, dp := range m.Gauge.DataPoints {
			if dp.TimeUnixNano == "" {
				t.Errorf("%s has a data point with no timestamp", m.Name)
			}
		}
	}
	for _, want := range []string{
		"segcheck.up", "segcheck.findings", "segcheck.check_severity",
		"segcheck.worst_severity", "segcheck.segments_sampled",
		"segcheck.bytes_downloaded", "segcheck.run_duration_seconds",
	} {
		if !byName[want] {
			t.Errorf("missing metric %q; got %v", want, byName)
		}
	}
}

// The two renderers must not drift: the same run has to produce the same
// numbers whichever way it is shipped, or a Prometheus dashboard and an OTLP
// one disagree about the same stream.
func TestOTLP_AgreesWithPrometheus(t *testing.T) {
	res := metricsResult()
	prom := Prometheus(res)

	s := OTLP(res)
	// segcheck_findings{check="continuity",status="BAD"} is 2 in both.
	if !strings.Contains(prom, `status="BAD",stream="https://cdn.example/master.m3u8"} 2`) {
		t.Fatalf("the Prometheus baseline changed; update this test:\n%s", prom)
	}
	if !strings.Contains(s, `"asDouble":2`) {
		t.Errorf("the OTLP payload does not carry the count the exposition does:\n%s", s)
	}
}

// A timestamp OTLP can read is nanoseconds since the epoch as a string, because
// the value overflows a JSON number's exact-integer range. Emitting it as a
// number is the mistake a collector rejects the whole payload for.
func TestOTLP_TimestampsAreStringNanoseconds(t *testing.T) {
	s := OTLP(metricsResult())
	if !strings.Contains(s, `"timeUnixNano":"1787308200000000000"`) {
		t.Errorf("the timestamp is not quoted nanoseconds:\n%s", s)
	}
}

// A clean run has no findings, so the per-check families have no samples at all
// and must be left out rather than emitted empty: a metric with no data points
// is a payload some collectors reject outright.
func TestOTLP_ACleanRunOmitsTheEmptyFamilies(t *testing.T) {
	s := OTLP(finding.Result{Source: "https://cdn.example/m.m3u8"})

	if strings.Contains(s, "segcheck.findings") {
		t.Errorf("a run with no findings still emitted the findings metric:\n%s", s)
	}
	if strings.Contains(s, "segcheck.check_severity") {
		t.Errorf("a run with no findings still emitted the per-check severity metric:\n%s", s)
	}
	// The run itself still has to be reported, or a dashboard cannot tell a
	// healthy stream from a checker that stopped running.
	if !strings.Contains(s, "segcheck.up") {
		t.Errorf("a run with no findings did not report itself:\n%s", s)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(s), &payload); err != nil {
		t.Errorf("the payload is not JSON: %v\n%s", err, s)
	}
}
