package manifest

import "testing"

// A discontinuity is a timeline the player must throw away and start again, and
// which one it is on is a number the playlist states only at the front:
// EXT-X-DISCONTINUITY-SEQUENCE counts the ones that have already rolled out of a
// live window, and every EXT-X-DISCONTINUITY still in it adds one more. A
// segment's own number is that sum, and it is what decides which timeline a
// player places the segment on. Two rungs of a ladder that disagree about it put
// the same media on two different timelines, and a switch between them stalls.
func TestParseHLS_SegmentsCarryTheirDiscontinuitySequence(t *testing.T) {
	pl, err := ParseHLS([]byte(`#EXTM3U
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:100
#EXT-X-DISCONTINUITY-SEQUENCE:7
#EXTINF:4.0,
a.ts
#EXT-X-DISCONTINUITY
#EXTINF:4.0,
b.ts
#EXTINF:4.0,
c.ts
#EXT-X-DISCONTINUITY
#EXTINF:4.0,
d.ts
`), "https://cdn.example/media.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if pl.DiscontinuitySequence != 7 {
		t.Errorf("playlist discontinuity sequence = %d, want 7", pl.DiscontinuitySequence)
	}
	want := []int{7, 8, 8, 9}
	if len(pl.Segments) != len(want) {
		t.Fatalf("parsed %d segments, want %d", len(pl.Segments), len(want))
	}
	for i, w := range want {
		if pl.Segments[i].DiscontinuitySequence != w {
			t.Errorf("segment %d (%s): discontinuity sequence %d, want %d",
				i, pl.Segments[i].URI, pl.Segments[i].DiscontinuitySequence, w)
		}
	}
}

// Absent the tag the count starts at zero, which is what a VOD playlist and a
// live one that has never rolled a discontinuity out both mean.
func TestParseHLS_NoDiscontinuitySequenceTagStartsAtZero(t *testing.T) {
	pl, err := ParseHLS([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\na.ts\n#EXT-X-DISCONTINUITY\n#EXTINF:4.0,\nb.ts\n"),
		"https://cdn.example/media.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if pl.DiscontinuitySequence != 0 {
		t.Errorf("playlist discontinuity sequence = %d, want 0", pl.DiscontinuitySequence)
	}
	if got := []int{pl.Segments[0].DiscontinuitySequence, pl.Segments[1].DiscontinuitySequence}; got[0] != 0 || got[1] != 1 {
		t.Errorf("segment discontinuity sequences = %v, want [0 1]", got)
	}
}
