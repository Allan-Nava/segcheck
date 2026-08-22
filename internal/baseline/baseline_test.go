package baseline

import (
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
)

func res(rends []string, fs ...finding.Finding) finding.Result {
	return finding.Result{Source: "https://cdn.example/master.m3u8", Renditions: rends, Findings: fs}
}

func f(check, target string, status finding.Status, msg string) finding.Finding {
	return finding.Finding{Check: check, Target: target, Status: status, Message: msg}
}

func withVal(fd finding.Finding, v float64, unit string) finding.Finding {
	fd.Value, fd.Unit = finding.Num(v), unit
	return fd
}

func find(fs []finding.Finding, status finding.Status, substr string) (finding.Finding, bool) {
	for _, x := range fs {
		if x.Status == status && strings.Contains(x.Message, substr) {
			return x, true
		}
	}
	return finding.Finding{}, false
}

func dump(fs []finding.Finding) string {
	var b strings.Builder
	for _, x := range fs {
		b.WriteString("  " + string(x.Status) + " " + x.Check + " " + x.Target + " — " + x.Message + "\n")
	}
	return b.String()
}

// The whole point of the item: a check that was fine and is not any more. This
// is the finding a regression gate exists to produce.
func TestCompare_AChecksStatusGettingWorseIsARegression(t *testing.T) {
	base := res([]string{"720p"}, f("continuity", "720p", finding.OK, "timeline continuous"))
	now := res([]string{"720p"}, f("continuity", "720p", finding.BAD, "gap of +512ms"))

	got := Compare(base, now)

	fd, ok := find(got, finding.BAD, "OK")
	if !ok {
		t.Fatalf("no regression reported for OK → BAD:\n%s", dump(got))
	}
	if !strings.Contains(fd.Message, "BAD") {
		t.Errorf("the finding does not name both statuses: %q", fd.Message)
	}
	if fd.Target != "720p" {
		t.Errorf("target = %q, want the thing that regressed", fd.Target)
	}
}

// The other direction is worth saying too, and must not be a problem. A gate
// that only ever reports bad news gives an operator no way to see a fix land.
func TestCompare_AChecksStatusImprovingIsNotAProblem(t *testing.T) {
	base := res([]string{"720p"}, f("continuity", "720p", finding.BAD, "gap of +512ms"))
	now := res([]string{"720p"}, f("continuity", "720p", finding.OK, "timeline continuous"))

	got := Compare(base, now)

	if _, ok := find(got, finding.OK, "BAD"); !ok {
		t.Fatalf("an improvement was not reported at OK level:\n%s", dump(got))
	}
	for _, x := range got {
		if finding.AtLeast(x.Status, finding.WARN) {
			t.Errorf("an improvement produced %s: %s", x.Status, x.Message)
		}
	}
}

// "A rung that lost 30% of its bitrate" — the example the item is written
// around. Same check, same target, same unit, a measurement that moved.
func TestCompare_AMeasurementThatMovedBeyondTheToleranceIsReported(t *testing.T) {
	base := res([]string{"720p"}, withVal(f("bitrate", "720p", finding.OK, "measured average"), 2_000_000, "bps"))
	now := res([]string{"720p"}, withVal(f("bitrate", "720p", finding.OK, "measured average"), 1_400_000, "bps"))

	got := Compare(base, now)

	fd, ok := find(got, finding.WARN, "30")
	if !ok {
		t.Fatalf("a 30%% drop was not reported:\n%s", dump(got))
	}
	if fd.Value == nil || *fd.Value > -29.9 || *fd.Value < -30.1 {
		t.Errorf("Value = %v, want the percentage change so nothing has to parse the prose", fd.Value)
	}
	if fd.Unit != "%" {
		t.Errorf("Unit = %q, want %%", fd.Unit)
	}
}

// A stream is measured, not computed: the same rendition sampled twice gives
// slightly different numbers every time. A diff that fired on that would report
// every run as a change and be turned off within a day.
func TestCompare_MeasurementNoiseIsNotAChange(t *testing.T) {
	base := res([]string{"720p"}, withVal(f("bitrate", "720p", finding.OK, "avg"), 2_000_000, "bps"))
	now := res([]string{"720p"}, withVal(f("bitrate", "720p", finding.OK, "avg"), 2_040_000, "bps"))

	if got := Compare(base, now); len(got) != 0 {
		t.Errorf("a 2%% wobble was reported as a change:\n%s", dump(got))
	}
}

