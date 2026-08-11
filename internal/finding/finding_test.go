package finding

import "testing"

func TestSortWorstFirst(t *testing.T) {
	fs := []Finding{
		{Check: "duration", Target: "720p", Status: OK},
		{Check: "continuity", Target: "1080p", Status: BAD},
		{Check: "fetch", Target: "480p", Status: ERROR},
		{Check: "bitrate", Target: "720p", Status: WARN},
		{Check: "continuity", Target: "480p", Status: BAD},
	}
	SortWorstFirst(fs)

	want := []Status{ERROR, BAD, BAD, WARN, OK}
	for i, w := range want {
		if fs[i].Status != w {
			t.Fatalf("position %d = %s, want %s (order: %v)", i, fs[i].Status, w, statuses(fs))
		}
	}
	// Equal severity is ordered by check then target — lexicographically, so
	// "1080p" precedes "480p" — which makes two identical runs render
	// byte-for-byte the same.
	if fs[1].Target != "1080p" || fs[2].Target != "480p" {
		t.Errorf("ties not broken by target: %s then %s", fs[1].Target, fs[2].Target)
	}
}

func TestWorstAndSummarize(t *testing.T) {
	if got := Worst(nil); got != OK {
		t.Errorf("Worst(nil) = %s, want OK", got)
	}
	fs := []Finding{{Status: OK}, {Status: WARN}, {Status: BAD}, {Status: OK}}
	if got := Worst(fs); got != BAD {
		t.Errorf("Worst = %s, want BAD", got)
	}
	s := Summarize(fs)
	if s[OK] != 2 || s[WARN] != 1 || s[BAD] != 1 || s[ERROR] != 0 {
		t.Errorf("Summarize = %v", s)
	}
}

func TestAtLeast(t *testing.T) {
	tests := []struct {
		s, threshold Status
		want         bool
	}{
		{OK, WARN, false},
		{WARN, WARN, true},
		{BAD, WARN, true},
		{ERROR, BAD, true},
		{WARN, BAD, false},
		// An empty threshold is satisfied by anything, including OK: callers
		// that mean "no gate at all" must test for "" themselves.
		{OK, "", true},
	}
	for _, tc := range tests {
		if got := AtLeast(tc.s, tc.threshold); got != tc.want {
			t.Errorf("AtLeast(%q, %q) = %v, want %v", tc.s, tc.threshold, got, tc.want)
		}
	}
}

// The severity order is the one rule the whole tool is built on: it decides what
// the first line of every report is, and ERROR sorting above BAD is deliberate —
// an operator needs to know the coverage has a hole before they read the
// findings, because a check that could not run is not a stream that is healthy.
func TestSeverity_OrdersOKBelowWarnBelowBadBelowError(t *testing.T) {
	order := []Status{OK, WARN, BAD, ERROR}
	for i := 1; i < len(order); i++ {
		if Severity(order[i]) <= Severity(order[i-1]) {
			t.Errorf("Severity(%s) = %d is not above Severity(%s) = %d",
				order[i], Severity(order[i]), order[i-1], Severity(order[i-1]))
		}
	}
	// An unset status ranks below OK rather than panicking or ranking high: a
	// finding built without a status must never lead the report.
	if Severity("") > Severity(OK) {
		t.Errorf("Severity(\"\") = %d outranks OK", Severity(""))
	}
}

func TestNum(t *testing.T) {
	p := Num(4.004)
	if p == nil {
		t.Fatal("Num returned nil")
	}
	if *p != 4.004 {
		t.Errorf("*Num(4.004) = %v", *p)
	}
	// Each call has to own its value, or two findings built in one loop would
	// end up sharing a measurement.
	a, b := Num(1), Num(2)
	if a == b || *a == *b {
		t.Error("Num returned a shared pointer: two findings would report the same value")
	}
}

func statuses(fs []Finding) []Status {
	out := make([]Status, len(fs))
	for i, f := range fs {
		out[i] = f.Status
	}
	return out
}
