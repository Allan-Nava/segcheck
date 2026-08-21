package output

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
)

// Metrics output, for the run that happens on a timer rather than in front of a
// person: a cron job or a Nomad periodic batch writing into a textfile
// collector, a Pushgateway, or an OTLP/HTTP ingest.
//
// The design decision that matters here is what is *not* exposed.
//
// A finding's Target names the exact thing that was looked at, which is what
// makes the text report useful and what makes it poison as a metric label:
// "720p seg 38" is a different label value on every run, because a live stream
// has different segments every time it is sampled. A `target` label would mint a
// new series on every tick and never retire one, so a dashboard fed by a
// minutely cron would carry hundreds of thousands of dead series within a week —
// and the operator would find out when Prometheus ran out of memory, not when
// they added the exporter. Nothing here carries a target.
//
// What is exposed is the aggregate, whose shape is fixed by the check set rather
// than by the stream: a count per check per status, the worst severity per check,
// the worst overall, and the facts of the run itself. That is enough for the two
// things a dashboard is for — alerting that a stream went bad and showing which
// check said so — and the detail an operator needs after the alert fires is in
// the JSON or the text output, which is where detail belongs.
//
// Every check present in a run states all four statuses, zeros included, so an
// alert reads `segcheck_findings{status="BAD"} > 0` rather than having to reason
// about a series that does not exist yet. A check that falls silent still
// disappears entirely — the same trap the smoke suite exists for — so alerting
// on a check going missing needs `absent()`, and the HELP text says so.
//
// The severity scale is the project's own order, OK < WARN < BAD < ERROR, so 3
// is a check that could not run. That outranks 2, a defect it did find, because
// a hole in the coverage is the thing an operator most needs to know about and
// the thing a green dashboard most easily hides.
//
// A round trip against this package's own tests could not catch a shared
// misreading of the exposition format, so the writer was checked against an
// outside authority the way the AES vectors and the SCTE-35 bytes were: the
// output of a real run, plus bodies carrying a quote, a backslash, a newline and
// non-ASCII in the stream URL, were parsed with the reference
// `prometheus_client` parser, which accepted all of them and round-tripped every
// label value byte for byte. That check is not in the suite, because a Python
// dependency in CI is not worth it and the zero-dependency rule covers the
// tooling — it is recorded here as the reason to trust the escaping.

// severityValue is the numeric scale the metrics expose, and it is deliberately
// finding.Severity rather than a second opinion about it.
func severityValue(s finding.Status) float64 { return float64(finding.Severity(s)) }

// statusOrder is the order statuses are emitted in, so two runs of the same
// stream produce byte-identical output when nothing changed.
var statusOrder = []finding.Status{finding.OK, finding.WARN, finding.BAD, finding.ERROR}

// metricSample is one number and the labels that identify it. The stream is not among
// them: each renderer places it where its own convention puts it — a label in
// Prometheus, a resource attribute in OTLP.
type metricSample struct {
	labels [][2]string
	value  float64
}

// family is one metric: every renderer needs the same name, help text and
// samples, and only the spelling of the name differs between them.
type family struct {
	prom    string // Prometheus name, segcheck_foo
	otlp    string // OTLP name, segcheck.foo
	help    string
	unit    string // OTLP unit, UCUM. Empty for a dimensionless count.
	samples []metricSample
}

