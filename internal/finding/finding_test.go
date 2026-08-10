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

func statuses(fs []Finding) []Status {
	out := make([]Status, len(fs))
	for i, f := range fs {
		out[i] = f.Status
	}
	return out
}
