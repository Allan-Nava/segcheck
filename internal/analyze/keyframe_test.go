package analyze

import (
	"errors"
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// SC-16, at the check level.
//
// The BAD is reserved for a segment carrying no random access point at all: that
// one genuinely cannot be switched into, and no manifest-level tool can see it —
// the boundaries can be perfectly aligned, every duration correct and the ladder
// flawless while switching is broken for everyone.
//
// Two weaker cases must stay at OK, and both were learned from real streams rather
// than reasoned out. A segment that carries a keyframe but not as its first
// picture is what Apple's byte-range bipbop reference stream does, because its
// segment boundaries fall on transport packets rather than access units; players
// start at the keyframe and it plays everywhere. And a rendition whose segments
// state nothing at all is the tool's limit, not a defect: an fMP4 fragment need
// carry no sample flags.

// kfNone is a segment the bitstream was walked for and that carries no random
// access point at all: the unswitchable case.
func kfNone() media.Track {
	t := videoTrack()
	t.KeyframeKnown, t.KeyframeScanned = true, true
	t.OpensOnKeyframe, t.HasKeyframe = false, false
	return t
}

// kfOpens is the healthy case: the first picture is a keyframe.
func kfOpens() media.Track {
	t := videoTrack()
	t.KeyframeKnown, t.KeyframeScanned = true, true
	t.OpensOnKeyframe, t.HasKeyframe = true, true
	return t
}

// kfLate is Apple's byte-range shape: a keyframe is there, just not first.
func kfLate() media.Track {
	t := videoTrack()
	t.KeyframeKnown, t.KeyframeScanned = true, true
	t.OpensOnKeyframe, t.HasKeyframe = false, true
	return t
}

// kfUnknown states nothing at all.
func kfUnknown() media.Track { return videoTrack() }

// The defect: a segment with no random access point anywhere. This is the one
// that genuinely cannot be switched into, and it is attributed to the exact
// segment because "somewhere in this rendition" is not actionable.
func TestCheckKeyframe_SegmentWithNoKeyframeAtAll(t *testing.T) {
	rd := rend("720p", withSegs(
		okSeg(1, media.ContainerTS, kfOpens()),
		okSeg(2, media.ContainerTS, kfNone()), // the offender
		okSeg(3, media.ContainerTS, kfOpens()),
	))

	got := checkKeyframe([]*renditionData{rd})
	f, ok := findIn(got, "keyframe", finding.BAD)
	if !ok {
		t.Fatalf("no BAD for a segment opening on a non-keyframe:\n%s", dumpFindings(got))
	}
	// Attribution is the point: "somewhere in this rendition" is not actionable.
	if !strings.Contains(f.Target, "seg 2") {
		t.Errorf("target = %q, want it to name segment 2", f.Target)
	}
	if f.Hint == "" {
		t.Error("no hint saying what this means for the viewer")
	}
	if !strings.Contains(strings.ToLower(f.Hint+f.Message), "switch") {
		t.Errorf("message/hint = %q / %q, want them to explain the switching failure", f.Message, f.Hint)
	}
}

// Every segment opening correctly is the healthy case, and it must be reported at
// OK rather than in silence: a check that says nothing is indistinguishable from
// a check that did not run.
func TestCheckKeyframe_EverySegmentOpensCorrectly(t *testing.T) {
	rd := rend("720p", withSegs(
		okSeg(1, media.ContainerTS, kfOpens()),
		okSeg(2, media.ContainerTS, kfOpens()),
	))
	got := checkKeyframe([]*renditionData{rd})

	for _, f := range got {
		if finding.AtLeast(f.Status, finding.WARN) {
			t.Errorf("a healthy rendition produced %s: %s", f.Status, f.Message)
		}
	}
	if _, ok := findIn(got, "keyframe", finding.OK); !ok {
		t.Errorf("no OK finding for a rendition whose segments all open on a keyframe:\n%s", dumpFindings(got))
	}
}

// Several offenders are counted rather than reported one per segment, so a
// rendition that is wrong throughout does not bury the rest of the report.
func TestCheckKeyframe_CountsTheOffenders(t *testing.T) {
	rd := rend("720p", withSegs(
		okSeg(1, media.ContainerTS, kfNone()),
		okSeg(2, media.ContainerTS, kfNone()),
		okSeg(3, media.ContainerTS, kfOpens()),
		okSeg(4, media.ContainerTS, kfNone()),
	))
	got := checkKeyframe([]*renditionData{rd})

	var bad int
	for _, f := range got {
		if f.Status == finding.BAD {
			bad++
		}
	}
	if bad > 2 {
		t.Errorf("got %d BAD findings for one rendition, want them summarised:\n%s", bad, dumpFindings(got))
	}
	f, _ := findIn(got, "keyframe", finding.BAD)
	if !strings.Contains(f.Message, "3/4") {
		t.Errorf("message = %q, want it to say 3/4", f.Message)
	}
}

// A rendition that states nothing about its keyframes is the tool's limit, not a
// defect in the stream. It gets an honest OK-level note saying so — never a BAD
// that sends someone hunting a phantom.
func TestCheckKeyframe_UnverifiableIsNotADefect(t *testing.T) {
	rd := rend("720p", withSegs(
		okSeg(1, media.ContainerMP4, kfUnknown()),
		okSeg(2, media.ContainerMP4, kfUnknown()),
	))
	got := checkKeyframe([]*renditionData{rd})

	for _, f := range got {
		if finding.AtLeast(f.Status, finding.WARN) {
			t.Errorf("an unverifiable rendition produced %s: %s", f.Status, f.Message)
		}
	}
	f, ok := findIn(got, "keyframe", finding.OK)
	if !ok {
		t.Fatalf("no finding at all for an unverifiable rendition:\n%s", dumpFindings(got))
	}
	if !strings.Contains(strings.ToLower(f.Message), "not") {
		t.Errorf("message = %q, want it to say the segments do not state it", f.Message)
	}
}

// A partly readable rendition is judged on what could be read, and the ones that
// could not must not be counted as offenders.
func TestCheckKeyframe_MixedKnownAndUnknown(t *testing.T) {
	rd := rend("720p", withSegs(
		okSeg(1, media.ContainerTS, kfOpens()),
		okSeg(2, media.ContainerTS, kfUnknown()), // unreadable
		okSeg(3, media.ContainerTS, kfNone()),    // a real offender
	))
	got := checkKeyframe([]*renditionData{rd})

	f, ok := findIn(got, "keyframe", finding.BAD)
	if !ok {
		t.Fatalf("the readable offender was not reported:\n%s", dumpFindings(got))
	}
	// One of the two readable segments is wrong, not one of three.
	if !strings.Contains(f.Message, "1/2") {
		t.Errorf("message = %q, want it counted against the 2 readable segments", f.Message)
	}
}

// A keyframe present but not first: Apple's byte-range reference stream. Reported
// at OK with the count, because it is worth knowing and not worth alarming over —
// this exact shape was what a stricter first draft of this check flagged as BAD on
// a canonical stream that plays everywhere.
func TestCheckKeyframe_KeyframePresentButNotFirstIsNotADefect(t *testing.T) {
	rd := rend("720p", withSegs(
		okSeg(1, media.ContainerTS, kfOpens()),
		okSeg(2, media.ContainerTS, kfLate()),
		okSeg(3, media.ContainerTS, kfLate()),
	))
	got := checkKeyframe([]*renditionData{rd})

	for _, f := range got {
		if finding.AtLeast(f.Status, finding.WARN) {
			t.Errorf("a byte-range-shaped rendition produced %s: %s", f.Status, f.Message)
		}
	}
	f, ok := findIn(got, "keyframe", finding.OK)
	if !ok {
		t.Fatalf("no finding at all:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "2/3") {
		t.Errorf("message = %q, want it to count the 2 of 3", f.Message)
	}
	if !strings.Contains(f.Message, "not as their first picture") {
		t.Errorf("message = %q, want it to say what was seen", f.Message)
	}
}

// A segment inside a video rendition that carries no video track at all — an
// audio-only segment served by mistake, which `tracks` reports on — has no
// keyframe to judge. It must be passed over rather than counted as an offender,
// or one check's finding becomes two.
func TestCheckKeyframe_SkipsSegmentsWithNoVideoTrack(t *testing.T) {
	rd := rend("720p", withSegs(
		okSeg(1, media.ContainerTS, kfOpens()),
		okSeg(2, media.ContainerTS, audioTrack()), // no video in this one
	))
	got := checkKeyframe([]*renditionData{rd})

	if f, ok := findIn(got, "keyframe", finding.BAD); ok {
		t.Errorf("a segment with no video track was counted as an offender: %s", f.Message)
	}
	f, ok := findIn(got, "keyframe", finding.OK)
	if !ok {
		t.Fatalf("no finding for the readable segment:\n%s", dumpFindings(got))
	}
	// Judged on the one segment that had video, not on both.
	if !strings.Contains(f.Message, "all 1 ") {
		t.Errorf("message = %q, want it counted against the 1 segment carrying video", f.Message)
	}
}

// Audio renditions have no keyframes to speak of and must produce nothing at all.
func TestCheckKeyframe_IgnoresAudio(t *testing.T) {
	rd := rend("audio", withSegs(okSeg(1, media.ContainerMP4, audioTrack())))
	rd.r.Kind = manifest.Audio

	if got := checkKeyframe([]*renditionData{rd}); len(got) != 0 {
		t.Errorf("checkKeyframe reported on an audio rendition:\n%s", dumpFindings(got))
	}
}

// A rendition that could not be sampled, or whose init is missing, has nothing to
// say — blaming the media for a fetch failure is what the rules forbid.
func TestCheckKeyframe_StaysQuietWithNothingToLookAt(t *testing.T) {
	failed := rend("720p")
	failed.err = errors.New("media playlist: 500")

	noInit := rend("1080p", withSegs(okSeg(1, media.ContainerMP4, kfNone())))
	noInit.initErr = errors.New("init.mp4: 404")

	if got := checkKeyframe([]*renditionData{failed, noInit, rend("360p")}); len(got) != 0 {
		t.Errorf("checkKeyframe spoke about renditions it could not look at:\n%s", dumpFindings(got))
	}
}
