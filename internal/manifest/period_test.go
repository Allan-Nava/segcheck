package manifest

import (
	"testing"
	"time"
)

// A multi-period MPD is several presentations spliced into one, and the splice is
// where an encoder change lands: a new period is exactly how an ad break, a
// programme junction or a re-encode is expressed. Each period restarts its own
// media timeline, so nothing about a segment says which period it came from —
// the manifest has to carry that, or every cross-period comparison is between
// two things that were never on the same clock.
func TestParseDASH_RenditionsCarryTheirPeriod(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT20S">
  <Period id="main" start="PT0S" duration="PT10S">
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000" media="a/$Number$.m4s" startNumber="1" presentationTimeOffset="0"/>
      <Representation id="v0" bandwidth="800000" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
  <Period id="ad" start="PT10S" duration="PT10S">
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000" media="b/$Number$.m4s" startNumber="1" presentationTimeOffset="900000"/>
      <Representation id="v0" bandwidth="800000" width="640" height="360" codecs="avc1.4d401f"/>
    </AdaptationSet>
  </Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/m.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if len(pl.Renditions) != 2 {
		t.Fatalf("parsed %d renditions, want one per period", len(pl.Renditions))
	}

	first, second := pl.Renditions[0], pl.Renditions[1]
	if first.PeriodID != "main" || second.PeriodID != "ad" {
		t.Errorf("period ids = %q/%q, want main/ad", first.PeriodID, second.PeriodID)
	}
	if first.PeriodIndex != 0 || second.PeriodIndex != 1 {
		t.Errorf("period indexes = %d/%d, want 0/1", first.PeriodIndex, second.PeriodIndex)
	}
	if first.PeriodStart != 0 || second.PeriodStart != 10 {
		t.Errorf("period starts = %v/%v, want 0/10", first.PeriodStart, second.PeriodStart)
	}
	if first.PeriodDuration != 10 || second.PeriodDuration != 10 {
		t.Errorf("period durations = %v/%v, want 10/10", first.PeriodDuration, second.PeriodDuration)
	}
	// The offset that maps a segment's own timeline onto the presentation one.
	// Without it a check comparing the two is comparing different clocks.
	if second.PresentationTimeOffset != 10 {
		t.Errorf("PresentationTimeOffset = %v, want 10s (900000 over a 90kHz timescale)", second.PresentationTimeOffset)
	}
	// And the name says which period, because two periods' 720p rungs are two
	// different things and a report naming both "720p" is unreadable.
	if first.Name == second.Name {
		t.Errorf("both periods' renditions are called %q", first.Name)
	}
}

// A single-period MPD is the ordinary case and must gain none of this: a period
// index of zero and a start of zero are what every existing check already assumes.
func TestParseDASH_SinglePeriodIsUnchanged(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT10S">
  <Period><AdaptationSet mimeType="video/mp4">
    <SegmentTemplate timescale="90000" duration="180000" media="v/$Number$.m4s" startNumber="1"/>
    <Representation id="v" bandwidth="800000" width="1280" height="720"/>
  </AdaptationSet></Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/m.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	r := pl.Renditions[0]
	if r.PeriodIndex != 0 || r.PeriodStart != 0 || r.PeriodID != "" {
		t.Errorf("a single-period MPD gained period metadata: %d/%v/%q", r.PeriodIndex, r.PeriodStart, r.PeriodID)
	}
	if r.Name != "720p" {
		t.Errorf("Name = %q, want the plain 720p a one-period ladder has always had", r.Name)
	}
}

