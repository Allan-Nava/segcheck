package analyze

import (
	"context"
	"testing"
	"time"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
)

// A conformance rule with no way to turn it off turns a run that was clean
// yesterday into a wall of findings today, on a stream nobody changed. Profiles
// are opt-in for that reason alone, and `none` has to mean none.

func runProfile(t *testing.T, url, profile string) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 4
	opts.Concurrency = 4
	opts.Profile = profile
	opts.Now = func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }
	return Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), url, opts)
}

func TestRun_NoProfileRunsNoRules(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})

	for _, p := range []string{"", ProfileNone} {
		res := runProfile(t, srv.URL+"/master.m3u8", p)
		if hasCheck(res, "profile") {
			t.Errorf("profile %q produced conformance findings; opt-in has to mean opt-in:\n%s", p, dump(res))
		}
	}
}

// Selecting a profile says which rule set ran, even when everything passes:
// "no findings" and "no rules" look identical in a report otherwise, and an
// operator who asked for conformance needs to know they got it.
func TestRun_SelectingAProfileSaysWhichRuleSetRan(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})

	res := runProfile(t, srv.URL+"/master.m3u8", ProfileApple)

	if !hasCheck(res, "profile") {
		t.Fatalf("--profile apple produced no profile finding at all:\n%s", dump(res))
	}
	var named bool
	for _, f := range res.Findings {
		if f.Check == "profile" && f.Rule != "" {
			named = true
		}
	}
	if !named {
		t.Errorf("no profile finding names the rule it comes from, so none can be argued with:\n%s", dump(res))
	}
}

// A rule set segcheck does not implement yet is a limit of the tool, not a
// verdict about the stream. It says so at OK level rather than reporting a
// clean pass it never actually made.
func TestRun_UnimplementedProfileSaysSoRatherThanPassing(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(4, 1280, 720)},
	})

	res := runProfile(t, srv.URL+"/master.m3u8", ProfileDASHIF)

	f, ok := findFinding(res, "profile", finding.OK)
	if !ok {
		t.Fatalf("--profile dash-if said nothing at all:\n%s", dump(res))
	}
	if f.Message == "" || !contains(f.Message, "not implemented") {
		t.Errorf("an unimplemented rule set did not say so: %q", f.Message)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOfSub(s, sub) >= 0)
}

func indexOfSub(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
