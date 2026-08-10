package manifest

import (
	"math"
	"testing"
	"time"
)

const mpdTimeline = `<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT30S">
  <BaseURL>https://cdn.example/dash/</BaseURL>
  <Period id="0" start="PT0S">
    <AdaptationSet mimeType="video/mp4" contentType="video">
      <SegmentTemplate timescale="90000" media="$RepresentationID$/seg-$Number%05d$.m4s" initialization="$RepresentationID$/init.mp4" startNumber="1">
        <SegmentTimeline>
          <S t="0" d="540000" r="2"/>
          <S d="450000"/>
        </SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v0" bandwidth="1200000" width="1280" height="720" codecs="avc1.4d401f"/>
      <Representation id="v1" bandwidth="4500000" width="1920" height="1080" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
</MPD>`

func TestParseDASH_SegmentTimeline(t *testing.T) {
	pl, err := ParseDASH([]byte(mpdTimeline), "https://origin.example/x/manifest.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if pl.Live {
		t.Error("Live = true for a static MPD")
	}
	if len(pl.Renditions) != 2 {
		t.Fatalf("renditions = %d, want 2", len(pl.Renditions))
	}

	r := pl.Renditions[0]
	if r.Name != "720p" || r.Width != 1280 || r.Height != 720 {
		t.Errorf("first rendition = %s %dx%d, want 720p 1280x720", r.Name, r.Width, r.Height)
	}
	if want := "https://cdn.example/dash/v0/init.mp4"; r.InitURI != want {
		t.Errorf("init URI = %q, want %q — BaseURL or $RepresentationID$ mishandled", r.InitURI, want)
	}
	// r="2" repeats the entry twice more: 3 segments, plus the last S entry.
	if len(r.Segments) != 4 {
		t.Fatalf("segments = %d, want 4 (S r=2 gives 3, plus one more)", len(r.Segments))
	}

	// The %05d format specifier must be honoured.
	if want := "https://cdn.example/dash/v0/seg-00001.m4s"; r.Segments[0].URI != want {
		t.Errorf("first segment URI = %q, want %q", r.Segments[0].URI, want)
	}
	if want := "https://cdn.example/dash/v0/seg-00004.m4s"; r.Segments[3].URI != want {
		t.Errorf("fourth segment URI = %q, want %q", r.Segments[3].URI, want)
	}

	for i, want := range []float64{6, 6, 6, 5} {
		if got := r.Segments[i].Duration; math.Abs(got-want) > 1e-9 {
			t.Errorf("segment %d duration = %v, want %v", i, got, want)
		}
	}
	// @t is on the media timeline, directly comparable with a segment's tfdt.
	for i, want := range []float64{0, 6, 12, 18} {
		s := r.Segments[i]
		if !s.HasDeclaredStart {
			t.Fatalf("segment %d has no declared start", i)
		}
		if math.Abs(s.DeclaredStart-want) > 1e-9 {
			t.Errorf("segment %d declared start = %v, want %v", i, s.DeclaredStart, want)
		}
	}
}

const mpdDurationTemplate = `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT20S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="1000" duration="4000" media="v/$Number$.m4s" initialization="v/init.mp4" startNumber="0"/>
      <Representation id="v" bandwidth="800000" width="640" height="360" codecs="avc1.42c01e"/>
    </AdaptationSet>
  </Period>
</MPD>`