// "A rendition that disappeared." The ladder is what a player chooses between,
// so a rung vanishing costs every viewer that was on it.
func TestCompare_ARenditionThatDisappearedIsReported(t *testing.T) {
	base := res([]string{"1080p", "720p", "360p"})
	now := res([]string{"1080p", "360p"})

	got := Compare(base, now)

	fd, ok := find(got, finding.BAD, "720p")
	if !ok {
		t.Fatalf("a rendition that vanished was not reported:\n%s", dump(got))
	}
	if !strings.Contains(fd.Message, "no longer") && !strings.Contains(fd.Message, "gone") {
		t.Errorf("the message does not say it disappeared: %q", fd.Message)
	}
}

// A rung appearing is a ladder change too, and not a defect: someone added it.
func TestCompare_ARenditionThatAppearedIsNotAProblem(t *testing.T) {
	got := Compare(res([]string{"1080p"}), res([]string{"1080p", "720p"}))

	if _, ok := find(got, finding.OK, "720p"); !ok {
		t.Fatalf("a new rendition was not mentioned:\n%s", dump(got))
	}
	for _, x := range got {
		if finding.AtLeast(x.Status, finding.WARN) {
			t.Errorf("a new rendition produced %s: %s", x.Status, x.Message)
		}
	}
}

// A check that spoke in the baseline and says nothing now is the failure mode
// this project treats as worst: silence reads exactly like a pass. It is an
// ERROR because the coverage has a hole, not because the stream is broken.
func TestCompare_ACheckThatFellSilentIsAnERROR(t *testing.T) {
	base := res([]string{"720p"},
		f("captions", "720p", finding.OK, "CC1 present"),
		f("continuity", "720p", finding.OK, "continuous"))
	now := res([]string{"720p"}, f("continuity", "720p", finding.OK, "continuous"))

	got := Compare(base, now)

	fd, ok := find(got, finding.ERROR, "captions")
	if !ok {
		t.Fatalf("a check that stopped reporting was not flagged:\n%s", dump(got))
	}
	if !strings.Contains(fd.Hint, "silen") {
		t.Errorf("the hint does not explain why silence matters: %q", fd.Hint)
	}
}

// A check that is new is not a hole. It is a segcheck upgrade, which is the
// common reason for it, and reporting it as missing coverage would make every
// version bump look like a regression.
func TestCompare_ANewCheckIsNotAHole(t *testing.T) {
	base := res([]string{"720p"}, f("continuity", "720p", finding.OK, "continuous"))
	now := res([]string{"720p"},
		f("continuity", "720p", finding.OK, "continuous"),
		f("byterange", "cdn.example", finding.OK, "honours Range"))

	got := Compare(base, now)

	for _, x := range got {
		if x.Status == finding.ERROR {
			t.Errorf("a newly added check was reported as a hole: %s", x.Message)
		}
	}
}

// Segment-scoped targets are deliberately not diffed. A live stream has
// different segments every run, so `720p seg 38` is never `720p seg 41`: pairing
// them would report the whole sample as vanished and a new one as appeared,
// every single run. This is the same reason the metric renderers carry no target
// label.
func TestCompare_TheSampleMovingIsNotAChange(t *testing.T) {
	base := res([]string{"720p"},
		f("continuity", "720p seg 38", finding.BAD, "gap of +512ms"),
		f("continuity", "720p", finding.BAD, "1 gap"))
	now := res([]string{"720p"},
		f("continuity", "720p seg 41", finding.BAD, "gap of +512ms"),
		f("continuity", "720p", finding.BAD, "1 gap"))

	if got := Compare(base, now); len(got) != 0 {
		t.Errorf("the sample moving was reported as a change:\n%s", dump(got))
	}
}

