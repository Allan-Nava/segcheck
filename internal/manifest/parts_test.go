package manifest

import (
	"testing"
	"time"
)

// Low-latency HLS publishes fractions of a segment before the segment exists.
// An EXT-X-PART is therefore a second, finer description of the same media, and
// wherever a stream describes the same media twice the two descriptions can
// disagree — which is the whole reason the parts have to be read.
//
// The playlist below is the shape Apple's own low-latency examples take: a
// completed segment whose parts are still listed, the segment currently being
// published as parts alone, and a hint at the part that comes next.
const llPlaylist = `#EXTM3U
#EXT-X-VERSION:9
#EXT-X-TARGETDURATION:4
#EXT-X-PART-INF:PART-TARGET=0.33334
#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES,PART-HOLD-BACK=1.00002,CAN-SKIP-UNTIL=24.0
#EXT-X-MEDIA-SEQUENCE:100
#EXTINF:4.00000,
fs100.mp4
#EXT-X-PART:DURATION=0.33334,URI="fs101.1.mp4",INDEPENDENT=YES
#EXT-X-PART:DURATION=0.33334,URI="fs101.2.mp4"
#EXT-X-PART:DURATION=0.33332,URI="fs101.3.mp4"
#EXTINF:1.00000,
fs101.mp4
#EXT-X-PART:DURATION=0.33334,URI="fs102.mp4",INDEPENDENT=YES,BYTERANGE="30000@0"
#EXT-X-PART:DURATION=0.33334,URI="fs102.mp4",BYTERANGE="30000"
#EXT-X-PRELOAD-HINT:TYPE=PART,URI="fs102.mp4",BYTERANGE-START=60000
`