// Where a segment sits inside its period is the fact a period-aware check needs
// and neither the media nor @presentationTimeOffset alone can give: the offset
// says which media time the period begins at, and this says how far past that
// beginning this particular segment starts. The two together are what a
// segment's own timestamp gets compared against.
func TestParseDASH_SegmentsCarryTheirOffsetIntoThePeriod(t *testing.T) {
	// @duration: index i covers [i*segDur, (i+1)*segDur) from the period start,
	// whatever the media timeline underneath it counts from.
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT8S">
  <Period id="main" start="PT0S" duration="PT4S">
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000" media="a/$Number$.m4s" startNumber="1"/>
      <Representation id="v0" bandwidth="800000" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
  <Period id="ad" start="PT4S" duration="PT4S">
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000" media="b/$Number$.m4s" startNumber="99" presentationTimeOffset="360000"/>
      <Representation id="v0" bandwidth="800000" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/m.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	segs := pl.Renditions[1].Segments
	if len(segs) != 2 {
		t.Fatalf("expanded %d segments, want 2", len(segs))
	}
	for i, want := range []float64{0, 2} {
		if !segs[i].HasPeriodOffset || segs[i].PeriodOffset != want {
			t.Errorf("segment %d (number %d): period offset %v/%v, want %v — the second period's first segment starts at its period, not at 198s",
				i, segs[i].Sequence, segs[i].PeriodOffset, segs[i].HasPeriodOffset, want)
		}
	}

	// A SegmentTimeline states the media time outright, so the offset into the
	// period is that time less the one the period begins at.
	mpd = `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT8S">
  <Period id="main" start="PT0S" duration="PT4S">
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="90000" media="a/$Time$.m4s" presentationTimeOffset="360000">
        <SegmentTimeline><S t="360000" d="180000" r="1"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v0" bandwidth="800000" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
  <Period id="ad" start="PT4S" duration="PT4S">
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000" media="b/$Number$.m4s" startNumber="1"/>
      <Representation id="v0" bandwidth="800000" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
</MPD>`
	pl, err = ParseDASH([]byte(mpd), "https://cdn.example/m.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	segs = pl.Renditions[0].Segments
	if len(segs) != 2 {
		t.Fatalf("expanded %d segments, want 2", len(segs))
	}
	for i, want := range []float64{0, 2} {
		if !segs[i].HasPeriodOffset || segs[i].PeriodOffset != want {
			t.Errorf("timeline segment %d: period offset %v/%v, want %v",
				i, segs[i].PeriodOffset, segs[i].HasPeriodOffset, want)
		}
	}
	// And the media timeline it states is untouched by the offset: that value is
	// what the segment's own tfdt is compared against.
	if segs[0].DeclaredStart != 4 {
		t.Errorf("declared start = %v, want 4s: @presentationTimeOffset must not move the media timeline", segs[0].DeclaredStart)
	}
}

// HLS has no periods, and a segment that gained an offset into one would be
// carrying a DASH fact into a format that cannot state it.
func TestParseHLS_SegmentsHaveNoPeriodOffset(t *testing.T) {
	pl, err := ParseHLS([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\na.ts\n#EXTINF:4.0,\nb.ts\n#EXT-X-ENDLIST\n"),
		"https://cdn.example/media.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	for i, s := range pl.Segments {
		if s.HasPeriodOffset {
			t.Errorf("HLS segment %d carries a period offset", i)
		}
	}
}

// A Period's start is not always stated. ISO/IEC 23009-1 says an absent @start
// on a static MPD is the previous Period's start plus its duration, and a
// parser that defaults it to zero puts every Period after the first at the
// beginning of the presentation — where the first one already is. Nothing
// errors: the numbers are simply wrong, and anything that reasons about where a
// Period plays reasons from them.
func TestParseDASH_APeriodWithNoStartFollowsTheOneBeforeIt(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT30S">
  <Period id="a" start="PT0S" duration="PT10S">
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000" media="a/$Number$.m4s" startNumber="1"/>
      <Representation id="v" bandwidth="800000" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
  <Period id="b" duration="PT10S">
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000" media="b/$Number$.m4s" startNumber="1"/>
      <Representation id="v" bandwidth="800000" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
  <Period id="c" duration="PT10S">
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000" media="c/$Number$.m4s" startNumber="1"/>
      <Representation id="v" bandwidth="800000" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/m.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	for i, want := range []float64{0, 10, 20} {
		r := pl.Renditions[i]
		if !r.PeriodStartKnown {
			t.Errorf("period %d: start reported as underivable, but the one before it states a duration", i)
		}
		if r.PeriodStart != want {
			t.Errorf("period %d starts at %v, want %v", i, r.PeriodStart, want)
		}
	}
}

// A Period that states no duration either: the next one's @start says where it
// ends, and the last one runs to the end of the presentation.
func TestParseDASH_APeriodWithNoDurationEndsWhereTheNextBegins(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT30S">
  <Period id="a" start="PT0S">
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000" media="a/$Number$.m4s" startNumber="1"/>
      <Representation id="v" bandwidth="800000" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
  <Period id="b" start="PT12S">
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000" media="b/$Number$.m4s" startNumber="1"/>
      <Representation id="v" bandwidth="800000" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/m.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if got := pl.Renditions[0].PeriodDuration; got != 12 {
		t.Errorf("the first period lasts %vs; the next one starts at 12s", got)
	}
	if got := pl.Renditions[1].PeriodDuration; got != 18 {
		t.Errorf("the last period lasts %vs; the presentation is 30s and it starts at 12s", got)
	}
}

// A Period held in another document — how an ad decision server splices a break
// in — states nothing here: no duration, and often no start on the Period after
// it. That start is then not derivable at all, and defaulting it to zero puts a
// Period a third of the way through the presentation at the very beginning.
// nomor's own DASH-IF vector 5_1a has exactly this shape.
func TestParseDASH_APeriodAfterAnXlinkOneHasNoDerivableStart(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD xmlns:xlink="http://www.w3.org/1999/xlink" type="static" mediaPresentationDuration="PT704S">
  <Period id="0" duration="PT250S">
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000" media="a/$Number$.m4s" startNumber="1"/>
      <Representation id="v" bandwidth="800000" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
  <Period xlink:href="https://cdn.example/ad.period" xlink:actuate="onLoad"></Period>
  <Period id="2" duration="PT344S">
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000" media="c/$Number$.m4s" startNumber="1"/>
      <Representation id="v" bandwidth="800000" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/m.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if len(pl.Renditions) != 2 {
		t.Fatalf("parsed %d renditions, want 2: the remote Period contributes none", len(pl.Renditions))
	}
	first, third := pl.Renditions[0], pl.Renditions[1]
	if !first.PeriodStartKnown || first.PeriodStart != 0 {
		t.Errorf("the first period starts at %v (known=%v), want 0", first.PeriodStart, first.PeriodStartKnown)
	}
	if third.PeriodStartKnown {
		t.Errorf("the period after a remote one claims to start at %v, but nothing in this document says when the remote one ends",
			third.PeriodStart)
	}
}
