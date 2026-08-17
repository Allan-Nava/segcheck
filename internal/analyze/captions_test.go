package analyze

import (
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// The defect SC-37 exists for: the manifest declares CC1, the encoder stopped
// emitting it, and nothing in the manifest changed — so no manifest-level checker
// will ever notice. In several countries this one is a legal obligation rather
// than a quality target.
func TestRun_CaptionsDeclaredButAbsent(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	srv := newHLSOrigin(t, []variantSpec{{
		name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720,
		captions: `CLOSED-CAPTIONS="cc"`, captionIDs: []string{"CC1"},
		segments: segs,
	}})
	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "captions", finding.BAD)
	if !ok {
		t.Fatalf("CC1 declared over a bitstream that carries no captions was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "CC1") {
		t.Errorf("finding does not name the declared channel: %q", f.Message)
	}
}

// Declared and present is the case that must stay quiet.
func TestRun_CaptionsDeclaredAndPresent(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	for i := range segs {
		segs[i].captions608 = []int{mediatest.CCTypeField1}
	}
	srv := newHLSOrigin(t, []variantSpec{{
		name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720,
		captions: `CLOSED-CAPTIONS="cc"`, captionIDs: []string{"CC1"},
		segments: segs,
	}})
	res := runOn(t, srv.URL+"/master.m3u8")

	for _, f := range res.Findings {
		if f.Status != finding.OK {
			t.Errorf("captions that are there produced %s on %s/%s: %s", f.Status, f.Check, f.Target, f.Message)
		}
	}
	if !hasCheck(res, "captions") {
		t.Error("no captions finding at all: the check did not run")
	}
}

// CC1 and CC3 share CEA-608 field 1. A declaration of CC3 over a field 1 that
// carries data cannot be confirmed to be CC3 specifically — that needs the
// line-21 control codes decoded — so it must not be reported as a defect either.
func TestRun_CaptionsChannelSharingAFieldIsNotADefect(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	for i := range segs {
		segs[i].captions608 = []int{mediatest.CCTypeField1}
	}
	srv := newHLSOrigin(t, []variantSpec{{
		name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720,
		captions: `CLOSED-CAPTIONS="cc"`, captionIDs: []string{"CC3"},
		segments: segs,
	}})
	res := runOn(t, srv.URL+"/master.m3u8")

	if f, ok := findFinding(res, "captions", finding.BAD); ok {
		t.Errorf("CC3 over a populated field 1 was reported as a defect: %q", f.Message)
	}
	// The declaration of CC2 is on field 2, which carries nothing: that is a
	// defect, and it proves the check is not simply silent about everything.
	srv2 := newHLSOrigin(t, []variantSpec{{
		name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720,
		captions: `CLOSED-CAPTIONS="cc"`, captionIDs: []string{"CC2"},
		segments: segs,
	}})
	if _, ok := findFinding(runOn(t, srv2.URL+"/master.m3u8"), "captions", finding.BAD); !ok {
		t.Error("CC2 over an empty field 2 was not reported")
	}
}

// CEA-708 names its services in the DTVCC packet layer, so SERVICE3 declared over
// a bitstream carrying only SERVICE1 is a defect a reader can be sure of.
func TestRun_CaptionsWrongServiceNumber(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	for i := range segs {
		segs[i].captions708 = []int{1}
	}
	srv := newHLSOrigin(t, []variantSpec{{
		name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720,
		captions: `CLOSED-CAPTIONS="cc"`, captionIDs: []string{"SERVICE3"},
		segments: segs,
	}})
	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "captions", finding.BAD)
	if !ok {
		t.Fatalf("SERVICE3 declared over a stream carrying SERVICE1 was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "SERVICE3") {
		t.Errorf("finding does not name the missing service: %q", f.Message)
	}
}

// CLOSED-CAPTIONS=NONE is a positive claim that there are none. Captions in the
// bitstream contradict it, and a player that believes the manifest will never
// offer them: the toggle is simply not there.
func TestRun_CaptionsPresentButDeclaredNone(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	for i := range segs {
		segs[i].captions608 = []int{mediatest.CCTypeField1}
	}
	srv := newHLSOrigin(t, []variantSpec{{
		name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720,
		captions: "CLOSED-CAPTIONS=NONE", segments: segs,
	}})
	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "captions", finding.WARN)
	if !ok {
		t.Fatalf("captions over a CLOSED-CAPTIONS=NONE variant were not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "NONE") {
		t.Errorf("finding does not name the contradicted claim: %q", f.Message)
	}
}

// No attribute at all is not the same claim as NONE. Report what is there and
// stay at OK: the manifest's silence is not the stream's fault.
func TestRun_CaptionsUndeclaredIsReportedNotFlagged(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	for i := range segs {
		segs[i].captions608 = []int{mediatest.CCTypeField1}
	}
	srv := newHLSOrigin(t, []variantSpec{{
		name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720,
		segments: segs,
	}})
	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "captions", finding.OK)
	if !ok {
		t.Fatalf("undeclared captions produced no finding at all.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "field 1") {
		t.Errorf("finding does not report what was found: %q", f.Message)
	}
	for _, g := range res.Findings {
		if g.Check == "captions" && g.Status != finding.OK {
			t.Errorf("undeclared captions produced %s: %s", g.Status, g.Message)
		}
	}
}

// A stream that declares nothing and carries nothing has nothing to say. A
// finding per rendition saying "no captions" would be noise on the large majority
// of the world's streams.
func TestCheckCaptions_SilentWhenNeitherDeclaredNorPresent(t *testing.T) {
	rd := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video},
		segs: []segmentData{{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{
			{Kind: media.Video, Captions: media.CaptionPresence{Scanned: true}},
		}}}},
	}
	if out := checkCaptions([]*renditionData{rd}); len(out) != 0 {
		t.Errorf("want no findings, got %+v", out)
	}
}

