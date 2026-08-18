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
