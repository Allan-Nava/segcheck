package manifest

import (
	"testing"
	"time"
)

// A dynamic MPD's live edge is computed, not listed: availabilityStartTime plus
// arithmetic against "now". Which "now" is the whole question. The client's own
// clock is the thing under test — a machine two minutes fast asks for segments
// the packager has not made yet and gets 404s that read as a CDN fault — so the
// MPD names its own time source, and a checker that ignores it is measuring its
// own clock.
func TestParseDASH_UTCTimingAndTheDVRWindow(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="dynamic" availabilityStartTime="2026-08-10T12:00:00Z" minimumUpdatePeriod="PT4S"
     timeShiftBufferDepth="PT1M30S" suggestedPresentationDelay="PT8S">
  <UTCTiming schemeIdUri="urn:mpeg:dash:utc:http-head:2014" value="https://time.example/"/>
  <UTCTiming schemeIdUri="urn:mpeg:dash:utc:http-iso:2014" value="https://time.example/?iso"/>
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="1" duration="4" media="v/$Number$.m4s" startNumber="1"/>
      <Representation id="v" bandwidth="800000" width="640" height="360"/>
    </AdaptationSet>
  </Period>
</MPD>`
	now := time.Date(2026, 8, 10, 12, 1, 40, 0, time.UTC)
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/live/manifest.mpd", now)
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}

	if len(pl.UTCTiming) != 2 {
		t.Fatalf("parsed %d UTCTiming elements, want 2 in the order the MPD lists them", len(pl.UTCTiming))
	}
	if pl.UTCTiming[0].Scheme != "urn:mpeg:dash:utc:http-head:2014" {
		t.Errorf("first scheme = %q", pl.UTCTiming[0].Scheme)
	}
	if pl.UTCTiming[0].Value != "https://time.example/" {
		t.Errorf("first value = %q", pl.UTCTiming[0].Value)
	}

	if !pl.HasAvailabilityStart {
		t.Fatal("availabilityStartTime was dropped; without it the live edge cannot be computed at all")
	}
	if want := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC); !pl.AvailabilityStart.Equal(want) {
		t.Errorf("AvailabilityStart = %s, want %s", pl.AvailabilityStart, want)
	}
	if pl.TimeShiftBufferDepth != 90 {
		t.Errorf("TimeShiftBufferDepth = %v, want 90 from PT1M30S", pl.TimeShiftBufferDepth)
	}
	if pl.PresentationDelay != 8 {
		t.Errorf("PresentationDelay = %v, want 8 from PT8S", pl.PresentationDelay)
	}
}

// A static MPD makes none of these claims, and a zero must not be readable as
// one: a timeShiftBufferDepth of nought is a window with nothing in it.
func TestParseDASH_StaticMPDMakesNoLiveClaims(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT10S">
  <Period><AdaptationSet mimeType="video/mp4">
    <SegmentTemplate timescale="1" duration="4" media="v/$Number$.m4s" startNumber="1"/>
    <Representation id="v" bandwidth="800000" width="640" height="360"/>
  </AdaptationSet></Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/vod.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if pl.HasAvailabilityStart || len(pl.UTCTiming) != 0 || pl.TimeShiftBufferDepth != 0 {
		t.Errorf("a static MPD gained live claims: ast=%v utc=%d tsbd=%v",
			pl.HasAvailabilityStart, len(pl.UTCTiming), pl.TimeShiftBufferDepth)
	}
}

// The segment just past the live edge is the other half of the availability
// question. The MPD's arithmetic says it does not exist yet; if the origin
// serves it anyway, the packager is ahead of the clock the MPD is computed
// against, and every player is holding back latency it does not need. Nothing
// but that one probe can tell, so the segment has to be addressable.
func TestParseDASH_NextSegmentPastTheLiveEdgeIsAddressable(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="dynamic" availabilityStartTime="2026-08-10T12:00:00Z">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="1" duration="4" media="v/$Number$.m4s" startNumber="1"/>
      <Representation id="v" bandwidth="800000" width="640" height="360"/>
    </AdaptationSet>
  </Period>
</MPD>`
	// 100s after availabilityStartTime: 25 whole segments have been published,
	// numbered 1..25, so the one the MPD says does not exist yet is 26.
	now := time.Date(2026, 8, 10, 12, 1, 40, 0, time.UTC)
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/live/manifest.mpd", now)
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	next := pl.Renditions[0].NextSegment
	if next == nil {
		t.Fatal("no NextSegment: the availability claim cannot be probed in the direction that matters")
	}
	if want := "https://cdn.example/live/v/26.m4s"; next.URI != want {
		t.Errorf("NextSegment = %q, want %q", next.URI, want)
	}
	// It must not be in the sampled list: fetching it as an ordinary segment
	// would report a 404 the MPD itself predicted.
	for _, s := range pl.Renditions[0].Segments {
		if s.URI == next.URI {
			t.Fatal("the unavailable segment is in the segment list; every run would report a phantom 404")
		}
	}
}

// A static MPD has no live edge, so there is no "next" segment to probe: every
// segment it describes exists already.
func TestParseDASH_StaticMPDHasNoNextSegment(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT12S">
  <Period><AdaptationSet mimeType="video/mp4">
    <SegmentTemplate timescale="1" duration="4" media="v/$Number$.m4s" startNumber="1"/>
    <Representation id="v" bandwidth="800000" width="640" height="360"/>
  </AdaptationSet></Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/vod.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if pl.Renditions[0].NextSegment != nil {
		t.Error("a static MPD produced a segment past its live edge")
	}
}