// A finding with no measurement keeps it in the message — the `resolution`
// check's "coded 1280x720 matches the declared resolution" is the whole
// observation. For those, and only those, the message is what gets compared:
// this is how "a resolution that moved" is caught when both runs agree with
// their own manifest and neither is a defect.
func TestCompare_AChangedMessageOnAFindingWithNoMeasurementIsReported(t *testing.T) {
	base := res([]string{"720p"}, f("resolution", "720p", finding.OK, "coded 1280x720 matches the declared resolution"))
	now := res([]string{"720p"}, f("resolution", "720p", finding.OK, "coded 960x540 matches the declared resolution"))

	got := Compare(base, now)

	if _, ok := find(got, finding.WARN, "960x540"); !ok {
		t.Fatalf("a resolution that moved was not reported:\n%s", dump(got))
	}
}

// The mirror of the above: a finding that carries a measurement must be
// compared on the number, never the prose. Its message restates the value, so
// diffing the text would fire on every run the measurement wobbled — the noise
// the tolerance exists to absorb.
func TestCompare_AMessageIsNotComparedWhenThereIsAMeasurement(t *testing.T) {
	base := res([]string{"720p"}, withVal(f("bitrate", "720p", finding.OK, "measured average 2.00 Mbps"), 2_000_000, "bps"))
	now := res([]string{"720p"}, withVal(f("bitrate", "720p", finding.OK, "measured average 2.04 Mbps"), 2_040_000, "bps"))

	if got := Compare(base, now); len(got) != 0 {
		t.Errorf("a restated measurement was diffed as prose:\n%s", dump(got))
	}
}

// Two runs of the same healthy stream produce nothing. A diff that always has
// something to say is a diff nobody reads.
func TestCompare_NothingChangedSaysNothing(t *testing.T) {
	r := res([]string{"720p", "360p"},
		withVal(f("bitrate", "720p", finding.OK, "avg"), 2_000_000, "bps"),
		f("resolution", "720p", finding.OK, "coded 1280x720 matches"))

	if got := Compare(r, r); len(got) != 0 {
		t.Errorf("comparing a run with itself produced findings:\n%s", dump(got))
	}
}

// A unit that changed means the two numbers are not comparable, and pretending
// otherwise would report a conversion as a regression.
func TestCompare_AChangedUnitIsNotComparedAsANumber(t *testing.T) {
	base := res([]string{"720p"}, withVal(f("bitrate", "720p", finding.OK, "avg"), 2_000_000, "bps"))
	now := res([]string{"720p"}, withVal(f("bitrate", "720p", finding.OK, "avg"), 2, "Mbps"))

	for _, x := range Compare(base, now) {
		if strings.Contains(x.Message, "%") {
			t.Errorf("two different units were compared as a percentage: %s", x.Message)
		}
	}
}

// The case a CLI test caught that unit-level pairing missed. A check often
// reports an OK about the rendition and a BAD about one of its segments, so when
// a stream breaks, the rendition-level finding is *replaced* by a segment-level
// one. Pairing on the exact target loses that entirely: `continuity/720p`
// disappears, `continuity/720p seg 2` is segment-scoped and skipped, and the
// regression the gate exists for goes unreported.
//
// So the comparison is per check *per rendition*, folding in whatever the check
// said about that rendition's segments. The segment number never enters the key,
// so the sample moving still costs nothing.
func TestCompare_ARenditionLevelOKBecomingASegmentLevelBADIsARegression(t *testing.T) {
	base := res([]string{"720p"}, f("continuity", "720p", finding.OK, "timeline continuous across 3 boundaries"))
	now := res([]string{"720p"}, f("continuity", "720p seg 2", finding.BAD, "gap of +500ms"))

	got := Compare(base, now)

	fd, ok := find(got, finding.BAD, "continuity")
	if !ok {
		t.Fatalf("a rendition whose check went from OK to a segment-level BAD was not reported:\n%s", dump(got))
	}
	if !strings.Contains(fd.Target, "720p") {
		t.Errorf("target = %q, want the rendition it is about", fd.Target)
	}
	// And it must not also claim the check fell silent: it spoke, about a segment.
	if _, silent := find(got, finding.ERROR, "reports nothing"); silent {
		t.Errorf("the check was also reported as silent:\n%s", dump(got))
	}
}

