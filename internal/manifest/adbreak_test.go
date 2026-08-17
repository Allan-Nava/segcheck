package manifest

import (
	"testing"
	"time"
)

// SC-20: ad-break signalling.
//
// HLS states a break two ways. EXT-X-DATERANGE with an SCTE35-OUT attribute is the
// standard one and puts the break on the wall clock, so placing it on the media
// timeline needs EXT-X-PROGRAM-DATE-TIME. EXT-X-CUE-OUT is not in the
// specification at all but is what most packagers emit, and it sits between
// segments — so its position in the playlist *is* its time.

func TestParseHLS_DateRangeAdBreaks(t *testing.T) {
	m := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-PROGRAM-DATE-TIME:2026-08-17T10:00:00.000Z
#EXT-X-DATERANGE:ID="break-1",START-DATE="2026-08-17T10:00:08.000Z",PLANNED-DURATION=30.0,SCTE35-OUT=0xFC302F
#EXTINF:4.0,
s0.ts
#EXTINF:4.0,
s1.ts
#EXTINF:4.0,
s2.ts
#EXT-X-DATERANGE:ID="break-1",START-DATE="2026-08-17T10:00:08.000Z",DURATION=8.0,SCTE35-IN=0xFC3020
#EXTINF:4.0,
s3.ts
#EXT-X-ENDLIST
`
	pl, err := ParseHLS([]byte(m), "https://e.test/index.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if len(pl.AdBreaks) != 2 {
		t.Fatalf("ad breaks = %d, want 2: %+v", len(pl.AdBreaks), pl.AdBreaks)
	}
	out := pl.AdBreaks[0]
	if out.ID != "break-1" || !out.OutOfNetwork {
		t.Errorf("first break = %+v, want break-1 out of network", out)
	}
	want := time.Date(2026, 8, 17, 10, 0, 8, 0, time.UTC)
	if !out.Start.Equal(want) || !out.HasStart {
		t.Errorf("start = %v (have %v), want %v", out.Start, out.HasStart, want)
	}
	if out.PlannedDuration != 30 {
		t.Errorf("planned duration = %v, want 30", out.PlannedDuration)
	}
	if in := pl.AdBreaks[1]; in.OutOfNetwork || in.Duration != 8 {
		t.Errorf("second break = %+v, want a return with an 8s duration", in)
	}
}

// EXT-X-CUE-OUT sits between segments, so the segment that follows it is where the
// break begins — no wall clock needed, and a boundary by construction.
func TestParseHLS_CueOutAdBreaks(t *testing.T) {
	m := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:10
#EXTINF:4.0,
s0.ts
#EXT-X-CUE-OUT:30.000
#EXTINF:4.0,
s1.ts
#EXT-X-CUE-OUT-CONT:4.000/30.000
#EXTINF:4.0,
s2.ts
#EXT-X-CUE-IN
#EXTINF:4.0,
s3.ts
#EXT-X-ENDLIST
`
	pl, err := ParseHLS([]byte(m), "https://e.test/index.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if len(pl.AdBreaks) != 2 {
		t.Fatalf("ad breaks = %d, want 2: %+v", len(pl.AdBreaks), pl.AdBreaks)
	}
	out := pl.AdBreaks[0]
	if !out.OutOfNetwork || out.Duration != 30 {
		t.Errorf("cue-out = %+v, want an out of 30s", out)
	}
	// Sequence 11 is the second segment: the one the cue-out precedes.
	if out.Sequence != 11 || !out.HasSequence {
		t.Errorf("cue-out sequence = %d (have %v), want 11", out.Sequence, out.HasSequence)
	}
	if in := pl.AdBreaks[1]; in.OutOfNetwork || in.Sequence != 13 {
		t.Errorf("cue-in = %+v, want a return at sequence 13", in)
	}
}