// A bitstream nobody could walk is a limit of this tool, not an absence in the
// stream: declared captions there get an ERROR saying the coverage has a hole,
// never a BAD that sends someone hunting a phantom.
func TestCheckCaptions_UnscannableBitstreamIsAnError(t *testing.T) {
	rd := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video,
			Captions: []manifest.Caption{{InstreamID: "CC1"}}},
		segs: []segmentData{{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{
			{Kind: media.Video, Captions: media.CaptionPresence{Scanned: false}},
		}}}},
	}
	out := checkCaptions([]*renditionData{rd})
	if len(out) != 1 || out[0].Status != finding.ERROR {
		t.Fatalf("want one ERROR finding, got %+v", out)
	}
	if !strings.Contains(out[0].Message, "could not") {
		t.Errorf("the finding should say segcheck could not look: %q", out[0].Message)
	}
}

// A CMAF caption track states which standard it carries and no more, so a channel
// declared against one can be neither confirmed nor contradicted. Report what is
// there and stay at OK: a BAD would send someone hunting a phantom, and an ERROR
// would claim segcheck could not look when it plainly did.
func TestCheckCaptions_CMAFTrackIsNotAttributable(t *testing.T) {
	rd := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video,
			Captions: []manifest.Caption{{InstreamID: "CC1"}}},
		segs: []segmentData{{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{
			{Kind: media.Video, Captions: media.CaptionPresence{Scanned: true, Track608: true}},
		}}}},
	}
	out := checkCaptions([]*renditionData{rd})
	if len(out) != 1 || out[0].Status != finding.OK {
		t.Fatalf("want one OK finding, got %+v", out)
	}
	if !strings.Contains(out[0].Message, "CEA-608 caption track") {
		t.Errorf("the finding should name what was found: %q", out[0].Message)
	}
}

// The CMAF form of the defect: the caption track is declared and still in the
// segment, but carries no samples. That is an encoder that stopped emitting
// captions, and it is a BAD.
func TestCheckCaptions_CMAFTrackWithNoSamples(t *testing.T) {
	rd := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video,
			Captions: []manifest.Caption{{InstreamID: "CC1"}}},
		segs: []segmentData{{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{
			{Kind: media.Video, Captions: media.CaptionPresence{Scanned: true}},
		}}}},
	}
	out := checkCaptions([]*renditionData{rd})
	if len(out) != 1 || out[0].Status != finding.BAD {
		t.Fatalf("want one BAD finding, got %+v", out)
	}
}

// CLOSED-CAPTIONS=NONE over a bitstream that indeed carries none is the manifest
// telling the truth, and it is worth saying so: the check ran and agreed.
func TestCheckCaptions_NoneDeclaredAndNonePresent(t *testing.T) {
	rd := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video, CaptionsNone: true},
		segs: []segmentData{{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{
			{Kind: media.Video, Captions: media.CaptionPresence{Scanned: true}},
		}}}},
	}
	out := checkCaptions([]*renditionData{rd})
	if len(out) != 1 || out[0].Status != finding.OK {
		t.Fatalf("want one OK finding, got %+v", out)
	}
}