func TestParseDASH_DurationTemplate(t *testing.T) {
	pl, err := ParseDASH([]byte(mpdDurationTemplate), "https://cdn.example/d/manifest.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	r := pl.Renditions[0]
	if len(r.Segments) != 5 {
		t.Fatalf("segments = %d, want 5 (20s / 4s)", len(r.Segments))
	}
	if want := "https://cdn.example/d/v/0.m4s"; r.Segments[0].URI != want {
		t.Errorf("first URI = %q, want %q (startNumber 0)", r.Segments[0].URI, want)
	}
	// Without a SegmentTimeline the media-timeline origin is unknown, so no
	// declared start is published: a phantom drift report is worse than none.
	if r.Segments[0].HasDeclaredStart {
		t.Error("a @duration template published a declared start; it cannot be compared with tfdt")
	}
}

func TestParseDASH_LiveEdgeFromWallClock(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="dynamic" availabilityStartTime="2026-08-10T12:00:00Z" minimumUpdatePeriod="PT4S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="1" duration="4" media="v/$Number$.m4s" startNumber="1"/>
      <Representation id="v" bandwidth="800000" width="640" height="360"/>
    </AdaptationSet>
  </Period>
</MPD>`
	// 100 seconds after availabilityStartTime: 25 whole segments have been
	// published, and the sampler must be looking at the newest of them.
	now := time.Date(2026, 8, 10, 12, 1, 40, 0, time.UTC)
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/live/manifest.mpd", now)
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if !pl.Live {
		t.Error("Live = false for a dynamic MPD")
	}
	segs := pl.Renditions[0].Segments
	if len(segs) == 0 {
		t.Fatal("no segment expanded for a live MPD")
	}
	last := segs[len(segs)-1]
	if want := "https://cdn.example/live/v/25.m4s"; last.URI != want {
		t.Errorf("newest segment = %q, want %q", last.URI, want)
	}
}

func TestParseDASH_SegmentBaseIsFlaggedNotSilent(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT10S">
  <Period><AdaptationSet mimeType="video/mp4">
    <Representation id="v" bandwidth="800000" width="640" height="360"><SegmentBase indexRange="0-800"/></Representation>
  </AdaptationSet></Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/sb.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if pl.Renditions[0].Unsupported == "" {
		t.Error("SegmentBase was skipped silently; an unsupported rendition must say so")
	}
}

func TestParseDASH_RejectsNonXML(t *testing.T) {
	if _, err := ParseDASH([]byte("#EXTM3U\n"), "https://cdn.example/x.mpd", time.Now()); err == nil {
		t.Fatal("a playlist parsed as an MPD")
	}
}

func TestSubstituteTemplate(t *testing.T) {
	rep := mpdRepresentation{ID: "video_1", Bandwidth: 1200000}
	tests := []struct {
		tmpl string
		want string
	}{
		{"$RepresentationID$/$Number$.m4s", "video_1/42.m4s"},
		{"$RepresentationID$/seg-$Number%05d$.m4s", "video_1/seg-00042.m4s"},
		{"chunk-$Time$.mp4", "chunk-900000.mp4"},
		{"$Bandwidth$/x.m4s", "1200000/x.m4s"},
		{"literal$$dollar.m4s", "literal$dollar.m4s"},
		{"$Unknown$/x.m4s", "$Unknown$/x.m4s"},
	}
	for _, tc := range tests {
		if got := substituteTemplate(tc.tmpl, rep, 42, 900000); got != tc.want {
			t.Errorf("substituteTemplate(%q) = %q, want %q", tc.tmpl, got, tc.want)
		}
	}
}

func TestParseISODuration(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{"", 0},
		{"PT30S", 30},
		{"PT1M30S", 90},
		{"PT1H2M3.5S", 3723.5},
		{"P1DT1H", 90000},
		{"PT0S", 0},
	}
	for _, tc := range tests {
		got, err := parseISODuration(tc.in)
		if err != nil {
			t.Errorf("parseISODuration(%q): %v", tc.in, err)
			continue
		}
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("parseISODuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if _, err := parseISODuration("30S"); err == nil {
		t.Error("a duration without the P prefix was accepted")
	}
}

func TestParseFrameRate(t *testing.T) {
	if got := parseFrameRate("30000/1001"); math.Abs(got-29.97) > 0.01 {
		t.Errorf("parseFrameRate(30000/1001) = %v, want ~29.97", got)
	}
	if got := parseFrameRate("25"); got != 25 {
		t.Errorf("parseFrameRate(25) = %v, want 25", got)
	}
}
