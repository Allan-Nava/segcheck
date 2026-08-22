package output

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Allan-Nava/segcheck/internal/finding"
)

func sample() finding.Result {
	return finding.Result{
		Source: "https://cdn.example/master.m3u8",
		Findings: []finding.Finding{
			{Check: "continuity", Target: "1080p seg 41", Status: finding.BAD,
				Message: "gap of +512ms", Value: finding.Num(512), Unit: "ms",
				Hint: "the player has nothing to show for this interval"},
			{Check: "bitrate", Target: "720p", Status: finding.WARN, Message: "segment peaks above BANDWIDTH"},
			{Check: "ladder", Target: "master", Status: finding.OK, Message: "4 video renditions, 360p/480p/720p/1080p"},
		},
		Started:  time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
		Duration: 3400 * time.Millisecond,
		Segments: 24,
		Bytes:    18 << 20,
	}
}

func TestText_WorstFirstAndHints(t *testing.T) {
	got := Text(sample(), false)
	lines := strings.Split(strings.TrimSpace(got), "\n")

	// The thing you must look at is the first line.
	if !strings.Contains(lines[0], "BAD") || !strings.Contains(lines[0], "continuity") {
		t.Errorf("first line is not the worst finding: %q", lines[0])
	}
	// Hints are shown for problems and suppressed for OK findings.
	if !strings.Contains(got, "the player has nothing to show") {
		t.Error("the hint on the BAD finding was not rendered")
	}
	if strings.Contains(got, "4 video renditions") && strings.Contains(got, "↳ 4 video") {
		t.Error("a hint was rendered for an OK finding")
	}
	if !strings.Contains(got, "24 segments") {
		t.Errorf("summary line missing the segment count: %q", got)
	}
}

func TestText_NoANSIWhenColourIsOff(t *testing.T) {
	if strings.Contains(Text(sample(), false), "\x1b[") {
		t.Error("ANSI escape codes leaked into uncoloured output")
	}
	if !strings.Contains(Text(sample(), true), "\x1b[") {
		t.Error("coloured output carries no ANSI codes")
	}
}