func TestParseHLS_LowLatencyParts(t *testing.T) {
	pl, err := ParseHLS([]byte(llPlaylist), "https://cdn.example/live/index.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}

	if pl.PartTarget != 0.33334 {
		t.Errorf("PartTarget = %v, want 0.33334 from EXT-X-PART-INF", pl.PartTarget)
	}
	if !pl.CanBlockReload {
		t.Error("CanBlockReload = false, but EXT-X-SERVER-CONTROL says CAN-BLOCK-RELOAD=YES")
	}
	if pl.PartHoldBack != 1.00002 {
		t.Errorf("PartHoldBack = %v, want 1.00002", pl.PartHoldBack)
	}

	if len(pl.Segments) != 2 {
		t.Fatalf("parsed %d segments, want 2", len(pl.Segments))
	}

	// The parts belong to the segment whose URI line follows them, not to the
	// one before: they are published ahead of the segment they make up.
	if n := len(pl.Segments[0].Parts); n != 0 {
		t.Errorf("the first segment took %d parts; the parts listed after it belong to the next one", n)
	}
	parts := pl.Segments[1].Parts
	if len(parts) != 3 {
		t.Fatalf("segment fs101 has %d parts, want 3", len(parts))
	}
	if want := "https://cdn.example/live/fs101.1.mp4"; parts[0].URI != want {
		t.Errorf("part URI = %q, want %q resolved against the playlist", parts[0].URI, want)
	}
	if !parts[0].Independent {
		t.Error("INDEPENDENT=YES was dropped; it is a claim about the bitstream and the only one a part makes")
	}
	if parts[1].Independent {
		t.Error("a part with no INDEPENDENT attribute was read as independent")
	}
	if parts[0].Duration != 0.33334 {
		t.Errorf("part duration = %v, want 0.33334", parts[0].Duration)
	}

	// The segment currently being published exists only as parts: there is no
	// URI line yet, and treating them as belonging to the last complete segment
	// would double-count that segment's media.
	if len(pl.PendingParts) != 2 {
		t.Fatalf("PendingParts = %d, want the 2 parts of the segment still being published", len(pl.PendingParts))
	}
	// A part BYTERANGE with no offset continues from the previous part of the
	// same resource, exactly as EXT-X-BYTERANGE does. Defaulting it to zero
	// fetches the first part twice and never fetches the second.
	if br := pl.PendingParts[1].ByteRange; br == nil {
		t.Fatal("the second part lost its BYTERANGE")
	} else if br.Offset != 30000 || br.Length != 30000 {
		t.Errorf("part BYTERANGE = %d@%d, want 30000@30000 continuing from the previous part", br.Length, br.Offset)
	}

	if pl.PreloadHint == nil {
		t.Fatal("EXT-X-PRELOAD-HINT was dropped")
	}
	if want := "https://cdn.example/live/fs102.mp4"; pl.PreloadHint.URI != want {
		t.Errorf("preload hint URI = %q, want %q", pl.PreloadHint.URI, want)
	}
	if pl.PreloadHint.ByteRangeStart != 60000 {
		t.Errorf("preload hint BYTERANGE-START = %d, want 60000", pl.PreloadHint.ByteRangeStart)
	}
}

// A playlist with no parts must gain none of this, and above all must not gain
// a zero PART-TARGET that a check could then compare against.
func TestParseHLS_NoPartsMeansNoPartClaims(t *testing.T) {
	pl, err := ParseHLS([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\na.ts\n#EXT-X-ENDLIST\n"),
		"https://cdn.example/v.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if pl.PartTarget != 0 || pl.PartHoldBack != 0 || pl.CanBlockReload {
		t.Errorf("an ordinary playlist gained low-latency claims: target=%v holdback=%v block=%v",
			pl.PartTarget, pl.PartHoldBack, pl.CanBlockReload)
	}
	if len(pl.Segments[0].Parts) != 0 || len(pl.PendingParts) != 0 || pl.PreloadHint != nil {
		t.Error("an ordinary playlist gained parts")
	}
}

// A GAP=YES part is a hole the packager is declaring on purpose. Fetching it is
// pointless and reporting it missing is reporting the manifest back at itself.
func TestParseHLS_GapPartIsMarked(t *testing.T) {
	pl, err := ParseHLS([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-PART-INF:PART-TARGET=1.0\n"+
		"#EXT-X-PART:DURATION=1.0,URI=\"p1.mp4\",GAP=YES\n#EXTINF:1.0,\ns.mp4\n"),
		"https://cdn.example/v.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if !pl.Segments[0].Parts[0].Gap {
		t.Error("GAP=YES was dropped; segcheck would report a deliberate hole as a missing part")
	}
}

// A real playlist states EXT-X-PROGRAM-DATE-TIME once, at the top, and leaves a
// client to derive every later segment's wall clock by adding the declared
// durations. Every live playlist Unified Streaming and most other packagers
// emit has exactly that shape — so a check that only looked at segments
// carrying the tag would look at one segment per playlist, and at none at all
// when sampling the live edge, which is where it was found doing nothing.
func TestParseHLS_ProgramDateTimeIsCarriedForward(t *testing.T) {
	pl, err := ParseHLS([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:100\n"+
		"#EXT-X-PROGRAM-DATE-TIME:2026-08-10T12:00:00.000Z\n"+
		"#EXTINF:2.0,\na.ts\n#EXTINF:2.0,\nb.ts\n#EXTINF:2.0,\nc.ts\n"),
		"https://cdn.example/v.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	want := []string{"2026-08-10T12:00:00Z", "2026-08-10T12:00:02Z", "2026-08-10T12:00:04Z"}
	for i, s := range pl.Segments {
		if !s.HasPDT {
			t.Fatalf("segment %d has no wall clock; a client derives one for every segment after the tag", i)
		}
		if got := s.PDT.UTC().Format(time.RFC3339); got != want[i] {
			t.Errorf("segment %d PDT = %s, want %s", i, got, want[i])
		}
		// Which segment actually carried the tag still has to be knowable: a
		// derived time is only as good as the durations it was summed over.
		if derived := s.PDTDerived; derived != (i > 0) {
			t.Errorf("segment %d PDTDerived = %v, want %v", i, derived, i > 0)
		}
	}
}

// After an EXT-X-DISCONTINUITY the specification requires a fresh
// EXT-X-PROGRAM-DATE-TIME, because the timeline restarts and the old anchor
// says nothing about what follows. A playlist that omits one has no wall-clock
// claim past that point, and inventing one by carrying the old anchor across
// would make segcheck report drift against a number it made up itself.
func TestParseHLS_ProgramDateTimeStopsAtAnUndeclaredDiscontinuity(t *testing.T) {
	pl, err := ParseHLS([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n"+
		"#EXT-X-PROGRAM-DATE-TIME:2026-08-10T12:00:00.000Z\n"+
		"#EXTINF:2.0,\na.ts\n#EXT-X-DISCONTINUITY\n#EXTINF:2.0,\nb.ts\n#EXTINF:2.0,\nc.ts\n"),
		"https://cdn.example/v.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if !pl.Segments[0].HasPDT {
		t.Error("the segment carrying the tag lost its wall clock")
	}
	for i := 1; i < len(pl.Segments); i++ {
		if pl.Segments[i].HasPDT {
			t.Errorf("segment %d carried a wall clock across a discontinuity with no fresh tag: %s",
				i, pl.Segments[i].PDT)
		}
	}
}

// A fresh tag after the discontinuity re-anchors, and everything after it is
// derived from the new anchor rather than the old one.
func TestParseHLS_ProgramDateTimeReanchorsAfterADiscontinuity(t *testing.T) {
	pl, err := ParseHLS([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n"+
		"#EXT-X-PROGRAM-DATE-TIME:2026-08-10T12:00:00.000Z\n#EXTINF:2.0,\na.ts\n"+
		"#EXT-X-DISCONTINUITY\n#EXT-X-PROGRAM-DATE-TIME:2026-08-10T13:00:00.000Z\n"+
		"#EXTINF:2.0,\nb.ts\n#EXTINF:2.0,\nc.ts\n"),
		"https://cdn.example/v.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if got := pl.Segments[2].PDT.UTC().Format(time.RFC3339); got != "2026-08-10T13:00:02Z" {
		t.Errorf("segment 2 PDT = %s, want 2026-08-10T13:00:02Z derived from the new anchor", got)
	}
}

// An EXT-X-PART with no URI names nothing a player can fetch, and adding it to
// the segment would give a check an entry it can never resolve.
func TestParseHLS_PartWithNoURI(t *testing.T) {
	pl, err := ParseHLS([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-PART-INF:PART-TARGET=1.0\n"+
		"#EXT-X-PART:DURATION=1.0\n#EXTINF:1.0,\ns.mp4\n"),
		"https://cdn.example/v.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if len(pl.Segments[0].Parts) != 0 {
		t.Errorf("a part with no URI was kept: %v", pl.Segments[0].Parts)
	}
}