// families builds every metric a run produces, in a fixed order.
func families(res finding.Result) []family {
	// Count per check per status, and the worst per check. A map for the
	// counting, a sorted key list for the emitting: the output has to be stable
	// or a textfile collector sees a changed file every tick.
	counts := map[string]map[finding.Status]int{}
	worstPerCheck := map[string]finding.Status{}
	var checks []string
	for _, f := range res.Findings {
		if _, seen := counts[f.Check]; !seen {
			counts[f.Check] = map[finding.Status]int{}
			worstPerCheck[f.Check] = finding.OK
			checks = append(checks, f.Check)
		}
		counts[f.Check][f.Status]++
		if finding.Severity(f.Status) > finding.Severity(worstPerCheck[f.Check]) {
			worstPerCheck[f.Check] = f.Status
		}
	}
	sort.Strings(checks)

	findings := family{
		prom: "segcheck_findings", otlp: "segcheck.findings",
		help: "Findings in the last run, by check and status. A check that produced nothing has no series at all, so alert on a check disappearing with absent().",
	}
	severities := family{
		prom: "segcheck_check_severity", otlp: "segcheck.check_severity",
		help: "Worst status per check in the last run: 0 OK, 1 WARN, 2 BAD, 3 ERROR. 3 means the check could not run, which outranks a defect it did find.",
	}
	for _, check := range checks {
		for _, st := range statusOrder {
			findings.samples = append(findings.samples, metricSample{
				labels: [][2]string{{"check", check}, {"status", string(st)}},
				value:  float64(counts[check][st]),
			})
		}
		severities.samples = append(severities.samples, metricSample{
			labels: [][2]string{{"check", check}},
			value:  severityValue(worstPerCheck[check]),
		})
	}

	// A zero Started is a Result built by hand rather than by a run. Emitting a
	// timestamp derived from it would report 1754, so the run clock is left out
	// entirely rather than stated wrongly.
	var startNanos int64
	if !res.Started.IsZero() {
		startNanos = res.Started.UnixNano()
	}

	out := []family{
		{
			prom: "segcheck_up", otlp: "segcheck.up",
			help:    "1 when the check ran to completion. Findings are not failure: a run full of BADs still reports 1.",
			samples: []metricSample{{value: 1}},
		},
		{
			prom: "segcheck_worst_severity", otlp: "segcheck.worst_severity",
			help:    "Worst status anywhere in the last run: 0 OK, 1 WARN, 2 BAD, 3 ERROR.",
			samples: []metricSample{{value: severityValue(finding.Worst(res.Findings))}},
		},
		severities,
		findings,
		{
			prom: "segcheck_segments_sampled", otlp: "segcheck.segments_sampled",
			help:    "Media segments downloaded and parsed in the last run.",
			unit:    "{segment}",
			samples: []metricSample{{value: float64(res.Segments)}},
		},
		{
			prom: "segcheck_bytes_downloaded", otlp: "segcheck.bytes_downloaded",
			help:    "Media bytes downloaded in the last run.",
			unit:    "By",
			samples: []metricSample{{value: float64(res.Bytes)}},
		},
		{
			prom: "segcheck_run_duration_seconds", otlp: "segcheck.run_duration_seconds",
			help:    "How long the last run took.",
			unit:    "s",
			samples: []metricSample{{value: res.Duration.Seconds()}},
		},
	}
	if startNanos != 0 {
		out = append(out, family{
			prom: "segcheck_run_timestamp_seconds", otlp: "segcheck.run_timestamp_seconds",
			help:    "When the last run started, in seconds since the epoch. Alert on this going stale to catch a checker that stopped running.",
			unit:    "s",
			samples: []metricSample{{value: float64(startNanos) / 1e9}},
		})
	}
	return out
}

// Prometheus renders the run in the text exposition format, for a textfile
// collector or a Pushgateway.
//
// Samples of one metric are emitted together, under one HELP and one TYPE, which
// the format requires: a scraper reading a name it has already seen in a
// different block rejects the whole body.
func Prometheus(res finding.Result) string {
	var b strings.Builder
	for i, f := range families(res) {
		if len(f.samples) == 0 {
			continue
		}
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("# HELP " + f.prom + " " + escapeHelp(f.help) + "\n")
		b.WriteString("# TYPE " + f.prom + " gauge\n")
		for _, s := range f.samples {
			// The stream goes last, so the labels a rule matches on read first.
			labels := append(append([][2]string{}, s.labels...), [2]string{"stream", res.Source})
			b.WriteString(f.prom + "{")
			for j, kv := range labels {
				if j > 0 {
					b.WriteString(",")
				}
				b.WriteString(kv[0] + `="` + escapeLabel(kv[1]) + `"`)
			}
			b.WriteString("} " + formatValue(s.value) + "\n")
		}
	}
	return b.String()
}