func TestJSON_RoundTrips(t *testing.T) {
	s, err := JSON(sample())
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var got struct {
		Source   string `json:"source"`
		Worst    string `json:"worst"`
		Segments int    `json:"segments"`
		Summary  map[string]int
		Findings []struct {
			Check  string   `json:"check"`
			Status string   `json:"status"`
			Value  *float64 `json:"value"`
			Unit   string   `json:"unit"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(s), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, s)
	}
	if got.Worst != "BAD" {
		t.Errorf("worst = %q, want BAD", got.Worst)
	}
	if got.Segments != 24 {
		t.Errorf("segments = %d, want 24", got.Segments)
	}
	if got.Summary["BAD"] != 1 || got.Summary["OK"] != 1 {
		t.Errorf("summary = %v", got.Summary)
	}
	// The measurement must survive as a number, so a pipeline never has to
	// parse it back out of the message.
	first := got.Findings[0]
	if first.Value == nil || *first.Value != 512 || first.Unit != "ms" {
		t.Errorf("first finding value = %v %q, want 512 ms", first.Value, first.Unit)
	}
}

func TestMarkdown_ProblemsBeforeTable(t *testing.T) {
	got := Markdown(sample())
	needsAttention := strings.Index(got, "## Needs attention")
	allFindings := strings.Index(got, "## All findings")
	if needsAttention < 0 || allFindings < 0 {
		t.Fatalf("markdown is missing its sections:\n%s", got)
	}
	if needsAttention > allFindings {
		t.Error("the full table comes before what needs attention")
	}
	// The OK finding belongs in the table but not in the problem list.
	problems := got[needsAttention:allFindings]
	if strings.Contains(problems, "4 video renditions") {
		t.Error("an OK finding was listed under \"Needs attention\"")
	}
	if !strings.Contains(got, "| 🔴 BAD |") {
		t.Error("the table does not carry the status column")
	}
}

func TestMarkdown_CleanRunSaysSo(t *testing.T) {
	res := sample()
	res.Findings = []finding.Finding{{Check: "ladder", Target: "master", Status: finding.OK, Message: "fine"}}
	got := Markdown(res)
	if !strings.Contains(got, "Nothing: every check came back OK") {
		t.Errorf("a clean run does not say so:\n%s", got)
	}
}

func TestMarkdown_EscapesPipes(t *testing.T) {
	res := sample()
	res.Findings = []finding.Finding{{Check: "container", Target: "a|b", Status: finding.BAD, Message: "x|y"}}
	got := Markdown(res)
	if strings.Contains(got, "| a|b |") {
		t.Error("an unescaped pipe in a target broke the table")
	}
	if !strings.Contains(got, `a\|b`) {
		t.Errorf("pipe not escaped:\n%s", got)
	}
}

// A conformance rule has an identity of its own: a finding that says a stream
// fails something has to name what, so it can be argued with — and a machine
// consumer must not have to parse prose to find out which rule fired.
func TestRenderers_NameTheRuleAFindingComesFrom(t *testing.T) {
	res := finding.Result{
		Source: "https://cdn.example/master.m3u8",
		Findings: []finding.Finding{{
			Check: "profile", Rule: "apple:peak-vs-average", Target: "1080p", Status: finding.WARN,
			Message: "peak 5.2 Mbps is 260% of the 2.0 Mbps average",
		}},
	}

	text := Text(res, false)
	if !strings.Contains(text, "apple:peak-vs-average") {
		t.Errorf("the text renderer drops the rule:\n%s", text)
	}

	md := Markdown(res)
	if !strings.Contains(md, "apple:peak-vs-average") {
		t.Errorf("the markdown renderer drops the rule:\n%s", md)
	}

	js, err := JSON(res)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !strings.Contains(js, `"rule": "apple:peak-vs-average"`) {
		t.Errorf("the rule is not a field of its own in JSON, so a consumer has to parse prose:\n%s", js)
	}
}

// A baseline is a file segcheck produced with --output json and reads back
// later (SC-41), so the reader has to accept exactly what the writer emits.
// Without this, the two drift the first time a field is added to one and not the
// other, and the symptom is a diff that quietly compares against zeroes.
func TestJSON_RoundTripsThroughParseJSON(t *testing.T) {
	want := sample()
	want.Renditions = []string{"1080p", "720p", "360p"}

	s, err := JSON(want)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	got, err := ParseJSON([]byte(s))
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}

	if got.Source != want.Source {
		t.Errorf("source = %q, want %q", got.Source, want.Source)
	}
	if !reflect.DeepEqual(got.Renditions, want.Renditions) {
		t.Errorf("renditions = %v, want %v", got.Renditions, want.Renditions)
	}
	if got.Segments != want.Segments || got.Bytes != want.Bytes {
		t.Errorf("segments/bytes = %d/%d, want %d/%d", got.Segments, got.Bytes, want.Segments, want.Bytes)
	}
	if !got.Started.Equal(want.Started) {
		t.Errorf("started = %v, want %v", got.Started, want.Started)
	}
	if got.Duration != want.Duration {
		t.Errorf("duration = %v, want %v", got.Duration, want.Duration)
	}
	if !reflect.DeepEqual(got.Findings, want.Findings) {
		t.Errorf("findings did not survive:\n got %+v\nwant %+v", got.Findings, want.Findings)
	}
}

// A baseline file that is not JSON at all — a truncated write, or the text
// report saved by mistake — has to fail rather than compare against an empty
// run, which would report every check as newly appeared.
func TestParseJSON_RefusesWhatIsNotAReport(t *testing.T) {
	if _, err := ParseJSON([]byte("🟢 OK  ladder  master  4 renditions\n")); err == nil {
		t.Error("a text report parsed as JSON")
	}
}
