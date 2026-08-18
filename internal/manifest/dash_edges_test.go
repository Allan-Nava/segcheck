package manifest

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// The MPD shapes that are not a plain SegmentTemplate: SegmentList, a timeline
// with an open-ended repeat, a representation that describes no segments at all,
// and the error paths where the manifest does not say enough to know how many
// segments exist.
//
// The rule these all serve is that segcheck must never invent a segment list. An
// MPD it cannot expand has to say so — an `Unsupported` string or an error the
// operator sees — rather than come back with zero findings, which reads as a
// stream that passed.

var epoch = time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

// SegmentList names every segment outright. It is the oldest of the three forms
// and still shipped by some packagers, and a rendition using it must be sampled
// rather than reported as unsupported.
func TestParseDASH_SegmentList(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT12S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <Representation id="v0" bandwidth="800000" width="1280" height="720">
        <SegmentList duration="90000" timescale="90000">
          <Initialization sourceURL="init.mp4"/>
          <SegmentURL media="seg1.m4s"/>
          <SegmentURL media="seg2.m4s"/>
          <SegmentURL media="seg3.m4s"/>
        </SegmentList>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example.com/dash/manifest.mpd", epoch)
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if len(pl.Renditions) != 1 {
		t.Fatalf("got %d renditions, want 1", len(pl.Renditions))
	}
	r := pl.Renditions[0]
	if r.Unsupported != "" {
		t.Errorf("a SegmentList rendition was reported unsupported: %q", r.Unsupported)
	}
	if len(r.Segments) != 3 {
		t.Fatalf("got %d segments, want 3", len(r.Segments))
	}
	if r.InitURI != "https://cdn.example.com/dash/init.mp4" {
		t.Errorf("InitURI = %q", r.InitURI)
	}
	if r.Segments[0].URI != "https://cdn.example.com/dash/seg1.m4s" {
		t.Errorf("segment 1 URI = %q", r.Segments[0].URI)
	}
	if r.Segments[0].Duration != 1 {
		t.Errorf("segment duration = %v, want 1s (90000/90000)", r.Segments[0].Duration)
	}
	if r.Segments[2].Sequence != 3 {
		t.Errorf("segment 3 sequence = %d, want 3", r.Segments[2].Sequence)
	}
	// The init segment is carried on each segment so the fetcher has it without
	// looking back at the rendition.
	if r.Segments[1].InitURI != r.InitURI {
		t.Errorf("segment 2 InitURI = %q, want the rendition's", r.Segments[1].InitURI)
	}
}

// A SegmentList with no @timescale counts in seconds, which is the schema
// default. Treating a missing timescale as zero would divide by it.
func TestParseDASH_SegmentListWithoutATimescale(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT8S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <Representation id="v0" bandwidth="800000">
        <SegmentList duration="4">
          <SegmentURL media="a.m4s"/>
          <SegmentURL media="b.m4s"/>
        </SegmentList>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example.com/m.mpd", epoch)
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	segs := pl.Renditions[0].Segments
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2", len(segs))
	}
	if segs[0].Duration != 4 {
		t.Errorf("duration = %v, want 4s with the default timescale of 1", segs[0].Duration)
	}
	// No Initialization element: there is no init segment to report.
	if pl.Renditions[0].InitURI != "" {
		t.Errorf("InitURI = %q, want empty", pl.Renditions[0].InitURI)
	}
}

// A representation with none of the three addressing forms cannot be sampled.
// That is a limit of the manifest, so it is recorded as unsupported — never
// silently skipped, and never reported as a defect in the media.
func TestParseDASH_RepresentationWithNoAddressing(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT10S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <Representation id="v0" bandwidth="800000"/>
    </AdaptationSet>
  </Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example.com/m.mpd", epoch)
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	r := pl.Renditions[0]
	if r.Unsupported == "" {
		t.Fatal("a representation with no addressing was not flagged unsupported")
	}
	if !strings.Contains(r.Unsupported, "SegmentTemplate") {
		t.Errorf("Unsupported = %q, want it to name what is missing", r.Unsupported)
	}
	if len(r.Segments) != 0 {
		t.Errorf("got %d segments for an unaddressable representation", len(r.Segments))
	}
}

