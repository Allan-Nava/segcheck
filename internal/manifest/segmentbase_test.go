package manifest

import (
	"strings"
	"testing"
)

// SC-19, the manifest half: a SegmentBase representation is one file whose
// subsegments only the `sidx` box describes.
//
// ParseDASH does no I/O — it is handed bytes and returns a model — so it cannot
// expand these into segments itself; the index has to be fetched first. What it
// can do is stop calling them unsupported and say instead exactly which bytes to
// fetch, which is what turns "every check skipped for this rendition" into "one
// range request away from being checkable".

const mpdSegmentBase = `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT30S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <Representation id="v0" bandwidth="2400000" width="1280" height="720" codecs="avc1.4d401f">
        <BaseURL>video/720p.mp4</BaseURL>
        <SegmentBase indexRange="852-1291" timescale="90000">
          <Initialization range="0-851"/>
        </SegmentBase>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

func TestParseDASH_SegmentBaseIsAddressableNotUnsupported(t *testing.T) {
	pl, err := ParseDASH([]byte(mpdSegmentBase), "https://cdn.example.com/dash/manifest.mpd", epoch)
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if len(pl.Renditions) != 1 {
		t.Fatalf("got %d renditions, want 1", len(pl.Renditions))
	}
	r := pl.Renditions[0]

	if r.Unsupported != "" {
		t.Errorf("a SegmentBase rendition is still reported unsupported: %q", r.Unsupported)
	}
	if r.IndexRange == nil {
		t.Fatal("no index range: without it the subsegments cannot be located")
	}
	// indexRange="852-1291" is inclusive at both ends, as HTTP ranges are.
	if r.IndexRange.Offset != 852 || r.IndexRange.Length != 440 {
		t.Errorf("index range = offset %d length %d, want 852 and 440",
			r.IndexRange.Offset, r.IndexRange.Length)
	}
	// The single file is the thing to range-request, and it resolves against the
	// MPD's base like any other URL.
	if r.URI != "https://cdn.example.com/dash/video/720p.mp4" {
		t.Errorf("URI = %q, want the resolved single file", r.URI)
	}
	if r.InitURI != r.URI {
		t.Errorf("InitURI = %q, want the same file: the init is a range of it", r.InitURI)
	}
	if r.InitRange == nil || r.InitRange.Offset != 0 || r.InitRange.Length != 852 {
		t.Errorf("init range = %+v, want offset 0 length 852", r.InitRange)
	}
	// No segments yet: they need the index, which needs a fetch.
	if len(r.Segments) != 0 {
		t.Errorf("got %d segments, want none until the index is read", len(r.Segments))
	}
}

// Without an indexRange there is nothing to fetch and nothing to locate, so the
// honest answer is still that the rendition cannot be sampled.
func TestParseDASH_SegmentBaseWithoutAnIndexRange(t *testing.T) {
	mpd := strings.Replace(mpdSegmentBase, ` indexRange="852-1291"`, "", 1)
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example.com/dash/manifest.mpd", epoch)
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	r := pl.Renditions[0]
	if r.Unsupported == "" {
		t.Fatal("a SegmentBase with no indexRange was accepted as addressable")
	}
	if !strings.Contains(r.Unsupported, "indexRange") {
		t.Errorf("Unsupported = %q, want it to name what is missing", r.Unsupported)
	}
}

func TestParseByteRangeAttr(t *testing.T) {
	tests := []struct {
		in     string
		offset int64
		length int64
		ok     bool
	}{
		// Inclusive at both ends: 0-851 is 852 bytes.
		{"0-851", 0, 852, true},
		{"852-1291", 852, 440, true},
		{" 100-199 ", 100, 100, true},
		{"", 0, 0, false},
		{"852", 0, 0, false},
		{"852-", 0, 0, false},
		{"-1291", 0, 0, false},
		{"abc-def", 0, 0, false},
		// An end before the start describes no bytes at all.
		{"900-100", 0, 0, false},
	}
	for _, tc := range tests {
		got, ok := parseByteRangeAttr(tc.in)
		if ok != tc.ok {
			t.Errorf("parseByteRangeAttr(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if ok && (got.Offset != tc.offset || got.Length != tc.length) {
			t.Errorf("parseByteRangeAttr(%q) = offset %d length %d, want %d and %d",
				tc.in, got.Offset, got.Length, tc.offset, tc.length)
		}
	}
}
