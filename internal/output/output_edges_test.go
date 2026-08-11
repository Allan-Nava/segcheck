package output

import (
	"math"
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
)

// The formatting edges: a target name too long for its column, byte counts at
// each unit boundary, and the one input JSON cannot render.

// The target column is bounded so a very long rendition name — a DASH
// representation id, or a segment URL — cannot push the message off the terminal
// and out of sight. The worst finding has to stay readable.
func TestText_BoundsTheTargetColumn(t *testing.T) {
	long := strings.Repeat("very-long-representation-id", 4)
	res := finding.Result{Findings: []finding.Finding{
		{Check: "duration", Target: long, Status: finding.BAD, Message: "declared 4.0s, real 3.2s"},
		{Check: "bitrate", Target: "720p", Status: finding.OK, Message: "within declared BANDWIDTH"},
	}}
	out := Text(res, false)

	if !strings.Contains(out, "declared 4.0s, real 3.2s") {
		t.Error("the message was lost behind a long target")
	}
	// No line may be padded out to the length of the untruncated target.
	for _, line := range strings.Split(out, "\n") {
		if len(line) > len(long) {
			t.Errorf("a line is %d chars wide, longer than the %d-char target: the column was not bounded",
				len(line), len(long))
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly-10", 10, "exactly-10"},
		{"much longer than ten", 10, "much long…"},
		// A width of one or less leaves nothing to show, so the string is passed
		// through rather than replaced by a bare ellipsis.
		{"anything", 1, "anything"},
		{"anything", 0, "anything"},
		{"", 5, ""},
	}
	for _, tc := range tests {
		if got := truncate(tc.in, tc.n); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestHumanBytes_EveryUnitBoundary(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
		// Past GiB the loop runs out of units and falls through to TiB.
		{1024 * 1024 * 1024 * 1024, "1.0 TiB"},
		{3 * 1024 * 1024 * 1024 * 1024, "3.0 TiB"},
	}
	for _, tc := range tests {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// JSON is what machine consumers read, so a value it cannot encode has to be an
// error rather than truncated or invalid output that a pipeline then fails on
// further downstream. A non-finite measurement is the case that reaches it.
func TestJSON_ReportsAnUnencodableValue(t *testing.T) {
	for _, v := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		res := finding.Result{Findings: []finding.Finding{
			{Check: "bitrate", Target: "720p", Status: finding.BAD, Value: finding.Num(v), Unit: "bps"},
		}}
		out, err := JSON(res)
		if err == nil {
			t.Errorf("JSON encoded a non-finite value as %q, want an error", out)
		}
		if out != "" {
			t.Errorf("JSON returned %q alongside its error, want nothing", out)
		}
	}
}