// A SegmentTemplate error is recorded against the rendition rather than failing
// the whole run: the other renditions may be perfectly checkable.
func TestParseDASH_TemplateErrorIsRecordedPerRendition(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT10S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <Representation id="broken" bandwidth="400000">
        <SegmentTemplate initialization="init.mp4"/>
      </Representation>
      <Representation id="fine" bandwidth="800000">
        <SegmentTemplate media="$RepresentationID$/$Number$.m4s" duration="90000" timescale="90000"/>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example.com/m.mpd", epoch)
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if len(pl.Renditions) != 2 {
		t.Fatalf("got %d renditions, want 2", len(pl.Renditions))
	}
	var broken, fine *Rendition
	for i := range pl.Renditions {
		if strings.Contains(pl.Renditions[i].Name, "broken") || pl.Renditions[i].Bandwidth == 400000 {
			broken = &pl.Renditions[i]
		} else {
			fine = &pl.Renditions[i]
		}
	}
	if broken == nil || fine == nil {
		t.Fatalf("could not tell the two renditions apart: %+v", pl.Renditions)
	}
	if broken.Unsupported == "" || !strings.Contains(broken.Unsupported, "@media") {
		t.Errorf("the template with no @media gave Unsupported = %q", broken.Unsupported)
	}
	if len(fine.Segments) == 0 {
		t.Error("the healthy rendition lost its segments because a sibling was broken")
	}
}