// A rendition nobody could load, and one that declares nothing over a bitstream
// nobody could walk: neither has anything to report.
func TestCheckCaptions_QuietCases(t *testing.T) {
	quiet := []*renditionData{
		{r: manifest.Rendition{Name: "broken", Kind: manifest.Video}, err: errUnusable},
		{r: manifest.Rendition{Name: "720p", Kind: manifest.Video},
			segs: []segmentData{{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{
				{Kind: media.Video, Captions: media.CaptionPresence{Scanned: false}},
			}}}}},
		// An audio rendition has no video track to walk.
		{r: manifest.Rendition{Name: "audio", Kind: manifest.Audio},
			segs: []segmentData{{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{
				{Kind: media.Audio},
			}}}}},
	}
	if out := checkCaptions(quiet); len(out) != 0 {
		t.Errorf("want no findings, got %+v", out)
	}
}

// The vocabulary of INSTREAM-IDs, and what each one can be checked against.
func TestCaptionCouldBePresent(t *testing.T) {
	both := media.CaptionPresence{Field1: true, Field2: true, Services: []int{1, 3}}
	for _, tc := range []struct {
		id   string
		got  media.CaptionPresence
		want bool
	}{
		{"CC1", both, true},
		{"CC4", both, true},
		{"SERVICE3", both, true},
		{"SERVICE2", both, false},
		{"CC2", media.CaptionPresence{Field1: true}, false},
		// A CMAF caption track cannot confirm or deny any particular channel.
		{"CC2", media.CaptionPresence{Track608: true}, true},
		{"SERVICE9", media.CaptionPresence{Track708: true}, true},
		// Out-of-range and unparseable ids are not evidence of anything.
		{"SERVICE0", both, true},
		{"SERVICE64", both, true},
		{"SERVICEX", both, true},
		{"WHAT", both, true},
	} {
		if got := captionCouldBePresent(tc.id, tc.got); got != tc.want {
			t.Errorf("captionCouldBePresent(%q, %+v) = %v, want %v", tc.id, tc.got, got, tc.want)
		}
	}
}

// What the report says it found, for every combination it can find.
func TestHumanCaptions(t *testing.T) {
	for _, tc := range []struct {
		got  media.CaptionPresence
		want string
	}{
		{media.CaptionPresence{}, "no caption data"},
		{media.CaptionPresence{Field1: true}, "CEA-608 field 1 (CC1/CC3)"},
		{media.CaptionPresence{Field2: true}, "CEA-608 field 2 (CC2/CC4)"},
		{media.CaptionPresence{Field1: true, Field2: true}, "CEA-608 fields 1 and 2"},
		{media.CaptionPresence{Services: []int{1, 3}}, "CEA-708 SERVICE1/SERVICE3"},
		{media.CaptionPresence{Field1: true, Services: []int{1}}, "CEA-608 field 1 (CC1/CC3) and CEA-708 SERVICE1"},
		{media.CaptionPresence{Track608: true}, "a populated CEA-608 caption track (channel not attributable)"},
		{media.CaptionPresence{Track708: true}, "a populated CEA-708 caption track (service not attributable)"},
		// A caption track beside data that is attributable: the data wins, because
		// naming the track adds nothing the field has not already said.
		{media.CaptionPresence{Field1: true, Track608: true}, "CEA-608 field 1 (CC1/CC3)"},
	} {
		if got := humanCaptions(tc.got); got != tc.want {
			t.Errorf("humanCaptions(%+v) = %q, want %q", tc.got, got, tc.want)
		}
	}
}

// A variant that declares CLOSED-CAPTIONS=NONE over a bitstream nobody could walk
// has nothing to report: there is no claim to verify and no coverage hole worth
// naming, because nothing was promised.
func TestCheckCaptions_NoneDeclaredAndUnscannable(t *testing.T) {
	rd := &renditionData{
		r: manifest.Rendition{Name: "720p", Kind: manifest.Video, CaptionsNone: true},
		segs: []segmentData{{parsed: true, info: media.SegmentInfo{Tracks: []media.Track{
			{Kind: media.Video, Captions: media.CaptionPresence{Scanned: false}},
		}}}},
	}
	if out := checkCaptions([]*renditionData{rd}); len(out) != 0 {
		t.Errorf("want no findings, got %+v", out)
	}
}