// A DATERANGE with no SCTE35 attribute at all is some other kind of range — a
// programme boundary, a chapter — and reporting it as an ad break would have the
// check hunting a splice point nobody signalled.
func TestParseHLS_DateRangeWithoutSCTE35IsNotABreak(t *testing.T) {
	m := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:4
#EXT-X-DATERANGE:ID="chapter-1",START-DATE="2026-08-17T10:00:00.000Z",CLASS="com.example.chapter"
#EXTINF:4.0,
s0.ts
#EXT-X-ENDLIST
`
	pl, err := ParseHLS([]byte(m), "https://e.test/index.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if len(pl.AdBreaks) != 0 {
		t.Errorf("ad breaks = %+v, want none", pl.AdBreaks)
	}
}

// DASH declares breaks in an EventStream at Period level, under one of the SCTE-35
// schemes, with each Event's time on the stream's own timescale.
func TestParseDASH_EventStreamAdBreaks(t *testing.T) {
	m := `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT20S">
 <Period id="0" start="PT0S">
  <EventStream schemeIdUri="urn:scte:scte35:2014:xml+bin" timescale="90000">
   <Event presentationTime="720000" duration="2700000" id="1"/>
   <Event presentationTime="3420000" id="2"/>
  </EventStream>
  <EventStream schemeIdUri="urn:mpeg:dash:event:2012" timescale="1000">
   <Event presentationTime="5000" id="9"/>
  </EventStream>
  <AdaptationSet contentType="video" mimeType="video/mp4">
   <SegmentTemplate media="v-$Number$.m4s" initialization="v-init.mp4" duration="2" timescale="1"/>
   <Representation id="v1" bandwidth="1000000" width="1280" height="720" codecs="avc1.4d401f"/>
  </AdaptationSet>
 </Period>
</MPD>
`
	pl, err := ParseDASH([]byte(m), "https://e.test/m.mpd", fixedNow())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if len(pl.AdBreaks) != 2 {
		t.Fatalf("ad breaks = %d, want 2 (the non-SCTE stream is not one): %+v", len(pl.AdBreaks), pl.AdBreaks)
	}
	first := pl.AdBreaks[0]
	if first.ID != "1" || !first.HasMediaTime || first.MediaTime != 8 {
		t.Errorf("first break = %+v, want id 1 at 8s", first)
	}
	if first.Duration != 30 {
		t.Errorf("first duration = %v, want 30", first.Duration)
	}
	if second := pl.AdBreaks[1]; !second.HasMediaTime || second.MediaTime != 38 {
		t.Errorf("second break = %+v, want 38s", second)
	}
}

// An EventStream with no timescale defaults to 1, which is what the specification
// says and what a reader that assumed 90000 would get wrong by five orders of
// magnitude.
func TestParseDASH_EventStreamDefaultTimescale(t *testing.T) {
	m := `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT20S">
 <Period id="0" start="PT0S">
  <EventStream schemeIdUri="urn:scte:scte35:2013:xml">
   <Event presentationTime="8" duration="30" id="1"/>
  </EventStream>
  <AdaptationSet contentType="video" mimeType="video/mp4">
   <SegmentTemplate media="v-$Number$.m4s" initialization="v-init.mp4" duration="2" timescale="1"/>
   <Representation id="v1" bandwidth="1000000" width="1280" height="720" codecs="avc1.4d401f"/>
  </AdaptationSet>
 </Period>
</MPD>
`
	pl, err := ParseDASH([]byte(m), "https://e.test/m.mpd", fixedNow())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if len(pl.AdBreaks) != 1 {
		t.Fatalf("ad breaks = %+v, want one", pl.AdBreaks)
	}
	if b := pl.AdBreaks[0]; !b.HasMediaTime || b.MediaTime != 8 || b.Duration != 30 {
		t.Errorf("break = %+v, want 8s for 30s", b)
	}
}

// The duration on a CUE-OUT is written either bare or as an attribute, depending on
// which packager wrote the playlist. Both are in the wild.
func TestParseHLS_CueOutDurationForms(t *testing.T) {
	for _, tag := range []string{"#EXT-X-CUE-OUT:30.000", "#EXT-X-CUE-OUT:DURATION=30.000", "#EXT-X-CUE-OUT"} {
		m := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:4\n" +
			tag + "\n#EXTINF:4.0,\ns0.ts\n#EXT-X-ENDLIST\n"
		pl, err := ParseHLS([]byte(m), "https://e.test/index.m3u8")
		if err != nil {
			t.Fatalf("%s: ParseHLS: %v", tag, err)
		}
		if len(pl.AdBreaks) != 1 {
			t.Fatalf("%s: ad breaks = %+v, want one", tag, pl.AdBreaks)
		}
		want := 30.0
		if tag == "#EXT-X-CUE-OUT" {
			want = 0 // no duration stated is not a duration of zero seconds claimed
		}
		if got := pl.AdBreaks[0].Duration; got != want {
			t.Errorf("%s: duration = %v, want %v", tag, got, want)
		}
	}
}