// An MPD with no Representation anywhere describes nothing. Returning an empty
// success would be a run that found no problems with no media.
func TestParseDASH_NoRepresentationAtAll(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT10S">
  <Period><AdaptationSet mimeType="video/mp4"/></Period>
</MPD>`
	_, err := ParseDASH([]byte(mpd), "https://cdn.example.com/m.mpd", epoch)
	if err == nil {
		t.Fatal("an MPD with no Representation parsed cleanly")
	}
	if !strings.Contains(err.Error(), "no Representation") {
		t.Errorf("err = %v, want it to say there is no Representation", err)
	}
}

// ---------- SegmentTimeline edges ----------

// r="-1" repeats to the end of the period. The count comes from the period
// duration; without one there is nothing to derive it from, and guessing would
// invent segments that do not exist.
func TestExpandTemplate_OpenEndedRepeat(t *testing.T) {
	tmpl := &mpdSegTemplate{
		Media:     "$Number$.m4s",
		Timescale: 90000,
		SegmentTimeline: &mpdSegTimeline{S: []struct {
			T *int64 `xml:"t,attr"`
			D int64  `xml:"d,attr"`
			R int    `xml:"r,attr"`
		}{
			{D: 90000, R: -1},
		}},
	}
	base, _ := url.Parse("https://cdn.example.com/d/m.mpd")

	// A 10-second period of 1-second segments: ten of them.
	_, segs, _, err := expandTemplate(tmpl, mpdRepresentation{ID: "v"}, base, time.Time{}, epoch, 0, 10, 0, false, false)
	if err != nil {
		t.Fatalf("expandTemplate: %v", err)
	}
	if len(segs) != 10 {
		t.Errorf("got %d segments, want 10 derived from the period duration", len(segs))
	}

	// With no period duration the repeat cannot be resolved, so it counts as one.
	_, segs, _, err = expandTemplate(tmpl, mpdRepresentation{ID: "v"}, base, time.Time{}, epoch, 0, 0, 0, false, false)
	if err != nil {
		t.Fatalf("expandTemplate: %v", err)
	}
	if len(segs) != 1 {
		t.Errorf("got %d segments with no period duration, want the single stated one", len(segs))
	}
}

// A period shorter than the first segment leaves a negative repeat count, which
// must clamp to the one segment the timeline states rather than going negative.
func TestExpandTemplate_OpenEndedRepeatShorterThanOneSegment(t *testing.T) {
	start := int64(900000)
	tmpl := &mpdSegTemplate{
		Media:     "$Time$.m4s",
		Timescale: 90000,
		SegmentTimeline: &mpdSegTimeline{S: []struct {
			T *int64 `xml:"t,attr"`
			D int64  `xml:"d,attr"`
			R int    `xml:"r,attr"`
		}{
			{T: &start, D: 90000, R: -1},
		}},
	}
	base, _ := url.Parse("https://cdn.example.com/d/m.mpd")
	_, segs, _, err := expandTemplate(tmpl, mpdRepresentation{ID: "v"}, base, time.Time{}, epoch, 0, 2, 0, false, false)
	if err != nil {
		t.Fatalf("expandTemplate: %v", err)
	}
	if len(segs) != 1 {
		t.Errorf("got %d segments, want 1", len(segs))
	}
}

// An S with no @d states no duration, so there is no segment to address. It is
// skipped rather than emitted as a zero-length segment that every duration check
// would then report on.
func TestExpandTemplate_TimelineEntryWithoutADuration(t *testing.T) {
	tmpl := &mpdSegTemplate{
		Media:     "$Number$.m4s",
		Timescale: 90000,
		SegmentTimeline: &mpdSegTimeline{S: []struct {
			T *int64 `xml:"t,attr"`
			D int64  `xml:"d,attr"`
			R int    `xml:"r,attr"`
		}{
			{D: 0},     // nothing to address
			{D: 90000}, // and one that does
			{D: -1},    // a negative duration is equally unusable
		}},
	}
	base, _ := url.Parse("https://cdn.example.com/d/m.mpd")
	_, segs, _, err := expandTemplate(tmpl, mpdRepresentation{ID: "v"}, base, time.Time{}, epoch, 0, 10, 0, false, false)
	if err != nil {
		t.Fatalf("expandTemplate: %v", err)
	}
	if len(segs) != 1 {
		t.Errorf("got %d segments, want only the one with a duration", len(segs))
	}
}

// The first S may omit @t, in which case the timeline starts at zero.
func TestExpandTemplate_TimelineWithoutAnExplicitStart(t *testing.T) {
	tmpl := &mpdSegTemplate{
		Media:     "$Time$.m4s",
		Timescale: 90000,
		SegmentTimeline: &mpdSegTimeline{S: []struct {
			T *int64 `xml:"t,attr"`
			D int64  `xml:"d,attr"`
			R int    `xml:"r,attr"`
		}{
			{D: 90000, R: 1},
		}},
	}
	base, _ := url.Parse("https://cdn.example.com/d/m.mpd")
	_, segs, _, err := expandTemplate(tmpl, mpdRepresentation{ID: "v"}, base, time.Time{}, epoch, 0, 10, 0, false, false)
	if err != nil {
		t.Fatalf("expandTemplate: %v", err)
	}
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2", len(segs))
	}
	if !segs[0].HasDeclaredStart || segs[0].DeclaredStart != 0 {
		t.Errorf("first declared start = %v (have %v), want 0", segs[0].DeclaredStart, segs[0].HasDeclaredStart)
	}
	if segs[1].DeclaredStart != 1 {
		t.Errorf("second declared start = %v, want 1", segs[1].DeclaredStart)
	}
	if segs[0].URI != "https://cdn.example.com/d/0.m4s" {
		t.Errorf("first URI = %q, want $Time$ substituted with 0", segs[0].URI)
	}
}

// A representation is bounded at maxExpandedSegments however many the manifest
// implies. An unbounded expansion is how a single malformed @r or a very long
// period turns a check into an out-of-memory kill, and the bound applies on both
// paths that build the list.
func TestExpandTemplate_BoundsTheSegmentListFromATimeline(t *testing.T) {
	tmpl := &mpdSegTemplate{
		Media:     "$Number$.m4s",
		Timescale: 1,
		SegmentTimeline: &mpdSegTimeline{S: []struct {
			T *int64 `xml:"t,attr"`
			D int64  `xml:"d,attr"`
			R int    `xml:"r,attr"`
		}{
			{D: 1, R: maxExpandedSegments + 5000},
		}},
	}
	base, _ := url.Parse("https://cdn.example.com/d/m.mpd")
	_, segs, _, err := expandTemplate(tmpl, mpdRepresentation{ID: "v"}, base, time.Time{}, epoch, 0, 0, 0, false, false)
	if err != nil {
		t.Fatalf("expandTemplate: %v", err)
	}
	if len(segs) != maxExpandedSegments {
		t.Errorf("got %d segments, want the %d cap", len(segs), maxExpandedSegments)
	}
}

func TestExpandTemplate_BoundsTheSegmentListFromADuration(t *testing.T) {
	tmpl := &mpdSegTemplate{Media: "$Number$.m4s", Timescale: 1, Duration: 1}
	base, _ := url.Parse("https://cdn.example.com/d/m.mpd")
	// A period long enough to imply more one-second segments than the cap allows.
	_, segs, _, err := expandTemplate(tmpl, mpdRepresentation{}, base, time.Time{}, epoch, 0, maxExpandedSegments+5000, 0, false, false)
	if err != nil {
		t.Fatalf("expandTemplate: %v", err)
	}
	if len(segs) != maxExpandedSegments {
		t.Errorf("got %d segments, want the %d cap", len(segs), maxExpandedSegments)
	}
}

// ---------- @duration edges ----------

// A template with neither a timeline nor @duration says nothing about how long a
// segment is, so the segment list cannot be derived at all.
func TestExpandTemplate_NeitherTimelineNorDuration(t *testing.T) {
	tmpl := &mpdSegTemplate{Media: "$Number$.m4s", Timescale: 90000}
	base, _ := url.Parse("https://cdn.example.com/d/m.mpd")
	if _, _, _, err := expandTemplate(tmpl, mpdRepresentation{}, base, time.Time{}, epoch, 0, 10, 0, false, false); err == nil {
		t.Fatal("a template with no duration and no timeline expanded")
	} else if !strings.Contains(err.Error(), "@duration") {
		t.Errorf("err = %v, want it to name @duration", err)
	}
}

// A static MPD with no mediaPresentationDuration gives no way to know how many
// segments exist. Guessing would either miss the tail or fetch 404s.
func TestExpandTemplate_StaticWithoutAPresentationDuration(t *testing.T) {
	tmpl := &mpdSegTemplate{Media: "$Number$.m4s", Timescale: 90000, Duration: 90000}
	base, _ := url.Parse("https://cdn.example.com/d/m.mpd")
	if _, _, _, err := expandTemplate(tmpl, mpdRepresentation{}, base, time.Time{}, epoch, 0, 0, 0, false, false); err == nil {
		t.Fatal("a static template with no presentation duration expanded")
	} else if !strings.Contains(err.Error(), "mediaPresentationDuration") {
		t.Errorf("err = %v, want it to name mediaPresentationDuration", err)
	}
}

// A dynamic MPD whose availabilityStartTime is still in the future has published
// nothing yet. Saying so, with how long the wait is, is more use than an empty
// segment list.
func TestExpandTemplate_LiveBeforeAnythingIsAvailable(t *testing.T) {
	tmpl := &mpdSegTemplate{Media: "$Number$.m4s", Timescale: 90000, Duration: 90000}
	base, _ := url.Parse("https://cdn.example.com/d/m.mpd")
	ast := epoch.Add(30 * time.Second) // starts half a minute from now

	_, _, _, err := expandTemplate(tmpl, mpdRepresentation{}, base, ast, epoch, 0, 0, 0, true, false)
	if err == nil {
		t.Fatal("a live MPD starting in the future expanded to segments")
	}
	if !strings.Contains(err.Error(), "no segment available yet") {
		t.Errorf("err = %v, want it to say nothing is available yet", err)
	}
	if !strings.Contains(err.Error(), "30s") {
		t.Errorf("err = %v, want it to say how far in the future the start is", err)
	}
}

// Just after the stream starts there are fewer segments than the sampling window,
// so the first index must clamp to zero rather than going negative.
func TestExpandTemplate_LiveWindowShorterThanTheSampleWindow(t *testing.T) {
	tmpl := &mpdSegTemplate{Media: "$Number$.m4s", Timescale: 90000, Duration: 90000}
	base, _ := url.Parse("https://cdn.example.com/d/m.mpd")
	// Three seconds of one-second segments have elapsed.
	ast := epoch.Add(-3 * time.Second)

	_, segs, _, err := expandTemplate(tmpl, mpdRepresentation{}, base, ast, epoch, 0, 0, 0, true, false)
	if err != nil {
		t.Fatalf("expandTemplate: %v", err)
	}
	if len(segs) != 3 {
		t.Fatalf("got %d segments, want the 3 published so far", len(segs))
	}
	if segs[0].Sequence != 1 {
		t.Errorf("first sequence = %d, want 1 — the window ran off the front", segs[0].Sequence)
	}
}

// A live window long enough to exceed the sample window is trimmed to its tail:
// the live edge is what matters, and the head may already have left the CDN.
func TestExpandTemplate_LiveSamplesTheTail(t *testing.T) {
	tmpl := &mpdSegTemplate{Media: "$Number$.m4s", Timescale: 90000, Duration: 90000}
	base, _ := url.Parse("https://cdn.example.com/d/m.mpd")
	ast := epoch.Add(-100 * time.Second) // a hundred one-second segments

	_, segs, _, err := expandTemplate(tmpl, mpdRepresentation{}, base, ast, epoch, 0, 0, 0, true, false)
	if err != nil {
		t.Fatalf("expandTemplate: %v", err)
	}
	if len(segs) != 12 {
		t.Errorf("got %d segments, want the 12-segment tail window", len(segs))
	}
	if segs[0].Sequence != 89 {
		t.Errorf("first sequence = %d, want 89 — the window is not at the live edge", segs[0].Sequence)
	}
}

// ---------- substituteTemplate ----------

// An unterminated `$` is not a placeholder. Copying the rest through verbatim is
// the only safe reading: dropping it would produce a URL that fetches something
// else, or nothing.
func TestSubstituteTemplate_UnterminatedPlaceholder(t *testing.T) {
	got := substituteTemplate("seg-$Number", mpdRepresentation{ID: "v0"}, 7, 0)
	if got != "seg-$Number" {
		t.Errorf("substituteTemplate = %q, want the unterminated tail copied through", got)
	}
}

// ---------- applyBaseURLs ----------

// A BaseURL that is not a URL at all cannot be resolved against, and it must be
// skipped rather than resetting the base to nothing.
func TestApplyBaseURLs_UnparseableEntryIsSkipped(t *testing.T) {
	base, _ := url.Parse("https://cdn.example.com/dash/m.mpd")
	got := applyBaseURLs(base, []string{"ht tp://\x7f-bad", "v1/"})
	if got == nil {
		t.Fatal("applyBaseURLs returned nothing")
	}
	if got.String() != "https://cdn.example.com/dash/v1/" {
		t.Errorf("base = %q, want the bad entry skipped and v1/ applied", got.String())
	}
}

// With no base at all — a manifest read from stdin — the first BaseURL becomes
// the base rather than being resolved against nothing.
func TestApplyBaseURLs_FirstEntryBecomesTheBaseWhenThereIsNone(t *testing.T) {
	got := applyBaseURLs(nil, []string{"https://cdn.example.com/dash/", "v1/"})
	if got == nil {
		t.Fatal("applyBaseURLs returned nothing")
	}
	if got.String() != "https://cdn.example.com/dash/v1/" {
		t.Errorf("base = %q", got.String())
	}
	if applyBaseURLs(nil, nil) != nil {
		t.Error("applyBaseURLs(nil, nil) invented a base")
	}
}

// ---------- parseISODuration ----------

// The date half of an xs:duration carries years, months, weeks and days. A
// manifest that states a DVR window in hours is common; one that states a
// programme length in days is rare but legal, and ignoring the unit would
// understate the duration by orders of magnitude.
func TestParseISODuration_DatePartUnits(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{"P1D", 24 * 3600},
		{"P1W", 7 * 24 * 3600},
		{"P1M", 30 * 24 * 3600},
		{"P1Y", 365 * 24 * 3600},
		{"P1DT1H", 24*3600 + 3600},
		{"PT1H30M", 5400},
		{"PT0.5S", 0.5},
		{"-PT10S", -10},
	}
	for _, tc := range tests {
		got, err := parseISODuration(tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseISODuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// A duration segcheck cannot read must be an error, not a zero: zero would be
// taken for a real period length and every segment count derived from it would
// be wrong.
func TestParseISODuration_Errors(t *testing.T) {
	for _, in := range []string{"PT10X", "PTXS", "P1Q", "PT..S", "nonsense"} {
		if got, err := parseISODuration(in); err == nil {
			t.Errorf("parseISODuration(%q) = %v, want an error", in, got)
		}
	}
}

// ---------- parseFrameRate ----------

func TestParseFrameRate_MalformedRatios(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{"30000/1001", 30000.0 / 1001.0},
		{"25", 25},
		{" 50 ", 50},
		{"", 0},
		// A zero denominator, and ratios whose halves are not numbers: none of
		// these is a frame rate, and reporting one would be a made-up measurement.
		{"25/0", 0},
		{"abc/1", 0},
		{"25/abc", 0},
		{"abc", 0},
	}
	for _, tc := range tests {
		if got := parseFrameRate(tc.in); got != tc.want {
			t.Errorf("parseFrameRate(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