// The mirror: a fix landing. The segment-level BAD is gone and the
// rendition-level OK is back.
func TestCompare_ASegmentLevelBADBecomingARenditionLevelOKIsAnImprovement(t *testing.T) {
	base := res([]string{"720p"}, f("continuity", "720p seg 2", finding.BAD, "gap of +500ms"))
	now := res([]string{"720p"}, f("continuity", "720p", finding.OK, "timeline continuous"))

	got := Compare(base, now)

	if _, ok := find(got, finding.OK, "continuity"); !ok {
		t.Fatalf("a fix was not reported:\n%s", dump(got))
	}
	for _, x := range got {
		if finding.AtLeast(x.Status, finding.WARN) {
			t.Errorf("a fix produced %s: %s", x.Status, x.Message)
		}
	}
}

// Two segments of one rendition, one of them bad: the rendition's worst is what
// counts, so a second bad segment appearing is not a second finding.
func TestCompare_TheWorstAcrossARenditionsSegmentsIsWhatCounts(t *testing.T) {
	base := res([]string{"720p"},
		f("continuity", "720p seg 1", finding.BAD, "gap"),
		f("continuity", "720p", finding.OK, "mostly fine"))
	now := res([]string{"720p"},
		f("continuity", "720p seg 3", finding.BAD, "gap"),
		f("continuity", "720p seg 4", finding.BAD, "gap"),
		f("continuity", "720p", finding.OK, "mostly fine"))

	if got := Compare(base, now); len(got) != 0 {
		t.Errorf("the same worst status on different segments was reported as a change:\n%s", dump(got))
	}
}

// One check reporting a change on several rungs. The order has to be stable, or
// two identical comparisons print differently and a diff of the diff is noise.
func TestCompare_SeveralRungsOfOneCheckAreReportedInAStableOrder(t *testing.T) {
	base := res([]string{"1080p", "360p", "720p"},
		withVal(f("bitrate", "1080p", finding.OK, "avg"), 4_000_000, "bps"),
		withVal(f("bitrate", "720p", finding.OK, "avg"), 2_000_000, "bps"),
		withVal(f("bitrate", "360p", finding.OK, "avg"), 800_000, "bps"))
	now := res([]string{"1080p", "360p", "720p"},
		withVal(f("bitrate", "1080p", finding.OK, "avg"), 2_000_000, "bps"),
		withVal(f("bitrate", "720p", finding.OK, "avg"), 1_000_000, "bps"),
		withVal(f("bitrate", "360p", finding.OK, "avg"), 400_000, "bps"))

	first := Compare(base, now)
	if len(first) != 3 {
		t.Fatalf("want a finding per rung, got %d:\n%s", len(first), dump(first))
	}
	for i := 0; i < 20; i++ {
		again := Compare(base, now)
		for j := range first {
			if again[j].Target != first[j].Target {
				t.Fatalf("run %d ordered the diff differently: %s before %s", i, again[j].Target, first[j].Target)
			}
		}
	}
	// Within one status, by target: the order the comparator states.
	if first[0].Target != "1080p" || first[1].Target != "360p" || first[2].Target != "720p" {
		t.Errorf("order = %s/%s/%s, want 1080p/360p/720p",
			first[0].Target, first[1].Target, first[2].Target)
	}
}

// Anything over nothing is infinite, so a baseline of zero cannot be a
// percentage. It is reported as the absolute move instead — a check that
// measured nothing last week and something now is worth saying, and "+Inf%"
// is not a measurement.
func TestCompare_AMeasurementThatWasZeroIsReportedAbsolutely(t *testing.T) {
	base := res([]string{"720p"}, withVal(f("continuity", "720p", finding.OK, "no gaps"), 0, "gaps"))
	now := res([]string{"720p"}, withVal(f("continuity", "720p", finding.OK, "gaps"), 3, "gaps"))

	got := Compare(base, now)

	fd, ok := find(got, finding.WARN, "from 0 to 3")
	if !ok {
		t.Fatalf("a move away from zero was not reported:\n%s", dump(got))
	}
	if fd.Unit == "%" {
		t.Error("a change from zero was expressed as a percentage")
	}
	if fd.Value == nil || *fd.Value != 3 {
		t.Errorf("Value = %v, want the measurement itself", fd.Value)
	}
}