// formatValue prints a float the way the exposition format wants it: an integral
// value with no decimal point, everything else at full precision.
func formatValue(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// escapeLabel escapes a label value. A backslash, a double quote or a newline
// reaching the output raw makes the whole body unparseable, so a scrape fails on
// every metric rather than on the one odd URL.
func escapeLabel(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeHelp escapes a HELP line, where a newline would start a bogus record and
// a backslash would be read as an escape.
func escapeHelp(v string) string {
	return strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), "\n", `\n`)
}

// OTLP renders the same metrics as an OTLP/HTTP ExportMetricsServiceRequest, for
// `curl -H 'Content-Type: application/json' --data-binary @- $ENDPOINT/v1/metrics`.
//
// The stream is a resource attribute rather than a per-point one, because every
// metric in one payload is about one stream — which is what a resource is for.
//
// It returns no error, unlike JSON. That is not a shortcut: JSON marshals the
// findings themselves, and a Finding carries a *float64 Value that a future check
// could set to NaN, which encoding/json refuses. This payload holds only strings
// and float64s derived from counts, severities and a duration — never a finding's
// own Value, because exposing per-target measurements is exactly what this
// renderer deliberately does not do — so there is no value in it that marshalling
// can reject, and an error return would be a branch no test could ever reach.
// TestOTLP_IsOTLPShapedJSON is what holds that claim up: it parses the output.
func OTLP(res finding.Result) string {
	type otlpValue struct {
		StringValue string `json:"stringValue"`
	}
	type otlpAttr struct {
		Key   string    `json:"key"`
		Value otlpValue `json:"value"`
	}
	type otlpPoint struct {
		Attributes []otlpAttr `json:"attributes,omitempty"`
		// Nanoseconds go over the wire as a string: the value is past the range a
		// JSON number is guaranteed to survive intact, and a collector rejects
		// the whole payload over one lossy timestamp.
		TimeUnixNano string  `json:"timeUnixNano"`
		AsDouble     float64 `json:"asDouble"`
	}
	type otlpGauge struct {
		DataPoints []otlpPoint `json:"dataPoints"`
	}
	type otlpMetric struct {
		Name        string    `json:"name"`
		Description string    `json:"description"`
		Unit        string    `json:"unit,omitempty"`
		Gauge       otlpGauge `json:"gauge"`
	}
	type otlpScope struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	type otlpScopeMetrics struct {
		Scope   otlpScope    `json:"scope"`
		Metrics []otlpMetric `json:"metrics"`
	}
	type otlpResource struct {
		Attributes []otlpAttr `json:"attributes"`
	}
	type otlpResourceMetrics struct {
		Resource     otlpResource       `json:"resource"`
		ScopeMetrics []otlpScopeMetrics `json:"scopeMetrics"`
	}
	type otlpRequest struct {
		ResourceMetrics []otlpResourceMetrics `json:"resourceMetrics"`
	}

	nanos := "0"
	if !res.Started.IsZero() {
		nanos = strconv.FormatInt(res.Started.UnixNano(), 10)
	}

	var metrics []otlpMetric
	for _, f := range families(res) {
		if len(f.samples) == 0 {
			continue
		}
		m := otlpMetric{Name: f.otlp, Description: f.help, Unit: f.unit}
		for _, s := range f.samples {
			p := otlpPoint{TimeUnixNano: nanos, AsDouble: s.value}
			for _, kv := range s.labels {
				p.Attributes = append(p.Attributes, otlpAttr{Key: kv[0], Value: otlpValue{StringValue: kv[1]}})
			}
			m.Gauge.DataPoints = append(m.Gauge.DataPoints, p)
		}
		metrics = append(metrics, m)
	}

	b, _ := json.Marshal(otlpRequest{ResourceMetrics: []otlpResourceMetrics{{
		Resource: otlpResource{Attributes: []otlpAttr{
			{Key: "service.name", Value: otlpValue{StringValue: "segcheck"}},
			// The same version string the User-Agent carries, so a dashboard and
			// an origin's access log name the same build.
			{Key: "service.version", Value: otlpValue{StringValue: fetch.Version}},
			{Key: "segcheck.stream", Value: otlpValue{StringValue: res.Source}},
		}},
		ScopeMetrics: []otlpScopeMetrics{{
			Scope:   otlpScope{Name: "segcheck", Version: fetch.Version},
			Metrics: metrics,
		}},
	}}})
	return string(b) + "\n"
}
