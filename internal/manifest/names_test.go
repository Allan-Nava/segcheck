package manifest

import (
	"testing"
	"time"
)

// A rendition's name is what every finding in the report is filed under, so two
// renditions sharing one is not a cosmetic problem: a ladder with two 720p rungs
// at different bitrates produces two `bitrate 720p` rows, two `container 720p`
// rows and two `pdt 720p` rows, and nothing says which rung any of them is
// about. Unified Streaming's own live demo has exactly that shape.
func TestParseHLS_TwoRungsAtOneResolutionGetDistinctNames(t *testing.T) {
	pl, err := ParseHLS([]byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=1300000,RESOLUTION=1280x720,CODECS="avc1.640028"
mid.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2500000,RESOLUTION=1280x720,CODECS="avc1.640028"
high.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=4000000,RESOLUTION=1920x1080,CODECS="avc1.640028"
top.m3u8
`), "https://cdn.example/master.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	names := map[string]int{}
	for _, r := range pl.Renditions {
		names[r.Name]++
	}
	for name, n := range names {
		if n > 1 {
			t.Errorf("%d renditions are all called %q", n, name)
		}
	}
	// The rung that does not collide keeps the name an operator says out loud.
	if pl.Renditions[2].Name != "1080p" {
		t.Errorf("the unique rung was renamed to %q; only the colliding ones should change", pl.Renditions[2].Name)
	}
	// And the ones that do collide are told apart by the thing that differs.
	for _, want := range []string{"720p 1300kbps", "720p 2500kbps"} {
		if names[want] != 1 {
			t.Errorf("no rendition named %q; got %v", want, names)
		}
	}
}

// The same shape in an MPD: two Representations of one AdaptationSet at one
// height is the ordinary way a ladder carries two bitrates of the same picture.
func TestParseDASH_TwoRepresentationsAtOneResolutionGetDistinctNames(t *testing.T) {
	pl, err := ParseDASH([]byte(`<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT4S">
  <Period id="p0" start="PT0S" duration="PT4S">
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000" media="$RepresentationID$/$Number$.m4s" startNumber="1"/>
      <Representation id="v0" bandwidth="1300000" width="1280" height="720" codecs="avc1.640028"/>
      <Representation id="v1" bandwidth="2500000" width="1280" height="720" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
</MPD>`), "https://cdn.example/m.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if len(pl.Renditions) != 2 {
		t.Fatalf("parsed %d renditions, want 2", len(pl.Renditions))
	}
	if pl.Renditions[0].Name == pl.Renditions[1].Name {
		t.Errorf("both representations are called %q", pl.Renditions[0].Name)
	}
}

// Two rungs that are identical in both respects are a defect the `ladder` check
// reports, and they still have to be nameable: appending a bandwidth that is the
// same on both would leave the report exactly as ambiguous as it was.
func TestParseHLS_IdenticalRungsAreStillToldApart(t *testing.T) {
	pl, err := ParseHLS([]byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=1300000,RESOLUTION=1280x720,CODECS="avc1.640028"
a.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1300000,RESOLUTION=1280x720,CODECS="avc1.640028"
b.m3u8
`), "https://cdn.example/master.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if pl.Renditions[0].Name == pl.Renditions[1].Name {
		t.Errorf("two identical rungs are both called %q", pl.Renditions[0].Name)
	}
}

// A ladder with no collisions must be untouched: `720p` is what an operator
// says, and renaming every rung to buy a uniqueness nothing needed makes the
// whole report harder to read.
func TestParseHLS_ALadderWithoutCollisionsIsUnchanged(t *testing.T) {
	pl, err := ParseHLS([]byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=1300000,RESOLUTION=1280x720,CODECS="avc1.640028"
mid.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=4000000,RESOLUTION=1920x1080,CODECS="avc1.640028"
top.m3u8
`), "https://cdn.example/master.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	for i, want := range []string{"720p", "1080p"} {
		if pl.Renditions[i].Name != want {
			t.Errorf("rendition %d is called %q, want %q", i, pl.Renditions[i].Name, want)
		}
	}
}
